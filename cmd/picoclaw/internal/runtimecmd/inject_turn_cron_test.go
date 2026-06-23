package runtimecmd

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// stubProvider is a minimal LLMProvider so we can build a real AgentLoop in
// tests without contacting any backend.
type stubProvider struct{}

func (stubProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{}, nil
}

func (stubProvider) GetDefaultModel() string { return "stub-model" }

func loopHasCronTool(al *agent.AgentLoop) bool {
	info := al.GetStartupInfo()
	toolsInfo, ok := info["tools"].(map[string]any)
	if !ok {
		return false
	}
	names, ok := toolsInfo["names"].([]string)
	if !ok {
		return false
	}
	for _, n := range names {
		if n == "cron" {
			return true
		}
	}
	return false
}

// TestRegisterInjectTurnCronTool_RegistersCronWhenEnabled proves the inject-turn
// path uses the shared builder to expose the cron tool when it is enabled.
func TestRegisterInjectTurnCronTool_RegistersCronWhenEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = true
	cfg.Agents.Defaults.Workspace = t.TempDir()

	msgBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, msgBus, stubProvider{})
	t.Cleanup(loop.Close)

	if err := registerInjectTurnCronTool(loop, msgBus, cfg); err != nil {
		t.Fatalf("registerInjectTurnCronTool() error: %v", err)
	}
	if !loopHasCronTool(loop) {
		t.Fatal("expected inject-turn loop to expose the cron tool when enabled")
	}
}

// TestRegisterInjectTurnCronTool_OmitsCronWhenDisabled proves the cron tool is
// not registered when cron is disabled.
func TestRegisterInjectTurnCronTool_OmitsCronWhenDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = false
	cfg.Agents.Defaults.Workspace = t.TempDir()

	msgBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, msgBus, stubProvider{})
	t.Cleanup(loop.Close)

	if err := registerInjectTurnCronTool(loop, msgBus, cfg); err != nil {
		t.Fatalf("registerInjectTurnCronTool() error: %v", err)
	}
	if loopHasCronTool(loop) {
		t.Fatal("expected no cron tool registered for inject-turn when cron is disabled")
	}
}
