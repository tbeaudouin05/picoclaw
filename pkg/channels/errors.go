package channels

import (
	"errors"
	"time"
)

var (
	// ErrNotRunning indicates the channel is not running.
	// Manager will not retry.
	ErrNotRunning = errors.New("channel not running")

	// ErrRateLimit indicates the platform returned a rate-limit response (e.g. HTTP 429).
	// Manager retries after a channel-provided RetryAfter delay when available,
	// otherwise it uses a short fixed delay.
	ErrRateLimit = errors.New("rate limited")

	// ErrTemporary indicates a transient failure (e.g. network timeout, 5xx).
	// Manager will use exponential backoff and retry.
	ErrTemporary = errors.New("temporary failure")

	// ErrSendFailed indicates a permanent failure (e.g. invalid chat ID, 4xx non-429).
	// Manager will not retry.
	ErrSendFailed = errors.New("send failed")
)

// RetryAfterError wraps a rate-limit error with a platform-provided retry delay.
// Channel implementations can return this when an API response includes a
// Retry-After value (for example Telegram 429 "retry after N").
type RetryAfterError struct {
	Err   error
	After time.Duration
}

func (e RetryAfterError) Error() string {
	if e.Err == nil {
		return ErrRateLimit.Error()
	}
	return e.Err.Error()
}

func (e RetryAfterError) Unwrap() error {
	if e.Err == nil {
		return ErrRateLimit
	}
	return e.Err
}

// RetryAfter returns the bounded retry delay advertised by the channel API.
func (e RetryAfterError) RetryAfter() time.Duration {
	if e.After <= 0 {
		return rateLimitDelay
	}
	return e.After
}

// WithRetryAfter annotates a rate-limit error with an API-provided retry delay.
func WithRetryAfter(err error, after time.Duration) error {
	if err == nil {
		err = ErrRateLimit
	}
	return RetryAfterError{Err: err, After: after}
}

func retryAfter(err error) (time.Duration, bool) {
	var retryErr interface{ RetryAfter() time.Duration }
	if errors.As(err, &retryErr) {
		return retryErr.RetryAfter(), true
	}
	return 0, false
}
