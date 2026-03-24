package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// ErrAlreadyRunning indicates an active daemon lock owner still exists.
	ErrAlreadyRunning = errors.New("daemon already running")
)

type lockRecord struct {
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
}

// LockManager coordinates singleton daemon ownership via a lock file.
type LockManager struct {
	path       string
	pid        int
	isAliveFn  func(int) bool
	nowFn      func() time.Time
	mu         sync.Mutex
	ownsLock   bool
}

// Lease represents an acquired singleton lock.
type Lease struct {
	path string
	once sync.Once
}

// NewLockManager returns a lock manager for path.
func NewLockManager(path string) *LockManager {
	return &LockManager{
		path:      path,
		pid:       os.Getpid(),
		isAliveFn: isProcessAlive,
		nowFn:     time.Now,
	}
}

// Acquire attempts to own the singleton lock. Stale lock files are recovered.
func (m *LockManager) Acquire() (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ownsLock {
		return &Lease{path: m.path}, nil
	}

	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	if err := m.tryCreateLock(); err == nil {
		m.ownsLock = true
		return &Lease{path: m.path}, nil
	}

	rec, parseErr := m.readRecord()
	if parseErr != nil || !m.isAliveFn(rec.PID) {
		if rmErr := os.Remove(m.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale lock: %w", rmErr)
		}
		if err := m.tryCreateLock(); err != nil {
			return nil, err
		}
		m.ownsLock = true
		return &Lease{path: m.path}, nil
	}

	return nil, ErrAlreadyRunning
}

// Release relinquishes singleton ownership.
func (m *LockManager) Release() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsLock {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.ownsLock = false
	return nil
}

// Release removes lock file once.
func (l *Lease) Release() error {
	var releaseErr error
	l.once.Do(func() {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			releaseErr = err
		}
	})
	return releaseErr
}

func (m *LockManager) tryCreateLock() error {
	record := lockRecord{
		PID:       m.pid,
		CreatedAt: m.nowFn().UTC(),
	}
	b, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal lock record: %w", err)
	}

	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrAlreadyRunning
		}
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write lock record: %w", err)
	}
	return nil
}

func (m *LockManager) readRecord() (lockRecord, error) {
	var rec lockRecord
	b, err := os.ReadFile(m.path)
	if err != nil {
		return rec, err
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return rec, errors.New("empty lock file")
	}
	if strings.HasPrefix(content, "{") {
		if err := json.Unmarshal([]byte(content), &rec); err != nil {
			return rec, err
		}
		return rec, nil
	}

	pid, err := strconv.Atoi(content)
	if err != nil {
		return rec, fmt.Errorf("parse pid: %w", err)
	}
	rec.PID = pid
	return rec, nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
