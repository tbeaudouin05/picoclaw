package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func telegramSessionModelOverride(agent *AgentInstance, sessionKey, channel string) string {
	if agent == nil || !strings.EqualFold(strings.TrimSpace(channel), "telegram") {
		return ""
	}
	store, ok := agent.Sessions.(sessionModelOverrideStore)
	if !ok {
		return ""
	}
	return strings.TrimSpace(store.GetModelOverride(sessionKey))
}

type sessionModelOverrideStore interface {
	GetModelOverride(sessionKey string) string
	SetModelOverride(sessionKey, model string) error
}

func configuredModel(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	for _, model := range cfg.ModelList {
		if model != nil && model.ModelName == name {
			return true
		}
	}
	return false
}

// agentWithTelegramModelOverride returns a per-turn model view. Provider
// instances are cached on the routed agent, but its active model is never
// mutated, so another Telegram chat (or another channel) cannot inherit it.
func (al *AgentLoop) agentWithTelegramModelOverride(agent *AgentInstance, model string) (*AgentInstance, error) {
	model = strings.TrimSpace(model)
	if model == "" || agent == nil {
		return agent, nil
	}
	base := agent
	if agent.modelOverrideBase != nil {
		base = agent.modelOverrideBase
	}
	cfg := al.GetConfig()
	if !configuredModel(cfg, model) {
		return nil, fmt.Errorf("model %q not found in model_list or providers", model)
	}

	nextCandidates := resolveModelCandidates(cfg, cfg.Agents.Defaults.Provider, model, base.Fallbacks)
	if len(nextCandidates) == 0 {
		return nil, fmt.Errorf("model %q did not resolve to any provider candidates", model)
	}
	cacheMu := base.candidateProviderCacheMutex()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if model == base.Model {
		return base, nil
	}
	if base.CandidateProviders == nil {
		base.CandidateProviders = make(map[string]providers.LLMProvider)
	}
	nextProvider := base.CandidateProviders[candidateProviderKey(nextCandidates[0])]
	if nextProvider == nil {
		modelCfg, err := resolvedCandidateModelConfig(cfg, nextCandidates[0], base.Workspace)
		if err != nil {
			return nil, err
		}
		factory := al.providerFactory
		if factory == nil {
			factory = providers.CreateProviderFromConfig
		}
		nextProvider, _, err = factory(modelCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize model %q: %w", model, err)
		}
		base.CandidateProviders[candidateProviderKey(nextCandidates[0])] = nextProvider
	}
	inheritPrimaryProviderForCandidates(
		cfg, base.Workspace, nextCandidates[0], nextCandidates[1:], nextProvider, base.CandidateProviders,
	)
	populateCandidateProvidersFromCandidates(cfg, base.Workspace, nextCandidates[1:], base.CandidateProviders)

	modelCfg, err := resolvedCandidateModelConfig(cfg, nextCandidates[0], base.Workspace)
	if err != nil {
		return nil, err
	}
	view := *base
	view.modelMu = &sync.RWMutex{}
	view.candidateProvidersMu = &sync.Mutex{}
	view.modelOverrideBase = base
	view.Model = model
	view.Provider = nextProvider
	view.Candidates = nextCandidates
	view.ThinkingLevel = parseThinkingLevel(modelCfg.ThinkingLevel)
	view.ThinkingLevelConfigured = isConfiguredThinkingLevel(modelCfg.ThinkingLevel)
	// A chat's explicit selection has precedence over automatic light-model routing.
	view.Router = nil
	view.LightCandidates = nil
	view.LightProvider = nil
	view.CandidateProviders = make(map[string]providers.LLMProvider, len(base.CandidateProviders))
	for key, provider := range base.CandidateProviders {
		view.CandidateProviders[key] = provider
	}
	return &view, nil
}
