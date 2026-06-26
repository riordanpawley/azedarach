package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type fakeScheduledScriptRunner struct {
	mu      sync.Mutex
	calls   []fakeScheduledScriptCall
	output  []byte
	err     error
	wait    <-chan struct{}
	started chan struct{}
}

type fakeScheduledScriptCall struct {
	shell   string
	command string
	cwd     string
	env     []string
}

func (r *fakeScheduledScriptRunner) Run(ctx context.Context, shell, command, cwd string, env []string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, fakeScheduledScriptCall{
		shell:   shell,
		command: command,
		cwd:     cwd,
		env:     append([]string(nil), env...),
	})
	started := r.started
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if r.wait != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.wait:
		}
	}
	return append([]byte(nil), r.output...), r.err
}

func (r *fakeScheduledScriptRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fakeScheduledScriptRunner) lastCall() fakeScheduledScriptCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return fakeScheduledScriptCall{}
	}
	return r.calls[len(r.calls)-1]
}

func TestScheduledScriptsRunDueScriptAndRecordStatus(t *testing.T) {
	root := scheduledScriptTestRepo(t, "repo-a")
	runner := &fakeScheduledScriptRunner{output: []byte("ok\n")}
	d := New(Config{
		RepoDir:               root,
		SessionShell:          "sh",
		ScheduledScripts:      scheduledScriptsConfig("prune", "echo ok", true, "1s", 1000),
		scheduledScriptRunner: runner,
	})
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	d.scheduledScripts.RegisterProject(protocol.DefaultProjectID, now)
	d.scheduledScripts.RunDue(context.Background(), now.Add(time.Second))

	status := waitForScheduledScriptStatus(t, d, protocol.DefaultProjectID, "prune", func(status protocol.ScheduledScriptStatus) bool {
		return status.RunCount == 1
	})
	if status.LastError != "" || status.LastExitCode != 0 || status.LastOutput != "ok" {
		t.Fatalf("status = %+v, want successful ok run", status)
	}
	if status.LastLogPath == "" {
		t.Fatalf("last log path empty in status %+v", status)
	}
	if _, err := os.Stat(status.LastLogPath); err != nil {
		t.Fatalf("stat last log path: %v", err)
	}
	call := runner.lastCall()
	if call.command != "echo ok" || call.cwd != root {
		t.Fatalf("call = %+v, want command in project root %s", call, root)
	}
	if !envContains(call.env, "AZEDARACH_ROOT_PATH="+root) || !envContains(call.env, "AZEDARACH_SCHEDULED_SCRIPT_NAME=prune") {
		t.Fatalf("env = %v, want scheduled script context", call.env)
	}
}

func TestScheduledScriptsDoNotRunDisabledScripts(t *testing.T) {
	root := scheduledScriptTestRepo(t, "repo-disabled")
	runner := &fakeScheduledScriptRunner{output: []byte("nope\n")}
	d := New(Config{
		RepoDir:               root,
		SessionShell:          "sh",
		ScheduledScripts:      scheduledScriptsConfig("disabled", "echo nope", false, "1s", 1000),
		scheduledScriptRunner: runner,
	})
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	d.scheduledScripts.RegisterProject(protocol.DefaultProjectID, now)
	d.scheduledScripts.RunDue(context.Background(), now.Add(time.Hour))

	status := d.scheduledScripts.Status(protocol.DefaultProjectID, []string{"disabled"}).Scripts[0]
	if runner.callCount() != 0 || status.RunCount != 0 || status.NextRunAt != nil {
		t.Fatalf("disabled status = %+v calls=%d, want no scheduled run", status, runner.callCount())
	}
}

func TestScheduledScriptsRecordFailedRuns(t *testing.T) {
	root := scheduledScriptTestRepo(t, "repo-fail")
	runner := &fakeScheduledScriptRunner{output: []byte("bad\n"), err: errors.New("exit 12")}
	d := New(Config{
		RepoDir:               root,
		SessionShell:          "sh",
		ScheduledScripts:      scheduledScriptsConfig("fail", "false", true, "1s", 1000),
		scheduledScriptRunner: runner,
	})
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	d.scheduledScripts.RegisterProject(protocol.DefaultProjectID, now)
	d.scheduledScripts.RunDue(context.Background(), now.Add(time.Second))

	status := waitForScheduledScriptStatus(t, d, protocol.DefaultProjectID, "fail", func(status protocol.ScheduledScriptStatus) bool {
		return status.RunCount == 1
	})
	if status.LastExitCode != 1 || !strings.Contains(status.LastError, "exit 12") || status.LastOutput != "bad" {
		t.Fatalf("status = %+v, want recorded failure", status)
	}
}

func TestScheduledScriptsRecordTimeouts(t *testing.T) {
	root := scheduledScriptTestRepo(t, "repo-timeout")
	runner := &fakeScheduledScriptRunner{wait: make(chan struct{}), started: make(chan struct{}, 1)}
	d := New(Config{
		RepoDir:               root,
		SessionShell:          "sh",
		ScheduledScripts:      scheduledScriptsConfig("timeout", "sleep 10", true, "1s", 10),
		scheduledScriptRunner: runner,
	})
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	d.scheduledScripts.RegisterProject(protocol.DefaultProjectID, now)
	d.scheduledScripts.RunDue(context.Background(), now.Add(time.Second))
	<-runner.started

	status := waitForScheduledScriptStatus(t, d, protocol.DefaultProjectID, "timeout", func(status protocol.ScheduledScriptStatus) bool {
		return status.RunCount == 1
	})
	if status.LastExitCode != -1 || status.LastError != "timeout" {
		t.Fatalf("status = %+v, want timeout", status)
	}
}

func TestScheduledScriptsPreventOverlapByDefault(t *testing.T) {
	root := scheduledScriptTestRepo(t, "repo-overlap")
	release := make(chan struct{})
	runner := &fakeScheduledScriptRunner{wait: release, started: make(chan struct{}, 1)}
	d := New(Config{
		RepoDir:               root,
		SessionShell:          "sh",
		ScheduledScripts:      scheduledScriptsConfig("slow", "sleep 1", true, "1s", 1000),
		scheduledScriptRunner: runner,
	})
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	d.scheduledScripts.RegisterProject(protocol.DefaultProjectID, now)
	d.scheduledScripts.RunDue(context.Background(), now.Add(time.Second))
	<-runner.started

	d.scheduledScripts.RunDue(context.Background(), now.Add(2*time.Second))
	status := d.scheduledScripts.Status(protocol.DefaultProjectID, []string{"slow"}).Scripts[0]
	if status.SkipCount != 1 || runner.callCount() != 1 {
		t.Fatalf("overlap status = %+v calls=%d, want one skipped overlap", status, runner.callCount())
	}
	close(release)
	_ = waitForScheduledScriptStatus(t, d, protocol.DefaultProjectID, "slow", func(status protocol.ScheduledScriptStatus) bool {
		return status.RunCount == 1 && !status.Running
	})
}

func TestScheduledScriptsKeepProjectRootsIsolated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootA := scheduledScriptTestRepo(t, "repo-a")
	rootB := scheduledScriptTestRepo(t, "repo-b")
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{{Name: "proj-b", Path: rootB}},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rootB, ".azedarach"), 0o755); err != nil {
		t.Fatalf("create rootB config dir: %v", err)
	}
	rootBConfig := `{"$version":9,"scheduledScripts":{"scripts":[{"name":"b","command":"echo b","enabled":true,"interval":"1s","cwd":"sub"}]}}`
	if err := os.WriteFile(filepath.Join(rootB, ".azedarach", "config.json"), []byte(rootBConfig), 0o644); err != nil {
		t.Fatalf("write rootB config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rootB, "sub"), 0o755); err != nil {
		t.Fatalf("create rootB subdir: %v", err)
	}

	runner := &fakeScheduledScriptRunner{output: []byte("ok\n")}
	d := New(Config{
		RepoDir:               rootA,
		SessionShell:          "sh",
		ScheduledScripts:      scheduledScriptsConfig("a", "echo a", true, "1s", 1000),
		scheduledScriptRunner: runner,
	})
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	d.scheduledScripts.RegisterProject(protocol.DefaultProjectID, now)
	d.scheduledScripts.RegisterProject("proj-b", now)
	d.scheduledScripts.RunDue(context.Background(), now.Add(time.Second))

	waitForCallCount(t, runner, 2)
	statusA := waitForScheduledScriptStatus(t, d, protocol.DefaultProjectID, "a", func(status protocol.ScheduledScriptStatus) bool {
		return status.RunCount == 1
	})
	statusB := waitForScheduledScriptStatus(t, d, "proj-b", "b", func(status protocol.ScheduledScriptStatus) bool {
		return status.RunCount == 1
	})
	if statusA.CWD != rootA {
		t.Fatalf("project A cwd = %q, want %q", statusA.CWD, rootA)
	}
	if want := filepath.Join(rootB, "sub"); statusB.CWD != want {
		t.Fatalf("project B cwd = %q, want %q", statusB.CWD, want)
	}
}

func TestScheduledScriptWorkerRegistersConfiguredProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootA := scheduledScriptTestRepo(t, "repo-a")
	rootB := scheduledScriptTestRepo(t, "repo-b")
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{
		Projects: []appconfig.Project{{Name: "proj-b", Path: rootB}},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rootB, ".azedarach"), 0o755); err != nil {
		t.Fatalf("create rootB config dir: %v", err)
	}
	rootBConfig := `{"$version":9,"scheduledScripts":{"scripts":[{"name":"registered","command":"echo b","enabled":true,"interval":"1s"}]}}`
	if err := os.WriteFile(filepath.Join(rootB, ".azedarach", "config.json"), []byte(rootBConfig), 0o644); err != nil {
		t.Fatalf("write rootB config: %v", err)
	}

	d := New(Config{
		RepoDir:          rootA,
		SessionShell:     "sh",
		ScheduledScripts: scheduledScriptsConfig("base", "echo a", true, "1s", 1000),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.startScheduledScriptWorker(ctx)
	defer d.scheduledScripts.Close()

	status := d.scheduledScripts.Status("proj-b", []string{"registered"})
	if len(status.Scripts) != 1 || status.Scripts[0].CWD != rootB {
		t.Fatalf("registered project status = %+v, want proj-b script rooted at %s", status, rootB)
	}
}

func scheduledScriptsConfig(name, command string, enabled bool, interval string, timeoutMs int) appconfig.ScheduledScriptsConfig {
	return appconfig.ScheduledScriptsConfig{
		Scripts: []appconfig.ScheduledScriptConfig{{
			Name:      name,
			Command:   command,
			Enabled:   enabled,
			Interval:  interval,
			TimeoutMs: timeoutMs,
		}},
	}
}

func scheduledScriptTestRepo(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return root
}

func waitForScheduledScriptStatus(t *testing.T, d *Daemon, projectID, name string, pred func(protocol.ScheduledScriptStatus) bool) protocol.ScheduledScriptStatus {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := d.scheduledScripts.Status(projectID, []string{name})
		if len(result.Scripts) == 1 && pred(result.Scripts[0]) {
			return result.Scripts[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	result := d.scheduledScripts.Status(projectID, []string{name})
	if len(result.Scripts) == 0 {
		t.Fatalf("script %q not found in status %+v", name, result)
	}
	t.Fatalf("timed out waiting for script %q status; last=%+v", name, result.Scripts[0])
	return protocol.ScheduledScriptStatus{}
}

func waitForCallCount(t *testing.T, runner *fakeScheduledScriptRunner, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runner.callCount() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("call count = %d, want at least %d", runner.callCount(), want)
}

func envContains(env []string, want string) bool {
	for _, got := range env {
		if got == want {
			return true
		}
	}
	return false
}
