package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/liveconfig"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func (al *AgentLoop) getLiveConfigRuntime() *liveconfig.Runtime {
	if al == nil {
		return nil
	}
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.liveConfigRuntime
}

func (al *AgentLoop) injectRuntimeLiveConfig(ctx context.Context, opts *processOptions) error {
	runtime := al.getLiveConfigRuntime()
	if runtime == nil || opts == nil {
		return nil
	}
	normalizeProcessOptionsInPlace(opts)
	if !runtime.ShouldInject(opts.Channel) {
		return nil
	}
	prompt, err := runtime.RuntimePrompt(ctx)
	if err != nil {
		var notFound liveconfig.NotFoundError
		if errors.As(err, &notFound) {
			logger.WarnCF("agent", "Runtime live config record not found", map[string]any{
				"error":   err.Error(),
				"channel": opts.Channel,
			})
			return nil
		}
		logger.ErrorCF("agent", "Runtime live config unavailable", map[string]any{
			"error":   err.Error(),
			"channel": opts.Channel,
		})
		prompt = "AUTHORITATIVE LIVE CONFIG UNAVAILABLE\n\nThe configured live config database could not be read for this message. Do not use filesystem config, setup files, memory copies, cached details, or earlier conversation config as a fallback. Do not invent deployment-specific facts. Tell the user briefly that the latest authoritative information is temporarily unavailable and a human can follow up."
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
