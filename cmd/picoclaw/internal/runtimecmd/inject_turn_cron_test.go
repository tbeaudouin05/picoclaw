package runtimecmd

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func newInjectTurnTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	return cfg
}

func TestBuildRuntimeCronTool_ExposesCronWhenEnabled(t *testing.T) {
	cfg := newInjectTurnTestConfig(t)
	if !cfg.Tools.IsToolEnabled("cron") {
		t.Fatal("expected cron enabled by default config")
	}

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	cronTool, err := buildRuntimeCronTool(cfg, nil, msgBus)
	if err != nil {
		t.Fatalf("buildRuntimeCronTool: %v", err)
	}
	if cronTool == nil {
		t.Fatal("expected cron tool when tools.cron.enabled is true")
	}
	if cronTool.Name() != "cron" {
		t.Fatalf("tool name = %q, want cron", cronTool.Name())
	}
}

func TestBuildRuntimeCronTool_NilWhenDisabled(t *testing.T) {
	cfg := newInjectTurnTestConfig(t)
	cfg.Tools.Cron.Enabled = false

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	cronTool, err := buildRuntimeCronTool(cfg, nil, msgBus)
	if err != nil {
		t.Fatalf("buildRuntimeCronTool: %v", err)
	}
	if cronTool != nil {
		t.Fatal("expected no cron tool when tools.cron.enabled is false")
	}
}

func TestBuildRuntimeCronTool_PreservesAllowCommandFalse(t *testing.T) {
	cfg := newInjectTurnTestConfig(t)
	cfg.Tools.Cron.AllowCommand = false

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	cronTool, err := buildRuntimeCronTool(cfg, nil, msgBus)
	if err != nil {
		t.Fatalf("buildRuntimeCronTool: %v", err)
	}
	if cronTool == nil {
		t.Fatal("expected cron tool to be built")
	}

	ctx := tools.WithToolContext(context.Background(), "cli", "direct")
	result := cronTool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "check disk",
		"command":    "df -h",
		"at_seconds": float64(60),
	})
	if !result.IsError {
		t.Fatal("expected command scheduling to require confirm when allow_command is false")
	}
	if !strings.Contains(result.ForLLM, "command_confirm=true") {
		t.Fatalf("expected command_confirm requirement, got: %s", result.ForLLM)
	}
}

func TestBuildRuntimeCronTool_AllowsCommandWhenAllowCommandTrue(t *testing.T) {
	cfg := newInjectTurnTestConfig(t)
	if !cfg.Tools.Cron.AllowCommand {
		t.Fatal("expected allow_command true by default config")
	}

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	cronTool, err := buildRuntimeCronTool(cfg, nil, msgBus)
	if err != nil {
		t.Fatalf("buildRuntimeCronTool: %v", err)
	}
	if cronTool == nil {
		t.Fatal("expected cron tool to be built")
	}

	ctx := tools.WithToolContext(context.Background(), "cli", "direct")
	result := cronTool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "check disk",
		"command":    "df -h",
		"at_seconds": float64(60),
	})
	if result.IsError {
		t.Fatalf("expected command scheduling to succeed when allow_command is true, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Cron job added") {
		t.Fatalf("expected 'Cron job added', got: %s", result.ForLLM)
	}
}
