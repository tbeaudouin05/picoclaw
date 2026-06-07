package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLiveConfig_MarshalJSON_OmitsAuthToken verifies that the custom MarshalJSON
// strips the Turso auth_token so it never leaks into JSON output.
func TestLiveConfig_MarshalJSON_OmitsAuthToken(t *testing.T) {
	lc := LiveConfig{
		Enabled:  true,
		RecordID: "rec-123",
		Driver: LiveConfigDriver{
			Turso: &LiveConfigTursoDriver{
				URL:       "https://example.turso.io",
				AuthToken: *NewSecureString("secret-token"),
				Schema: LiveConfigSchema{
					Table:         "live_config",
					IDColumn:      "id",
					PayloadColumn: "config_json",
				},
			},
		},
		InjectChannels:      []string{"telegram"},
		AdminUpdateChannels: []string{"admin"},
		AdminUpdatesEnabled: true,
	}

	data, err := json.Marshal(lc)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))

	// auth_token must never appear in JSON
	raw := string(data)
	require.NotContains(t, raw, "secret-token", "auth_token must not appear in JSON output")
	require.NotContains(t, raw, "auth_token", "auth_token key must not appear in JSON output")

	// Other fields must be present
	require.Equal(t, true, m["enabled"])
	require.Equal(t, "rec-123", m["record_id"])
	require.Equal(t, []any{"telegram"}, m["inject_channels"])
	require.Equal(t, []any{"admin"}, m["admin_update_channels"])
	require.Equal(t, true, m["admin_updates_enabled"])

	driver, ok := m["driver"].(map[string]any)
	require.True(t, ok)
	turso, ok := driver["turso"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "https://example.turso.io", turso["url"])

	schema, ok := turso["schema"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "live_config", schema["table"])
	require.Equal(t, "id", schema["id_column"])
	require.Equal(t, "config_json", schema["payload_column"])
}

// TestLiveConfig_MarshalJSON_NoDriver verifies JSON output when no driver is set.
func TestLiveConfig_MarshalJSON_NoDriver(t *testing.T) {
	lc := LiveConfig{Enabled: false}
	data, err := json.Marshal(lc)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	_, hasDriver := m["driver"]
	require.False(t, hasDriver, "driver should be omitted when nil")
}

// TestLiveConfig_JSONRoundTrip_PublicFields verifies public (non-secret) fields survive a round-trip.
func TestLiveConfig_JSONRoundTrip_PublicFields(t *testing.T) {
	lc := LiveConfig{
		Enabled:  true,
		RecordID: "rec-456",
		Driver: LiveConfigDriver{
			Turso: &LiveConfigTursoDriver{
				URL: "https://db.turso.io",
				Schema: LiveConfigSchema{
					Table:         "cfg",
					IDColumn:      "id",
					VersionColumn: "ver",
					UpdatedColumn: "ts",
					PayloadColumn: "payload",
				},
			},
		},
	}

	data, err := json.Marshal(lc)
	require.NoError(t, err)

	var got LiveConfig
	require.NoError(t, json.Unmarshal(data, &got))

	require.True(t, got.Enabled)
	require.Equal(t, "rec-456", got.RecordID)
	require.NotNil(t, got.Driver.Turso)
	require.Equal(t, "https://db.turso.io", got.Driver.Turso.URL)
	require.Equal(t, "cfg", got.Driver.Turso.Schema.Table)
	require.Equal(t, "payload", got.Driver.Turso.Schema.PayloadColumn)
}

// TestConfig_LiveConfigField_Default verifies that DefaultConfig has live_config disabled.
func TestConfig_LiveConfigField_Default(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.LiveConfig.Enabled, "default config should have live_config disabled")
	require.Nil(t, cfg.LiveConfig.Driver.Turso, "default config should have no Turso driver")
}

// TestConfig_LiveConfig_JSONParse verifies that live_config parses correctly from a full config JSON.
func TestConfig_LiveConfig_JSONParse(t *testing.T) {
	raw := `{
		"version": 4,
		"live_config": {
			"enabled": true,
			"record_id": "my-record",
			"driver": {
				"turso": {
					"url": "https://example.turso.io",
					"schema": {"table": "live_config", "id_column": "id"}
				}
			},
			"inject_channels": ["telegram", "discord"],
			"admin_update_channels": ["admin"],
			"admin_updates_enabled": true
		},
		"gateway": {"host": "localhost", "port": 18790}
	}`
	cfg := DefaultConfig()
	require.NoError(t, json.Unmarshal([]byte(raw), cfg))

	lc := cfg.LiveConfig
	require.True(t, lc.Enabled)
	require.Equal(t, "my-record", lc.RecordID)
	require.NotNil(t, lc.Driver.Turso)
	require.Equal(t, "https://example.turso.io", lc.Driver.Turso.URL)
	require.Equal(t, "live_config", lc.Driver.Turso.Schema.Table)
	require.Equal(t, "id", lc.Driver.Turso.Schema.IDColumn)
	require.Equal(t, []string{"telegram", "discord"}, lc.InjectChannels)
	require.Equal(t, []string{"admin"}, lc.AdminUpdateChannels)
	require.True(t, lc.AdminUpdatesEnabled)
}
