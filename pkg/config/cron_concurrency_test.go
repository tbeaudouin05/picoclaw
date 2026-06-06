// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"testing"
)

func TestCronEffectiveMaxConcurrent(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unset clamps to 1", 0, 1},
		{"negative clamps to 1", -3, 1},
		{"one is preserved", 1, 1},
		{"positive is preserved", 8, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &CronToolsConfig{MaxConcurrent: tc.in}
			if got := c.EffectiveMaxConcurrent(); got != tc.want {
				t.Errorf("CronMaxConcurrentJobsLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestDefaultConfigCronConcurrency documents the default: without explicit config,
// cron concurrency is 1 (one job at a time).
func TestDefaultConfigCronConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Tools.Cron.EffectiveMaxConcurrent(); got != 1 {
		t.Errorf("default CronMaxConcurrentJobsLimit = %d, want 1", got)
	}
}

// TestCronConcurrencyJSONRoundTrip verifies the JSON tag parses as documented.
func TestCronConcurrencyJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"max_concurrent": 5}`)
	var c CronToolsConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent = %d, want 5", c.MaxConcurrent)
	}
	if got := c.EffectiveMaxConcurrent(); got != 5 {
		t.Errorf("CronMaxConcurrentJobsLimit = %d, want 5", got)
	}
}
