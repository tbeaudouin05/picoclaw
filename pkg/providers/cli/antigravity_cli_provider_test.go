package cliprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var _ LLMProvider = (*AntigravityCliProvider)(nil)

func createMockAntigravityCLI(t *testing.T, argsFile, printFile, cwdFile, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	if strings.HasPrefix(output, `{"status":`) {
		output = `{"event":"result","result":` + output + `}`
	}
	dir := t.TempDir()
	outputFile := filepath.Join(dir, "output.json")
	if err := os.WriteFile(outputFile, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "agy")
	contents := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > '%s'
cat > '%s'
pwd > '%s'
cat '%s'
`, argsFile, printFile, cwdFile, outputFile)
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func createMockAntigravityFailingCLI(t *testing.T, stdout, stderr string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	dir := t.TempDir()
	stdoutFile := filepath.Join(dir, "stdout")
	stderrFile := filepath.Join(dir, "stderr")
	if err := os.WriteFile(stdoutFile, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrFile, []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "agy")
	contents := fmt.Sprintf("#!/bin/sh\ncat %q\ncat %q >&2\nexit %d\n", stdoutFile, stderrFile, exitCode)
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func createMockAntigravityCLIWithStderr(t *testing.T, stdout, stderr string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	dir := t.TempDir()
	stdoutFile := filepath.Join(dir, "stdout")
	stderrFile := filepath.Join(dir, "stderr")
	if err := os.WriteFile(stdoutFile, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrFile, []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "agy")
	contents := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\ncat %q\ncat %q >&2\n", stdoutFile, stderrFile)
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func createMockAntigravityRepairCLI(t *testing.T, requestsFile string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	dir := t.TempDir()
	countFile := filepath.Join(dir, "count")
	script := filepath.Join(dir, "agy")
	contents := fmt.Sprintf(`#!/bin/sh
count=0
if [ -f %q ]; then count=$(cat %q); fi
count=$((count + 1))
printf '%%s' "$count" > %q
cat >> %q
if [ "$count" -eq 1 ]; then
  printf '%%s\n' '{"event":"result","result":{"status":"SUCCESS","response":""}}'
  printf 'command permission was required and denied in headless mode; access_token=repair-secret\n' >&2
else
  printf '%%s\n' '{"event":"result","result":{"status":"SUCCESS","response":"repaired"}}'
fi
`, countFile, countFile, countFile, requestsFile)
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestAntigravityCliChatUsesSafeScopedInvocationAndTextProtocol(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	argsFile := filepath.Join(stateDir, "args")
	printFile := filepath.Join(stateDir, "print")
	cwdFile := filepath.Join(stateDir, "cwd")
	output := `{"status":"SUCCESS","response":"{\"tool_calls\":[{\"id\":\"call_ok\",\"type\":\"function\",\"function\":{\"name\":\"cron\",\"arguments\":\"{\\\"action\\\":\\\"list\\\"}\"}},{\"id\":\"call_bad\",\"type\":\"function\",\"function\":{\"name\":\"terminal\",\"arguments\":\"{\\\"command\\\":\\\"touch nope\\\"}\"}}]}","usage":{"input_tokens":7,"output_tokens":5,"thinking_tokens":3,"cache_read_tokens":2,"total_tokens":17}}`

	p := NewAntigravityCliProvider(workspace)
	p.command = createMockAntigravityCLI(t, argsFile, printFile, cwdFile, output)
	resp, err := p.Chat(context.Background(), []Message{
		{Role: "system", Content: "System policy."},
		{Role: "user", Content: "List jobs."},
	}, []ToolDefinition{{
		Type: "function",
		Function: ToolFunctionDefinition{
			Name: "cron", Description: "Manage jobs", Parameters: map[string]any{"type": "object"},
		},
	}}, "gemini-test", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.FinishReason != "tool_calls" || len(resp.ToolCalls) != 2 || resp.ToolCalls[0].Name != "cron" {
		t.Fatalf("response = %#v, want advertised cron and corrective terminal calls", resp)
	}
	if resp.ToolCalls[0].NonExecutableReason != "" {
		t.Fatalf("advertised cron call marked non-executable: %#v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "call_bad" || resp.ToolCalls[1].Name != "terminal" ||
		resp.ToolCalls[1].NonExecutableReason == "" {
		t.Fatalf("native terminal call = %#v, want correlated corrective call", resp.ToolCalls[1])
	}
	if resp.Content != "" {
		t.Fatalf("Content = %q, want empty terminal tool call response", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 17 {
		t.Fatalf("Usage = %#v, want total 17", resp.Usage)
	}

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	wantArgs := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--print-timeout", "15m0s",
		"--sandbox",
		"--mode", "accept-edits",
		"--dangerously-skip-permissions",
		"--add-dir", workspace,
		"--model", "gemini-test",
	}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %q, want exact invocation %q", args, wantArgs)
	}
	if containsString(args, "--disable-slash-commands") {
		t.Fatalf("--disable-slash-commands must not be present: %q", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "List jobs.") || strings.HasPrefix(arg, "--print=") {
			t.Fatalf("prompt was passed in argv: %q", args)
		}
	}
	if !containsString(args, "--dangerously-skip-permissions") {
		t.Fatalf("missing expected flag --dangerously-skip-permissions: %q", args)
	}
	cwdBytes, _ := os.ReadFile(cwdFile)
	if got := strings.TrimSpace(string(cwdBytes)); got != workspace {
		t.Errorf("cwd = %q, want exact workspace %q", got, workspace)
	}
	promptBytes, err := os.ReadFile(printFile)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(promptBytes, &request); err != nil {
		t.Fatalf("stdin request is not JSON: %v", err)
	}
	if request.Event != "user" {
		t.Fatalf("stdin request event = %q, want user", request.Event)
	}
	prompt := request.Message.Content
	for _, want := range []string{
		"## System Instructions", "System policy.",
		"## Conversation", "User: List jobs.",
		"## Available Tools", "cron", "terminal JSON text protocol",
		"Do not use any Antigravity-native tools", "file, terminal, browser, search, or IDE tools",
		"All inspection and actions must use only advertised PicoClaw terminal-JSON tools",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestAntigravityCLIPrintTimeout(t *testing.T) {
	now := time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)
	p := NewAntigravityCliProvider("")
	tests := []struct {
		name string
		ctx  context.Context
		want time.Duration
	}{
		{name: "no deadline", ctx: context.Background(), want: 15 * time.Minute},
		{name: "short deadline", ctx: deadlineContext(t, now.Add(2*time.Minute)), want: 2 * time.Minute},
		{name: "sub-max deadline", ctx: deadlineContext(t, now.Add(90*time.Second)), want: 90 * time.Second},
		{name: "long deadline capped", ctx: deadlineContext(t, now.Add(time.Hour)), want: 15 * time.Minute},
		{name: "expired deadline", ctx: deadlineContext(t, now.Add(-time.Second)), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := p.argsAt(tt.ctx, now, "")
			for i := range args {
				if args[i] == "--print-timeout" && i+1 < len(args) {
					if got := args[i+1]; got != tt.want.String() {
						t.Fatalf("--print-timeout = %q, want %q", got, tt.want)
					}
					return
				}
			}
			t.Fatal("args missing --print-timeout")
		})
	}
}

func deadlineContext(t *testing.T, deadline time.Time) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	return ctx
}

func TestAntigravityCliChatRetriesHeadlessNativeToolPermissionDenialOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	requestsFile := filepath.Join(t.TempDir(), "requests")
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityRepairCLI(t, requestsFile)

	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "inspect this"}}, []ToolDefinition{{
		Type: "function", Function: ToolFunctionDefinition{Name: "terminal"},
	}}, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "repaired" {
		t.Fatalf("response = %#v, want repaired response", resp)
	}
	requests, err := os.ReadFile(requestsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(requests), "\n"); got != 2 {
		t.Fatalf("request count = %d, want one guarded retry", got)
	}
	if strings.Contains(string(requests), "repair-secret") {
		t.Fatal("retry prompt leaked raw stderr")
	}
	if got := strings.Count(string(requests), antigravityCLINativeToolRepairInstruction); got != 1 {
		t.Fatalf("repair instruction count = %d, want exactly one", got)
	}
}

func TestAntigravityCliHeadlessNativeToolPermissionDenialClassifiesAfterRetry(t *testing.T) {
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLIWithStderr(t,
		`{"event":"result","result":{"status":"SUCCESS","response":""}}`+"\n",
		"command permission was required and denied in headless mode\n")

	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "inspect this"}}, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "native_tool_permission_denied") {
		t.Fatalf("Chat() error = %v, want native-tool permission classification marker", err)
	}
}

func TestAntigravityCliParseResponseTerminalToolCallProtocol(t *testing.T) {
	call := `{"tool_calls":[{"id":"call_ok","type":"function","function":{"name":"cron","arguments":"{\"action\":\"list\"}"}}]}`
	tests := []struct {
		name        string
		response    string
		wantValid   bool
		wantInvalid bool
	}{
		{name: "bare valid call", response: call, wantValid: true},
		{name: "leading prose plus trailing call", response: "I will list the jobs.\n" + call, wantValid: true},
		{name: "trailing prose rejection", response: call + "\nDone.", wantInvalid: true},
		{name: "fenced rejection", response: "```json\n" + call + "\n```", wantInvalid: true},
		{name: "inline code rejection", response: "Use `" + call + "`", wantInvalid: true},
		{name: "malformed rejection", response: `{"tool_calls":[}`, wantInvalid: true},
		{name: "malformed function arguments rejection", response: `{"tool_calls":[{"id":"call_bad","type":"function","function":{"name":"cron","arguments":"{not-json}"}}]}`, wantInvalid: true},
		{name: "two candidate rejection", response: call + "\n" + call, wantInvalid: true},
	}

	p := NewAntigravityCliProvider("")
	tools := []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{Name: "cron"}}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := p.parseResponse(fmt.Sprintf(`{"status":"SUCCESS","response":%q}`, tt.response), tools)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("ToolCalls = %#v, want one call", resp.ToolCalls)
			}
			if tt.wantValid {
				if resp.FinishReason != "tool_calls" || resp.Content != "" || resp.ToolCalls[0].Name != "cron" {
					t.Fatalf("response = %#v, want terminal cron call with leading prose suppressed", resp)
				}
				return
			}
			if !tt.wantInvalid || resp.FinishReason != "tool_calls" || resp.Content != "" ||
				resp.ToolCalls[0].NonExecutableReason != invalidTextProtocolToolCallReason {
				t.Fatalf("response = %#v, want synthetic protocol feedback call", resp)
			}
		})
	}
}

func TestAntigravityCliParseResponseOrdinaryJSONMentioningToolCallsIsText(t *testing.T) {
	p := NewAntigravityCliProvider("")
	result := `{"topic":"tool_calls","enabled":true}`
	resp, err := p.parseResponse(fmt.Sprintf(`{"status":"SUCCESS","response":%q}`, result), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != result || resp.FinishReason != "stop" || len(resp.ToolCalls) != 0 {
		t.Fatalf("response = %#v, want ordinary JSON preserved as text", resp)
	}
}

func TestAntigravityCliParseResponseReturnsExactUnknownTerminalCall(t *testing.T) {
	const result = `{"tool_calls":[{"id":"call_missing","type":"function","function":{"name":"missing_tool","arguments":"{\"path\":\"notes.txt\",\"limit\":3}"}}]}`
	p := NewAntigravityCliProvider("")
	resp, err := p.parseResponse(fmt.Sprintf(`{"status":"SUCCESS","response":%q}`, result), []ToolDefinition{{
		Type: "function", Function: ToolFunctionDefinition{Name: "cron"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "tool_calls" || resp.Content != "" || len(resp.ToolCalls) != 1 {
		t.Fatalf("response = %#v, want one terminal tool call", resp)
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_missing" || call.Name != "missing_tool" {
		t.Fatalf("tool call = %#v, want preserved ID and name", call)
	}
	if call.NonExecutableReason != `requested tool "missing_tool" is not available for this request; use only advertised PicoClaw tools` {
		t.Fatalf("NonExecutableReason = %q", call.NonExecutableReason)
	}
	if call.Function == nil || call.Function.Arguments != `{"path":"notes.txt","limit":3}` {
		t.Fatalf("tool call function = %#v, want preserved arguments", call.Function)
	}
	if call.Arguments["path"] != "notes.txt" || call.Arguments["limit"] != float64(3) {
		t.Fatalf("tool call arguments = %#v, want decoded arguments", call.Arguments)
	}
}

func TestAntigravityCliChatMarksNativeTerminalCallNonExecutable(t *testing.T) {
	stateDir := t.TempDir()
	p := NewAntigravityCliProvider(t.TempDir())
	p.command = createMockAntigravityCLI(t,
		filepath.Join(stateDir, "args"), filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"),
		`{"status":"SUCCESS","response":"{\"tool_calls\":[{\"id\":\"native\",\"type\":\"function\",\"function\":{\"name\":\"terminal\",\"arguments\":\"{}\"}}]}"}`)

	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, []ToolDefinition{{
		Type: "function", Function: ToolFunctionDefinition{Name: "cron"},
	}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "tool_calls" || len(resp.ToolCalls) != 1 {
		t.Fatalf("response = %#v, want one corrective-feedback call", resp)
	}
	if resp.ToolCalls[0].ID != "native" || resp.ToolCalls[0].NonExecutableReason == "" {
		t.Fatalf("tool call = %#v, want correlated non-executable call", resp.ToolCalls[0])
	}
}

func TestAntigravityCliChatRejectsTerminalError(t *testing.T) {
	p := NewAntigravityCliProvider("")
	_, err := p.parseResponse(`{"status":"ERROR","response":"request failed","error":"model unavailable"}`, nil)
	if err == nil || !strings.Contains(err.Error(), "ERROR") || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("parseResponse() error = %v, want useful terminal error", err)
	}
}

func TestAntigravityCliChatRejectsSuccessfulEmptyResponse(t *testing.T) {
	p := NewAntigravityCliProvider("")
	_, err := p.parseResponse(`{"status":"SUCCESS","response":" \t\n "}`, nil)
	if err == nil || !strings.Contains(err.Error(), "antigravity cli returned an empty response") {
		t.Fatalf("parseResponse() error = %v, want retryable empty-response error", err)
	}
}

func TestAntigravityCliChatStreamEventsUsesCurrentNDJSONAndDoesNotDuplicateFinalResponse(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	argsFile := filepath.Join(stateDir, "args")
	output := "{\"event\":\"init\"}\n" +
		"{\"event\":\"step_update\",\"step_update\":{\"text_delta\":\"Hello\"}}\n" +
		"{\"event\":\"step_update\",\"step_update\":{\"text_delta\":\" world\"}}\n" +
		"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"Hello world\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"thinking_tokens\":1,\"cache_read_tokens\":1,\"total_tokens\":8}}}\n"
	p := NewAntigravityCliProvider(workspace)
	p.command = createMockAntigravityCLI(t, argsFile, filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"), output)

	var chunks []string
	resp, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "gemini-test", nil, func(chunk StreamChunk) {
		chunks = append(chunks, chunk.Content)
	})
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if got, want := strings.Join(chunks, "|"), "Hello|Hello world"; got != want {
		t.Fatalf("chunks = %q, want %q", got, want)
	}
	if resp.Content != "Hello world" || resp.FinishReason != "stop" || resp.Usage == nil || resp.Usage.TotalTokens != 8 {
		t.Fatalf("response = %#v, want parsed terminal result", resp)
	}
	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	wantArgs := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--print-timeout", "15m0s",
		"--sandbox",
		"--mode", "accept-edits",
		"--dangerously-skip-permissions",
		"--add-dir", workspace,
		"--model", "gemini-test",
	}
	if strings.Join(args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %q, want exact invocation %q", args, wantArgs)
	}
	if containsString(args, "--disable-slash-commands") {
		t.Fatalf("--disable-slash-commands must not be present: %q", args)
	}
	if !containsString(args, "--dangerously-skip-permissions") {
		t.Fatalf("missing expected flag --dangerously-skip-permissions: %q", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "hello") || strings.HasPrefix(arg, "--print=") {
			t.Fatalf("prompt was passed in argv: %q", args)
		}
	}
	requestBytes, err := os.ReadFile(filepath.Join(stateDir, "print"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatalf("stream stdin request is not JSON: %v", err)
	}
	if request.Event != "user" || !strings.HasSuffix(request.Message.Content, "## Conversation\n\nUser: hello") ||
		!strings.Contains(request.Message.Content, "Do not use any Antigravity-native tools") {
		t.Fatalf("stream stdin request = %#v, want one user prompt", request)
	}
}

func TestAntigravityCliChatLargePromptIsSentViaSingleNDJSONStdinRequest(t *testing.T) {
	stateDir := t.TempDir()
	argsFile := filepath.Join(stateDir, "args")
	stdinFile := filepath.Join(stateDir, "stdin")
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLI(t, argsFile, stdinFile, filepath.Join(stateDir, "cwd"),
		`{"event":"result","result":{"status":"SUCCESS","response":"ok"}}`)

	largePrompt := strings.Repeat("large prompt ", 256*1024) // 3 MiB
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: largePrompt}}, nil, "", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("response = %#v, want ok", resp)
	}
	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argsBytes), largePrompt[:64]) {
		t.Fatal("large prompt was passed in argv")
	}
	requestBytes, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(requestBytes), "\n") || strings.Count(string(requestBytes), "\n") != 1 {
		t.Fatalf("stdin = %q, want exactly one NDJSON record", requestBytes[:min(len(requestBytes), 128)])
	}
	var request struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatal(err)
	}
	if request.Event != "user" || !strings.HasSuffix(request.Message.Content, "## Conversation\n\nUser: "+largePrompt) ||
		!strings.Contains(request.Message.Content, "Do not use any Antigravity-native tools") {
		t.Fatalf("large prompt was not preserved in stdin request")
	}
}

func TestAntigravityCliChatStreamEventsUsesDeltasWhenSuccessfulTerminalResponseIsEmpty(t *testing.T) {
	stateDir := t.TempDir()
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLI(t,
		filepath.Join(stateDir, "args"), filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"),
		"{\"event\":\"step_update\",\"step_update\":{\"text_delta\":\"Hello\"}}\n"+
			"{\"event\":\"step_update\",\"step_update\":{\"text_delta\":\"world\"}}\n"+
			"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"\"}}\n")

	var chunks []string
	resp, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, func(chunk StreamChunk) {
		chunks = append(chunks, chunk.Content)
	})
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if got, want := strings.Join(chunks, "|"), "Hello|Helloworld"; got != want {
		t.Fatalf("chunks = %q, want accumulated deltas without a final duplicate %q", got, want)
	}
	if resp.Content != "Helloworld" || resp.FinishReason != "stop" || len(resp.ToolCalls) != 0 {
		t.Fatalf("response = %#v, want delta-backed stop response without tool calls", resp)
	}
}

func TestAntigravityCliChatStreamEventsStreamsCompletedNativeToolStepsUIOnly(t *testing.T) {
	stateDir := t.TempDir()
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLI(t,
		filepath.Join(stateDir, "args"), filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"),
		"{\"event\":\"step_update\",\"step_update\":{\"text_delta\":\"Working\"}}\n"+
			"{\"event\":\"step_update\",\"step_update\":{\"state\":\"DONE\",\"step_type\":\"tool\",\"tool_name\":\"terminal\",\"tool_info\":{\"name\":\"run_command\",\"parameters\":{\"command\":\"pwd\"},\"output\":\"/workspace\",\"error\":\"secret error\"}}}\n"+
			"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"done\"}}\n")

	var chunks []StreamChunk
	resp, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if len(chunks) != 2 || chunks[0].Content != "Working" || len(chunks[1].ToolCalls) != 1 {
		t.Fatalf("chunks = %#v, want text followed by one native tool chunk", chunks)
	}
	call := chunks[1].ToolCalls[0]
	if call.Type != "function" || call.Name != "run_command" || call.Function == nil || call.Function.Name != "run_command" {
		t.Fatalf("streamed tool call = %#v, want run_command function", call)
	}
	if call.Function.Arguments != `{"command":"pwd"}` || call.Arguments["command"] != "pwd" {
		t.Fatalf("streamed tool arguments = %#v, want only command", call)
	}
	if strings.Contains(call.Function.Arguments, "output") || strings.Contains(call.Function.Arguments, "error") {
		t.Fatalf("streamed tool arguments leaked native result: %q", call.Function.Arguments)
	}
	if resp.Content != "done" || resp.FinishReason != "stop" || len(resp.ToolCalls) != 0 {
		t.Fatalf("response = %#v, want terminal text without executable tool calls", resp)
	}
}

func TestAntigravityCliChatStreamEventsIgnoresIncompleteOrNonToolSteps(t *testing.T) {
	stateDir := t.TempDir()
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLI(t,
		filepath.Join(stateDir, "args"), filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"),
		"{\"event\":\"step_update\",\"step_update\":{\"state\":\"RUNNING\",\"step_type\":\"tool\",\"tool_name\":\"terminal\",\"tool_info\":{\"name\":\"run_command\",\"parameters\":{\"command\":\"pwd\"}}}}\n"+
			"{\"event\":\"step_update\",\"step_update\":{\"state\":\"DONE\",\"step_type\":\"analysis\",\"tool_name\":\"terminal\",\"tool_info\":{\"name\":\"run_command\",\"parameters\":{\"command\":\"pwd\"}}}}\n"+
			"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"done\"}}\n")

	var chunks []StreamChunk
	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, func(chunk StreamChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("ChatStreamEvents() error = %v", err)
	}
	if len(chunks) != 1 || chunks[0].Content != "done" || len(chunks[0].ToolCalls) != 0 {
		t.Fatalf("chunks = %#v, want only terminal text fallback", chunks)
	}
}

func TestAntigravityCliChatStreamEventsFallsBackToTerminalResponse(t *testing.T) {
	stateDir := t.TempDir()
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLI(t,
		filepath.Join(stateDir, "args"), filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"),
		"{\"event\":\"init\"}\n{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"final only\"}}\n")
	var chunks []string
	resp, err := p.ChatStream(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(chunks, "|"); got != "final only" {
		t.Fatalf("chunks = %q, want final fallback", got)
	}
	if resp.Content != "final only" {
		t.Fatalf("response content = %q", resp.Content)
	}
}

func TestAntigravityCliChatStreamEventsRejectsTerminalError(t *testing.T) {
	stateDir := t.TempDir()
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLI(t,
		filepath.Join(stateDir, "args"), filepath.Join(stateDir, "print"), filepath.Join(stateDir, "cwd"),
		"{\"event\":\"result\",\"result\":{\"status\":\"ERROR\",\"response\":\"failed\",\"error\":\"quota exceeded\"}}\n")
	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("ChatStreamEvents() error = %v, want terminal error", err)
	}
}

func TestAntigravityCliChatStreamEventsTerminalErrorIncludesStderrAndRedactsCredentials(t *testing.T) {
	terminalError := "request failed; Authorization: Bearer terminal-bearer; api_key=terminal-api-key; endpoint=https://alice:terminal-password@example.test; provider diagnostic"
	stderr := "retry after 5 seconds\naccess_token=stderr-access-token refresh_token=stderr-refresh-token id_token=stderr-id-token token=stderr-token password=stderr-password secret=stderr-secret\nprovider diagnostic detail\n"
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLIWithStderr(t,
		fmt.Sprintf("{\"event\":\"result\",\"result\":{\"status\":\"ERROR\",\"error\":%q}}\n", terminalError), stderr)

	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	if err == nil {
		t.Fatal("ChatStreamEvents() expected error")
	}
	got := err.Error()
	wantStderr := "stderr: retry after 5 seconds\naccess_token=[REDACTED] refresh_token=[REDACTED] id_token=[REDACTED] token=[REDACTED] password=[REDACTED] secret=[REDACTED]\nprovider diagnostic detail\n"
	if !strings.Contains(got, wantStderr) {
		t.Fatalf("ChatStreamEvents() error = %q, want complete redacted stderr %q", got, wantStderr)
	}
	for _, want := range []string{"antigravity cli returned ERROR", "stderr: retry after 5 seconds", "provider diagnostic", "provider diagnostic detail", "[REDACTED]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ChatStreamEvents() error = %q, want %q", got, want)
		}
	}
	for _, secret := range []string{"terminal-bearer", "terminal-api-key", "terminal-password", "stderr-access-token", "stderr-refresh-token", "stderr-id-token", "stderr-token", "stderr-password", "stderr-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("ChatStreamEvents() error leaked credential %q: %q", secret, got)
		}
	}
}

func TestAntigravityCliChatStreamEventsEmptyResponseIncludesDiagnosticsAndRedactedStderr(t *testing.T) {
	stderr := "access_token=stderr-access-token provider note\n"
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLIWithStderr(t,
		"{\"event\":\"step_update\",\"step_update\":{}}\n"+
			"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0,\"thinking_tokens\":3,\"cache_read_tokens\":2,\"total_tokens\":12}}}\n", stderr)

	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	if err == nil {
		t.Fatal("ChatStreamEvents() expected error")
	}
	got := err.Error()
	for _, want := range []string{
		"antigravity cli returned an empty response",
		`stream diagnostics: status="SUCCESS" got_delta=false step_updates=1 response_bytes=0 error_bytes=0 input_tokens=7 output_tokens=0 thinking_tokens=3 cache_read_tokens=2 total_tokens=12`,
		"stderr: access_token=[REDACTED] provider note",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ChatStreamEvents() error = %q, want %q", got, want)
		}
	}
	for _, secret := range []string{"stderr-access-token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("ChatStreamEvents() error leaked credential %q: %q", secret, got)
		}
	}
}

func TestAntigravityCliChatStreamEventsReturnsPrintTimeoutDiagnostic(t *testing.T) {
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityCLIWithStderr(t,
		"{\"event\":\"result\",\"result\":{\"status\":\"SUCCESS\",\"response\":\"\"}}\n",
		"[agy] print timeout after 15m\n")

	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	if err == nil {
		t.Fatal("ChatStreamEvents() expected error")
	}
	if got := err.Error(); !strings.Contains(got, "[agy] print timeout after 15m") ||
		strings.Contains(got, "antigravity cli returned an empty response") {
		t.Fatalf("ChatStreamEvents() error = %q, want print-timeout diagnostic", got)
	}
}

func TestAntigravityCliChatStreamEventsFailsFastOnMalformedRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "agy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'not json\\n'\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewAntigravityCliProvider("")
	p.command = script

	started := time.Now()
	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "failed to parse antigravity cli stream record") {
		t.Fatalf("ChatStreamEvents() error = %v, want stream parse error", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("ChatStreamEvents() returned after %s, want immediate child termination", elapsed)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAntigravityCliChatNonZeroExitUsesStderrWhenPresent(t *testing.T) {
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityFailingCLI(t, `{"event":"init"}`, "quota exceeded", 1)

	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil)
	if err == nil {
		t.Fatal("Chat() expected error")
	}
	if got := err.Error(); !strings.Contains(got, "quota exceeded") || strings.Contains(got, "raw output") {
		t.Fatalf("Chat() error = %q, want stderr-only error", got)
	}
}

func TestAntigravityCliChatNonZeroExitWithoutStderrIncludesRawOutput(t *testing.T) {
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityFailingCLI(t, `{"event":"init"}`, "", 1)

	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil)
	if err == nil {
		t.Fatal("Chat() expected error")
	}
	for _, want := range []string{"unclassified", "exit status 1", `raw output: {"event":"init"}`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Chat() error = %q, want %q", err, want)
		}
	}
}

func TestAntigravityCliChatStreamEventsNonZeroExitWithoutStderrIncludesRawOutput(t *testing.T) {
	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityFailingCLI(t, `{"event":"notice","detail":"credit balance exhausted"}`, "", 1)

	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	if err == nil {
		t.Fatal("ChatStreamEvents() expected error")
	}
	for _, want := range []string{"unclassified", "exit status 1", "credit balance exhausted"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ChatStreamEvents() error = %q, want %q", err, want)
		}
	}
}

func TestAntigravityCliChatCancellationReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := NewAntigravityCliProvider("")
	p.command = createMockAntigravityFailingCLI(t, "credit balance exhausted", "", 1)
	_, err := p.Chat(ctx, []Message{{Role: "user", Content: "hello"}}, nil, "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want context cancellation", err)
	}
}

func TestIsAntigravityCLIQuotaExhausted(t *testing.T) {
	tests := []struct {
		diagnostic string
		want       bool
	}{
		{"RESOURCE_EXHAUSTED (429): Individual quota reached", true},
		{"error: RESOURCE_EXHAUSTED (429)", true},
		{"RESOURCE_EXHAUSTED: quota exceeded for metric", true},
		{"exceeded your current quota, please check your plan", true},
		{"quota exceeded", true},
		{"429 Too Many Requests", false},
		{"context deadline exceeded", false},
		{"connection reset by peer", false},
		{"model unavailable", false},
	}
	for _, tt := range tests {
		if got := isAntigravityCLIQuotaExhausted(tt.diagnostic); got != tt.want {
			t.Errorf("isAntigravityCLIQuotaExhausted(%q) = %v, want %v", tt.diagnostic, got, tt.want)
		}
	}
}

func TestAntigravityCliChatStreamEventsFastFailsOnQuotaExhaustion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "agy")
	// Mock agy CLI that outputs quota exhaustion on stderr and then sleeps
	contents := `#!/bin/sh
cat >/dev/null
printf 'RESOURCE_EXHAUSTED (429): Individual quota reached\n' >&2
while :; do :; done
`
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewAntigravityCliProvider("")
	p.command = script

	started := time.Now()
	_, err := p.ChatStreamEvents(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "", nil, nil)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("ChatStreamEvents() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") || !strings.Contains(err.Error(), "Individual quota reached") {
		t.Fatalf("ChatStreamEvents() error = %q, want quota exhaustion details", err.Error())
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("ChatStreamEvents() took %s, want fast-fail in < 3s", elapsed)
	}
}
