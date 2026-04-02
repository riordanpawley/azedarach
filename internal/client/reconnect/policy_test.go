package reconnect

import (
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
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

func TestIsTransientTransportError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "dial unix", err: errString("dial unix /tmp/daemon.sock: connect: connection refused"), want: true},
		{name: "daemon socket unavailable", err: errString("daemon socket unavailable: stat /tmp/daemon.sock: no such file or directory"), want: true},
		{name: "permission denied", err: errString("permission denied"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientTransportError(tc.err); got != tc.want {
				t.Fatalf("IsTransientTransportError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultReconciliationPolicyDecidesProjectionRecovery(t *testing.T) {
	policy := DefaultReconciliationPolicy()
	if policy.ReattachRetryInterval != DefaultReattachRetryInterval {
		t.Fatalf("retry interval = %s, want %s", policy.ReattachRetryInterval, DefaultReattachRetryInterval)
	}
	cursor := protocol.StreamCursor{Revision: 4}

	tests := []struct {
		name string
		evt  protocol.EventEnvelope
		want ProjectionReconciliationAction
	}{
		{
			name: "duplicate ignored",
			evt:  protocol.EventEnvelope{Revision: 4, Event: "task.updated"},
			want: ProjectionReconciliationIgnore,
		},
		{
			name: "sequential refreshes snapshot",
			evt:  protocol.EventEnvelope{Revision: 5, Event: "task.updated"},
			want: ProjectionReconciliationRefreshSnapshot,
		},
		{
			name: "gap rehydrates",
			evt:  protocol.EventEnvelope{Revision: 7, Event: "task.updated"},
			want: ProjectionReconciliationRehydrate,
		},
		{
			name: "runtime events ignored",
			evt:  protocol.EventEnvelope{Revision: 5, Event: protocol.EventGitStatusUpdated},
			want: ProjectionReconciliationIgnore,
		},
		{
			name: "worktree projection events ignored",
			evt:  protocol.EventEnvelope{Revision: 5, Event: protocol.EventWorktreeProjectionUpdated},
			want: ProjectionReconciliationIgnore,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideProjectionAction(cursor, tc.evt); got != tc.want {
				t.Fatalf("DecideProjectionAction() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconciliationPolicyShouldQueueReattach(t *testing.T) {
	policy := DefaultReconciliationPolicy()
	now := time.Now()
	transient := errString("daemon command transport: dial unix /tmp/daemon.sock: connect: connection refused")

	if !policy.ShouldQueueReattach(time.Time{}, now, transient) {
		t.Fatal("expected initial transient error to queue reattach")
	}
	if policy.ShouldQueueReattach(now.Add(-policy.ReattachRetryInterval/2), now, transient) {
		t.Fatal("did not expect queue reattach within retry interval")
	}
	if !policy.ShouldQueueReattach(now.Add(-policy.ReattachRetryInterval-time.Second), now, transient) {
		t.Fatal("expected queue reattach after retry interval")
	}
	if policy.ShouldQueueReattach(time.Time{}, now, errString("permission denied")) {
		t.Fatal("did not expect queue reattach for permanent error")
	}
}

func errString(s string) error { return stringErr(s) }

type stringErr string

func (e stringErr) Error() string { return string(e) }
