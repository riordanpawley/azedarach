package config

import (
	"os"
	"path/filepath"
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
