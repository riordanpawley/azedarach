package sqliteutil

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

var writeLocks sync.Map

type writeLockContextKey struct{}

type writeLock struct {
	token chan struct{}
}

func newWriteLock() *writeLock {
	lock := &writeLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

func (l *writeLock) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		return nil
	}
}

func (l *writeLock) release() {
	l.token <- struct{}{}
}

// WithWriteLock serializes process-local writes for one SQLite database file.
func WithWriteLock(dbPath string, fn func() error) error {
	lock := writeLockForPath(dbPath)
	if err := lock.acquire(context.Background()); err != nil {
		return err
	}
	defer lock.release()
	return fn()
}

// WithWriteLockContext is the context-propagating form of WithWriteLock. It
// permits nested store helpers in one call chain to reuse the same canonical
// database lock without self-deadlocking.
func WithWriteLockContext(ctx context.Context, dbPath string, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	key := CanonicalPath(dbPath)
	if held, _ := ctx.Value(writeLockContextKey{}).(map[string]struct{}); held != nil {
		if _, ok := held[key]; ok {
			return fn(ctx)
		}
	}
	lock := writeLockForPath(key)
	if err := lock.acquire(ctx); err != nil {
		return err
	}
	defer lock.release()
	held, _ := ctx.Value(writeLockContextKey{}).(map[string]struct{})
	next := make(map[string]struct{}, len(held)+1)
	for heldKey := range held {
		next[heldKey] = struct{}{}
	}
	next[key] = struct{}{}
	return fn(context.WithValue(ctx, writeLockContextKey{}, next))
}

func writeLockForPath(dbPath string) *writeLock {
	key := CanonicalPath(dbPath)
	if key == "" {
		key = "."
	}
	value, _ := writeLocks.LoadOrStore(key, newWriteLock())
	return value.(*writeLock)
}

// CanonicalPath returns the stable process-local identity used to coordinate
// SQLite handles that reach the same database through relative or symlinked
// paths.
func CanonicalPath(dbPath string) string {
	key := strings.TrimSpace(dbPath)
	if abs, err := filepath.Abs(key); err == nil {
		key = filepath.Clean(abs)
	}
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = filepath.Clean(resolved)
	} else if resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(key)); parentErr == nil {
		key = filepath.Join(filepath.Clean(resolvedParent), filepath.Base(key))
	}
	return key
}
