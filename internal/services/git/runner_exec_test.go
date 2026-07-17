package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestRealProcessProfileExecRunnerCancellationDrainsGitProcessGroup(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	childPIDFile := filepath.Join(repo, "child.pid")
	t.Setenv("AZEDARACH_TEST_CHILD_PID_FILE", childPIDFile)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	hook := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s' \"$child\" >\"$AZEDARACH_TEST_CHILD_PID_FILE\"\nwait \"$child\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write blocking pre-commit hook: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := NewExecRunner(repo).Run(ctx, "commit", "--allow-empty", "-m", "exercise cancellation")
		done <- err
	}()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for childPID == 0 {
		contents, err := os.ReadFile(childPIDFile)
		if err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(contents)))
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for child PID in %s", childPIDFile)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}

	deadline = time.Now().Add(2 * time.Second)
	for syscall.Kill(childPID, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("git descendant process %d still alive after cancellation", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRealProcessProfileCandidateGateCancellationDrainsTimeoutDescendants(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		if _, err := exec.LookPath("gtimeout"); err != nil {
			t.Skip("GNU timeout unavailable")
		}
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "status.showUntrackedFiles", "no")
	runGit(t, repo, "commit", "--allow-empty", "-m", "candidate")
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	sourceWrapper := filepath.Join("..", "..", "..", "scripts", "git-merge-rebase-gate.sh")
	wrapper, err := os.ReadFile(sourceWrapper)
	if err != nil {
		t.Fatalf("read candidate gate wrapper: %v", err)
	}
	gatePath := filepath.Join(scriptsDir, "git-merge-rebase-gate.sh")
	if err := os.WriteFile(gatePath, wrapper, 0o755); err != nil {
		t.Fatalf("write candidate gate wrapper: %v", err)
	}
	childPIDFile := filepath.Join(t.TempDir(), "gate-child.pid")
	body := "#!/bin/sh\ntrap '' TERM\nsleep 30 &\nchild=$!\nprintf '%s' \"$child\" >\"$AZEDARACH_TEST_CHILD_PID_FILE\"\nwait \"$child\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate-body.sh"), []byte(body), 0o755); err != nil {
		t.Fatalf("write candidate gate body: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	env := gitEnvWithOverrides(sanitizedGitEnv(os.Environ()), []string{
		"AZEDARACH_CANDIDATE_HEAD=" + strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD")),
		"AZEDARACH_MERGE_GATE_BODY=" + filepath.Join(scriptsDir, "git-merge-rebase-gate-body.sh"),
		"AZEDARACH_SKIP_MERGE_REBASE_GATE=0",
		"AZEDARACH_MERGE_GATE_TIMEOUT=1h",
		"AZEDARACH_TEST_CHILD_PID_FILE=" + childPIDFile,
	})
	go func() {
		_, _, err := runProcessGroupCommand(ctx, repo, env, gatePath)
		done <- err
	}()

	childPID := waitForTestPID(t, childPIDFile)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("candidate gate error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("candidate gate did not return after cancellation")
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(childPID, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("candidate gate descendant %d still alive after cancellation", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForTestPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		contents, err := os.ReadFile(path)
		if err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents))); parseErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for PID in %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
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
