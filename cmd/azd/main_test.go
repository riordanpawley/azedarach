package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsScopedDaemonMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		source string
		want bool
	}{
		{name: "worktree", in: "worktree", source: "just-run", want: true},
		{name: "scoped", in: "scoped", source: "just-run", want: true},
		{name: "local", in: "local", source: "just-run", want: true},
		{name: "whitespace and case", in: "  WorkTree  ", source: "  JUST-RUN  ", want: true},
		{name: "mode without source", in: "worktree", source: "", want: false},
		{name: "empty", in: "", want: false},
		{name: "global", in: "global", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isScopedDaemonMode(tt.in, tt.source); got != tt.want {
				t.Fatalf("isScopedDaemonMode(%q, %q) = %v, want %v", tt.in, tt.source, got, tt.want)
			}
		})
	}
}

func TestResolveScopedWorktreeWatchPathUsesRepoDir(t *testing.T) {
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

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	t.Setenv("PATH", "")
	if got := resolveScopedWorktreeWatchPath(nested); got != worktree {
		t.Fatalf("resolveScopedWorktreeWatchPath(%q) = %q, want %q", nested, got, worktree)
	}
}
