package agent

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/school"
)

func (al *AgentLoop) getSchoolRuntime() *school.Runtime {
	if al == nil {
		return nil
	}
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.schoolRuntime
}

func (al *AgentLoop) injectRuntimeSchoolConfig(ctx context.Context, opts *processOptions) error {
	runtime := al.getSchoolRuntime()
	if runtime == nil || opts == nil {
		return nil
	}
	normalizeProcessOptionsInPlace(opts)
	if !runtime.ShouldInject(opts.Channel) {
		return nil
	}
	prompt, err := runtime.RuntimePrompt(ctx)
	if err != nil {
		logger.ErrorCF("agent", "Runtime school config unavailable", map[string]any{
			"error":   err.Error(),
			"channel": opts.Channel,
		})
		prompt = "AUTHORITATIVE RUNTIME SCHOOL CONFIG UNAVAILABLE\n\nThe configured Turso school database could not be read for this message. Do not use filesystem school config, school-info.json, setup.json, memory copies, or earlier conversation config as a fallback. Do not invent school offerings, prices, policies, schedules, identity, or personality. Tell the user briefly that the latest school information is temporarily unavailable and a human can follow up."
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	if existing := strings.TrimSpace(opts.SystemPromptOverride); existing != "" {
		opts.SystemPromptOverride = prompt + "\n\n" + existing
	} else {
		opts.SystemPromptOverride = prompt
	}
	return nil
}
