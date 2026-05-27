// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// ErrLLMSlotUnavailable is returned by acquireLLMSlot when no agent-loop LLM
// concurrency slot becomes free within the configured slot-wait timeout.
var ErrLLMSlotUnavailable = errors.New("no LLM concurrency slot available")

// newLLMConcurrencyLimiter builds the agent-loop LLM concurrency semaphore and the
// slot-wait timeout from config. A configured limit <= 0 returns a nil channel,
// which acquireLLMSlot treats as "unlimited".
func newLLMConcurrencyLimiter(cfg *config.Config) (chan struct{}, time.Duration) {
	timeout := config.DefaultLLMSlotWaitTimeout
	limit := 0
	if cfg != nil {
		timeout = cfg.Agents.Defaults.GetLLMSlotWaitTimeout()
		limit = cfg.Agents.Defaults.MaxConcurrentLLMCallsLimit()
	}
	if limit <= 0 {
		return nil, timeout
	}
	return make(chan struct{}, limit), timeout
}

// acquireLLMSlot blocks until an agent-loop LLM provider-call slot is free, the
// configured slot-wait timeout elapses, or ctx is canceled. It returns a
// release func that MUST be called exactly once to return the slot (it is safe
// to call on every path, including the unlimited one). When concurrency is
// unlimited (max_concurrent_llm_calls <= 0), it returns immediately with a
// no-op release.
//
// On timeout it returns ErrLLMSlotUnavailable; on cancellation it returns
// ctx.Err(). Both cases leave the returned release func nil, so callers must
// check the error before deferring the release.
func (al *AgentLoop) acquireLLMSlot(ctx context.Context) (func(), error) {
	sem := al.llmSem
	if sem == nil {
		return func() {}, nil
	}

	release := func() { <-sem }

	// Fast path: a slot is immediately available.
	select {
	case sem <- struct{}{}:
		return release, nil
	default:
	}

	// Respect an already-canceled context before starting to wait.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	timeout := al.llmSlotWaitTimeout
	if timeout <= 0 {
		timeout = config.DefaultLLMSlotWaitTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case sem <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf(
			"%w within %s (max_concurrent_llm_calls=%d)",
			ErrLLMSlotUnavailable, timeout, cap(sem),
		)
	}
}
