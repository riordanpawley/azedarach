package tmux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/riordanpawley/azedarach/internal/domain"
)

// Client wraps tmux CLI for session management operations
type Client struct {
	runner CommandRunner
	logger *slog.Logger
}

var pasteSubmitDelay = 150 * time.Millisecond

// SessionInfo captures tmux session identity plus tmux timestamps.
type SessionInfo struct {
	Name           string
	CreatedAt      *time.Time
	LastAttachedAt *time.Time
	Path           string
	AttachedCount  int
}

// PaneInfo captures the tmux session and pane identity for liveness checks.
type PaneInfo struct {
	SessionName string
	PaneID      string
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

// NewSessionWithCommand creates a detached tmux session running command.
// Uses: tmux new-session -d -s <name> -c <workdir> <command>
func (c *Client) NewSessionWithCommand(ctx context.Context, name, workdir, command string) error {
	c.logger.Debug("creating tmux session with command", "name", name, "workdir", workdir, "command", command)

	args := []string{"new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	if strings.TrimSpace(command) != "" {
		args = append(args, command)
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

// PasteTextAndSubmit pastes literal text into a pane, then submits it.
//
// This is intentionally separate from SendKeys. Session messages are often
// multiline prompts for AI TUIs, and sending them as key arguments can let the
// text payload consume or reinterpret the trailing submit key.
func (c *Client) PasteTextAndSubmit(ctx context.Context, name string, text string) error {
	c.logger.Debug("pasting text to tmux session", "name", name, "bytes", len(text))

	bufferName := "azedarach-message-" + safeTmuxBufferSuffix(name)
	if inputRunner, ok := c.runner.(InputCommandRunner); ok {
		if _, err := inputRunner.RunWithInput(ctx, text, "load-buffer", "-b", bufferName, "-"); err != nil {
			return &domain.TmuxError{Op: "load-buffer", Session: name, Err: err}
		}
	} else if _, err := c.runner.Run(ctx, "set-buffer", "-b", bufferName, text); err != nil {
		return &domain.TmuxError{Op: "set-buffer", Session: name, Err: err}
	}
	if _, err := c.runner.Run(ctx, "paste-buffer", "-dp", "-b", bufferName, "-t", name); err != nil {
		return &domain.TmuxError{Op: "paste-buffer", Session: name, Err: err}
	}
	if err := sleepWithContext(ctx, pasteSubmitDelay); err != nil {
		return &domain.TmuxError{Op: "send-keys", Session: name, Err: err}
	}
	if _, err := c.runner.Run(ctx, "send-keys", "-t", name, "Enter"); err != nil {
		return &domain.TmuxError{Op: "send-keys", Session: name, Err: err}
	}

	c.logger.Debug("text pasted and submitted to tmux session", "name", name)
	return nil
}

func safeTmuxBufferSuffix(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SendKey sends a single tmux key token without appending Enter.
func (c *Client) SendKey(ctx context.Context, name string, key string) error {
	c.logger.Debug("sending key to tmux session", "name", name, "key", key)

	_, err := c.runner.Run(ctx, "send-keys", "-t", name, key)
	if err != nil {
		return &domain.TmuxError{Op: "send-keys", Session: name, Err: err}
	}

	c.logger.Debug("key sent to tmux session", "name", name)
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
	out = tailPaneLines(out, lines)

	c.logger.Debug("tmux pane captured", "name", name, "bytes", len(out))
	return out, nil
}

// tailPaneLines enforces CapturePane's line-count contract. tmux interprets
// -S -N as N history lines above the visible pane, so its raw output can
// contain more than N lines once the visible pane is included.
func tailPaneLines(output string, limit int) string {
	if output == "" || limit <= 0 {
		return output
	}
	hasTrailingNewline := strings.HasSuffix(output, "\n")
	logicalLines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(logicalLines) <= limit {
		return output
	}
	output = strings.Join(logicalLines[len(logicalLines)-limit:], "\n")
	if hasTrailingNewline {
		output += "\n"
	}
	return output
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

// ListPaneInfos returns all tmux panes with their owning session name.
// Uses: tmux list-panes -a -F "#{session_name}\t#{pane_id}"
func (c *Client) ListPaneInfos(ctx context.Context) ([]PaneInfo, error) {
	c.logger.Debug("listing tmux panes")

	out, err := c.runner.Run(ctx, "list-panes", "-a", "-F", "#{session_name}\t#{pane_id}")
	if err != nil {
		if isNoTmuxSessionsError(err) {
			c.logger.Debug("no tmux panes found")
			return []PaneInfo{}, nil
		}
		return nil, &domain.TmuxError{Op: "list-panes", Err: err}
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return []PaneInfo{}, nil
	}

	panes := make([]PaneInfo, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		sessionName := strings.TrimSpace(parts[0])
		paneID := sanitizePaneID(parts[1])
		if sessionName == "" || paneID == "" {
			continue
		}
		panes = append(panes, PaneInfo{SessionName: sessionName, PaneID: paneID})
	}

	c.logger.Debug("tmux panes listed", "count", len(panes))
	return panes, nil
}

// ListSessionInfos returns tmux sessions with timestamps and client attachment count.
// Uses: tmux list-sessions -F "#{session_name}\t#{session_created}\t#{session_last_attached}\t#{session_path}\t#{session_attached}"
func (c *Client) ListSessionInfos(ctx context.Context) ([]SessionInfo, error) {
	c.logger.Debug("listing tmux sessions")

	out, err := c.runner.Run(ctx, "list-sessions", "-F", "#{session_name}\t#{session_created}\t#{session_last_attached}\t#{session_path}\t#{session_attached}")
	if err != nil {
		if isNoTmuxSessionsError(err) {
			c.logger.Debug("no tmux sessions found")
			return []SessionInfo{}, nil
		}
		return nil, &domain.TmuxError{Op: "list-sessions", Err: err}
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
		parts := strings.SplitN(line, "\t", 5)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		info := SessionInfo{Name: name}
		if len(parts) >= 2 {
			info.CreatedAt = parseTmuxUnixTime(parts[1])
		}
		if len(parts) >= 3 {
			info.LastAttachedAt = parseTmuxUnixTime(parts[2])
		}
		if len(parts) == 4 {
			info.Path = strings.TrimSpace(parts[3])
		}
		if len(parts) == 5 {
			info.Path = strings.TrimSpace(parts[3])
			info.AttachedCount = parseTmuxInt(parts[4])
		}
		sessions = append(sessions, info)
	}

	c.logger.Debug("tmux sessions listed", "count", len(sessions))
	return sessions, nil
}

func sanitizePaneID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-_.")
}

func isNoTmuxSessionsError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.ToLower(strings.TrimSpace(string(exitErr.Stderr)))
		if strings.Contains(stderr, "no server running") || strings.Contains(stderr, "no sessions") || strings.Contains(stderr, "no tmux sessions") {
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "no server running") || strings.Contains(msg, "no sessions") || strings.Contains(msg, "no tmux sessions")
}

func parseTmuxInt(raw string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func parseTmuxUnixTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || sec <= 0 {
		return nil
	}
	parsed := time.Unix(sec, 0).UTC()
	return &parsed
}

func (c *Client) CurrentSession(ctx context.Context) (string, error) {
	c.logger.Debug("getting current tmux session")

	out, err := c.runner.Run(ctx, "display-message", "-p", "#{client_session}")
	if err != nil {
		return "", &domain.TmuxError{Op: "display-message", Err: err}
	}
	return strings.TrimSpace(out), nil
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

func (c *Client) ClosePopup(ctx context.Context) error {
	c.logger.Debug("closing tmux popup")

	_, err := c.runner.Run(ctx, "display-popup", "-C")
	if err != nil {
		return &domain.TmuxError{Op: "display-popup", Err: err}
	}

	return nil
}
