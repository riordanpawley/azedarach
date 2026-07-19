package daemonprocess

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	RepoDir                      string
	SocketPath                   string
	LockPath                     string
	BinPath                      string
	Logger                       *slog.Logger
	openLogFile                  func(path string) (io.WriteCloser, error)
	waitForReady                 func(ctx context.Context, socketPath string) error
	shutdownViaSocket            func(ctx context.Context, socketPath string) error
	shutdownWithReason           func(ctx context.Context, socketPath string, reason string) error
	replaceReason                string
	sleepFn                      func(time.Duration)
	processExitTimeout           time.Duration
	gracefulExitTimeout          time.Duration
	termExitTimeout              time.Duration
	killExitTimeout              time.Duration
	terminateLockOwner           func(lockPath string) error
	openProcessSignalHandle      func(int) (processSignalHandle, error)
	waitForOwnerExit             func(context.Context, processIdentity) error
	beforePredecessorSignal      func(syscall.Signal)
	afterPredecessorVerification func()
	replacementSuccessorVerifier func(daemonCommand) error
	startProcess                 daemonProcessStarter
	beforeReplaceLock            func()
	preflightReplace             func(context.Context, daemonCommand) error
	preflightRollback            func(context.Context, daemonCommand) error
	captureOwner                 func() (processIdentity, bool, error)
	verifyDaemonArguments        func(processIdentity, string) error
}

type daemonCommand struct {
	executable              string
	args                    []string
	dir                     string
	env                     []string
	sourceFallback          bool
	candidateStage          string
	rollbackStage           string
	retiredPredecessorStage string
}

type boundedOutput struct {
	bytes.Buffer
	remaining int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	written := len(p)
	if b.remaining <= 0 {
		return written, nil
	}
	keep := min(len(p), b.remaining)
	_, _ = b.Buffer.Write(p[:keep])
	b.remaining -= keep
	return written, nil
}

type daemonProcessSpec struct {
	command                  daemonCommand
	args                     []string
	stdout                   io.Writer
	stderr                   io.Writer
	requireGroupCleanupProof bool
}

type daemonProcess interface {
	exited() <-chan error
	stopAndWait(context.Context) error
}

type daemonProcessStarter func(daemonProcessSpec) (daemonProcess, error)

type execDaemonProcess struct {
	cmd                *exec.Cmd
	done               chan error
	waitDone           chan struct{}
	waitResult         chan error
	reapAllowed        chan struct{}
	reapOnce           sync.Once
	waitMu             sync.Mutex
	waitErr            error
	signalProcessGroup func(syscall.Signal) error
	processGroupAlive  func() (bool, error)
}

var errSpawnedDaemonExited = errors.New("spawned daemon process exited before readiness")
var errPairedDaemonUnavailable = errors.New("paired daemon executable unavailable")
var errReplacementCandidateCleanupUnproven = errors.New("exact rejected replacement candidate cleanup was not proven")
var errProcessExitObservationUnsupported = errors.New("kernel-bound non-reaping process exit observation is unsupported")

var currentExecutable = os.Executable

func startExecDaemonProcess(spec daemonProcessSpec) (daemonProcess, error) {
	return startExecDaemonProcessWithObserver(spec, observePlatformProcessExit)
}

func startExecDaemonProcessWithObserver(spec daemonProcessSpec, observe func(int) (<-chan error, error)) (daemonProcess, error) {
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
	exitObserved, err := observe(cmd.Process.Pid)
	if err != nil {
		if errors.Is(err, errProcessExitObservationUnsupported) && !spec.requireGroupCleanupProof {
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
		// The leader has not been reaped, so its PID still reserves the process
		// group ID and makes this numeric group signal safe. Fail the launch
		// closed rather than running without an exit-observation fence.
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr := cmd.Wait()
		return nil, fmt.Errorf("establish kernel-bound daemon exit observation: %w (cleanup: %v)", err, errors.Join(killErr, waitErr))
	}
	process := &execDaemonProcess{
		cmd:         cmd,
		done:        make(chan error, 1),
		waitDone:    make(chan struct{}),
		waitResult:  make(chan error, 1),
		reapAllowed: make(chan struct{}),
		signalProcessGroup: func(signal syscall.Signal) error {
			return syscall.Kill(-cmd.Process.Pid, signal)
		},
	}
	go func() {
		observationErr := <-exitObserved
		process.done <- observationErr
		<-process.reapAllowed
		waitErr := cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = waitErr
		process.waitMu.Unlock()
		process.waitResult <- waitErr
		close(process.waitDone)
	}()
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

func (p *execDaemonProcess) releaseCleanupAuthority() {
	if p == nil || p.reapAllowed == nil {
		return
	}
	p.reapOnce.Do(func() { close(p.reapAllowed) })
}

func (p *execDaemonProcess) reapedExitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func releaseDaemonProcessCleanupAuthority(process daemonProcess) {
	if retained, ok := process.(interface{ releaseCleanupAuthority() }); ok {
		retained.releaseCleanupAuthority()
	}
}

func refineSpawnedDaemonExitError(err error, process daemonProcess) error {
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) {
		return err
	}
	reaped, ok := process.(interface{ reapedExitError() error })
	if !ok {
		return err
	}
	if waitErr := reaped.reapedExitError(); waitErr != nil {
		return fmt.Errorf("%w (process wait: %v)", err, waitErr)
	}
	return err
}

func (p *execDaemonProcess) stopAndWait(ctx context.Context) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if p.waitDone != nil {
		select {
		case <-p.waitDone:
			return errors.New("spawned daemon group leader was reaped before process-group cleanup; refusing numeric PGID signaling")
		default:
		}
		if p.reapAllowed != nil {
			return p.stopRetainedProcessGroupAndWait(ctx)
		}
		return p.stopProcessGroupAndWait(ctx)
	}
	return p.stopAndWaitLegacy(ctx)
}

func (p *execDaemonProcess) stopRetainedProcessGroupAndWait(ctx context.Context) error {
	pid := p.cmd.Process.Pid
	termErr := p.signal(syscall.SIGTERM)
	if termErr != nil && !errors.Is(termErr, syscall.ESRCH) && !errors.Is(termErr, syscall.EPERM) {
		return fmt.Errorf("terminate spawned daemon process group %d: %w", pid, termErr)
	}
	if errors.Is(termErr, syscall.ESRCH) || errors.Is(termErr, syscall.EPERM) {
		return p.finishRetainedProcessGroupCleanup(pid)
	}
	// The unreaped leader reserves the PGID while descendants receive their
	// graceful shutdown window. A group-level zero probe cannot report absence
	// during this interval because it intentionally includes that leader.
	<-ctx.Done()
	if err := p.signal(syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("force-kill retained spawned daemon process group %d after cleanup timeout: %w", pid, err)
	}
	return p.finishRetainedProcessGroupCleanup(pid)
}

func (p *execDaemonProcess) finishRetainedProcessGroupCleanup(pid int) error {
	p.releaseCleanupAuthority()
	forceCtx, forceCancel := context.WithTimeout(context.Background(), time.Second)
	defer forceCancel()
	if err := p.waitForLeaderReap(forceCtx); err != nil {
		return fmt.Errorf("reap retained spawned daemon group leader %d after cleanup signaling: %w", pid, err)
	}
	// Reaping releases the PGID reservation. From this point onward probes are
	// evidence only: a positive result may be a reused unrelated group and must
	// never authorize another signal.
	if err := p.waitForProcessGroupExit(forceCtx); err != nil {
		return fmt.Errorf("prove spawned daemon process group %d disappeared after retained cleanup: %w", pid, err)
	}
	return nil
}

func (p *execDaemonProcess) waitForLeaderReap(ctx context.Context) error {
	if p.waitResult == nil {
		select {
		case <-p.waitDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-p.waitResult:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *execDaemonProcess) stopAndWaitLegacy(ctx context.Context) error {
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
			// Wait completing for this exact exec.Cmd proves that the candidate
			// was reaped. The expired cleanup context only explains why SIGKILL
			// was required; it must not make proven cleanup appear unproven.
			return nil
		case <-time.After(time.Second):
			return fmt.Errorf("spawned daemon process group %d did not reap after force-kill: %w", pid, ctx.Err())
		}
	}
}

func (p *execDaemonProcess) stopProcessGroupAndWait(ctx context.Context) error {
	pid := p.cmd.Process.Pid
	termErr := p.signal(syscall.SIGTERM)
	if termErr != nil && !errors.Is(termErr, syscall.ESRCH) {
		return fmt.Errorf("terminate spawned daemon process group %d: %w", pid, termErr)
	}
	if err := p.waitForProcessGroupExit(ctx); err == nil {
		return nil
	} else if ctx.Err() == nil {
		return fmt.Errorf("prove spawned daemon process group %d cleanup after TERM: %w", pid, err)
	}
	if err := p.signal(syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force-kill spawned daemon process group %d after cleanup timeout: %w", pid, err)
	}
	forceCtx, forceCancel := context.WithTimeout(context.Background(), time.Second)
	defer forceCancel()
	if err := p.waitForProcessGroupExit(forceCtx); err != nil {
		return fmt.Errorf("spawned daemon process group %d did not exit after force-kill: %w", pid, err)
	}
	return nil
}

func (p *execDaemonProcess) waitForProcessGroupExit(ctx context.Context) error {
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	parentDone := (<-chan struct{})(p.waitDone)
	parentExited := false
	for {
		if !parentExited {
			select {
			case <-parentDone:
				parentExited = true
				parentDone = nil
			default:
			}
		}
		alive, err := p.spawnedProcessGroupAlive()
		if err != nil {
			return err
		}
		if parentExited && !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-parentDone:
			parentExited = true
			parentDone = nil
		case <-poll.C:
		}
	}
}

func (p *execDaemonProcess) spawnedProcessGroupAlive() (bool, error) {
	if p.processGroupAlive != nil {
		return p.processGroupAlive()
	}
	err := syscall.Kill(-p.cmd.Process.Pid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf("inspect spawned daemon process group %d: %w", p.cmd.Process.Pid, err)
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
		RepoDir:                 repoDir,
		SocketPath:              socketPath,
		LockPath:                lockPath,
		Logger:                  slog.Default(),
		openLogFile:             openDaemonLog,
		waitForReady:            waitForDaemonReady,
		shutdownWithReason:      gracefulShutdownViaSocketWithReason,
		replaceReason:           "compatibility-replace",
		sleepFn:                 time.Sleep,
		processExitTimeout:      10 * time.Second,
		gracefulExitTimeout:     2 * time.Second,
		termExitTimeout:         2 * time.Second,
		killExitTimeout:         2 * time.Second,
		terminateLockOwner:      lifecycle.TerminateLockOwner,
		openProcessSignalHandle: openPlatformProcessSignalHandle,
		preflightReplace:        preflightReplacementCommand,
		preflightRollback:       preflightPredecessorRollbackCommand,
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
	// advisory and used only to coordinate spawn/recovery. Canonical scoped
	// runtimes serialize the readiness check with Stop so a concurrent Start
	// cannot report the daemon ready while Stop is committed to removing it.
	if !l.ownsCanonicalRuntime() && l.waitForSocketReadyWithin(250*time.Millisecond) == nil {
		return nil
	}

	daemonCmd, err := l.resolveCommand()
	if err != nil {
		return err
	}
	releaseStartLock, lockAcquired, err := l.acquireStartLock(ctx)
	if err != nil {
		if !l.ownsCanonicalRuntime() &&
			(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) &&
			l.waitForSocketReadyWithin(300*time.Millisecond) == nil {
			return nil
		}
		return err
	}
	if !lockAcquired {
		return nil
	}
	defer releaseStartLock()
	return l.startWithLifecycleLock(ctx, daemonCmd)
}

// startWithLifecycleLock starts the daemon while the caller owns the exact
// runtime's lifecycle lock. Keeping lock acquisition outside this helper lets
// Replace serialize predecessor shutdown, exact exit proof, and successor
// startup without recursively acquiring the same flock.
func (l *Launcher) startWithLifecycleLock(ctx context.Context, daemonCmd daemonCommand) error {
	return l.startWithLifecycleLockMode(ctx, daemonCmd, true)
}

func (l *Launcher) startWithLifecycleLockMode(ctx context.Context, daemonCmd daemonCommand, allowUnreadyOwnerRecovery bool) error {
	process, err := l.startWithLifecycleLockModeRetained(ctx, daemonCmd, allowUnreadyOwnerRecovery, false)
	if err != nil && process != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		cleanupErr := process.stopAndWait(cleanupCtx)
		cleanupCancel()
		if cleanupErr != nil {
			err = fmt.Errorf("wait for daemon readiness and cleanup spawned daemon: %w", errors.Join(err, cleanupErr))
		} else {
			err = refineSpawnedDaemonExitError(err, process)
			err = fmt.Errorf("%w (spawned daemon cleaned up)", err)
		}
	}
	if err == nil {
		releaseDaemonProcessCleanupAuthority(process)
	}
	return err
}

// startWithLifecycleLockModeRetained returns the exact process handle only when
// this call spawned the daemon. Replacement keeps that handle until runtime
// identity verification finishes, so a rejected candidate can be cleaned up
// without addressing whichever process later owns the shared lock or socket.
func (l *Launcher) startWithLifecycleLockModeRetained(ctx context.Context, daemonCmd daemonCommand, allowUnreadyOwnerRecovery, requireGroupCleanupProof bool) (daemonProcess, error) {
	// Re-check after acquiring start lock to avoid duplicate spawns from racing
	// clients.
	if err := l.waitForSocketReadyWithin(500 * time.Millisecond); err == nil {
		return nil, nil
	}

	if l.daemonLockOwnerAlive() {
		if l.waitForReady != nil {
			err := l.waitForSocketReadyWithin(2 * time.Second)
			if err == nil {
				return nil, nil
			}
			if !allowUnreadyOwnerRecovery {
				return nil, errors.New("daemon lock owner appeared before replacement successor startup; refusing unverified recovery termination")
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
			owner, ownerPresent, captureErr := l.captureLockOwnerIdentity()
			if captureErr != nil {
				return nil, fmt.Errorf("capture stale daemon lock owner before recovery: %w", captureErr)
			}
			if err := terminate(l.LockPath); err != nil {
				if l.waitForSocketReadyWithin(1*time.Second) == nil {
					return nil, nil
				}
				return nil, fmt.Errorf("recover stale daemon lock owner: %w", err)
			}
			if ownerPresent {
				if err := l.waitForCapturedOwnerExit(ctx, owner, "start recovery"); err != nil {
					return nil, err
				}
			}
		} else if !allowUnreadyOwnerRecovery {
			return nil, errors.New("daemon lock owner appeared before replacement successor startup; refusing unverified recovery termination")
		}
	}
	openLogFile := l.openLogFile
	if openLogFile == nil {
		openLogFile = openDaemonLog
	}
	cfg, _ := config.LoadConfig(l.RepoDir)
	logFile, err := openLogFile(filepath.Join(config.SessionLogDirFor(cfg, l.RepoDir), logging.DaemonLogFileName))
	if err != nil {
		return nil, fmt.Errorf("open daemon log: %w", err)
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
		command:                  daemonCmd,
		args:                     args,
		stdout:                   logFile,
		stderr:                   logFile,
		requireGroupCleanupProof: requireGroupCleanupProof,
	})
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("start daemon %s: %w", daemonCmd.displayName(), err)
	}
	if process == nil {
		spanErr = errors.New("daemon process starter returned nil process")
		return nil, fmt.Errorf("start daemon %s: %w", daemonCmd.displayName(), spanErr)
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
			return process, fmt.Errorf("wait for daemon socket readiness: %w", spanErr)
		case err = <-readyResult:
			cancel()
		}
		if err != nil {
			spanErr = err
			return process, fmt.Errorf("wait for daemon socket readiness: %w", err)
		}
	}
	return process, nil
}

func (l *Launcher) waitForSocketReadyWithin(timeout time.Duration) error {
	if l.waitForReady == nil {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return l.waitForReady(readyCtx, l.SocketPath)
}

// Stop attempts to stop existing lock-owner process.
func (l *Launcher) Stop(ctx context.Context) error {
	var releaseLifecycleLock func()
	if l.ownsCanonicalRuntime() {
		var acquired bool
		var err error
		releaseLifecycleLock, acquired, err = l.acquireLifecycleLock(ctx, false)
		if err != nil {
			return fmt.Errorf("acquire daemon lifecycle lock for stop: %w", err)
		}
		if !acquired {
			return errors.New("acquire daemon lifecycle lock for stop: lock bypassed unexpectedly")
		}
		defer releaseLifecycleLock()
	}

	// Capture the owner before shutdown: azd releases daemon.lock before its
	// deferred telemetry flush completes, so lock disappearance is not an exit
	// signal. The OS start token distinguishes the exact process from later PID
	// reuse.
	_, ownerRecorded := l.readLockedPID()
	if l.ownsCanonicalRuntime() {
		if _, err := os.Lstat(l.SocketPath); err == nil && !ownerRecorded {
			return errors.New("missing daemon lock owner identity for live canonical runtime socket")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect canonical daemon socket before stop: %w", err)
		}
	}
	owner, ownerPresent, err := l.captureLockOwnerIdentity()
	if err != nil {
		return fmt.Errorf("capture daemon lock owner identity: %w", err)
	}
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
	if ownerPresent {
		if err := l.waitForCapturedOwnerExit(ctx, owner, "stop"); err != nil {
			return err
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

	// Remove the pre-stable-lock artifact when cleaning a runtime created by an
	// older launcher; it is not part of the new serialization authority.
	legacyStartLockPath := wantLock + ".start"
	if err := os.Remove(legacyStartLockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy scoped daemon start lock: %w", err)
	}
	if _, err := os.Lstat(wantSocket); err == nil {
		return fmt.Errorf("daemon socket still exists after stop: %s", wantSocket)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect stopped daemon socket: %w", err)
	}
	if err := os.Remove(wantLock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove scoped daemon asset %s: %w", wantLock, err)
	}
	stagedExecutables := filepath.Join(runtimeDir, "executables")
	if err := os.RemoveAll(stagedExecutables); err != nil {
		return fmt.Errorf("remove scoped daemon staged executables: %w", err)
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
		if entry.Name() != filepath.Base(wantLock) {
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
	if err := os.Remove(tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove detached scoped daemon runtime: %w", err)
	}
	return nil
}

func (l *Launcher) waitForProcessExit(ctx context.Context, identity processIdentity) error {
	if l.waitForOwnerExit != nil {
		return l.waitForOwnerExit(ctx, identity)
	}
	for {
		alive, err := processIdentityAlive(identity)
		if err != nil {
			return fmt.Errorf("inspect daemon lock owner %d after stop: %w", identity.pid, err)
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon lock owner %d still alive after stop: %w", identity.pid, ctx.Err())
		default:
		}
		sleep := l.sleepFn
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(20 * time.Millisecond)
	}
}

func (l *Launcher) verifyCapturedPredecessor(ctx context.Context, owner processIdentity, daemonCmd daemonCommand) error {
	if !l.ownsCanonicalRuntime() || config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return errors.New("refuse forced predecessor termination outside the canonical global runtime")
	}
	current, present, err := captureProcessIdentity(owner.pid)
	if err != nil {
		return fmt.Errorf("recapture daemon predecessor identity: %w", err)
	}
	if !present {
		return nil
	}
	if !sameProcessIdentity(owner, true, current, true) {
		return errors.New("daemon predecessor process identity changed during termination")
	}
	if pid, recorded := l.readLockedPID(); recorded {
		lockOwner, lockOwnerPresent, captureErr := captureProcessIdentity(pid)
		if captureErr != nil {
			return fmt.Errorf("recapture daemon lock owner: %w", captureErr)
		}
		if !sameProcessIdentity(owner, true, lockOwner, lockOwnerPresent) {
			return errors.New("daemon lock ownership changed during predecessor termination")
		}
	} else if _, lockErr := os.Lstat(l.LockPath); lockErr == nil {
		return errors.New("daemon lock became unreadable during predecessor termination")
	} else if !errors.Is(lockErr, os.ErrNotExist) {
		return fmt.Errorf("inspect daemon lock during predecessor termination: %w", lockErr)
	} else if _, socketErr := os.Lstat(l.SocketPath); socketErr == nil {
		return errors.New("daemon lock disappeared while canonical socket remains")
	} else if !errors.Is(socketErr, os.ErrNotExist) {
		return fmt.Errorf("inspect daemon socket during predecessor termination: %w", socketErr)
	}
	predecessorGeneration, predecessorManaged := config.ManagedGenerationBinDir(current.executable, "azd")
	successorGeneration, successorManaged := config.ManagedGenerationBinDir(daemonCmd.executable, "azd")
	if !predecessorManaged {
		return errors.New("refuse forced predecessor termination for unmanaged executable")
	}
	if !successorManaged || filepath.Clean(filepath.Dir(predecessorGeneration)) != filepath.Clean(filepath.Dir(successorGeneration)) {
		return errors.New("refuse forced predecessor termination across unrelated install roots")
	}
	if err := l.verifyCanonicalDaemonArguments(current, "forced predecessor termination"); err != nil {
		return err
	}
	return nil
}

func (l *Launcher) signalCapturedPredecessor(ctx context.Context, owner processIdentity, daemonCmd daemonCommand, signal syscall.Signal) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if l.beforePredecessorSignal != nil {
		l.beforePredecessorSignal(signal)
	}
	openSignalHandle := l.openProcessSignalHandle
	if openSignalHandle == nil {
		openSignalHandle = openPlatformProcessSignalHandle
	}
	handle, err := openSignalHandle(owner.pid)
	if err != nil {
		alive, identityErr := processIdentityAlive(owner)
		if identityErr == nil && !alive {
			return nil
		}
		if identityErr != nil {
			return fmt.Errorf("open identity-bound signal handle for daemon predecessor: %w (identity check: %v)", err, identityErr)
		}
		return fmt.Errorf("open identity-bound signal handle for daemon predecessor: %w", err)
	}
	if handle == nil {
		return fmt.Errorf("open identity-bound signal handle for daemon predecessor: platform returned no handle")
	}
	defer func() { _ = handle.Close() }()
	if err := l.verifyCapturedPredecessor(ctx, owner, daemonCmd); err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	alive, err := processIdentityAlive(owner)
	if err != nil || !alive {
		return err
	}
	if l.afterPredecessorVerification != nil {
		l.afterPredecessorVerification()
	}
	signalCtx, endSpan := latencytrace.StartSpan(ctx, "dependency", "daemon_process.reap_predecessor",
		"dependency.operation", strings.ToLower(strings.TrimPrefix(signal.String(), "SIG")),
	)
	_ = signalCtx
	if err := handle.Signal(signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		endSpan(err)
		return fmt.Errorf("signal verified daemon predecessor with %s: %w", signal, err)
	}
	endSpan(nil)
	return nil
}

func (l *Launcher) waitForCapturedOwnerExitWithin(ctx context.Context, owner processIdentity, timeout time.Duration) error {
	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(waitCtx, timeout)
		defer cancel()
	}
	return l.waitForProcessExit(waitCtx, owner)
}

func (l *Launcher) reapCapturedPredecessor(ctx context.Context, owner processIdentity, daemonCmd daemonCommand, action string) error {
	gracefulTimeout := l.gracefulExitTimeout
	if gracefulTimeout <= 0 {
		gracefulTimeout = 2 * time.Second
	}
	if err := l.waitForCapturedOwnerExitWithin(ctx, owner, gracefulTimeout); err == nil {
		if err := l.cleanupReapedPredecessor(owner); err != nil {
			return err
		}
		l.logPredecessorReapStage(action, "graceful", "exited")
		return nil
	} else if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("wait for graceful daemon predecessor exit before %s: %w", action, err)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("inspect graceful daemon predecessor exit before %s: %w", action, err)
	}
	if l.Logger != nil {
		l.Logger.Warn("verified daemon predecessor did not exit after graceful shutdown; escalating",
			"action", action,
			"stage", "term",
		)
	}
	if err := l.signalCapturedPredecessor(ctx, owner, daemonCmd, syscall.SIGTERM); err != nil {
		return fmt.Errorf("terminate verified daemon predecessor before %s: %w", action, err)
	}
	termTimeout := l.termExitTimeout
	if termTimeout <= 0 {
		termTimeout = 2 * time.Second
	}
	if err := l.waitForCapturedOwnerExitWithin(ctx, owner, termTimeout); err == nil {
		if err := l.cleanupReapedPredecessor(owner); err != nil {
			return err
		}
		l.logPredecessorReapStage(action, "term", "exited")
		return nil
	} else if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("wait for verified daemon predecessor after TERM before %s: %w", action, err)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("inspect verified daemon predecessor after TERM before %s: %w", action, err)
	}
	if l.Logger != nil {
		l.Logger.Warn("verified daemon predecessor ignored TERM; force killing",
			"action", action,
			"stage", "kill",
		)
	}
	if err := l.signalCapturedPredecessor(ctx, owner, daemonCmd, syscall.SIGKILL); err != nil {
		return fmt.Errorf("force-kill verified daemon predecessor before %s: %w", action, err)
	}
	killTimeout := l.killExitTimeout
	if killTimeout <= 0 {
		killTimeout = 2 * time.Second
	}
	if err := l.waitForCapturedOwnerExitWithin(ctx, owner, killTimeout); err != nil {
		return fmt.Errorf("prove force-killed daemon predecessor exited before %s: %w", action, err)
	}
	if err := l.cleanupReapedPredecessor(owner); err != nil {
		return err
	}
	l.logPredecessorReapStage(action, "kill", "exited")
	return nil
}

func (l *Launcher) logPredecessorReapStage(action, stage, outcome string) {
	if l.Logger == nil {
		return
	}
	l.Logger.Info("daemon predecessor replacement stage completed",
		"action", action,
		"stage", stage,
		"outcome", outcome,
	)
}

func (l *Launcher) startReplacementWithLifecycleLock(ctx context.Context, daemonCmd daemonCommand) error {
	process, err := l.startWithLifecycleLockModeRetained(ctx, daemonCmd, false, true)
	if err == nil && (l.startProcess == nil || l.replacementSuccessorVerifier != nil) {
		verifySuccessor := l.replacementSuccessorVerifier
		if verifySuccessor == nil {
			verifySuccessor = l.verifyReplacementSuccessor
		}
		err = verifySuccessor(daemonCmd)
	}
	if err != nil && process != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		cleanupErr := process.stopAndWait(cleanupCtx)
		cleanupCancel()
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %v", errReplacementCandidateCleanupUnproven, cleanupErr))
		} else {
			err = refineSpawnedDaemonExitError(err, process)
		}
	}
	if err == nil {
		releaseDaemonProcessCleanupAuthority(process)
	}
	if l.Logger == nil {
		return err
	}
	if err != nil {
		l.Logger.Error("daemon replacement successor failed to start; runtime state preserved for recovery",
			"stage", "successor_start",
			"outcome", "failed",
			"reason", "start_failed",
		)
		return err
	}
	l.Logger.Info("daemon replacement successor is ready",
		"stage", "successor_start",
		"outcome", "ready",
	)
	return nil
}

func (l *Launcher) verifyReplacementSuccessor(daemonCmd daemonCommand) error {
	successor, present, err := l.captureLockOwnerIdentity()
	if err != nil {
		return fmt.Errorf("capture replacement successor identity: %w", err)
	}
	if !present {
		return errors.New("replacement socket became ready without a verified lock owner")
	}
	wantExecutable, err := filepath.EvalSymlinks(daemonCmd.executable)
	if err != nil {
		return fmt.Errorf("resolve replacement successor executable: %w", err)
	}
	gotExecutable, err := filepath.EvalSymlinks(successor.executable)
	if err != nil {
		return fmt.Errorf("resolve replacement lock owner executable: %w", err)
	}
	if filepath.Clean(gotExecutable) != filepath.Clean(wantExecutable) {
		return errors.New("replacement socket lock owner does not run the installed successor executable")
	}
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		if _, managed := config.ManagedGenerationBinDir(successor.executable, "azd"); !managed {
			return errors.New("replacement socket lock owner is not a managed installed daemon")
		}
	}
	if err := l.verifyCanonicalDaemonArguments(successor, "replacement socket lock owner"); err != nil {
		return err
	}
	confirmed, confirmedPresent, err := l.captureLockOwnerIdentity()
	if err != nil {
		return fmt.Errorf("recapture replacement successor identity: %w", err)
	}
	if !sameProcessIdentity(successor, true, confirmed, confirmedPresent) {
		return errors.New("replacement successor lock ownership changed during readiness verification")
	}
	return nil
}

func (l *Launcher) cleanupReapedPredecessor(owner processIdentity) error {
	if alive, err := processIdentityAlive(owner); err != nil {
		return err
	} else if alive {
		return fmt.Errorf("daemon predecessor %d remains alive after termination", owner.pid)
	}
	if pid, recorded := l.readLockedPID(); recorded {
		if pid != owner.pid {
			return errors.New("daemon lock owner changed before predecessor cleanup")
		}
		_, present, err := captureProcessIdentity(pid)
		if err != nil {
			return fmt.Errorf("inspect daemon lock owner before predecessor cleanup: %w", err)
		}
		if present {
			return errors.New("daemon predecessor PID was reused before lock cleanup")
		}
		if err := os.Remove(l.LockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove reaped daemon predecessor lock: %w", err)
		}
	}
	if _, err := os.Lstat(l.SocketPath); err == nil {
		return errors.New("daemon predecessor exited but canonical socket remains; recovery requires removing the verified stale runtime asset")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect reaped daemon predecessor socket: %w", err)
	}
	return nil
}

func (l *Launcher) ownsCanonicalScopedRuntime() bool {
	if l == nil || !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return false
	}
	return filepath.Clean(l.SocketPath) == filepath.Clean(config.ScopedDaemonSocketPath(l.RepoDir)) &&
		filepath.Clean(l.LockPath) == filepath.Clean(config.ScopedDaemonLockPath(l.RepoDir))
}

func (l *Launcher) ownsCanonicalRuntime() bool {
	if l == nil {
		return false
	}
	if l.ownsCanonicalScopedRuntime() {
		return true
	}
	return filepath.Clean(l.SocketPath) == filepath.Clean(config.GlobalDaemonSocketPath()) &&
		filepath.Clean(l.LockPath) == filepath.Clean(config.GlobalDaemonLockPath())
}

func (l *Launcher) scopedLifecycleLockPath() string {
	if l != nil && config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		runtimeDir := config.ScopedDaemonRuntimeDir(l.RepoDir)
		return filepath.Join(filepath.Dir(runtimeDir), filepath.Base(runtimeDir)+".lifecycle.lock")
	}
	return l.LockPath + ".start"
}

// Replace attempts to stop an existing daemon process, then starts daemon.
func (l *Launcher) Replace(ctx context.Context) (resultErr error) {
	// Observe the caller-visible predecessor before queueing. A concurrent
	// Replace that wins the lifecycle lock may replace this exact incarnation;
	// after we acquire the lock, that identity change lets us coalesce onto its
	// ready successor instead of immediately replacing the successor again.
	observedOwner, observedOwnerPresent, err := l.captureLockOwnerIdentity()
	if err != nil {
		return fmt.Errorf("capture daemon predecessor before replace: %w", err)
	}
	if l.beforeReplaceLock != nil {
		l.beforeReplaceLock()
	}
	releaseLifecycleLock, acquired, err := l.acquireLifecycleLock(ctx, false)
	if err != nil {
		return fmt.Errorf("acquire daemon lifecycle lock for replace: %w", err)
	}
	if !acquired {
		return errors.New("acquire daemon lifecycle lock for replace: lock bypassed unexpectedly")
	}
	defer releaseLifecycleLock()

	owner, ownerPresent, err := l.captureLockOwnerIdentity()
	if err != nil {
		return fmt.Errorf("capture daemon predecessor for replace: %w", err)
	}
	if ownerPresent && !sameProcessIdentity(observedOwner, observedOwnerPresent, owner, ownerPresent) {
		if l.waitForSocketReadyWithin(300*time.Millisecond) == nil {
			return nil
		}
		return errors.New("daemon owner identity changed while replacement waited for the lifecycle lock; refusing to signal the unready successor")
	}
	if !ownerPresent && l.ownsCanonicalRuntime() && l.waitForSocketReadyWithin(300*time.Millisecond) == nil {
		return errors.New("missing daemon lock owner identity for live canonical runtime during replace")
	}

	daemonCmd, err := l.resolveCommand()
	if err != nil {
		return err
	}
	daemonCmd, err = l.materializeReplacementCommand(ctx, daemonCmd)
	if err != nil {
		return fmt.Errorf("resolve daemon replacement candidate: %w", err)
	}
	candidateStageOwned := strings.TrimSpace(daemonCmd.candidateStage) != ""
	defer func() {
		if candidateStageOwned {
			resultErr = errors.Join(resultErr, l.cleanupInactiveCandidateStage(daemonCmd))
		}
	}()
	preflight := l.preflightReplace
	if preflight == nil {
		preflight = preflightReplacementCommand
	}
	if err := preflight(ctx, daemonCmd); err != nil {
		return fmt.Errorf("preflight daemon replacement %s before stopping predecessor: %w", daemonCmd.displayName(), err)
	}
	predecessorCmd, predecessorPresent, err := l.resolvePredecessorRollbackCommand(owner, ownerPresent)
	if err != nil {
		return fmt.Errorf("preflight daemon predecessor rollback before stopping predecessor: %w", err)
	}
	rollbackStageOwned := predecessorPresent && strings.TrimSpace(predecessorCmd.rollbackStage) != ""
	defer func() {
		if rollbackStageOwned {
			resultErr = errors.Join(resultErr, l.cleanupInactiveRollbackStage(predecessorCmd))
		}
	}()
	if predecessorPresent {
		rollbackPreflight := l.preflightRollback
		if rollbackPreflight == nil {
			rollbackPreflight = preflightPredecessorRollbackCommand
		}
		if err := rollbackPreflight(ctx, predecessorCmd); err != nil {
			return fmt.Errorf("preflight daemon predecessor rollback %s before stopping predecessor: %w", predecessorCmd.displayName(), err)
		}
	}
	// Preflight is the last point where the predecessor is guaranteed healthy.
	// From the first shutdown request onward, retain the verified rollback stage
	// across every failure; only verified candidate success may remove it.
	rollbackStageOwned = false
	if strings.TrimSpace(l.SocketPath) != "" && (ownerPresent || !l.ownsCanonicalRuntime()) {
		reason := strings.TrimSpace(l.replaceReason)
		if reason == "" {
			reason = "compatibility-replace"
		}
		if err := l.requestGracefulShutdown(ctx, reason); err == nil {
			if ownerPresent {
				if err := l.reapCapturedPredecessor(ctx, owner, daemonCmd, "replace"); err != nil {
					return err
				}
			}
			candidateStageOwned = false
			return l.startReplacementWithRollback(ctx, daemonCmd, predecessorCmd, predecessorPresent)
		} else if l.Logger != nil {
			l.Logger.Warn("graceful daemon socket shutdown failed before replace; evaluating verified predecessor escalation",
				"stage", "graceful_request",
				"outcome", "failed",
				"reason", "typed_shutdown_failed",
			)
		}
	}

	if ownerPresent {
		if err := l.reapCapturedPredecessor(ctx, owner, daemonCmd, "replace"); err != nil {
			return err
		}
	} else {
		if pid, recorded := l.readLockedPID(); recorded {
			return fmt.Errorf("daemon lock owner %d appeared after predecessor capture; refusing unverified replacement termination", pid)
		}
		if _, lockErr := os.Lstat(l.LockPath); lockErr == nil {
			return errors.New("unreadable daemon lock appeared after predecessor capture; refusing replacement start")
		} else if !errors.Is(lockErr, os.ErrNotExist) {
			return fmt.Errorf("inspect daemon lock before replacement start: %w", lockErr)
		}
		if _, socketErr := os.Lstat(l.SocketPath); socketErr == nil {
			return errors.New("daemon socket appeared without a verified predecessor; refusing replacement start")
		} else if !errors.Is(socketErr, os.ErrNotExist) {
			return fmt.Errorf("inspect daemon socket before replacement start: %w", socketErr)
		}
	}
	candidateStageOwned = false
	return l.startReplacementWithRollback(ctx, daemonCmd, predecessorCmd, predecessorPresent)
}

func (l *Launcher) resolvePredecessorRollbackCommand(owner processIdentity, ownerPresent bool) (daemonCommand, bool, error) {
	if !ownerPresent {
		return daemonCommand{}, false, nil
	}
	if strings.TrimSpace(owner.executable) == "" {
		return daemonCommand{}, false, errors.New("predecessor executable path was unavailable")
	}
	resolvedExecutable, err := resolveCommandExecutable(daemonCommand{executable: owner.executable})
	if err != nil {
		return daemonCommand{}, false, fmt.Errorf("resolve predecessor executable %s: %w", owner.executable, err)
	}
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		if _, ok := config.ManagedGenerationBinDir(resolvedExecutable, "azd"); !ok {
			return daemonCommand{}, false, fmt.Errorf("predecessor executable %s is not a managed azd generation", resolvedExecutable)
		}
	}
	if err := l.verifyCanonicalDaemonArguments(owner, "predecessor"); err != nil {
		return daemonCommand{}, false, err
	}
	retiredPredecessorStage, _ := l.scopedCandidateStage(owner.executable)
	stagedExecutable, err := l.stagePredecessorExecutableCopy(owner, "predecessor")
	if err != nil {
		return daemonCommand{}, false, err
	}
	resolvedExecutable = stagedExecutable
	command := l.commandForExecutable(resolvedExecutable)
	command.rollbackStage = filepath.Dir(resolvedExecutable)
	command.retiredPredecessorStage = retiredPredecessorStage
	return command, true, nil
}

func (l *Launcher) scopedCandidateStage(executable string) (string, bool) {
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return "", false
	}
	resolvedExecutable, err := filepath.EvalSymlinks(strings.TrimSpace(executable))
	if err != nil || filepath.Base(resolvedExecutable) != "azd" {
		return "", false
	}
	stageDir := filepath.Clean(filepath.Dir(resolvedExecutable))
	executableRoot := filepath.Join(config.ScopedDaemonRuntimeDir(l.RepoDir), "executables")
	resolvedRoot, err := filepath.EvalSymlinks(executableRoot)
	if err != nil {
		return "", false
	}
	if filepath.Clean(filepath.Dir(stageDir)) != filepath.Clean(resolvedRoot) || !strings.HasPrefix(filepath.Base(stageDir), "candidate-") {
		return "", false
	}
	return stageDir, true
}

func (l *Launcher) cleanupInactiveRollbackStage(command daemonCommand) error {
	stageDir := filepath.Clean(strings.TrimSpace(command.rollbackStage))
	if stageDir == "." || stageDir == "" {
		return nil
	}
	if filepath.Clean(filepath.Dir(command.executable)) != stageDir {
		return errors.New("refuse rollback cleanup whose executable is outside its retained stage")
	}
	base := filepath.Base(stageDir)
	switch {
	case strings.HasPrefix(base, "generation.rollback-"):
		generationDir, managed := config.ManagedGenerationBinDir(command.executable, "azd")
		if !managed || filepath.Clean(generationDir) != stageDir {
			return fmt.Errorf("refuse rollback cleanup outside a managed daemon generation: %s", stageDir)
		}
	case strings.HasPrefix(base, "predecessor-"):
		executableRoot := filepath.Clean(filepath.Join(config.ScopedDaemonRuntimeDir(l.RepoDir), "executables"))
		if resolvedRoot, err := filepath.EvalSymlinks(executableRoot); err == nil {
			executableRoot = filepath.Clean(resolvedRoot)
		}
		if filepath.Clean(filepath.Dir(stageDir)) != executableRoot {
			return fmt.Errorf("refuse rollback cleanup outside the scoped executable root: %s", stageDir)
		}
	default:
		return fmt.Errorf("refuse rollback cleanup for unrecognized stage %s", stageDir)
	}
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("remove inactive daemon predecessor rollback stage: %w", err)
	}
	return nil
}

func (l *Launcher) cleanupInactiveCandidateStage(command daemonCommand) error {
	stageDir := filepath.Clean(strings.TrimSpace(command.candidateStage))
	if stageDir == "." || stageDir == "" {
		return nil
	}
	if filepath.Clean(filepath.Dir(command.executable)) != stageDir {
		return errors.New("refuse candidate cleanup whose executable is outside its retained stage")
	}
	executableRoot := filepath.Clean(filepath.Join(config.ScopedDaemonRuntimeDir(l.RepoDir), "executables"))
	if resolvedRoot, err := filepath.EvalSymlinks(executableRoot); err == nil {
		executableRoot = filepath.Clean(resolvedRoot)
	}
	if !strings.HasPrefix(filepath.Base(stageDir), "candidate-") || filepath.Clean(filepath.Dir(stageDir)) != executableRoot {
		return fmt.Errorf("refuse candidate cleanup outside the scoped executable root: %s", stageDir)
	}
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("remove inactive daemon replacement candidate stage: %w", err)
	}
	return nil
}

func (l *Launcher) cleanupRetiredPredecessorStage(command daemonCommand) error {
	stageDir := filepath.Clean(strings.TrimSpace(command.retiredPredecessorStage))
	if stageDir == "." || stageDir == "" {
		return nil
	}
	executableRoot := filepath.Join(config.ScopedDaemonRuntimeDir(l.RepoDir), "executables")
	resolvedRoot, err := filepath.EvalSymlinks(executableRoot)
	if err != nil {
		return fmt.Errorf("resolve scoped executable root for retired predecessor cleanup: %w", err)
	}
	if filepath.Clean(filepath.Dir(stageDir)) != filepath.Clean(resolvedRoot) || !strings.HasPrefix(filepath.Base(stageDir), "candidate-") {
		return fmt.Errorf("refuse retired predecessor cleanup outside the scoped executable root: %s", stageDir)
	}
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("remove retired daemon predecessor candidate stage: %w", err)
	}
	return nil
}

func (l *Launcher) materializeReplacementCommand(ctx context.Context, command daemonCommand) (daemonCommand, error) {
	if !command.sourceFallback {
		resolvedExecutable, err := resolveCommandExecutable(command)
		if err != nil {
			return daemonCommand{}, err
		}
		command.executable = resolvedExecutable
		return command, nil
	}
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return daemonCommand{}, errors.New("source-fallback daemon materialization requires worktree-scoped runtime")
	}
	goExecutable, err := resolveCommandExecutable(command)
	if err != nil {
		return daemonCommand{}, fmt.Errorf("resolve Go tool for scoped daemon source fallback: %w", err)
	}
	stageDir, stagedExecutable, err := l.newScopedExecutableStage("candidate")
	if err != nil {
		return daemonCommand{}, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stageDir)
		}
	}()
	build := exec.CommandContext(ctx, goExecutable, "build", "-o", stagedExecutable, "./cmd/azd")
	build.Dir = command.dir
	build.Env = command.env
	build.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	build.Cancel = func() error {
		if build.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-build.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	stdout := boundedOutput{remaining: 64 << 10}
	stderr := boundedOutput{remaining: 64 << 10}
	build.Stdout = &stdout
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		return daemonCommand{}, fmt.Errorf("build scoped daemon source fallback: %w (stdout %q, stderr %q)", err, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
	}
	resolvedExecutable, err := resolveCommandExecutable(daemonCommand{executable: stagedExecutable})
	if err != nil {
		return daemonCommand{}, fmt.Errorf("resolve staged scoped daemon candidate: %w", err)
	}
	keepStage = true
	return daemonCommand{executable: resolvedExecutable, env: command.env, candidateStage: filepath.Dir(resolvedExecutable)}, nil
}

func (l *Launcher) stageScopedExecutableCopy(owner processIdentity, purpose string) (string, error) {
	return l.stagePredecessorExecutableCopy(owner, purpose)
}

func (l *Launcher) stagePredecessorExecutableCopy(owner processIdentity, purpose string) (string, error) {
	stageDir, stagedExecutable, err := l.newPredecessorExecutableStage(owner, purpose)
	if err != nil {
		return "", err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stageDir)
		}
	}()
	source, err := openPlatformProcessExecutable(owner)
	if err != nil {
		return "", fmt.Errorf("open process-bound daemon predecessor executable for rollback: %w", err)
	}
	before, err := source.Stat()
	if err != nil {
		_ = source.Close()
		return "", fmt.Errorf("inspect daemon predecessor executable before staging: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 {
		_ = source.Close()
		return "", errors.New("daemon predecessor executable is not a regular executable file")
	}
	destination, err := os.OpenFile(stagedExecutable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, before.Mode().Perm())
	if err != nil {
		_ = source.Close()
		return "", fmt.Errorf("create staged daemon predecessor executable: %w", err)
	}
	copiedHash := sha256.New()
	copiedBytes, copyErr := io.Copy(io.MultiWriter(destination, copiedHash), source)
	syncErr := destination.Sync()
	closeDestinationErr := destination.Close()
	_, seekErr := source.Seek(0, io.SeekStart)
	sourceHash := sha256.New()
	_, hashErr := io.Copy(sourceHash, source)
	after, statErr := source.Stat()
	closeSourceErr := source.Close()
	if copyErr != nil {
		return "", fmt.Errorf("copy daemon predecessor executable for rollback: %w", copyErr)
	}
	if syncErr != nil {
		return "", fmt.Errorf("sync staged daemon predecessor executable: %w", syncErr)
	}
	if closeDestinationErr != nil {
		return "", fmt.Errorf("close staged daemon predecessor executable: %w", closeDestinationErr)
	}
	if seekErr != nil {
		return "", fmt.Errorf("rewind daemon predecessor executable after staging: %w", seekErr)
	}
	if hashErr != nil {
		return "", fmt.Errorf("hash daemon predecessor executable after staging: %w", hashErr)
	}
	if statErr != nil {
		return "", fmt.Errorf("inspect daemon predecessor executable after staging: %w", statErr)
	}
	if closeSourceErr != nil {
		return "", fmt.Errorf("close daemon predecessor executable: %w", closeSourceErr)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("daemon predecessor executable changed while staging rollback copy")
	}
	if copiedBytes != before.Size() || !bytes.Equal(copiedHash.Sum(nil), sourceHash.Sum(nil)) {
		return "", errors.New("staged daemon predecessor executable does not match the verified source bytes")
	}
	confirmed, confirmedPresent, err := l.captureLockOwnerIdentity()
	if err != nil {
		return "", fmt.Errorf("recapture daemon predecessor after staging rollback copy: %w", err)
	}
	if !sameProcessIdentity(owner, true, confirmed, confirmedPresent) {
		return "", errors.New("daemon predecessor identity changed while staging rollback copy")
	}
	resolvedExecutable, err := resolveCommandExecutable(daemonCommand{executable: stagedExecutable})
	if err != nil {
		return "", fmt.Errorf("resolve staged daemon predecessor: %w", err)
	}
	if !config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		if _, managed := config.ManagedGenerationBinDir(resolvedExecutable, "azd"); !managed {
			return "", errors.New("staged global daemon predecessor is not a coherent managed generation")
		}
	}
	keepStage = true
	return resolvedExecutable, nil
}

func (l *Launcher) newPredecessorExecutableStage(owner processIdentity, purpose string) (string, string, error) {
	if config.UseScopedDaemonRuntimeFor(l.RepoDir) {
		return l.newScopedExecutableStage(purpose)
	}
	generationDir, managed := config.ManagedGenerationBinDir(owner.executable, "azd")
	if !managed {
		return "", "", errors.New("global daemon predecessor is not a coherent managed generation")
	}
	generationsRoot := filepath.Dir(generationDir)
	stageDir, err := os.MkdirTemp(generationsRoot, "generation.rollback-")
	if err != nil {
		return "", "", fmt.Errorf("create managed predecessor rollback generation: %w", err)
	}
	stagedExecutable := filepath.Join(stageDir, "azd")
	if err := copyExecutablePath(filepath.Join(generationDir, "az"), filepath.Join(stageDir, "az")); err != nil {
		_ = os.RemoveAll(stageDir)
		return "", "", fmt.Errorf("stage managed predecessor az companion: %w", err)
	}
	return stageDir, stagedExecutable, nil
}

func copyExecutablePath(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	info, err := source.Stat()
	if err != nil {
		_ = source.Close()
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		_ = source.Close()
		return errors.New("source is not a regular executable file")
	}
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		_ = source.Close()
		return err
	}
	_, copyErr := io.Copy(destination, source)
	syncErr := destination.Sync()
	closeDestinationErr := destination.Close()
	closeSourceErr := source.Close()
	return errors.Join(copyErr, syncErr, closeDestinationErr, closeSourceErr)
}

func (l *Launcher) newScopedExecutableStage(purpose string) (string, string, error) {
	root := filepath.Join(config.ScopedDaemonRuntimeDir(l.RepoDir), "executables")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", fmt.Errorf("create scoped daemon executable staging root: %w", err)
	}
	stageDir, err := os.MkdirTemp(root, purpose+"-")
	if err != nil {
		return "", "", fmt.Errorf("create scoped daemon executable stage: %w", err)
	}
	return stageDir, filepath.Join(stageDir, "azd"), nil
}

func (l *Launcher) verifyCanonicalDaemonArguments(identity processIdentity, label string) error {
	if l.verifyDaemonArguments != nil {
		return l.verifyDaemonArguments(identity, label)
	}
	want := map[string]string{"repo": l.RepoDir, "socket": l.SocketPath, "lock": l.LockPath}
	parsed := make(map[string]string, len(want))
	arguments := identity.arguments
	for i := 1; i < len(arguments); i++ {
		argument := arguments[i]
		if argument == "--" || argument == "-" || !strings.HasPrefix(argument, "-") {
			return fmt.Errorf("%s executable arguments contain positional or trailing argument %q", label, argument)
		}
		nameValue := strings.TrimPrefix(argument, "-")
		nameValue = strings.TrimPrefix(nameValue, "-")
		name, value, hasValue := strings.Cut(nameValue, "=")
		_, known := want[name]
		if !known {
			return fmt.Errorf("%s executable arguments contain unexpected flag %q", label, argument)
		}
		if _, duplicate := parsed[name]; duplicate {
			return fmt.Errorf("%s executable arguments contain ambiguous duplicate --%s", label, name)
		}
		if !hasValue {
			i++
			if i >= len(arguments) {
				return fmt.Errorf("%s executable arguments omit canonical %s value", label, name)
			}
			value = arguments[i]
		}
		parsed[name] = value
	}
	for name, expected := range want {
		value, present := parsed[name]
		if !present || value != expected {
			return fmt.Errorf("%s executable arguments do not match canonical %s", label, name)
		}
	}
	return nil
}

func (l *Launcher) startReplacementWithRollback(ctx context.Context, daemonCmd, predecessorCmd daemonCommand, predecessorPresent bool) error {
	startErr := l.startReplacementWithLifecycleLock(ctx, daemonCmd)
	if startErr == nil {
		return errors.Join(l.cleanupInactiveRollbackStage(predecessorCmd), l.cleanupRetiredPredecessorStage(predecessorCmd))
	}
	if errors.Is(startErr, errReplacementCandidateCleanupUnproven) {
		return fmt.Errorf("start replacement daemon: %w; refusing predecessor restore while rejected candidate cleanup is unproven", startErr)
	}
	startErr = errors.Join(startErr, l.cleanupInactiveCandidateStage(daemonCmd))
	if !predecessorPresent {
		return fmt.Errorf("start replacement daemon: %w; no predecessor was present to restore", startErr)
	}
	rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rollbackCancel()
	if rollbackErr := l.startReplacementWithLifecycleLock(rollbackCtx, predecessorCmd); rollbackErr != nil {
		return fmt.Errorf("start replacement daemon: %w; restore predecessor %s: %v", startErr, predecessorCmd.displayName(), rollbackErr)
	}
	retiredCleanupErr := l.cleanupRetiredPredecessorStage(predecessorCmd)
	return errors.Join(fmt.Errorf("start replacement daemon: %w; restored predecessor %s", startErr, predecessorCmd.displayName()), retiredCleanupErr)
}

func preflightReplacementCommand(ctx context.Context, command daemonCommand) error {
	stdout, stderr, err := runDaemonExecutableProbe(ctx, command, "--preflight")
	if err != nil {
		return fmt.Errorf("run candidate compatibility preflight: %w (stdout %q, stderr %q)", err, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	var report protocol.DaemonExecutablePreflight
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		return fmt.Errorf("decode candidate preflight report: %w (stdout %q, stderr %q)", err, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	if !report.Accepts(protocol.CurrentVersion) {
		return fmt.Errorf("candidate %q supports protocol %d..%d, incompatible with client protocol %d", report.DaemonVersion, report.MinProtocolVersion, report.MaxProtocolVersion, protocol.CurrentVersion)
	}
	return nil
}

func preflightPredecessorRollbackCommand(ctx context.Context, command daemonCommand) error {
	// The predecessor is the exact executable/argv identity of the live daemon
	// that already serves this client's protocol. Probe the process-bound staged
	// bytes for loader/executable viability without requiring the new --preflight
	// flag, so the first upgrade from a pre-contract daemon remains recoverable.
	stdout, stderr, err := runDaemonExecutableProbe(ctx, command, "--version")
	if err != nil {
		return fmt.Errorf("run verified predecessor rollback viability preflight: %w (stdout %q, stderr %q)", err, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	if strings.TrimSpace(stdout) == "" {
		return fmt.Errorf("verified predecessor rollback viability preflight returned empty output (stderr %q)", strings.TrimSpace(stderr))
	}
	return nil
}

func runDaemonExecutableProbe(ctx context.Context, command daemonCommand, probeFlag string) (string, string, error) {
	args := append(append([]string(nil), command.args...), probeFlag)
	probe := exec.CommandContext(ctx, command.executable, args...)
	probe.Dir = command.dir
	probe.Env = command.env
	probe.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	probe.Cancel = func() error {
		if probe.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-probe.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	stdout := boundedOutput{remaining: 64 << 10}
	stderr := boundedOutput{remaining: 64 << 10}
	probe.Stdout = &stdout
	probe.Stderr = &stderr
	if err := probe.Start(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	pid := probe.Process.Pid
	exitObserved, observeErr := observePlatformProcessExit(pid)
	if observeErr != nil {
		killErr := syscall.Kill(-pid, syscall.SIGKILL)
		waitErr := probe.Wait()
		proofCtx, proofCancel := context.WithTimeout(context.Background(), 2*time.Second)
		groupErr := waitForNumericProcessGroupExit(proofCtx, pid)
		proofCancel()
		return stdout.String(), stderr.String(), fmt.Errorf("establish probe process-group exit observation: %w (cleanup: %v)", observeErr, errors.Join(ignoreAbsentProcessGroupSignal(killErr), waitErr, groupErr))
	}
	var observationErr error
	select {
	case observationErr = <-exitObserved:
	case <-ctx.Done():
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			observationErr = errors.Join(ctx.Err(), fmt.Errorf("cancel daemon executable probe group %d: %w", pid, err))
		} else {
			observationErr = ctx.Err()
		}
		observationErr = errors.Join(observationErr, <-exitObserved)
	}
	// The exited-but-unreaped leader still reserves its numeric PGID. Signal the
	// exact group before Wait releases that reservation, then reap the leader and
	// use group-zero probes only as disappearance evidence.
	killErr := ignoreAbsentProcessGroupSignal(syscall.Kill(-pid, syscall.SIGKILL))
	waitErr := probe.Wait()
	proofCtx, proofCancel := context.WithTimeout(context.Background(), 2*time.Second)
	groupErr := waitForNumericProcessGroupExit(proofCtx, pid)
	proofCancel()
	err := errors.Join(observationErr, killErr, waitErr, groupErr)
	return stdout.String(), stderr.String(), err
}

func ignoreNoSuchProcess(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func ignoreAbsentProcessGroupSignal(err error) error {
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		return nil
	}
	return err
}

func waitForNumericProcessGroupExit(ctx context.Context, pid int) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Kill(-pid, 0)
		switch {
		case errors.Is(err, syscall.ESRCH):
			return nil
		case err != nil && !errors.Is(err, syscall.EPERM):
			return fmt.Errorf("inspect daemon executable probe process group %d: %w", pid, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("prove daemon executable probe process group %d disappeared: %w", pid, ctx.Err())
		case <-ticker.C:
		}
	}
}

func resolveCommandExecutable(command daemonCommand) (string, error) {
	executable := strings.TrimSpace(command.executable)
	if executable == "" {
		return "", errors.New("empty daemon executable candidate")
	}
	if strings.ContainsRune(executable, filepath.Separator) {
		if !executableFile(executable) {
			return "", fmt.Errorf("daemon executable candidate is missing, not a regular file, or not executable: %s", executable)
		}
		resolved, err := filepath.EvalSymlinks(executable)
		if err != nil {
			return "", fmt.Errorf("resolve daemon executable candidate %s: %w", executable, err)
		}
		return resolved, nil
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return "", fmt.Errorf("daemon executable candidate %q was not found on PATH: %w", executable, err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve daemon executable candidate %s: %w", executable, err)
	}
	return resolved, nil
}

func (l *Launcher) captureLockOwnerIdentity() (processIdentity, bool, error) {
	if l.captureOwner != nil {
		return l.captureOwner()
	}
	ownerPID, ownerRecorded := l.readLockedPID()
	if !ownerRecorded || ownerPID == os.Getpid() {
		return processIdentity{}, false, nil
	}
	return captureProcessIdentity(ownerPID)
}

func sameProcessIdentity(left processIdentity, leftPresent bool, right processIdentity, rightPresent bool) bool {
	if leftPresent != rightPresent {
		return false
	}
	if !leftPresent {
		return true
	}
	return left.pid == right.pid &&
		left.startToken == right.startToken &&
		left.executable == right.executable &&
		slices.Equal(left.arguments, right.arguments)
}

func (l *Launcher) waitForCapturedOwnerExit(ctx context.Context, owner processIdentity, action string) error {
	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	timeout := l.processExitTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(waitCtx, timeout)
	defer cancel()
	if err := l.waitForProcessExit(waitCtx, owner); err != nil {
		return fmt.Errorf("prove daemon predecessor exited before %s: %w", action, err)
	}
	return nil
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
		return daemonCommand{}, fmt.Errorf("%w: global daemon launch requires the running az to resolve under .azedarach-generations/generation.* with an executable sibling azd; %s; reinstall the managed az/azd pair or set AZEDARACH_DAEMON_BIN explicitly", errPairedDaemonUnavailable, daemonCandidateDiagnostics())
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
		return daemonCommand{executable: "go", args: []string{"run", "./cmd/azd"}, dir: sourceDir, sourceFallback: true}, nil
	}
	return daemonCommand{executable: "azd"}, nil
}

func daemonCandidateDiagnostics() string {
	parts := make([]string, 0, 3)
	if executable, err := currentExecutable(); err != nil {
		parts = append(parts, "running az unresolved: "+err.Error())
	} else {
		parts = append(parts, "running az="+executable, "required sibling="+filepath.Join(filepath.Dir(executable), "azd"))
	}
	if pathAzd, err := exec.LookPath("azd"); err == nil {
		parts = append(parts, "PATH azd ignored for global replacement="+pathAzd)
	} else {
		parts = append(parts, "PATH azd unavailable")
	}
	return strings.Join(parts, "; ")
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
	return l.acquireLifecycleLock(ctx, true)
}

func (l *Launcher) acquireLifecycleLock(ctx context.Context, allowReadyBypass bool) (func(), bool, error) {
	startLockPath := l.scopedLifecycleLockPath()
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
		if allowReadyBypass && !l.ownsCanonicalRuntime() && l.waitForSocketReadyWithin(100*time.Millisecond) == nil {
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
	shutdownCtx, cancel := context.WithTimeout(shutdownCtx, 2*time.Second)
	defer cancel()

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
