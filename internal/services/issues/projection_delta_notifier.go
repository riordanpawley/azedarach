package issues

// projectionDeltaNotifier reports filesystem changes that may advance a
// projection cursor. If an implementation opens unrelated descriptors for the
// SQLite database, WAL, or SHM files, they must remain open for the SQLite
// pool's lifetime and close only after the pool: closing any such descriptor
// can cancel SQLite's process-wide POSIX locks.
type projectionDeltaNotifier interface {
	Events() <-chan struct{}
	Errors() <-chan error
	Close() error
}
