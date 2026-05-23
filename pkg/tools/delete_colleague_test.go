package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func writeTestConfig(t *testing.T, agents []config.AgentConfig) (cfg *config.Config, path string) {
	t.Helper()
	cfg = config.DefaultConfig()
	cfg.Agents.List = agents
	dir := t.TempDir()
	path = filepath.Join(dir, "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig(setup): %v", err)
	}
	return cfg, path
}

func reloadAgentsFromDisk(t *testing.T, path string) []config.AgentConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var raw struct {
		Agents struct {
			List []config.AgentConfig `json:"list"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal config.json: %v", err)
	}
	return raw.Agents.List
}

func TestDeleteColleague_DeletesAndPersists(t *testing.T) {
	cfg, path := writeTestConfig(t, []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
		{ID: "bob"},
	})
	tool := NewDeleteColleagueTool(cfg, path)

	result := tool.Execute(context.Background(), map[string]any{"id": "alice"})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "deleted locally") || !strings.Contains(result.ForLLM, "restart") {
		t.Fatalf("result message missing required phrasing: %q", result.ForLLM)
	}

	if len(cfg.Agents.List) != 2 {
		t.Fatalf("expected 2 in-memory agents, got %d", len(cfg.Agents.List))
	}
	for _, a := range cfg.Agents.List {
		if a.ID == "alice" {
			t.Fatalf("alice still present in cfg.Agents.List after delete")
		}
	}

	persisted := reloadAgentsFromDisk(t, path)
	if len(persisted) != 2 {
		t.Fatalf("expected 2 persisted agents, got %d", len(persisted))
	}
	for _, a := range persisted {
		if a.ID == "alice" {
			t.Fatalf("alice still present on disk after delete")
		}
	}
}

func TestDeleteColleague_NormalizesIDForMatching(t *testing.T) {
	cfg, path := writeTestConfig(t, []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
	})
	tool := NewDeleteColleagueTool(cfg, path)

	// "  ALICE " should normalize to "alice" and match.
	result := tool.Execute(context.Background(), map[string]any{"id": "  ALICE "})
	if result.IsError {
		t.Fatalf("expected normalization match, got error: %s", result.ForLLM)
	}
	for _, a := range cfg.Agents.List {
		if a.ID == "alice" {
			t.Fatalf("alice should have been removed")
		}
	}
}

func TestDeleteColleague_UnknownIDLeavesConfigUnchanged(t *testing.T) {
	original := []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
	}
	cfg, path := writeTestConfig(t, original)
	tool := NewDeleteColleagueTool(cfg, path)

	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	result := tool.Execute(context.Background(), map[string]any{"id": "ghost"})
	if !result.IsError {
		t.Fatalf("expected error for unknown id, got: %s", result.ForLLM)
	}
	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agent list should be unchanged, got %d entries", len(cfg.Agents.List))
	}

	persisted := reloadAgentsFromDisk(t, path)
	if len(persisted) != 2 {
		t.Fatalf("persisted list should be unchanged, got %d entries", len(persisted))
	}

	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config (after): %v", err)
	}
	// SaveConfig was not called, so mtime should not have advanced.
	if statBefore.ModTime() != statAfter.ModTime() {
		t.Fatalf("config file was rewritten on unknown-id error: before=%v after=%v",
			statBefore.ModTime(), statAfter.ModTime())
	}
}

func TestDeleteColleague_RefusesDefaultAgent(t *testing.T) {
	cfg, path := writeTestConfig(t, []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
	})
	tool := NewDeleteColleagueTool(cfg, path)

	result := tool.Execute(context.Background(), map[string]any{"id": "main"})
	if !result.IsError {
		t.Fatalf("expected refusal for default agent, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "default agent") {
		t.Fatalf("error should mention default agent, got: %q", result.ForLLM)
	}
	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agent list should be unchanged, got %d entries", len(cfg.Agents.List))
	}
}

func TestDeleteColleague_RefusesCurrentAgent(t *testing.T) {
	cfg, path := writeTestConfig(t, []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
		{ID: "bob"},
	})
	tool := NewDeleteColleagueTool(cfg, path)

	// alice is the agent currently executing this call.
	ctx := WithToolSessionContext(context.Background(), "alice", "session-1", nil)
	result := tool.Execute(ctx, map[string]any{"id": "alice"})
	if !result.IsError {
		t.Fatalf("expected refusal for current agent, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "currently executing") {
		t.Fatalf("error should mention currently executing, got: %q", result.ForLLM)
	}
	if len(cfg.Agents.List) != 3 {
		t.Fatalf("agent list should be unchanged, got %d entries", len(cfg.Agents.List))
	}
}

func TestDeleteColleague_RefusesLastRemainingAgent(t *testing.T) {
	// Single configured agent, not the default — refusal should still fire
	// because removing it would leave the config with zero agents.
	cfg, path := writeTestConfig(t, []config.AgentConfig{
		{ID: "alice"},
	})
	tool := NewDeleteColleagueTool(cfg, path)

	result := tool.Execute(context.Background(), map[string]any{"id": "alice"})
	if !result.IsError {
		t.Fatalf("expected refusal for last remaining agent, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "last remaining") {
		t.Fatalf("error should mention last remaining, got: %q", result.ForLLM)
	}
	if len(cfg.Agents.List) != 1 {
		t.Fatalf("agent list should be unchanged, got %d entries", len(cfg.Agents.List))
	}
}

func TestDeleteColleague_MissingConfigPath(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
	}
	tool := NewDeleteColleagueTool(cfg, "")

	result := tool.Execute(context.Background(), map[string]any{"id": "alice"})
	if !result.IsError {
		t.Fatalf("expected error for missing config path, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "config path") {
		t.Fatalf("error should mention config path, got: %q", result.ForLLM)
	}
}

func TestDeleteColleague_NilConfig(t *testing.T) {
	tool := NewDeleteColleagueTool(nil, "/tmp/does-not-matter.json")

	result := tool.Execute(context.Background(), map[string]any{"id": "alice"})
	if !result.IsError {
		t.Fatalf("expected error for nil cfg, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "nil cfg") {
		t.Fatalf("error should mention nil cfg, got: %q", result.ForLLM)
	}
}

func TestDeleteColleague_EmptyID(t *testing.T) {
	cfg, path := writeTestConfig(t, []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "alice"},
	})
	tool := NewDeleteColleagueTool(cfg, path)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"empty string", map[string]any{"id": ""}},
		{"whitespace only", map[string]any{"id": "   "}},
		{"un-normalizable", map[string]any{"id": "!!!"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), tc.args)
			if !result.IsError {
				t.Fatalf("expected error for %s, got: %s", tc.name, result.ForLLM)
			}
		})
	}

	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agent list should be unchanged after empty-id errors, got %d entries", len(cfg.Agents.List))
	}
}
