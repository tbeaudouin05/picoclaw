package config

import "encoding/json"

type RuntimeStateConfig struct {
	Enabled             bool         `json:"enabled,omitempty" yaml:"-"`
	StateID             string       `json:"state_id,omitempty" yaml:"-"`
	TursoURL            string       `json:"turso_url,omitempty" yaml:"-"`
	TursoAuthToken      SecureString `json:"-" yaml:"turso_auth_token,omitempty"`
	InjectChannels      []string     `json:"inject_channels,omitempty" yaml:"-"`
	AdminUpdateChannels []string     `json:"admin_update_channels,omitempty" yaml:"-"`
	AdminUpdatesEnabled bool         `json:"admin_updates_enabled,omitempty" yaml:"-"`
}

func (c RuntimeStateConfig) MarshalJSON() ([]byte, error) {
	type publicRuntimeStateConfig struct {
		Enabled             bool     `json:"enabled,omitempty"`
		StateID             string   `json:"state_id,omitempty"`
		TursoURL            string   `json:"turso_url,omitempty"`
		InjectChannels      []string `json:"inject_channels,omitempty"`
		AdminUpdateChannels []string `json:"admin_update_channels,omitempty"`
		AdminUpdatesEnabled bool     `json:"admin_updates_enabled,omitempty"`
	}
	return json.Marshal(publicRuntimeStateConfig{
		Enabled:             c.Enabled,
		StateID:             c.StateID,
		TursoURL:            c.TursoURL,
		InjectChannels:      c.InjectChannels,
		AdminUpdateChannels: c.AdminUpdateChannels,
		AdminUpdatesEnabled: c.AdminUpdatesEnabled,
	})
}
