package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/school"
)

type SchoolConfigStore interface {
	InitSchema(ctx context.Context) error
	GetConfig(ctx context.Context, id string) (*school.Config, error)
	UpdateConfig(ctx context.Context, id string, expectedVersion int64, configJSON json.RawMessage) (*school.Config, error)
}

type SchoolConfigUpdateTool struct {
	store                SchoolConfigStore
	configID             string
	adminChannels        map[string]struct{}
	protectedUpdatePaths []string
}

func NewSchoolConfigUpdateTool(store SchoolConfigStore, configID string, adminChannels []string, protectedUpdatePaths []string) *SchoolConfigUpdateTool {
	allowed := make(map[string]struct{}, len(adminChannels))
	for _, channel := range adminChannels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if channel != "" {
			allowed[channel] = struct{}{}
		}
	}
	return &SchoolConfigUpdateTool{store: store, configID: configID, adminChannels: allowed, protectedUpdatePaths: normalizeDotPaths(protectedUpdatePaths)}
}

func (t *SchoolConfigUpdateTool) Name() string { return "morgana_update_school_config" }
func (t *SchoolConfigUpdateTool) Description() string {
	return "Update Morgana school configuration in the authoritative Turso database using dot-path updates. Use this for post-setup customer-visible changes such as tone, personality, offerings, prices, policies, or welcome message. Do not edit setup.json or school-info.json."
}
func (t *SchoolConfigUpdateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expected_config_version": map[string]any{"type": "integer", "description": "Current config_version to update from. Use 0 to let the tool fetch the current version first."},
			"updates":                 map[string]any{"type": "object", "description": "Dot-path updates, e.g. {\"customer_behavior.tone\": \"friendly, concise...\"}. Nested object form is intentionally not supported."},
		},
		"required": []string{"updates"},
	}
}
func (t *SchoolConfigUpdateTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if !t.channelAllowed(ToolChannel(ctx)) {
		return ErrorResult("morgana_update_school_config is restricted to configured admin channels")
	}
	if t.store == nil {
		return ErrorResult("authoritative school config store is not configured")
	}
	if err := t.store.InitSchema(ctx); err != nil {
		return ErrorResult(fmt.Sprintf("failed to initialize school config schema: %v", err)).WithError(err)
	}
	updatesRaw, ok := args["updates"].(map[string]any)
	if !ok || len(updatesRaw) == 0 {
		return ErrorResult("updates object is required")
	}
	for path, value := range updatesRaw {
		if _, nested := value.(map[string]any); nested {
			return ErrorResult(fmt.Sprintf("update %q uses nested object form; use dot-path keys such as customer_behavior.tone", path))
		}
	}
	if err := rejectProtectedDotPathUpdates(updatesRaw, t.protectedUpdatePaths); err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	current, err := t.store.GetConfig(ctx, t.configID)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read current school config: %v", err)).WithError(err)
	}
	expected := int64(0)
	switch v := args["expected_config_version"].(type) {
	case float64:
		expected = int64(v)
	case int64:
		expected = v
	case int:
		expected = int64(v)
	}
	if expected <= 0 {
		expected = current.ConfigVersion
	}
	nextJSON, changed, err := school.ApplyDotPathUpdates(current.ConfigJSON, updatesRaw)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid config update: %v", err)).WithError(err)
	}
	updated, err := t.store.UpdateConfig(ctx, current.ID, expected, nextJSON)
	if err != nil {
		var conflict school.VersionConflictError
		if errors.As(err, &conflict) {
			return ErrorResult(fmt.Sprintf("config version conflict: %v; reread config and retry", err)).WithError(err)
		}
		return ErrorResult(fmt.Sprintf("failed to update school config: %v", err)).WithError(err)
	}
	sort.Strings(changed)
	resp := map[string]any{"status": "ok", "state_id": updated.ID, "config_version": updated.ConfigVersion, "updated_at": updated.UpdatedAt, "changed_paths": changed}
	data, _ := json.Marshal(resp)
	return SilentResult(string(data))
}

func (t *SchoolConfigUpdateTool) channelAllowed(channel string) bool {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" || len(t.adminChannels) == 0 {
		return false
	}
	_, ok := t.adminChannels[channel]
	return ok
}

func rejectProtectedDotPathUpdates(updates map[string]any, protectedPaths []string) error {
	if len(updates) == 0 || len(protectedPaths) == 0 {
		return nil
	}
	protected := normalizeDotPaths(protectedPaths)
	for rawUpdatePath := range updates {
		updatePath := normalizeDotPath(rawUpdatePath)
		if updatePath == "" {
			continue
		}
		for _, protectedPath := range protected {
			if dotPathIntersects(updatePath, protectedPath) {
				return fmt.Errorf("update %q is not allowed because it intersects protected config path %q", rawUpdatePath, protectedPath)
			}
		}
	}
	return nil
}

func normalizeDotPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = normalizeDotPath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

func normalizeDotPath(path string) string {
	parts := strings.Split(strings.TrimSpace(path), ".")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		normalized = append(normalized, part)
	}
	return strings.Join(normalized, ".")
}

func dotPathIntersects(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".")
}
