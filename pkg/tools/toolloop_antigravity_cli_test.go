package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

type antigravityLoopRecordingTool struct {
	name  string
	calls int
}

func (t *antigravityLoopRecordingTool) Name() string        { return t.name }
func (t *antigravityLoopRecordingTool) Description() string { return "test tool" }
func (t *antigravityLoopRecordingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *antigravityLoopRecordingTool) Execute(context.Context, map[string]any) *ToolResult {
	t.calls++
	return SilentResult("executed " + t.name)
}

func TestRunToolLoopReturnsUnknownAntigravityCallErrorInNextPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}

	dir := t.TempDir()
	promptsFile := filepath.Join(dir, "prompts")
	countFile := filepath.Join(dir, "count")
	script := filepath.Join(dir, "agy")
	contents := `#!/bin/sh
sed 's/\\"/"/g' >> "` + promptsFile + `"
printf '%s\n' '---PROMPT---' >> "` + promptsFile + `"
if [ ! -f "` + countFile + `" ]; then
	: > "` + countFile + `"
	printf '%s' ` + strconv.Quote(`{"event":"result","result":{"status":"SUCCESS","response":`+strconv.Quote(`{"tool_calls":[{"id":"call_missing","type":"function","function":{"name":"missing_tool","arguments":"{\"query\":\"status\"}"}}]}`)+`}}`) + `
else
	printf '%s' '{"event":"result","result":{"status":"SUCCESS","response":"Handled missing tool."}}'
fi
`
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider: providers.NewAntigravityCliProvider(dir),
		Model:    "antigravity-cli", Tools: NewToolRegistry(), MaxIterations: 2,
	}, []providers.Message{{Role: "user", Content: "Check status."}}, "test", "chat")
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if result.Content != "Handled missing tool." || result.Iterations != 2 {
		t.Fatalf("result = %#v, want second-iteration answer", result)
	}

	prompts, err := os.ReadFile(promptsFile)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(prompts), "---PROMPT---\n")
	if len(parts) < 3 {
		t.Fatalf("captured prompts = %q, want two prompts", prompts)
	}
	if strings.Contains(parts[0], `tool "missing_tool" not found`) {
		t.Fatalf("first prompt unexpectedly contains tool-not-found feedback: %q", parts[0])
	}
	const feedback = `[Tool Result for call_missing]: requested tool "missing_tool" is not available for this request; use only advertised PicoClaw tools`
	if !strings.Contains(parts[1], feedback) {
		t.Fatalf("second prompt missing unavailable-tool feedback: %q", parts[1])
	}
}

func TestRunToolLoopDoesNotExecuteUnadvertisedAntigravityCalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}

	for _, name := range []string{"hidden_tool", "terminal"} {
		t.Run(name, func(t *testing.T) {
			tool := &antigravityLoopRecordingTool{name: name}
			registry := NewToolRegistry()
			registry.RegisterHidden(tool)
			result, prompts := runAntigravityToolLoop(t, registry, name, "call_unavailable")

			if result.Content != "Handled tool result." || result.Iterations != 2 {
				t.Fatalf("result = %#v, want second-iteration answer", result)
			}
			if tool.calls != 0 {
				t.Fatalf("%s executed %d times, want 0", name, tool.calls)
			}
			feedback := `[Tool Result for call_unavailable]: requested tool ` + strconv.Quote(name) +
				` is not available for this request; use only advertised PicoClaw tools`
			if !strings.Contains(prompts, feedback) {
				t.Fatalf("next prompt missing corrective feedback: %q", prompts)
			}
		})
	}
}

func TestRunToolLoopExecutesAdvertisedAntigravityCallNormally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}

	tool := &antigravityLoopRecordingTool{name: "advertised_tool"}
	registry := NewToolRegistry()
	registry.Register(tool)
	result, prompts := runAntigravityToolLoop(t, registry, tool.name, "call_advertised")

	if result.Content != "Handled tool result." || result.Iterations != 2 {
		t.Fatalf("result = %#v, want second-iteration answer", result)
	}
	if tool.calls != 1 {
		t.Fatalf("advertised tool executed %d times, want 1", tool.calls)
	}
	if !strings.Contains(prompts, `[Tool Result for call_advertised]: executed advertised_tool`) {
		t.Fatalf("next prompt missing normal tool result: %q", prompts)
	}
}

func runAntigravityToolLoop(
	t *testing.T, registry *ToolRegistry, toolName, callID string,
) (*ToolLoopResult, string) {
	t.Helper()
	dir := t.TempDir()
	promptsFile := filepath.Join(dir, "prompts")
	countFile := filepath.Join(dir, "count")
	script := filepath.Join(dir, "agy")
	toolCall := `{"tool_calls":[{"id":` + strconv.Quote(callID) +
		`,"type":"function","function":{"name":` + strconv.Quote(toolName) + `,"arguments":"{}"}}]}`
	response, err := strconv.Unquote(strconv.Quote(toolCall))
	if err != nil {
		t.Fatal(err)
	}
	contents := `#!/bin/sh
sed 's/\\"/"/g' >> "` + promptsFile + `"
printf '%s\n' '---PROMPT---' >> "` + promptsFile + `"
if [ ! -f "` + countFile + `" ]; then
	: > "` + countFile + `"
	printf '%s' ` + strconv.Quote(`{"event":"result","result":{"status":"SUCCESS","response":`+strconv.Quote(response)+`}}`) + `
else
	printf '%s' '{"event":"result","result":{"status":"SUCCESS","response":"Handled tool result."}}'
fi
`
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := RunToolLoop(context.Background(), ToolLoopConfig{
		Provider: providers.NewAntigravityCliProvider(dir),
		Model:    "antigravity-cli", Tools: registry, MaxIterations: 2,
	}, []providers.Message{{Role: "user", Content: "Use the tool."}}, "test", "chat")
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	prompts, err := os.ReadFile(promptsFile)
	if err != nil {
		t.Fatal(err)
	}
	return result, string(prompts)
}
