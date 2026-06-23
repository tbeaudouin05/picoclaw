package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

// recordingRegistrar captures every tool registered through the shared builder
// so tests can assert what the cron bootstrap exposed.
type recordingRegistrar struct {
	registered []Tool
}

func (r *recordingRegistrar) RegisterTool(t Tool) {
	r.registered = append(r.registered, t)
}

func (r *recordingRegistrar) names() []string {
	out := make([]string, 0, len(r.registered))
	for _, t := range r.registered {
		out = append(out, t.Name())
	}
	return out
}

func newCronBootstrapParams(t *testing.T, cfg *config.Config) (CronRuntimeParams, *recordingRegistrar) {
	t.Helper()
	reg := &recordingRegistrar{}
	return CronRuntimeParams{
		Executor:    &stubJobExecutor{},
		Registrar:   reg,
		MsgBus:      bus.NewMessageBus(),
		Workspace:   t.TempDir(),
		Restrict:    true,
		ExecTimeout: 0,
		Config:      cfg,
	}, reg
}

func TestBuildCronRuntime_ExposesCronWhenEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = true

	params, reg := newCronBootstrapParams(t, cfg)
	cronService, cronTool, err := BuildCronRuntime(params)
	if err != nil {
		t.Fatalf("BuildCronRuntime() error: %v", err)
	}
	if cronService == nil {
		t.Fatal("expected non-nil cron service")
	}
	if cronTool == nil {
		t.Fatal("expected cron tool when cron is enabled")
	}
	if got := reg.names(); len(got) != 1 || got[0] != "cron" {
		t.Fatalf("expected only the cron tool registered, got %v", got)
	}
}

func TestBuildCronRuntime_OmitsCronWhenDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = false

	params, reg := newCronBootstrapParams(t, cfg)
	cronService, cronTool, err := BuildCronRuntime(params)
	if err != nil {
		t.Fatalf("BuildCronRuntime() error: %v", err)
	}
	if cronService == nil {
		t.Fatal("expected non-nil cron service even when the tool is disabled")
	}
	if cronTool != nil {
		t.Fatal("expected no cron tool when cron is disabled")
	}
	if got := reg.names(); len(got) != 0 {
		t.Fatalf("expected no tools registered when cron is disabled, got %v", got)
	}
}

// TestBuildCronRuntime_DoesNotStartScheduler proves the safety contract: the
// shared builder registers the cron tool but must never start the scheduler
// loop or wire a job-execution callback. Scheduled execution stays the caller's
// responsibility (the gateway), so the inject-turn smoke path is side-effect
// free.
func TestBuildCronRuntime_DoesNotStartScheduler(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.Enabled = true

	params, _ := newCronBootstrapParams(t, cfg)
	cronService, _, err := BuildCronRuntime(params)
	if err != nil {
		t.Fatalf("BuildCronRuntime() error: %v", err)
	}
	if cronService.IsRunning() {
		t.Fatal("builder must not start the cron scheduler")
	}
}

// TestBuildCronRuntime_PreservesAllowCommandBehavior exercises the cron tool
// built by the shared path to confirm config-driven allow_command gating still
// flows through unchanged.
func TestBuildCronRuntime_PreservesAllowCommandBehavior(t *testing.T) {
	addCommandJob := func(t *testing.T, allowCommand bool) *ToolResult {
		t.Helper()
		cfg := config.DefaultConfig()
		cfg.Tools.Cron.Enabled = true
		cfg.Tools.Cron.AllowCommand = allowCommand

		params, _ := newCronBootstrapParams(t, cfg)
		_, cronTool, err := BuildCronRuntime(params)
		if err != nil {
			t.Fatalf("BuildCronRuntime() error: %v", err)
		}
		if cronTool == nil {
			t.Fatal("expected cron tool when cron is enabled")
		}
		ctx := WithToolContext(context.Background(), "cli", "direct")
		return cronTool.Execute(ctx, map[string]any{
			"action":     "add",
			"message":    "check disk",
			"command":    "df -h",
			"at_seconds": float64(60),
		})
	}

	t.Run("allowed by default", func(t *testing.T) {
		result := addCommandJob(t, true)
		if result.IsError {
			t.Fatalf("expected command scheduling to succeed when allow_command is true, got: %s", result.ForLLM)
		}
		if !strings.Contains(result.ForLLM, "Cron job added") {
			t.Errorf("expected 'Cron job added', got: %s", result.ForLLM)
		}
	})

	t.Run("requires confirm when disabled", func(t *testing.T) {
		result := addCommandJob(t, false)
		if !result.IsError {
			t.Fatal("expected command scheduling to require confirm when allow_command is disabled")
		}
		if !strings.Contains(result.ForLLM, "command_confirm=true") {
			t.Errorf("expected command_confirm requirement message, got: %s", result.ForLLM)
		}
	})
}
