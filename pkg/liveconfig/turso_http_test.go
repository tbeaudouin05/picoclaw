package liveconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTursoHTTPStore_UpdateRecord_UsesIntegerUpdatedAtAndDecodesIntegerVersion
// models the admin/injected-turn update_live_config save path: LiveConfigUpdateTool
// calls UpdateRecord, which writes updated_at/config_json and then fetches the
// updated row via GetRecord. Mist's live_config/runtime_config tables store
// updated_at and config_version as INTEGER columns, so the UPDATE must bind
// updated_at as an integer. The Turso/libsql server may also return INTEGER
// column values as raw JSON numbers (42) rather than the spec-canonical string
// form ("42"). This test fails before the fix because UpdateRecord writes a
// text timestamp to updated_at and sqlValue.Value cannot decode numeric JSON.
func TestTursoHTTPStore_UpdateRecord_UsesIntegerUpdatedAtAndDecodesIntegerVersion(t *testing.T) {
	const (
		recordID      = "main"
		expectedAfter = int64(6)
	)

	callN := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callN++
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			var req pipelineRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(req.Requests) == 0 || req.Requests[0].Stmt == nil || len(req.Requests[0].Stmt.Args) != 4 {
				t.Fatalf("unexpected update request: %#v", req)
			}
			args := req.Requests[0].Stmt.Args
			if args[0].Type != "integer" {
				t.Fatalf("updated_at arg type = %q, want integer; full args=%#v", args[0].Type, args)
			}
			if args[1].Type != "text" || args[2].Type != "text" || args[3].Type != "integer" {
				t.Fatalf("unexpected update arg types: %#v", args)
			}
			// UPDATE response: affected_row_count=1, no rows.
			fmt.Fprint(w, `{"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[],"rows":[],"affected_row_count":1}}}]}`)
		default:
			// SELECT response (GetRecord): config_version and updated_at returned as
			// raw JSON numbers, not strings.
			fmt.Fprint(w, `{"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[{"name":"id"},{"name":"config_version"},{"name":"updated_at"},{"name":"config_json"}],"rows":[[{"type":"text","value":"main"},{"type":"integer","value":6},{"type":"integer","value":1781705705369},{"type":"text","value":"{}"}]],"affected_row_count":0}}}]}`)
		}
	}))
	defer srv.Close()

	store, err := NewTursoHTTPStore(srv.URL, "test-token", DefaultSchema())
	if err != nil {
		t.Fatalf("NewTursoHTTPStore: %v", err)
	}

	rec, err := store.UpdateRecord(context.Background(), recordID, 5, json.RawMessage(`{"tone":"friendly"}`))
	if err != nil {
		t.Fatalf("UpdateRecord failed (datatype mismatch): %v", err)
	}
	if rec.ConfigVersion != expectedAfter {
		t.Fatalf("ConfigVersion = %d, want %d", rec.ConfigVersion, expectedAfter)
	}
	if rec.ID != recordID {
		t.Fatalf("ID = %q, want %q", rec.ID, recordID)
	}
	if rec.UpdatedAt != "1781705705369" {
		t.Fatalf("UpdatedAt = %q, want raw integer string", rec.UpdatedAt)
	}
}

// TestTursoHTTPStore_GetRecord_DecodesIntegerVersionAsString verifies the
// canonical string form still works correctly after the fix.
func TestTursoHTTPStore_GetRecord_DecodesIntegerVersionAsString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Canonical hrana-2 format: integer value as JSON string.
		fmt.Fprint(w, `{"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[{"name":"id"},{"name":"config_version"},{"name":"updated_at"},{"name":"config_json"}],"rows":[[{"type":"text","value":"main"},{"type":"integer","value":"3"},{"type":"integer","value":"1781705705369"},{"type":"text","value":"{\"k\":\"v\"}"}]],"affected_row_count":0}}}]}`)
	}))
	defer srv.Close()

	store, err := NewTursoHTTPStore(srv.URL, "test-token", DefaultSchema())
	if err != nil {
		t.Fatalf("NewTursoHTTPStore: %v", err)
	}

	rec, err := store.GetRecord(context.Background(), "main")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if rec.ConfigVersion != 3 {
		t.Fatalf("ConfigVersion = %d, want 3", rec.ConfigVersion)
	}
	if rec.ID != "main" {
		t.Fatalf("ID = %q, want main", rec.ID)
	}
	if rec.UpdatedAt != "1781705705369" {
		t.Fatalf("UpdatedAt = %q, want raw integer string", rec.UpdatedAt)
	}
	var cfg map[string]any
	if err := json.Unmarshal(rec.ConfigJSON, &cfg); err != nil || cfg["k"] != "v" {
		t.Fatalf("ConfigJSON = %s (parse err=%v)", rec.ConfigJSON, err)
	}
}
