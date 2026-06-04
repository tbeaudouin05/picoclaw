package liveconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const MainRecordID = "main"

type Record struct {
	ID            string          `json:"id"`
	ConfigVersion int64           `json:"config_version"`
	UpdatedAt     string          `json:"updated_at"`
	ConfigJSON    json.RawMessage `json:"config_json"`
}

type Store interface {
	InitSchema(ctx context.Context) error
	GetRecord(ctx context.Context, id string) (*Record, error)
	UpdateRecord(ctx context.Context, id string, expectedVersion int64, configJSON json.RawMessage) (*Record, error)
	Close() error
}

type NotFoundError struct{ ID string }

func (e NotFoundError) Error() string { return fmt.Sprintf("live config record %q not found", e.ID) }

type VersionConflictError struct {
	ID       string
	Expected int64
}

func (e VersionConflictError) Error() string {
	return fmt.Sprintf("live config record %q version conflict at expected version %d", e.ID, e.Expected)
}

func BuildRuntimePrompt(rec *Record) string {
	if rec == nil {
		return ""
	}
	body := strings.TrimSpace(string(rec.ConfigJSON))
	if body == "" {
		body = "{}"
	}
	updated := strings.TrimSpace(rec.UpdatedAt)
	if updated == "" {
		updated = time.Now().UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(`AUTHORITATIVE LIVE CONFIG

This block is loaded from the configured live config database for this message. It is authoritative for the assistant behavior controlled by this deployment.

Ignore any earlier local config files, setup files, memory copies, cached details, or in-conversation config if they conflict with this block.

Record id: %s
Config version: %d
Updated at: %s

Current live config JSON:
%s

Live-config rules:
- Operate according to the JSON in this block.
- Do not invent or assume values absent from this JSON.
- If this JSON defines identity, behavior, constraints, capabilities, content, policies, routing, handoff, availability, or other deployment-specific facts, use those values as authoritative.
`, rec.ID, rec.ConfigVersion, updated, body)
}
