//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestShellTool_AbsoluteShellPath_Sync verifies that the sync exec path invokes
// /bin/sh directly rather than resolving sh through PATH. It strips PATH of any
// directory that contains sh before running, so a PATH-based lookup would fail.
func TestShellTool_AbsoluteShellPath_Sync(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-path-that-has-no-sh")

	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": "echo hello-absolute-sh",
	})

	require.False(t, result.IsError, "sync exec should succeed without sh in PATH: %s", result.ForLLM)
	require.Contains(t, result.ForLLM, "hello-absolute-sh")
}

// TestShellTool_AbsoluteShellPath_Background verifies that the background exec
// path also invokes /bin/sh directly rather than resolving through PATH.
func TestShellTool_AbsoluteShellPath_Background(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-path-that-has-no-sh")

	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	sm := NewSessionManager()
	t.Cleanup(sm.Stop)
	tool.sessionManager = sm

	ctx := WithToolContext(context.Background(), "cli", "test")

	runResult := tool.Execute(ctx, map[string]any{
		"action":     "run",
		"command":    "echo hello-bg-absolute-sh",
		"background": "true",
	})
	require.False(t, runResult.IsError, "background exec should succeed without sh in PATH: %s", runResult.ForLLM)

	var resp ExecResponse
	require.NoError(t, json.Unmarshal([]byte(runResult.ForLLM), &resp))
	require.NotEmpty(t, resp.SessionID)

	time.Sleep(300 * time.Millisecond)

	readResult := tool.Execute(ctx, map[string]any{
		"action":    "read",
		"sessionId": resp.SessionID,
	})
	require.False(t, readResult.IsError, "read should succeed: %s", readResult.ForLLM)

	var readResp ExecResponse
	require.NoError(t, json.Unmarshal([]byte(readResult.ForLLM), &readResp))
	require.True(t,
		strings.Contains(readResp.Output, "hello-bg-absolute-sh") ||
			strings.Contains(readResult.ForLLM, "hello-bg-absolute-sh"),
		"output should contain marker: %s", readResult.ForLLM,
	)

	tool.Execute(ctx, map[string]any{"action": "kill", "sessionId": resp.SessionID})
}
