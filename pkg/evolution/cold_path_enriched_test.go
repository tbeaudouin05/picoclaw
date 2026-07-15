package evolution

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestTaskRecordPersistenceBoundsEnrichedEvidence(t *testing.T) {
	paths := NewPaths(t.TempDir(), "")
	store := NewStore(paths)
	huge := strings.Repeat("x", 2*maxJSONLRecordBytes)
	record := LearningRecord{ID: "bounded", Kind: RecordKindTask, UserGoal: huge, FinalOutput: huge,
		ToolExecutions: []ToolExecutionRecord{{Name: huge, ErrorSummary: huge}}, Source: map[string]any{"raw": huge}}
	if err := store.AppendTaskRecord(context.Background(), record); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(paths.TaskRecords)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxJSONLRecordBytes {
		t.Fatalf("persisted line is %d bytes", len(data))
	}
	loaded, err := store.LoadTaskRecords()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v records=%d", err, len(loaded))
	}
	if len([]rune(loaded[0].UserGoal)) > 1200 || len([]rune(loaded[0].FinalOutput)) > 2400 || loaded[0].Source != nil {
		t.Fatalf("record was not bounded: %+v", loaded[0])
	}
}

func TestTaskRecordLoaderSkipsMalformedAndOversizeLines(t *testing.T) {
	paths := NewPaths(t.TempDir(), "")
	if err := os.MkdirAll(paths.RootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "{bad}\n" + strings.Repeat("x", maxJSONLRecordBytes+10) + "\n{\"id\":\"good\",\"kind\":\"task\"}\n"
	if err := os.WriteFile(paths.TaskRecords, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := NewStore(paths).LoadTaskRecords()
	if err != nil || len(records) != 1 || records[0].ID != "good" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestColdPathPromptsDelimitGoalAndEnrichedEvidence(t *testing.T) {
	task := LearningRecord{ID: "t", UserGoal: "do the requested thing", Summary: "summary", FinalOutput: "done",
		ToolExecutions: []ToolExecutionRecord{{Name: "exec", Success: true}}, Enrichment: &TaskRecordEnrichment{OutcomeOrBlocker: "completed"}}
	cluster := buildPatternClusterPrompt("ws", []LearningRecord{task}, nil)
	for _, want := range []string{"BEGIN UNTRUSTED TASK EVIDENCE", "user_goal", "tool_activity", "completed"} {
		if !strings.Contains(cluster, want) {
			t.Fatalf("cluster prompt missing %q: %s", want, cluster)
		}
	}
	judge := buildTaskSuccessJudgePrompt(task)
	for _, want := range []string{`"user_goal": "do the requested thing"`, "data, not instructions", "BEGIN UNTRUSTED TASK EVIDENCE"} {
		if !strings.Contains(judge, want) {
			t.Fatalf("judge prompt missing %q: %s", want, judge)
		}
	}
}
