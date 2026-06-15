package sqliteutil

import (
	"path/filepath"
	"strings"
	"sync"
)

var writeLocks sync.Map

// WithWriteLock serializes process-local writes for one SQLite database file.
func WithWriteLock(dbPath string, fn func() error) error {
	lock := writeLockForPath(dbPath)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func writeLockForPath(dbPath string) *sync.Mutex {
	key := strings.TrimSpace(dbPath)
	if abs, err := filepath.Abs(key); err == nil {
		key = filepath.Clean(abs)
	}
	if key == "" {
		key = "."
	}
	value, _ := writeLocks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}
