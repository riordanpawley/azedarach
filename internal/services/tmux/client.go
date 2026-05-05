package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Client wraps tmux CLI for session management operations
type Client struct {
	runner CommandRunner
	logger *slog.Logger
}

// SessionInfo captures tmux session identity plus creation time.
type SessionInfo struct {
	Name      string
	CreatedAt *time.Time
}

// NewClient creates a new tmux client with dependency injection
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
	return &Client{
		runner: runner,
		logger: logger,
	}
}

// NewSession creates a new tmux session with the given name and working directory
// Uses: tmux new-session -d -s <name> -c <workdir>
func (c *Client) NewSession(ctx context.Context, name string, workdir string) error {
	c.logger.Debug("creating tmux session", "name", name, "workdir", workdir)

	args := []string{"new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}

	_, err := c.runner.Run(ctx, args...)
	if err != nil {
		return &domain.TmuxError{Op: "new-session", Session: name, Err: err}
	}

	c.logger.Debug("tmux session created", "name", name)
	return nil
}

// EnsureWindow creates a named window in an existing session when it is absent.
// It returns true when the window already existed.
func (c *Client) EnsureWindow(ctx context.Context, sessionName, windowName, workdir string) (bool, error) {
	c.logger.Debug("ensuring tmux window", "session", sessionName, "window", windowName, "workdir", workdir)

	out, err := c.runner.Run(ctx, "list-windows", "-t", sessionName, "-F", "#{window_name}")
	if err != nil {
		return false, &domain.TmuxError{Op: "list-windows", Session: sessionName, Err: err}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == windowName {
			c.logger.Debug("tmux window exists", "session", sessionName, "window", windowName)
			return true, nil
		}
	}

	args := []string{"new-window", "-d", "-t", sessionName, "-n", windowName}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	if _, err := c.runner.Run(ctx, args...); err != nil {
		return false, &domain.TmuxError{Op: "new-window", Session: sessionName, Err: err}
	}

	c.logger.Debug("tmux window created", "session", sessionName, "window", windowName)
	return false, nil
}

// HasSession checks if a tmux session with the given name exists
// Uses: tmux has-session -t <name>
func (c *Client) HasSession(ctx context.Context, name string) (bool, error) {
	c.logger.Debug("checking tmux session", "name", name)

	_, err := c.runner.Run(ctx, "has-session", "-t", name)
	if err != nil {
		// tmux has-session exits with non-zero if session doesn't exist
		// This is expected, not an error
		c.logger.Debug("tmux session not found", "name", name)
		return false, nil
	}

	c.logger.Debug("tmux session exists", "name", name)
	return true, nil
}

// AttachSession attaches to an existing tmux session
// Note: This is a blocking operation meant to be used with exec.Cmd
// Uses: tmux attach-session -t <name>
func (c *Client) AttachSession(ctx context.Context, name string) error {
	c.logger.Debug("attaching to tmux session", "name", name)

	_, err := c.runner.Run(ctx, "attach-session", "-t", name)
	if err != nil {
		return &domain.TmuxError{Op: "attach-session", Session: name, Err: err}
	}

	return nil
}

// KillSession terminates a tmux session
// Uses: tmux kill-session -t <name>
func (c *Client) KillSession(ctx context.Context, name string) error {
	c.logger.Debug("killing tmux session", "name", name)

	_, err := c.runner.Run(ctx, "kill-session", "-t", name)
	if err != nil {
		return &domain.TmuxError{Op: "kill-session", Session: name, Err: err}
	}

	c.logger.Debug("tmux session killed", "name", name)
	return nil
}

// SendKeys sends keystrokes to a tmux session
// Uses: tmux send-keys -t <name> <keys> C-m
func (c *Client) SendKeys(ctx context.Context, name string, keys string) error {
	c.logger.Debug("sending keys to tmux session", "name", name, "keys", keys)

	_, err := c.runner.Run(ctx, "send-keys", "-t", name, keys, "C-m")
	if err != nil {
		return &domain.TmuxError{Op: "send-keys", Session: name, Err: err}
	}

	c.logger.Debug("keys sent to tmux session", "name", name)
	return nil
}

// CapturePane captures the last N lines from a tmux session's pane
// Uses: tmux capture-pane -t <name> -p -S -<lines>
func (c *Client) CapturePane(ctx context.Context, name string, lines int) (string, error) {
	c.logger.Debug("capturing tmux pane", "name", name, "lines", lines)

	start := fmt.Sprintf("-%d", lines)
	out, err := c.runner.Run(ctx, "capture-pane", "-t", name, "-p", "-S", start)
	if err != nil {
		return "", &domain.TmuxError{Op: "capture-pane", Session: name, Err: err}
	}

	c.logger.Debug("tmux pane captured", "name", name, "bytes", len(out))
	return out, nil
}

// ListSessions returns a list of all tmux session names
// Uses: tmux list-sessions -F "#{session_name}"
func (c *Client) ListSessions(ctx context.Context) ([]string, error) {
	infos, err := c.ListSessionInfos(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]string, 0, len(infos))
	for _, info := range infos {
		sessions = append(sessions, info.Name)
	}
	return sessions, nil
}

// ListSessionInfos returns tmux sessions with creation timestamps.
// Uses: tmux list-sessions -F "#{session_name}\t#{session_created}"
func (c *Client) ListSessionInfos(ctx context.Context) ([]SessionInfo, error) {
	c.logger.Debug("listing tmux sessions")

	out, err := c.runner.Run(ctx, "list-sessions", "-F", "#{session_name}\t#{session_created}")
	if err != nil {
		// If no sessions exist, tmux returns an error
		// Return empty list instead
		c.logger.Debug("no tmux sessions found")
		return []SessionInfo{}, nil
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return []SessionInfo{}, nil
	}

	sessions := make([]SessionInfo, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		info := SessionInfo{Name: name}
		if len(parts) == 2 {
			createdRaw := strings.TrimSpace(parts[1])
			if createdRaw != "" {
				if sec, parseErr := strconv.ParseInt(createdRaw, 10, 64); parseErr == nil && sec > 0 {
					createdAt := time.Unix(sec, 0).UTC()
					info.CreatedAt = &createdAt
				}
			}
		}
		sessions = append(sessions, info)
	}

	c.logger.Debug("tmux sessions listed", "count", len(sessions))
	return sessions, nil
}

// SetEnvironment sets an environment variable in a tmux session
// Uses: tmux set-environment -t <name> <key> <value>
func (c *Client) SetEnvironment(ctx context.Context, name, key, value string) error {
	c.logger.Debug("setting tmux environment variable", "name", name, "key", key)

	_, err := c.runner.Run(ctx, "set-environment", "-t", name, key, value)
	if err != nil {
		return &domain.TmuxError{Op: "set-environment", Session: name, Err: err}
	}

	c.logger.Debug("tmux environment variable set", "name", name, "key", key)
	return nil
}

func (c *Client) SwitchClient(ctx context.Context, name string) error {
	c.logger.Debug("switching tmux client", "name", name)

	_, err := c.runner.Run(ctx, "switch-client", "-t", name)
	if err != nil {
		return &domain.TmuxError{Op: "switch-client", Session: name, Err: err}
	}

	return nil
}

func (c *Client) DisplayPopup(ctx context.Context, title, width, height, command string) error {
	c.logger.Debug("opening tmux popup", "title", title)

	args := []string{"display-popup", "-E", "-w", width, "-h", height}
	if strings.TrimSpace(title) != "" {
		args = append(args, "-T", title)
	}
	args = append(args, command)

	_, err := c.runner.Run(ctx, args...)
	if err != nil {
		return &domain.TmuxError{Op: "display-popup", Err: err}
	}

	return nil
}
