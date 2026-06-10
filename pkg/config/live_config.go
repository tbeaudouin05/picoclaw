package config

import "encoding/json"

// LiveConfig configures an authoritative runtime JSON record loaded from an
// external live-config store and injected into selected agent prompts.
type LiveConfig struct {
	Enabled              bool             `json:"enabled,omitempty" yaml:"-"`
	RecordID             string           `json:"record_id,omitempty" yaml:"record_id,omitempty"`
	Driver               LiveConfigDriver `json:"driver,omitempty" yaml:"driver,omitempty"`
	InjectChannels       []string         `json:"inject_channels,omitempty" yaml:"-"`
	AdminUpdateChannels  []string         `json:"admin_update_channels,omitempty" yaml:"-"`
	AdminUpdatesEnabled  bool             `json:"admin_updates_enabled,omitempty" yaml:"-"`
	ProtectedUpdatePaths []string         `json:"protected_update_paths,omitempty" yaml:"-"`
}

// LiveConfigDriver selects the concrete live-config storage backend. Only Turso
// is supported today, but this tagged shape leaves future drivers additive.
type LiveConfigDriver struct {
	Turso *LiveConfigTursoDriver `json:"turso,omitempty" yaml:"turso,omitempty"`
}

// LiveConfigTursoDriver configures the Turso HTTP live-config driver.
type LiveConfigTursoDriver struct {
	URL       string           `json:"url,omitempty" yaml:"-"`
	AuthToken SecureString     `json:"-" yaml:"auth_token,omitempty"`
	Schema    LiveConfigSchema `json:"schema,omitempty" yaml:"-"`
}

// LiveConfigSchema describes the generic record table shape. These fields are
// optional and default to the canonical live_config/id/config_version/updated_at/
// config_json shape, but exposing them now keeps future storage variation local.
type LiveConfigSchema struct {
	Table         string `json:"table,omitempty" yaml:"-"`
	IDColumn      string `json:"id_column,omitempty" yaml:"-"`
	VersionColumn string `json:"version_column,omitempty" yaml:"-"`
	UpdatedColumn string `json:"updated_column,omitempty" yaml:"-"`
	PayloadColumn string `json:"payload_column,omitempty" yaml:"-"`
}

func (c LiveConfig) MarshalJSON() ([]byte, error) {
	type publicTurso struct {
		URL    string           `json:"url,omitempty"`
		Schema LiveConfigSchema `json:"schema,omitempty"`
	}
	type publicDriver struct {
		Turso *publicTurso `json:"turso,omitempty"`
	}
	type publicLiveConfig struct {
		Enabled              bool          `json:"enabled,omitempty"`
		RecordID             string        `json:"record_id,omitempty"`
		Driver               *publicDriver `json:"driver,omitempty"`
		InjectChannels       []string      `json:"inject_channels,omitempty"`
		AdminUpdateChannels  []string      `json:"admin_update_channels,omitempty"`
		AdminUpdatesEnabled  bool          `json:"admin_updates_enabled,omitempty"`
		ProtectedUpdatePaths []string      `json:"protected_update_paths,omitempty"`
	}
	out := publicLiveConfig{
		Enabled:              c.Enabled,
		RecordID:             c.RecordID,
		InjectChannels:       c.InjectChannels,
		AdminUpdateChannels:  c.AdminUpdateChannels,
		AdminUpdatesEnabled:  c.AdminUpdatesEnabled,
		ProtectedUpdatePaths: c.ProtectedUpdatePaths,
	}
	if c.Driver.Turso != nil {
		out.Driver = &publicDriver{Turso: &publicTurso{URL: c.Driver.Turso.URL, Schema: c.Driver.Turso.Schema}}
	}
	return json.Marshal(out)
}

func (c LiveConfig) MarshalYAML() (any, error) {
	type secureTurso struct {
		AuthToken SecureString `yaml:"auth_token,omitempty"`
	}
	type secureDriver struct {
		Turso *secureTurso `yaml:"turso,omitempty"`
	}
	type secureLiveConfig struct {
		Driver *secureDriver `yaml:"driver,omitempty"`
	}
	out := secureLiveConfig{}
	if c.Driver.Turso != nil {
		out.Driver = &secureDriver{Turso: &secureTurso{AuthToken: c.Driver.Turso.AuthToken}}
	}
	return out, nil
}
