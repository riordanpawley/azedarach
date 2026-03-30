package daemonprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/riordanpawley/azedarach/internal/config"
)

// Launcher starts/replaces the singleton daemon process for a user-global socket.
type Launcher struct {
	RepoDir     string
	SocketPath  string
	LockPath    string
	BinPath     string
	openLogFile func(path string) (io.WriteCloser, error)
}

// NewLauncher returns a daemon process launcher for repoDir.
func NewLauncher(repoDir, socketPath string) *Launcher {
	if normalizedRepoDir, err := config.ResolveProjectRoot(repoDir); err == nil {
		repoDir = normalizedRepoDir
	}
	lockPath := config.GlobalDaemonLockPath()
	return &Launcher{
		RepoDir:     repoDir,
		SocketPath:  socketPath,
		LockPath:    lockPath,
		openLogFile: openDaemonLog,
	}
}

// Start spawns daemon process in background.
func (l *Launcher) Start(ctx context.Context) error {
	_ = ctx
	bin := l.resolveBinary()
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
	cmd := exec.Command(bin, "--repo", l.RepoDir, "--socket", l.SocketPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon %s: %w", bin, err)
	}
	return nil
}

// Replace attempts to stop existing lock-owner process, then starts daemon.
func (l *Launcher) Replace(ctx context.Context) error {
	if pid, ok := l.readLockedPID(); ok {
		_ = syscall.Kill(pid, syscall.SIGTERM)
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
