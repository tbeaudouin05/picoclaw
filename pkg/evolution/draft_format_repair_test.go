package evolution_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
)

// feedbackDraftGenerator implements DraftGenerator plus the optional
// FeedbackAwareDraftGenerator so the cold path can attempt one format repair. It
// records the repair inputs so a test can assert the generator saw the exact
// deterministic validation error and was called at most once.
type feedbackDraftGenerator struct {
	first     evolution.SkillDraft
	repaired  evolution.SkillDraft
	repairErr error

	repairCalls       int
	lastPrior         evolution.SkillDraft
	lastValidationErr string
}

func (g *feedbackDraftGenerator) GenerateDraft(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
) (evolution.SkillDraft, error) {
	return g.first, nil
}

func (g *feedbackDraftGenerator) RegenerateDraftWithFeedback(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
	_ evolution.DraftEvidence,
	prior evolution.SkillDraft,
	validationError string,
) (evolution.SkillDraft, error) {
	g.repairCalls++
	g.lastPrior = prior
	g.lastValidationErr = validationError
	if g.repairErr != nil {
		return evolution.SkillDraft{}, g.repairErr
	}
	return g.repaired, nil
}

// feedbackReviewer implements DraftGenerator, ReplacementReviewer, AND
// FeedbackAwareDraftGenerator so a replacement scenario can prove a non-format
// (review-unavailable) failure never triggers the format repair.
type feedbackReviewer struct {
	first     evolution.SkillDraft
	reviewed  evolution.SkillDraft
	reviewErr error
	repaired  evolution.SkillDraft

	reviewCalls int
	repairCalls int
}

func (g *feedbackReviewer) GenerateDraft(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
) (evolution.SkillDraft, error) {
	return g.first, nil
}

func (g *feedbackReviewer) ReviewReplacement(
	_ context.Context,
	_ evolution.ReplacementReviewRequest,
) (evolution.SkillDraft, error) {
	g.reviewCalls++
	return g.reviewed, g.reviewErr
}

func (g *feedbackReviewer) RegenerateDraftWithFeedback(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
	_ evolution.DraftEvidence,
	_ evolution.SkillDraft,
	_ string,
) (evolution.SkillDraft, error) {
	g.repairCalls++
	return g.repaired, nil
}

func seedWeatherReadyRule(t *testing.T, store *evolution.Store, root string) {
	t.Helper()
	rule := evolution.LearningRecord{
		ID:          "rule-1",
		Kind:        evolution.RecordKindRule,
		WorkspaceID: root,
		CreatedAt:   time.Unix(1700000000, 0).UTC(),
		Summary:     "weather native-name path",
		Status:      evolution.RecordStatus("ready"),
		EventCount:  4,
	}
	if err := store.AppendLearningRecords([]evolution.LearningRecord{rule}); err != nil {
		t.Fatalf("AppendLearningRecords: %v", err)
	}
}

func weatherCreateDraft(root, body string) evolution.SkillDraft {
	return evolution.SkillDraft{
		ID:              "draft-weather",
		WorkspaceID:     root,
		SourceRecordID:  "rule-1",
		TargetSkillName: "weather",
		DraftType:       evolution.DraftTypeShortcut,
		ChangeKind:      evolution.ChangeKindCreate,
		HumanSummary:    "weather helper",
		BodyOrPatch:     body,
	}
}

const validWeatherBody = "---\nname: weather\ndescription: Use to look up weather by native name.\n---\n" +
	"# Weather\n\n## Start Here\nQuery the weather service using the native place name first.\n"

// mismatchedNameBody is a well-formed SKILL.md whose frontmatter name does not
// match the target skill. Since the YAML frontmatter requirement was removed,
// this — not a malformed/absent frontmatter block — is the deterministic,
// retryable body-format failure used to exercise the format-repair path.
const mismatchedNameBody = "---\nname: not-weather\ndescription: Use to look up weather by native name.\n---\n" +
	"# Weather\n\n## Start Here\nQuery the weather service using the native place name first.\n"

// secondMismatchedNameBody is a distinct still-failing candidate for asserting a
// single repair attempt does not loop.
const secondMismatchedNameBody = "---\nname: still-not-weather\ndescription: Use to look up weather by native name.\n---\n" +
	"# Weather\n\n## Start Here\nQuery the weather service using the native place name first.\n"

const nameMismatchValidationErr = "does not match target skill"

// TestGenerationPromptIncludesExactDeployableTemplate covers requirement 1: the
// generation request embeds the exact minimal SKILL.md template and the
// no-prose/no-fences rule.
func TestGenerationPromptIncludesExactDeployableTemplate(t *testing.T) {
	provider := &llmDraftTestProvider{
		defaultModel: "test-model",
		response: &providers.LLMResponse{
			Content: `{"target_skill_name":"weather","draft_type":"shortcut","change_kind":"create","human_summary":"x","body_or_patch":"` +
				strings.ReplaceAll(validWeatherBody, "\n", "\\n") + `"}`,
		},
	}
	generator := evolution.NewLLMDraftGenerator(provider, "", &recordingDraftGenerator{})
	if _, err := generator.GenerateDraft(context.Background(), testLearningRule(), testSkillMatches()); err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if len(provider.lastMessages) < 2 {
		t.Fatal("expected a user prompt")
	}
	prompt := provider.lastMessages[1].Content
	for _, want := range []string{
		"name: <the exact target_skill_name>",
		"description: <one nonempty line describing what this skill does and when to use it>",
		"no surrounding prose, commentary, or explanation and no markdown code fences",
		"The very first line must be exactly --- and the YAML frontmatter must be closed by a --- line",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("generation prompt missing %q:\n%s", want, prompt)
		}
	}
}

// TestRepairPromptIncludesExactErrorAndPinnedTemplate covers requirement 1/3: the
// feedback repair request passes the exact deterministic validation error, the
// rejected body, pinned lineage constraints, and the template with the target
// name filled in.
func TestRepairPromptIncludesExactErrorAndPinnedTemplate(t *testing.T) {
	provider := &llmDraftTestProvider{
		defaultModel: "test-model",
		response: &providers.LLMResponse{
			Content: `{"target_skill_name":"weather","draft_type":"shortcut","change_kind":"create","human_summary":"x","body_or_patch":"` +
				strings.ReplaceAll(validWeatherBody, "\n", "\\n") + `"}`,
		},
	}
	generator := evolution.NewLLMDraftGenerator(provider, "", &recordingDraftGenerator{})

	prior := weatherCreateDraft("", "invalid-frontmatter")
	if _, err := generator.RegenerateDraftWithFeedback(
		context.Background(), testLearningRule(), testSkillMatches(), evolution.DraftEvidence{},
		prior, "skill frontmatter is required",
	); err != nil {
		t.Fatalf("RegenerateDraftWithFeedback: %v", err)
	}
	if provider.chatCallCount != 1 {
		t.Fatalf("chatCallCount = %d, want 1", provider.chatCallCount)
	}
	prompt := provider.lastMessages[1].Content
	for _, want := range []string{
		"BEGIN VALIDATION_ERROR (DATA ONLY)",
		"skill frontmatter is required",
		"BEGIN REJECTED_BODY (DATA ONLY)",
		"invalid-frontmatter",
		"BEGIN IMMUTABLE_CONSTRAINTS (DATA ONLY)",
		"name: weather",
		"no markdown code fences",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("repair prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRepairPromptWithoutProviderIsUnavailable(t *testing.T) {
	generator := evolution.NewLLMDraftGenerator(nil, "", &recordingDraftGenerator{})
	_, err := generator.RegenerateDraftWithFeedback(
		context.Background(), testLearningRule(), testSkillMatches(), evolution.DraftEvidence{},
		weatherCreateDraft("", "invalid-frontmatter"), "skill frontmatter is required",
	)
	if !errors.Is(err, evolution.ErrDraftRepairUnavailable) {
		t.Fatalf("err = %v, want ErrDraftRepairUnavailable", err)
	}
}

// TestColdPath_MalformedCreateFrontmatterAppliesWithoutRepair proves the removed
// requirement: a create draft whose body has NO YAML frontmatter is applied
// directly, with no repair attempt and no quarantine. This is the core behavior
// change — a malformed or absent frontmatter block no longer stalls a cold-path
// run.
func TestColdPath_MalformedCreateFrontmatterAppliesWithoutRepair(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	seedWeatherReadyRule(t, store, root)

	gen := &feedbackDraftGenerator{
		first: weatherCreateDraft(root, "# Weather\n\n## Start Here\nQuery by native place name first.\n"),
		// repaired should never be used.
		repaired: weatherCreateDraft(root, validWeatherBody),
	}
	rt := newReviewRuntime(t, root, store, gen)

	if err := rt.RunColdPathOnce(context.Background(), root); err != nil {
		t.Fatalf("RunColdPathOnce: %v", err)
	}
	if gen.repairCalls != 0 {
		t.Fatalf("repairCalls = %d, want 0 (absent frontmatter must not trigger repair)", gen.repairCalls)
	}

	applied, err := os.ReadFile(filepath.Join(root, "skills", "weather", "SKILL.md"))
	if err != nil {
		t.Fatalf("expected the skill on disk: %v", err)
	}
	if !strings.Contains(string(applied), "native place name") {
		t.Fatalf("applied skill missing body:\n%s", string(applied))
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusAccepted {
		t.Fatalf("drafts = %+v, want exactly 1 accepted", drafts)
	}
}

// TestColdPath_MismatchedNameCreateRepairsOnceAndApplies covers the retained
// target-selection gate and the repair plumbing: a well-formed frontmatter whose
// name does not match the target is caught before any write, the feedback
// generator receives the exact deterministic error, repairs exactly once, and the
// corrected skill is applied.
func TestColdPath_MismatchedNameCreateRepairsOnceAndApplies(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	seedWeatherReadyRule(t, store, root)

	gen := &feedbackDraftGenerator{
		first:    weatherCreateDraft(root, mismatchedNameBody),
		repaired: weatherCreateDraft(root, validWeatherBody),
	}
	rt := newReviewRuntime(t, root, store, gen)

	if err := rt.RunColdPathOnce(context.Background(), root); err != nil {
		t.Fatalf("RunColdPathOnce: %v", err)
	}

	if gen.repairCalls != 1 {
		t.Fatalf("repairCalls = %d, want exactly 1", gen.repairCalls)
	}
	if !strings.Contains(gen.lastValidationErr, nameMismatchValidationErr) {
		t.Fatalf("repair validation error = %q, want it to mention the name mismatch", gen.lastValidationErr)
	}
	if gen.lastPrior.TargetSkillName != "weather" || gen.lastPrior.BodyOrPatch != mismatchedNameBody {
		t.Fatalf("repair prior draft mismatch: %+v", gen.lastPrior)
	}

	skillPath := filepath.Join(root, "skills", "weather", "SKILL.md")
	applied, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("expected the repaired skill on disk: %v", err)
	}
	if !strings.Contains(string(applied), "native place name") {
		t.Fatalf("applied skill missing repaired body:\n%s", string(applied))
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("len(drafts) = %d, want exactly 1 (no double-quarantine)", len(drafts))
	}
	if drafts[0].Status != evolution.DraftStatusAccepted {
		t.Fatalf("draft status = %q, want accepted", drafts[0].Status)
	}
}

// TestColdPath_SecondMalformedRepairQuarantines covers requirement 4: if the one
// repair is still malformed, the cold path stops after exactly one repair and
// quarantines rather than looping or writing.
func TestColdPath_SecondMalformedRepairQuarantines(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	seedWeatherReadyRule(t, store, root)

	gen := &feedbackDraftGenerator{
		first:    weatherCreateDraft(root, mismatchedNameBody),
		repaired: weatherCreateDraft(root, secondMismatchedNameBody),
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("err = %v, want ErrApplyDraftFailed", err)
	}
	if gen.repairCalls != 1 {
		t.Fatalf("repairCalls = %d, want exactly 1", gen.repairCalls)
	}
	if _, statErr := os.Stat(filepath.Join(root, "skills", "weather", "SKILL.md")); statErr == nil {
		t.Fatal("expected no skill written after a failed repair")
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("len(drafts) = %d, want exactly 1", len(drafts))
	}
	if drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("draft status = %q, want quarantined", drafts[0].Status)
	}
}

// TestColdPath_RepairUnavailableQuarantinesOriginal covers requirement 3: when the
// repair call yields no usable draft, the original candidate is quarantined
// (existing safe behavior) rather than an unrepaired result being applied.
func TestColdPath_RepairUnavailableQuarantinesOriginal(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	seedWeatherReadyRule(t, store, root)

	gen := &feedbackDraftGenerator{
		first:     weatherCreateDraft(root, mismatchedNameBody),
		repairErr: evolution.ErrDraftRepairUnavailable,
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("err = %v, want ErrApplyDraftFailed", err)
	}
	if gen.repairCalls != 1 {
		t.Fatalf("repairCalls = %d, want exactly 1", gen.repairCalls)
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want exactly 1 quarantined", drafts)
	}
}

// TestColdPath_NonFeedbackGeneratorDoesNotRetry covers requirement 4: a generator
// without the optional interface keeps the existing single-attempt behavior and
// quarantines a malformed create with no repair.
func TestColdPath_NonFeedbackGeneratorDoesNotRetry(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	seedWeatherReadyRule(t, store, root)

	// stubDraftGenerator does not implement FeedbackAwareDraftGenerator.
	rt := newReviewRuntime(t, root, store, stubDraftGenerator{
		draft: weatherCreateDraft(root, mismatchedNameBody),
	})

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("err = %v, want ErrApplyDraftFailed", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "skills", "weather", "SKILL.md")); statErr == nil {
		t.Fatal("expected no skill written for a non-feedback generator")
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want exactly 1 quarantined", drafts)
	}
}

// reviewOnceWithBadBodyGenerator implements DraftGenerator, ReplacementReviewer,
// and FeedbackAwareDraftGenerator. On the first ReviewReplacement call it returns
// a draft whose BodyOrPatch embeds a wrong skill name (so preflightAppliedBody
// fails with a retryable body-format error while every other gate passes), and on
// subsequent calls it returns a valid reviewed body. RegenerateDraftWithFeedback
// records the prior draft it received so the test can assert the repair path
// handed it the REVIEWED body (finalDraft), not the pre-review candidate.
type reviewOnceWithBadBodyGenerator struct {
	first        evolution.SkillDraft
	reviewedBad  evolution.SkillDraft
	reviewedGood evolution.SkillDraft
	repaired     evolution.SkillDraft

	reviewCalls  int
	repairCalls  int
	lastPrior    evolution.SkillDraft
	lastValidErr string
}

func (g *reviewOnceWithBadBodyGenerator) GenerateDraft(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
) (evolution.SkillDraft, error) {
	return g.first, nil
}

func (g *reviewOnceWithBadBodyGenerator) ReviewReplacement(
	_ context.Context,
	_ evolution.ReplacementReviewRequest,
) (evolution.SkillDraft, error) {
	g.reviewCalls++
	if g.reviewCalls == 1 {
		return g.reviewedBad, nil
	}
	return g.reviewedGood, nil
}

func (g *reviewOnceWithBadBodyGenerator) RegenerateDraftWithFeedback(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
	_ evolution.DraftEvidence,
	prior evolution.SkillDraft,
	validationError string,
) (evolution.SkillDraft, error) {
	g.repairCalls++
	g.lastPrior = prior
	g.lastValidErr = validationError
	return g.repaired, nil
}

// TestColdPath_ReplaceMalformedReviewedBodyPassesFinalDraftToRepair is the
// deterministic replace-path proof that the feedback repair generator receives
// the exact body the replacement reviewer produced (finalDraft), not the
// pre-review candidate. The reviewer returns a body with a wrong skill name
// embedded in the frontmatter: this passes reviewReplacementDraft's holistic
// frontmatter-key check (the key "name" is still present) and the
// enforceLineageConstraints check (the struct-level TargetSkillName is correct)
// but fails preflightAppliedBody with a retryable Stage="applied_body" error.
// The test asserts that lastPrior.BodyOrPatch equals the reviewed body, not the
// pre-review candidate body.
func TestColdPath_ReplaceMalformedReviewedBodyPassesFinalDraftToRepair(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	candidateBody := "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n## Start Here\nPre-review candidate procedure.\n"
	// reviewedBadBody passes validateHolisticReplacement (frontmatter keys "name"
	// and "description" are both present) but fails preflightAppliedBody because
	// the embedded name does not match the target skill name.
	reviewedBadBody := "---\nname: wrong-name\ndescription: restart helper\n---\n" +
		"# wrong-name\n\n## Start Here\nReviewer-modified but wrong name in body.\n"
	reviewedBad := selfRestartReplaceDraft(root, reviewedBadBody)

	gen := &reviewOnceWithBadBodyGenerator{
		first:        selfRestartReplaceDraft(root, candidateBody),
		reviewedBad:  reviewedBad,
		reviewedGood: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
		repaired:     selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	if err := rt.RunColdPathOnce(context.Background(), root); err != nil {
		t.Fatalf("RunColdPathOnce: %v", err)
	}

	if gen.reviewCalls != 2 {
		t.Fatalf("reviewCalls = %d, want 2 (first bad, second after repair good)", gen.reviewCalls)
	}
	if gen.repairCalls != 1 {
		t.Fatalf("repairCalls = %d, want exactly 1", gen.repairCalls)
	}
	// The repair must have received the REVIEWED body (wrong-name), not the
	// pre-review candidate body. This is the targeted regression: the buggy code
	// passed `draft` (pre-review candidate) instead of `finalDraft` (reviewed).
	if gen.lastPrior.BodyOrPatch != reviewedBadBody {
		t.Fatalf("repair received prior.BodyOrPatch = %q\nwant the reviewed body %q\n\n"+
			"If this asserts the candidate body (%q), the fix is missing.",
			gen.lastPrior.BodyOrPatch, reviewedBadBody, candidateBody)
	}
	// Sanity: confirm a skill was applied (the repair succeeded).
	applied, err := os.ReadFile(filepath.Join(root, "skills", selfRestartSkill, "SKILL.md"))
	if err != nil {
		t.Fatalf("expected skill on disk after successful repair: %v", err)
	}
	if !strings.Contains(string(applied), "fall back to the documented steps") {
		t.Fatalf("applied skill missing repaired content:\n%s", string(applied))
	}
}

// TestColdPath_RepairLineageDriftIsRejectedAndQuarantines covers the lineage-
// pinning contract: a feedback repair that returns a draft whose immutable
// identity (target skill name, change kind, or draft_type) differs from the
// original must be rejected, and the original candidate must be quarantined.
// A repair may only correct the body, never drift the identity fields.
func TestColdPath_RepairLineageDriftIsRejectedAndQuarantines(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	seedWeatherReadyRule(t, store, root)

	gen := &feedbackDraftGenerator{
		first: weatherCreateDraft(root, mismatchedNameBody),
		// Repair returns a draft with a drifted target name.
		repaired: evolution.SkillDraft{
			ID:              "draft-weather",
			WorkspaceID:     root,
			SourceRecordID:  "rule-1",
			TargetSkillName: "other-skill",
			DraftType:       evolution.DraftTypeShortcut,
			ChangeKind:      evolution.ChangeKindCreate,
			HumanSummary:    "drifted target after repair",
			BodyOrPatch:     validWeatherBody,
		},
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("err = %v, want ErrApplyDraftFailed", err)
	}
	if gen.repairCalls != 1 {
		t.Fatalf("repairCalls = %d, want exactly 1", gen.repairCalls)
	}
	// Neither the original target nor the drifted target must be written.
	for _, name := range []string{"weather", "other-skill"} {
		if _, statErr := os.Stat(filepath.Join(root, "skills", name, "SKILL.md")); statErr == nil {
			t.Fatalf("skill %q was written after a repair with lineage drift", name)
		}
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want exactly 1 quarantined", drafts)
	}
	// The quarantined draft retains the ORIGINAL target name, not the drifted one.
	if drafts[0].TargetSkillName != "weather" {
		t.Fatalf("quarantined draft target = %q, want original %q", drafts[0].TargetSkillName, "weather")
	}
}

// TestColdPath_ReviewUnavailableIsNotRepaired covers requirement 3/4: a non-format
// safety failure (mandatory replacement review unavailable) must fail closed
// without ever invoking the feedback repair.
func TestColdPath_ReviewUnavailableIsNotRepaired(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &feedbackReviewer{
		first:     selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
		reviewErr: errors.New("reviewer boom"),
		repaired:  selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("err = %v, want ErrApplyDraftFailed", err)
	}
	if gen.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want exactly 1", gen.reviewCalls)
	}
	if gen.repairCalls != 0 {
		t.Fatalf("repairCalls = %d, want 0 (review failures must never be repaired)", gen.repairCalls)
	}
}
