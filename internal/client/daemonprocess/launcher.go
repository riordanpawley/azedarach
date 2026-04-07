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
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
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
	sleepFn            func(time.Duration)
	terminateLockOwner func(lockPath string) error
}

// NewLauncher returns a daemon process launcher for repoDir.
func NewLauncher(repoDir, socketPath string) *Launcher {
	if scopedDaemonRuntimeEnabled() {
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
		sleepFn:            time.Sleep,
		terminateLockOwner: lifecycle.TerminateLockOwner,
	}
}

func scopedDaemonRuntimeEnabled() bool {
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE")))
	source := strings.TrimSpace(strings.ToLower(os.Getenv("AZEDARACH_DAEMON_SCOPE_SOURCE")))
	modeEnabled := mode == "worktree" || mode == "scoped" || mode == "local"
	return modeEnabled && source == "just-run"
}

// WithLogger overrides launcher logging sink for lifecycle warnings.
func (l *Launcher) WithLogger(logger *slog.Logger) *Launcher {
	if l == nil || logger == nil {
		return l
	}
	l.Logger = logger
	return l
}

// Start spawns daemon process in background.
func (l *Launcher) Start(ctx context.Context) error {
	_ = ctx
	bin := l.resolveBinary()
	releaseStartLock, err := l.acquireStartLock(ctx)
	if err != nil {
		return err
	}
	defer releaseStartLock()

	if l.daemonLockOwnerAlive() {
		if l.waitForReady != nil {
			readyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := l.waitForReady(readyCtx, l.SocketPath)
			cancel()
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
				return fmt.Errorf("recover stale daemon lock owner: %w", err)
			}
		}
	}

	if err := os.MkdirAll(filepath.Join(l.RepoDir, ".azedarach"), 0o755); err != nil {
		return fmt.Errorf("create .azedarach dir: %w", err)
	}
	openLogFile := l.openLogFile
	if openLogFile == nil {
		openLogFile = openDaemonLog
	}
	logFile, err := openLogFile(filepath.Join(l.RepoDir, ".azedarach", "daemon.log"))
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() {
		_ = logFile.Close()
	}()
	// Do not bind daemon lifetime to the caller context. Attach contexts are short-lived.
	cmd := exec.Command(bin, "--repo", l.RepoDir, "--socket", l.SocketPath, "--lock", l.LockPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon %s: %w", bin, err)
	}
	if l.waitForReady != nil {
		readyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := l.waitForReady(readyCtx, l.SocketPath)
		cancel()
		if err != nil {
			return fmt.Errorf("wait for daemon socket readiness: %w", err)
		}
	}
	return nil
}

// Stop attempts to stop existing lock-owner process.
func (l *Launcher) Stop(ctx context.Context) error {
	_ = ctx
	terminate := l.terminateLockOwner
	if terminate == nil {
		terminate = lifecycle.TerminateLockOwner
	}
	if err := terminate(l.LockPath); err != nil {
		return fmt.Errorf("terminate daemon lock owner: %w", err)
	}
	return nil
}

// Replace attempts to stop existing lock-owner process, then starts daemon.
func (l *Launcher) Replace(ctx context.Context) error {
	if pid, ok := l.readLockedPID(); ok {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && l.Logger != nil {
			l.Logger.Warn("failed to terminate existing daemon process before replace",
				"pid", pid,
				"lock_path", l.LockPath,
				"error", err,
			)
		}
	}
	return l.Start(ctx)
}

func (l *Launcher) resolveBinary() string {
	if l.BinPath != "" {
		return l.BinPath
	}
	if env := os.Getenv("AZEDARACH_DAEMON_BIN"); env != "" {
		return env
	}
	candidates := []string{
		filepath.Join(l.RepoDir, "bin", "azd"),
		// Support monorepo root launcher repo dir with go-bubbletea-local binaries.
		filepath.Join(l.RepoDir, "go-bubbletea", "bin", "azd"),
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		candidates = append(candidates, filepath.Join(cwd, "bin", "azd"))
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "azd"
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
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

func (l *Launcher) acquireStartLock(ctx context.Context) (func(), error) {
	startLockPath := l.LockPath + ".start"
	if err := os.MkdirAll(filepath.Dir(startLockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create start lock dir: %w", err)
	}
	f, err := os.OpenFile(startLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open start lock: %w", err)
	}
	for {
		if lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		} else if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("acquire start lock: %w", lockErr)
		}

		if ctx.Err() != nil {
			_ = f.Close()
			return nil, fmt.Errorf("acquire start lock: %w", ctx.Err())
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
	if strings.TrimSpace(socketPath) == "" {
		return nil
	}
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
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
