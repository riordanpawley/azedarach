package daemon

import (
	"context"
	"sync"
)

type contextOperationLockWaitHookKey struct{}
type contextOperationLockQueuedHookKey struct{}

type contextOperationLockWaiter struct {
	operation   string
	ready       chan struct{}
	predecessor string
	granted     bool
}

// contextOperationLock serializes one in-process authority while allowing
// callers to abandon queued admission through context cancellation. Waiters
// are FIFO and receive the holder that directly handed authority to them.
type contextOperationLock struct {
	mu      sync.Mutex
	held    bool
	holder  string
	waiters []*contextOperationLockWaiter
}

func (l *contextOperationLock) currentHolder() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holder
}

func (l *contextOperationLock) acquire(ctx context.Context, operation string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	l.mu.Lock()
	if !l.held {
		l.held = true
		l.holder = operation
		l.mu.Unlock()
		return "", nil
	}
	waiter := &contextOperationLockWaiter{operation: operation, ready: make(chan struct{})}
	l.waiters = append(l.waiters, waiter)
	l.mu.Unlock()

	if hook, _ := ctx.Value(contextOperationLockQueuedHookKey{}).(func(string)); hook != nil {
		hook(operation)
	}
	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			l.release()
			return waiter.predecessor, err
		}
		if hook, _ := ctx.Value(contextOperationLockWaitHookKey{}).(func(string, string)); hook != nil {
			hook(operation, waiter.predecessor)
		}
		return waiter.predecessor, nil
	case <-ctx.Done():
		l.mu.Lock()
		for i, candidate := range l.waiters {
			if candidate == waiter {
				l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
				l.mu.Unlock()
				return "", ctx.Err()
			}
		}
		granted := waiter.granted
		l.mu.Unlock()
		if granted {
			l.release()
		}
		return waiter.predecessor, ctx.Err()
	}
}

func (l *contextOperationLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	predecessor := l.holder
	if len(l.waiters) == 0 {
		l.held = false
		l.holder = ""
		return
	}
	waiter := l.waiters[0]
	l.waiters = l.waiters[1:]
	l.holder = waiter.operation
	waiter.predecessor = predecessor
	waiter.granted = true
	close(waiter.ready)
}

func withContextOperationLockWaitHookForTest(ctx context.Context, hook func(string, string)) context.Context {
	return context.WithValue(ctx, contextOperationLockWaitHookKey{}, hook)
}

func withContextOperationLockQueuedHookForTest(ctx context.Context, hook func(string)) context.Context {
	return context.WithValue(ctx, contextOperationLockQueuedHookKey{}, hook)
}
