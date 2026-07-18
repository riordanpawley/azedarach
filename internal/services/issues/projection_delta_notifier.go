package issues

import "sync"

// projectionDeltaNotifier reports filesystem changes that may advance a
// projection cursor. Implementations must never watch the SQLite database,
// WAL, SHM, or their containing directory: kqueue directory watches open every
// child and closing any descriptor for a SQLite inode can cancel process-wide
// POSIX locks owned by independent pools.
type projectionDeltaNotifier interface {
	Events() <-chan struct{}
	Errors() <-chan error
	Close() error
}

type projectionDeltaNotifierCloseState struct {
	once sync.Once
	err  error
}
