package reconnect

import (
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const DefaultReattachRetryInterval = 5 * time.Second

// ProjectionReconciliationAction describes how a client should recover from a stream event drift condition.
type ProjectionReconciliationAction uint8

const (
	ProjectionReconciliationIgnore ProjectionReconciliationAction = iota
	ProjectionReconciliationRefreshSnapshot
	ProjectionReconciliationRehydrate
)

// ReconciliationPolicy controls deterministic stream and reattach recovery behavior.
type ReconciliationPolicy struct {
	ReattachRetryInterval time.Duration
}

// DefaultReconciliationPolicy returns conservative deterministic recovery defaults.
func DefaultReconciliationPolicy() ReconciliationPolicy {
	return ReconciliationPolicy{
		ReattachRetryInterval: DefaultReattachRetryInterval,
	}
}

func (p ReconciliationPolicy) normalize() ReconciliationPolicy {
	if p.ReattachRetryInterval <= 0 {
		p.ReattachRetryInterval = DefaultReattachRetryInterval
	}
	return p
}

// ShouldQueueReattach reports whether a daemon reattach should be scheduled for the given error.
func (p ReconciliationPolicy) ShouldQueueReattach(lastAttempt, now time.Time, err error) bool {
	if !IsTransientTransportError(err) {
		return false
	}
	p = p.normalize()
	if lastAttempt.IsZero() {
		return true
	}
	return now.Sub(lastAttempt) >= p.ReattachRetryInterval
}

// DecideProjectionAction turns a stream revision/cursor mismatch into a deterministic recovery action.
//
// Worktree and git projection events are handled by the specialized projection path and are ignored here.
func DecideProjectionAction(cursor protocol.StreamCursor, evt protocol.EventEnvelope) ProjectionReconciliationAction {
	switch evt.Event {
	case protocol.EventWorktreeProjectionUpdated, protocol.EventGitStatusUpdated:
		return ProjectionReconciliationIgnore
	}

	switch cursor.Decide(evt) {
	case protocol.StreamProjectionDecisionIgnore:
		return ProjectionReconciliationIgnore
	case protocol.StreamProjectionDecisionResync:
		return ProjectionReconciliationRehydrate
	default:
		return ProjectionReconciliationRefreshSnapshot
	}
}

// IsTransientTransportError reports whether the transport error is a transient reconnect candidate.
func IsTransientTransportError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "daemon socket unavailable") {
		return true
	}
	return strings.Contains(message, "dial unix") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no such file or directory") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "eof")
}
