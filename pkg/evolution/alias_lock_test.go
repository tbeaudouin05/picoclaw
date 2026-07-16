package evolution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// symlinkWorkspaceAlias creates a real workspace directory and a sibling symlink
// pointing at it, returning (real, alias). It skips the test if the platform
// cannot create symlinks (e.g. an unprivileged Windows runner).
func symlinkWorkspaceAlias(t *testing.T) (realWs, aliasWs string) {
	t.Helper()
	base := t.TempDir()
	realWs = filepath.Join(base, "real")
	if err := os.MkdirAll(realWs, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasWs = filepath.Join(base, "alias")
	if err := os.Symlink(realWs, aliasWs); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	return realWs, aliasWs
}

// canonicalTargetIdentity must collapse two workspace aliases that resolve to the
// same physical SKILL.md onto one key — both when the target is absent (the
// create case, where the leaf must not itself be resolved) and when it exists.
// A plain non-symlink path must be unaffected beyond ordinary cleaning.
func TestCanonicalTargetIdentityCollapsesWorkspaceAliases(t *testing.T) {
	realWs, aliasWs := symlinkWorkspaceAlias(t)
	realPath := filepath.Join(realWs, "skills", "weather", "SKILL.md")
	aliasPath := filepath.Join(aliasWs, "skills", "weather", "SKILL.md")

	// Absent target (create case): identities must already agree via the resolved
	// ancestor, without EvalSymlinks on the missing SKILL.md.
	if got, want := canonicalTargetIdentity(aliasPath), canonicalTargetIdentity(realPath); got != want {
		t.Fatalf("absent-target alias identity = %q, want %q", got, want)
	}

	// Present target: still one identity.
	writeExistingSkill(t, realWs, "weather", "---\nname: weather\ndescription: weather\n---\n# Weather\n")
	if got, want := canonicalTargetIdentity(aliasPath), canonicalTargetIdentity(realPath); got != want {
		t.Fatalf("present-target alias identity = %q, want %q", got, want)
	}

	// Non-symlink behavior unchanged: identity is the (resolved) textual path.
	if got := canonicalTargetIdentity(realPath); got != filepath.Clean(realPath) {
		t.Fatalf("non-symlink identity = %q, want %q", got, filepath.Clean(realPath))
	}
}

// A replacement observed and applied through one workspace alias must serialize
// coherently with a guarded replacement through another alias: the second apply,
// guarding on the body the first wrote, sees the same physical file and lands.
// This exercises the shared canonical lock across aliases on the normal path.
func TestApplyDraftAcrossAliasesSharesTargetState(t *testing.T) {
	realWs, aliasWs := symlinkWorkspaceAlias(t)
	name := "weather"
	body1 := "---\nname: weather\ndescription: weather\n---\n# Weather\n\n## Procedure\nFirst.\n"
	writeExistingSkill(t, realWs, name, body1)

	applier := NewApplier(NewPaths(filepath.Join(realWs, "state"), ""), nil)
	body2 := "---\nname: weather\ndescription: weather\n---\n# Weather\n\n## Procedure\nSecond.\n"
	// Guard through the alias on the body written to the real workspace: the alias
	// resolves to the same file, so the guard must be satisfied and the write land.
	if _, err := applier.applyDraftWithRollback(
		context.Background(), aliasWs,
		SkillDraft{TargetSkillName: name, ChangeKind: ChangeKindReplace, BodyOrPatch: body2},
		observedTargetState{guard: true, existed: true, body: body1},
	); err != nil {
		t.Fatalf("guarded replacement through alias: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(realWs, "skills", name, "SKILL.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(got), "## Procedure\nSecond.") {
		t.Fatalf("alias apply did not write the physical target:\n%s", string(got))
	}
}

// The critical concurrency correction: a stale rollback taken through one
// workspace alias must fail closed after a NEWER same-byte apply lands through a
// different alias, and the newer outcome must be retained. Before the fix the two
// aliases used separate ownership counters, so the stale rollback would still see
// its own version as current and clobber the newer apply back to the original.
func TestAliasRollbackFailsClosedAfterNewerSameBodyApply(t *testing.T) {
	realWs, aliasWs := symlinkWorkspaceAlias(t)
	name := "weather"
	original := "---\nname: weather\ndescription: weather\n---\n# Weather\n\n## Procedure\nOriginal.\n"
	writeExistingSkill(t, realWs, name, original)
	realPath := filepath.Join(realWs, "skills", name, "SKILL.md")

	applier := NewApplier(NewPaths(filepath.Join(realWs, "state"), ""), nil)
	draft := SkillDraft{
		TargetSkillName: name,
		DraftType:       DraftTypeWorkflow,
		ChangeKind:      ChangeKindReplace,
		HumanSummary:    "refine",
		BodyOrPatch:     "---\nname: weather\ndescription: weather\n---\n# Weather\n\n## Procedure\nRefined.\n",
	}

	// Apply through the real workspace and hold its rollback closure.
	rollback, err := applier.applyDraftWithRollback(
		context.Background(), realWs, draft,
		observedTargetState{guard: true, existed: true, body: original},
	)
	if err != nil {
		t.Fatalf("first apply through real workspace: %v", err)
	}
	afterFirst, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// A newer cooperating apply lands byte-identical content through the ALIAS.
	// It must take ownership on the shared canonical identity.
	if _, err := applier.applyDraftWithRollback(
		context.Background(), aliasWs, draft,
		observedTargetState{guard: true, existed: true, body: string(afterFirst)},
	); err != nil {
		t.Fatalf("newer apply through alias: %v", err)
	}

	// The stale rollback (taken through the real workspace) must fail closed
	// because the alias apply now owns the target, even though the on-disk bytes
	// still equal what the first apply wrote.
	if err := rollback(); err == nil || !strings.Contains(err.Error(), "a newer evolution apply owns the target") {
		t.Fatalf("stale alias rollback err = %v, want a fail-closed ownership error", err)
	}
	got, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(afterFirst) {
		t.Fatalf("stale alias rollback clobbered the newer same-body apply:\n%s", string(got))
	}
}

// The apply-time ownership-version guard must share the canonical identity across
// aliases too: a reviewed replacement observed through one workspace alias must
// fail closed when a NEWER evolution-owned apply lands byte-identical content
// through a DIFFERENT alias before the first apply runs. The captured version and
// the newer apply's stamp resolve to the same canonical counter, so the stale
// apply is superseded and must not overwrite the newer result.
func TestAliasReviewedReplacementFailsClosedWhenNewerAliasApplyWritesSameBody(t *testing.T) {
	realWs, aliasWs := symlinkWorkspaceAlias(t)
	name := "weather"
	canonical := renderDeployableSkillBody(
		"---\nname: weather\ndescription: weather\n---\n# Weather\n\n## Procedure\nShared.\n",
	)
	writeExistingSkill(t, realWs, name, canonical)
	realPath := filepath.Join(realWs, "skills", name, "SKILL.md")

	applier := NewApplier(NewPaths(filepath.Join(realWs, "state"), ""), nil)

	// The earlier review observes through the real workspace: current body plus the
	// canonical ownership version (0 — no evolution-owned write yet).
	staleObserved := observedTargetState{
		guard:        true,
		existed:      true,
		body:         canonical,
		versionGuard: true,
		version:      currentApplyOwnership(canonicalTargetIdentity(realPath)),
	}

	// A newer evolution-owned apply lands byte-identical content through the ALIAS
	// and takes ownership on the shared canonical identity (version -> 1).
	if _, err := applier.applyDraftWithRollback(
		context.Background(), aliasWs,
		SkillDraft{TargetSkillName: name, ChangeKind: ChangeKindReplace, BodyOrPatch: canonical},
		observedTargetState{guard: true, existed: true, body: canonical, versionGuard: true, version: 0},
	); err != nil {
		t.Fatalf("newer byte-identical apply through alias: %v", err)
	}

	// The stale reviewed apply through the real workspace must fail closed, even
	// though the on-disk bytes still equal what it observed.
	refined := "---\nname: weather\ndescription: weather\n---\n# Weather\n\n## Procedure\nStale refinement.\n"
	if _, err := applier.applyDraftWithRollback(
		context.Background(), realWs,
		SkillDraft{TargetSkillName: name, ChangeKind: ChangeKindReplace, BodyOrPatch: refined},
		staleObserved,
	); err == nil || !strings.Contains(err.Error(), "written by a newer evolution apply after review") {
		t.Fatalf("stale alias apply err = %v, want a fail-closed ownership-version error", err)
	}
	got, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != canonical {
		t.Fatalf("stale alias apply clobbered the newer byte-identical result:\n%s", string(got))
	}
}
