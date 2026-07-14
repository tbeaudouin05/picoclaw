package evolution

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRuntime_PruneExpiredTaskRecordsPreservesLiveDependencies(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(NewPaths(workspace, ""))
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	old := now.Add(-25 * time.Hour)
	recent := now.Add(-23 * time.Hour)

	tasks := []LearningRecord{
		{ID: "expired-unreferenced", Kind: RecordKindTask, WorkspaceID: workspace, CreatedAt: old},
		{ID: "active-pattern-task", Kind: RecordKindTask, WorkspaceID: workspace, CreatedAt: old},
		{ID: "pending-draft-task", Kind: RecordKindTask, WorkspaceID: workspace, CreatedAt: old},
		{ID: "applied-skill-task", Kind: RecordKindTask, WorkspaceID: workspace, CreatedAt: old},
		{ID: "recent-unreferenced", Kind: RecordKindTask, WorkspaceID: workspace, CreatedAt: recent},
		{ID: "other-workspace-old", Kind: RecordKindTask, WorkspaceID: "other-workspace", CreatedAt: old},
	}
	if err := store.AppendTaskRecords(t.Context(), tasks); err != nil {
		t.Fatalf("AppendTaskRecords: %v", err)
	}
	patterns := []LearningRecord{
		{ID: "active-pattern", Kind: RecordKindPattern, WorkspaceID: workspace, Status: RecordStatus("ready"), TaskRecordIDs: []string{"active-pattern-task"}},
		{ID: "pending-pattern", Kind: RecordKindPattern, WorkspaceID: workspace, Status: RecordStatus("inactive"), TaskRecordIDs: []string{"pending-draft-task"}},
		{ID: "applied-pattern", Kind: RecordKindPattern, WorkspaceID: workspace, Status: RecordStatus("inactive"), TaskRecordIDs: []string{"applied-skill-task"}},
	}
	if err := store.AppendPatternRecords(patterns); err != nil {
		t.Fatalf("AppendPatternRecords: %v", err)
	}
	if err := store.SaveDrafts([]SkillDraft{
		{ID: "pending-draft", WorkspaceID: workspace, SourceRecordID: "pending-pattern", Status: DraftStatusCandidate},
		{ID: "applied-draft", WorkspaceID: workspace, SourceRecordID: "applied-pattern", Status: DraftStatusAccepted},
	}); err != nil {
		t.Fatalf("SaveDrafts: %v", err)
	}
	if err := store.SaveProfile(SkillProfile{
		SkillName: "applied-skill", WorkspaceID: workspace, Status: SkillStatusActive,
		VersionHistory: []SkillVersionEntry{{DraftID: "applied-draft"}},
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	runtime, err := NewRuntime(RuntimeOptions{
		Config: config.EvolutionConfig{Enabled: true, Mode: "draft", TaskRecordRetentionHours: 24},
		Now:    func() time.Time { return now },
		Store:  store,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	pruned, err := runtime.pruneExpiredTaskRecords(store, workspace, patterns)
	if err != nil {
		t.Fatalf("pruneExpiredTaskRecords: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	remaining, err := store.LoadTaskRecords()
	if err != nil {
		t.Fatalf("LoadTaskRecords: %v", err)
	}
	got := make(map[string]bool, len(remaining))
	for _, record := range remaining {
		got[record.ID] = true
	}
	if got["expired-unreferenced"] {
		t.Fatal("expired unreferenced task record was not pruned")
	}
	for _, id := range []string{"active-pattern-task", "pending-draft-task", "applied-skill-task", "recent-unreferenced", "other-workspace-old"} {
		if !got[id] {
			t.Fatalf("live or recent task record %q was pruned", id)
		}
	}
}
