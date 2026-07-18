package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
)

type staleWorktreeGitRefreshError struct {
	reason   string
	worktree string
	cause    error
}

func (e *staleWorktreeGitRefreshError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("stale worktree git refresh suppressed: %s: %v", e.reason, e.cause)
	}
	return fmt.Sprintf("stale worktree git refresh suppressed: %s", e.reason)
}

func (e *staleWorktreeGitRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newStaleWorktreeGitRefreshError(reason, worktree string, cause error) error {
	return &staleWorktreeGitRefreshError{
		reason:   strings.TrimSpace(reason),
		worktree: strings.TrimSpace(worktree),
		cause:    cause,
	}
}

func staleWorktreeGitRefreshErrorReason(err error) (string, bool) {
	var target *staleWorktreeGitRefreshError
	if !errors.As(err, &target) || target == nil {
		return "", false
	}
	return strings.TrimSpace(target.reason), true
}

func staleGitWorktreeRefreshReason(worktree string, err error) (string, bool) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return "", false
	}
	info, statErr := os.Stat(worktree)
	switch {
	case statErr != nil && errors.Is(statErr, os.ErrNotExist):
		return "missing_worktree_path", true
	case statErr != nil:
		return "", false
	case !info.IsDir():
		return "worktree_path_not_directory", true
	}
	if err == nil {
		return "", false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not a git repository"):
		return "not_git_repository", true
	case strings.Contains(msg, "not a working tree"),
		strings.Contains(msg, "must be run in a work tree"):
		return "not_working_tree", true
	default:
		return "", false
	}
}

func (d *Daemon) suppressStaleWorktreeGitRefresh(ctx context.Context, projectID, issueID, worktree string, cause error) bool {
	reason, ok := staleGitWorktreeRefreshReason(worktree, cause)
	if !ok {
		return false
	}
	projectID = d.canonicalProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)
	if issueID == "" {
		if store := d.worktreeRuntimeStateStore(projectID); store != nil {
			if projection, found, err := store.GetWorktreeStateByPath(ctx, projectID, worktree); err == nil && found {
				issueID = strings.TrimSpace(projection.IssueID)
			}
		}
	}
	if issueID != "" {
		if _, err := d.runtimeProjectionStateWriter().DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID); err != nil && d.cfg.Logger != nil {
			d.cfg.Logger.Warn("delete stale worktree projection failed; observation will be retried", "project_id", projectID, "issue_id", issueID, "error", err)
		}
	}
	logStaleWorktreeGitRefreshSuppressed(d.cfg.Logger, projectID, issueID, worktree, reason, cause)
	return true
}

func (d *Daemon) suppressProjectedStaleWorktreeGitRefresh(ctx context.Context, projectID, issueID, worktree string, cause error) bool {
	if _, ok := staleGitWorktreeRefreshReason(worktree, cause); !ok {
		return false
	}
	projectID = d.canonicalProjectID(projectID)
	issueID = strings.TrimSpace(issueID)
	worktree = strings.TrimSpace(worktree)
	store := d.worktreeRuntimeStateStore(projectID)
	if store == nil {
		return false
	}
	if issueID != "" {
		if projection, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, issueID); err == nil && found && strings.TrimSpace(projection.Path) == worktree {
			return d.suppressStaleWorktreeGitRefresh(ctx, projectID, issueID, worktree, cause)
		}
	}
	if projection, found, err := store.GetWorktreeStateByPath(ctx, projectID, worktree); err == nil && found {
		return d.suppressStaleWorktreeGitRefresh(ctx, projectID, strings.TrimSpace(projection.IssueID), worktree, cause)
	}
	return false
}

func suppressStaleWorktreeGitRefreshProjection(
	ctx context.Context,
	projectID, worktree string,
	cause error,
	store *daemonstate.RuntimeStateStore,
	writer runtimeProjectionWriter,
	logger *slog.Logger,
	stopPoller func(),
) bool {
	reason, ok := staleGitWorktreeRefreshReason(worktree, cause)
	if !ok {
		return false
	}
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	issueID := ""
	if store != nil {
		if projection, found, err := store.GetWorktreeStateByPath(ctx, projectID, worktree); err == nil && found {
			issueID = strings.TrimSpace(projection.IssueID)
		}
	}
	if issueID != "" {
		if writer != nil {
			if _, err := writer.DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID); err != nil && logger != nil {
				logger.Warn("delete stale worktree projection failed; observation will be retried", "project_id", projectID, "issue_id", issueID, "error", err)
			}
		} else if store != nil {
			if err := store.DeleteWorktreeState(ctx, projectID, issueID); err != nil && logger != nil {
				logger.Warn("delete stale worktree runtime state failed",
					"project_id", projectID,
					"issue_id", issueID,
					"worktree", worktree,
					"stale_reason", reason,
					"error", err,
				)
			}
		}
	}
	if stopPoller != nil {
		stopPoller()
	}
	logStaleWorktreeGitRefreshSuppressed(logger, projectID, issueID, worktree, reason, cause)
	return true
}

func logStaleWorktreeGitRefreshSuppressed(logger *slog.Logger, projectID, issueID, worktree, reason string, cause error) {
	if logger == nil {
		return
	}
	attrs := []any{
		"project_id", strings.TrimSpace(projectID),
		"issue_id", strings.TrimSpace(issueID),
		"worktree", strings.TrimSpace(worktree),
		"stale_reason", strings.TrimSpace(reason),
	}
	if cause != nil {
		attrs = append(attrs, "error", cause)
	}
	logger.Info("suppressed stale worktree git refresh", attrs...)
}
