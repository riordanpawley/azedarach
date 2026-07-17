package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

type testOutputBarrier struct {
	marker string
	ready  chan struct{}
	once   sync.Once
	mu     sync.Mutex
	output strings.Builder
}

type testProcessReapBarrier struct {
	path        string
	dummyWriter *os.File
	reaped      chan error
}

func TestProcessOutputObserverRunsAfterCapture(t *testing.T) {
	var captured bytes.Buffer
	observerCalled := false
	writer := processOutputWriter{
		stream: "stdout",
		dst:    &captured,
		observer: func(stream string, output []byte) {
			observerCalled = true
			if stream != "stdout" {
				t.Fatalf("observer stream = %q, want stdout", stream)
			}
			if got := captured.String(); got != string(output) {
				t.Fatalf("captured output at observer = %q, want %q", got, output)
			}
		},
	}
	if _, err := writer.Write([]byte("captured-before-ack")); err != nil {
		t.Fatalf("write observed output: %v", err)
	}
	if !observerCalled {
		t.Fatal("output observer was not called")
	}
}

func TestProcessReapSupervisor(t *testing.T) {
	if os.Getenv("AZEDARACH_TEST_REAP_SUPERVISOR") != "1" {
		return
	}

	managedProcessGroup := syscall.Getpgrp()
	if err := syscall.Setpgid(0, 0); err != nil {
		t.Fatalf("leave managed process group: %v", err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve cancellation blocker binary: %v", err)
	}
	blockerRead, blockerWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create cancellation blocker pipe: %v", err)
	}
	defer blockerRead.Close()
	defer blockerWrite.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create cancellation readiness pipe: %v", err)
	}
	defer readyRead.Close()
	defer readyWrite.Close()

	child := exec.Command(testBinary, "-test.run=^TestProcessCancellationBlocker$")
	child.Env = append(os.Environ(), "AZEDARACH_TEST_CANCELLATION_BLOCKER=1")
	child.ExtraFiles = []*os.File{blockerRead, readyWrite}
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: managedProcessGroup}
	if err := child.Start(); err != nil {
		t.Fatalf("start supervised process-group child: %v", err)
	}
	childWaited := false
	defer func() {
		if childWaited {
			return
		}
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	_ = blockerRead.Close()
	_ = readyWrite.Close()
	var ready [1]byte
	if count, err := readyRead.Read(ready[:]); err != nil || count != len(ready) {
		t.Fatalf("receive cancellation blocker readiness: count=%d err=%v", count, err)
	}
	writeTestFIFO(t, os.Getenv("AZEDARACH_TEST_CHILD_PID_FILE"), strconv.Itoa(child.Process.Pid)+"\n")
	if err := child.Wait(); err == nil {
		t.Fatal("supervised process-group child exited without cancellation")
	}
	childWaited = true
	writeTestFIFO(t, os.Getenv("AZEDARACH_TEST_CHILD_REAP_FIFO"), "reaped\n")
}

func TestProcessCancellationBlocker(t *testing.T) {
	if os.Getenv("AZEDARACH_TEST_CANCELLATION_BLOCKER") != "1" {
		return
	}

	blocker := os.NewFile(3, "cancellation-blocker")
	ready := os.NewFile(4, "cancellation-ready")
	if blocker == nil || ready == nil {
		t.Fatal("cancellation blocker file descriptors unavailable")
	}
	defer blocker.Close()
	defer ready.Close()
	signal.Ignore(syscall.SIGTERM)
	if _, err := ready.Write([]byte{1}); err != nil {
		t.Fatalf("publish cancellation blocker readiness: %v", err)
	}
	_ = ready.Close()
	var release [1]byte
	if count, err := blocker.Read(release[:]); err != nil || count != len(release) {
		t.Fatalf("cancellation blocker released without process cancellation: count=%d err=%v", count, err)
	}
	t.Fatal("cancellation blocker received an unexpected release event")
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
	childReaped := newTestProcessReapBarrier(t)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	t.Setenv("AZEDARACH_TEST_REAP_SUPERVISOR", "1")
	t.Setenv("AZEDARACH_TEST_REAP_SUPERVISOR_BINARY", testBinary)
	t.Setenv("AZEDARACH_TEST_CHILD_PID_FILE", childReady.path)
	t.Setenv("AZEDARACH_TEST_CHILD_REAP_FIFO", childReaped.path)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	hook := "#!/bin/sh\n\"$AZEDARACH_TEST_REAP_SUPERVISOR_BINARY\" -test.run '^TestProcessReapSupervisor$' &\nsupervisor=$!\nprintf 'exec-runner-child-ready\\n'\nwait \"$supervisor\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write blocking pre-commit hook: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result testProcessResult
	done := make(chan struct{})
	outputReady := newTestOutputBarrier("exec-runner-child-ready")
	runner := NewExecRunner(repo)
	runner.outputObserver = outputReady.observe
	go func() {
		result.stdout, result.err = runner.Run(ctx, "commit", "--allow-empty", "-m", "exercise cancellation")
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	childReady.wait(t, done, &result)
	outputReady.wait(t, done, &result)

	cancel()
	childReaped.wait(t)
	<-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", result.err)
	}
	publicEvidence := result.stdout + "\n" + result.err.Error()
	if !strings.Contains(publicEvidence, "exec-runner-child-ready") {
		t.Fatalf("Run() public output = %q, want preserved child readiness evidence", publicEvidence)
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
	childReady := newTestProcessBarrier(t)
	childReaped := newTestProcessReapBarrier(t)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	body := "#!/bin/sh\ntrap '' TERM\n\"$AZEDARACH_TEST_REAP_SUPERVISOR_BINARY\" -test.run '^TestProcessReapSupervisor$' &\nsupervisor=$!\nprintf 'candidate-gate-child-ready\\n'\nwait \"$supervisor\"\n"
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
		"AZEDARACH_TEST_REAP_SUPERVISOR=1",
		"AZEDARACH_TEST_REAP_SUPERVISOR_BINARY=" + testBinary,
		"AZEDARACH_TEST_CHILD_PID_FILE=" + childReady.path,
		"AZEDARACH_TEST_CHILD_REAP_FIFO=" + childReaped.path,
	})
	outputReady := newTestOutputBarrier("candidate-gate-child-ready")
	go func() {
		result.stdout, result.stderr, result.err = runProcessGroupCommandWithObserver(ctx, repo, env, outputReady.observe, gatePath)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	childReady.wait(t, done, &result)
	outputReady.wait(t, done, &result)
	cancel()
	childReaped.wait(t)
	<-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("candidate gate error = %v, want context cancellation", result.err)
	}
	if output := result.stdout + "\n" + result.stderr; !strings.Contains(output, "candidate-gate-child-ready") {
		t.Fatalf("candidate gate output = %q, want preserved child readiness output", output)
	}
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

func newTestOutputBarrier(marker string) *testOutputBarrier {
	return &testOutputBarrier{
		marker: marker,
		ready:  make(chan struct{}),
	}
}

func (b *testOutputBarrier) observe(_ string, output []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _ = b.output.Write(output)
	if strings.Contains(b.output.String(), b.marker) {
		b.once.Do(func() { close(b.ready) })
	}
}

func (b *testOutputBarrier) wait(t *testing.T, processDone <-chan struct{}, result *testProcessResult) {
	t.Helper()
	select {
	case <-b.ready:
	case <-processDone:
		t.Fatalf("process exited before outer output capture acknowledged %q: %v\nstdout: %s\nstderr: %s", b.marker, result.err, result.stdout, result.stderr)
	}
}

func newTestProcessReapBarrier(t *testing.T) *testProcessReapBarrier {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child-reaped")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create child reap FIFO: %v", err)
	}
	barrier := &testProcessReapBarrier{
		path:   path,
		reaped: make(chan error, 1),
	}
	readerOpened := make(chan error, 1)
	go func() {
		reader, err := os.Open(path)
		readerOpened <- err
		if err != nil {
			barrier.reaped <- err
			return
		}
		defer reader.Close()
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "reaped" {
			err = errors.New("unexpected child reap acknowledgement")
		}
		barrier.reaped <- err
	}()

	dummyWriter, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open child reap FIFO writer: %v", err)
	}
	barrier.dummyWriter = dummyWriter
	if err := <-readerOpened; err != nil {
		_ = dummyWriter.Close()
		t.Fatalf("open child reap FIFO reader: %v", err)
	}
	t.Cleanup(func() { _ = barrier.dummyWriter.Close() })
	return barrier
}

func (b *testProcessReapBarrier) wait(t *testing.T) {
	t.Helper()
	if err := <-b.reaped; err != nil {
		t.Fatalf("receive child reap acknowledgement: %v", err)
	}
	_ = b.dummyWriter.Close()
}

func writeTestFIFO(t *testing.T, path, value string) {
	t.Helper()
	if path == "" {
		t.Fatal("test FIFO path is empty")
	}
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open test FIFO %s: %v", path, err)
	}
	defer writer.Close()
	if _, err := writer.WriteString(value); err != nil {
		t.Fatalf("write test FIFO %s: %v", path, err)
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
