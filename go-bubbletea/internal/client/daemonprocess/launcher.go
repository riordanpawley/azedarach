package daemonprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Launcher starts/replaces the daemon process for a repo-scoped socket.
type Launcher struct {
	RepoDir    string
	SocketPath string
	LockPath   string
	BinPath    string
}

// NewLauncher returns a daemon process launcher for repoDir.
func NewLauncher(repoDir, socketPath string) *Launcher {
	lockPath := filepath.Join(repoDir, ".beads", "daemon.lock")
	return &Launcher{
		RepoDir:    repoDir,
		SocketPath: socketPath,
		LockPath:   lockPath,
	}
}

// Start spawns daemon process in background.
func (l *Launcher) Start(ctx context.Context) error {
	bin := l.resolveBinary()
	if err := os.MkdirAll(filepath.Join(l.RepoDir, ".beads"), 0o755); err != nil {
		return fmt.Errorf("create .beads dir: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(l.RepoDir, ".beads", "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, "--repo", l.RepoDir, "--socket", l.SocketPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
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
	local := filepath.Join(l.RepoDir, "bin", "azd")
	if _, err := os.Stat(local); err == nil {
		return local
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
