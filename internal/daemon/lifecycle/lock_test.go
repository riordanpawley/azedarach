package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

func TestLockManagerHandlesLegacyPIDLockFile(t *testing.T) {
	t.Run("rejects active pid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "daemon.lock")
		if err := os.WriteFile(path, []byte("42\n"), 0o644); err != nil {
			t.Fatalf("write lock: %v", err)
		}

		m := NewLockManager(path)
		m.isAliveFn = func(pid int) bool { return pid == 42 }

		if _, err := m.Acquire(); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Acquire err = %v, want ErrAlreadyRunning", err)
		}
	})

	t.Run("recovers stale pid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "daemon.lock")
		if err := os.WriteFile(path, []byte("999999\n"), 0o644); err != nil {
			t.Fatalf("write lock: %v", err)
		}

		m := NewLockManager(path)
		m.isAliveFn = func(int) bool { return false }

		lease, err := m.Acquire()
		if err != nil {
			t.Fatalf("Acquire stale pid lock: %v", err)
		}
		if lease == nil {
			t.Fatal("expected lease")
		}
		if err := m.Release(); err != nil {
			t.Fatalf("release: %v", err)
		}
	})
}

func TestTerminateLockOwnerMissingLockIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := TerminateLockOwner(path); err != nil {
		t.Fatalf("TerminateLockOwner() error = %v", err)
	}
}

func TestTerminateLockOwnerRemovesStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte(`{"pid":999999,"created_at":"2026-03-24T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if err := TerminateLockOwner(path); err != nil {
		t.Fatalf("TerminateLockOwner() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed, stat err = %v", err)
	}
}

func TestTerminateLockOwnerPermissionDeniedKeepsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte(`{"pid":42,"created_at":"2026-03-24T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	err := terminateLockOwnerWith(
		path,
		func(int, syscall.Signal) error { return syscall.EPERM },
		func(int) bool { return true },
		time.Now,
		func(time.Duration) {},
	)
	if !errors.Is(err, ErrLockOwnerPermissionDenied) {
		t.Fatalf("terminateLockOwnerWith() err = %v, want ErrLockOwnerPermissionDenied", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("lock file should remain after permission-denied kill, stat err = %v", statErr)
	}
}

func TestTerminateLockOwnerTimeoutKeepsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte(`{"pid":42,"created_at":"2026-03-24T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	now := time.Now()
	err := terminateLockOwnerWith(
		path,
		func(int, syscall.Signal) error { return nil },
		func(int) bool { return true },
		func() time.Time {
			now = now.Add(3 * time.Second)
			return now
		},
		func(time.Duration) {},
	)
	if !errors.Is(err, ErrLockOwnerTerminationTimeout) {
		t.Fatalf("terminateLockOwnerWith() err = %v, want ErrLockOwnerTerminationTimeout", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("lock file should remain after timeout, stat err = %v", statErr)
	}
}

func TestTerminateLockOwnerWaitsForExitBeforeRemovingLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte(`{"pid":42,"created_at":"2026-03-24T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	checks := 0
	err := terminateLockOwnerWith(
		path,
		func(int, syscall.Signal) error { return nil },
		func(int) bool {
			checks++
			return checks == 1
		},
		time.Now,
		func(time.Duration) {},
	)
	if err != nil {
		t.Fatalf("terminateLockOwnerWith() error = %v", err)
	}
	if checks < 2 {
		t.Fatalf("isAlive checks = %d, want >= 2", checks)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lock file should be removed after owner exits, stat err = %v", statErr)
	}
}
