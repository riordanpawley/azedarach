package daemon

import (
	"context"

	"github.com/riordanpawley/azedarach/internal/services/git"
)

type worktreeServiceAdapter struct {
	manager *git.WorktreeManager
}

func (a worktreeServiceAdapter) List(ctx context.Context, _ string) ([]git.Worktree, error) {
	return a.manager.List(ctx)
}

func (a worktreeServiceAdapter) Create(ctx context.Context, _ string, beadID string, baseBranch string) (*git.Worktree, error) {
	return a.manager.Create(ctx, beadID, baseBranch)
}

func (a worktreeServiceAdapter) Delete(ctx context.Context, _ string, beadID string) error {
	return a.manager.Delete(ctx, beadID)
}
