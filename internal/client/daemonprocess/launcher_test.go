package daemonprocess

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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
