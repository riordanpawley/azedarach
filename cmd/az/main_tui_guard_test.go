package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateTUILaunchContextRejectsLinkedWorktreeWithDefaultGlobalDaemon(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	err := validateTUILaunchContext()
	if err == nil {
		t.Fatal("validateTUILaunchContext() error = nil, want default-global linked-worktree guard")
	}
	if !strings.Contains(err.Error(), "uses the shared production daemon") {
		t.Fatalf("error = %q, want shared production daemon guidance", err)
	}
}

func TestValidateTUILaunchContextRejectsLinkedWorktreeWithForcedGlobalScope(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")

	err := validateTUILaunchContext()
	if err == nil {
		t.Fatal("validateTUILaunchContext() error = nil, want forced-global linked-worktree guard")
	}
	if !strings.Contains(err.Error(), "uses the shared production daemon") {
		t.Fatalf("error = %q, want shared production daemon guidance", err)
	}
}

func TestValidateTUILaunchContextAllowsNonAzedarachLinkedWorktreeWithGlobalScope(t *testing.T) {
	_, worktree := makeLinkedWorktreeWithModule(t, "github.com/acme/chefy")
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	if err := validateTUILaunchContext(); err != nil {
		t.Fatalf("validateTUILaunchContext() error = %v, want nil", err)
	}
}

func TestValidateTUILaunchContextAllowsLinkedWorktreeWithExplicitWorktreeScope(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")

	if err := validateTUILaunchContext(); err != nil {
		t.Fatalf("validateTUILaunchContext() error = %v, want nil", err)
	}
}

func TestValidateTUILaunchContextAllowsMainWorktreeWithoutScope(t *testing.T) {
	repo, _ := makeLinkedWorktree(t)
	t.Chdir(repo)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	if err := validateTUILaunchContext(); err != nil {
		t.Fatalf("validateTUILaunchContext() error = %v, want nil", err)
	}
}

func makeLinkedWorktree(t *testing.T) (string, string) {
	return makeLinkedWorktreeWithModule(t, "github.com/riordanpawley/azedarach")
}

func makeLinkedWorktreeWithModule(t *testing.T, module string) (string, string) {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	worktree := filepath.Join(parent, "repo-wt")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module "+module+"\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "add", "go.mod")
	runGit(t, repo, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runGit(t, repo, "worktree", "add", "-b", "wt", worktree)
	return repo, worktree
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func gitTestEnv() []string {
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
