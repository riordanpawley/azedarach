package git

import "context"

type Status struct {
	HasChanges bool
	Staged     []string
	Untracked  []string
}

type MergeResult struct {
	HasConflicts bool
	Conflicts    []string
}

type Client interface {
	Status(ctx context.Context, worktree string) (Status, error)
	Merge(ctx context.Context, worktree string, branch string) (MergeResult, error)
	AbortMerge(ctx context.Context, worktree string) error
}
