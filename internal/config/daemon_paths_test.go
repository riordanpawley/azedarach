package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobalDaemonRuntimeDirUsesXdgRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))

	got := GlobalDaemonRuntimeDir()
	want := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "azedarach")
	if got != want {
		t.Fatalf("GlobalDaemonRuntimeDir() = %q, want %q", got, want)
	}
}

func TestGlobalDaemonRuntimeDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	fakeHome := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatalf("mkdir fake home: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	got := GlobalDaemonRuntimeDir()
	want := filepath.Join(fakeHome, ".azedarach", "run")
	if got != want {
		t.Fatalf("GlobalDaemonRuntimeDir() = %q, want %q", got, want)
	}
}

func TestGlobalDaemonRuntimeDirSkipsUnwritableCandidates(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(string(os.PathSeparator), "dev", "null"))
	fakeHome := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatalf("mkdir fake home: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	got := GlobalDaemonRuntimeDir()
	want := filepath.Join(fakeHome, ".azedarach", "run")
	if got != want {
		t.Fatalf("GlobalDaemonRuntimeDir() = %q, want %q", got, want)
	}
}

func TestGlobalDaemonPaths(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))

	if got, want := GlobalDaemonSocketPath(), filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "azedarach", "daemon.sock"); got != want {
		t.Fatalf("GlobalDaemonSocketPath() = %q, want %q", got, want)
	}
	if got, want := GlobalDaemonLockPath(), filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "azedarach", "daemon.lock"); got != want {
		t.Fatalf("GlobalDaemonLockPath() = %q, want %q", got, want)
	}
}
