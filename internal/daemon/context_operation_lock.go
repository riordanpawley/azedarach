package daemon

import (
	"context"
	"sync"
)

type contextOperationLockWaitHookKey struct{}

// contextOperationLock serializes one in-process authority while allowing
// callers to abandon admission through context cancellation.
type contextOperationLock struct {
	once   sync.Once
	token  chan struct{}
	mu     sync.RWMutex
	holder string
}

func (l *contextOperationLock) init() {
	l.once.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
}

func (l *contextOperationLock) currentHolder() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.holder
}

func (l *contextOperationLock) acquire(ctx context.Context, operation string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.init()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.token <- struct{}{}
			return "", err
		}
		l.setHolder(operation)
		return "", nil
	default:
	}
	holder := l.currentHolder()
	if hook, _ := ctx.Value(contextOperationLockWaitHookKey{}).(func(string, string)); hook != nil {
		hook(operation, holder)
	}
	select {
	case <-ctx.Done():
		return holder, ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.token <- struct{}{}
			return holder, err
		}
		l.setHolder(operation)
		return holder, nil
	}
}

func (l *contextOperationLock) setHolder(operation string) {
	l.mu.Lock()
	l.holder = operation
	l.mu.Unlock()
}

func (l *contextOperationLock) release() {
	l.setHolder("")
	l.token <- struct{}{}
}

func withContextOperationLockWaitHookForTest(ctx context.Context, hook func(string, string)) context.Context {
	return context.WithValue(ctx, contextOperationLockWaitHookKey{}, hook)
}
