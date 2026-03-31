package reconnect

import (
	"testing"
	"time"
)

func TestPolicyDelayCappedExponential(t *testing.T) {
	p := Policy{
		MaxAttempts: 4,
		BaseBackoff: 50 * time.Millisecond,
		MaxBackoff:  400 * time.Millisecond,
	}

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 50 * time.Millisecond},
		{attempt: 0, want: 50 * time.Millisecond},
		{attempt: 1, want: 100 * time.Millisecond},
		{attempt: 2, want: 200 * time.Millisecond},
		{attempt: 3, want: 400 * time.Millisecond},
		{attempt: 4, want: 400 * time.Millisecond},
	}

	for _, tc := range cases {
		if got := p.Delay(tc.attempt); got != tc.want {
			t.Fatalf("Delay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestPolicyDelayFallsBackToMaxBackoffOnOverflow(t *testing.T) {
	p := Policy{
		MaxAttempts: 2,
		BaseBackoff: time.Duration(1) << 62,
		MaxBackoff:  375 * time.Millisecond,
	}

	if got := p.Delay(1); got != p.MaxBackoff {
		t.Fatalf("Delay(1) = %s, want %s", got, p.MaxBackoff)
	}
}

func TestPolicyShouldRetry(t *testing.T) {
	p := Policy{MaxAttempts: 3}
	if !p.ShouldRetry(0) || !p.ShouldRetry(2) {
		t.Fatal("expected attempts 0 and 2 to be retryable")
	}
	if p.ShouldRetry(3) {
		t.Fatal("attempt 3 should not be retryable")
	}
}
