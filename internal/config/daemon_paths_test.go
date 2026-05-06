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

func TestScopedDaemonPathsUseWorktreeRoot(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("PATH", "")

	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	wantRuntime := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "azedarach", "scopes", daemonScopeID(worktree))
	if got := ScopedDaemonRuntimeDir(nested); got != wantRuntime {
		t.Fatalf("ScopedDaemonRuntimeDir() = %q, want %q", got, wantRuntime)
	}
	if got := ScopedDaemonSocketPath(nested); got != filepath.Join(wantRuntime, "daemon.sock") {
		t.Fatalf("ScopedDaemonSocketPath() = %q, want %q", got, filepath.Join(wantRuntime, "daemon.sock"))
	}
	if got := ScopedDaemonLockPath(nested); got != filepath.Join(wantRuntime, "daemon.lock") {
		t.Fatalf("ScopedDaemonLockPath() = %q, want %q", got, filepath.Join(wantRuntime, "daemon.lock"))
	}
}

func TestScopedDaemonPathsDifferAcrossWorktrees(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	pathA := filepath.Join(t.TempDir(), "repo-a")
	pathB := filepath.Join(t.TempDir(), "repo-b")
	if gotA, gotB := ScopedDaemonSocketPath(pathA), ScopedDaemonSocketPath(pathB); gotA == gotB {
		t.Fatalf("ScopedDaemonSocketPath() should differ across scopes; got %q for both", gotA)
	}
}

func TestDaemonPathsDefaultToGlobal(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	start := filepath.Join(t.TempDir(), "repo")
	if got := DaemonSocketPathFor(start); got != GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, GlobalDaemonSocketPath())
	}
	if got := DaemonLockPathFor(start); got != GlobalDaemonLockPath() {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, GlobalDaemonLockPath())
	}
}

func TestDaemonPathsUseScopedWhenEnabled(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	start := filepath.Join(t.TempDir(), "repo")
	if got := DaemonSocketPathFor(start); got != ScopedDaemonSocketPath(start) {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, ScopedDaemonSocketPath(start))
	}
	if got := DaemonLockPathFor(start); got != ScopedDaemonLockPath(start) {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, ScopedDaemonLockPath(start))
	}
}

func TestPreferredDaemonSocketPathForUsesLiveScopedSocketOutsideScopedEnvironment(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")

	start := filepath.Join(t.TempDir(), "repo")
	scoped := ScopedDaemonSocketPath(start)
	if err := os.MkdirAll(filepath.Dir(scoped), 0o755); err != nil {
		t.Fatalf("mkdir scoped runtime: %v", err)
	}
	if err := os.WriteFile(scoped, []byte("socket placeholder"), 0o644); err != nil {
		t.Fatalf("write scoped socket placeholder: %v", err)
	}

	if got := PreferredDaemonSocketPathFor(start); got != scoped {
		t.Fatalf("PreferredDaemonSocketPathFor() = %q, want %q", got, scoped)
	}
}

func TestPreferredDaemonSocketPathForFallsBackToDefaultWhenScopedSocketMissing(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")

	start := filepath.Join(t.TempDir(), "repo")
	if got := PreferredDaemonSocketPathFor(start); got != GlobalDaemonSocketPath() {
		t.Fatalf("PreferredDaemonSocketPathFor() = %q, want %q", got, GlobalDaemonSocketPath())
	}
}
