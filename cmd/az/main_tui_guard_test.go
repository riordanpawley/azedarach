package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
)

type fakeTUIProgramRunner struct {
	err error
}

func (r fakeTUIProgramRunner) Run() (tea.Model, error) {
	return nil, r.err
}

type recordingTUIDaemonStopper struct {
	stops  int
	err    error
	onStop func()
}

func (s *recordingTUIDaemonStopper) Stop(context.Context) error {
	s.stops++
	if s.onStop != nil {
		s.onStop()
	}
	return s.err
}

type tuiLaunchTestHooks struct {
	stopper   *recordingTUIDaemonStopper
	repoDir   string
	socket    string
	exitCodes []int
	events    []string
}

func installTUILaunchTestHooks(t *testing.T, programErr error) *tuiLaunchTestHooks {
	t.Helper()
	hooks := &tuiLaunchTestHooks{stopper: &recordingTUIDaemonStopper{}}
	hooks.stopper.onStop = func() {
		hooks.events = append(hooks.events, "stop")
	}
	previousStopper := newTUIDaemonStopper
	previousProgram := newTUIProgramRunner
	previousExit := exitProcess
	newTUIDaemonStopper = func(repoDir, socketPath string) tuiDaemonStopper {
		hooks.repoDir = repoDir
		hooks.socket = socketPath
		return hooks.stopper
	}
	newTUIProgramRunner = func(tea.Model) tuiProgramRunner {
		return fakeTUIProgramRunner{err: programErr}
	}
	exitProcess = func(code int) {
		hooks.events = append(hooks.events, "exit")
		hooks.exitCodes = append(hooks.exitCodes, code)
	}
	t.Cleanup(func() {
		newTUIDaemonStopper = previousStopper
		newTUIProgramRunner = previousProgram
		exitProcess = previousExit
	})
	return hooks
}

func forbidTUIDaemonStopper(t *testing.T) {
	t.Helper()
	previous := newTUIDaemonStopper
	newTUIDaemonStopper = func(repoDir, socketPath string) tuiDaemonStopper {
		t.Fatalf("newTUIDaemonStopper(%q, %q) called, want no scoped daemon cleanup", repoDir, socketPath)
		return nil
	}
	t.Cleanup(func() {
		newTUIDaemonStopper = previous
	})
}

func testTUIConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(os.TempDir(), "azedarach-test-logs")
	return cfg
}

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

func TestOwnedJustRunScopedDaemonCleanupStopsWorktreeScopedDaemon(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	hooks := installTUILaunchTestHooks(t, nil)

	cleanup := ownedJustRunScopedDaemonCleanup()
	if cleanup == nil {
		t.Fatal("ownedJustRunScopedDaemonCleanup() = nil, want cleanup")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v, want nil", err)
	}

	if hooks.stopper.stops != 1 {
		t.Fatalf("Stop calls = %d, want 1", hooks.stopper.stops)
	}
	wantWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatalf("EvalSymlinks(worktree): %v", err)
	}
	gotRepoDir, err := filepath.EvalSymlinks(hooks.repoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(hooks.repoDir): %v", err)
	}
	if gotRepoDir != wantWorktree {
		t.Fatalf("launcher repoDir = %q, want %q", hooks.repoDir, wantWorktree)
	}
	wantSocket := config.ScopedDaemonSocketPath(wantWorktree)
	if hooks.socket != wantSocket {
		t.Fatalf("launcher socket = %q, want %q", hooks.socket, wantSocket)
	}
	if hooks.socket == config.GlobalDaemonSocketPath() {
		t.Fatalf("launcher socket = global daemon socket %q, want scoped", hooks.socket)
	}
}

func TestOwnedJustRunScopedDaemonCleanupSkipsWithoutJustRunSource(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	forbidTUIDaemonStopper(t)

	if cleanup := ownedJustRunScopedDaemonCleanup(); cleanup != nil {
		t.Fatal("ownedJustRunScopedDaemonCleanup() returned cleanup without just-run ownership source")
	}
}

func TestOwnedJustRunScopedDaemonCleanupSkipsGlobalDaemonScope(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	forbidTUIDaemonStopper(t)

	if cleanup := ownedJustRunScopedDaemonCleanup(); cleanup != nil {
		t.Fatal("ownedJustRunScopedDaemonCleanup() returned cleanup for global daemon scope")
	}
}

func TestRunTUICleansOwnedScopedDaemonOnNormalExit(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	hooks := installTUILaunchTestHooks(t, nil)

	runTUIWithOptions(testTUIConfig())

	if hooks.stopper.stops != 1 {
		t.Fatalf("Stop calls = %d, want 1", hooks.stopper.stops)
	}
	if len(hooks.exitCodes) != 0 {
		t.Fatalf("exit codes = %v, want none", hooks.exitCodes)
	}
}

func TestRunTUICleansOwnedScopedDaemonBeforeErrorExit(t *testing.T) {
	_, worktree := makeLinkedWorktree(t)
	t.Chdir(worktree)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	hooks := installTUILaunchTestHooks(t, errors.New("program failed"))

	runTUIWithOptions(testTUIConfig())

	if hooks.stopper.stops != 1 {
		t.Fatalf("Stop calls = %d, want 1", hooks.stopper.stops)
	}
	if len(hooks.exitCodes) != 1 || hooks.exitCodes[0] != 1 {
		t.Fatalf("exit codes = %v, want [1]", hooks.exitCodes)
	}
	if got, want := strings.Join(hooks.events, ","), "stop,exit"; got != want {
		t.Fatalf("events = %v, want cleanup before exit", hooks.events)
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
