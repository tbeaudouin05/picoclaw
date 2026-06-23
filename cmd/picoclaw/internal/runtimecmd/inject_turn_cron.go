package runtimecmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// buildRuntimeCronTool constructs the cron tool capability for an injected
// runtime turn, mirroring the gateway's cron wiring (see setupCronTool) so an
// injected admin or customer turn can create/list/disable/remove cron jobs in
// the normal runtime cron store at <workspace>/cron/jobs.json.
//
// It deliberately does NOT start a scheduler loop or register a SetOnJob
// callback: inject-turn is a one-shot smoke-test path and must never begin
// executing scheduled jobs. When tools.cron is disabled it returns (nil, nil)
// so no tool is registered, and allow_command behaviour is preserved by passing
// cfg straight through to NewCronTool.
func buildRuntimeCronTool(
	cfg *config.Config, executor tools.JobExecutor, msgBus *bus.MessageBus,
) (*tools.CronTool, error) {
	if cfg == nil || !cfg.Tools.IsToolEnabled("cron") {
		return nil, nil
	}

	workspace := cfg.WorkspacePath()
	cronStorePath := filepath.Join(workspace, "cron", "jobs.json")
	cronService := cron.NewCronService(cronStorePath, nil)
	cronService.SetMaxConcurrentJobs(cfg.Tools.Cron.EffectiveMaxConcurrent())

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	cronTool, err := tools.NewCronTool(
		cronService,
		executor,
		msgBus,
		workspace,
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("runtime cron tool init: %w", err)
	}
	return cronTool, nil
}
