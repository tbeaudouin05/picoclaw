package config

import "encoding/json"

type RuntimeSchoolConfig struct {
	Enabled             bool         `json:"enabled,omitempty" yaml:"-"`
	ConfigID            string       `json:"config_id,omitempty" yaml:"-"`
	TursoURL            string       `json:"turso_url,omitempty" yaml:"-"`
	TursoAuthToken      SecureString `json:"-" yaml:"turso_auth_token,omitempty"`
	InjectChannels      []string     `json:"inject_channels,omitempty" yaml:"-"`
	AdminUpdateChannels []string     `json:"admin_update_channels,omitempty" yaml:"-"`
	AdminUpdatesEnabled bool         `json:"admin_updates_enabled,omitempty" yaml:"-"`
}

func (c RuntimeSchoolConfig) MarshalJSON() ([]byte, error) {
	type publicRuntimeSchoolConfig struct {
		Enabled             bool     `json:"enabled,omitempty"`
		ConfigID            string   `json:"config_id,omitempty"`
		TursoURL            string   `json:"turso_url,omitempty"`
		InjectChannels      []string `json:"inject_channels,omitempty"`
		AdminUpdateChannels []string `json:"admin_update_channels,omitempty"`
		AdminUpdatesEnabled bool     `json:"admin_updates_enabled,omitempty"`
	}
	return json.Marshal(publicRuntimeSchoolConfig{
		Enabled:             c.Enabled,
		ConfigID:            c.ConfigID,
		TursoURL:            c.TursoURL,
		InjectChannels:      c.InjectChannels,
		AdminUpdateChannels: c.AdminUpdateChannels,
		AdminUpdatesEnabled: c.AdminUpdatesEnabled,
	})
}
