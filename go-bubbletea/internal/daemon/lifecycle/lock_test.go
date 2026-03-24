package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLockManagerRejectsActiveOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte(`{"pid":42,"created_at":"2026-03-24T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	m := NewLockManager(path)
	m.isAliveFn = func(pid int) bool { return pid == 42 }
	if _, err := m.Acquire(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Acquire err = %v, want ErrAlreadyRunning", err)
	}
}

func TestLockManagerRecoversStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte(`{"pid":999999,"created_at":"2026-03-24T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	m := NewLockManager(path)
	m.isAliveFn = func(int) bool { return false }
	lease, err := m.Acquire()
	if err != nil {
		t.Fatalf("Acquire stale lock: %v", err)
	}
	if lease == nil {
		t.Fatal("expected lease")
	}
	if err := m.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestLockManagerConcurrentAcquireSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var winningLease *Lease
	var winMu sync.Mutex

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m := NewLockManager(path)
			m.isAliveFn = func(int) bool { return true }
			lease, err := m.Acquire()
			if err != nil {
				if errors.Is(err, ErrAlreadyRunning) {
					return
				}
				t.Errorf("Acquire unexpected error: %v", err)
				return
			}
			winners.Add(1)
			winMu.Lock()
			if winningLease == nil {
				winningLease = lease
			}
			winMu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Fatalf("winners = %d, want 1", got)
	}
	if winningLease == nil {
		t.Fatal("expected winning lease")
	}
	if err := winningLease.Release(); err != nil {
		t.Fatalf("winning lease release: %v", err)
	}
}

func TestLockManagerRecoversMalformedLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	m := NewLockManager(path)
	m.isAliveFn = func(int) bool { return false }

	if _, err := m.Acquire(); err != nil {
		t.Fatalf("Acquire malformed lock: %v", err)
	}
}
