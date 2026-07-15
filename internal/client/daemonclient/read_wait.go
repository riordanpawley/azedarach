package daemonclient

import (
	"context"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
)

type ReadWaitMode string

const (
	ReadWaitModeDefault  ReadWaitMode = "default"
	ReadWaitModeExplicit ReadWaitMode = "explicit"
)

const (
	DefaultReadWaitBudget  = 2 * time.Second
	ExplicitReadWaitBudget = 5 * time.Second
)

type ReadWaitPolicy struct {
	Default  time.Duration
	Explicit time.Duration
}

func DefaultReadWaitPolicy() ReadWaitPolicy {
	return ReadWaitPolicy{}.Normalize()
}

func (p ReadWaitPolicy) Normalize() ReadWaitPolicy {
	if p.Default <= 0 {
		p.Default = DefaultReadWaitBudget
	}
	if p.Explicit <= 0 {
		p.Explicit = ExplicitReadWaitBudget
	}
	if p.Explicit <= p.Default {
		p.Explicit = p.Default + (p.Default / 2)
		if p.Explicit <= p.Default {
			p.Explicit = p.Default + time.Second
		}
	}
	return p
}

func (p ReadWaitPolicy) Budget(mode ReadWaitMode) time.Duration {
	p = p.Normalize()
	if mode == ReadWaitModeExplicit {
		return p.Explicit
	}
	return p.Default
}

func (p ReadWaitPolicy) effectiveBudget(ctx context.Context, mode ReadWaitMode) time.Duration {
	budget := p.Budget(mode)
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < budget {
			budget = remaining
		}
	}
	return budget
}

func (p ReadWaitPolicy) contextWithBudget(ctx context.Context, mode ReadWaitMode) (context.Context, context.CancelFunc, time.Duration) {
	budget := p.effectiveBudget(ctx, mode)
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	return waitCtx, cancel, budget
}

func (p ReadWaitPolicy) timeoutError(mode ReadWaitMode, budget time.Duration, err error) *ReadWaitTimeoutError {
	return &ReadWaitTimeoutError{
		Mode:   mode,
		Budget: budget,
		Hint:   fmt.Sprintf("Task snapshot read timed out after %s; keeping current local view", budget),
		Err:    err,
	}
}

func (p ReadWaitPolicy) tracedTimeoutError(ctx context.Context, command string, mode ReadWaitMode, budget time.Duration, startedAt time.Time, err error) *ReadWaitTimeoutError {
	latencytrace.LogPhaseContext(ctx, nil, "daemonclient", "task_snapshot.timeout", startedAt,
		"command", command, "outcome", "timeout", "budget_ms", budget.Milliseconds(), "error", err)
	return p.timeoutError(mode, budget, err)
}

type ReadWaitTimeoutError struct {
	Mode   ReadWaitMode
	Budget time.Duration
	Hint   string
	Err    error
}

func (e *ReadWaitTimeoutError) Error() string {
	if e == nil {
		return ""
	}
	if e.Hint != "" {
		return e.Hint
	}
	return fmt.Sprintf("Task snapshot read timed out after %s", e.Budget)
}

func (e *ReadWaitTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ReadWaitTimeoutError) Timeout() bool {
	return true
}

func (e *ReadWaitTimeoutError) Temporary() bool {
	return true
}
