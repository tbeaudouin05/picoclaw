// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for buildModelWithProtocol helper function.

func TestBuildModelWithProtocol_NoPrefix(t *testing.T) {
	result := buildModelWithProtocol("openai", "gpt-5.4")
	if result != "openai/gpt-5.4" {
		t.Errorf("buildModelWithProtocol(openai, gpt-5.4) = %q, want %q", result, "openai/gpt-5.4")
	}
}

func TestBuildModelWithProtocol_AlreadyHasPrefix(t *testing.T) {
	result := buildModelWithProtocol("openrouter", "openrouter/auto")
	if result != "openrouter/auto" {
		t.Errorf("buildModelWithProtocol(openrouter, openrouter/auto) = %q, want %q", result, "openrouter/auto")
	}
}

func TestBuildModelWithProtocol_DifferentPrefix(t *testing.T) {
	result := buildModelWithProtocol("anthropic", "openrouter/claude-sonnet-4.6")
	if result != "openrouter/claude-sonnet-4.6" {
		t.Errorf(
			"buildModelWithProtocol(anthropic, openrouter/claude-sonnet-4.6) = %q, want %q",
			result,
			"openrouter/claude-sonnet-4.6",
		)
	}
}

// ---------------------------------------------------------------------------
// V0/V1/V2 → V3 migration tests
// ---------------------------------------------------------------------------

// TestLoadConfig_V0MigrateProducesV2 verifies that V0→V3 migration produces
// correct Enabled fields and version.
func TestLoadConfig_V0MigrateProducesV2(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	v0Config := `{
		"model_list": [
			{
				"model_name": "gpt-4",
				"model": "openai/gpt-4",
				"api_key": "sk-test"
			},
			{
				"model_name": "claude",
				"model": "anthropic/claude"
			},
			{
				"model_name": "local-model",
				"model": "vllm/custom-model"
			}
		],
		"gateway": {"host": "127.0.0.1", "port": 18790}
	}`

	if err := os.WriteFile(configPath, []byte(v0Config), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", cfg.Version, CurrentVersion)
	}

	// Check enabled status
	modelEnabled := func(name string) bool {
		m, err := cfg.GetModelConfig(name)
		if err != nil {
			return false
		}
		return m.Enabled
	}

	if !modelEnabled("gpt-4") {
		t.Error("gpt-4 with API key from V0 should be enabled")
	}
	if modelEnabled("claude") {
		t.Error("claude without API key from V0 should be disabled")
	}
	if !modelEnabled("local-model") {
		t.Error("local-model from V0 should be enabled")
	}
}

// TestLoadConfig_UnsupportedVersion verifies that unsupported versions return an error.
func TestLoadConfig_UnsupportedVersion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	badConfig := `{"version": 99, "gateway": {"host": "127.0.0.1", "port": 18790}}`
	if err := os.WriteFile(configPath, []byte(badConfig), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig should return error for unsupported version")
	}
	if !containsString(err.Error(), "unsupported config version") {
		t.Errorf("error = %q, want 'unsupported config version'", err.Error())
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMigrateV0ToV3 verifies V0 (legacy, no version) → V3 migration.
// V0 configs use the old providers format without model_list.
func TestMigrateV0ToV3(t *testing.T) {
	// V0 config: no version field, uses legacy providers
	v0Config := `{
		"agents": {
			"defaults": {
				"provider": "openai",
				"model": "gpt-4"
			}
		},
		"providers": {
			"openai": {
				"api_key": "sk-test123",
				"api_base": "https://api.openai.com/v1"
			}
		},
		"channels": {
			"telegram": {
				"token": "bot-token"
			},
			"discord": {
				"mention_only": true
			}
		}
	}`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(v0Config), 0o600))
	m, err := loadConfigMap(configPath)
	require.NoError(t, err)

	err = migrateV0ToV1(m)
	require.NoError(t, err)
	err = migrateV1ToV2(m)
	require.NoError(t, err)
	err = migrateV2ToV3(m)
	require.NoError(t, err)

	// Version should be set to V3 by the V2→V3 step; LoadConfig applies V3→V4.
	require.Equal(t, 3, m["version"])

	// Providers should be converted to model_list
	modelList, ok := m["model_list"].([]any)
	require.True(t, ok, "model_list should exist")
	require.NotEmpty(t, modelList, "model_list should not be empty")

	t.Logf("modelList: %+v", modelList)
	// First model should be the user's configured provider with user's model
	firstModel := modelList[0].(map[string]any)
	require.Equal(t, "openai", firstModel["model_name"])
	require.Equal(t, "openai/gpt-4", firstModel["model"])
	// api_key is converted to api_keys during migration
	require.Contains(t, firstModel, "api_keys", "api_keys should exist")

	// Channels should be converted to nested format with channel_list
	channelList, ok := m["channel_list"].(map[string]any)
	require.True(t, ok, "channel_list should exist")
	require.NotContains(t, m, "channels", "old 'channels' key should be removed")

	// telegram channel should have settings
	telegram := channelList["telegram"].(map[string]any)
	require.Equal(t, "telegram", telegram["type"])
	require.Contains(t, telegram, "settings", "telegram should have settings")
	settings := telegram["settings"].(map[string]any)
	require.Equal(t, "bot-token", settings["token"])

	// discord channel should have group_trigger and mention_only in group_trigger
	discord := channelList["discord"].(map[string]any)
	require.Equal(t, "discord", discord["type"])
	discordGroupTrigger := discord["group_trigger"].(map[string]any)
	require.Equal(t, true, discordGroupTrigger["mention_only"])
}

// TestMigrateV0ToV3_WithExistingModelList preserves existing model_list when present.
func TestMigrateV0ToV3_WithExistingModelList(t *testing.T) {
	v0Config := `{
		"model_list": [
			{"model_name": "custom", "model": "openai/custom-model", "api_key": "sk-existing"}
		],
		"channels": {
			"telegram": {"token": "bot123"}
		}
	}`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(v0Config), 0o600))
	m, err := loadConfigMap(configPath)
	require.NoError(t, err)

	err = migrateV0ToV1(m)
	require.NoError(t, err)
	err = migrateV1ToV2(m)
	require.NoError(t, err)
	err = migrateV2ToV3(m)
	require.NoError(t, err)
	err = migrateV3ToV4(m)
	require.NoError(t, err)

	// Existing model_list should be preserved (not overridden by providers)
	modelList := m["model_list"].([]any)
	require.Len(t, modelList, 1)
	firstModel := modelList[0].(map[string]any)
	require.Equal(t, "custom", firstModel["model_name"])
}

// TestMigrateV1ToV3 verifies V1 → V3 migration.
// V1 uses flat channel format without "settings" wrapper.
func TestMigrateV1ToV3(t *testing.T) {
	v1Config := `{
		"version": 1,
		"model_list": [
			{"model_name": "gpt-4", "model": "openai/gpt-4", "api_key": "sk-test"}
		],
		"channels": {
			"telegram": {
				"token": "bot-token",
				"base_url": "https://custom.api.com"
			},
			"discord": {
				"mention_only": true,
				"proxy": "socks5://localhost:1080"
			},
			"onebot": {
				"ws_url": "ws://localhost:3001",
				"group_trigger_prefix": ["/"]
			}
		}
	}`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(v1Config), 0o600))
	m, err := loadConfigMap(configPath)
	require.NoError(t, err)

	err = migrateV1ToV2(m)
	require.NoError(t, err)
	err = migrateV2ToV3(m)
	require.NoError(t, err)

	// Version should be set to V3 by the V2→V3 step; LoadConfig applies V3→V4.
	require.Equal(t, 3, m["version"])

	// Channels should be converted to nested format
	channelList, ok := m["channel_list"].(map[string]any)
	require.True(t, ok, "channel_list should exist")
	require.NotContains(t, m, "channels", "old 'channels' key should be removed")

	// telegram: flat fields moved to settings
	telegram := channelList["telegram"].(map[string]any)
	require.Equal(t, "telegram", telegram["type"])
	tgSettings := telegram["settings"].(map[string]any)
	require.Equal(t, "bot-token", tgSettings["token"])
	require.Equal(t, "https://custom.api.com", tgSettings["base_url"])

	// discord: mention_only should be moved to group_trigger
	discord := channelList["discord"].(map[string]any)
	require.Equal(t, "discord", discord["type"])
	require.Contains(t, discord, "group_trigger", "mention_only should be migrated to group_trigger")
	gt := discord["group_trigger"].(map[string]any)
	require.Equal(t, true, gt["mention_only"])
	discordSettings := discord["settings"].(map[string]any)
	require.Equal(t, "socks5://localhost:1080", discordSettings["proxy"])

	// onebot: group_trigger_prefix should be moved to group_trigger.prefixes
	onebot := channelList["onebot"].(map[string]any)
	require.Equal(t, "onebot", onebot["type"])
	obGroupTrigger := onebot["group_trigger"].(map[string]any)
	require.Equal(
		t,
		[]any{"/"},
		obGroupTrigger["prefixes"],
		"group_trigger_prefix should be moved to group_trigger.prefixes",
	)
	obSettings := onebot["settings"].(map[string]any)
	require.Equal(t, "ws://localhost:3001", obSettings["ws_url"])
}

// TestMigrateV1ToV3_ApiKeyConversion verifies api_key → api_keys conversion.
func TestMigrateV1ToV3_ApiKeyConversion(t *testing.T) {
	v1Config := `{
		"version": 1,
		"model_list": [
			{"model_name": "gpt-4", "model": "openai/gpt-4", "api_key": "sk-single"},
			{"model_name": "no-key", "model": "openai/no-key"}
		],
		"channels": {
			"telegram": {"token": "bot"}
		}
	}`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(v1Config), 0o600))
	m, err := loadConfigMap(configPath)
	require.NoError(t, err)

	err = migrateV1ToV2(m)
	require.NoError(t, err)
	err = migrateV2ToV3(m)
	require.NoError(t, err)
	err = migrateV3ToV4(m)
	require.NoError(t, err)

	// api_key should be converted to api_keys array
	modelList := m["model_list"].([]any)
	firstModel := modelList[0].(map[string]any)
	require.NotContains(t, firstModel, "api_key", "api_key should be removed")
	require.Contains(t, firstModel, "api_keys", "api_keys should exist")
	// api_keys can be []string or []any depending on how it was set
	if apiKeys, ok := firstModel["api_keys"].([]string); ok {
		require.Len(t, apiKeys, 1)
		require.Equal(t, "sk-single", apiKeys[0])
	} else if apiKeys, ok := firstModel["api_keys"].([]any); ok {
		require.Len(t, apiKeys, 1)
		require.Equal(t, "sk-single", apiKeys[0])
	} else {
		t.Fatalf("api_keys has unexpected type: %T", firstModel["api_keys"])
	}

	// Model without api_key should not have api_keys added
	secondModel := modelList[1].(map[string]any)
	require.NotContains(t, secondModel, "api_key")
	require.NotContains(t, secondModel, "api_keys")
}

// TestMigrateV1ToV3_AlreadyNestedFormat leaves already-nested channels unchanged.
func TestMigrateV1ToV3_AlreadyNestedFormat(t *testing.T) {
	v1Config := `{
		"version": 1,
		"model_list": [
			{"model_name": "gpt-4", "model": "openai/gpt-4"}
		],
		"channels": {
			"telegram": {
				"type": "telegram",
				"settings": {
					"token": "bot-token"
				}
			}
		}
	}`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(v1Config), 0o600))
	m, err := loadConfigMap(configPath)
	require.NoError(t, err)

	err = migrateV1ToV2(m)
	require.NoError(t, err)
	err = migrateV2ToV3(m)
	require.NoError(t, err)
	err = migrateV3ToV4(m)
	require.NoError(t, err)

	channelList := m["channel_list"].(map[string]any)
	telegram := channelList["telegram"].(map[string]any)
	// Should not be double-wrapped
	require.Equal(t, "telegram", telegram["type"])
	settings := telegram["settings"].(map[string]any)
	require.Equal(t, "bot-token", settings["token"])
	// Should NOT have nested settings inside settings
	require.NotContains(t, settings, "settings")
}

// TestMigrateV3ToV4 verifies V3 → V4 migration bumps version without touching live_config.
func TestMigrateV3ToV4(t *testing.T) {
	m := map[string]any{
		"version": 3,
		"gateway": map[string]any{"host": "localhost", "port": float64(18790)},
	}

	err := migrateV3ToV4(m)
	require.NoError(t, err)

	require.Equal(t, 4, m["version"])
	// No live_config block should be injected by the migration
	_, hasLC := m["live_config"]
	require.False(t, hasLC, "migrateV3ToV4 must not inject a default live_config block")
}

// TestMigrateV3ToV4_RejectsRuntimeState verifies that configs with the legacy
// runtime_state key are rejected instead of silently migrated.
func TestMigrateV3ToV4_RejectsRuntimeState(t *testing.T) {
	m := map[string]any{
		"version": 3,
		"runtime_state": map[string]any{
			"driver": "turso",
		},
	}

	err := migrateV3ToV4(m)
	require.Error(t, err, "runtime_state should cause migrateV3ToV4 to return an error")
	require.Contains(t, err.Error(), "runtime_state")
	require.Contains(t, err.Error(), "live_config")
}

// TestMigrateV3ToV4_PreservesExistingLiveConfig leaves an existing live_config unchanged.
func TestMigrateV3ToV4_PreservesExistingLiveConfig(t *testing.T) {
	m := map[string]any{
		"version": 3,
		"live_config": map[string]any{
			"enabled":   true,
			"record_id": "rec-123",
		},
	}

	err := migrateV3ToV4(m)
	require.NoError(t, err)

	require.Equal(t, 4, m["version"])

	lc, ok := m["live_config"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, lc["enabled"])
	require.Equal(t, "rec-123", lc["record_id"])
}

// TestMigrateV3ToV4_WrongVersion returns an error for non-V3 input.
func TestMigrateV3ToV4_WrongVersion(t *testing.T) {
	m := map[string]any{"version": 2}
	err := migrateV3ToV4(m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected version 3")
}

// TestLoadConfig_V3MigratestoV4 verifies that a V3 config file is automatically
// upgraded to V4 on load.
func TestLoadConfig_V3MigratestoV4(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	v3Config := `{
		"version": 3,
		"gateway": {"host": "127.0.0.1", "port": 18790},
		"model_list": [
			{"model_name": "gpt-5.4", "provider": "openai", "model": "gpt-5.4", "api_base": "https://api.openai.com/v1", "enabled": true}
		]
	}`

	require.NoError(t, os.WriteFile(configPath, []byte(v3Config), 0o600))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, cfg.Version, "V3 config should be migrated to CurrentVersion")
	require.False(t, cfg.LiveConfig.Enabled, "live_config.enabled should be false when not configured")
}

// TestLoadConfig_V3WithRuntimeState_Fails verifies that a V3 config with the
// legacy runtime_state key fails to load rather than silently migrating.
func TestLoadConfig_V3WithRuntimeState_Fails(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	v3Config := `{
		"version": 3,
		"gateway": {"host": "127.0.0.1", "port": 18790},
		"runtime_state": {"driver": "turso", "record_id": "old-rec"}
	}`

	require.NoError(t, os.WriteFile(configPath, []byte(v3Config), 0o600))

	_, err := LoadConfig(configPath)
	require.Error(t, err, "LoadConfig should fail when V3 config has runtime_state")
	require.Contains(t, err.Error(), "runtime_state")
}
