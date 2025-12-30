package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Client wraps tmux CLI for session management operations
type Client struct {
	runner CommandRunner
	logger *slog.Logger
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
	c.logger.Debug("listing tmux sessions")

	out, err := c.runner.Run(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		// If no sessions exist, tmux returns an error
		// Return empty list instead
		c.logger.Debug("no tmux sessions found")
		return []string{}, nil
	}

	sessions := strings.Split(strings.TrimSpace(out), "\n")
	if len(sessions) == 1 && sessions[0] == "" {
		return []string{}, nil
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
