package sqliteutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWithWriteLockContextIsReentrantForCanonicalAliases(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "azedarach.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("create database path: %v", err)
	}
	aliasPath := filepath.Join(dir, "alias.db")
	if err := os.Symlink(dbPath, aliasPath); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- WithWriteLockContext(context.Background(), dbPath, func(ctx context.Context) error {
			return WithWriteLockContext(ctx, aliasPath, func(context.Context) error { return nil })
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested lock: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canonical alias self-deadlocked")
	}
}

func TestCanonicalPathResolvesSymlinkedParentBeforeDatabaseExists(t *testing.T) {
	realDir := t.TempDir()
	aliasRoot := t.TempDir()
	aliasDir := filepath.Join(aliasRoot, "db-dir")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatalf("create directory alias: %v", err)
	}
	realPath := filepath.Join(realDir, "future.db")
	aliasPath := filepath.Join(aliasDir, "future.db")
	if got, want := CanonicalPath(aliasPath), CanonicalPath(realPath); got != want {
		t.Fatalf("CanonicalPath(alias) = %q, want %q", got, want)
	}
}

func TestWithWriteLockContextBoundsLockAcquisitionByCallerDeadline(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "azedarach.db")
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithWriteLock(dbPath, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	err := WithWriteLockContext(ctx, dbPath, func(context.Context) error {
		t.Fatal("callback ran without acquiring the held lock")
		return nil
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("lock acquisition error = %v, want %v", err, context.DeadlineExceeded)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("lock acquisition ignored caller deadline: %s", elapsed)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("release held lock: %v", err)
	}
}
