package liveconfig

import "testing"

func TestDefaultSchemaUsesLiveConfigTable(t *testing.T) {
	s := DefaultSchema()
	if s.Table != "live_config" {
		t.Fatalf("table = %q, want live_config", s.Table)
	}
	if s.IDColumn == "" || s.VersionColumn == "" || s.UpdatedColumn == "" || s.PayloadColumn == "" {
		t.Fatalf("default schema has empty columns: %+v", s)
	}
}

func TestSchemaNormalizedRejectsUnsafeIdentifiers(t *testing.T) {
	cases := map[string]Schema{
		"table":   {Table: "live_config; drop table users"},
		"id":      {IDColumn: "id; drop table users"},
		"version": {VersionColumn: "config_version; drop table users"},
		"updated": {UpdatedColumn: "updated_at; drop table users"},
		"payload": {PayloadColumn: "config_json; drop table users"},
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := schema.normalized(); err == nil {
				t.Fatal("expected unsafe identifier error")
			}
		})
	}
}
