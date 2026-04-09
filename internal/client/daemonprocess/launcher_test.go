package daemonprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	launcher.waitForReady = func(context.Context, string) error { return nil }
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
		if readyCalls == 1 {
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
	if readyCalls != 2 {
		t.Fatalf("waitForReady call count = %d, want 2", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
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

func TestLauncherStart_RechecksSocketWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.sleepFn = func(time.Duration) {}
	launcher.terminateLockOwner = func(string) error { return lifecycle.ErrLockOwnerPermissionDenied }

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls < 2 {
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
	if readyCalls != 2 {
		t.Fatalf("waitForReady call count = %d, want 2", readyCalls)
	}
}

func TestLauncherStartHonorsCallerContextDeadlineForReadyWait(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.waitForReady = func(ctx context.Context, _ string) error {
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
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Start() elapsed = %s, want < 500ms", elapsed)
	}
}

func TestLauncherStopUsesTerminateLockOwner(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

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
	launcher.terminateLockOwner = func(string) error { return errors.New("boom") }

	err := launcher.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "terminate daemon lock owner: boom") {
		t.Fatalf("Stop() error = %v, want wrapped terminate error", err)
	}
}
