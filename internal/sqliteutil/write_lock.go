package sqliteutil

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var writeLocks sync.Map

type writeLockContextKey struct{}
type writeOperationContextKey struct{}

type writeLock struct {
	token       chan struct{}
	mu          sync.RWMutex
	holder      string
	holderSince time.Time
}

func newWriteLock() *writeLock {
	lock := &writeLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

func (l *writeLock) acquire(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		l.mu.Lock()
		l.holder = operation
		l.holderSince = time.Now()
		l.mu.Unlock()
		return nil
	}
}

func (l *writeLock) release() {
	l.mu.Lock()
	l.holder = ""
	l.holderSince = time.Time{}
	l.mu.Unlock()
	l.token <- struct{}{}
}

func (l *writeLock) diagnostics(now time.Time) WriteLockDiagnostics {
	l.mu.RLock()
	defer l.mu.RUnlock()
	diagnostic := WriteLockDiagnostics{Holder: l.holder}
	if l.holder != "" && !l.holderSince.IsZero() && !now.Before(l.holderSince) {
		diagnostic.HeldFor = now.Sub(l.holderSince)
	}
	return diagnostic
}

// WriteLockDiagnostics identifies the current process-local SQLite write-lock
// owner. HeldFor is operational evidence only and is never a correctness gate.
type WriteLockDiagnostics struct {
	Holder  string
	HeldFor time.Duration
}

// ContextWithWriteOperation attaches stable provenance to a shared SQLite
// write-lock acquisition.
func ContextWithWriteOperation(ctx context.Context, operation string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return ctx
	}
	return context.WithValue(ctx, writeOperationContextKey{}, operation)
}

// WriteLockResourceDiagnostics reports the shared write-lock holder for dbPath.
func WriteLockResourceDiagnostics(dbPath string) WriteLockDiagnostics {
	return writeLockForPath(dbPath).diagnostics(time.Now())
}

// WithWriteLock serializes process-local writes for one SQLite database file.
func WithWriteLock(dbPath string, fn func() error) error {
	lock := writeLockForPath(dbPath)
	if err := lock.acquire(context.Background(), writeOperationFromCaller()); err != nil {
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
	operation, _ := ctx.Value(writeOperationContextKey{}).(string)
	if strings.TrimSpace(operation) == "" {
		operation = writeOperationFromCaller()
	}
	if err := lock.acquire(ctx, operation); err != nil {
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

func writeOperationFromCaller() string {
	pcs := make([]uintptr, 12)
	count := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:count])
	for {
		frame, more := frames.Next()
		name := frame.Function
		if !strings.HasSuffix(name, ".writeOperationFromCaller") &&
			!strings.HasSuffix(name, ".WithWriteLock") &&
			!strings.HasSuffix(name, ".WithWriteLockContext") {
			if slash := strings.LastIndex(name, "/"); slash >= 0 {
				name = name[slash+1:]
			}
			if name != "" {
				return name
			}
		}
		if !more {
			break
		}
	}
	return "sqlite.write"
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
