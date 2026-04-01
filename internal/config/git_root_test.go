package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProjectRootReturnsBaseRepoForWorktreePath(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	got, err := ResolveProjectRoot(nested)
	if err != nil {
		t.Fatalf("ResolveProjectRoot() error = %v", err)
	}
	if got != repo {
		t.Fatalf("ResolveProjectRoot() = %q, want %q", got, repo)
	}
}

func TestResolveProjectRootFallsBackToAbsolutePathOutsideGit(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}

	got, err := ResolveProjectRoot(nested)
	if err != nil {
		t.Fatalf("ResolveProjectRoot() error = %v", err)
	}
	if got != nested {
		t.Fatalf("ResolveProjectRoot() = %q, want %q", got, nested)
	}
}

func TestResolveWorktreeRootReturnsWorktreeTopLevel(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	got, err := ResolveWorktreeRoot(nested)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot() error = %v", err)
	}
	if got != worktree {
		t.Fatalf("ResolveWorktreeRoot() = %q, want %q", got, worktree)
	}
}

func TestGitExecEnvStripsAmbientGitRouting(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin",
		"GIT_DIR=/tmp/repo/.git",
		"GIT_WORK_TREE=/tmp/repo",
		"GIT_COMMON_DIR=/tmp/repo/.git",
		"GIT_INDEX_FILE=/tmp/repo/.git/index",
		"GIT_OBJECT_DIRECTORY=/tmp/repo/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/tmp/objects",
		"HOME=/tmp/home",
	}
	got := gitExecEnv(in)

	for _, kv := range got {
		if strings.HasPrefix(kv, "GIT_DIR=") ||
			strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_COMMON_DIR=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=") ||
			strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY=") ||
			strings.HasPrefix(kv, "GIT_ALTERNATE_OBJECT_DIRECTORIES=") {
			t.Fatalf("unexpected git routing var in env: %s", kv)
		}
	}
}
