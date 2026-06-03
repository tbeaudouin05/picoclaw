package school

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
}

func NewTursoHTTPStore(databaseURL, authToken string) (*TursoHTTPStore, error) {
	databaseURL = strings.TrimSpace(databaseURL)
	authToken = strings.TrimSpace(authToken)
	if databaseURL == "" {
		return nil, fmt.Errorf("turso database URL is required")
	}
	if authToken == "" {
		return nil, fmt.Errorf("turso auth token is required")
	}
	httpURL, err := tursoHTTPBaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	return &TursoHTTPStore{
		endpoint: strings.TrimRight(httpURL, "/") + "/v2/pipeline",
		token:    authToken,
		client:   &http.Client{Timeout: 15 * time.Second},
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
	_, err := s.execute(ctx, "CREATE TABLE IF NOT EXISTS school_config (id TEXT PRIMARY KEY, config_version INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')), config_json TEXT NOT NULL)")
	return err
}

func (s *TursoHTTPStore) GetConfig(ctx context.Context, id string) (*Config, error) {
	id = cleanID(id)
	res, err := s.execute(ctx, "SELECT id, config_version, updated_at, config_json FROM school_config WHERE id = ?", textArg(id))
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, NotFoundError{ID: id}
	}
	row := res.Rows[0]
	if len(row) < 4 {
		return nil, fmt.Errorf("school_config row has %d columns, want 4", len(row))
	}
	version, err := row[1].Int64()
	if err != nil {
		return nil, fmt.Errorf("decode config_version: %w", err)
	}
	return &Config{
		ID:            row[0].String(),
		ConfigVersion: version,
		UpdatedAt:     row[2].String(),
		ConfigJSON:    json.RawMessage(row[3].String()),
	}, nil
}

func (s *TursoHTTPStore) UpdateConfig(ctx context.Context, id string, expectedVersion int64, configJSON json.RawMessage) (*Config, error) {
	id = cleanID(id)
	configJSON = json.RawMessage(strings.TrimSpace(string(configJSON)))
	if len(configJSON) == 0 || !json.Valid(configJSON) {
		return nil, fmt.Errorf("config_json must be valid JSON")
	}
	res, err := s.execute(ctx, "UPDATE school_config SET config_version = config_version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'), config_json = ? WHERE id = ? AND config_version = ?", textArg(string(configJSON)), textArg(id), intArg(expectedVersion))
	if err != nil {
		return nil, err
	}
	if res.AffectedRowCount == 0 {
		return nil, VersionConflictError{ID: id, Expected: expectedVersion}
	}
	return s.GetConfig(ctx, id)
}

func (s *TursoHTTPStore) UpsertInitialConfig(ctx context.Context, id string, configJSON json.RawMessage) (*Config, error) {
	id = cleanID(id)
	if len(configJSON) == 0 || !json.Valid(configJSON) {
		return nil, fmt.Errorf("config_json must be valid JSON")
	}
	_, err := s.execute(ctx, "INSERT INTO school_config (id, config_version, updated_at, config_json) VALUES (?, 1, strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?) ON CONFLICT(id) DO NOTHING", textArg(id), textArg(string(configJSON)))
	if err != nil {
		return nil, err
	}
	return s.GetConfig(ctx, id)
}

func cleanID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return MainConfigID
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
	Type   string `json:"type"`
	Value  string `json:"value"`
	Base64 string `json:"base64"`
}

func (v sqlValue) String() string {
	if v.Type == "null" {
		return ""
	}
	return v.Value
}
func (v sqlValue) Int64() (int64, error) { return strconv.ParseInt(v.Value, 10, 64) }

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
	reqBody := pipelineRequest{Requests: []pipelineOp{{Type: "execute", Stmt: &stmt{SQL: sql, Args: args}}, {Type: "close"}}}
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
