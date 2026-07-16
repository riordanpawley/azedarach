package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const integrationTransactionLockPollInterval = 10 * time.Millisecond

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

// withIntegrationTransactionLock serializes target integration publication
// across Client instances and daemon processes. The common-directory lock file
// is intentionally retained so contenders always coordinate on the same inode.
func (c *Client) withIntegrationTransactionLock(ctx context.Context, worktree string, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return nil
	}
	return c.WithWorktreeLock(ctx, worktree, func(ctx context.Context) error {
		commonDir, err := c.gitCommonDir(ctx, worktree)
		if err != nil {
			return fmt.Errorf("resolve integration lock common directory: %w", err)
		}
		lockDir := filepath.Join(commonDir, "azedarach")
		if err := os.MkdirAll(lockDir, 0o755); err != nil {
			return fmt.Errorf("create integration lock directory: %w", err)
		}
		lockPath := filepath.Join(lockDir, integrationLockName(worktree))
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("open integration transaction lock: %w", err)
		}
		defer file.Close()

		ticker := time.NewTicker(integrationTransactionLockPollInterval)
		defer ticker.Stop()
		for {
			err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if err == nil {
				break
			}
			if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
				return fmt.Errorf("acquire integration transaction lock: %w", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck
		return fn(ctx)
	})
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
