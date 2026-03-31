package linearsync

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidWorkerLifecycle  = errors.New("invalid worker lifecycle")
	ErrInvalidWorkerTransition = errors.New("invalid worker transition")
)

type WorkerState string

const (
	WorkerStateStarting WorkerState = "starting"
	WorkerStateHealthy  WorkerState = "healthy"
	WorkerStateDegraded WorkerState = "degraded"
	WorkerStateRetrying WorkerState = "retrying"
	WorkerStateStopped  WorkerState = "stopped"
)

type WorkerTrigger string

const (
	WorkerTriggerHealthy  WorkerTrigger = "healthy"
	WorkerTriggerDegraded WorkerTrigger = "degraded"
	WorkerTriggerRetrying WorkerTrigger = "retrying"
	WorkerTriggerStopped  WorkerTrigger = "stopped"
)

type ProjectWorkerLifecycle struct {
	ProjectID string
	State     WorkerState
}

func NewProjectWorkerLifecycle(projectID string) (ProjectWorkerLifecycle, error) {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" {
		return ProjectWorkerLifecycle{}, fmt.Errorf("%w: missing project id", ErrInvalidWorkerLifecycle)
	}

	return ProjectWorkerLifecycle{
		ProjectID: trimmed,
		State:     WorkerStateStarting,
	}, nil
}

func (l ProjectWorkerLifecycle) Transition(trigger WorkerTrigger) (ProjectWorkerLifecycle, error) {
	nextState, err := nextWorkerState(l.State, trigger)
	if err != nil {
		return ProjectWorkerLifecycle{}, err
	}

	l.State = nextState
	return l, nil
}

func (l ProjectWorkerLifecycle) ResolveFallback(status WebhookFallbackStatus) ProjectFallbackDecision {
	return ResolveProjectFallback(l.ProjectID, l.State, status)
}

func nextWorkerState(current WorkerState, trigger WorkerTrigger) (WorkerState, error) {
	switch trigger {
	case WorkerTriggerHealthy:
		switch current {
		case WorkerStateStarting, WorkerStateHealthy, WorkerStateDegraded, WorkerStateRetrying:
			return WorkerStateHealthy, nil
		case WorkerStateStopped:
			return "", fmt.Errorf("%w: %s -> %s", ErrInvalidWorkerTransition, current, trigger)
		default:
			return "", fmt.Errorf("%w: unknown current state %q", ErrInvalidWorkerTransition, current)
		}
	case WorkerTriggerDegraded:
		switch current {
		case WorkerStateStarting, WorkerStateHealthy, WorkerStateDegraded, WorkerStateRetrying:
			return WorkerStateDegraded, nil
		case WorkerStateStopped:
			return "", fmt.Errorf("%w: %s -> %s", ErrInvalidWorkerTransition, current, trigger)
		default:
			return "", fmt.Errorf("%w: unknown current state %q", ErrInvalidWorkerTransition, current)
		}
	case WorkerTriggerRetrying:
		switch current {
		case WorkerStateDegraded, WorkerStateRetrying:
			return WorkerStateRetrying, nil
		default:
			return "", fmt.Errorf("%w: %s -> %s", ErrInvalidWorkerTransition, current, trigger)
		}
	case WorkerTriggerStopped:
		return WorkerStateStopped, nil
	default:
		return "", fmt.Errorf("%w: unknown trigger %q", ErrInvalidWorkerTransition, trigger)
	}
}

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxExponent int
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: defaultMaxSyncAttempts,
		BaseDelay:   time.Duration(baseRetryDelaySeconds) * time.Second,
		MaxExponent: 8,
	}.Normalize()
}

func (p RetryPolicy) Normalize() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultMaxSyncAttempts
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = time.Duration(baseRetryDelaySeconds) * time.Second
	}
	if p.MaxExponent < 0 {
		p.MaxExponent = 0
	}
	return p
}

func (p RetryPolicy) CanRetry(attempts int) bool {
	p = p.Normalize()
	return normalizedAttempt(attempts)+1 < p.MaxAttempts
}

func (p RetryPolicy) DelayForAttempt(attempts int) time.Duration {
	p = p.Normalize()

	exponent := normalizedAttempt(attempts)
	if exponent > p.MaxExponent {
		exponent = p.MaxExponent
	}

	return p.BaseDelay * time.Duration(1<<exponent)
}

func (p RetryPolicy) DelaySeconds(attempts int) int {
	return int(p.DelayForAttempt(attempts) / time.Second)
}

type ProjectFallbackDecision struct {
	ProjectID string
	State     WorkerState
	Active    bool
	Reason    string
}

func ResolveProjectFallback(projectID string, state WorkerState, status WebhookFallbackStatus) ProjectFallbackDecision {
	trimmedProjectID := strings.TrimSpace(projectID)
	decision := ProjectFallbackDecision{
		ProjectID: trimmedProjectID,
		State:     state,
	}

	switch state {
	case WorkerStateStarting, WorkerStateHealthy:
		decision.Reason = fmt.Sprintf("project %s remains on primary transport while worker is %s", fallbackProjectLabel(trimmedProjectID), state)
		return decision
	case WorkerStateDegraded, WorkerStateRetrying:
		if status.Healthy {
			decision.Reason = fmt.Sprintf("project %s transport is healthy while worker is %s", fallbackProjectLabel(trimmedProjectID), state)
			return decision
		}

		decision.Active = true
		if reason := status.NormalizedReason(); reason != "" {
			decision.Reason = fmt.Sprintf("project %s switched to fallback while worker is %s: %s", fallbackProjectLabel(trimmedProjectID), state, reason)
			return decision
		}
		decision.Reason = fmt.Sprintf("project %s switched to fallback while worker is %s (mode=%s)", fallbackProjectLabel(trimmedProjectID), state, strings.TrimSpace(status.Mode))
		return decision
	case WorkerStateStopped:
		decision.Active = true
		if reason := status.NormalizedReason(); reason != "" {
			decision.Reason = fmt.Sprintf("project %s retains fallback while worker is stopped: %s", fallbackProjectLabel(trimmedProjectID), reason)
			return decision
		}
		decision.Reason = fmt.Sprintf("project %s retains fallback while worker is stopped (mode=%s)", fallbackProjectLabel(trimmedProjectID), strings.TrimSpace(status.Mode))
		return decision
	default:
		decision.Reason = fmt.Sprintf("project %s has unknown worker state %q", fallbackProjectLabel(trimmedProjectID), state)
		return decision
	}
}

func fallbackProjectLabel(projectID string) string {
	if projectID == "" {
		return "<unknown>"
	}
	return projectID
}
