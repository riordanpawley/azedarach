package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/services/git"
)

type gitServiceAdapter struct {
	client          *git.Client
	projectionStore *daemonstate.ProjectionStore
	logger          *slog.Logger
	pollInterval    time.Duration
	onStatusUpdate  func(projectID, issueID, worktree string)

	refreshMu      sync.Mutex
	refreshRunning map[string]bool
	pollers        map[string]context.CancelFunc
}

func (a *gitServiceAdapter) Fetch(ctx context.Context, projectID, worktree, remote string) error {
	if err := a.client.Fetch(ctx, worktree, remote); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true)
	return nil
}

func (a *gitServiceAdapter) Merge(ctx context.Context, projectID, worktree, branch string) (*git.MergeResult, error) {
	result, err := a.client.Merge(ctx, worktree, branch)
	if err != nil {
		return nil, err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true)
	return result, nil
}

func (a *gitServiceAdapter) Checkout(ctx context.Context, projectID, worktree, branch string) error {
	if err := a.client.Checkout(ctx, worktree, branch); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true)
	return nil
}

func (a *gitServiceAdapter) AbortMerge(ctx context.Context, projectID, worktree string) error {
	if err := a.client.AbortMerge(ctx, worktree); err != nil {
		return err
	}
	a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true)
	return nil
}

func (a *gitServiceAdapter) DiffStat(ctx context.Context, _ string, worktree, baseBranch string) (string, error) {
	return a.client.DiffStat(ctx, worktree, baseBranch)
}

func (a *gitServiceAdapter) Status(ctx context.Context, projectID, worktree string) (*git.GitStatus, error) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return a.client.Status(ctx, worktree)
	}
	if a.projectionStore != nil {
		if projection, found, err := a.projectionStore.GetWorktreeByPath(ctx, projectID, worktree); err == nil && found && len(projection.GitStatusRaw) > 0 {
			var cached git.GitStatus
			if unmarshalErr := json.Unmarshal(projection.GitStatusRaw, &cached); unmarshalErr == nil {
				a.refreshGitStatusAsync(projectID, worktree)
				a.ensureStatusPoller(projectID, worktree)
				return &cached, nil
			}
		}
	}

	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		return nil, err
	}
	a.persistStatusSnapshot(ctx, projectID, worktree, status)
	a.ensureStatusPoller(projectID, worktree)
	return status, nil
}

func (a *gitServiceAdapter) refreshGitStatusWriteThrough(ctx context.Context, projectID, worktree string, publishOnChange bool) {
	status, err := a.client.Status(ctx, worktree)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("git status refresh after mutation failed", "project_id", projectID, "worktree", worktree, "error", err)
		}
		return
	}
	changed, issueID := a.persistStatusSnapshot(ctx, projectID, worktree, status)
	if publishOnChange && changed && a.onStatusUpdate != nil && strings.TrimSpace(issueID) != "" {
		a.onStatusUpdate(normalizeProjectID(projectID), issueID, worktree)
	}
}

func (a *gitServiceAdapter) refreshGitStatusAsync(projectID, worktree string) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return
	}
	key := projectID + "|" + worktree
	a.refreshMu.Lock()
	if a.refreshRunning == nil {
		a.refreshRunning = map[string]bool{}
	}
	if a.refreshRunning[key] {
		a.refreshMu.Unlock()
		return
	}
	a.refreshRunning[key] = true
	a.refreshMu.Unlock()

	go func() {
		defer func() {
			a.refreshMu.Lock()
			delete(a.refreshRunning, key)
			a.refreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		a.refreshGitStatusWriteThrough(ctx, projectID, worktree, true)
	}()
}

func (a *gitServiceAdapter) persistStatusSnapshot(ctx context.Context, projectID, worktree string, status *git.GitStatus) (bool, string) {
	if a.projectionStore == nil || status == nil {
		return false, ""
	}
	projection, found, err := a.projectionStore.GetWorktreeByPath(ctx, projectID, worktree)
	if err != nil || !found || strings.TrimSpace(projection.IssueID) == "" {
		return false, ""
	}
	rawStatus, err := json.Marshal(status)
	if err != nil {
		return false, ""
	}
	changed := string(rawStatus) != string(projection.GitStatusRaw)
	if err := a.projectionStore.UpsertWorktreeGitStatus(ctx, projectID, projection.IssueID, rawStatus, time.Now().UTC()); err != nil && a.logger != nil {
		a.logger.Debug("persist git status projection failed", "project_id", projectID, "issue_id", projection.IssueID, "worktree", worktree, "error", err)
		return false, ""
	}
	return changed, projection.IssueID
}

func (a *gitServiceAdapter) ensureStatusPoller(projectID, worktree string) {
	projectID = normalizeProjectID(projectID)
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return
	}
	interval := a.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	key := projectID + "|" + worktree
	a.refreshMu.Lock()
	if a.pollers == nil {
		a.pollers = map[string]context.CancelFunc{}
	}
	if _, exists := a.pollers[key]; exists {
		a.refreshMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pollers[key] = cancel
	a.refreshMu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, pollCancel := context.WithTimeout(context.Background(), 3*time.Second)
				a.refreshGitStatusWriteThrough(pollCtx, projectID, worktree, true)
				pollCancel()
			}
		}
	}()
}

func normalizeProjectID(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "default"
	}
	return projectID
}
