package cliprovider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var _ LLMProvider = (*AntigravityCliProvider)(nil)

func createMockAntigravityCLI(t *testing.T, argsFile, printFile, cwdFile, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}
	dir := t.TempDir()
	outputFile := filepath.Join(dir, "output.json")
	if err := os.WriteFile(outputFile, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "agy")
	contents := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > '%s'
for arg do
	case "$arg" in
	--print=*) printf '%%s' "${arg#--print=}" > '%s' ;;
	esac
done
pwd > '%s'
cat '%s'
`, argsFile, printFile, cwdFile, outputFile)
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
	output := `{"result":"{\"tool_calls\":[{\"id\":\"call_ok\",\"type\":\"function\",\"function\":{\"name\":\"cron\",\"arguments\":\"{\\\"action\\\":\\\"list\\\"}\"}},{\"id\":\"call_bad\",\"type\":\"function\",\"function\":{\"name\":\"terminal\",\"arguments\":\"{\\\"command\\\":\\\"touch nope\\\"}\"}}]}","usage":{"input_tokens":7,"output_tokens":5}}`

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
	if resp.Usage == nil || resp.Usage.TotalTokens != 12 {
		t.Fatalf("Usage = %#v, want total 12", resp.Usage)
	}

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	for _, want := range []string{"--sandbox", "--mode", "plan", "--disable-slash-commands", "--add-dir", workspace, "--model", "gemini-test"} {
		if !containsString(args, want) {
			t.Errorf("args missing %q: %q", want, args)
		}
	}
	if !containsStringPrefix(args, "--print=") {
		t.Fatalf("args missing attached print prompt: %q", args)
	}
	if containsString(args, "--dangerously-skip-permissions") {
		t.Fatalf("unsafe auto-approve flag present: %q", args)
	}
	cwdBytes, _ := os.ReadFile(cwdFile)
	if got := strings.TrimSpace(string(cwdBytes)); got != workspace {
		t.Errorf("cwd = %q, want exact workspace %q", got, workspace)
	}
	promptBytes, err := os.ReadFile(printFile)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(promptBytes)
	for _, want := range []string{
		"## System Instructions", "System policy.",
		"## Conversation", "User: List jobs.",
		"## Available Tools", "cron", "terminal JSON text protocol",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestAntigravityCliParseResponseDoesNotExecuteProseEmbeddedTextCall(t *testing.T) {
	result := "I will list the jobs.\n" +
		`{"tool_calls":[{"id":"call_ok","type":"function","function":{"name":"cron","arguments":"{\"action\":\"list\"}"}}]}`
	p := NewAntigravityCliProvider("")
	resp, err := p.parseResponse(fmt.Sprintf(`{"result":%q}`, result), []ToolDefinition{{
		Type: "function", Function: ToolFunctionDefinition{Name: "cron"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "stop" || len(resp.ToolCalls) != 0 {
		t.Fatalf("response = %#v, want no executable calls", resp)
	}
	if resp.Content != result {
		t.Fatalf("Content = %q, want prose-prefixed JSON preserved as %q", resp.Content, result)
	}
}

func TestAntigravityCliParseResponseReturnsExactUnknownTerminalCall(t *testing.T) {
	const result = `{"tool_calls":[{"id":"call_missing","type":"function","function":{"name":"missing_tool","arguments":"{\"path\":\"notes.txt\",\"limit\":3}"}}]}`
	p := NewAntigravityCliProvider("")
	resp, err := p.parseResponse(fmt.Sprintf(`{"result":%q}`, result), []ToolDefinition{{
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
		`{"result":"{\"tool_calls\":[{\"id\":\"native\",\"type\":\"function\",\"function\":{\"name\":\"terminal\",\"arguments\":\"{}\"}}]}"}`)

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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsStringPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
