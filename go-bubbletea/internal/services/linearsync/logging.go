package linearsync

import (
	"log/slog"
	"strings"
)

const (
	defaultMaxSyncAttempts = 5
	baseRetryDelaySeconds  = 5
)

type Operation string

const (
	OperationUpsert Operation = "upsert"
	OperationClose  Operation = "close"
)

type lifecycleContext struct {
	RunID         string
	ProjectPath   string
	PendingItems  int
	IssueID       string
	LinearIssueID string
	Operation     Operation
	Attempt       int
	MaxAttempts   int
	DelaySeconds  int
	Reason        string
}

func normalizedMaxAttempts(maxAttempts int) int {
	if maxAttempts <= 0 {
		return defaultMaxSyncAttempts
	}
	return maxAttempts
}

func normalizedAttempt(attempts int) int {
	if attempts < 0 {
		return 0
	}
	return attempts
}

func retryDelaySeconds(attempts int) int {
	return DefaultRetryPolicy().DelaySeconds(attempts)
}

func emitLifecycleLog(logger *slog.Logger, level slog.Level, message string, ctx lifecycleContext) {
	if logger == nil {
		logger = slog.Default()
	}
	if logger == nil {
		return
	}

	attrs := []any{
		"run", strings.TrimSpace(ctx.RunID),
		"project_path", strings.TrimSpace(ctx.ProjectPath),
		"pending_items", ctx.PendingItems,
		"issue_id", strings.TrimSpace(ctx.IssueID),
		"linear_issue_id", strings.TrimSpace(ctx.LinearIssueID),
		"operation", ctx.Operation,
		"attempts", ctx.Attempt,
		"max_attempts", ctx.MaxAttempts,
	}
	if ctx.DelaySeconds > 0 {
		attrs = append(attrs, "delay_seconds", ctx.DelaySeconds)
	}
	if strings.TrimSpace(ctx.Reason) != "" {
		attrs = append(attrs, "reason", strings.TrimSpace(ctx.Reason))
	}

	switch level {
	case slog.LevelWarn:
		logger.Warn(message, attrs...)
	case slog.LevelError:
		logger.Error(message, attrs...)
	default:
		logger.Info(message, attrs...)
	}
}

func logFlushRunStart(logger *slog.Logger, runID, projectPath string, pendingItems int) {
	emitLifecycleLog(logger, slog.LevelInfo, "Linear flush run start", lifecycleContext{
		RunID:        runID,
		ProjectPath:  projectPath,
		PendingItems: pendingItems,
	})
}

func logFlushSkipped(logger *slog.Logger, runID, projectPath, reason string) {
	emitLifecycleLog(logger, slog.LevelInfo, "Linear flush skipped", lifecycleContext{
		RunID:       runID,
		ProjectPath: projectPath,
		Reason:      reason,
	})
}

func logDispatchStart(logger *slog.Logger, runID, projectPath string, item DispatchItem, maxAttempts int) {
	emitLifecycleLog(logger, slog.LevelInfo, "Linear sync dispatch start", lifecycleContext{
		RunID:         runID,
		ProjectPath:   projectPath,
		IssueID:       item.IssueID,
		LinearIssueID: item.LinearIssueID,
		Operation:     item.Operation,
		Attempt:       normalizedAttempt(item.Attempts) + 1,
		MaxAttempts:   normalizedMaxAttempts(maxAttempts),
	})
}

func logDispatchSuccess(logger *slog.Logger, runID, projectPath string, item DispatchItem, maxAttempts int) {
	emitLifecycleLog(logger, slog.LevelInfo, "Linear sync dispatch success", lifecycleContext{
		RunID:         runID,
		ProjectPath:   projectPath,
		IssueID:       item.IssueID,
		LinearIssueID: item.LinearIssueID,
		Operation:     item.Operation,
		Attempt:       normalizedAttempt(item.Attempts) + 1,
		MaxAttempts:   normalizedMaxAttempts(maxAttempts),
	})
}

func logDispatchRetryScheduled(logger *slog.Logger, runID, projectPath string, item DispatchItem, maxAttempts int, delaySeconds int, err error) {
	emitLifecycleLog(logger, slog.LevelWarn, "Linear sync dispatch retry scheduled", lifecycleContext{
		RunID:         runID,
		ProjectPath:   projectPath,
		IssueID:       item.IssueID,
		LinearIssueID: item.LinearIssueID,
		Operation:     item.Operation,
		Attempt:       normalizedAttempt(item.Attempts) + 1,
		MaxAttempts:   normalizedMaxAttempts(maxAttempts),
		DelaySeconds:  delaySeconds,
		Reason:        err.Error(),
	})
}

func logDispatchTerminalFailure(logger *slog.Logger, runID, projectPath string, item DispatchItem, maxAttempts int, err error) {
	emitLifecycleLog(logger, slog.LevelWarn, "Linear sync dispatch terminal failure", lifecycleContext{
		RunID:         runID,
		ProjectPath:   projectPath,
		IssueID:       item.IssueID,
		LinearIssueID: item.LinearIssueID,
		Operation:     item.Operation,
		Attempt:       normalizedAttempt(item.Attempts) + 1,
		MaxAttempts:   normalizedMaxAttempts(maxAttempts),
		Reason:        err.Error(),
	})
}
