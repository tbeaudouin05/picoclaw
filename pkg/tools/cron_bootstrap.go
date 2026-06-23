package tools

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
)

// CronToolRegistrar registers a tool with every agent so scheduled and ad-hoc
// turns can invoke it. *agent.AgentLoop satisfies this interface.
type CronToolRegistrar interface {
	RegisterTool(Tool)
}

// CronRuntimeParams carries the inputs shared by the gateway and the runtime
// inject-turn helper when constructing the cron runtime.
type CronRuntimeParams struct {
	// Executor runs scheduled cron jobs through the agent. Required when the
	// cron tool is enabled.
	Executor JobExecutor
	// Registrar receives the cron tool so the agent can call it. May be nil to
	// build the service without exposing the tool.
	Registrar   CronToolRegistrar
	MsgBus      *bus.MessageBus
	Workspace   string
	Restrict    bool
	ExecTimeout time.Duration
	Config      *config.Config
}

// BuildCronRuntime creates the cron service and, when the cron tool is enabled,
// constructs and registers the cron tool on the provided registrar.
//
// It deliberately does NOT start the scheduler or wire job-execution callbacks:
// that lifecycle stays with callers that genuinely want scheduled execution
// (the gateway, via cron.CronService.SetOnJob + Start). The runtime inject-turn
// smoke path uses this shared builder to expose the same cron tool surface as
// the gateway without ever running the scheduler.
//
// The returned *CronTool is nil when the cron tool is disabled.
func BuildCronRuntime(p CronRuntimeParams) (*cron.CronService, *CronTool, error) {
	cronStorePath := filepath.Join(p.Workspace, "cron", "jobs.json")

	cronService := cron.NewCronService(cronStorePath, nil)
	maxConcurrent := 1
	if p.Config != nil {
		maxConcurrent = p.Config.Tools.Cron.EffectiveMaxConcurrent()
	}
	cronService.SetMaxConcurrentJobs(maxConcurrent)

	if p.Config == nil || !p.Config.Tools.IsToolEnabled("cron") {
		return cronService, nil, nil
	}

	cronTool, err := NewCronTool(
		cronService, p.Executor, p.MsgBus, p.Workspace, p.Restrict, p.ExecTimeout, p.Config,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("critical error during CronTool initialization: %w", err)
	}
	if p.Registrar != nil {
		p.Registrar.RegisterTool(cronTool)
	}

	return cronService, cronTool, nil
}
