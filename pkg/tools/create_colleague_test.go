package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newTestConfigWithPath(t *testing.T) (*config.Config, string) {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := config.DefaultConfig()
	// Start from an empty agent list so duplicate-detection tests have a known
	// baseline regardless of what DefaultConfig seeds.
	cfg.Agents.List = nil

	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("seed SaveConfig: %v", err)
	}
	return cfg, path
}

func TestCreateColleague_CreatesAndPersists(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{
		"id":              "Alice",
		"name":            "  Alice the Architect  ",
		"model":           "  gpt-4o  ",
		"workspace":       "/tmp/alice",
		"skills":          []any{"planning", "  planning  ", " ", "review"},
		"allow_subagents": []any{"  Bob ", "bob", "Carol"},
	})

	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %+v", res)
	}

	if len(cfg.Agents.List) != 1 {
		t.Fatalf("expected 1 agent in list, got %d", len(cfg.Agents.List))
	}
	got := cfg.Agents.List[0]
	if got.ID != "alice" {
		t.Errorf("ID = %q, want %q", got.ID, "alice")
	}
	if got.Name != "Alice the Architect" {
		t.Errorf("Name = %q, want trimmed display name", got.Name)
	}
	if got.Workspace != "/tmp/alice" {
		t.Errorf("Workspace = %q, want %q", got.Workspace, "/tmp/alice")
	}
	if got.Model == nil || got.Model.Primary != "gpt-4o" {
		t.Errorf("Model = %+v, want Primary=gpt-4o", got.Model)
	}
	if want := []string{"planning", "review"}; !equalStrings(got.Skills, want) {
		t.Errorf("Skills = %v, want %v", got.Skills, want)
	}
	if got.Subagents == nil {
		t.Fatalf("Subagents nil; expected populated allow_agents")
	}
	if want := []string{"bob", "carol"}; !equalStrings(got.Subagents.AllowAgents, want) {
		t.Errorf("Subagents.AllowAgents = %v, want %v", got.Subagents.AllowAgents, want)
	}

	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(reloaded.Agents.List) != 1 || reloaded.Agents.List[0].ID != "alice" {
		t.Fatalf("colleague did not survive round-trip; agents=%+v", reloaded.Agents.List)
	}
	if !strings.Contains(res.ForLLM, "Restart or reload PicoClaw") {
		t.Errorf("result message missing restart/reload notice: %q", res.ForLLM)
	}
}

func TestCreateColleague_OmitsEmptyOptionalFields(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{
		"id":              "bob",
		"name":            "   ",
		"model":           "",
		"workspace":       "   ",
		"skills":          []any{"   ", ""},
		"allow_subagents": []any{" "},
	})
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %+v", res)
	}
	got := cfg.Agents.List[0]
	if got.Name != "" {
		t.Errorf("Name should be empty, got %q", got.Name)
	}
	if got.Workspace != "" {
		t.Errorf("Workspace should be empty, got %q", got.Workspace)
	}
	if got.Model != nil {
		t.Errorf("Model should be nil when not specified, got %+v", got.Model)
	}
	if len(got.Skills) != 0 {
		t.Errorf("Skills should be empty, got %v", got.Skills)
	}
	if got.Subagents != nil {
		t.Errorf("Subagents should be nil when allow_subagents contains only blanks, got %+v", got.Subagents)
	}
}

func TestCreateColleague_DuplicateID(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	cfg.Agents.List = []config.AgentConfig{{ID: "alice"}}

	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{
		"id": "  ALICE ",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected error on duplicate ID, got: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "already exists") {
		t.Errorf("error message should mention duplicate: %q", res.ForLLM)
	}
	if len(cfg.Agents.List) != 1 {
		t.Errorf("agent list should be unchanged on duplicate, got %d", len(cfg.Agents.List))
	}
}

func TestCreateColleague_EmptyID(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{"id": "   "})
	if res == nil || !res.IsError {
		t.Fatalf("expected error on empty ID, got: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "'id' is required") {
		t.Errorf("error message should mention id requirement: %q", res.ForLLM)
	}
}

func TestCreateColleague_MissingConfigPath(t *testing.T) {
	cfg := config.DefaultConfig()
	tool := NewCreateColleagueTool(cfg, "")

	res := tool.Execute(context.Background(), map[string]any{"id": "alice"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error when config path missing, got: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "config path") {
		t.Errorf("error message should mention config path: %q", res.ForLLM)
	}
}

func TestCreateColleague_MissingConfig(t *testing.T) {
	tool := NewCreateColleagueTool(nil, "/tmp/whatever.json")
	res := tool.Execute(context.Background(), map[string]any{"id": "alice"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error when config nil, got: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "config is not loaded") {
		t.Errorf("error message should mention missing config: %q", res.ForLLM)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCreateColleague_RejectsUnnormalizableID(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{"id": "@@@"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error on unnormalizable ID, got: %+v", res)
	}
	if len(cfg.Agents.List) != 0 {
		t.Fatalf("agent list should be unchanged, got %+v", cfg.Agents.List)
	}
}

func TestCreateColleague_RejectsReservedDefaultID(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{"id": "main"})
	if res == nil || !res.IsError {
		t.Fatalf("expected error on reserved default ID, got: %+v", res)
	}
	if !strings.Contains(res.ForLLM, "reserved default") {
		t.Errorf("error message should mention reserved default: %q", res.ForLLM)
	}
	if len(cfg.Agents.List) != 0 {
		t.Fatalf("agent list should be unchanged, got %+v", cfg.Agents.List)
	}
}

func TestCreateColleague_DropsUnnormalizableAndDefaultAllowSubagents(t *testing.T) {
	cfg, path := newTestConfigWithPath(t)
	tool := NewCreateColleagueTool(cfg, path)

	res := tool.Execute(context.Background(), map[string]any{
		"id":              "alice",
		"allow_subagents": []any{"@@@", "main", "Bob"},
	})
	if res == nil || res.IsError {
		t.Fatalf("Execute returned error: %+v", res)
	}
	got := cfg.Agents.List[0]
	if got.Subagents == nil {
		t.Fatalf("expected one valid allow_subagents entry, got nil")
	}
	if want := []string{"bob"}; !equalStrings(got.Subagents.AllowAgents, want) {
		t.Errorf("Subagents.AllowAgents = %v, want %v", got.Subagents.AllowAgents, want)
	}
}
