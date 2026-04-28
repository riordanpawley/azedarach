package daemonprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
)

type trackingWriteCloser struct {
	closed atomic.Bool
}

func (w *trackingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *trackingWriteCloser) Close() error {
	w.closed.Store(true)
	return nil
}

func TestLauncherStartClosesDaemonLog(t *testing.T) {
	repoDir := t.TempDir()
	socketRoot, err := os.MkdirTemp(".", "azd-launcher-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketPath := filepath.Join(socketRoot, "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	if launcher.LockPath != filepath.Join(socketRoot, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(socketRoot, "daemon.lock"))
	}
	launcher.BinPath = "true"
	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(path string) (io.WriteCloser, error) {
		want := filepath.Join(repoDir, ".azedarach", "daemon.log")
		if path != want {
			t.Fatalf("daemon log path = %q, want %q", path, want)
		}
		return tracker, nil
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".azedarach")); err != nil {
		t.Fatalf("expected .azedarach dir to exist: %v", err)
	}
}

func TestNewLauncherNormalizesWorktreeToBaseRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
	if launcher.LockPath != filepath.Join(base, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(base, "daemon.lock"))
	}
}

func TestNewLauncherScopedModeKeepsWorktreeRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != worktree {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, worktree)
	}
}

func TestLauncherResolveBinary_UsesMonorepoGoBubbleteaBin(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	nestedBin := filepath.Join(repoDir, "go-bubbletea", "bin")
	if err := os.MkdirAll(nestedBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested bin): %v", err)
	}
	azd := filepath.Join(nestedBin, "azd")
	if err := os.WriteFile(azd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != azd {
		t.Fatalf("resolveBinary() = %q, want %q", got, azd)
	}
}

func TestLauncherResolveBinary_UsesWorkingDirBinFallback(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	cwd := t.TempDir()
	t.Chdir(cwd)

	binDir := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cwd bin): %v", err)
	}
	azd := filepath.Join(binDir, "azd")
	if err := os.WriteFile(azd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cwd azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != azd {
		t.Fatalf("resolveBinary() = %q, want %q", got, azd)
	}
}

func TestLauncherResolveBinary_PrefersWorkingDirBinOverRepoBin(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	cwd := t.TempDir()
	t.Chdir(cwd)

	repoBinDir := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(repoBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo bin): %v", err)
	}
	repoAzd := filepath.Join(repoBinDir, "azd")
	if err := os.WriteFile(repoAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(repo azd): %v", err)
	}

	cwdBinDir := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(cwdBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cwd bin): %v", err)
	}
	cwdAzd := filepath.Join(cwdBinDir, "azd")
	if err := os.WriteFile(cwdAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cwd azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != cwdAzd {
		t.Fatalf("resolveBinary() = %q, want %q", got, cwdAzd)
	}
}

func TestLauncherStart_SkipsSpawnWhenLockOwnerAlive(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.waitForReady = func(context.Context, string) error { return nil }
	launcher.sleepFn = func(time.Duration) {}

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil (skip spawn when daemon lock owner alive)", err)
	}
}

func TestLauncherStart_SpawnsWhenLockOwnerAliveButSocketUnready(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(lockPath string) error {
		terminateCalls++
		return os.Remove(lockPath)
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherReplaceTerminatesBeforeStart(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	terminated := false
	launcher.terminateLockOwner = func(lockPath string) error {
		terminated = true
		return os.Remove(lockPath)
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if !terminated {
			return nil
		}
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if !terminated {
		t.Fatal("Replace() did not terminate lock owner before Start")
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after replacement Start() returned")
	}
}

func TestLauncherReplaceGracefullyStopsSocketBeforeStart(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if socketUp {
		t.Fatal("Replace() did not request socket shutdown")
	}
	if !spawned {
		t.Fatal("Replace() did not start a replacement daemon")
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after replacement Start() returned")
	}
}

func TestLauncherStart_ErrorsWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.terminateLockOwner = func(string) error { return errors.New("kill denied") }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	err = launcher.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "recover stale daemon lock owner") {
		t.Fatalf("Start() error = %v, want lock recovery failure", err)
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsEPERM(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return syscall.EPERM
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by permission fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsWrappedPermissionDenied(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return lifecycle.ErrLockOwnerPermissionDenied
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by permission fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsTerminationTimeout(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return fmt.Errorf("%w: pid %d", lifecycle.ErrLockOwnerTerminationTimeout, os.Getpid())
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by timeout fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsPermissionDeniedString(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return errors.New("lock owner permission denied: operation not permitted")
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by permission fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_RechecksSocketWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.sleepFn = func(time.Duration) {}
	launcher.terminateLockOwner = func(string) error { return errors.New("kill denied") }

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls < 4 {
			return context.DeadlineExceeded
		}
		return nil
	}

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil after recheck-ready", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
}

func TestLauncherStartHonorsCallerContextDeadlineForReadyWait(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 100*time.Millisecond {
			return context.DeadlineExceeded
		}
		<-ctx.Done()
		return ctx.Err()
	}
	launcher.sleepFn = func(time.Duration) {}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return &trackingWriteCloser{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := launcher.Start(ctx)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Fatalf("Start() elapsed = %s, want < 700ms", elapsed)
	}
}

func TestLauncherStart_SocketReadySkipsSpawnWithoutLock(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.waitForReady = func(context.Context, string) error { return nil }

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil when socket is already ready", err)
	}
}

func TestLauncherStart_SocketReadyWhileWaitingForStartLockReturnsNil(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")

	startLockPath := launcher.LockPath + ".start"
	if err := os.MkdirAll(filepath.Dir(startLockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(start lock dir): %v", err)
	}
	lockFile, err := os.OpenFile(startLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(start lock): %v", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		t.Fatalf("Flock(start lock): %v", err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls >= 2 {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.sleepFn = func(time.Duration) {}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	if err := launcher.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v, want nil when socket becomes ready under lock contention", err)
	}
	if readyCalls < 2 {
		t.Fatalf("waitForReady call count = %d, want >= 2", readyCalls)
	}
}

func TestLauncherStopUsesTerminateLockOwner(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("socket unavailable") }

	called := false
	var gotLockPath string
	launcher.terminateLockOwner = func(lockPath string) error {
		called = true
		gotLockPath = lockPath
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !called {
		t.Fatal("expected terminateLockOwner to be called")
	}
	if gotLockPath != launcher.LockPath {
		t.Fatalf("lock path = %q, want %q", gotLockPath, launcher.LockPath)
	}
}

func TestLauncherStopWrapsTerminateError(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("socket unavailable") }
	launcher.terminateLockOwner = func(string) error { return errors.New("boom") }

	err := launcher.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "terminate daemon lock owner: boom") {
		t.Fatalf("Stop() error = %v, want wrapped terminate error", err)
	}
}

func TestLauncherStopUsesGracefulSocketShutdownWhenAvailable(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	socketShutdownCalls := 0
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketShutdownCalls++
		return nil
	}
	terminateCalled := false
	launcher.terminateLockOwner = func(string) error {
		terminateCalled = true
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if socketShutdownCalls != 1 {
		t.Fatalf("socket shutdown calls = %d, want 1", socketShutdownCalls)
	}
	if terminateCalled {
		t.Fatal("terminateLockOwner should not be called when graceful socket shutdown succeeds")
	}
}

func TestLauncherStopFallsBackWhenGracefulSocketShutdownFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("rpc failed") }
	terminateCalled := false
	launcher.terminateLockOwner = func(string) error {
		terminateCalled = true
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !terminateCalled {
		t.Fatal("expected terminateLockOwner fallback when graceful socket shutdown fails")
	}
}
