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

// scriptedRegenerator is a DraftGenerator that also implements DraftRegenerator,
// so it opts into the cold path's one bounded feedback-aware retry. It records
// how many times RegenerateDraft was called and the request it received.
type scriptedRegenerator struct {
	first    evolution.SkillDraft
	firstErr error

	regen    evolution.SkillDraft
	regenErr error

	regenCalls  int
	lastRequest evolution.DraftRegenerationRequest
}

func (g *scriptedRegenerator) GenerateDraft(
	_ context.Context,
	_ evolution.LearningRecord,
	_ []skills.SkillInfo,
) (evolution.SkillDraft, error) {
	return g.first, g.firstErr
}

func (g *scriptedRegenerator) RegenerateDraft(
	_ context.Context,
	req evolution.DraftRegenerationRequest,
) (evolution.SkillDraft, error) {
	g.regenCalls++
	g.lastRequest = req
	return g.regen, g.regenErr
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

func newRetryRuntime(t *testing.T, root string, store *evolution.Store, gen evolution.DraftGenerator) *evolution.Runtime {
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

func TestRuntime_RunColdPathOnce_RegeneratesOnceAndAppliesCorrectedReplacement(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedRegenerator{
		first: selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		regen: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newRetryRuntime(t, root, store, gen)

	if runErr := rt.RunColdPathOnce(context.Background(), root); runErr != nil {
		t.Fatalf("RunColdPathOnce: %v", runErr)
	}

	if gen.regenCalls != 1 {
		t.Fatalf("regenCalls = %d, want exactly 1", gen.regenCalls)
	}
	if !strings.Contains(gen.lastRequest.FailureReason, `root heading "`+selfRestartSkill+`" was not preserved`) {
		t.Fatalf("regeneration failure reason = %q, want exact root-heading rejection", gen.lastRequest.FailureReason)
	}
	if gen.lastRequest.TargetSkillName != selfRestartSkill {
		t.Fatalf("regeneration target = %q, want %q", gen.lastRequest.TargetSkillName, selfRestartSkill)
	}
	if gen.lastRequest.ChangeKind != evolution.ChangeKindReplace {
		t.Fatalf("regeneration change kind = %q, want replace", gen.lastRequest.ChangeKind)
	}
	if !gen.lastRequest.TargetExists {
		t.Fatal("regeneration request should mark the target as existing")
	}
	if gen.lastRequest.CurrentSkillBody != selfRestartOriginalBody() {
		t.Fatalf("regeneration current skill body did not match the original target:\n%s", gen.lastRequest.CurrentSkillBody)
	}

	applied, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(applied)
	if !strings.Contains(content, "# "+selfRestartSkill) {
		t.Fatalf("applied skill lost its root heading:\n%s", content)
	}
	if !strings.Contains(content, "fall back to the documented steps") {
		t.Fatalf("applied skill missing corrected learned content:\n%s", content)
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

func TestRuntime_RunColdPathOnce_SecondInvalidResultStopsAfterOneRetry(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedRegenerator{
		first: selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		// The regenerated draft is still invalid (root heading not preserved).
		regen: selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
	}
	rt := newRetryRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail after the second invalid draft")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if gen.regenCalls != 1 {
		t.Fatalf("regenCalls = %d, want exactly 1 (no recursion)", gen.regenCalls)
	}

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != selfRestartOriginalBody() {
		t.Fatalf("skill changed despite two rejected drafts:\n%s", string(got))
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
	findings := strings.Join(drafts[0].ScanFindings, "\n")
	if !strings.Contains(findings, "apply failed:") {
		t.Fatalf("quarantined draft missing second-attempt reason:\n%s", findings)
	}
	if !strings.Contains(findings, "first apply attempt failed before regeneration") {
		t.Fatalf("quarantined draft missing first-attempt reason:\n%s", findings)
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

// TestRuntime_RunColdPathOnce_AuditPersistenceFailureStillRegeneratesOnce proves
// that a typed apply-safety rejection whose rollback-audit persistence ALSO fails
// stays eligible for the one bounded regeneration. The two audit-failure joins in
// applyCandidateDraft must wrap the apply error with %w (not %v) so the
// DraftApplySafetyError classification survives being joined with the audit/save
// error; otherwise errors.As would miss it and the cold path would give up without
// regenerating. It further asserts the retry is fail-closed: a still-invalid
// regenerated draft leaves the skill untouched and the draft quarantined.
func TestRuntime_RunColdPathOnce_AuditPersistenceFailureStillRegeneratesOnce(t *testing.T) {
	root := t.TempDir()
	paths := evolution.NewPaths(root, "")
	store := evolution.NewStore(paths)
	skillPath := writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	// Inject an audit persistence failure: making the profiles path a regular file
	// forces recordRollbackAudit's UpdateProfile to error (ENOTDIR) after the
	// apply-safety rejection, exercising the auditErr branch of applyCandidateDraft.
	if err := os.MkdirAll(filepath.Dir(paths.ProfilesDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(paths.ProfilesDir, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(profiles): %v", err)
	}

	gen := &scriptedRegenerator{
		first: selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		// The regenerated draft is still invalid (root heading not preserved), so
		// the retry must fail closed rather than apply an unsafe replacement.
		regen: selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
	}
	rt := newRetryRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail after the second invalid draft")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	// The compounded audit failure must not have erased the typed apply-safety
	// classification from the returned chain — that is what kept regeneration eligible.
	var safety *evolution.DraftApplySafetyError
	if !errors.As(err, &safety) {
		t.Fatalf("error = %v, want a preserved DraftApplySafetyError in the chain", err)
	}
	if gen.regenCalls != 1 {
		t.Fatalf("regenCalls = %d, want exactly 1 despite the audit persistence failure", gen.regenCalls)
	}
	if !strings.Contains(gen.lastRequest.FailureReason, `root heading "`+selfRestartSkill+`" was not preserved`) {
		t.Fatalf("regeneration failure reason = %q, want exact root-heading rejection", gen.lastRequest.FailureReason)
	}

	// Fail-closed: the on-disk skill is unchanged and the draft is quarantined.
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != selfRestartOriginalBody() {
		t.Fatalf("skill changed despite two rejected drafts:\n%s", string(got))
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

func TestRuntime_RunColdPathOnce_NonRetryableProfileFailureNeverRegenerates(t *testing.T) {
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

	gen := &scriptedRegenerator{
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
		regen: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newRetryRuntime(t, root, store, gen)

	err := rt.RunColdPathOnce(context.Background(), root)
	if err == nil {
		t.Fatal("expected RunColdPathOnce to fail on profile save")
	}
	if !errors.Is(err, evolution.ErrApplyDraftFailed) {
		t.Fatalf("error = %v, want ErrApplyDraftFailed", err)
	}
	if gen.regenCalls != 0 {
		t.Fatalf("regenCalls = %d, want 0 for a non-retryable failure", gen.regenCalls)
	}

	if _, statErr := os.Stat(filepath.Join(root, "skills", "weather", "SKILL.md")); !os.IsNotExist(statErr) {
		t.Fatalf("expected applied skill to be rolled back, got err=%v", statErr)
	}
}

func TestRuntime_RunColdPathOnce_CanceledContextNeverRegeneratesAndRecordsCanceled(t *testing.T) {
	root := t.TempDir()
	store := evolution.NewStore(evolution.NewPaths(root, ""))
	writeSelfRestartSkill(t, root, selfRestartOriginalBody())
	seedSelfRestartReadyRule(t, store, root)

	gen := &scriptedRegenerator{
		first: selfRestartReplaceDraft(root, selfRestartBadHeadingBody()),
		regen: selfRestartReplaceDraft(root, selfRestartCorrectedBody()),
	}
	rt := newRetryRuntime(t, root, store, gen)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rt.RunColdPathOnce(ctx, root)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if gen.regenCalls != 0 {
		t.Fatalf("regenCalls = %d, want 0 on cancellation", gen.regenCalls)
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
	rt := newRetryRuntime(t, root, store, stubDraftGenerator{
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

	rt := newRetryRuntime(t, root, store, stubDraftGenerator{
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
