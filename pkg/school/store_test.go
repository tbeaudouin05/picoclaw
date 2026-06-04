package school

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildRuntimePromptIncludesWelcomeMessageGuidance(t *testing.T) {
	cfg := &Config{
		ID:            "main",
		ConfigVersion: 3,
		UpdatedAt:     "2026-06-03T08:00:00Z",
		ConfigJSON: json.RawMessage(`{
			"setup_complete": true,
			"customer_behavior": {
				"welcome_message": "Welcome! Send /ai off to pause AI replies and /ai on to resume."
			}
		}`),
	}

	prompt := BuildRuntimePrompt(cfg)

	for _, want := range []string{
		"customer_behavior.welcome_message",
		"visible conversation history does not already contain that welcome message",
		"very generic greeting",
		"hi",
		"hello",
		"hey",
		"occasionally",
		"human handoff/manual reply expectation",
		"/ai off",
		"/ai on",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("BuildRuntimePrompt() missing %q in:\n%s", want, prompt)
		}
	}
}
