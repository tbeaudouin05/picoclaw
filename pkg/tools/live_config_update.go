package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/liveconfig"
)

type LiveConfigStore interface {
	InitSchema(ctx context.Context) error
	GetRecord(ctx context.Context, id string) (*liveconfig.Record, error)
	UpdateRecord(ctx context.Context, id string, expectedVersion int64, configJSON json.RawMessage) (*liveconfig.Record, error)
}

type LiveConfigInitialRecordStore interface {
	UpsertInitialRecord(ctx context.Context, id string, configJSON json.RawMessage) (*liveconfig.Record, error)
}

type LiveConfigUpdateTool struct {
	store         LiveConfigStore
	recordID      string
	adminChannels map[string]struct{}
}

func NewLiveConfigUpdateTool(store LiveConfigStore, recordID string, adminChannels []string) *LiveConfigUpdateTool {
	allowed := make(map[string]struct{}, len(adminChannels))
	for _, channel := range adminChannels {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if channel != "" {
			allowed[channel] = struct{}{}
		}
	}
	return &LiveConfigUpdateTool{store: store, recordID: recordID, adminChannels: allowed}
}

func (t *LiveConfigUpdateTool) Name() string { return "update_live_config" }
func (t *LiveConfigUpdateTool) Description() string {
	return "Update the authoritative live configuration record using flat dot-path updates. Use this for deployment-specific live configuration changes. Do not edit setup files or cached local config copies."
}
func (t *LiveConfigUpdateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expected_config_version": map[string]any{"type": "integer", "description": "Current config_version to update from. Use 0 to let the tool fetch the current version first."},
			"updates":                 map[string]any{"type": "object", "description": "Flat dot-path updates, e.g. {\"behavior.tone\": \"friendly, concise...\"}. Nested object form is intentionally not supported."},
		},
		"required": []string{"updates"},
	}
}
func (t *LiveConfigUpdateTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if !t.channelAllowed(ToolChannel(ctx)) {
		return ErrorResult("update_live_config is restricted to configured admin channels")
	}
	if t.store == nil {
		return ErrorResult("live config store is not configured")
	}
	updatesRaw, ok := args["updates"].(map[string]any)
	if !ok || len(updatesRaw) == 0 {
		return ErrorResult("updates object is required")
	}
	for path, value := range updatesRaw {
		if _, nested := value.(map[string]any); nested {
			return ErrorResult(fmt.Sprintf("update %q uses nested object form; use flat dot-path keys such as behavior.tone", path))
		}
	}
	if err := t.store.InitSchema(ctx); err != nil {
		return ErrorResult(fmt.Sprintf("failed to initialize live config schema: %v", err)).WithError(err)
	}
	current, err := t.store.GetRecord(ctx, t.recordID)
	if err != nil {
		var notFound liveconfig.NotFoundError
		if errors.As(err, &notFound) {
			initialStore, ok := t.store.(LiveConfigInitialRecordStore)
			if !ok {
				return ErrorResult(fmt.Sprintf("live config record %q does not exist and this store cannot create it", t.recordID)).WithError(err)
			}
			current, err = initialStore.UpsertInitialRecord(ctx, t.recordID, json.RawMessage(`{}`))
			if err != nil {
				return ErrorResult(fmt.Sprintf("failed to create initial live config record: %v", err)).WithError(err)
			}
		} else {
			return ErrorResult(fmt.Sprintf("failed to read current live config: %v", err)).WithError(err)
		}
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
	nextJSON, changed, err := liveconfig.ApplyDotPathUpdates(current.ConfigJSON, updatesRaw)
	if err != nil {
		return ErrorResult(fmt.Sprintf("invalid config update: %v", err)).WithError(err)
	}
	updated, err := t.store.UpdateRecord(ctx, current.ID, expected, nextJSON)
	if err != nil {
		var conflict liveconfig.VersionConflictError
		if errors.As(err, &conflict) {
			return ErrorResult(fmt.Sprintf("config version conflict: %v; reread config and retry", err)).WithError(err)
		}
		return ErrorResult(fmt.Sprintf("failed to update live config: %v", err)).WithError(err)
	}
	sort.Strings(changed)
	resp := map[string]any{"status": "ok", "record_id": updated.ID, "config_version": updated.ConfigVersion, "updated_at": updated.UpdatedAt, "changed_paths": changed}
	data, _ := json.Marshal(resp)
	return SilentResult(string(data))
}

func (t *LiveConfigUpdateTool) channelAllowed(channel string) bool {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" || len(t.adminChannels) == 0 {
		return false
	}
	_, ok := t.adminChannels[channel]
	return ok
}
