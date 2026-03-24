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

func TestPolicyShouldRetry(t *testing.T) {
	p := Policy{MaxAttempts: 3}
	if !p.ShouldRetry(0) || !p.ShouldRetry(2) {
		t.Fatal("expected attempts 0 and 2 to be retryable")
	}
	if p.ShouldRetry(3) {
		t.Fatal("attempt 3 should not be retryable")
	}
}
