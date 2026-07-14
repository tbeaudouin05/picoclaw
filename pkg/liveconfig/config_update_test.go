package liveconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyDotPathUpdates(t *testing.T) {
	base := json.RawMessage(`{"customer_behavior":{"tone":"pirate"},"setup_complete":true}`)
	out, changed, err := ApplyDotPathUpdates(base, map[string]any{"customer_behavior.tone": "dolphin"})
	if err != nil {
		t.Fatalf("ApplyDotPathUpdates() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "customer_behavior.tone" {
		t.Fatalf("changed = %v", changed)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("output JSON invalid: %v", err)
	}
	behavior := decoded["customer_behavior"].(map[string]any)
	if behavior["tone"] != "dolphin" {
		t.Fatalf("tone = %v", behavior["tone"])
	}
}

func TestApplyDotPathUpdatesRejectsNestedUpdateValue(t *testing.T) {
	_, _, err := ApplyDotPathUpdates(json.RawMessage(`{}`), map[string]any{"customer_behavior..tone": "bad"})
	if err == nil {
		t.Fatal("expected invalid path error")
	}
}

func TestApplyDotPathUpdatesRejectsArrayIntermediate(t *testing.T) {
	base := json.RawMessage(`{"offerings":[{"name":"lesson"}]}`)
	_, _, err := ApplyDotPathUpdates(base, map[string]any{"offerings.name": "bad"})
	if err == nil {
		t.Fatal("expected non-object intermediate error")
	}
}

func TestBuildRuntimePromptIncludesAuthoritativeOverride(t *testing.T) {
	prompt := BuildRuntimePrompt(
		&Record{
			ID:            "main",
			ConfigVersion: 7,
			UpdatedAt:     "2026-06-02T20:00:00Z",
			ConfigJSON:    json.RawMessage(`{"customer_behavior":{"tone":"dolphin"}}`),
		},
	)
	for _, want := range []string{"AUTHORITATIVE LIVE CONFIG", "Config version: 7", "Ignore any earlier local config files", "dolphin"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestApplyDotPathUpdatesRejectsOverlappingPaths(t *testing.T) {
	_, _, err := ApplyDotPathUpdates(
		json.RawMessage(`{"foo":{"bar":"old"}}`),
		map[string]any{"foo": "new", "foo.bar": "also-new"},
	)
	if err == nil {
		t.Fatal("expected overlapping path error")
	}
}

func TestApplyDotPathChangesDeletesExistingLeaf(t *testing.T) {
	base := json.RawMessage(`{"customer_behavior":{"tone":"pirate","smoke":"sentinel"}}`)
	out, changed, err := ApplyDotPathChanges(base, nil, []string{"customer_behavior.smoke"})
	if err != nil {
		t.Fatalf("ApplyDotPathChanges() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "customer_behavior.smoke" {
		t.Fatalf("changed = %v", changed)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("output JSON invalid: %v", err)
	}
	behavior := decoded["customer_behavior"].(map[string]any)
	if _, ok := behavior["smoke"]; ok {
		t.Fatalf("smoke key still present: %#v", behavior)
	}
	if behavior["tone"] != "pirate" {
		t.Fatalf("tone changed: %#v", behavior)
	}
}
