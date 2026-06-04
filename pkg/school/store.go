package school

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const MainConfigID = "main"

type Config struct {
	ID            string          `json:"id"`
	ConfigVersion int64           `json:"config_version"`
	UpdatedAt     string          `json:"updated_at"`
	ConfigJSON    json.RawMessage `json:"config_json"`
}

type Store interface {
	InitSchema(ctx context.Context) error
	GetConfig(ctx context.Context, id string) (*Config, error)
	UpdateConfig(ctx context.Context, id string, expectedVersion int64, configJSON json.RawMessage) (*Config, error)
	Close() error
}

type NotFoundError struct{ ID string }

func (e NotFoundError) Error() string { return fmt.Sprintf("school config %q not found", e.ID) }

type VersionConflictError struct {
	ID       string
	Expected int64
}

func (e VersionConflictError) Error() string {
	return fmt.Sprintf("school config %q version conflict at expected version %d", e.ID, e.Expected)
}

func BuildRuntimePrompt(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	body := strings.TrimSpace(string(cfg.ConfigJSON))
	if body == "" {
		body = "{}"
	}
	updated := strings.TrimSpace(cfg.UpdatedAt)
	if updated == "" {
		updated = time.Now().UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(`AUTHORITATIVE RUNTIME SCHOOL CONFIG

This block is loaded from the configured Turso school database for this message. It is authoritative for customer/admin school behavior.

Ignore any earlier school config, school-info.json content, setup.json content, memory copy, tone, personality, offerings, prices, policies, or school details in this conversation if they conflict with this block.

Config id: %s
Config version: %d
Updated at: %s

Current school config JSON:
%s

Customer-facing identity/personality rules:
- Answer as the school assistant described by this JSON, not as PicoClaw, Aster, Alice, or a generic assistant.
- Obey customer_behavior/tone/personality/welcome/assistant-trigger fields when present.
- If customer_behavior.welcome_message is present, send it or a concise equivalent when the visible conversation history does not already contain that welcome message, or when the current user message is a very generic greeting such as "hi", "hello", "hey", or similar.
- Be willing to remind customers how AI replies work. It is acceptable to include or repeat the /ai off and /ai on control instructions from the welcome message occasionally, especially in first replies, generic greetings, or moments where a human handoff/manual reply expectation may be relevant.
- Do not invent offerings, prices, schedules, availability, policies, or contact details absent from this JSON.
`, cfg.ID, cfg.ConfigVersion, updated, body)
}
