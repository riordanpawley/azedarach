package linearsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type RetryableError interface {
	error
	Retryable() bool
}

type DispatchItem struct {
	IssueID     string
	ExternalRef domain.ExternalIssueRef
	Operation   Operation
	Attempts    int
	Work        func(context.Context) error
}

type DispatchOutcome struct {
	IssueID     string
	ExternalRef domain.ExternalIssueRef
	Operation   Operation
	Attempts    int
	Retried     bool
	Err         error
}

type FlushOptions struct {
	RunID       string
	ProjectPath string
}

type Runner struct {
	logger      *slog.Logger
	retryPolicy RetryPolicy
	now         func() time.Time
}

func NewRunner(logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		logger:      logger,
		retryPolicy: DefaultRetryPolicy(),
		now:         time.Now,
	}
}

func NewRunnerWithMaxAttempts(logger *slog.Logger, maxAttempts int) *Runner {
	runner := NewRunner(logger)
	runner.retryPolicy.MaxAttempts = normalizedMaxAttempts(maxAttempts)
	runner.retryPolicy = runner.retryPolicy.Normalize()
	return runner
}

func (r *Runner) Flush(ctx context.Context, opts FlushOptions, items []DispatchItem) []DispatchOutcome {
	if r == nil {
		return nil
	}
	if opts.RunID == "" {
		opts.RunID = r.now().UTC().Format("20060102150405.000000000")
	}

	logFlushRunStart(r.logger, opts.RunID, opts.ProjectPath, len(items))
	if len(items) == 0 {
		logFlushSkipped(r.logger, opts.RunID, opts.ProjectPath, "no_pending_items")
		return nil
	}

	outcomes := make([]DispatchOutcome, 0, len(items))
	for _, item := range items {
		attempts := normalizedAttempt(item.Attempts)
		if item.Work == nil {
			err := errors.New("missing work func")
			logDispatchTerminalFailure(r.logger, opts.RunID, opts.ProjectPath, item, r.retryPolicy.MaxAttempts, err)
			outcomes = append(outcomes, DispatchOutcome{
				IssueID:     item.IssueID,
				ExternalRef: item.ExternalRef,
				Operation:   item.Operation,
				Attempts:    attempts + 1,
				Err:         err,
			})
			continue
		}

		logDispatchStart(r.logger, opts.RunID, opts.ProjectPath, item, r.retryPolicy.MaxAttempts)
		err := item.Work(ctx)
		if err == nil {
			logDispatchSuccess(r.logger, opts.RunID, opts.ProjectPath, item, r.retryPolicy.MaxAttempts)
			outcomes = append(outcomes, DispatchOutcome{
				IssueID:     item.IssueID,
				ExternalRef: item.ExternalRef,
				Operation:   item.Operation,
				Attempts:    attempts + 1,
			})
			continue
		}

		if isRetryable(err) && r.retryPolicy.CanRetry(attempts) {
			delaySeconds := r.retryPolicy.DelaySeconds(attempts)
			logDispatchRetryScheduled(r.logger, opts.RunID, opts.ProjectPath, item, r.retryPolicy.MaxAttempts, delaySeconds, err)
			outcomes = append(outcomes, DispatchOutcome{
				IssueID:     item.IssueID,
				ExternalRef: item.ExternalRef,
				Operation:   item.Operation,
				Attempts:    attempts + 1,
				Retried:     true,
				Err:         err,
			})
			continue
		}

		logDispatchTerminalFailure(r.logger, opts.RunID, opts.ProjectPath, item, r.retryPolicy.MaxAttempts, err)
		outcomes = append(outcomes, DispatchOutcome{
			IssueID:     item.IssueID,
			ExternalRef: item.ExternalRef,
			Operation:   item.Operation,
			Attempts:    attempts + 1,
			Err:         err,
		})
	}

	return outcomes
}

func isRetryable(err error) bool {
	var retryable RetryableError
	return errors.As(err, &retryable) && retryable.Retryable()
}

func (o DispatchOutcome) String() string {
	return fmt.Sprintf("issue=%s external_provider=%s external_remote=%s external_display=%s operation=%s attempts=%d retried=%t err=%v", o.IssueID, o.ExternalRef.Provider, o.ExternalRef.RemoteKey, o.ExternalRef.DisplayKey, o.Operation, o.Attempts, o.Retried, o.Err)
}
