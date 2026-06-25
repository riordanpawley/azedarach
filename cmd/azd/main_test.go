package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/logging"
)

func TestRedirectDaemonProcessOutputKeepsRotatingStderrWrites(t *testing.T) {
	repoDir := t.TempDir()
	logDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = logDir
	logPath := filepath.Join(logDir, logging.DaemonLogFileName)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(log dir): %v", err)
	}
	seed, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(seed) error = %v", err)
	}
	if err := seed.Truncate(logging.DefaultMaxLogBytes); err != nil {
		_ = seed.Close()
		t.Fatalf("Truncate(seed) error = %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("Close(seed) error = %v", err)
	}

	redirect, err := redirectDaemonProcessOutput(repoDir, cfg)
	if err != nil {
		t.Fatalf("redirectDaemonProcessOutput() error = %v", err)
	}
	if err := writeAll(os.Stderr, []byte("new\n")); err != nil {
		_ = redirect.Close()
		t.Fatalf("write new log: %v", err)
	}
	filler := strings.Repeat("x", int(logging.DefaultMaxLogBytes)-len("new\n"))
	if err := writeAll(os.Stderr, []byte(filler)); err != nil {
		_ = redirect.Close()
		t.Fatalf("write filler: %v", err)
	}
	if err := writeAll(os.Stderr, []byte("after-rotation\n")); err != nil {
		_ = redirect.Close()
		t.Fatalf("write after rotation: %v", err)
	}
	if err := redirect.Close(); err != nil {
		t.Fatalf("Close(redirect) error = %v", err)
	}

	if info, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("Stat(rotated backup) error = %v", err)
	} else if info.Size() <= 0 || info.Size() > logging.DefaultMaxLogBytes {
		t.Fatalf("rotated backup size = %d, want between 1 and %d", info.Size(), logging.DefaultMaxLogBytes)
	}
	if info, err := os.Stat(logPath + ".2"); err != nil {
		t.Fatalf("Stat(startup rotated backup) error = %v", err)
	} else if info.Size() != logging.DefaultMaxLogBytes {
		t.Fatalf("startup rotated backup size = %d, want %d", info.Size(), logging.DefaultMaxLogBytes)
	}
	if info, err := os.Stat(logPath); err != nil {
		t.Fatalf("Stat(active daemon log) error = %v", err)
	} else if info.Size() <= 0 || info.Size() > logging.DefaultMaxLogBytes {
		t.Fatalf("active daemon log size = %d, want between 1 and %d", info.Size(), logging.DefaultMaxLogBytes)
	}
	if got := mustReadFile(t, logPath); !strings.HasSuffix(got, "after-rotation\n") {
		t.Fatalf("active daemon log suffix = %q, want latest write", got[max(0, len(got)-64):])
	}
}

func writeAll(file *os.File, p []byte) error {
	for len(p) > 0 {
		n, err := file.Write(p)
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(b)
}

func TestResolveScopedWorktreeWatchPathUsesRepoDir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	t.Setenv("PATH", "")
	if got := resolveScopedWorktreeWatchPath(nested); got != worktree {
		t.Fatalf("resolveScopedWorktreeWatchPath(%q) = %q, want %q", nested, got, worktree)
	}
}

func TestValidateDaemonLaunchFenceRejectsWorktreeAzdForGlobalSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	executable := filepath.Join(worktree, "bin", "azd")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("MkdirAll(executable dir): %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	previousExecutable := daemonExecutable
	daemonExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { daemonExecutable = previousExecutable })

	err := validateDaemonLaunchFence(config.GlobalDaemonSocketPath())
	if err == nil {
		t.Fatal("validateDaemonLaunchFence() error = nil, want fence error")
	}
	if !strings.Contains(err.Error(), "refusing to use the shared production daemon") {
		t.Fatalf("validateDaemonLaunchFence() error = %q, want shared daemon fence error", err)
	}
}

func TestValidateDaemonLaunchFenceAllowsScopedSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	executable := filepath.Join(worktree, "bin", "azd")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("MkdirAll(executable dir): %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	previousExecutable := daemonExecutable
	daemonExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { daemonExecutable = previousExecutable })

	if err := validateDaemonLaunchFence(config.ScopedDaemonSocketPath(worktree)); err != nil {
		t.Fatalf("validateDaemonLaunchFence() error = %v", err)
	}
}
