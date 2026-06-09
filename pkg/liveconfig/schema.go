package liveconfig

import (
	"fmt"
	"regexp"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Schema defines the storage shape for a live-config driver. Only the default
// schema is surfaced in config today, but keeping SQL generation behind this
// struct makes future table/column configurability a small driver change rather
// than a source-wide refactor.
type Schema struct {
	Table         string
	IDColumn      string
	VersionColumn string
	UpdatedColumn string
	PayloadColumn string
}

func DefaultSchema() Schema {
	return Schema{
		Table:         "live_config",
		IDColumn:      "id",
		VersionColumn: "config_version",
		UpdatedColumn: "updated_at",
		PayloadColumn: "config_json",
	}
}

var sqlIdentifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s Schema) normalized() (Schema, error) {
	defaults := DefaultSchema()
	if s.Table == "" {
		s.Table = defaults.Table
	}
	if s.IDColumn == "" {
		s.IDColumn = defaults.IDColumn
	}
	if s.VersionColumn == "" {
		s.VersionColumn = defaults.VersionColumn
	}
	if s.UpdatedColumn == "" {
		s.UpdatedColumn = defaults.UpdatedColumn
	}
	if s.PayloadColumn == "" {
		s.PayloadColumn = defaults.PayloadColumn
	}
	for label, value := range map[string]string{
		"table":          s.Table,
		"id column":      s.IDColumn,
		"version column": s.VersionColumn,
		"updated column": s.UpdatedColumn,
		"payload column": s.PayloadColumn,
	} {
		if !sqlIdentifierRE.MatchString(value) {
			return Schema{}, fmt.Errorf("invalid live config %s identifier %q", label, value)
		}
	}
	return s, nil
}

func SchemaFromConfig(cfg config.LiveConfigSchema) Schema {
	return Schema{
		Table:         cfg.Table,
		IDColumn:      cfg.IDColumn,
		VersionColumn: cfg.VersionColumn,
		UpdatedColumn: cfg.UpdatedColumn,
		PayloadColumn: cfg.PayloadColumn,
	}
}
