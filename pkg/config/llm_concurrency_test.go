// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMaxConcurrentLLMCallsLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unset means unlimited", 0, 0},
		{"negative means unlimited", -5, 0},
		{"positive is preserved", 4, 4},
		{"one", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &AgentDefaults{MaxConcurrentLLMCalls: tc.in}
			if got := d.MaxConcurrentLLMCallsLimit(); got != tc.want {
				t.Errorf("MaxConcurrentLLMCallsLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestGetLLMSlotWaitTimeout(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want time.Duration
	}{
		{"unset defaults to 30s", 0, 30 * time.Second},
		{"negative defaults to 30s", -1, 30 * time.Second},
		{"positive seconds", 10, 10 * time.Second},
		{"one second", 1, 1 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &AgentDefaults{LLMSlotWaitTimeout: tc.in}
			if got := d.GetLLMSlotWaitTimeout(); got != tc.want {
				t.Errorf("GetLLMSlotWaitTimeout(%d) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestDefaultConfigLLMConcurrencyUnlimited documents the backward-compatible
// default: without explicit config, concurrency is unlimited and the slot wait
// timeout falls back to 30s.
func TestDefaultConfigLLMConcurrencyUnlimited(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Agents.Defaults.MaxConcurrentLLMCallsLimit(); got != 0 {
		t.Errorf("default MaxConcurrentLLMCallsLimit = %d, want 0 (unlimited)", got)
	}
	if got := cfg.Agents.Defaults.GetLLMSlotWaitTimeout(); got != DefaultLLMSlotWaitTimeout {
		t.Errorf("default GetLLMSlotWaitTimeout = %s, want %s", got, DefaultLLMSlotWaitTimeout)
	}
}

// TestLLMConcurrencyJSONRoundTrip verifies the JSON tags parse as documented.
func TestLLMConcurrencyJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"max_concurrent_llm_calls": 3, "llm_slot_wait_timeout": 15}`)
	var d AgentDefaults
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.MaxConcurrentLLMCalls != 3 {
		t.Errorf("MaxConcurrentLLMCalls = %d, want 3", d.MaxConcurrentLLMCalls)
	}
	if d.LLMSlotWaitTimeout != 15 {
		t.Errorf("LLMSlotWaitTimeout = %d, want 15", d.LLMSlotWaitTimeout)
	}
	if got := d.MaxConcurrentLLMCallsLimit(); got != 3 {
		t.Errorf("MaxConcurrentLLMCallsLimit = %d, want 3", got)
	}
	if got := d.GetLLMSlotWaitTimeout(); got != 15*time.Second {
		t.Errorf("GetLLMSlotWaitTimeout = %s, want 15s", got)
	}
}
