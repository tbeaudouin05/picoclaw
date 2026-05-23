package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// hasNormalizableAgentIDCharRe matches the minimum requirement for an agent ID
// to survive routing.NormalizeAgentID without collapsing to the default
// ("main"): at least one byte in [a-z0-9] after lower-casing.
var hasNormalizableAgentIDCharRe = regexp.MustCompile(`[a-z0-9]`)

// CreateColleagueTool appends a local AgentConfig entry to the on-disk
// PicoClaw config. It mirrors Aster Live's "save local colleague" behavior:
// values are trimmed, empty values are not persisted, and the new entry is
// only visible to Agent Discovery / registry after a PicoClaw restart or
// reload.
type CreateColleagueTool struct {
	cfg        *config.Config
	configPath string
}

// NewCreateColleagueTool constructs the tool. cfg and configPath must both be
// supplied — the tool refuses to mutate without an explicit on-disk path.
func NewCreateColleagueTool(cfg *config.Config, configPath string) *CreateColleagueTool {
	return &CreateColleagueTool{cfg: cfg, configPath: configPath}
}

func (t *CreateColleagueTool) Name() string { return "create_colleague" }

func (t *CreateColleagueTool) Description() string {
	return "Create a local colleague (agent config) and persist it to the PicoClaw config file. " +
		"The colleague is saved locally; a PicoClaw restart or reload is required before it " +
		"appears in Agent Discovery and the runtime registry."
}

func (t *CreateColleagueTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "Required agent ID. Trimmed and normalized to [a-z0-9][a-z0-9_-]{0,63}.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Optional display name. Empty/whitespace values are not persisted.",
			},
			"model": map[string]any{
				"type":        "string",
				"description": "Optional primary model name. When empty, the agent inherits the default model.",
			},
			"workspace": map[string]any{
				"type":        "string",
				"description": "Optional workspace path. Empty/whitespace values are not persisted.",
			},
			"skills": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of skill names. Trimmed; empty entries dropped; duplicates removed.",
			},
			"allow_subagents": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of agent IDs this colleague may spawn as subagents. Each entry is trimmed and normalized; empty entries dropped; duplicates removed.",
			},
		},
		"required": []string{"id"},
	}
}

func (t *CreateColleagueTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	if t.configPath == "" {
		return ErrorResult("create_colleague: config path is not configured; cannot persist changes")
	}
	if t.cfg == nil {
		return ErrorResult("create_colleague: config is not loaded")
	}

	rawID, _ := args["id"].(string)
	trimmedID := strings.TrimSpace(rawID)
	if trimmedID == "" {
		return ErrorResult("create_colleague: 'id' is required and must be a non-empty string")
	}
	if !hasNormalizableAgentIDCharRe.MatchString(strings.ToLower(trimmedID)) {
		return ErrorResult("create_colleague: 'id' must contain at least one ASCII letter or digit")
	}
	normalizedID := routing.NormalizeAgentID(trimmedID)
	if normalizedID == routing.DefaultAgentID {
		return ErrorResult(fmt.Sprintf("create_colleague: refusing to create reserved default agent id %q", routing.DefaultAgentID))
	}

	entry := config.AgentConfig{ID: normalizedID}

	if name, ok := args["name"].(string); ok {
		if v := strings.TrimSpace(name); v != "" {
			entry.Name = v
		}
	}
	if workspace, ok := args["workspace"].(string); ok {
		if v := strings.TrimSpace(workspace); v != "" {
			entry.Workspace = v
		}
	}
	if model, ok := args["model"].(string); ok {
		if v := strings.TrimSpace(model); v != "" {
			entry.Model = &config.AgentModelConfig{Primary: v}
		}
	}

	if skills, ok := stringSliceArg(args["skills"]); ok {
		entry.Skills = trimDedupePreserveOrder(skills, false)
	}
	if allow, ok := stringSliceArg(args["allow_subagents"]); ok {
		if normalized := trimDedupePreserveOrder(allow, true); len(normalized) > 0 {
			entry.Subagents = &config.SubagentsConfig{AllowAgents: normalized}
		}
	}

	colleagueConfigMu.Lock()
	defer colleagueConfigMu.Unlock()

	for _, existing := range t.cfg.Agents.List {
		if routing.NormalizeAgentID(existing.ID) == normalizedID {
			return ErrorResult(fmt.Sprintf(
				"create_colleague: a colleague with id %q already exists in the config", normalizedID))
		}
	}

	t.cfg.Agents.List = append(t.cfg.Agents.List, entry)

	if err := config.SaveConfig(t.configPath, t.cfg); err != nil {
		t.cfg.Agents.List = t.cfg.Agents.List[:len(t.cfg.Agents.List)-1]
		return ErrorResult(fmt.Sprintf("create_colleague: failed to save config: %v", err))
	}

	return NewToolResult(fmt.Sprintf(
		"Colleague %q saved locally to %s. Restart or reload PicoClaw for it to appear in Agent Discovery and the runtime registry.",
		normalizedID, t.configPath,
	))
}

// stringSliceArg accepts either []string or []any (the JSON-decoded form) and
// returns the values as a []string. The second return value is false when the
// argument is absent or not a recognised array shape.
func stringSliceArg(v any) ([]string, bool) {
	switch s := v.(type) {
	case nil:
		return nil, false
	case []string:
		out := make([]string, len(s))
		copy(out, s)
		return out, true
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

// trimDedupePreserveOrder trims each entry, drops empties, and removes
// duplicates while preserving first-seen order. When normalize is true, entries
// are run through routing.NormalizeAgentID after trimming and the normalized
// form is used for both the value and the dedupe key.
func trimDedupePreserveOrder(in []string, normalize bool) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if normalize {
			if !hasNormalizableAgentIDCharRe.MatchString(strings.ToLower(v)) {
				continue
			}
			v = routing.NormalizeAgentID(v)
			if v == routing.DefaultAgentID {
				continue
			}
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
