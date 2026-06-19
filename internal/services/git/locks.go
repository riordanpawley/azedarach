package git

import (
	"context"
	"path/filepath"
	"strings"
)

type worktreeLock struct {
	token chan struct{}
}

func newWorktreeLock() *worktreeLock {
	lock := &worktreeLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

// WithWorktreeLock serializes daemon-owned mutations for a target worktree.
func (c *Client) WithWorktreeLock(ctx context.Context, worktree string, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return nil
	}
	lock := c.lockForWorktree(worktree)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lock.token:
	}
	defer func() {
		lock.token <- struct{}{}
	}()
	return fn(ctx)
}

func (c *Client) lockForWorktree(worktree string) *worktreeLock {
	key := normalizeWorktreeLockKey(worktree)
	c.worktreeLocksMu.Lock()
	defer c.worktreeLocksMu.Unlock()
	if c.worktreeLocks == nil {
		c.worktreeLocks = map[string]*worktreeLock{}
	}
	if lock := c.worktreeLocks[key]; lock != nil {
		return lock
	}
	lock := newWorktreeLock()
	c.worktreeLocks[key] = lock
	return lock
}

func normalizeWorktreeLockKey(worktree string) string {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return "."
	}
	if abs, err := filepath.Abs(worktree); err == nil {
		worktree = abs
	}
	if realPath, err := filepath.EvalSymlinks(worktree); err == nil {
		return filepath.Clean(realPath)
	}
	return filepath.Clean(worktree)
}
