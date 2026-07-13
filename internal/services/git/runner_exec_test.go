package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealProcessProfileExecRunnerReturnsStdoutOnMergeTreeConflict(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.name", "Azedarach Test")
	runGit(t, repo, "config", "user.email", "azedarach@example.com")
	writeFile(t, filepath.Join(repo, "f.txt"), "init\n")
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-q", "-m", "init")

	runGit(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(repo, "f.txt"), "feature\n")
	runGit(t, repo, "commit", "-q", "-am", "feature change")

	runGit(t, repo, "checkout", "-q", "main")
	writeFile(t, filepath.Join(repo, "f.txt"), "main\n")
	runGit(t, repo, "commit", "-q", "-am", "main change")

	runner := NewExecRunner(repo)
	output, err := runner.Run(context.Background(), "merge-tree", "--write-tree", "main", "feature")
	if err == nil {
		t.Fatal("Run() error = nil, want merge-tree conflict")
	}
	if !strings.Contains(output, "CONFLICT") {
		t.Fatalf("Run() output = %q, want conflict markers", output)
	}
	if !strings.Contains(err.Error(), "CONFLICT") {
		t.Fatalf("Run() error = %v, want conflict details", err)
	}
}

func TestGitOperationSkipsGlobalOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "simple", args: []string{"status", "--porcelain"}, want: "status"},
		{name: "repo dir", args: []string{"-C", "/tmp/repo", "status", "--porcelain"}, want: "status"},
		{name: "config option", args: []string{"-c", "core.quotePath=false", "worktree", "list"}, want: "worktree"},
		{name: "long global equals", args: []string{"--git-dir=/tmp/repo/.git", "rev-parse", "--git-dir"}, want: "rev-parse"},
		{name: "version flag", args: []string{"--version"}, want: ""},
		{name: "empty", args: nil, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitOperation(tc.args); got != tc.want {
				t.Fatalf("gitOperation(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestGitEnvWithOverridesReplacesExistingKeys(t *testing.T) {
	got := gitEnvWithOverrides([]string{
		"PATH=/bin",
		"GIT_TRACE2_EVENT=/tmp/user-trace",
		"GIT_DIR=/tmp/repo/.git",
	}, []string{
		"GIT_TRACE2_EVENT=/tmp/az-trace",
	})

	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "/tmp/user-trace") {
		t.Fatalf("env = %v, still contains overridden trace2 value", got)
	}
	if !strings.Contains(joined, "GIT_TRACE2_EVENT=/tmp/az-trace") {
		t.Fatalf("env = %v, missing replacement trace2 value", got)
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "GIT_DIR=/tmp/repo/.git") {
		t.Fatalf("env = %v, want unrelated entries preserved", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
