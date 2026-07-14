package daemonprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	"github.com/riordanpawley/azedarach/internal/logging"
)

type trackingWriteCloser struct {
	closed atomic.Bool
}

type recordingDaemonProcess struct {
	stopCalls atomic.Int32
	stopErr   error
	exitCh    chan error
}

func (p *recordingDaemonProcess) exited() <-chan error { return p.exitCh }

func (p *recordingDaemonProcess) stopAndWait(context.Context) error {
	p.stopCalls.Add(1)
	return p.stopErr
}

type recordingDaemonStarter struct {
	process *recordingDaemonProcess
	specs   []daemonProcessSpec
}

func useRecordingDaemonStarter(launcher *Launcher) *recordingDaemonStarter {
	starter := &recordingDaemonStarter{process: &recordingDaemonProcess{exitCh: make(chan error)}}
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		spec.args = append([]string(nil), spec.args...)
		starter.specs = append(starter.specs, spec)
		return starter.process, nil
	}
	return starter
}

func (w *trackingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *trackingWriteCloser) Close() error {
	w.closed.Store(true)
	return nil
}

func writeLauncherConfig(t *testing.T, repoDir, logDir string) {
	t.Helper()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir): %v", err)
	}
	body := fmt.Sprintf(`{"session":{"logDir":%q}}`, logDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(config.json): %v", err)
	}
}

func writeTestExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "azd-test")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(test executable): %v", err)
	}
	return path
}

func TestLauncherStartClosesDaemonLog(t *testing.T) {
	repoDir := t.TempDir()
	socketRoot, err := os.MkdirTemp(".", "azd-launcher-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketPath := filepath.Join(socketRoot, "daemon.sock")
	tracker := &trackingWriteCloser{}
	logDir := filepath.Join(t.TempDir(), "logs")
	writeLauncherConfig(t, repoDir, logDir)

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	if launcher.LockPath != filepath.Join(socketRoot, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(socketRoot, "daemon.lock"))
	}
	launcher.BinPath = writeTestExecutable(t)
	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(path string) (io.WriteCloser, error) {
		want := filepath.Join(logDir, logging.DaemonLogFileName)
		if path != want {
			t.Fatalf("daemon log path = %q, want %q", path, want)
		}
		return tracker, nil
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
	if len(starter.specs) != 1 {
		t.Fatalf("daemon starts = %d, want 1", len(starter.specs))
	}
	spec := starter.specs[0]
	wantArgs := []string{"--repo", repoDir, "--socket", socketPath, "--lock", launcher.LockPath}
	if spec.command.executable != launcher.BinPath || !reflect.DeepEqual(spec.args, wantArgs) || spec.command.dir != "" {
		t.Fatalf("daemon start spec = command %+v args %v, want executable %q args %v", spec.command, spec.args, launcher.BinPath, wantArgs)
	}
	if spec.stdout != tracker || spec.stderr != tracker {
		t.Fatalf("daemon start stdio = %T/%T, want shared tracked log", spec.stdout, spec.stderr)
	}
}

func TestLauncherStartUsesWorktreeLocalDaemonLogForScopedRuntime(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	writeLauncherConfig(t, repo, filepath.Join(t.TempDir(), "logs"))
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	socketRoot, err := os.MkdirTemp(".", "azd-launcher-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketPath := filepath.Join(socketRoot, "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(nested, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = writeTestExecutable(t)
	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(path string) (io.WriteCloser, error) {
		want := filepath.Join(worktree, ".azedarach", logging.DaemonLogFileName)
		if path != want {
			t.Fatalf("daemon log path = %q, want %q", path, want)
		}
		return tracker, nil
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestOpenDaemonLogRotatesOversizedLogAndReturnsRawFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), logging.DaemonLogFileName)
	seed, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(seed) error = %v", err)
	}
	if err := seed.Truncate(logging.DefaultMaxLogBytes); err != nil {
		_ = seed.Close()
		t.Fatalf("Truncate(seed) error = %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("Close(seed) error = %v", err)
	}

	logFile, err := openDaemonLog(path)
	if err != nil {
		t.Fatalf("openDaemonLog() error = %v", err)
	}
	rawFile, ok := logFile.(*os.File)
	if !ok {
		_ = logFile.Close()
		t.Fatalf("openDaemonLog() returned %T, want *os.File for daemon stdio handoff", logFile)
	}
	if _, err := rawFile.WriteString("new\n"); err != nil {
		_ = rawFile.Close()
		t.Fatalf("WriteString(new log) error = %v", err)
	}
	if err := rawFile.Close(); err != nil {
		t.Fatalf("Close(rawFile) error = %v", err)
	}

	if info, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("Stat(rotated backup) error = %v", err)
	} else if info.Size() != logging.DefaultMaxLogBytes {
		t.Fatalf("rotated backup size = %d, want %d", info.Size(), logging.DefaultMaxLogBytes)
	}
	if got := mustReadFile(t, path); got != "new\n" {
		t.Fatalf("active daemon log = %q, want new log content only", got)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(b)
}

func TestNewLauncherNormalizesWorktreeToBaseRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
	if launcher.LockPath != filepath.Join(base, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(base, "daemon.lock"))
	}
}

func TestNewLauncherKeepsLinkedWorktreeRootForExplicitScopedRuntime(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != worktree {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, worktree)
	}
}

func TestNewLauncherUsesBaseRepoRootForLinkedWorktreeByDefault(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
}

func TestNewLauncherKeepsMainWorktreeAtBaseRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")

	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}

	t.Setenv("PATH", "")
	launcher := NewLauncher(filepath.Join(repo, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
	if launcher.LockPath != filepath.Join(base, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(base, "daemon.lock"))
	}
}

func TestLauncherResolveBinary_UsesMonorepoGoBubbleteaBin(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	nestedBin := filepath.Join(repoDir, "go-bubbletea", "bin")
	if err := os.MkdirAll(nestedBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested bin): %v", err)
	}
	azd := filepath.Join(nestedBin, "azd")
	if err := os.WriteFile(azd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != azd {
		t.Fatalf("resolveBinary() = %q, want %q", got, azd)
	}
}

func TestLauncherResolveBinary_UsesWorkingDirBinFallback(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	cwd := t.TempDir()
	t.Chdir(cwd)

	binDir := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cwd bin): %v", err)
	}
	azd := filepath.Join(binDir, "azd")
	if err := os.WriteFile(azd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cwd azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != azd {
		t.Fatalf("resolveBinary() = %q, want %q", got, azd)
	}
}

func TestLauncherResolveBinary_PrefersWorkingDirBinOverRepoBin(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	cwd := t.TempDir()
	t.Chdir(cwd)

	repoBinDir := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(repoBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo bin): %v", err)
	}
	repoAzd := filepath.Join(repoBinDir, "azd")
	if err := os.WriteFile(repoAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(repo azd): %v", err)
	}

	cwdBinDir := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(cwdBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cwd bin): %v", err)
	}
	cwdAzd := filepath.Join(cwdBinDir, "azd")
	if err := os.WriteFile(cwdAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cwd azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != cwdAzd {
		t.Fatalf("resolveBinary() = %q, want %q", got, cwdAzd)
	}
}

func TestLauncherResolveCommand_UsesLocalGoRunForScopedWorktreeWithoutBinary(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, "cmd", "azd"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree cmd/azd): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	launcher := NewLauncher(filepath.Join(worktree, "nested"), filepath.Join(base, "daemon.sock"))

	got := launcher.resolveCommand()
	if got.executable != "go" {
		t.Fatalf("resolveCommand().executable = %q, want go", got.executable)
	}
	if strings.Join(got.args, " ") != "run ./cmd/azd" {
		t.Fatalf("resolveCommand().args = %q, want %q", strings.Join(got.args, " "), "run ./cmd/azd")
	}
	if got.dir != worktree {
		t.Fatalf("resolveCommand().dir = %q, want %q", got.dir, worktree)
	}
}

func TestLauncherResolveCommand_DaemonBinOverrideWinsOverScopedGoRun(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, "cmd", "azd"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree cmd/azd): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	override := filepath.Join(base, "override-azd")
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", override)
	launcher := NewLauncher(worktree, filepath.Join(base, "daemon.sock"))

	got := launcher.resolveCommand()
	if got.executable != override {
		t.Fatalf("resolveCommand().executable = %q, want %q", got.executable, override)
	}
	if len(got.args) != 0 || got.dir != "" {
		t.Fatalf("resolveCommand() args=%v dir=%q, want empty override command", got.args, got.dir)
	}
}

func TestLauncherResolveCommand_MainRepoStillFallsBackToPathAzd(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "cmd", "azd"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo cmd/azd): %v", err)
	}
	t.Chdir(t.TempDir())

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	launcher := NewLauncher(repoDir, socketPath)

	got := launcher.resolveCommand()
	if got.executable != "azd" {
		t.Fatalf("resolveCommand().executable = %q, want azd", got.executable)
	}
	if len(got.args) != 0 || got.dir != "" {
		t.Fatalf("resolveCommand() args=%v dir=%q, want PATH azd fallback", got.args, got.dir)
	}
}

func TestLauncherStart_SkipsSpawnWhenLockOwnerAlive(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.waitForReady = func(context.Context, string) error { return nil }
	launcher.sleepFn = func(time.Duration) {}

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil (skip spawn when daemon lock owner alive)", err)
	}
}

func TestLauncherStart_SpawnsWhenLockOwnerAliveButSocketUnready(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(lockPath string) error {
		terminateCalls++
		return os.Remove(lockPath)
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherReplaceTerminatesBeforeStart(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	terminated := false
	launcher.terminateLockOwner = func(lockPath string) error {
		terminated = true
		return os.Remove(lockPath)
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if !terminated {
			return nil
		}
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if !terminated {
		t.Fatal("Replace() did not terminate lock owner before Start")
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after replacement Start() returned")
	}
}

func TestLauncherReplaceGracefullyStopsSocketBeforeStart(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if socketUp {
		t.Fatal("Replace() did not request socket shutdown")
	}
	if !spawned {
		t.Fatal("Replace() did not start a replacement daemon")
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after replacement Start() returned")
	}
}

func TestLauncherReplaceAttributesGracefulShutdownReason(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	var gotReason string
	launcher.shutdownWithReason = func(_ context.Context, _ string, reason string) error {
		gotReason = reason
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error {
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if gotReason != "compatibility-replace" {
		t.Fatalf("shutdown reason = %q, want compatibility-replace", gotReason)
	}
}

func TestLauncherReplaceReasonOverride(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath).WithReplaceReason("manual-restart")
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	var gotReason string
	launcher.shutdownWithReason = func(_ context.Context, _ string, reason string) error {
		gotReason = reason
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error {
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if gotReason != "manual-restart" {
		t.Fatalf("shutdown reason = %q, want manual-restart", gotReason)
	}
}

func TestLauncherStart_ErrorsWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.terminateLockOwner = func(string) error { return errors.New("kill denied") }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	err = launcher.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "recover stale daemon lock owner") {
		t.Fatalf("Start() error = %v, want lock recovery failure", err)
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsEPERM(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return syscall.EPERM
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by permission fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsWrappedPermissionDenied(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return lifecycle.ErrLockOwnerPermissionDenied
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by permission fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsTerminationTimeout(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return fmt.Errorf("%w: pid %d", lifecycle.ErrLockOwnerTerminationTimeout, os.Getpid())
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by timeout fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_ForceClearsLockWhenTerminateReturnsPermissionDeniedString(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return errors.New("lock owner permission denied: operation not permitted")
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file should be removed by permission fallback, stat err = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherStart_RechecksSocketWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.sleepFn = func(time.Duration) {}
	launcher.terminateLockOwner = func(string) error { return errors.New("kill denied") }

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls < 4 {
			return context.DeadlineExceeded
		}
		return nil
	}

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil after recheck-ready", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
}

func TestLauncherStartHonorsCallerContextDeadlineForReadyWait(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > 100*time.Millisecond {
			return context.DeadlineExceeded
		}
		<-ctx.Done()
		return ctx.Err()
	}
	launcher.sleepFn = func(time.Duration) {}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return &trackingWriteCloser{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := launcher.Start(ctx)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Fatalf("Start() elapsed = %s, want < 700ms", elapsed)
	}
	if got := starter.process.stopCalls.Load(); got != 1 {
		t.Fatalf("spawn cleanup calls = %d, want 1", got)
	}
}

func TestLauncherStartReportsSpawnCleanupFailure(t *testing.T) {
	repoDir := t.TempDir()
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	starter := useRecordingDaemonStarter(launcher)
	starter.process.stopErr = errors.New("cleanup denied")
	launcher.BinPath = "azd-test"
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }

	err := launcher.Start(context.Background())
	if err == nil || !errors.Is(err, starter.process.stopErr) || !strings.Contains(err.Error(), "cleanup spawned daemon") {
		t.Fatalf("Start() error = %v, want readiness and cleanup failure", err)
	}
	if got := starter.process.stopCalls.Load(); got != 1 {
		t.Fatalf("spawn cleanup calls = %d, want 1", got)
	}
}

func TestLauncherStartProcessSupervisorPreservesExitPublishedAfterInitialProbe(t *testing.T) {
	done := make(chan error)
	process := &execDaemonProcess{
		cmd:  &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done: done,
		signalProcessGroup: func(signal syscall.Signal) error {
			if signal != syscall.SIGTERM {
				t.Fatalf("signal = %v, want SIGTERM", signal)
			}
			go func() { done <- errors.New("exit status 7") }()
			return syscall.ESRCH
		},
	}

	err := process.stopAndWait(context.Background())
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("stopAndWait() error = %v, want observable pre-readiness exit status", err)
	}
}

func TestLauncherStartProcessSupervisorPreservesExitAfterSignalPermissionRace(t *testing.T) {
	done := make(chan error)
	process := &execDaemonProcess{
		cmd:  &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done: done,
		signalProcessGroup: func(signal syscall.Signal) error {
			if signal != syscall.SIGTERM {
				t.Fatalf("signal = %v, want SIGTERM", signal)
			}
			go func() { done <- errors.New("exit status 7") }()
			return syscall.EPERM
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := process.stopAndWait(ctx)
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("stopAndWait() error = %v, want observable pre-readiness exit status", err)
	}
}

func TestRealProcessProfileLauncherReportsExitBeforeReadiness(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "azd-test")
	script := "#!/bin/sh\nexit 7\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := NewLauncher(filepath.Join(root, "repo"), filepath.Join(root, "daemon.sock"))
	launcher.BinPath = executable
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}

	err := launcher.Start(context.Background())
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("Start() error = %v, want observable pre-readiness exit status", err)
	}
}

func TestRealProcessProfileLauncherReadinessFailureCleansExactLaunch(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(root, "pid")
	argsPath := filepath.Join(root, "args")
	readyPath := filepath.Join(root, "ready")
	executable := filepath.Join(root, "azd-test")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" > %q\nprintf '%%s\\n' \"$@\" > %q\n: > %q\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n", pidPath, argsPath, readyPath)
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := NewLauncher(repoDir, filepath.Join(root, "daemon.sock"))
	launcher.BinPath = executable
	readyCalls := 0
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		readyCalls++
		if readyCalls < 3 {
			return context.DeadlineExceeded
		}
		for {
			if _, err := os.Stat(readyPath); err == nil {
				return context.DeadlineExceeded
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}

	err := launcher.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "spawned daemon cleaned up") {
		t.Fatalf("Start() error = %v, want observable spawned-process cleanup", err)
	}
	pidText, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatalf("read spawned pid: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if parseErr != nil {
		t.Fatalf("parse spawned pid %q: %v", pidText, parseErr)
	}
	if signalErr := syscall.Kill(pid, 0); !errors.Is(signalErr, syscall.ESRCH) {
		t.Fatalf("spawned daemon pid %d still exists after readiness failure: %v", pid, signalErr)
	}
	argsText, readErr := os.ReadFile(argsPath)
	if readErr != nil {
		t.Fatalf("read spawned args: %v", readErr)
	}
	wantArgs := strings.Join([]string{"--repo", repoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath}, "\n") + "\n"
	if string(argsText) != wantArgs {
		t.Fatalf("spawned args = %q, want exact isolated launch %q", argsText, wantArgs)
	}
}

func TestLauncherStart_SocketReadySkipsSpawnWithoutLock(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.waitForReady = func(context.Context, string) error { return nil }

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil when socket is already ready", err)
	}
}

func TestLauncherStart_SocketReadyWhileWaitingForStartLockReturnsNil(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")

	startLockPath := launcher.LockPath + ".start"
	if err := os.MkdirAll(filepath.Dir(startLockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(start lock dir): %v", err)
	}
	lockFile, err := os.OpenFile(startLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(start lock): %v", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		t.Fatalf("Flock(start lock): %v", err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls >= 2 {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.sleepFn = func(time.Duration) {}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	if err := launcher.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v, want nil when socket becomes ready under lock contention", err)
	}
	if readyCalls < 2 {
		t.Fatalf("waitForReady call count = %d, want >= 2", readyCalls)
	}
}

func TestLauncherStopUsesTerminateLockOwner(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("socket unavailable") }

	called := false
	var gotLockPath string
	launcher.terminateLockOwner = func(lockPath string) error {
		called = true
		gotLockPath = lockPath
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !called {
		t.Fatal("expected terminateLockOwner to be called")
	}
	if gotLockPath != launcher.LockPath {
		t.Fatalf("lock path = %q, want %q", gotLockPath, launcher.LockPath)
	}
}

func TestLauncherStopWrapsTerminateError(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("socket unavailable") }
	launcher.terminateLockOwner = func(string) error { return errors.New("boom") }

	err := launcher.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "terminate daemon lock owner: boom") {
		t.Fatalf("Stop() error = %v, want wrapped terminate error", err)
	}
}

func TestLauncherStopUsesGracefulSocketShutdownWhenAvailable(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	socketShutdownCalls := 0
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketShutdownCalls++
		return nil
	}
	terminateCalled := false
	launcher.terminateLockOwner = func(string) error {
		terminateCalled = true
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if socketShutdownCalls != 1 {
		t.Fatalf("socket shutdown calls = %d, want 1", socketShutdownCalls)
	}
	if terminateCalled {
		t.Fatal("terminateLockOwner should not be called when graceful socket shutdown succeeds")
	}
}

func TestLauncherStopAttributesGracefulShutdownReason(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	var gotReason string
	launcher.shutdownWithReason = func(_ context.Context, _ string, reason string) error {
		gotReason = reason
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if gotReason != "stop" {
		t.Fatalf("shutdown reason = %q, want stop", gotReason)
	}
}

func TestLauncherStopFallsBackWhenGracefulSocketShutdownFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("rpc failed") }
	terminateCalled := false
	launcher.terminateLockOwner = func(string) error {
		terminateCalled = true
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !terminateCalled {
		t.Fatal("expected terminateLockOwner fallback when graceful socket shutdown fails")
	}
}
