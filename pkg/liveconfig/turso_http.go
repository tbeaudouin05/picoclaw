package liveconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type TursoHTTPStore struct {
	endpoint string
	token    string
	client   *http.Client
	schema   Schema
}

func NewTursoHTTPStore(databaseURL, authToken string, schema Schema) (*TursoHTTPStore, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	authToken = strings.TrimSpace(authToken)
	if databaseURL == "" {
		return nil, fmt.Errorf("turso database URL is required")
	}
	if authToken == "" {
		return nil, fmt.Errorf("turso auth token is required")
	}
	normalizedSchema, err := schema.normalized()
	if err != nil {
		return nil, err
	}
	httpURL, err := tursoHTTPBaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	return &TursoHTTPStore{
		endpoint: strings.TrimRight(httpURL, "/") + "/v2/pipeline",
		token:    authToken,
		client:   &http.Client{Timeout: 15 * time.Second},
		schema:   normalizedSchema,
	}, nil
}

func tursoHTTPBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "libsql":
		u.Scheme = "https"
	case "https", "http":
	default:
		return "", fmt.Errorf("unsupported Turso URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(u.Path, "/v2/pipeline") {
		u.Path = strings.TrimSuffix(u.Path, "/v2/pipeline")
	}
	return u.String(), nil
}

func (s *TursoHTTPStore) Close() error { return nil }

func (s *TursoHTTPStore) InitSchema(ctx context.Context) error {
	sql := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s TEXT PRIMARY KEY, %s INTEGER NOT NULL DEFAULT 1, %s INTEGER NOT NULL DEFAULT 0, %s TEXT NOT NULL)",
		s.schema.Table,
		s.schema.IDColumn,
		s.schema.VersionColumn,
		s.schema.UpdatedColumn,
		s.schema.PayloadColumn,
	)
	_, err := s.execute(ctx, sql)
	return err
}

func (s *TursoHTTPStore) GetRecord(ctx context.Context, id string) (*Record, error) {
	id = cleanID(id)
	sql := fmt.Sprintf(
		"SELECT %s, %s, %s, %s FROM %s WHERE %s = ?",
		s.schema.IDColumn,
		s.schema.VersionColumn,
		s.schema.UpdatedColumn,
		s.schema.PayloadColumn,
		s.schema.Table,
		s.schema.IDColumn,
	)
	res, err := s.execute(ctx, sql, textArg(id))
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, NotFoundError{ID: id}
	}
	row := res.Rows[0]
	if len(row) < 4 {
		return nil, fmt.Errorf("%s row has %d columns, want 4", s.schema.Table, len(row))
	}
	version, err := row[1].Int64()
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", s.schema.VersionColumn, err)
	}
	return &Record{
		ID:            row[0].String(),
		ConfigVersion: version,
		UpdatedAt:     row[2].String(),
		ConfigJSON:    json.RawMessage(row[3].String()),
	}, nil
}

func (s *TursoHTTPStore) UpdateRecord(
	ctx context.Context,
	id string,
	expectedVersion int64,
	configJSON json.RawMessage,
) (*Record, error) {
	id = cleanID(id)
	configJSON = json.RawMessage(strings.TrimSpace(string(configJSON)))
	if len(configJSON) == 0 || !json.Valid(configJSON) {
		return nil, fmt.Errorf("config_json must be valid JSON")
	}
	sql := fmt.Sprintf(
		"UPDATE %s SET %s = %s + 1, %s = ?, %s = ? WHERE %s = ? AND %s = ?",
		s.schema.Table,
		s.schema.VersionColumn,
		s.schema.VersionColumn,
		s.schema.UpdatedColumn,
		s.schema.PayloadColumn,
		s.schema.IDColumn,
		s.schema.VersionColumn,
	)
	res, err := s.execute(
		ctx,
		sql,
		intArg(time.Now().UnixMilli()),
		textArg(string(configJSON)),
		textArg(id),
		intArg(expectedVersion),
	)
	if err != nil {
		return nil, err
	}
	if res.AffectedRowCount == 0 {
		return nil, VersionConflictError{ID: id, Expected: expectedVersion}
	}
	return s.GetRecord(ctx, id)
}

func (s *TursoHTTPStore) UpsertInitialRecord(
	ctx context.Context,
	id string,
	configJSON json.RawMessage,
) (*Record, error) {
	id = cleanID(id)
	if len(configJSON) == 0 || !json.Valid(configJSON) {
		return nil, fmt.Errorf("config_json must be valid JSON")
	}
	sql := fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s, %s) VALUES (?, 1, ?, ?) ON CONFLICT(%s) DO NOTHING",
		s.schema.Table,
		s.schema.IDColumn,
		s.schema.VersionColumn,
		s.schema.UpdatedColumn,
		s.schema.PayloadColumn,
		s.schema.IDColumn,
	)
	_, err := s.execute(ctx, sql, textArg(id), intArg(time.Now().UnixMilli()), textArg(string(configJSON)))
	if err != nil {
		return nil, err
	}
	return s.GetRecord(ctx, id)
}

func cleanID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return MainRecordID
	}
	return id
}

type sqlArg struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

func textArg(v string) sqlArg { return sqlArg{Type: "text", Value: v} }
func intArg(v int64) sqlArg   { return sqlArg{Type: "integer", Value: strconv.FormatInt(v, 10)} }

type executeResult struct {
	Cols             []any        `json:"cols"`
	Rows             [][]sqlValue `json:"rows"`
	AffectedRowCount int64        `json:"affected_row_count"`
}

type sqlValue struct {
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value"`
	Base64 string          `json:"base64"`
}

func (v sqlValue) String() string {
	if v.Type == "null" || len(v.Value) == 0 {
		return ""
	}
	// Turso returns text columns as a JSON-encoded string: "\"hello\"".
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	// Fallback for numeric or other raw JSON forms: return raw bytes as string.
	return string(v.Value)
}

func (v sqlValue) Int64() (int64, error) {
	if len(v.Value) == 0 {
		return 0, fmt.Errorf("empty integer value")
	}
	// Turso spec encodes integers as JSON strings ("42") to preserve precision,
	// but some server versions return them as raw JSON numbers (42). Handle both.
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return strconv.ParseInt(s, 10, 64)
	}
	var n int64
	if json.Unmarshal(v.Value, &n) == nil {
		return n, nil
	}
	return 0, fmt.Errorf("cannot parse integer from %s", v.Value)
}

type pipelineRequest struct {
	Requests []pipelineOp `json:"requests"`
}
type pipelineOp struct {
	Type string `json:"type"`
	Stmt *stmt  `json:"stmt,omitempty"`
}
type stmt struct {
	SQL  string   `json:"sql"`
	Args []sqlArg `json:"args,omitempty"`
}
type pipelineResponse struct {
	Results []pipelineResult `json:"results"`
}
type pipelineResult struct {
	Type     string                  `json:"type"`
	Error    *pipelineError          `json:"error,omitempty"`
	Response *pipelineResultResponse `json:"response,omitempty"`
}
type pipelineError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}
type pipelineResultResponse struct {
	Type   string        `json:"type"`
	Result executeResult `json:"result"`
}

func (s *TursoHTTPStore) execute(ctx context.Context, sql string, args ...sqlArg) (*executeResult, error) {
	reqBody := pipelineRequest{
		Requests: []pipelineOp{{Type: "execute", Stmt: &stmt{SQL: sql, Args: args}}, {Type: "close"}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("turso HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded pipelineResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Results) == 0 {
		return nil, fmt.Errorf("empty Turso pipeline response")
	}
	first := decoded.Results[0]
	if first.Type != "ok" {
		if first.Error != nil {
			return nil, fmt.Errorf("turso query error: %s", first.Error.Message)
		}
		return nil, fmt.Errorf("turso query result type %q", first.Type)
	}
	if first.Response == nil {
		return nil, fmt.Errorf("missing Turso execute response")
	}
	return &first.Response.Result, nil
}
