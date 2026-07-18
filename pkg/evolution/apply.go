package evolution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/skills"
)

// applyOwnershipVersions tracks, per target SKILL.md path, a monotonically
// increasing version stamped on each SUCCESSFUL evolution-owned apply. It gives
// a rollback closure a durable in-process identity for the write it owns, so it
// can detect that a NEWER evolution-owned apply successfully wrote the same
// target after it — even when that newer apply wrote byte-identical content, in
// which case the on-disk body still equals what this apply wrote and the
// writtenBody comparison alone cannot tell the two writes apart.
//
// Access to a given path's counter is serialized by the per-target file lock
// that both the apply (when stamping) and the rollback (when reading) hold, so
// the stamp and the later read are ordered with respect to each other. The
// atomic value additionally guards against torn reads across the different
// paths that share this sync.Map. Only successful writes stamp a version, and
// applies never consult ownership — it gates rollback only — so a failed or
// refused operation never blocks an ordinary future apply.
var applyOwnershipVersions sync.Map // path -> *atomic.Uint64

// stampApplyOwnership records a new successful apply of path and returns the
// version identifying this apply's ownership. Must be called under the
// per-target lock, immediately after the write lands.
func stampApplyOwnership(path string) uint64 {
	actual, _ := applyOwnershipVersions.LoadOrStore(path, new(atomic.Uint64))
	return actual.(*atomic.Uint64).Add(1)
}

// currentApplyOwnership returns the version of the most recent successful apply
// of path, or 0 if none has been recorded. Must be called under the per-target
// lock.
func currentApplyOwnership(path string) uint64 {
	actual, ok := applyOwnershipVersions.Load(path)
	if !ok {
		return 0
	}
	return actual.(*atomic.Uint64).Load()
}

// canonicalTargetIdentity derives one stable identity for the actual file behind
// a skill's textual path, collapsing workspace aliases — e.g. two workspace roots
// where one is a symlink to the other — that resolve to the same physical
// SKILL.md onto a single lock/ownership key. Without this, aliases produce
// distinct textual paths and therefore separate per-target locks and ownership
// counters, allowing a concurrent stale-check/write on the same file and letting
// a stale rollback through one alias clobber a newer same-byte apply made through
// another.
//
// Because a create's target does not exist yet, it must NOT simply EvalSymlinks
// the (possibly absent) SKILL.md. Instead it resolves symlinks on the nearest
// EXISTING ancestor and re-joins the validated, not-yet-existing suffix, so an
// absent target and an aliased workspace directory both canonicalize robustly.
//
// The result is used ONLY as an in-process map key for locking and ownership.
// Every filesystem operation keeps using the caller's user-facing skillPath, so
// workspace/path containment and every other security semantic are unchanged. On
// any resolution error it falls back to the cleaned textual path, preserving the
// prior behavior rather than failing the apply.
func canonicalTargetIdentity(skillPath string) string {
	if resolved, err := resolveAgainstExistingAncestor(skillPath); err == nil {
		return resolved
	}
	return filepath.Clean(skillPath)
}

// resolveAgainstExistingAncestor resolves symlinks on the nearest existing
// ancestor of path and re-appends the remaining (not-yet-existing) suffix. It
// handles an absent leaf — the common create case — without resolving the leaf
// itself, while still following an aliased/symlinked ancestor directory to its
// physical location.
func resolveAgainstExistingAncestor(path string) (string, error) {
	cleaned := filepath.Clean(path)
	for current := cleaned; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			suffix, relErr := filepath.Rel(current, cleaned)
			if relErr != nil {
				return "", relErr
			}
			if suffix == "." {
				return filepath.Clean(resolved), nil
			}
			return filepath.Clean(filepath.Join(resolved, suffix)), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}

// errUnsupportedFrontmatterField sentinels a frontmatter block that parses as
// valid YAML but carries a field outside the supported set (name, description).
// It is distinguished from a malformed (unparseable) frontmatter block: the
// supported-field restriction is a real, retained safety constraint, whereas a
// malformed or absent frontmatter block no longer blocks an apply.
var errUnsupportedFrontmatterField = errors.New("unsupported skill frontmatter field")

// DraftApplySafetyError marks a draft rejection that stems from the candidate's
// own structure or apply-time validation (frontmatter/heading shape,
// name/description, a dropped required frontmatter field, or a stale-replacement
// conflict). It preserves the original human-readable message (including the
// "unsafe incomplete replacement" prefix) so logs and existing substring
// expectations remain compatible. A genuine apply-safety failure keeps the
// existing quarantine-on-apply-failure behavior; it is not retried.
type DraftApplySafetyError struct {
	Stage  string
	reason error
}

func (e *DraftApplySafetyError) Error() string {
	if e == nil || e.reason == nil {
		return "draft apply safety rejection"
	}
	return e.reason.Error()
}

func (e *DraftApplySafetyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.reason
}

type Applier struct {
	paths Paths
	now   func() time.Time
}

func NewApplier(paths Paths, now func() time.Time) *Applier {
	if now == nil {
		now = time.Now
	}
	return &Applier{
		paths: paths,
		now:   now,
	}
}

// observedTargetState is the exact target state a replacement review observed:
// whether an expected-state guard applies at all, whether the target existed on
// disk at review time, and, if it existed, its exact body. It replaces the
// earlier empty-string sentinel, which conflated three distinct cases — an
// absent target, an existing-but-empty target, and no guard at all — into "".
// The zero value (guard == false) means no expected-state guard applies.
//
// versionGuard/version additionally capture the canonical target's ownership
// version (see applyOwnershipVersions) at review time, using the SAME canonical
// identity as apply locking. The body/existence fields alone cannot tell two
// evolution-owned writes apart when the later one wrote byte-identical content:
// the on-disk body still equals what the review observed, so a stale reviewed
// replacement would pass the body comparison and overwrite the newer
// evolution-owned result. When versionGuard is set, apply additionally rejects
// the replacement if the current ownership version differs from version, even
// when the bytes match. versionGuard is opt-in so a caller that supplies only an
// existence/body guard (and the unreviewed direct ApplyDraft path, which
// supplies the zero value) keeps its existing semantics; version 0 is a real,
// enforced value meaning "no evolution-owned write had happened at review time".
type observedTargetState struct {
	guard        bool   // an expected-state guard applies (a review observed the target)
	existed      bool   // the target existed on disk when the review observed it
	body         string // exact on-disk body when observed (meaningful only if existed)
	versionGuard bool   // an ownership-version guard applies (version was captured)
	version      uint64 // canonical ownership version observed at review time
}

// ApplyDraft writes a draft through the direct, UNREVIEWED path: it supplies no
// observed target state (the zero observedTargetState, guard == false), so it
// never runs the mandatory proactive replacement review. Because a complete
// replacement of an existing on-disk skill requires that reviewer — reachable
// only through the reviewed internal path, which passes an explicit
// observedTargetState (guard == true) — this path cannot replace a skill that
// already exists: applyDraftWithRollback refuses a guard-less ChangeKindReplace
// whose target is present. Create and the append/merge rejections are unchanged,
// and a replace whose target is absent still follows the create-like
// replace-nonexistent handling in renderAppliedBody.
func (a *Applier) ApplyDraft(ctx context.Context, workspace string, draft SkillDraft) error {
	rollback, err := a.applyDraftWithRollback(ctx, workspace, draft, observedTargetState{})
	if err != nil {
		return err
	}
	_ = rollback
	return nil
}

// applyDraftWithRollback writes the draft, returning a rollback closure. When
// expected.guard is set it carries the EXACT target state a prior step (the
// replacement review) observed: whether the target existed and, if so, its exact
// body. Before writing, the current on-disk state must still match it. This is
// the conflict guard for Gap 3: it refuses to overwrite an edit that landed after
// the review, so a refinement is never applied on top of a target that has since
// changed underneath it — including a target that was absent at review but has
// since been created, or one whose (possibly empty) body has changed.
//
// A per-target lock (keyed on the exact SKILL.md path) serializes the whole
// check → backup → write section here, and the returned rollback closure
// re-acquires the same lock, so restore is serialized too. This makes the guard
// sound against every OTHER evolution-owned apply of the same skill, which is the
// only writer that cooperates on the lock. It does NOT promise atomicity against
// an arbitrary external filesystem writer (e.g. a hand editor): POSIX rename
// cannot provide compare-and-swap for replacing an existing pathname, so a
// non-cooperating writer racing the final rename cannot be excluded. The lock is
// scoped to this deterministic apply only — it is never held across the LLM
// review, which runs earlier in reviewReplacementDraft.
func (a *Applier) applyDraftWithRollback(
	ctx context.Context,
	workspace string,
	draft SkillDraft,
	expected observedTargetState,
) (func() error, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if validateErr := skills.ValidateSkillName(draft.TargetSkillName); validateErr != nil {
		return nil, validateErr
	}
	skillPath := filepath.Join(workspace, "skills", draft.TargetSkillName, "SKILL.md")
	// Lock and stamp ownership on one canonical identity for the actual target so
	// distinct workspace aliases (e.g. a symlinked workspace root) that resolve to
	// the same physical SKILL.md share a single lock and ownership counter.
	// Filesystem operations below keep using the user-facing skillPath.
	lockKey := canonicalTargetIdentity(skillPath)

	// Serialize the check-then-write section for this specific skill so an
	// intervening edit cannot slip between the stale comparison and the rename.
	unlock, lockErr := lockStoreFileContext(ctx, lockKey)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()

	if draft.ChangeKind == ChangeKindAppend || draft.ChangeKind == ChangeKindMerge {
		if _, statErr := os.Stat(skillPath); statErr == nil {
			return nil, fmt.Errorf(
				"cannot %s existing skill %q: evolution updates require a complete replacement",
				draft.ChangeKind,
				draft.TargetSkillName,
			)
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}

	existingBody, backupPath, hadOriginal, err := a.backupCurrentSkill(workspace, draft.TargetSkillName)
	if err != nil {
		return nil, err
	}
	// Gap 3 conflict guard: re-verify, under the per-target lock, the exact target
	// state the review observed. Fail closed before any write on any drift —
	// absent-at-review but now present (absent->created), present-at-review but now
	// gone, or a changed body (including an empty body that changed) — rather than
	// overwrite the intervening edit with a refinement based on a stale target.
	if expected.guard {
		var drifted bool
		if expected.existed {
			drifted = !hadOriginal || existingBody != expected.body
		} else {
			drifted = hadOriginal
		}
		if drifted {
			return nil, &DraftApplySafetyError{
				Stage: "stale_replacement",
				reason: fmt.Errorf(
					"target skill %q changed on disk after review; refusing to overwrite the intervening edit",
					draft.TargetSkillName,
				),
			}
		}
		// Ownership-version guard: the body/existence comparison above cannot
		// detect a later evolution-owned apply that wrote byte-identical content
		// after the review — the on-disk body still equals what the review
		// observed. Reject when the canonical ownership version has advanced past
		// the version captured at review time, so a stale reviewed replacement
		// cannot overwrite that newer evolution-owned result. The version is read
		// under this per-target lock (keyed on the same canonical identity as the
		// stamp), so the comparison is sound against every other cooperating apply
		// of the same physical target. Version 0 is enforced normally: it means no
		// evolution-owned write had happened when the review observed the target.
		if expected.versionGuard && currentApplyOwnership(lockKey) != expected.version {
			return nil, &DraftApplySafetyError{
				Stage: "stale_replacement",
				reason: fmt.Errorf(
					"target skill %q was written by a newer evolution apply after review (byte-identical content); refusing to overwrite the newer result",
					draft.TargetSkillName,
				),
			}
		}
	}
	if hadOriginal && (draft.ChangeKind == ChangeKindAppend || draft.ChangeKind == ChangeKindMerge) {
		return nil, fmt.Errorf(
			"cannot %s existing skill %q: evolution updates require a complete replacement",
			draft.ChangeKind,
			draft.TargetSkillName,
		)
	}
	// An unreviewed direct apply (the exported ApplyDraft path) carries no
	// observed target state (expected.guard == false). It must never replace an
	// existing skill: a complete replacement of an on-disk skill requires the
	// mandatory proactive reviewer, reachable only through the reviewed internal
	// path, which supplies an explicit observedTargetState (guard == true). The
	// check reads hadOriginal captured under the per-target lock, so it cannot
	// race a file created after an out-of-lock stat. An absent target falls
	// through to renderAppliedBody's create-like replace-nonexistent handling.
	if !expected.guard && draft.ChangeKind == ChangeKindReplace && hadOriginal {
		return nil, &DraftApplySafetyError{
			Stage: "unreviewed_replacement",
			reason: fmt.Errorf(
				"cannot replace existing skill %q without the mandatory replacement review",
				draft.TargetSkillName,
			),
		}
	}
	renderedBody, err := renderAppliedBody(draft, existingBody, hadOriginal)
	if err != nil {
		return nil, err
	}

	if err := validateAppliedSkillBody(
		renderedBody,
		draft.TargetSkillName,
		allowsExistingFrontmatterFields(draft.ChangeKind, hadOriginal),
	); err != nil {
		return nil, &DraftApplySafetyError{Stage: "applied_body", reason: err}
	}
	if hadOriginal && draft.ChangeKind == ChangeKindReplace {
		if err := validateHolisticReplacement(existingBody, renderedBody); err != nil {
			return nil, &DraftApplySafetyError{
				Stage:  "holistic_replacement",
				reason: fmt.Errorf("unsafe incomplete replacement: %w", err),
			}
		}
	}

	skillDir := filepath.Join(workspace, "skills", draft.TargetSkillName)
	if mkdirErr := os.MkdirAll(skillDir, 0o755); mkdirErr != nil {
		return nil, mkdirErr
	}

	if err := fileutil.WriteFileAtomic(skillPath, []byte(renderedBody), 0o644); err != nil {
		return nil, err
	}
	// Stamp ownership under the still-held per-target lock, atomically with the
	// write landing, so this operation records the version any later apply must
	// exceed to take ownership away from it. Keyed on the canonical identity so
	// applies through different aliases contend on the same counter.
	ownedVersion := stampApplyOwnership(lockKey)

	return func() error {
		// Restore under the same per-target lock so a rollback is serialized
		// against any concurrent apply of this skill.
		rollbackUnlock, rollbackLockErr := lockStoreFileContext(context.Background(), lockKey)
		if rollbackLockErr != nil {
			return rollbackLockErr
		}
		defer rollbackUnlock()
		return a.rollbackSkill(lockKey, skillPath, backupPath, hadOriginal, renderedBody, ownedVersion)
	}, nil
}

// validateHolisticReplacement enforces the deterministic gate that survives a
// complete replacement of an existing skill: every well-formed frontmatter field
// present in the old document must still be present in the candidate. It
// deliberately applies NO natural-language safety-wording heuristic and NO
// structural-diff heuristic (no literal root heading, minimum body size, named
// section, or safety-constraint-line comparison). Preserving still-relevant safety
// boundaries and invariants across a replacement is the job of the mandatory
// proactive reviewer, whose refined output is trusted after the deterministic
// schema/type, name/path, and secret-scan gates pass; the secret scan runs
// separately in ReviewDraft.
//
// YAML frontmatter is no longer required: a malformed (unparseable) or absent
// frontmatter block — on either the existing document or the candidate — no
// longer blocks the replacement. The skills loader falls back to the skill
// directory name and the Markdown body for a missing/unparseable block, so a
// malformed frontmatter must not stall a cold-path replacement.
func validateHolisticReplacement(existing, candidate string) error {
	existingFM, _ := splitSkillFrontmatter(existing)
	candidateFM, _ := splitSkillFrontmatter(candidate)
	existingFields, err := parseSkillFrontmatterFields(existingFM, true)
	if err != nil {
		// A malformed existing frontmatter cannot define a preservation contract.
		return nil
	}
	candidateFields, err := parseSkillFrontmatterFields(candidateFM, true)
	if err != nil {
		// A malformed candidate frontmatter is tolerated rather than blocking.
		return nil
	}
	for key := range existingFields {
		if _, ok := candidateFields[key]; !ok {
			return fmt.Errorf("required frontmatter field %q was removed", key)
		}
	}
	return nil
}

// preflightAppliedBody reproduces, WITHOUT writing anything, the exact
// deterministic body validation the final apply performs on the fully rendered
// deployable SKILL.md. It renders the body for the draft's change kind — using
// the exact old body a replacement review observed (expected.body) so create and
// replace are handled with the same allow-existing-frontmatter rule apply uses —
// and runs validateAppliedSkillBody, plus the required-frontmatter preservation
// gate for an existing-skill replacement. Failures are returned with the SAME
// typed classification apply produces (a DraftApplySafetyError whose Stage is
// "applied_body" for a formatting/body failure, "holistic_replacement" for a
// dropped required frontmatter field), so a caller can distinguish a
// self-correctable formatting failure from a safety failure that must never be
// retried.
//
// It deliberately performs NO stale/ownership/create-exists disk-state check:
// those are the locked apply's job and would race the file here. It never
// mutates state and issues no I/O beyond what expected already captured.
func preflightAppliedBody(draft SkillDraft, expected observedTargetState) error {
	hadOriginal := expected.guard && expected.existed
	existingBody := ""
	if hadOriginal {
		existingBody = expected.body
	}
	renderedBody, err := renderAppliedBody(draft, existingBody, hadOriginal)
	if err != nil {
		// A lifecycle/render error (e.g. replace of an absent target) is not a
		// formatting failure; return it plain, exactly as apply does, so it is
		// never treated as a self-correctable body defect.
		return err
	}
	if err := validateAppliedSkillBody(
		renderedBody,
		draft.TargetSkillName,
		allowsExistingFrontmatterFields(draft.ChangeKind, hadOriginal),
	); err != nil {
		return &DraftApplySafetyError{Stage: "applied_body", reason: err}
	}
	if hadOriginal && draft.ChangeKind == ChangeKindReplace {
		if err := validateHolisticReplacement(existingBody, renderedBody); err != nil {
			return &DraftApplySafetyError{
				Stage:  "holistic_replacement",
				reason: fmt.Errorf("unsafe incomplete replacement: %w", err),
			}
		}
	}
	return nil
}

// isRetryableBodyFormatError reports whether err is a formatting/body-validation
// rejection (DraftApplySafetyError stage "applied_body") that a single bounded
// feedback repair is allowed to attempt. Every other cause — a holistic
// frontmatter-preservation failure, a stale/ownership conflict, a
// review-unavailable failure, a capacity failure, or any lifecycle/render error —
// returns false and is never retried.
func isRetryableBodyFormatError(err error) bool {
	if err == nil {
		return false
	}
	var safety *DraftApplySafetyError
	return errors.As(err, &safety) && safety != nil && safety.Stage == "applied_body"
}

func (a *Applier) backupCurrentSkill(
	workspace, skillName string,
) (currentBody, backupPath string, hadOriginal bool, err error) {
	if validateErr := skills.ValidateSkillName(skillName); validateErr != nil {
		return "", "", false, validateErr
	}

	skillPath := filepath.Join(workspace, "skills", skillName, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if os.IsNotExist(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}

	backupDir := filepath.Join(
		a.paths.BackupsDir,
		workspaceScopeDir(workspace),
		skillName,
		a.now().Format("20060102-150405.000000000"),
	)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", "", false, err
	}

	backupPath = filepath.Join(backupDir, "SKILL.md")
	if err := fileutil.WriteFileAtomic(backupPath, data, 0o644); err != nil {
		return "", "", false, err
	}
	return string(data), backupPath, true, nil
}

// rollbackSkill undoes this operation's write, but only if this operation still
// owns the target and the on-disk body is still byte-for-byte the body this
// apply wrote (writtenBody). Reading and the restore/delete happen under the
// caller-held per-target lock, so both checks are a sound compare-and-swap
// against every OTHER evolution-owned apply of the same skill.
//
// The ownedVersion guard is the primary check: if a later cooperating apply
// stamped a higher version after we wrote and before rollback runs, it now owns
// the target and we fail closed — even if that newer apply wrote byte-identical
// content, so the writtenBody comparison below would otherwise pass and let us
// clobber it. The writtenBody comparison still catches a non-cooperating
// external writer (e.g. a hand editor) that changed the file without stamping a
// version.
//
// ownershipKey is the canonical target identity used for the ownership check;
// skillPath is the caller's user-facing path used for the actual filesystem
// read/restore/delete. They differ only when the target was reached through a
// workspace alias, and both refer to the same physical file.
func (a *Applier) rollbackSkill(ownershipKey, skillPath, backupPath string, hadOriginal bool, writtenBody string, ownedVersion uint64) error {
	if currentApplyOwnership(ownershipKey) != ownedVersion {
		return fmt.Errorf(
			"refusing to roll back skill %q: a newer evolution apply owns the target after this apply wrote it",
			filepath.Base(filepath.Dir(skillPath)),
		)
	}
	current, err := os.ReadFile(skillPath)
	if os.IsNotExist(err) {
		// We wrote a body here; its absence means a later writer removed or
		// replaced the target. Do not resurrect it from a stale backup.
		return fmt.Errorf(
			"refusing to roll back skill %q: target was removed after this apply wrote it",
			filepath.Base(filepath.Dir(skillPath)),
		)
	}
	if err != nil {
		return err
	}
	if string(current) != writtenBody {
		return fmt.Errorf(
			"refusing to roll back skill %q: target changed on disk after this apply wrote it",
			filepath.Base(filepath.Dir(skillPath)),
		)
	}

	if hadOriginal {
		data, err := os.ReadFile(backupPath)
		if err != nil {
			return err
		}
		return fileutil.WriteFileAtomic(skillPath, data, 0o644)
	}
	if err := os.Remove(skillPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	skillDir := filepath.Dir(skillPath)
	if err := os.Remove(skillDir); err != nil && !os.IsNotExist(err) && !isDirNotEmptyError(err) {
		return err
	}
	return nil
}

func isDirNotEmptyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "directory not empty")
}

// validateAppliedSkillBody performs the deterministic body checks the final apply
// enforces on a fully rendered deployable SKILL.md. YAML frontmatter is NO LONGER
// required: an absent or malformed (unparseable) frontmatter block does not block
// an otherwise safe evolution draft, because the skills loader derives the name
// from the skill directory and the description from the Markdown body when the
// frontmatter is missing or cannot be parsed. This removes the failure mode where
// repeated cold-path runs stalled on a generated SKILL.md whose frontmatter was
// malformed.
//
// The retained deterministic checks are the ones that are not part of the
// frontmatter-shape requirement: a nonempty body (ordinary body validity), the
// target-selection contract (a well-formed frontmatter name, when present, must
// match the target skill), and the create-time supported-field restriction.
func validateAppliedSkillBody(body, targetSkillName string, allowExtraFrontmatterFields bool) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("skill body is required")
	}
	frontmatter, _ := splitSkillFrontmatter(body)
	if strings.TrimSpace(frontmatter) == "" {
		// Absent frontmatter (no delimiters, or an unterminated block) no longer
		// blocks apply.
		return nil
	}
	fields, err := parseSkillFrontmatterFields(frontmatter, allowExtraFrontmatterFields)
	if err != nil {
		// The supported-field restriction is a real constraint and is retained; a
		// malformed (unparseable) frontmatter block is tolerated.
		if errors.Is(err, errUnsupportedFrontmatterField) {
			return err
		}
		return nil
	}
	if name := strings.TrimSpace(fields["name"]); name != "" && name != targetSkillName {
		return fmt.Errorf("skill frontmatter name %q does not match target skill %q", name, targetSkillName)
	}
	return nil
}

func allowsExistingFrontmatterFields(kind ChangeKind, hadOriginal bool) bool {
	return hadOriginal && kind == ChangeKindReplace
}

func renderAppliedBody(draft SkillDraft, existingBody string, hadOriginal bool) (string, error) {
	switch draft.ChangeKind {
	case ChangeKindCreate:
		if hadOriginal {
			return "", fmt.Errorf("cannot create skill %q: skill already exists", draft.TargetSkillName)
		}
		return renderDeployableSkillBody(draft.BodyOrPatch), nil
	case ChangeKindReplace:
		if !hadOriginal {
			return "", fmt.Errorf("cannot replace skill %q: skill does not exist", draft.TargetSkillName)
		}
		return renderDeployableSkillBody(draft.BodyOrPatch), nil
	case ChangeKindAppend:
		patch, err := renderDeployablePatchBody(draft.BodyOrPatch, draft.TargetSkillName)
		if err != nil {
			return "", err
		}
		if !hadOriginal || strings.TrimSpace(existingBody) == "" {
			return renderDeployableSkillBody(draft.BodyOrPatch), nil
		}
		return strings.TrimRight(existingBody, "\n") + "\n\n" + strings.TrimLeft(patch, "\n"), nil
	case ChangeKindMerge:
		patch, err := renderDeployablePatchBody(draft.BodyOrPatch, draft.TargetSkillName)
		if err != nil {
			return "", err
		}
		if !hadOriginal || strings.TrimSpace(existingBody) == "" {
			return renderDeployableSkillBody(draft.BodyOrPatch), nil
		}
		mergedSection := strings.Join([]string{
			"",
			"## Merged Knowledge",
			strings.TrimSpace(patch),
			"",
		}, "\n")
		return strings.TrimRight(existingBody, "\n") + mergedSection, nil
	default:
		return "", fmt.Errorf("unsupported change_kind %q", draft.ChangeKind)
	}
}

func renderDeployablePatchBody(body, targetSkillName string) (string, error) {
	body = renderDeployableSkillBody(body)
	frontmatter, markdownBody := splitSkillFrontmatter(body)
	if frontmatter == "" {
		markdownBody = body
	} else {
		fields, err := parseSkillFrontmatterFields(frontmatter, true)
		if err != nil {
			// Malformed frontmatter no longer blocks: treat the whole body as
			// Markdown rather than rejecting the patch.
			markdownBody = body
		} else if name := strings.TrimSpace(fields["name"]); name != "" && name != targetSkillName {
			return "", fmt.Errorf(
				"skill patch frontmatter name %q does not match target skill %q",
				name,
				targetSkillName,
			)
		}
	}
	return strings.TrimSpace(stripLeadingH1(markdownBody)), nil
}

func splitSkillFrontmatter(body string) (frontmatter, markdownBody string) {
	normalized := strings.ReplaceAll(strings.TrimSpace(body), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", body
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", body
	}
	return strings.Join(lines[1:end], "\n"), strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
}

func parseSkillFrontmatterFields(frontmatter string, allowExtraFields bool) (map[string]string, error) {
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return nil, fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	for key := range raw {
		if key != "name" && key != "description" {
			if allowExtraFields {
				continue
			}
			return nil, fmt.Errorf("%w %q", errUnsupportedFrontmatterField, key)
		}
	}

	var typed struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &typed); err != nil {
		return nil, fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	return map[string]string{
		"name":        typed.Name,
		"description": typed.Description,
	}, nil
}

func stripLeadingH1(body string) string {
	lines := strings.Split(strings.TrimLeft(body, "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

func errorsJoin(errs ...error) error {
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if first == nil {
			first = err
			continue
		}
		first = fmt.Errorf("%w; %v", first, err)
	}
	return first
}
