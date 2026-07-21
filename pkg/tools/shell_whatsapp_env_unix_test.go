//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/stretchr/testify/require"
)

const testWhatsAppSenderE164 = "+33695651381"

// TestExecTool_WhatsAppSenderEnv_ReachesChild_Sync proves that an authenticated
// native WhatsApp sender identity carried in the tool exec context is exposed to
// the synchronous exec child process via PICOCLAW_WHATSAPP_SENDER_E164.
func TestExecTool_WhatsAppSenderEnv_ReachesChild_Sync(t *testing.T) {
	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	ctx := WithToolWhatsAppSenderE164(context.Background(), testWhatsAppSenderE164)

	result := tool.Execute(ctx, map[string]any{
		"action":  "run",
		"command": "printf 'SENDER=[%s]' \"$" + constants.EnvWhatsAppSenderE164 + "\"",
	})

	require.False(t, result.IsError, "sync exec should succeed: %s", result.ForLLM)
	require.Contains(t, result.ForLLM, "SENDER=["+testWhatsAppSenderE164+"]")
}

// TestExecTool_WhatsAppSenderEnv_ReachesChild_Background proves the background
// exec path also exposes the authenticated sender identity to the child.
func TestExecTool_WhatsAppSenderEnv_ReachesChild_Background(t *testing.T) {
	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	sm := NewSessionManager()
	t.Cleanup(sm.Stop)
	tool.sessionManager = sm

	ctx := WithToolWhatsAppSenderE164(context.Background(), testWhatsAppSenderE164)

	runResult := tool.Execute(ctx, map[string]any{
		"action":     "run",
		"command":    "printf 'SENDER=[%s]' \"$" + constants.EnvWhatsAppSenderE164 + "\"",
		"background": "true",
	})
	require.False(t, runResult.IsError, "background exec should succeed: %s", runResult.ForLLM)

	var resp ExecResponse
	require.NoError(t, json.Unmarshal([]byte(runResult.ForLLM), &resp))
	require.NotEmpty(t, resp.SessionID)

	time.Sleep(300 * time.Millisecond)

	readResult := tool.Execute(ctx, map[string]any{
		"action":    "read",
		"sessionId": resp.SessionID,
	})
	t.Cleanup(func() {
		tool.Execute(ctx, map[string]any{"action": "kill", "sessionId": resp.SessionID})
	})
	require.False(t, readResult.IsError, "read should succeed: %s", readResult.ForLLM)
	require.Contains(t, readResult.ForLLM, "SENDER=["+testWhatsAppSenderE164+"]")
}

// TestExecTool_WhatsAppSenderEnv_AbsentWhenUnset proves the variable is not set
// in the child when no authenticated sender identity is present on the context.
func TestExecTool_WhatsAppSenderEnv_AbsentWhenUnset(t *testing.T) {
	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	// A normal inbound tool context (channel/chat only) — no WhatsApp sender.
	ctx := WithToolContext(context.Background(), "cli", "test")

	result := tool.Execute(ctx, map[string]any{
		"action":  "run",
		"command": "printf 'SENDER=[%s]' \"$" + constants.EnvWhatsAppSenderE164 + "\"",
	})

	require.False(t, result.IsError, "sync exec should succeed: %s", result.ForLLM)
	require.Contains(t, result.ForLLM, "SENDER=[]")
	require.NotContains(t, result.ForLLM, testWhatsAppSenderE164)
}

// TestExecTool_WhatsAppSenderEnv_NotSettableViaToolArgs proves that spoofable
// tool input cannot set the sender identity variable. Even when the model-
// supplied tool arguments contain fields named after the env var and the
// authenticated metadata key, the child sees no value because the variable is
// derived solely from the exec context, never from tool arguments.
func TestExecTool_WhatsAppSenderEnv_NotSettableViaToolArgs(t *testing.T) {
	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	// No WhatsApp sender on the context; attacker controls only the tool args.
	ctx := WithToolContext(context.Background(), "cli", "test")

	result := tool.Execute(ctx, map[string]any{
		"action":                        "run",
		"command":                       "printf 'SENDER=[%s]' \"$" + constants.EnvWhatsAppSenderE164 + "\"",
		constants.EnvWhatsAppSenderE164: "+14155550100",
		"whatsapp_linked_phone_number":  "+14155550100",
		"sender":                        "+14155550100",
		"env":                           map[string]any{constants.EnvWhatsAppSenderE164: "+14155550100"},
	})

	require.False(t, result.IsError, "sync exec should succeed: %s", result.ForLLM)
	require.Contains(t, result.ForLLM, "SENDER=[]")
	require.NotContains(t, result.ForLLM, "+14155550100")
}

// TestApplyInboundSenderEnv_PreservesInheritedEnv is a focused check that the
// injection appends to (rather than replaces) the inherited environment, so the
// child still receives its normal variables alongside the sender identity.
func TestExecTool_WhatsAppSenderEnv_PreservesInheritedEnv(t *testing.T) {
	tool, err := NewExecTool("", false)
	require.NoError(t, err)

	t.Setenv("PICOCLAW_TEST_INHERITED", "inherited-value")
	ctx := WithToolWhatsAppSenderE164(context.Background(), testWhatsAppSenderE164)

	result := tool.Execute(ctx, map[string]any{
		"action":  "run",
		"command": "printf 'INHERITED=[%s] SENDER=[%s]' \"$PICOCLAW_TEST_INHERITED\" \"$" + constants.EnvWhatsAppSenderE164 + "\"",
	})

	require.False(t, result.IsError, "sync exec should succeed: %s", result.ForLLM)
	require.True(t, strings.Contains(result.ForLLM, "INHERITED=[inherited-value]"),
		"inherited env should survive injection: %s", result.ForLLM)
	require.Contains(t, result.ForLLM, "SENDER=["+testWhatsAppSenderE164+"]")
}
