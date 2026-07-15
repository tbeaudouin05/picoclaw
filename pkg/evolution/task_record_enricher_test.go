package evolution

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type enrichmentProvider struct {
	content  string
	err      error
	model    string
	messages []providers.Message
}

func (p *enrichmentProvider) Chat(_ context.Context, messages []providers.Message, _ []providers.ToolDefinition, model string, _ map[string]any) (*providers.LLMResponse, error) {
	p.model, p.messages = model, messages
	if p.err != nil {
		return nil, p.err
	}
	return &providers.LLMResponse{Content: p.content}, nil
}
func (p *enrichmentProvider) GetDefaultModel() string { return "default-model" }

type blockingEnrichmentProvider struct{ release chan struct{} }

func (p *blockingEnrichmentProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	<-p.release
	return nil, context.Canceled
}
func (p *blockingEnrichmentProvider) GetDefaultModel() string { return "unused" }

func TestLLMTaskRecordEnricherRequiresSeparateModel(t *testing.T) {
	p := &enrichmentProvider{content: `{}`}
	if enricher := NewLLMTaskRecordEnricher(p, "", 0, true); enricher != nil {
		t.Fatal("enricher should be disabled when its separately configured model is empty")
	}
	if len(p.messages) != 0 {
		t.Fatal("provider was called without an enrichment model")
	}
}

func TestRuntimeAdmissionCountsFailedToolAttempts(t *testing.T) {
	workspace := t.TempDir()
	rt, err := NewRuntime(RuntimeOptions{Config: config.EvolutionConfig{Enabled: true, MinToolCallsToRecord: 3}})
	if err != nil {
		t.Fatal(err)
	}
	base := TurnCaseInput{Workspace: workspace, TurnID: "turn", Status: "error", UserMessage: "raw user", FinalContent: "raw final"}
	base.ToolExecutions = []ToolExecutionRecord{{Name: "a", Success: false}, {Name: "b", Success: true}}
	if err := rt.FinalizeTurn(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NewPaths(workspace, "").TaskRecords); !os.IsNotExist(err) {
		t.Fatalf("below-threshold turn was persisted: %v", err)
	}
	base.ToolExecutions = append(base.ToolExecutions, ToolExecutionRecord{Name: "c", Success: false, ErrorSummary: "failed"})
	if err := rt.FinalizeTurn(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	record := loadOnlyTaskRecord(t, workspace)
	if record.AttemptedToolCalls != 3 || record.ToolExecutions[2].Success {
		t.Fatalf("failed attempts not counted: %+v", record)
	}
	if record.UserGoal != "raw user" || record.FinalOutput != "raw final" {
		t.Fatalf("raw content not preserved: %+v", record)
	}
}

func TestRuntimeAdmissionUsesDefaultMinimumToolCalls(t *testing.T) {
	workspace := t.TempDir()
	rt, err := NewRuntime(RuntimeOptions{Config: config.EvolutionConfig{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	input := TurnCaseInput{Workspace: workspace, TurnID: "turn", Status: "completed", ToolExecutions: []ToolExecutionRecord{{Name: "a", Success: true}, {Name: "b", Success: true}}}
	if err := rt.FinalizeTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NewPaths(workspace, "").TaskRecords); !os.IsNotExist(err) {
		t.Fatalf("default threshold should reject two tool calls: %v", err)
	}
	input.ToolExecutions = append(input.ToolExecutions, ToolExecutionRecord{Name: "c", Success: true})
	if err := rt.FinalizeTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if record := loadOnlyTaskRecord(t, workspace); record.AttemptedToolCalls != 3 {
		t.Fatalf("AttemptedToolCalls = %d, want 3", record.AttemptedToolCalls)
	}

	overrideWorkspace := t.TempDir()
	overrideRuntime, err := NewRuntime(RuntimeOptions{Config: config.EvolutionConfig{Enabled: true, MinToolCallsToRecord: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := overrideRuntime.FinalizeTurn(context.Background(), TurnCaseInput{Workspace: overrideWorkspace, TurnID: "override", Status: "completed", ToolExecutions: []ToolExecutionRecord{{Name: "exec", Success: true}}}); err != nil {
		t.Fatal(err)
	}
	if record := loadOnlyTaskRecord(t, overrideWorkspace); record.AttemptedToolCalls != 1 {
		t.Fatalf("explicit override did not admit one tool call: %+v", record)
	}
}

func TestLLMTaskRecordEnrichmentSuccessUsesConfiguredModelAndRuntimeFacts(t *testing.T) {
	p := &enrichmentProvider{content: `{"summary":"Investigated a failing build","task_type":"debugging","outcome_or_blocker":"Blocked by compiler error","top_frictions_errors":[{"text":"Compiler failed","confidence":1,"evidence":"tool execution c reported failed"}],"process_improvements":[{"text":"Run the focused compiler check earlier","confidence":0.8,"evidence":"the final attempt exposed the compiler error"}],"reusable_knowledge":[],"learning_value":{"text":"Useful failure-path evidence","confidence":0.9,"evidence":"status and failed attempt are present"}}`}
	enricher := NewLLMTaskRecordEnricher(p, "cheap-model", 0, true)
	success := false
	out, err := enricher.Enrich(context.Background(), LearningRecord{UserGoal: "fix build", FinalOutput: "failed", Status: "new", Success: &success, AttemptedToolCalls: 3, ToolKinds: []string{"exec"}, ToolExecutions: []ToolExecutionRecord{{Name: "c", Success: false}}, UsedSkillNames: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.TaskType != "debugging" || p.model != "cheap-model" {
		t.Fatalf("unexpected enrichment/model: %+v %q", out, p.model)
	}
	if len(p.messages) != 2 || !strings.Contains(p.messages[1].Content, `"attempted_tool_calls":3`) || !strings.Contains(p.messages[1].Content, `"success":false`) {
		t.Fatalf("runtime facts missing from prompt: %+v", p.messages)
	}
}

func TestLLMTaskRecordEnricherBoundsPromptText(t *testing.T) {
	p := &enrichmentProvider{content: `{"summary":"Investigated a task","task_type":"debugging","outcome_or_blocker":"Completed","top_frictions_errors":[],"process_improvements":[],"reusable_knowledge":[],"learning_value":{"text":"Useful evidence","confidence":1,"evidence":"the supplied runtime facts"}}`}
	enricher := NewLLMTaskRecordEnricher(p, "cheap-model", 0, true)
	if _, err := enricher.Enrich(context.Background(), LearningRecord{
		UserGoal:    strings.Repeat("g", taskRecordEnrichmentUserGoalLimit+1),
		FinalOutput: strings.Repeat("f", taskRecordEnrichmentFinalOutputLimit+1),
	}); err != nil {
		t.Fatal(err)
	}
	const prefix = "Runtime-derived evidence (do not contradict or re-infer these facts):\n"
	var facts struct {
		UserMessage string `json:"user_message"`
		FinalOutput string `json:"final_output"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(p.messages[1].Content, prefix)), &facts); err != nil {
		t.Fatal(err)
	}
	if len([]rune(facts.UserMessage)) != taskRecordEnrichmentUserGoalLimit || !strings.HasSuffix(facts.UserMessage, "...") {
		t.Fatalf("UserMessage was not bounded: %q", facts.UserMessage)
	}
	if len([]rune(facts.FinalOutput)) != taskRecordEnrichmentFinalOutputLimit || !strings.HasSuffix(facts.FinalOutput, "...") {
		t.Fatalf("FinalOutput was not bounded: %q", facts.FinalOutput)
	}
}

func TestRuntimeMalformedEnrichmentPersistsDeterministicFallback(t *testing.T) {
	workspace := t.TempDir()
	provider := &enrichmentProvider{content: `{"summary":"not enough fields"}`}
	rt, err := NewRuntime(RuntimeOptions{
		Config:             config.EvolutionConfig{Enabled: true, MinToolCallsToRecord: 1},
		TaskRecordEnricher: NewLLMTaskRecordEnricher(provider, "cheap-model", 0, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.FinalizeTurn(context.Background(), TurnCaseInput{Workspace: workspace, TurnID: "fallback", Status: "completed", UserMessage: "keep me", FinalContent: "kept", ToolExecutions: []ToolExecutionRecord{{Name: "exec", Success: true}}}); err != nil {
		t.Fatalf("enrichment failure escaped: %v", err)
	}
	record := loadOnlyTaskRecord(t, workspace)
	if record.Enrichment != nil || record.UserGoal != "keep me" || record.FinalOutput != "kept" || record.AttemptedToolCalls != 1 {
		t.Fatalf("bad fallback: %+v", record)
	}
}

func TestRuntimeEnrichmentTimeoutPersistsWhenProviderIgnoresContext(t *testing.T) {
	workspace := t.TempDir()
	provider := &blockingEnrichmentProvider{release: make(chan struct{})}
	defer close(provider.release)
	rt, err := NewRuntime(RuntimeOptions{
		Config:             config.EvolutionConfig{Enabled: true, MinToolCallsToRecord: 1},
		TaskRecordEnricher: NewLLMTaskRecordEnricher(provider, "enrichment-model", 10*time.Millisecond, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := TurnCaseInput{Workspace: workspace, TurnID: "timeout", Status: "error", ToolExecutions: []ToolExecutionRecord{{Name: "exec", Success: false}}}
	if err := rt.FinalizeTurn(context.Background(), input); err != nil {
		t.Fatalf("provider timeout prevented persistence: %v", err)
	}
	record := loadOnlyTaskRecord(t, workspace)
	if record.Enrichment != nil || record.AttemptedToolCalls != 1 || record.TurnStatus != "error" {
		t.Fatalf("deterministic fallback was not preserved: %+v", record)
	}
}

func TestParseTaskRecordEnrichmentRejectsMalformedOrUnbounded(t *testing.T) {
	if _, err := parseTaskRecordEnrichment(`{"summary":"x"}`); err == nil {
		t.Fatal("expected malformed enrichment rejection")
	}
	tooMany := `{"summary":"s","task_type":"t","outcome_or_blocker":"o","top_frictions_errors":[],"process_improvements":[],"reusable_knowledge":[],"learning_value":{"text":"` + strings.Repeat("x", 301) + `","confidence":1,"evidence":"e"}}`
	if _, err := parseTaskRecordEnrichment(tooMany); err == nil {
		t.Fatal("expected oversized enrichment rejection")
	}
}

func loadOnlyTaskRecord(t *testing.T, workspace string) LearningRecord {
	t.Helper()
	data, err := os.ReadFile(NewPaths(workspace, "").TaskRecords)
	if err != nil {
		t.Fatal(err)
	}
	var record LearningRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatal(err)
	}
	return record
}
