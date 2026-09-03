package oauthprovider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCreateAntigravityTokenSourceRefreshesAndPersistsGoogleCredential(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(config.EnvOpenAIAuthFile, "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", r.Method, http.MethodPost)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
			return
		}
		if got := r.PostForm.Get("refresh_token"); got != "stored-refresh-token" {
			t.Errorf("refresh_token = %q, want %q", got, "stored-refresh-token")
		}
		if _, ok := r.PostForm["scope"]; ok {
			t.Error("refresh request unexpectedly included scope")
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "refreshed-access-token",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	originalConfigFactory := googleAntigravityOAuthConfig
	googleAntigravityOAuthConfig = func() auth.OAuthProviderConfig {
		return auth.OAuthProviderConfig{
			Issuer:       "https://accounts.google.com/o/oauth2/v2",
			TokenURL:     server.URL,
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			Scopes:       "scope that must not be sent during refresh",
		}
	}
	t.Cleanup(func() { googleAntigravityOAuthConfig = originalConfigFactory })

	if err := auth.SetCredential("google-antigravity", &auth.AuthCredential{
		AccessToken:  "expired-access-token",
		RefreshToken: "stored-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Minute),
		Provider:     "google-antigravity",
		AuthMethod:   "oauth",
		Email:        "person@example.com",
		ProjectID:    "stored-project",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	accessToken, projectID, err := createAntigravityTokenSource()()
	if err != nil {
		t.Fatalf("token source() error = %v", err)
	}
	if accessToken != "refreshed-access-token" {
		t.Errorf("access token = %q, want %q", accessToken, "refreshed-access-token")
	}
	if projectID != "stored-project" {
		t.Errorf("project ID = %q, want %q", projectID, "stored-project")
	}

	persisted, err := auth.GetCredential("google-antigravity")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if persisted == nil {
		t.Fatal("persisted credential is nil")
	}
	if persisted.AccessToken != "refreshed-access-token" {
		t.Errorf("persisted access token = %q, want %q", persisted.AccessToken, "refreshed-access-token")
	}
	if persisted.RefreshToken != "stored-refresh-token" {
		t.Errorf("persisted refresh token = %q, want %q", persisted.RefreshToken, "stored-refresh-token")
	}
	if persisted.Email != "person@example.com" {
		t.Errorf("persisted email = %q, want %q", persisted.Email, "person@example.com")
	}
	if persisted.ProjectID != "stored-project" {
		t.Errorf("persisted project ID = %q, want %q", persisted.ProjectID, "stored-project")
	}
	if !persisted.ExpiresAt.After(time.Now()) {
		t.Errorf("persisted expiry = %v, want a future time", persisted.ExpiresAt)
	}
}

func TestBuildRequestUsesFunctionFieldsWhenToolCallNameMissing(t *testing.T) {
	p := &AntigravityProvider{}

	messages := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "call_read_file_123",
				Function: &FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"README.md"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_read_file_123",
			Content:    "ok",
		},
	}

	req := p.buildRequest(messages, nil, "", nil)
	if len(req.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(req.Contents))
	}

	modelPart := req.Contents[0].Parts[0]
	if modelPart.FunctionCall == nil {
		t.Fatal("expected functionCall in assistant message")
	}
	if modelPart.FunctionCall.Name != "read_file" {
		t.Fatalf("expected functionCall name read_file, got %q", modelPart.FunctionCall.Name)
	}
	if got := modelPart.FunctionCall.Args["path"]; got != "README.md" {
		t.Fatalf("expected functionCall args[path] to be README.md, got %v", got)
	}

	toolPart := req.Contents[1].Parts[0]
	if toolPart.FunctionResponse == nil {
		t.Fatal("expected functionResponse in tool message")
	}
	if toolPart.FunctionResponse.Name != "read_file" {
		t.Fatalf("expected functionResponse name read_file, got %q", toolPart.FunctionResponse.Name)
	}
}

func TestParseSSEResponse_SplitsThoughtAndVisibleContent(t *testing.T) {
	p := &AntigravityProvider{}
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hidden reasoning\",\"thought\":true},{\"text\":\"visible answer\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":17,\"totalTokenCount\":216}}}\n" +
		"data: [DONE]\n"

	resp, err := p.parseSSEResponse(body)
	if err != nil {
		t.Fatalf("parseSSEResponse() error = %v", err)
	}

	if resp.Content != "visible answer" {
		t.Fatalf("Content = %q, want %q", resp.Content, "visible answer")
	}
	if resp.ReasoningContent != "hidden reasoning" {
		t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, "hidden reasoning")
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 216 {
		t.Fatalf("Usage.TotalTokens = %v, want %d", resp.Usage, 216)
	}
}

func TestBuildRequest_PreservesComplexToolSchemasByDefault(t *testing.T) {
	p := &AntigravityProvider{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"parent": map[string]any{
				"anyOf": []any{
					map[string]any{"$ref": "#/$defs/pageParent"},
					map[string]any{"$ref": "#/$defs/databaseParent"},
				},
			},
			"icon": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "null"},
					map[string]any{"$ref": "#/$defs/emoji"},
				},
			},
		},
		"$defs": map[string]any{
			"pageParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"page_id": map[string]any{"type": "string"},
				},
				"required": []any{"page_id"},
			},
			"databaseParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"database_id": map[string]any{"type": "string"},
				},
				"required": []any{"database_id"},
			},
			"emoji": map[string]any{
				"type":    "string",
				"pattern": "^:[a-z_]+:$",
			},
		},
	}

	req := p.buildRequest(
		[]Message{{Role: "user", Content: "hello"}},
		[]ToolDefinition{{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name:        "mcp_notion_create",
				Description: "Create a Notion object",
				Parameters:  schema,
			},
		}},
		"gemini-3-flash",
		nil,
	)

	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("request tools = %#v, want one function declaration", req.Tools)
	}

	got, ok := req.Tools[0].FunctionDeclarations[0].Parameters.(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want map", req.Tools[0].FunctionDeclarations[0].Parameters)
	}
	if got["$defs"] == nil {
		t.Fatalf("parameters = %#v, want raw schema with $defs preserved by default", got)
	}
}
