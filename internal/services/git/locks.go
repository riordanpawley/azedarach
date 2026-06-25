package git

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

type worktreeLock struct {
	mu      sync.Mutex
	held    bool
	waiters []chan struct{}
}

func newWorktreeLock() *worktreeLock {
	return &worktreeLock{}
}

// WithWorktreeLock serializes daemon-owned mutations for a target worktree.
func (c *Client) WithWorktreeLock(ctx context.Context, worktree string, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return nil
	}
	unlock, err := c.lockForWorktree(worktree).acquire(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return fn(ctx)
}

func (l *worktreeLock) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	waiter := make(chan struct{})
	l.mu.Lock()
	if !l.held && len(l.waiters) == 0 {
		l.held = true
		l.mu.Unlock()
		return l.release, nil
	}
	l.waiters = append(l.waiters, waiter)
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		if !l.removeWaiter(waiter) {
			l.release()
		}
		return nil, ctx.Err()
	case <-waiter:
		return l.release, nil
	}
}

func (l *worktreeLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.waiters) == 0 {
		l.held = false
		return
	}
	next := l.waiters[0]
	copy(l.waiters, l.waiters[1:])
	l.waiters[len(l.waiters)-1] = nil
	l.waiters = l.waiters[:len(l.waiters)-1]
	close(next)
}

func (l *worktreeLock) removeWaiter(waiter chan struct{}) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, candidate := range l.waiters {
		if candidate != waiter {
			continue
		}
		copy(l.waiters[i:], l.waiters[i+1:])
		l.waiters[len(l.waiters)-1] = nil
		l.waiters = l.waiters[:len(l.waiters)-1]
		return true
	}
	return false
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
