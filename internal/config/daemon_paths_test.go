package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestDaemonPathsDefaultToScopedForLinkedWorktree(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	_, worktree := makeLinkedDaemonPathWorktreeWithModule(t, "github.com/riordanpawley/azedarach")

	if got := DaemonSocketPathFor(worktree); got != ScopedDaemonSocketPath(worktree) {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, ScopedDaemonSocketPath(worktree))
	}
	if got := DaemonLockPathFor(worktree); got != ScopedDaemonLockPath(worktree) {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, ScopedDaemonLockPath(worktree))
	}
}

func TestDaemonPathsDefaultToGlobalForNonAzedarachLinkedWorktree(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	_, worktree := makeLinkedDaemonPathWorktreeWithModule(t, "github.com/acme/chefy")

	if got := DaemonSocketPathFor(worktree); got != GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, GlobalDaemonSocketPath())
	}
	if got := DaemonLockPathFor(worktree); got != GlobalDaemonLockPath() {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, GlobalDaemonLockPath())
	}
}

func TestDaemonPathsGlobalScopeOverridesLinkedWorktreeDefault(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	_, worktree := makeLinkedDaemonPathWorktreeWithModule(t, "github.com/riordanpawley/azedarach")

	if got := DaemonSocketPathFor(worktree); got != GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, GlobalDaemonSocketPath())
	}
	if got := DaemonLockPathFor(worktree); got != GlobalDaemonLockPath() {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, GlobalDaemonLockPath())
	}
}

func TestDaemonPathsUseScopedForLinkedWorktreeWithoutEnv(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	t.Setenv("PATH", "")

	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "cmd")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}

	if got := DaemonSocketPathFor(nested); got != ScopedDaemonSocketPath(nested) {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, ScopedDaemonSocketPath(nested))
	}
	if got := DaemonLockPathFor(nested); got != ScopedDaemonLockPath(nested) {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, ScopedDaemonLockPath(nested))
	}
}

func TestDaemonPathsUseScopedWhenEnabledInAzedarachDevelopmentWorktree(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	_, worktree := makeLinkedDaemonPathWorktreeWithModule(t, "github.com/riordanpawley/azedarach")

	if got := DaemonSocketPathFor(worktree); got != ScopedDaemonSocketPath(worktree) {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, ScopedDaemonSocketPath(worktree))
	}
	if got := DaemonLockPathFor(worktree); got != ScopedDaemonLockPath(worktree) {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, ScopedDaemonLockPath(worktree))
	}
}

func TestDaemonPathsIgnoreScopedEnvOutsideAzedarachDevelopmentWorktree(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "xdg-runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	_, worktree := makeLinkedDaemonPathWorktreeWithModule(t, "github.com/acme/chefy")

	if got := DaemonSocketPathFor(worktree); got != GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocketPathFor() = %q, want %q", got, GlobalDaemonSocketPath())
	}
	if got := DaemonLockPathFor(worktree); got != GlobalDaemonLockPath() {
		t.Fatalf("DaemonLockPathFor() = %q, want %q", got, GlobalDaemonLockPath())
	}
}

func makeLinkedDaemonPathWorktree(t *testing.T) (string, string) {
	return makeLinkedDaemonPathWorktreeWithModule(t, "github.com/riordanpawley/azedarach")
}

func makeLinkedDaemonPathWorktreeWithModule(t *testing.T, module string) (string, string) {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	worktree := filepath.Join(parent, "repo-wt")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runDaemonPathGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module "+module+"\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	runDaemonPathGit(t, repo, "add", "README.md")
	runDaemonPathGit(t, repo, "add", "go.mod")
	runDaemonPathGit(t, repo, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runDaemonPathGit(t, repo, "worktree", "add", "-b", "wt", worktree)
	return repo, worktree
}

func runDaemonPathGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = daemonPathGitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func daemonPathGitTestEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="),
			strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_COMMON_DIR="),
			strings.HasPrefix(kv, "GIT_INDEX_FILE="):
			continue
		default:
			out = append(out, kv)
		}
	}
	return out
}
