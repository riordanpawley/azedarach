package daemonprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	"github.com/riordanpawley/azedarach/internal/ipc/transport"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/naming"
)

// Launcher starts/replaces the singleton daemon process for a user-global socket.
type Launcher struct {
	RepoDir            string
	SocketPath         string
	LockPath           string
	BinPath            string
	Logger             *slog.Logger
	openLogFile        func(path string) (io.WriteCloser, error)
	waitForReady       func(ctx context.Context, socketPath string) error
	shutdownViaSocket  func(ctx context.Context, socketPath string) error
	shutdownWithReason func(ctx context.Context, socketPath string, reason string) error
	replaceReason      string
	sleepFn            func(time.Duration)
	terminateLockOwner func(lockPath string) error
	startProcess       daemonProcessStarter
}

type daemonCommand struct {
	executable string
	args       []string
	dir        string
	env        []string
}

type daemonProcessSpec struct {
	command daemonCommand
	args    []string
	stdout  io.Writer
	stderr  io.Writer
}

type daemonProcess interface {
	exited() <-chan error
	stopAndWait(context.Context) error
}

type daemonProcessStarter func(daemonProcessSpec) (daemonProcess, error)

type execDaemonProcess struct {
	cmd                *exec.Cmd
	done               chan error
	signalProcessGroup func(syscall.Signal) error
}

var errSpawnedDaemonExited = errors.New("spawned daemon process exited before readiness")
var errPairedDaemonUnavailable = errors.New("paired daemon executable unavailable")

var currentExecutable = os.Executable

func startExecDaemonProcess(spec daemonProcessSpec) (daemonProcess, error) {
	cmd := exec.Command(spec.command.executable, spec.args...)
	if strings.TrimSpace(spec.command.dir) != "" {
		cmd.Dir = spec.command.dir
	}
	if spec.command.env != nil {
		cmd.Env = spec.command.env
	}
	cmd.Stdout = spec.stdout
	cmd.Stderr = spec.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &execDaemonProcess{
		cmd:  cmd,
		done: make(chan error, 1),
		signalProcessGroup: func(signal syscall.Signal) error {
			return syscall.Kill(-cmd.Process.Pid, signal)
		},
	}
	go func() { process.done <- cmd.Wait() }()
	return process, nil
}

func spawnedDaemonExitError(waitErr error) error {
	if waitErr == nil {
		return errSpawnedDaemonExited
	}
	return fmt.Errorf("%w: %v", errSpawnedDaemonExited, waitErr)
}

func stoppedByCleanupSignal(waitErr error, signalDelivered bool) bool {
	if !signalDelivered {
		return false
	}
	if waitErr == nil {
		// The daemon may trap SIGTERM and exit cleanly.
		return true
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGTERM
}

func (p *execDaemonProcess) signal(signal syscall.Signal) error {
	if p.signalProcessGroup != nil {
		return p.signalProcessGroup(signal)
	}
	return syscall.Kill(-p.cmd.Process.Pid, signal)
}

func (p *execDaemonProcess) exited() <-chan error {
	return p.done
}

func (p *execDaemonProcess) stopAndWait(ctx context.Context) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	select {
	case waitErr := <-p.done:
		return spawnedDaemonExitError(waitErr)
	default:
	}
	pid := p.cmd.Process.Pid
	termErr := p.signal(syscall.SIGTERM)
	if termErr != nil && !errors.Is(termErr, syscall.ESRCH) {
		// A short-lived child can exit between the initial done probe and the
		// process-group signal. On some platforms that race is reported as EPERM
		// rather than ESRCH, while cmd.Wait is still publishing the exit status.
		// Prefer that authoritative status when the caller supplied the bounded
		// cleanup context used by Launcher.Start.
		if _, bounded := ctx.Deadline(); bounded {
			select {
			case waitErr := <-p.done:
				return spawnedDaemonExitError(waitErr)
			case <-ctx.Done():
				return fmt.Errorf("terminate spawned daemon process group %d: %w", pid, termErr)
			}
		}
		return fmt.Errorf("terminate spawned daemon process group %d: %w", pid, termErr)
	}
	select {
	case waitErr := <-p.done:
		if stoppedByCleanupSignal(waitErr, termErr == nil) {
			return nil
		}
		return spawnedDaemonExitError(waitErr)
	case <-ctx.Done():
		if err := p.signal(syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("force-kill spawned daemon process group %d after cleanup timeout: %w", pid, err)
		}
		select {
		case <-p.done:
			return fmt.Errorf("wait for spawned daemon process cleanup: %w", ctx.Err())
		case <-time.After(time.Second):
			return fmt.Errorf("spawned daemon process group %d did not reap after force-kill: %w", pid, ctx.Err())
		}
	}
}

func (c daemonCommand) displayName() string {
	if len(c.args) == 0 {
		return c.executable
	}
	return c.executable + " " + strings.Join(c.args, " ")
}

// NewLauncher returns a daemon process launcher for repoDir.
func NewLauncher(repoDir, socketPath string) *Launcher {
	if config.UseScopedDaemonRuntimeFor(repoDir) || socketPath == config.ScopedDaemonSocketPath(repoDir) {
		if normalizedRepoDir, err := config.ResolveWorktreeRoot(repoDir); err == nil {
			repoDir = normalizedRepoDir
		}
	} else {
		if normalizedRepoDir, err := config.ResolveProjectRoot(repoDir); err == nil {
			repoDir = normalizedRepoDir
		}
	}
	lockPath := config.GlobalDaemonLockPath()
	if strings.TrimSpace(socketPath) != "" {
		lockPath = filepath.Join(filepath.Dir(socketPath), "daemon.lock")
	}
	return &Launcher{
		RepoDir:            repoDir,
		SocketPath:         socketPath,
		LockPath:           lockPath,
		Logger:             slog.Default(),
		openLogFile:        openDaemonLog,
		waitForReady:       waitForDaemonReady,
		shutdownWithReason: gracefulShutdownViaSocketWithReason,
		replaceReason:      "compatibility-replace",
		sleepFn:            time.Sleep,
		terminateLockOwner: lifecycle.TerminateLockOwner,
	}
}

// WithLogger overrides launcher logging sink for lifecycle warnings.
func (l *Launcher) WithLogger(logger *slog.Logger) *Launcher {
	if l == nil || logger == nil {
		return l
	}
	l.Logger = logger
	return l
}

// WithReplaceReason overrides the graceful shutdown reason used by Replace.
func (l *Launcher) WithReplaceReason(reason string) *Launcher {
	if l == nil || strings.TrimSpace(reason) == "" {
		return l
	}
	l.replaceReason = strings.TrimSpace(reason)
	return l
}

// Start spawns daemon process in background.
func (l *Launcher) Start(ctx context.Context) error {
	// Socket readiness is authoritative for service availability. Lock state is
	// advisory and used only to coordinate spawn/recovery.
	if err := l.waitForSocketReadyWithin(250 * time.Millisecond); err == nil {
		return nil
	}

	daemonCmd, err := l.resolveCommand()
	if err != nil {
		return err
	}
	releaseStartLock, lockAcquired, err := l.acquireStartLock(ctx)
	if err != nil {
		if (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) && l.waitForSocketReadyWithin(300*time.Millisecond) == nil {
			return nil
		}
		return err
	}
	if !lockAcquired {
		return nil
	}
	defer releaseStartLock()

	// Re-check after acquiring start lock to avoid duplicate spawns from racing
	// clients.
	if err := l.waitForSocketReadyWithin(500 * time.Millisecond); err == nil {
		return nil
	}

	if l.daemonLockOwnerAlive() {
		if l.waitForReady != nil {
			err := l.waitForSocketReadyWithin(2 * time.Second)
			if err == nil {
				return nil
			}
			if l.Logger != nil {
				l.Logger.Warn("daemon lock owner alive but socket is not ready; attempting fresh spawn",
					"lock_path", l.LockPath,
					"socket_path", l.SocketPath,
					"error", err,
				)
			}
			terminate := l.terminateLockOwner
			if terminate == nil {
				terminate = lifecycle.TerminateLockOwner
			}
			if err := terminate(l.LockPath); err != nil {
				if !isRecoverableLockOwnerTerminationError(err) {
					readyErr := l.waitForSocketReadyWithin(1 * time.Second)
					if readyErr == nil {
						return nil
					}
					return fmt.Errorf("recover stale daemon lock owner: %w", err)
				}
				if l.Logger != nil {
					l.Logger.Warn("lock owner termination did not complete cleanly; force-clearing stale daemon lock",
						"lock_path", l.LockPath,
						"error", err,
					)
				}
				if rmErr := os.Remove(l.LockPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
					return fmt.Errorf("recover stale daemon lock owner after forced lock clear: %w", rmErr)
				}
			}
		}
	}

	openLogFile := l.openLogFile
	if openLogFile == nil {
		openLogFile = openDaemonLog
	}
	cfg, _ := config.LoadConfig(l.RepoDir)
	logFile, err := openLogFile(filepath.Join(config.SessionLogDirFor(cfg, l.RepoDir), logging.DaemonLogFileName))
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() {
		_ = logFile.Close()
	}()
	// Do not bind daemon lifetime to the caller context. Attach contexts are short-lived.
	args := append([]string{}, daemonCmd.args...)
	args = append(args, "--repo", l.RepoDir, "--socket", l.SocketPath, "--lock", l.LockPath)
	launchCtx, endSpan := latencytrace.StartSpan(ctx, "dependency", "daemon_process",
		"dependency.name", filepath.Base(daemonCmd.executable),
		"dependency.operation", "start",
		"arg_count", len(args),
	)
	var spanErr error
	defer func() { endSpan(spanErr) }()
	startProcess := l.startProcess
	if startProcess == nil {
		startProcess = startExecDaemonProcess
	}
	process, err := startProcess(daemonProcessSpec{
		command: daemonCmd,
		args:    args,
		stdout:  logFile,
		stderr:  logFile,
	})
	if err != nil {
		spanErr = err
		return fmt.Errorf("start daemon %s: %w", daemonCmd.displayName(), err)
	}
	if process == nil {
		spanErr = errors.New("daemon process starter returned nil process")
		return fmt.Errorf("start daemon %s: %w", daemonCmd.displayName(), spanErr)
	}
	if l.waitForReady != nil {
		readyCtx, cancel := context.WithTimeout(launchCtx, 15*time.Second)
		readyResult := make(chan error, 1)
		go func() {
			readyResult <- l.waitForReady(readyCtx, l.SocketPath)
		}()
		select {
		case waitErr := <-process.exited():
			cancel()
			spanErr = spawnedDaemonExitError(waitErr)
			return fmt.Errorf("wait for daemon socket readiness: %w", spanErr)
		case err = <-readyResult:
			cancel()
		}
		if err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
			cleanupErr := process.stopAndWait(cleanupCtx)
			cleanupCancel()
			if cleanupErr != nil {
				spanErr = errors.Join(err, cleanupErr)
				return fmt.Errorf("wait for daemon socket readiness and cleanup spawned daemon: %w", spanErr)
			}
			spanErr = err
			return fmt.Errorf("wait for daemon socket readiness: %w (spawned daemon cleaned up)", err)
		}
	}
	return nil
}

func (l *Launcher) waitForSocketReadyWithin(timeout time.Duration) error {
	if l.waitForReady == nil {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return l.waitForReady(readyCtx, l.SocketPath)
}

func isRecoverableLockOwnerTerminationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, lifecycle.ErrLockOwnerPermissionDenied) ||
		errors.Is(err, lifecycle.ErrLockOwnerTerminationTimeout) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EACCES) ||
		errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "lock owner permission denied") ||
		strings.Contains(msg, "operation not permitted") ||
		strings.Contains(msg, "permission denied")
}

// Stop attempts to stop existing lock-owner process.
func (l *Launcher) Stop(ctx context.Context) error {
	stopped := false
	if strings.TrimSpace(l.SocketPath) != "" {
		if err := l.requestGracefulShutdown(ctx, "stop"); err == nil {
			stopped = true
		} else if l.Logger != nil {
			l.Logger.Warn("graceful daemon socket shutdown failed; falling back to lock-owner termination",
				"socket_path", l.SocketPath,
				"lock_path", l.LockPath,
				"error", err,
			)
		}
	}

	if !stopped {
		terminate := l.terminateLockOwner
		if terminate == nil {
			terminate = lifecycle.TerminateLockOwner
		}
		if err := terminate(l.LockPath); err != nil {
			return fmt.Errorf("terminate daemon lock owner: %w", err)
		}
	}
	if err := l.cleanupScopedRuntimeAssets(); err != nil {
		return fmt.Errorf("clean worktree-scoped daemon runtime: %w", err)
	}
	return nil
}

func (l *Launcher) cleanupScopedRuntimeAssets() error {
	if l == nil || !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return nil
	}
	runtimeDir := config.ScopedDaemonRuntimeDir(l.RepoDir)
	wantSocket := config.ScopedDaemonSocketPath(l.RepoDir)
	wantLock := config.ScopedDaemonLockPath(l.RepoDir)
	if filepath.Clean(l.SocketPath) != filepath.Clean(wantSocket) || filepath.Clean(l.LockPath) != filepath.Clean(wantLock) {
		// Tests and specialized callers may supply a private socket while the
		// process environment is scoped. Only canonical worktree runtime assets
		// are eligible for automatic removal.
		return nil
	}
	if info, err := os.Lstat(runtimeDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect scoped daemon runtime: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("scoped daemon runtime is not a directory: %s", runtimeDir)
	}

	startLockPath := wantLock + ".start"
	startLock, err := os.OpenFile(startLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open scoped daemon cleanup lock: %w", err)
	}
	defer startLock.Close()
	if err := syscall.Flock(int(startLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("acquire scoped daemon cleanup lock: %w", err)
	}
	defer syscall.Flock(int(startLock.Fd()), syscall.LOCK_UN) //nolint:errcheck // best-effort unlock during teardown

	if _, err := os.Lstat(wantSocket); err == nil {
		return fmt.Errorf("daemon socket still exists after stop: %s", wantSocket)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect stopped daemon socket: %w", err)
	}
	if err := l.waitForScopedLockOwnerExit(2 * time.Second); err != nil {
		return err
	}
	if err := os.Remove(wantLock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove scoped daemon asset %s: %w", wantLock, err)
	}
	sessionLaunchDir := filepath.Join(runtimeDir, "session-launch")
	if err := os.Remove(sessionLaunchDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove scoped daemon runtime directory %s: %w", sessionLaunchDir, err)
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect scoped daemon runtime residue: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(startLockPath) {
			return fmt.Errorf("unexpected scoped daemon runtime residue: %s", filepath.Join(runtimeDir, entry.Name()))
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	// Rename the entire stopped runtime generation while holding its start
	// lock. A concurrent launcher can then safely recreate the canonical path;
	// cleanup below touches only the detached generation and cannot remove new
	// socket or lock assets.
	tombstone := fmt.Sprintf("%s.cleanup-%d-%d", runtimeDir, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(runtimeDir, tombstone); err != nil {
		return fmt.Errorf("detach stopped scoped daemon runtime: %w", err)
	}
	if err := syscall.Flock(int(startLock.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock detached scoped daemon runtime: %w", err)
	}
	if err := startLock.Close(); err != nil {
		return fmt.Errorf("close detached scoped daemon cleanup lock: %w", err)
	}
	startLock = nil
	if err := os.Remove(filepath.Join(tombstone, filepath.Base(startLockPath))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove detached scoped daemon start lock: %w", err)
	}
	if err := os.Remove(tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove detached scoped daemon runtime: %w", err)
	}
	return nil
}

func (l *Launcher) waitForScopedLockOwnerExit(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pid, ok := l.readLockedPID()
		if !ok || !processAlive(pid) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon lock owner %d still alive after stop", pid)
		}
		sleep := l.sleepFn
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(20 * time.Millisecond)
	}
}

// Replace attempts to stop an existing daemon process, then starts daemon.
func (l *Launcher) Replace(ctx context.Context) error {
	if strings.TrimSpace(l.SocketPath) != "" {
		reason := strings.TrimSpace(l.replaceReason)
		if reason == "" {
			reason = "compatibility-replace"
		}
		if err := l.requestGracefulShutdown(ctx, reason); err == nil {
			if err := l.waitForSocketUnavailable(ctx, 2*time.Second); err != nil {
				return fmt.Errorf("wait for daemon socket shutdown before replace: %w", err)
			}
			return l.Start(ctx)
		} else if l.Logger != nil {
			l.Logger.Warn("graceful daemon socket shutdown failed before replace; falling back to lock-owner termination",
				"socket_path", l.SocketPath,
				"lock_path", l.LockPath,
				"error", err,
			)
		}
	}

	terminate := l.terminateLockOwner
	if terminate == nil {
		terminate = lifecycle.TerminateLockOwner
	}
	if err := terminate(l.LockPath); err != nil {
		return fmt.Errorf("terminate daemon lock owner before replace: %w", err)
	}
	return l.Start(ctx)
}

func (l *Launcher) requestGracefulShutdown(ctx context.Context, reason string) error {
	if l.shutdownViaSocket != nil {
		return l.shutdownViaSocket(ctx, l.SocketPath)
	}
	shutdown := l.shutdownWithReason
	if shutdown == nil {
		shutdown = gracefulShutdownViaSocketWithReason
	}
	return shutdown(ctx, l.SocketPath, reason)
}

func (l *Launcher) waitForSocketUnavailable(ctx context.Context, timeout time.Duration) error {
	if l.waitForReady == nil {
		return nil
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		readyCtx, readyCancel := context.WithTimeout(deadlineCtx, 100*time.Millisecond)
		err := l.waitForReady(readyCtx, l.SocketPath)
		readyCancel()
		if err != nil {
			return nil
		}
		if deadlineCtx.Err() != nil {
			return deadlineCtx.Err()
		}
		sleep := l.sleepFn
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(50 * time.Millisecond)
	}
}

func (l *Launcher) resolveCommand() (daemonCommand, error) {
	if l.BinPath != "" {
		return l.commandForExecutable(l.BinPath), nil
	}
	if env := os.Getenv("AZEDARACH_DAEMON_BIN"); env != "" {
		return l.commandForExecutable(env), nil
	}
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		if paired := daemonBinaryNearCurrentExecutable(); paired != "" {
			return l.commandForExecutable(paired), nil
		}
		return daemonCommand{}, fmt.Errorf("%w: global daemon launch requires the running az to resolve under .azedarach-generations/generation.* with an executable sibling azd; reinstall the managed az/azd pair or set AZEDARACH_DAEMON_BIN explicitly", errPairedDaemonUnavailable)
	}
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		// Prefer the caller's worktree-local build when present so daemon behavior
		// matches the code under test/development.
		candidates = append(candidates, filepath.Join(cwd, "bin", "azd"))
	}
	candidates = append(candidates,
		filepath.Join(l.RepoDir, "bin", "azd"),
		// Support monorepo root launcher repo dir with go-bubbletea-local binaries.
		filepath.Join(l.RepoDir, "go-bubbletea", "bin", "azd"),
	)
	for _, candidate := range candidates {
		if executableFile(candidate) {
			return daemonCommand{executable: candidate}, nil
		}
	}
	if sourceDir := l.localScopedDaemonSourceDir(); sourceDir != "" {
		return daemonCommand{executable: "go", args: []string{"run", "./cmd/azd"}, dir: sourceDir}, nil
	}
	return daemonCommand{executable: "azd"}, nil
}

func (l *Launcher) commandForExecutable(executable string) daemonCommand {
	command := daemonCommand{executable: executable}
	if config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return command
	}
	if generationDir, ok := config.ManagedGenerationBinDir(executable, "azd"); ok {
		command.env = environmentWithPathPrefix(os.Environ(), generationDir)
	}
	return command
}

func environmentWithPathPrefix(environment []string, prefix string) []string {
	out := make([]string, 0, len(environment)+1)
	pathValue := ""
	for _, entry := range environment {
		if strings.HasPrefix(entry, "PATH=") {
			pathValue = strings.TrimPrefix(entry, "PATH=")
			continue
		}
		out = append(out, entry)
	}
	return append(out, "PATH="+config.PrependPathEntry(pathValue, prefix))
}

func daemonBinaryNearCurrentExecutable() string {
	executable, err := currentExecutable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return ""
	}
	generationDir, ok := config.ManagedGenerationBinDir(executable, "az")
	if !ok {
		return ""
	}
	candidate := filepath.Join(generationDir, "azd")
	if executableFile(candidate) {
		return candidate
	}
	return ""
}

func executableFile(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.Mode().IsRegular() && stat.Mode().Perm()&0o111 != 0
}

func (l *Launcher) resolveBinary() string {
	command, _ := l.resolveCommand()
	return command.executable
}

func (l *Launcher) localScopedDaemonSourceDir() string {
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return ""
	}
	sourceDirs := []string{
		l.RepoDir,
		filepath.Join(l.RepoDir, "go-bubbletea"),
	}
	for _, sourceDir := range sourceDirs {
		cmdDir := filepath.Join(sourceDir, "cmd", "azd")
		if stat, err := os.Stat(cmdDir); err == nil && stat.IsDir() {
			return sourceDir
		}
	}
	return ""
}

func (l *Launcher) readLockedPID() (int, bool) {
	b, err := os.ReadFile(l.LockPath)
	if err != nil {
		return 0, false
	}
	content := strings.TrimSpace(string(b))
	if content == "" {
		return 0, false
	}
	if strings.HasPrefix(content, "{") {
		var v struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(b, &v); err == nil && v.PID > 0 {
			return v.PID, true
		}
	}
	pid, err := strconv.Atoi(content)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func openDaemonLog(path string) (io.WriteCloser, error) {
	logFile, err := logging.OpenRotatingFile(path, logging.DefaultMaxLogBytes, logging.DefaultLogBackups)
	if err != nil {
		return nil, err
	}
	if err := logFile.Close(); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

func (l *Launcher) acquireStartLock(ctx context.Context) (func(), bool, error) {
	startLockPath := l.LockPath + ".start"
	if err := os.MkdirAll(filepath.Dir(startLockPath), 0o755); err != nil {
		return nil, false, fmt.Errorf("create start lock dir: %w", err)
	}
	f, err := os.OpenFile(startLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open start lock: %w", err)
	}
	for {
		if lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, true, nil
		} else if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			_ = f.Close()
			return nil, false, fmt.Errorf("acquire start lock: %w", lockErr)
		}
		// Another client currently owns startup. If daemon socket becomes ready
		// while we are queued on the lock, return early rather than timing out
		// waiting for lock ownership we no longer need.
		if err := l.waitForSocketReadyWithin(100 * time.Millisecond); err == nil {
			_ = f.Close()
			return func() {}, false, nil
		}

		if ctx.Err() != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("acquire start lock: %w", ctx.Err())
		}
		sleep := l.sleepFn
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(50 * time.Millisecond)
	}
}

func (l *Launcher) daemonLockOwnerAlive() bool {
	pid, ok := l.readLockedPID()
	if !ok {
		return false
	}
	return processAlive(pid)
}

func waitForDaemonReady(ctx context.Context, socketPath string) error {
	waitCtx, endSpan := latencytrace.StartSpan(ctx, "dependency", "daemon_process.wait_ready")
	var spanErr error
	defer func() { endSpan(spanErr) }()
	if strings.TrimSpace(socketPath) == "" {
		return nil
	}
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	for {
		if waitCtx.Err() != nil {
			spanErr = waitCtx.Err()
			return waitCtx.Err()
		}
		conn, err := dialer.DialContext(waitCtx, "unix", socketPath)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func gracefulShutdownViaSocket(ctx context.Context, socketPath string) error {
	return gracefulShutdownViaSocketWithReason(ctx, socketPath, "unknown")
}

func gracefulShutdownViaSocketWithReason(ctx context.Context, socketPath string, reason string) error {
	shutdownCtx := ctx
	if shutdownCtx == nil {
		shutdownCtx = context.Background()
	}
	if _, hasDeadline := shutdownCtx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(shutdownCtx, 2*time.Second)
		defer cancel()
	}

	client := transport.NewClient(socketPath).WithTimeout(1 * time.Second)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unknown"
	}
	body, err := json.Marshal(protocol.DaemonShutdownCommandBody{Reason: reason})
	if err != nil {
		return fmt.Errorf("encode daemon shutdown command: %w", err)
	}
	resp, err := client.Command(shutdownCtx, protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       naming.RequestID(fmt.Sprintf("daemon-%s-%d", reason, time.Now().UnixNano())),
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandDaemonShutdown,
		SentAt:          time.Now().UTC(),
		Body:            body,
	})
	if err != nil {
		return fmt.Errorf("daemon shutdown command: %w", err)
	}
	if !resp.OK {
		if resp.Error != nil {
			return fmt.Errorf("daemon shutdown rejected: %s", resp.Error.Message)
		}
		return errors.New("daemon shutdown rejected")
	}
	if err := waitForSocketGone(shutdownCtx, socketPath); err != nil {
		return fmt.Errorf("wait for daemon socket shutdown: %w", err)
	}
	return nil
}

func waitForSocketGone(ctx context.Context, socketPath string) error {
	waitCtx, endSpan := latencytrace.StartSpan(ctx, "dependency", "daemon_process.wait_socket_gone")
	var spanErr error
	defer func() { endSpan(spanErr) }()
	for {
		if strings.TrimSpace(socketPath) == "" {
			return nil
		}
		if _, err := os.Stat(socketPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			spanErr = err
			return err
		}
		if waitCtx != nil {
			if err := waitCtx.Err(); err != nil {
				spanErr = err
				return err
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
}
