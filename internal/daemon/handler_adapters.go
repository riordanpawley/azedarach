package daemon

import (
	"context"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"

	"github.com/riordanpawley/azedarach/internal/services/git"
)

type worktreeServiceAdapter struct {
	manager *git.WorktreeManager
}

func (a worktreeServiceAdapter) List(ctx context.Context, _ string) ([]git.Worktree, error) {
	return a.manager.List(ctx)
}

func (a worktreeServiceAdapter) Create(ctx context.Context, _ string, issueID string, baseBranch string) (*git.Worktree, error) {
	return a.manager.Create(ctx, issueID, baseBranch)
}

func (a worktreeServiceAdapter) Delete(ctx context.Context, _ string, issueID string) error {
	worktree, err := a.manager.Get(ctx, issueID)
	if err != nil {
		return err
	}
	if err := a.manager.Delete(ctx, issueID); err != nil {
		return err
	}
	if worktree != nil {
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(worktree.Path))
	}
	return nil
}

func (a worktreeServiceAdapter) CleanupOrphaned(ctx context.Context, projectID string) (*daemonhandlers.CleanupOrphanedResult, error) {
	worktrees, err := a.manager.List(ctx)
	if err != nil {
		return nil, err
	}

	result := &daemonhandlers.CleanupOrphanedResult{
		ProjectID: projectID,
	}
	for _, wt := range worktrees {
		if err := a.manager.Delete(ctx, wt.IssueID); err != nil {
			result.Skipped = append(result.Skipped, wt)
			continue
		}
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(wt.Path))
		result.Removed = append(result.Removed, wt)
	}

	return result, nil
}
