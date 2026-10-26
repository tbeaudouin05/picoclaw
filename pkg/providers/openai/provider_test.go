package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestProviderChat_ResponsesRequestAndResponse(t *testing.T) {
	var requestBody map[string]any
	var requestPath string
	var authorization string
	var userAgent string
	var customHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authorization = r.Header.Get("Authorization")
		userAgent = r.Header.Get("User-Agent")
		customHeader = r.Header.Get("X-Test-Header")
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeResponse(t, w, map[string]any{
			"id":     "resp_1",
			"object": "response",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":              "reasoning",
					"id":                "rs_1",
					"summary":           []any{map[string]any{"type": "summary_text", "text": "plan"}},
					"encrypted_content": "opaque-1",
				},
				map[string]any{
					"type":    "message",
					"content": []any{map[string]any{"type": "output_text", "text": "answer"}},
				},
			},
			"usage": map[string]any{
				"input_tokens":  11,
				"output_tokens": 7,
				"total_tokens":  18,
			},
		})
	}))
	defer server.Close()

	p := NewProvider(
		"test-key",
		server.URL+"/v1",
		"",
		"PicoClaw/test",
		WithExtraBody(map[string]any{"metadata": map[string]any{"source": "test"}}),
		WithCustomHeaders(map[string]string{"X-Test-Header": "custom"}),
	)

	out, err := p.Chat(
		t.Context(),
		[]Message{{Role: "system", Content: "Be concise"}, {Role: "user", Content: "hello"}},
		[]ToolDefinition{{
			Type: "function",
			Function: protocoltypes.ToolFunctionDefinition{
				Name:        "lookup",
				Description: "Look something up",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		"gpt-5.4",
		map[string]any{
			"max_tokens":     321,
			"thinking_level": "high",
		},
	)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}
	if authorization != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", authorization)
	}
	if userAgent != "PicoClaw/test" {
		t.Fatalf("User-Agent = %q, want PicoClaw/test", userAgent)
	}
	if customHeader != "custom" {
		t.Fatalf("custom header = %q, want custom", customHeader)
	}
	if requestBody["model"] != "gpt-5.4" {
		t.Fatalf("model = %#v, want gpt-5.4", requestBody["model"])
	}
	if requestBody["store"] != false {
		t.Fatalf("store = %#v, want false", requestBody["store"])
	}
	if got := requestBody["max_output_tokens"]; got != float64(321) {
		t.Fatalf("max_output_tokens = %#v, want 321", got)
	}
	if _, ok := requestBody["max_tokens"]; ok {
		t.Fatal("Responses request should not use max_tokens")
	}
	include, ok := requestBody["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", requestBody["include"])
	}
	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning = %#v, want effort=high", requestBody["reasoning"])
	}
	if _, ok := requestBody["temperature"]; ok {
		t.Fatal("reasoning model request should omit temperature unless effort is none")
	}
	if _, ok := requestBody["tools"].([]any); !ok {
		t.Fatalf("tools = %#v, want array", requestBody["tools"])
	}
	if requestBody["metadata"] == nil {
		t.Fatal("extra body metadata was not preserved")
	}

	if out.Content != "answer" {
		t.Fatalf("Content = %q, want answer", out.Content)
	}
	if out.ReasoningContent != "plan" {
		t.Fatalf("ReasoningContent = %q, want plan", out.ReasoningContent)
	}
	if out.ReasoningID != "rs_1" || out.ReasoningEncryptedContent != "opaque-1" {
		t.Fatalf("reasoning replay fields = (%q, %q), want (rs_1, opaque-1)", out.ReasoningID, out.ReasoningEncryptedContent)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 11 || out.Usage.CompletionTokens != 7 || out.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want 11/7/18", out.Usage)
	}
}

func TestProviderChat_ReplaysReasoningAndFunctionCallOutput(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, body)
		writeResponse(t, w, map[string]any{
			"id":     fmt.Sprintf("resp_%d", len(requests)),
			"object": "response",
			"status": "completed",
			"output": []any{map[string]any{
				"type":              "reasoning",
				"id":                "rs_round",
				"summary":           []any{map[string]any{"type": "summary_text", "text": "check"}},
				"encrypted_content": "encrypted-round",
			}},
		})
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "", "")
	first, err := p.Chat(t.Context(), []Message{{Role: "user", Content: "weather?"}}, nil, "gpt-5.4", nil)
	if err != nil {
		t.Fatalf("first Chat() error = %v", err)
	}
	_, err = p.Chat(
		t.Context(),
		[]Message{
			{Role: "user", Content: "weather?"},
			{
				Role:                      "assistant",
				Content:                   "I will check",
				ReasoningContent:          first.ReasoningContent,
				ReasoningID:               first.ReasoningID,
				ReasoningEncryptedContent: first.ReasoningEncryptedContent,
				ToolCalls: []protocoltypes.ToolCall{{
					ID:        "call_weather",
					Name:      "get_weather",
					Arguments: map[string]any{"city": "Seattle"},
				}},
			},
			{Role: "tool", ToolCallID: "call_weather", Content: `{"temp":72}`},
		},
		nil,
		"gpt-5.4",
		nil,
	)
	if err != nil {
		t.Fatalf("second Chat() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}

	input, ok := requests[1]["input"].([]any)
	if !ok {
		t.Fatalf("second input = %#v, want array", requests[1]["input"])
	}
	if len(input) != 5 {
		t.Fatalf("second input length = %d, want 5", len(input))
	}
	if got := input[1].(map[string]any); got["type"] != "reasoning" || got["id"] != "rs_round" || got["encrypted_content"] != "encrypted-round" {
		t.Fatalf("reasoning replay item = %#v", got)
	}
	if got := input[3].(map[string]any); got["type"] != "function_call" || got["call_id"] != "call_weather" {
		t.Fatalf("function call item = %#v", got)
	}
	if got := input[4].(map[string]any); got["type"] != "function_call_output" || got["call_id"] != "call_weather" || got["output"] != `{"temp":72}` {
		t.Fatalf("function call output item = %#v", got)
	}
}

func TestProviderChat_RejectsFailedAndCancelledTerminalResponses(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		code    string
		message string
		want    []string
	}{
		{
			name:    "failed with error details",
			status:  "failed",
			code:    "server_error",
			message: "model execution failed",
			want:    []string{"failed", "server_error", "model execution failed"},
		},
		{
			name:   "cancelled",
			status: "cancelled",
			want:   []string{"cancelled"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := map[string]any{
					"id":     "resp_terminal",
					"object": "response",
					"status": tt.status,
					"output": []any{},
				}
				if tt.code != "" || tt.message != "" {
					response["error"] = map[string]any{
						"code":    tt.code,
						"message": tt.message,
					}
				}
				writeResponse(t, w, response)
			}))
			defer server.Close()

			_, err := NewProvider("key", server.URL, "", "").Chat(
				t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil,
			)
			if err == nil {
				t.Fatal("Chat() error = nil, want terminal response error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Chat() error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestProviderChatStream_ResponsesEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["stream"] != true {
			t.Fatalf("stream = %#v, want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n",
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n",
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"}}\n\n",
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"reasoning\",\"id\":\"rs_stream\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"think\"}],\"encrypted_content\":\"enc-stream\"},{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n",
		)
	}))
	defer server.Close()

	p := NewProvider("key", server.URL, "", "")
	var contents []string
	var reasonings []string
	var toolCalls []protocoltypes.ToolCall
	out, err := p.ChatStreamEvents(
		t.Context(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"gpt-5.4",
		nil,
		func(chunk StreamChunk) {
			if chunk.Content != "" {
				contents = append(contents, chunk.Content)
			}
			if chunk.ReasoningContent != "" {
				reasonings = append(reasonings, chunk.ReasoningContent)
			}
			toolCalls = append(toolCalls, chunk.ToolCalls...)
		},
	)
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if out.Content != "hello" || out.ReasoningContent != "think" {
		t.Fatalf("response = %+v, want hello/think", out)
	}
	if out.ReasoningEncryptedContent != "enc-stream" {
		t.Fatalf("encrypted reasoning = %q, want enc-stream", out.ReasoningEncryptedContent)
	}
	if len(contents) != 1 || contents[0] != "hello" {
		t.Fatalf("content chunks = %#v, want [hello]", contents)
	}
	if len(reasonings) != 1 || reasonings[0] != "think" {
		t.Fatalf("reasoning chunks = %#v, want [think]", reasonings)
	}
	if len(toolCalls) != 1 || toolCalls[0].ID != "call_1" || toolCalls[0].Name != "lookup" {
		t.Fatalf("tool chunks = %#v, want lookup call_1", toolCalls)
	}
}

func TestProviderBuildRequestBody_TemperatureCompatibility(t *testing.T) {
	p := NewProvider("key", "https://api.openai.com/v1", "", "")

	if body := p.buildRequestBody(nil, nil, "gpt-5.4", map[string]any{"temperature": 0.7}, false); body["temperature"] != nil {
		t.Fatalf("gpt-5.4 temperature = %#v, want omitted with default reasoning", body["temperature"])
	}
	if body := p.buildRequestBody(nil, nil, "gpt-5.4", map[string]any{
		"temperature":    0.7,
		"thinking_level": "off",
	}, false); body["temperature"] != 0.7 {
		t.Fatalf("gpt-5.4 temperature with thinking off = %#v, want 0.7", body["temperature"])
	}
	if body := p.buildRequestBody(nil, nil, "gpt-4o", map[string]any{"temperature": 0.7}, false); body["temperature"] != 0.7 {
		t.Fatalf("gpt-4o temperature = %#v, want 0.7", body["temperature"])
	}
}

func TestProviderChatStream_IncompleteResponseWithoutDoneIsReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
			"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_incomplete\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}]}}\n\n",
		)
	}))
	defer server.Close()

	out, err := NewProvider("key", server.URL, "", "").ChatStreamEvents(
		t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil, nil,
	)
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if out.Content != "partial" || out.FinishReason != "length" {
		t.Fatalf("response = %+v, want partial/length", out)
	}
}

func TestProviderChatStream_EOFWithoutTerminalResponseReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}))
	defer server.Close()

	_, err := NewProvider("key", server.URL, "", "").ChatStreamEvents(
		t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "terminal response") {
		t.Fatalf("ChatStreamEvents() error = %v, want missing terminal response error", err)
	}
}

func TestProviderChatStream_ErrorEventReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"error\",\"code\":\"rate_limit\",\"param\":\"model\",\"message\":\"slow down\"}\n\n")
	}))
	defer server.Close()

	_, err := NewProvider("key", server.URL, "", "").ChatStreamEvents(
		t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "rate_limit: slow down") {
		t.Fatalf("ChatStreamEvents() error = %v, want SSE error event", err)
	}
}

func TestProviderChatStream_RejectsFailedTerminalResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"object\":\"response\",\"status\":\"failed\",\"error\":{\"code\":\"invalid_prompt\",\"message\":\"prompt rejected\"},\"output\":[]}}\n\n")
	}))
	defer server.Close()

	_, err := NewProvider("key", server.URL, "", "").ChatStreamEvents(
		t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil, nil,
	)
	if err == nil {
		t.Fatal("ChatStreamEvents() error = nil, want terminal response error")
	}
	for _, want := range []string{"failed", "invalid_prompt", "prompt rejected"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ChatStreamEvents() error = %q, want %q", err, want)
		}
	}
}

func TestProviderChatStream_ContextCancellationReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := NewProvider("key", server.URL, "", "").ChatStreamEvents(
			ctx, []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil, nil,
		)
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream request did not reach the test server")
	}
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("ChatStreamEvents() error = nil, want context cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChatStreamEvents() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ChatStreamEvents() did not return after context cancellation")
	}
}

func TestProviderChat_HTTPErrorPreservesStatusClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer server.Close()

	_, err := NewProvider("key", server.URL, "", "").Chat(
		t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-5.4", nil,
	)
	if err == nil {
		t.Fatal("Chat() error = nil, want HTTP error")
	}
	var httpErr *common.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want common.HTTPError status 429", err)
	}
}

func writeResponse(t *testing.T, w http.ResponseWriter, response map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestProviderBuildRequestBody_GeneratedThinkingByModel(t *testing.T) {
	p := NewProvider("test-key", "https://api.openai.com/v1", "", "")
	for _, model := range []string{"gpt-4o", "gpt-4.1", "openai/gpt-4o", "gpt-5-chat-latest", "o1-mini", "o1-preview"} {
		for _, level := range []string{"off", "low", "medium", "high", "xhigh"} {
			t.Run(model+"/"+level, func(t *testing.T) {
				for _, stream := range []bool{false, true} {
					body := p.buildRequestBody(nil, nil, model, map[string]any{"thinking_level": level}, stream)
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					var wire map[string]any
					if err := json.Unmarshal(data, &wire); err != nil {
						t.Fatal(err)
					}
					if _, exists := wire["reasoning"]; exists {
						t.Fatalf("unexpected reasoning: %s", data)
					}
				}
			})
		}
	}
	for _, tc := range []struct{ model, level, want string }{
		{"o1", "off", "low"}, {"o3-mini", "off", "low"}, {"o4-mini", "off", "low"},
		{"gpt-5", "off", "minimal"}, {"gpt-5-mini-2025-08-07", "off", "minimal"},
		{"gpt-5.1", "off", "none"}, {"gpt-5.4", "off", "none"},
		{"gpt-5.1-codex", "off", "low"}, {"gpt-6-astra", "off", "low"},
		{"gpt-5-pro", "low", "high"}, {"gpt-5.2-pro", "off", "medium"},
		{"o3", "xhigh", "high"}, {"gpt-5.1", "xhigh", "high"},
		{"gpt-5.4", "medium", "medium"},
	} {
		t.Run(tc.model+"/"+tc.level, func(t *testing.T) {
			body := p.buildRequestBody(nil, nil, tc.model, map[string]any{"thinking_level": tc.level}, false)
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Reasoning struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
			}
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Reasoning.Effort != tc.want {
				t.Fatalf("body = %s, want effort %q", data, tc.want)
			}
		})
	}
}

func TestProviderBuildRequestBody_MergesMandatoryInclude(t *testing.T) {
	for _, tc := range []struct {
		name    string
		include any
		want    string
	}{
		{"additional", []any{"web_search_call.action.sources"}, `["web_search_call.action.sources","reasoning.encrypted_content"]`},
		{"empty", []any{}, `["reasoning.encrypted_content"]`},
		{"present", []any{"reasoning.encrypted_content", "web_search_call.action.sources"}, `["reasoning.encrypted_content","web_search_call.action.sources"]`},
		{"typed", []string{"web_search_call.action.sources"}, `["web_search_call.action.sources","reasoning.encrypted_content"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"include": tc.include, "store": true}
			before, _ := json.Marshal(extra)
			p := NewProvider("test-key", "https://api.openai.com/v1", "", "", WithExtraBody(extra))
			for _, stream := range []bool{false, true} {
				body := p.buildRequestBody(nil, nil, "gpt-5.4", nil, stream)
				got, err := json.Marshal(body["include"])
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.want {
					t.Fatalf("include = %s, want %s", got, tc.want)
				}
				if body["store"] != false {
					t.Fatalf("store = %v", body["store"])
				}
			}
			after, _ := json.Marshal(extra)
			if string(before) != string(after) {
				t.Fatalf("extra_body mutated: %s -> %s", before, after)
			}
		})
	}
}
