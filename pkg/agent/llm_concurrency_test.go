// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestNewLLMConcurrencyLimiter verifies the semaphore is nil (unlimited) only
// when the configured limit is <= 0, and is sized to the limit otherwise.
func TestNewLLMConcurrencyLimiter(t *testing.T) {
	cases := []struct {
		name        string
		limit       int
		timeoutSecs int
		wantCap     int // -1 == nil channel
		wantTimeout time.Duration
	}{
		{"unlimited default timeout", 0, 0, -1, config.DefaultLLMSlotWaitTimeout},
		{"negative is unlimited", -3, 5, -1, 5 * time.Second},
		{"limited with custom timeout", 4, 12, 4, 12 * time.Second},
		{"limit one default timeout", 1, 0, 1, config.DefaultLLMSlotWaitTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Agents.Defaults.MaxConcurrentLLMCalls = tc.limit
			cfg.Agents.Defaults.LLMSlotWaitTimeout = tc.timeoutSecs

			sem, timeout := newLLMConcurrencyLimiter(cfg)
			if tc.wantCap < 0 {
				if sem != nil {
					t.Errorf("expected nil semaphore for limit %d, got cap %d", tc.limit, cap(sem))
				}
			} else {
				if sem == nil {
					t.Fatalf("expected semaphore with cap %d, got nil", tc.wantCap)
				}
				if cap(sem) != tc.wantCap {
					t.Errorf("semaphore cap = %d, want %d", cap(sem), tc.wantCap)
				}
			}
			if timeout != tc.wantTimeout {
				t.Errorf("timeout = %s, want %s", timeout, tc.wantTimeout)
			}
		})
	}
}

// TestAcquireLLMSlotUnlimited verifies the unlimited path never blocks and
// returns a usable no-op release.
func TestAcquireLLMSlotUnlimited(t *testing.T) {
	al := &AgentLoop{} // nil llmSem == unlimited

	for i := 0; i < 100; i++ {
		release, err := al.acquireLLMSlot(context.Background())
		if err != nil {
			t.Fatalf("acquire %d: unexpected error %v", i, err)
		}
		if release == nil {
			t.Fatalf("acquire %d: nil release", i)
		}
		release()
	}
}

// TestAcquireLLMSlotLimitsConcurrency verifies that no more than the configured
// number of slots are ever held simultaneously under heavy contention.
func TestAcquireLLMSlotLimitsConcurrency(t *testing.T) {
	const limit = 3
	const workers = 40

	al := &AgentLoop{
		llmSem:             make(chan struct{}, limit),
		llmSlotWaitTimeout: 5 * time.Second,
	}

	var inFlight atomic.Int32
	var peak atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := al.acquireLLMSlot(context.Background())
			if err != nil {
				t.Errorf("acquire: unexpected error %v", err)
				return
			}
			defer release()

			cur := inFlight.Add(1)
			defer inFlight.Add(-1)
			// Track the high-water mark.
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Errorf("observed peak concurrency %d, want <= %d", got, limit)
	}
	if got := peak.Load(); got == 0 {
		t.Errorf("observed peak concurrency 0, expected work to run")
	}
	if got := inFlight.Load(); got != 0 {
		t.Errorf("in-flight = %d after completion, want 0 (slots leaked)", got)
	}
}

// TestAcquireLLMSlotTimeout verifies a full pool fails the LLM step with
// ErrLLMSlotUnavailable once the slot-wait timeout elapses.
func TestAcquireLLMSlotTimeout(t *testing.T) {
	al := &AgentLoop{
		llmSem:             make(chan struct{}, 1),
		llmSlotWaitTimeout: 30 * time.Millisecond,
	}

	// Fill the only slot.
	release, err := al.acquireLLMSlot(context.Background())
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer release()

	start := time.Now()
	release2, err := al.acquireLLMSlot(context.Background())
	elapsed := time.Since(start)

	if release2 != nil {
		t.Errorf("expected nil release on timeout, got non-nil")
	}
	if !errors.Is(err, ErrLLMSlotUnavailable) {
		t.Fatalf("error = %v, want ErrLLMSlotUnavailable", err)
	}
	if elapsed < 25*time.Millisecond {
		t.Errorf("returned after %s, expected to wait ~30ms", elapsed)
	}
}

// TestAcquireLLMSlotContextCanceledWhileWaiting verifies cancellation during
// the wait returns ctx.Err() rather than the timeout error.
func TestAcquireLLMSlotContextCanceledWhileWaiting(t *testing.T) {
	al := &AgentLoop{
		llmSem:             make(chan struct{}, 1),
		llmSlotWaitTimeout: 5 * time.Second,
	}

	release, err := al.acquireLLMSlot(context.Background())
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	release2, err := al.acquireLLMSlot(ctx)
	if release2 != nil {
		t.Errorf("expected nil release on cancellation, got non-nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestAcquireLLMSlotContextAlreadyCanceled verifies the fast cancellation path.
func TestAcquireLLMSlotContextAlreadyCanceled(t *testing.T) {
	al := &AgentLoop{
		llmSem:             make(chan struct{}, 1),
		llmSlotWaitTimeout: 5 * time.Second,
	}

	// Fill the slot so the fast acquire path misses.
	release, err := al.acquireLLMSlot(context.Background())
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	release2, err := al.acquireLLMSlot(ctx)
	if release2 != nil {
		t.Errorf("expected nil release for canceled context, got non-nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestAcquireLLMSlotReleaseFreesSlot verifies a waiter proceeds once a held slot
// is released before the timeout.
func TestAcquireLLMSlotReleaseFreesSlot(t *testing.T) {
	al := &AgentLoop{
		llmSem:             make(chan struct{}, 1),
		llmSlotWaitTimeout: 2 * time.Second,
	}

	release, err := al.acquireLLMSlot(context.Background())
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := al.acquireLLMSlot(context.Background())
		if err != nil {
			t.Errorf("waiter acquire: unexpected error %v", err)
			return
		}
		release2()
		close(acquired)
	}()

	// Give the waiter time to block, then free the slot.
	time.Sleep(20 * time.Millisecond)
	release()

	select {
	case <-acquired:
		// success
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire slot after release")
	}
}
