package daemonprocess

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
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
	launcher.BinPath = "true"
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
}
