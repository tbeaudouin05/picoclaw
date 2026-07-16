package evolution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestStore_RunLedgerAppendAndLoad(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	started := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)

	entry := ColdPathRunLedgerEntry{
		RunID:             "run-1",
		WorkspaceID:       workspace,
		Mode:              "draft",
		Status:            runLedgerStatusCompleted,
		StartedAt:         started,
		FinishedAt:        started.Add(2 * time.Second),
		TaskRecordCount:   5,
		PrunedTaskCount:   1,
		AdmittedTaskCount: 3,
		NewPatternCount:   2,
		ReadyPatternCount: 2,
		ProcessedPatterns: 1,
	}
	if err := store.AppendRunLedgerEntry(entry); err != nil {
		t.Fatalf("AppendRunLedgerEntry: %v", err)
	}
	if err := store.AppendRunLedgerEntry(ColdPathRunLedgerEntry{
		RunID: "run-2", WorkspaceID: workspace, Mode: "apply",
		Status: runLedgerStatusFailed, ErrorSummary: "boom", StartedAt: started.Add(time.Hour),
	}); err != nil {
		t.Fatalf("AppendRunLedgerEntry second: %v", err)
	}

	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("len(loaded) = %d, want 2", len(loaded))
	}
	if loaded[0].RunID != "run-1" || loaded[0].Mode != "draft" || loaded[0].AdmittedTaskCount != 3 {
		t.Fatalf("first entry content unexpected: %+v", loaded[0])
	}
	if !loaded[0].StartedAt.Equal(started) {
		t.Fatalf("StartedAt = %v, want %v", loaded[0].StartedAt, started)
	}
	if loaded[1].Status != runLedgerStatusFailed || loaded[1].ErrorSummary != "boom" {
		t.Fatalf("second entry content unexpected: %+v", loaded[1])
	}
}

func TestStore_PruneRunLedger_RetentionAndWorkspaceScope(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	entries := []ColdPathRunLedgerEntry{
		{RunID: "old", WorkspaceID: workspace, StartedAt: now.Add(-6 * 24 * time.Hour)},
		{RunID: "recent", WorkspaceID: workspace, StartedAt: now.Add(-1 * time.Hour)},
		{RunID: "other-old", WorkspaceID: "other", StartedAt: now.Add(-30 * 24 * time.Hour)},
	}
	for _, entry := range entries {
		if err := store.AppendRunLedgerEntry(entry); err != nil {
			t.Fatalf("AppendRunLedgerEntry: %v", err)
		}
	}

	cutoff := now.Add(-120 * time.Hour) // 5 days
	pruned, err := store.PruneRunLedger(workspace, cutoff)
	if err != nil {
		t.Fatalf("PruneRunLedger: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	got := make(map[string]bool, len(loaded))
	for _, entry := range loaded {
		got[entry.RunID] = true
	}
	if got["old"] {
		t.Fatal("expired entry was not pruned")
	}
	if !got["recent"] {
		t.Fatal("recent entry was pruned")
	}
	if !got["other-old"] {
		t.Fatal("other-workspace entry must not be pruned by this workspace")
	}
}

func TestStore_PruneRunLedger_CapsEntryCount(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	base := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)

	overflow := 5
	for i := 0; i < maxRunLedgerEntries+overflow; i++ {
		if err := store.AppendRunLedgerEntry(ColdPathRunLedgerEntry{
			RunID:       "run",
			WorkspaceID: workspace,
			StartedAt:   base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendRunLedgerEntry %d: %v", i, err)
		}
	}

	// Cutoff older than everything: only the count cap should fire.
	pruned, err := store.PruneRunLedger(workspace, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneRunLedger: %v", err)
	}
	if pruned != overflow {
		t.Fatalf("pruned = %d, want %d", pruned, overflow)
	}
	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(loaded) != maxRunLedgerEntries {
		t.Fatalf("len(loaded) = %d, want %d", len(loaded), maxRunLedgerEntries)
	}
	// The newest entries must survive the cap.
	if !loaded[len(loaded)-1].StartedAt.Equal(base.Add(time.Duration(maxRunLedgerEntries+overflow-1) * time.Minute)) {
		t.Fatalf("newest entry was dropped by cap: %+v", loaded[len(loaded)-1])
	}
}

func TestStore_PruneRunLedger_CapIsPerWorkspace(t *testing.T) {
	// A single shared StateDir ledger holding entries for two workspaces. The
	// noisy workspace overflows the hard cap; the quiet workspace's entries must
	// survive untouched.
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	base := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)

	const noisy = "noisy-workspace"
	const quiet = "quiet-workspace"

	overflow := 3
	for i := 0; i < maxRunLedgerEntries+overflow; i++ {
		if err := store.AppendRunLedgerEntry(ColdPathRunLedgerEntry{
			RunID:       "noisy",
			WorkspaceID: noisy,
			StartedAt:   base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendRunLedgerEntry noisy %d: %v", i, err)
		}
	}
	// A small, well-within-cap set of quiet-workspace entries.
	quietCount := 4
	for i := 0; i < quietCount; i++ {
		if err := store.AppendRunLedgerEntry(ColdPathRunLedgerEntry{
			RunID:       "quiet",
			WorkspaceID: quiet,
			StartedAt:   base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendRunLedgerEntry quiet %d: %v", i, err)
		}
	}

	// Prune on behalf of the noisy workspace with a cutoff older than everything,
	// so only the per-workspace count cap can fire.
	pruned, err := store.PruneRunLedger(noisy, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneRunLedger: %v", err)
	}
	if pruned != overflow {
		t.Fatalf("pruned = %d, want %d (only the noisy workspace overflow)", pruned, overflow)
	}

	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	counts := make(map[string]int)
	for _, entry := range loaded {
		counts[entry.WorkspaceID]++
	}
	if counts[noisy] != maxRunLedgerEntries {
		t.Fatalf("noisy count = %d, want %d", counts[noisy], maxRunLedgerEntries)
	}
	if counts[quiet] != quietCount {
		t.Fatalf("quiet workspace lost entries to a noisy neighbor: count = %d, want %d", counts[quiet], quietCount)
	}
}

// stubPatternClusterer forces a deterministic error out of the cold-path
// clustering stage. It implements only PatternClusterer (not the evidence
// variant), so the runtime takes the BuildPatterns path.
type stubPatternClusterer struct {
	err error
}

func (c stubPatternClusterer) BuildPatterns(
	_ context.Context,
	_ string,
	_ []LearningRecord,
	_ []LearningRecord,
) ([]LearningRecord, []string, error) {
	return nil, nil, c.err
}

func seedRunLedgerTaskRecords(t *testing.T, store *Store, workspace string, now time.Time, n int) {
	t.Helper()
	success := true
	records := make([]LearningRecord, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, LearningRecord{
			ID:          "task-" + string(rune('a'+i)),
			Kind:        RecordKindTask,
			WorkspaceID: workspace,
			CreatedAt:   now,
			SessionKey:  "session",
			Summary:     "seeded task summary",
			UserGoal:    "seeded goal",
			FinalOutput: "seeded final output",
			Status:      RecordStatus("new"),
			Success:     &success,
		})
	}
	if err := store.AppendTaskRecords(context.Background(), records); err != nil {
		t.Fatalf("AppendTaskRecords: %v", err)
	}
}

func TestRuntime_RunColdPathOnce_FailedRunWritesLedgerRecordWithSummary(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	seedRunLedgerTaskRecords(t, store, workspace, now, 2)

	buildErr := errors.New("cluster build exploded")
	rt, err := NewRuntime(RuntimeOptions{
		Config:           config.EvolutionConfig{Enabled: true, Mode: "draft"},
		Now:              func() time.Time { return now },
		Store:            store,
		PatternClusterer: stubPatternClusterer{err: buildErr},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if runErr := rt.RunColdPathOnce(context.Background(), workspace); !errors.Is(runErr, buildErr) {
		t.Fatalf("RunColdPathOnce err = %v, want %v", runErr, buildErr)
	}

	entry := onlyRunLedgerEntry(t, store)
	if entry.Status != runLedgerStatusFailed {
		t.Fatalf("Status = %q, want %q", entry.Status, runLedgerStatusFailed)
	}
	if entry.ErrorSummary != buildErr.Error() {
		t.Fatalf("ErrorSummary = %q, want %q", entry.ErrorSummary, buildErr.Error())
	}
	// Partial metrics captured before the failure must be recorded.
	if entry.TaskRecordCount != 2 {
		t.Fatalf("TaskRecordCount = %d, want 2 (partial metric before failure)", entry.TaskRecordCount)
	}
	if !entry.StartedAt.Equal(now) {
		t.Fatalf("StartedAt = %v, want %v", entry.StartedAt, now)
	}
}

func TestRuntime_RunColdPathOnce_CanceledRunWritesCanceledLedgerRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewStore(NewPaths(workspace, ""))
			now := time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC)
			seedRunLedgerTaskRecords(t, store, workspace, now, 3)

			rt, err := NewRuntime(RuntimeOptions{
				Config:           config.EvolutionConfig{Enabled: true, Mode: "draft"},
				Now:              func() time.Time { return now },
				Store:            store,
				PatternClusterer: stubPatternClusterer{err: tc.err},
			})
			if err != nil {
				t.Fatalf("NewRuntime: %v", err)
			}
			if runErr := rt.RunColdPathOnce(context.Background(), workspace); !errors.Is(runErr, tc.err) {
				t.Fatalf("RunColdPathOnce err = %v, want %v", runErr, tc.err)
			}

			entry := onlyRunLedgerEntry(t, store)
			if entry.Status != runLedgerStatusCanceled {
				t.Fatalf("Status = %q, want %q", entry.Status, runLedgerStatusCanceled)
			}
			if entry.ErrorSummary != tc.err.Error() {
				t.Fatalf("ErrorSummary = %q, want %q", entry.ErrorSummary, tc.err.Error())
			}
			// Partial metrics captured before the early return must be recorded.
			if entry.TaskRecordCount != 3 {
				t.Fatalf("TaskRecordCount = %d, want 3 (partial metric before cancel)", entry.TaskRecordCount)
			}
		})
	}
}

func onlyRunLedgerEntry(t *testing.T, store *Store) ColdPathRunLedgerEntry {
	t.Helper()
	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded) = %d, want 1", len(loaded))
	}
	return loaded[0]
}

func TestRuntime_RunColdPathOnce_WritesRunLedgerEntry(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)

	rt, err := NewRuntime(RuntimeOptions{
		Config: config.EvolutionConfig{Enabled: true, Mode: "draft"},
		Now:    func() time.Time { return now },
		Store:  store,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if runErr := rt.RunColdPathOnce(context.Background(), workspace); runErr != nil {
		t.Fatalf("RunColdPathOnce: %v", runErr)
	}

	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded) = %d, want 1", len(loaded))
	}
	entry := loaded[0]
	if entry.Status != runLedgerStatusCompleted {
		t.Fatalf("Status = %q, want %q", entry.Status, runLedgerStatusCompleted)
	}
	if entry.Mode != "draft" || entry.WorkspaceID != workspace || entry.RunID == "" {
		t.Fatalf("entry content unexpected: %+v", entry)
	}
	if !entry.StartedAt.Equal(now) {
		t.Fatalf("StartedAt = %v, want %v", entry.StartedAt, now)
	}
}

func TestRuntime_RunColdPathOnce_ObserveModeWritesNoLedger(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))

	rt, err := NewRuntime(RuntimeOptions{
		Config: config.EvolutionConfig{Enabled: true, Mode: "observe"},
		Store:  store,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if runErr := rt.RunColdPathOnce(context.Background(), workspace); runErr != nil {
		t.Fatalf("RunColdPathOnce: %v", runErr)
	}

	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("observe mode wrote %d ledger entries, want 0", len(loaded))
	}
}

func TestRuntime_RunColdPathOnce_CleansExpiredLedgerEntries(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	// Seed an entry older than the default 120h (5 day) retention window.
	if err := store.AppendRunLedgerEntry(ColdPathRunLedgerEntry{
		RunID: "stale", WorkspaceID: workspace, StartedAt: now.Add(-6 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed AppendRunLedgerEntry: %v", err)
	}

	rt, err := NewRuntime(RuntimeOptions{
		Config: config.EvolutionConfig{Enabled: true, Mode: "draft"},
		Now:    func() time.Time { return now },
		Store:  store,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if runErr := rt.RunColdPathOnce(context.Background(), workspace); runErr != nil {
		t.Fatalf("RunColdPathOnce: %v", runErr)
	}

	loaded, err := store.LoadRunLedger()
	if err != nil {
		t.Fatalf("LoadRunLedger: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded) = %d, want 1 (stale pruned, current appended)", len(loaded))
	}
	if loaded[0].RunID == "stale" {
		t.Fatal("stale entry beyond retention was not cleaned")
	}
	if loaded[0].PrunedLedgerCount != 1 {
		t.Fatalf("PrunedLedgerCount = %d, want 1", loaded[0].PrunedLedgerCount)
	}
}
