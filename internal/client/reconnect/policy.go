package reconnect

import (
	"time"
)

// Policy defines deterministic reconnect behavior.
type Policy struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// DefaultPolicy returns conservative reconnect defaults.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts: 5,
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  2 * time.Second,
	}
}

// Delay returns retry delay for the given zero-based attempt.
func (p Policy) Delay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	backoff := p.BaseBackoff << attempt
	if backoff <= 0 {
		return p.MaxBackoff
	}
	if backoff > p.MaxBackoff {
		return p.MaxBackoff
	}
	return backoff
}

// ShouldRetry reports whether another attempt is allowed.
func (p Policy) ShouldRetry(attempt int) bool {
	return attempt < p.MaxAttempts
}
