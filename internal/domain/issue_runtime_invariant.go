package domain

import "fmt"

type ManagedRuntimeLifecycleAction string

const (
	ManagedRuntimeLifecycleNoop          ManagedRuntimeLifecycleAction = "noop"
	ManagedRuntimeLifecycleRepairWorking ManagedRuntimeLifecycleAction = "repair_working"
	ManagedRuntimeLifecycleReject        ManagedRuntimeLifecycleAction = "reject"
)

// EvaluateManagedRuntimeLifecycle is the shared semantic boundary between
// durable issue intent and an observed live managed runtime. Callers must only
// pass live=true after consulting runtime authority (tmux for daemon sessions).
func EvaluateManagedRuntimeLifecycle(state IssueState, live bool) (ManagedRuntimeLifecycleAction, error) {
	if err := state.Validate(); err != nil {
		return ManagedRuntimeLifecycleReject, fmt.Errorf("invalid durable issue state: %w", err)
	}
	if !live {
		return ManagedRuntimeLifecycleNoop, nil
	}
	if state.Visibility == IssueVisibilityArchived {
		return ManagedRuntimeLifecycleReject, fmt.Errorf("live managed runtime conflicts with archived issue")
	}
	switch state.Disposition {
	case IssueDispositionReady:
		if state.Engagement == IssueEngagementIdle {
			return ManagedRuntimeLifecycleRepairWorking, nil
		}
		return ManagedRuntimeLifecycleNoop, nil
	case IssueDispositionBacklog:
		return ManagedRuntimeLifecycleReject, fmt.Errorf("live managed runtime conflicts with backlog issue")
	case IssueDispositionCompleted, IssueDispositionCancelled:
		return ManagedRuntimeLifecycleReject, fmt.Errorf("live managed runtime conflicts with terminal issue (%s)", state.Disposition)
	default:
		return ManagedRuntimeLifecycleReject, fmt.Errorf("live managed runtime conflicts with unknown issue disposition %q", state.Disposition)
	}
}
