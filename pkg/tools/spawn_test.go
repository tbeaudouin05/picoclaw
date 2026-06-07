package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// mockSpawner implements SubTurnSpawner for testing.
type mockSpawner struct {
	lastConfig SubTurnConfig
	done       chan struct{}
}

type blockingSpawner struct {
	started chan struct{}
	release chan struct{}
	result  *ToolResult
	err     error
}

func (b *blockingSpawner) SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*ToolResult, error) {
	close(b.started)
	<-b.release
	return b.result, b.err
}

func (m *mockSpawner) SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*ToolResult, error) {
	m.lastConfig = cfg
	if m.done != nil {
		close(m.done)
	}

	// Extract task from system prompt for response
	task := cfg.SystemPrompt
	if strings.Contains(task, "Task: ") {
		parts := strings.Split(task, "Task: ")
		if len(parts) > 1 {
			task = parts[1]
		}
	}
	return &ToolResult{
		ForLLM:  "Task completed: " + task,
		ForUser: "Task completed",
	}, nil
}

func TestSpawnTool_Execute_EmptyTask(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test")
	tool := NewSpawnTool(manager)

	ctx := context.Background()

	tests := []struct {
		name string
		args map[string]any
	}{
		{"empty string", map[string]any{"task": ""}},
		{"whitespace only", map[string]any{"task": "   "}},
		{"tabs and newlines", map[string]any{"task": "\t\n  "}},
		{"missing task key", map[string]any{"label": "test"}},
		{"wrong type", map[string]any{"task": 123}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.Execute(ctx, tt.args)
			if result == nil {
				t.Fatal("Result should not be nil")
			}
			if !result.IsError {
				t.Error("Expected error for invalid task parameter")
			}
			if !strings.Contains(result.ForLLM, "task is required") {
				t.Errorf("Error message should mention 'task is required', got: %s", result.ForLLM)
			}
		})
	}
}

func TestSpawnTool_Execute_ValidTask(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test")
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{done: make(chan struct{})}
	tool.SetSpawner(spawner)

	ctx := context.Background()
	args := map[string]any{
		"task":     "Write a haiku about coding",
		"label":    "haiku-task",
		"agent_id": "research",
	}

	result := tool.Execute(ctx, args)
	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Errorf("Expected success for valid task, got error: %s", result.ForLLM)
	}
	if !result.Async {
		t.Error("SpawnTool should return async result")
	}
	<-spawner.done
	if spawner.lastConfig.TargetAgentID != "research" {
		t.Errorf("TargetAgentID = %q, want research", spawner.lastConfig.TargetAgentID)
	}
	if !spawner.lastConfig.Critical {
		t.Error("SpawnTool should mark background subturns as critical")
	}
}

func TestSpawnTool_Execute_UsesResolvedTargetModel(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "parent-model", "/tmp/test")
	tool := NewSpawnTool(manager)
	spawner := &mockSpawner{done: make(chan struct{})}
	tool.SetSpawner(spawner)
	tool.SetTargetModelResolver(func(targetAgentID string) string {
		if targetAgentID == "mini" {
			return "mini-model"
		}
		return ""
	})

	ctx := context.Background()
	args := map[string]any{
		"task":     "Do the thing",
		"agent_id": "mini",
	}

	result := tool.Execute(ctx, args)
	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Fatalf("Expected success for valid task, got error: %s", result.ForLLM)
	}
	<-spawner.done
	if spawner.lastConfig.Model != "mini-model" {
		t.Fatalf("Model = %q, want mini-model", spawner.lastConfig.Model)
	}
}

func TestSpawnTool_DirectAsyncIsVisibleToSpawnStatus(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test")
	spawnTool := NewSpawnTool(manager)
	spawner := &blockingSpawner{
		started: make(chan struct{}),
		release: make(chan struct{}),
		result:  NewToolResult("done"),
	}
	spawnTool.SetSpawner(spawner)
	statusTool := NewSpawnStatusTool(manager)

	ctx := WithToolContext(context.Background(), "telegram", "chat-123")
	result := spawnTool.Execute(ctx, map[string]any{
		"task":     "check direct async visibility",
		"label":    "status-gap",
		"agent_id": "powerup",
	})
	if result.IsError {
		t.Fatalf("spawn returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "subagent-1") {
		t.Fatalf("spawn acknowledgement should include task ID, got: %s", result.ForLLM)
	}

	<-spawner.started

	status := statusTool.Execute(ctx, map[string]any{})
	if status.IsError {
		t.Fatalf("spawn_status returned error: %s", status.ForLLM)
	}
	for _, want := range []string{"subagent-1", "status=running", "label=\"status-gap\"", "agent=powerup", "check direct async visibility"} {
		if !strings.Contains(status.ForLLM, want) {
			t.Fatalf("spawn_status missing %q in:\n%s", want, status.ForLLM)
		}
	}

	close(spawner.release)
}

func TestSpawnTool_DirectAsyncCompletionUpdatesSpawnStatus(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test")
	spawnTool := NewSpawnTool(manager)
	spawner := &mockSpawner{done: make(chan struct{})}
	spawnTool.SetSpawner(spawner)
	statusTool := NewSpawnStatusTool(manager)

	ctx := WithToolContext(context.Background(), "telegram", "chat-123")
	result := spawnTool.Execute(ctx, map[string]any{"task": "finish direct async"})
	if result.IsError {
		t.Fatalf("spawn returned error: %s", result.ForLLM)
	}
	<-spawner.done

	status := statusTool.Execute(ctx, map[string]any{"task_id": "subagent-1"})
	if status.IsError {
		t.Fatalf("spawn_status returned error: %s", status.ForLLM)
	}
	for _, want := range []string{"status=completed", "Task completed: finish direct async"} {
		if !strings.Contains(status.ForLLM, want) {
			t.Fatalf("spawn_status missing %q in:\n%s", want, status.ForLLM)
		}
	}
}

func TestSpawnTool_DirectAsyncFailureUpdatesSpawnStatus(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test")
	spawnTool := NewSpawnTool(manager)
	spawner := &blockingSpawner{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("boom"),
	}
	spawnTool.SetSpawner(spawner)
	statusTool := NewSpawnStatusTool(manager)

	ctx := WithToolContext(context.Background(), "telegram", "chat-123")
	result := spawnTool.Execute(ctx, map[string]any{"task": "fail direct async"})
	if result.IsError {
		t.Fatalf("spawn returned error: %s", result.ForLLM)
	}
	<-spawner.started
	close(spawner.release)

	// Wait until the background goroutine records failure.
	for i := 0; i < 100; i++ {
		status := statusTool.Execute(ctx, map[string]any{"task_id": "subagent-1"})
		if strings.Contains(status.ForLLM, "status=failed") {
			if !strings.Contains(status.ForLLM, "Error: boom") {
				t.Fatalf("spawn_status missing failure detail:\n%s", status.ForLLM)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("spawn_status did not observe failure")
}

func TestSpawnTool_Execute_NilManager(t *testing.T) {
	tool := NewSpawnTool(nil)

	ctx := context.Background()
	args := map[string]any{"task": "test task"}

	result := tool.Execute(ctx, args)
	if !result.IsError {
		t.Error("Expected error for nil manager")
	}
	if !strings.Contains(result.ForLLM, "Subagent manager not configured") {
		t.Errorf("Error message should mention manager not configured, got: %s", result.ForLLM)
	}
}
