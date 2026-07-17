package git

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

type testProcessResult struct {
	stdout string
	stderr string
	err    error
}

type testProcessBarrier struct {
	path        string
	dummyWriter *os.File
	ready       chan testProcessBarrierResult
}

type testProcessBarrierResult struct {
	pid int
	err error
}

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
	childReady := newTestProcessBarrier(t)
	t.Setenv("AZEDARACH_TEST_CHILD_PID_FILE", childReady.path)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	hook := "#!/bin/sh\nsleep 30 &\nchild=$!\nkill -0 \"$child\"\nprintf 'exec-runner-child-ready\\n'\nprintf '%s\\n' \"$child\" >\"$AZEDARACH_TEST_CHILD_PID_FILE\"\nwait \"$child\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write blocking pre-commit hook: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result testProcessResult
	done := make(chan struct{})
	go func() {
		result.stdout, result.err = NewExecRunner(repo).Run(ctx, "commit", "--allow-empty", "-m", "exercise cancellation")
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	childPID := childReady.wait(t, done, &result)

	cancel()
	<-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", result.err)
	}
	if !strings.Contains(result.stdout, "exec-runner-child-ready") {
		t.Fatalf("Run() stdout = %q, want preserved child readiness output", result.stdout)
	}
	assertTestProcessExited(t, childPID, "git descendant")
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
	childReady := newTestProcessBarrier(t)
	body := "#!/bin/sh\ntrap '' TERM\nsleep 30 &\nchild=$!\nkill -0 \"$child\"\nprintf 'candidate-gate-child-ready\\n'\nprintf '%s\\n' \"$child\" >\"$AZEDARACH_TEST_CHILD_PID_FILE\"\nwait \"$child\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "git-merge-rebase-gate-body.sh"), []byte(body), 0o755); err != nil {
		t.Fatalf("write candidate gate body: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result testProcessResult
	done := make(chan struct{})
	env := gitEnvWithOverrides(os.Environ(), []string{
		"AZEDARACH_CANDIDATE_HEAD=" + strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD")),
		"AZEDARACH_MERGE_GATE_TIMEOUT=1h",
		"AZEDARACH_TEST_CHILD_PID_FILE=" + childReady.path,
	})
	go func() {
		result.stdout, result.stderr, result.err = runProcessGroupCommand(ctx, repo, env, gatePath)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	childPID := childReady.wait(t, done, &result)
	cancel()
	<-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("candidate gate error = %v, want context cancellation", result.err)
	}
	if output := result.stdout + "\n" + result.stderr; !strings.Contains(output, "candidate-gate-child-ready") {
		t.Fatalf("candidate gate output = %q, want preserved child readiness output", output)
	}
	assertTestProcessExited(t, childPID, "candidate gate descendant")
}

func newTestProcessBarrier(t *testing.T) *testProcessBarrier {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child-ready")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create child readiness FIFO: %v", err)
	}
	barrier := &testProcessBarrier{
		path:  path,
		ready: make(chan testProcessBarrierResult, 1),
	}
	readerOpened := make(chan error, 1)
	go func() {
		reader, err := os.Open(path)
		readerOpened <- err
		if err != nil {
			barrier.ready <- testProcessBarrierResult{err: err}
			return
		}
		defer reader.Close()
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err != nil {
			barrier.ready <- testProcessBarrierResult{err: err}
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid <= 0 {
			err = errors.New("PID must be positive")
		}
		barrier.ready <- testProcessBarrierResult{pid: pid, err: err}
	}()

	dummyWriter, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open child readiness FIFO writer: %v", err)
	}
	barrier.dummyWriter = dummyWriter
	if err := <-readerOpened; err != nil {
		_ = dummyWriter.Close()
		t.Fatalf("open child readiness FIFO reader: %v", err)
	}
	t.Cleanup(func() { _ = barrier.dummyWriter.Close() })
	return barrier
}

func (b *testProcessBarrier) wait(t *testing.T, processDone <-chan struct{}, result *testProcessResult) int {
	t.Helper()
	select {
	case ready := <-b.ready:
		_ = b.dummyWriter.Close()
		if ready.err != nil {
			t.Fatalf("receive child readiness: %v", ready.err)
		}
		return ready.pid
	case <-processDone:
		_ = b.dummyWriter.Close()
		t.Fatalf("process exited before child readiness: %v\nstdout: %s\nstderr: %s", result.err, result.stdout, result.stderr)
		return 0
	}
}

func assertTestProcessExited(t *testing.T, pid int, description string) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("%s process %d still present after joined cancellation: %v", description, pid, err)
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
