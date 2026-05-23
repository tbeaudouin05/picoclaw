package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// DeleteColleagueTool removes a single locally-configured colleague (agent)
// from the loaded config and persists the change atomically via SaveConfig.
//
// It mirrors Aster Live's local-only registry: the deletion only affects
// the local config.json; remote Agent Discovery / registry views update
// only after PicoClaw restart/reload.
type DeleteColleagueTool struct {
	cfg        *config.Config
	configPath string
}

// NewDeleteColleagueTool constructs a DeleteColleagueTool bound to the given
// config pointer and on-disk config path.
//
// Both arguments are required at registration time; the tool refuses to
// execute at runtime if either is missing.
func NewDeleteColleagueTool(cfg *config.Config, configPath string) *DeleteColleagueTool {
	return &DeleteColleagueTool{cfg: cfg, configPath: configPath}
}

func (t *DeleteColleagueTool) Name() string { return "delete_colleague" }

func (t *DeleteColleagueTool) Description() string {
	return "Remove a locally-configured colleague (agent) from PicoClaw's config.json. " +
		"Matches by normalized agent ID. Refuses to delete the default agent, the agent currently executing this call, or the last remaining configured agent. " +
		"This only edits the local config — a PicoClaw restart/reload is required before Agent Discovery and the runtime registry reflect the deletion."
}

func (t *DeleteColleagueTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Agent ID of the colleague to remove. Trimmed and normalized via the same rule as agent registration; must be non-empty and contain at least one [a-z0-9] character.",
			},
		},
		"required": []string{"id"},
	}
}

func (t *DeleteColleagueTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t.cfg == nil {
		return ErrorResult("delete_colleague is not configured: missing config (nil cfg)")
	}
	if strings.TrimSpace(t.configPath) == "" {
		return ErrorResult("delete_colleague is not configured: missing config path")
	}

	rawID, ok := args["id"].(string)
	if !ok {
		return ErrorResult("id is required and must be a string")
	}
	trimmed := strings.TrimSpace(rawID)
	if trimmed == "" {
		return ErrorResult("id is required and cannot be empty")
	}
	if !hasNormalizableAgentIDCharRe.MatchString(strings.ToLower(trimmed)) {
		return ErrorResult(fmt.Sprintf("id %q is un-normalizable (no valid [a-z0-9] characters)", rawID))
	}
	target := routing.NormalizeAgentID(trimmed)

	colleagueConfigMu.Lock()
	defer colleagueConfigMu.Unlock()

	list := t.cfg.Agents.List
	if len(list) == 0 {
		return ErrorResult(fmt.Sprintf("no colleague matches id %q (agent list is empty)", target))
	}

	idx := -1
	for i, a := range list {
		if routing.NormalizeAgentID(a.ID) == target {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrorResult(fmt.Sprintf("no colleague matches id %q", target))
	}

	match := list[idx]

	if match.Default {
		return ErrorResult(fmt.Sprintf("refusing to delete colleague %q: it is the default agent", target))
	}
	if currentID := strings.TrimSpace(ToolAgentID(ctx)); currentID != "" {
		if routing.NormalizeAgentID(currentID) == target {
			return ErrorResult(fmt.Sprintf("refusing to delete colleague %q: it is the agent currently executing this call", target))
		}
	}
	if len(list) == 1 {
		return ErrorResult(fmt.Sprintf("refusing to delete colleague %q: it is the last remaining configured agent", target))
	}

	updated := make([]config.AgentConfig, 0, len(list)-1)
	updated = append(updated, list[:idx]...)
	updated = append(updated, list[idx+1:]...)
	t.cfg.Agents.List = updated

	if err := config.SaveConfig(t.configPath, t.cfg); err != nil {
		// Roll back in-memory change so a save failure leaves cfg consistent.
		t.cfg.Agents.List = list
		return ErrorResult(fmt.Sprintf("failed to persist config after deleting colleague %q: %v", target, err)).
			WithError(err)
	}

	msg := fmt.Sprintf(
		"Colleague %q deleted locally from config.json. A PicoClaw restart/reload is required before Agent Discovery and the runtime registry reflect this change.",
		target,
	)
	return SilentResult(msg)
}
