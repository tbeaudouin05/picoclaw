package evolution_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/skills"
)

// scriptedReviewer is a DraftGenerator that also implements ReplacementReviewer,
// so it opts into the cold path's one proactive old-vs-candidate review pass. It
// records how many times ReviewReplacement was called and the request it saw.
type scriptedReviewer struct {
	first    evolution.SkillDraft
	firstErr error

	reviewed  evolution.SkillDraft
	reviewErr error

	// onReview, when set, runs at the start of ReviewReplacement. It lets a test
	// simulate an intervening on-disk edit that lands after the review captured
	// the old document but before apply.
	onReview func()

	reviewCalls int
	lastRequest evolution.ReplacementReviewRequest
}

func (g *scriptedReviewer) GenerateDraft(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
) (evolution.SkillDraft, error) {
	return g.first, g.firstErr
}

func (g *scriptedReviewer) ReviewReplacement(
	_ context.Context,
	req evolution.ReplacementReviewRequest,
) (evolution.SkillDraft, error) {
	g.reviewCalls++
	g.lastRequest = req
	if g.onReview != nil {
		g.onReview()
	}
	return g.reviewed, g.reviewErr
}

const selfRestartSkill = "picoclaw-self-restart"

func selfRestartOriginalBody() string {
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n" +
		"## Start Here\nRun the documented restart steps in order.\n\n" +
		"## Notes\nKeep the log of each restart attempt.\n"
}

func selfRestartBadHeadingBody() string {
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# Picoclaw Restart\n\n" +
		"## Start Here\nPrefer the fast restart path first.\n\n" +
		"## Notes\nKeep the log of each restart attempt.\n"
}

func selfRestartCorrectedBody() string {
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n" +
		"## Start Here\nPrefer the fast restart path first, then fall back to the documented steps.\n\n" +
		"## Notes\nKeep the log of each restart attempt.\n"
}

// The safety-bearing variants carry an explicit safety boundary in prose. Under
// the trust-the-reviewer model the deterministic gate no longer matches that
// wording; preserving the boundary in substance is the reviewer's judgment, and
// the reviewer may rephrase it freely.
func selfRestartSafetyOriginalBody() string {
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n" +
		"## Start Here\nRun the documented restart steps. Never delete the workspace state directory.\n"
}

func selfRestartSafetyCandidateBody() string {
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n" +
		"## Start Here\nRun the documented restart steps. Never delete the workspace state directory.\n\n" +
		"## Notes\nPrefer the fast path.\n"
}

// selfRestartRephrasedSafetyReviewBody keeps the safety boundary but rephrases it
// as a synonym instead of the literal line. The old brittle safety-wording
// matcher would have rejected this rephrase; the trust-the-reviewer model applies
// it.
func selfRestartRephrasedSafetyReviewBody() string {
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n" +
		"## Start Here\nRun the documented restart steps. Do not erase the workspace state folder under any circumstances.\n\n" +
		"## Notes\nPrefer the fast path.\n"
}

func writeSelfRestartSkill(t *testing.T, root, body string) string {
	t.Helper()
	skillDir := filepath.Join(root, "skills", selfRestartSkill)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return skillPath
}

func seedSelfRestartReadyRule(t *testing.T, store *evolution.Store, root string) {
	t.Helper()
	rule := evolution.LearningRecord{
		ID:          "rule-1",
		Kind:        evolution.RecordKindRule,
		WorkspaceID: root,
		CreatedAt:   time.Unix(1700000000, 0).UTC(),
		Summary:     "self restart fast path",
		Status:      evolution.RecordStatus("ready"),
		EventCount:  4,
	}
	if err := store.AppendLearningRecords([]evolution.LearningRecord{rule}); err != nil {
		t.Fatalf("AppendLearningRecords: %v", err)
	}
}

func selfRestartReplaceDraft(root, body string) evolution.SkillDraft {
	return evolution.SkillDraft{
		ID:              "draft-rule-dfe6883011ba",
		WorkspaceID:     root,
		SourceRecordID:  "rule-1",
		TargetSkillName: selfRestartSkill,
		DraftType:       evolution.DraftTypeWorkflow,
		ChangeKind:      evolution.ChangeKindReplace,
		HumanSummary:    "integrate learned self-restart fast path",
		BodyOrPatch:     body,
	}
}

func newReviewRuntime(t *testing.T, root string, store *evolution.Store, gen evolution.DraftGenerator) *evolution.Runtime {
	t.Helper()
	rt, err := evolution.NewRuntime(evolution.RuntimeOptions{
		Config: config.EvolutionConfig{Enabled: true, Mode: "apply", RunLedgerRetentionHours: 120},
		Now:    func() time.Time { return time.Unix(1700001000, 0).UTC() },
		Store:  store,
		Applier: evolution.NewApplier(evolution.NewPaths(root, ""), func() time.Time {
			return time.Unix(1700001000, 0).UTC()
		}),
		DraftGenerator: gen,
		Organizer:      evolution.NewOrganizer(evolution.OrganizerOptions{MinCaseCount: 3, MinSuccessRate: 0.7}),
		SkillsRecaller: evolution.NewSkillsRecaller(root),
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func TestRuntime_RunColdPathOnce_ReviewsReplacementOnceAndAppliesRefinedDocument(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedReviewer{
		first:    selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		reviewed: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	if runErr := rt.RunColdPathOnce(context.Background(), root); runErr != nil {
		t.Fatalf("RunColdPathOnce: %v", runErr)
	}

	if gen.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want exactly 1", gen.reviewCalls)
	}
	if gen.lastRequest.TargetSkillName != selfRestartSkill {
		t.Fatalf("review target = %q, want %q", gen.lastRequest.TargetSkillName, selfRestartSkill)
	}
	if gen.lastRequest.OldDocument != selfRestartOriginalBody() {
		t.Fatalf("review old document did not match the on-disk target:\n%s", gen.lastRequest.OldDocument)
	}
	if !strings.Contains(gen.lastRequest.CandidateDocument, "Prefer the fast restart path first") {
		t.Fatalf("review candidate document missing the proposed content:\n%s", gen.lastRequest.CandidateDocument)
	}

	applied, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(applied)
	if !strings.Contains(content, "# "+selfRestartSkill) {
		t.Fatalf("applied skill missing refined heading:\n%s", content)
	}
	if !strings.Contains(content, "fall back to the documented steps") {
		t.Fatalf("applied skill missing refined learned content:\n%s", content)
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("len(drafts) = %d, want 1", len(drafts))
	}
	if drafts[0].Status != evolution.DraftStatusAccepted {
		t.Fatalf("draft status = %q, want accepted", drafts[0].Status)
	}

	ledger, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("len(ledger) = %d, want 1", len(ledger))
	}
	if ledger[0].Status != "completed" {
		t.Fatalf("ledger status = %q, want completed", ledger[0].Status)
	}
	if ledger[0].WorkspaceID != root {
		t.Fatalf("ledger workspace = %q, want %q", ledger[0].WorkspaceID, root)
	}
}

// Under the trust-the-reviewer model a reviewer that REPHRASES still-relevant
// safety guidance (a synonym rather than the literal line) is authoritative: its
// output applies once the deterministic schema/frontmatter/secret gates pass. The
// old brittle safety-wording matcher would have rejected the rephrase and fallen
// back; there is now exactly one review, no fallback, and no retry loop.
func TestRuntime_RunColdPathOnce_ReviewRephrasingSafetyGuidanceIsApplied(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartSafetyOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedReviewer{
		first: selfRestartReplaceDraft(root, selfRestartSafetyCandidateBody()),
		// The refined draft rephrases the "Never delete ..." boundary as a synonym.
		reviewed: selfRestartReplaceDraft(root, selfRestartRephrasedSafetyReviewBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	if runErr := rt.RunColdPathOnce(context.Background(), root); runErr != nil {
		t.Fatalf("RunColdPathOnce: %v", runErr)
	}
	if gen.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want exactly 1 (no retry loop)", gen.reviewCalls)
	}

	// The reviewer's rephrased body applied: the synonym boundary is present and
	// the literal original line is gone.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "Do not erase the workspace state folder under any circumstances") {
		t.Fatalf("applied skill missing the rephrased safety guidance:\n%s", content)
	}
	if strings.Contains(content, "Never delete the workspace state directory") {
		t.Fatalf("applied skill kept the literal original line, so the reviewer output was not applied:\n%s", content)
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("len(drafts) = %d, want 1", len(drafts))
	}
	if drafts[0].Status != evolution.DraftStatusAccepted {
		t.Fatalf("draft status = %q, want accepted", drafts[0].Status)
	}

	ledger, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("len(ledger) = %d, want 1", len(ledger))
	}
	if ledger[0].Status != "completed" {
		t.Fatalf("ledger status = %q, want completed", ledger[0].Status)
	}
}

// A reviewer that RAN but returned an error (a provider failure surfaced through
// ReviewReplacement, distinct from cancellation) must fail the apply CLOSED: no
// unreviewed candidate is written, the draft is quarantined, and there is exactly
// one review with no retry.
func TestRuntime_RunColdPathOnce_ReviewerErrorFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedReviewer{
		first:     selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		reviewErr: evolution.ErrReplacementReviewUnavailable,
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if gen.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want exactly 1 (no retry)", gen.reviewCalls)
	}

	// No unreviewed replacement was written: the on-disk skill is untouched.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != selfRestartOriginalBody() {
		t.Fatalf("skill was modified despite failing closed:\n%s", string(got))
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want one quarantined draft", drafts)
	}
}

// selfRestartOversizedBody returns a valid, complete SKILL.md whose trimmed
// length exceeds the reviewer's per-document capacity (maxReplacementReviewBodyBytes
// = 32000). Feeding it to the reviewer would silently truncate it, so a review of
// the whole document is impossible.
func selfRestartOversizedBody() string {
	filler := strings.Repeat(
		"This is a long but valid line of restart guidance that pads the body.\n", 600)
	return "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n" +
		"## Start Here\nRun the documented restart steps in order.\n\n" +
		"## Notes\n" + filler
}

// A complete replacement whose EXACT OLD on-disk document is too large for the
// reviewer to see in full cannot be reviewed: the reviewer would only judge a
// truncated old document while applying its output as a full replacement. The
// run must fail CLOSED before any reviewer call and write nothing, quarantining
// the draft exactly like every other review-unavailable cause.
func TestRuntime_RunColdPathOnce_OversizedOldDocumentFailsClosedWithoutReview(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	oldBody := selfRestartOversizedBody()
	if len(oldBody) <= 32000 {
		t.Fatalf("test old body is %d bytes, want > 32000 to exceed reviewer capacity", len(oldBody))
	}
	skillPath := writeSelfRestartSkill(t, root, oldBody)
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedReviewer{
		first: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	// The reviewer (and thus its provider) was never called.
	if gen.reviewCalls != 0 {
		t.Fatalf("reviewCalls = %d, want 0 (oversized old document is not reviewable)", gen.reviewCalls)
	}
	// No replacement was written: the on-disk skill is untouched.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != oldBody {
		t.Fatalf("skill was modified despite failing closed")
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want one quarantined draft", drafts)
	}
}

// A complete replacement whose rendered CANDIDATE document is too large for the
// reviewer to see in full cannot be reviewed either: the reviewer would judge a
// truncated candidate while the full candidate is applied. The run must fail
// CLOSED before any reviewer call and write nothing, quarantining the draft.
func TestRuntime_RunColdPathOnce_OversizedCandidateFailsClosedWithoutReview(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	candidateBody := selfRestartOversizedBody()
	if len(candidateBody) <= 32000 {
		t.Fatalf("test candidate body is %d bytes, want > 32000 to exceed reviewer capacity", len(candidateBody))
	}
	gen := &scriptedReviewer{
		first: selfRestartReplaceDraft(root, candidateBody),
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	// The reviewer (and thus its provider) was never called.
	if gen.reviewCalls != 0 {
		t.Fatalf("reviewCalls = %d, want 0 (oversized candidate is not reviewable)", gen.reviewCalls)
	}
	// No replacement was written: the on-disk skill is untouched.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != selfRestartOriginalBody() {
		t.Fatalf("skill was modified despite failing closed:\n%s", string(got))
	}
	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want one quarantined draft", drafts)
	}
}

// TestRuntime_RunColdPathOnce_ReviewedBodyProfileFailureStillFailsClosed
// exercises the fail-closed profile-persistence path: the trusted reviewer output
// applies, but the applied-profile save itself fails (profiles path is a regular
// file). The run must still fail closed — the on-disk skill is rolled back to the
// original and the draft is quarantined — with exactly one review and no retry.
func TestRuntime_RunColdPathOnce_ReviewedBodyProfileFailureStillFailsClosed(t *testing.T) {
	root := t.TempDir()
	paths := evolution.NewPaths(root, "")
	store := evolution.NewStore(paths)
	skillPath := writeSelfRestartSkill(t, root, selfRestartSafetyOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	// Inject a profile persistence failure: making the profiles path a regular file
	// forces saveAppliedProfile to error (ENOTDIR) after the reviewer's refined body
	// applies, exercising the profile-save fail-closed branch of applyCandidateDraft
	// (rollback + quarantine).
	if err := os.MkdirAll(filepath.Dir(paths.ProfilesDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(paths.ProfilesDir, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(profiles): %v", err)
	}

	gen := &scriptedReviewer{
		first: selfRestartReplaceDraft(root, selfRestartSafetyCandidateBody()),
		// The reviewer's refined body is trusted and applies before the profile
		// save fails, driving the rollback-and-quarantine branch.
		reviewed: selfRestartReplaceDraft(root, selfRestartRephrasedSafetyReviewBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail on the profile save")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if gen.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want exactly 1 despite the profile persistence failure", gen.reviewCalls)
	}

	// Fail-closed: the on-disk skill is rolled back to the original and the draft
	// is quarantined.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != selfRestartSafetyOriginalBody() {
		t.Fatalf("skill not rolled back after the profile save failure:\n%s", string(got))
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("len(drafts) = %d, want 1", len(drafts))
	}
	if drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("draft status = %q, want quarantined", drafts[0].Status)
	}
}

func TestRuntime_RunColdPathOnce_NonRetryableProfileFailureNeverReviews(t *testing.T) {
	root := t.TempDir()
	paths := evolution.NewPaths(root, "")
	store := evolution.NewStore(paths)

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

	// Force the profile save (a non-safety, non-retryable failure) to fail by
	// making the profiles directory a regular file.
	if err := os.MkdirAll(filepath.Dir(paths.ProfilesDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(paths.ProfilesDir, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(profiles): %v", err)
	}

	gen := &scriptedReviewer{
		first: evolution.SkillDraft{
			ID:              "draft-weather",
			WorkspaceID:     root,
			SourceRecordID:  "rule-1",
			TargetSkillName: "weather",
			DraftType:       evolution.DraftTypeShortcut,
			ChangeKind:      evolution.ChangeKindCreate,
			HumanSummary:    "weather helper",
			BodyOrPatch:     "---\nname: weather\ndescription: weather helper\n---\n# Weather\n## Start Here\nUse native-name query first.\n",
		},
		reviewed: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail on profile save")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if gen.reviewCalls != 0 {
		t.Fatalf("reviewCalls = %d, want 0 for a create draft (no replacement review)", gen.reviewCalls)
	}

	if _, statErr := os.Stat(filepath.Join(root, "skills", "weather", "SKILL.md")); !os.IsNotExist(statErr) {
		t.Fatalf("expected applied skill to be rolled back, got err=%v", statErr)
	}
}

func TestRuntime_RunColdPathOnce_CanceledContextNeverReviewsAndRecordsCanceled(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedReviewer{
		first:    selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		reviewed: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newReviewRuntime(t, root, store, gen)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rt.RunColdPathOnce(ctx, root)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if gen.reviewCalls != 0 {
		t.Fatalf("reviewCalls = %d, want 0 on cancellation", gen.reviewCalls)
	}

	ledger, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("len(ledger) = %d, want 1", len(ledger))
	}
	if ledger[0].Status != "canceled" {
		t.Fatalf("ledger status = %q, want canceled", ledger[0].Status)
	}
}

func TestRuntime_RunColdPathOnce_FailedApplyIsRecordedInLedger(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))

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

	// A create draft with invalid frontmatter fails apply-safety; the default
	// (non-regenerating) generator keeps the existing single-attempt behavior.
	rt := newReviewRuntime(t, root, store, stubDraftGenerator{
		draft: evolution.SkillDraft{
			ID:              "draft-broken",
			WorkspaceID:     root,
			SourceRecordID:  "rule-1",
			TargetSkillName: "weather",
			DraftType:       evolution.DraftTypeShortcut,
			ChangeKind:      evolution.ChangeKindCreate,
			HumanSummary:    "broken weather helper",
			BodyOrPatch:     "invalid-frontmatter",
		},
	})

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil || !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}

	ledger, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(ledger) != 1 {
		t.Fatalf("len(ledger) = %d, want 1", len(ledger))
	}
	if ledger[0].Status != "failed" {
		t.Fatalf("ledger status = %q, want failed", ledger[0].Status)
	}
	if !strings.Contains(ledger[0].ErrorSummary, "apply draft failed") {
		t.Fatalf("ledger error summary = %q, want apply draft failed", ledger[0].ErrorSummary)
	}
}

func TestRuntime_RunColdPathOnce_LedgerPersistenceFailureIsSurfaced(t *testing.T) {
	root := t.TempDir()
	paths := evolution.NewPaths(root, "")
	store := evolution.NewStore(paths)

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

	// Make the run-ledger path an (unwritable) directory so the durable append
	// fails even though the cold-path operation itself succeeds.
	if err := os.MkdirAll(paths.RunLedger, 0o755); err != nil {
		t.Fatalf("MkdirAll(ledger dir): %v", err)
	}

	rt := newReviewRuntime(t, root, store, stubDraftGenerator{
		draft: evolution.SkillDraft{
			ID:              "draft-weather",
			WorkspaceID:     root,
			SourceRecordID:  "rule-1",
			TargetSkillName: "weather",
			DraftType:       evolution.DraftTypeShortcut,
			ChangeKind:      evolution.ChangeKindCreate,
			HumanSummary:    "weather helper",
			BodyOrPatch:     "---\nname: weather\ndescription: weather helper\n---\n# Weather\n## Start Here\nUse native-name query first.\n",
		},
	})

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected a durable ledger failure to be surfaced, got nil")
	}
	if !strings.Contains(err.Error(), "run ledger") {
		t.Fatalf("error = %v, want a surfaced run ledger failure", err)
	}

	// The operation itself did succeed (skill applied) — success must not be
	// reported when the terminal record could not be persisted.
	if _, statErr := os.Stat(filepath.Join(root, "skills", "weather", "SKILL.md")); statErr != nil {
		t.Fatalf("expected applied skill despite ledger failure: %v", statErr)
	}
}

// Gap 1: a complete replacement of an existing on-disk skill whose generator
// cannot review (it does not implement ReplacementReviewer) must fail CLOSED
// before apply rather than silently writing an unreviewed replacement — even
// though the candidate body is itself valid and would otherwise apply.
func TestRuntime_RunColdPathOnce_ReplaceWithoutReviewerFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	// stubDraftGenerator has no ReviewReplacement method, so the mandatory review
	// cannot run. The candidate body is a valid corrected replacement.
	rt := newReviewRuntime(t, root, store, stubDraftGenerator{
		draft: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	})

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail closed without a reviewer")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if !strings.Contains(err.Error(), "replacement review unavailable") {
		t.Fatalf("error = %v, want a replacement-review-unavailable reason", err)
	}

	// The on-disk skill must be untouched — no unreviewed replacement was written.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != selfRestartOriginalBody() {
		t.Fatalf("skill was modified despite failing closed:\n%s", string(got))
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("len(drafts) = %d, want 1", len(drafts))
	}
	if drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("draft status = %q, want quarantined", drafts[0].Status)
	}
}

// Gap 3: the reviewer compares against the old document read before apply. If the
// on-disk skill changes after that read (an intervening edit), apply must detect
// the conflict and fail CLOSED, preserving the intervening edit rather than
// overwriting it with a refinement based on the stale old document.
func TestRuntime_RunColdPathOnce_StaleOldDocumentConflictFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	intervening := "---\nname: " + selfRestartSkill + "\ndescription: restart helper\n---\n" +
		"# " + selfRestartSkill + "\n\n## Start Here\nEdited out of band after the review.\n"

	gen := &scriptedReviewer{
		first:    selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		reviewed: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
		// Simulate a concurrent edit landing after the review captured the old
		// document but before apply verifies it.
		onReview: func() {
			_ = os.WriteFile(skillPath, []byte(intervening), 0o644)
		},
	}
	rt := newReviewRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail on the stale-document conflict")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if gen.reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want exactly 1", gen.reviewCalls)
	}

	// The intervening edit must survive: apply refused to overwrite it.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != intervening {
		t.Fatalf("intervening edit was overwritten:\n%s", string(got))
	}

	drafts, err := store.LoadDrafts()
	if err != nil {
		t.Fatalf("LoadDrafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Status != evolution.DraftStatusQuarantined {
		t.Fatalf("drafts = %+v, want one quarantined draft", drafts)
	}
}
