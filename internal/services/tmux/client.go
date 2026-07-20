package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
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
	SessionName    string
	PaneID         string
	PanePID        int
	CurrentCommand string
}

// AttachedClientInfo captures the mutable input flag for one attached tmux
// client. ClientName is tmux's stable target for refresh-client while the
// client remains attached.
type AttachedClientInfo struct {
	ClientName  string
	SessionName string
	Flags       string
	ReadOnly    bool
}

// NewClient creates a new tmux client with dependency injection
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
	return &Client{
		runner: runner,
		logger: logger,
	}
}

// ListAttachedClients returns clients currently attached to one exact session.
// An empty session returns all attached clients so durable gate recovery can
// restore a recorded client even after it switches to another session.
func (c *Client) ListAttachedClients(ctx context.Context, session string) ([]AttachedClientInfo, error) {
	out, err := c.runner.Run(ctx, "list-clients", "-F", "#{client_name}\t#{client_session}\t#{client_flags}\t#{client_readonly}")
	if err != nil {
		return nil, &domain.TmuxError{Op: "list-clients", Session: session, Err: err}
	}
	clients := make([]AttachedClientInfo, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || (session != "" && fields[1] != session) || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		clients = append(clients, AttachedClientInfo{ClientName: fields[0], SessionName: fields[1], Flags: fields[2], ReadOnly: fields[3] == "1"})
	}
	return clients, nil
}

// SetClientReadOnly changes only the read-only flag, preserving every other
// client flag. A leading ! is tmux's attached-client flag removal syntax.
func (c *Client) SetClientReadOnly(ctx context.Context, clientName string, readOnly bool) error {
	flag := "read-only"
	if !readOnly {
		flag = "!read-only"
	}
	if _, err := c.runner.Run(ctx, "refresh-client", "-t", clientName, "-f", flag); err != nil {
		return &domain.TmuxError{Op: "refresh-client", Session: clientName, Err: err}
	}
	return nil
}

// SetPaneInputEnabled changes tmux's pane-wide input fence. Automated input
// keeps this native fence closed from gate acquisition through submission and
// restoration; asynchronous client hooks are not an input-exclusion boundary.
func (c *Client) SetPaneInputEnabled(ctx context.Context, paneID string, enabled bool) error {
	target := canonicalPaneTarget(paneID)
	flag := "-d"
	if enabled {
		flag = "-e"
	}
	if _, err := c.runner.Run(ctx, "select-pane", flag, "-t", target); err != nil {
		return &domain.TmuxError{Op: "select-pane", Session: paneID, Err: err}
	}
	return nil
}

// DisablePaneInputIfIdentity compares the exact tmux-owned pane identity and
// disables input in the same server command queue item. This prevents a
// respawned pane from being fenced after a stale client-side identity check.
func (c *Client) DisablePaneInputIfIdentity(ctx context.Context, session, paneID string, panePID int) (bool, error) {
	normalizedPane, err := normalizeInputGatePaneID(paneID)
	if err != nil {
		return false, err
	}
	if !isSafeTmuxFormatLiteral(session) || panePID <= 0 {
		return false, fmt.Errorf("invalid managed pane identity")
	}
	const disabled = "az-input-gate-disabled"
	const mismatch = "az-input-gate-identity-mismatch"
	predicate := fmt.Sprintf("#{&&:#{==:#{session_name},%s},#{==:#{pane_id},%s},#{==:#{pane_pid},%d}}", session, normalizedPane, panePID)
	onMatch := fmt.Sprintf("select-pane -d -t %s ; display-message -p %s", normalizedPane, disabled)
	out, err := c.runner.Run(ctx, "if-shell", "-F", "-t", normalizedPane, predicate, onMatch, "display-message -p "+mismatch)
	if err != nil {
		return false, &domain.TmuxError{Op: "compare-and-disable-pane", Session: session, Err: err}
	}
	switch strings.TrimSpace(out) {
	case disabled:
		return true, nil
	case mismatch:
		return false, nil
	default:
		return false, &domain.TmuxError{Op: "compare-and-disable-pane", Session: session, Err: fmt.Errorf("unexpected result %q", strings.TrimSpace(out))}
	}
}

func normalizeInputGatePaneID(paneID string) (string, error) {
	value := strings.TrimPrefix(strings.TrimSpace(paneID), "%")
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || strconv.Itoa(n) != value {
		return "", fmt.Errorf("invalid managed pane id %q", paneID)
	}
	return "%" + value, nil
}

func isSafeTmuxFormatLiteral(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.:", r) {
			return false
		}
	}
	return true
}

// PaneInputEnabled reports the current pane-wide input fence.
func (c *Client) PaneInputEnabled(ctx context.Context, paneID string) (bool, error) {
	out, err := c.runner.Run(ctx, "display-message", "-p", "-t", canonicalPaneTarget(paneID), "#{pane_input_off}")
	if err != nil {
		return false, &domain.TmuxError{Op: "display-message", Session: paneID, Err: err}
	}
	switch strings.TrimSpace(out) {
	case "0":
		return true, nil
	case "1":
		return false, nil
	default:
		return false, &domain.TmuxError{Op: "display-message", Session: paneID, Err: fmt.Errorf("unexpected pane_input_off value %q", strings.TrimSpace(out))}
	}
}

func canonicalPaneTarget(paneID string) string {
	trimmed := strings.TrimSpace(paneID)
	value := strings.TrimPrefix(trimmed, "%")
	if n, err := strconv.Atoi(value); err == nil && n >= 0 && strconv.Itoa(n) == value {
		return "%" + value
	}
	return trimmed
}

// SetSessionReadOnlyAttachHooks installs or removes session-scoped hooks that
// eventually make newly attached or switched-in clients read-only and record
// their prior read-only flag for exact restoration. Callers must hold the
// pane-wide input fence because run-shell hooks do not preempt client input.
func (c *Client) SetSessionReadOnlyAttachHooks(ctx context.Context, session, hookID, recordPath string, enabled bool) error {
	lock := sessionReadOnlyAttachHookLock(hookID, recordPath)
	gateOption := sessionReadOnlyAttachHookGateOption(hookID, recordPath)
	gateValue := "1"
	if !enabled {
		// Close the generation before removing either hook. A hook already
		// dispatched on another tmux command queue may acquire the shared lock
		// after restoration does; it must then observe closed and skip mutation.
		gateValue = "0"
	}
	if _, err := c.runner.Run(ctx, "set-option", "-gq", gateOption, gateValue); err != nil {
		return &domain.TmuxError{Op: "set-option", Session: session, Err: err}
	}
	for _, hook := range []string{"client-attached", "client-session-changed"} {
		name := hook + "[" + hookID + "]"
		args := []string{"set-hook", "-t", session}
		if !enabled {
			args = append(args, "-u", name)
		} else {
			release := "tmux wait-for -U " + lock
			record := "umask 077; tmux wait-for -L " + lock + " || exit 1; trap " + shellSingleQuote(release) + " EXIT; trap 'exit 1' HUP INT TERM; [ \"$(tmux show-options -gqv " + gateOption + ")\" = 1 ] || exit 0; __az_client=#{q:hook_client}; printf '%s\\t%s\\n' \"$__az_client\" #{client_readonly} >> " + shellSingleQuote(recordPath) + "; tmux refresh-client -t \"$__az_client\" -f read-only; printf '\\tcomplete\\n' >> " + shellSingleQuote(recordPath)
			// The hook shell owns the tmux-server lock across its final generation
			// check and mutation. Restoration closes the generation first, then
			// acquires the same lock: earlier mutations complete before restoration,
			// while later dispatched shells observe closed and cannot mutate.
			command := "run-shell " + tmuxDoubleQuote(record)
			args = append(args, name, command)
		}
		if _, err := c.runner.Run(ctx, args...); err != nil {
			return &domain.TmuxError{Op: "set-hook", Session: session, Err: err}
		}
	}
	return nil
}

// LockSessionReadOnlyAttachHooks acquires or releases the exact gate's
// tmux-server lock. Callers remove the hooks before acquiring it and retain it
// through ledger merge and client restoration, so a dispatched hook cannot
// apply read-only after restoration completes.
func (c *Client) LockSessionReadOnlyAttachHooks(ctx context.Context, hookID, recordPath string, locked bool) error {
	flag := "-L"
	if !locked {
		flag = "-U"
	}
	if _, err := c.runner.Run(ctx, "wait-for", flag, sessionReadOnlyAttachHookLock(hookID, recordPath)); err != nil {
		return &domain.TmuxError{Op: "wait-for", Session: hookID, Err: err}
	}
	if !locked {
		if _, err := c.runner.Run(ctx, "set-option", "-gu", sessionReadOnlyAttachHookGateOption(hookID, recordPath)); err != nil {
			return &domain.TmuxError{Op: "set-option", Session: hookID, Err: err}
		}
	}
	return nil
}

func sessionReadOnlyAttachHookLock(hookID, recordPath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(hookID) + "\x00" + strings.TrimSpace(recordPath)))
	return "az-codex-input-gate-" + hex.EncodeToString(sum[:12])
}

func sessionReadOnlyAttachHookGateOption(hookID, recordPath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(hookID) + "\x00" + strings.TrimSpace(recordPath)))
	return "@az_codex_input_gate_" + hex.EncodeToString(sum[:12])
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func tmuxDoubleQuote(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$")
	return "\"" + replacer.Replace(value) + "\""
}

// NewSession creates a new tmux session with the given name and working directory
// Uses: tmux new-session -d -s <name> -c <workdir>
func (c *Client) NewSession(ctx context.Context, name string, workdir string) error {
	return c.NewSessionWithEnvironment(ctx, name, workdir, nil)
}

// NewSessionWithEnvironment installs environment before the initial shell starts.
func (c *Client) NewSessionWithEnvironment(ctx context.Context, name string, workdir string, environment map[string]string) error {
	c.logger.Debug("creating tmux session", "name", name, "workdir", workdir)

	args := []string{"new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	args = appendTmuxEnvironmentArgs(args, environment)

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
	return c.NewSessionWithCommandAndEnvironment(ctx, name, workdir, command, nil)
}

// NewSessionWithCommandAndEnvironment installs environment before command starts.
func (c *Client) NewSessionWithCommandAndEnvironment(ctx context.Context, name, workdir, command string, environment map[string]string) error {
	c.logger.Debug("creating tmux session with command", "name", name, "workdir", workdir, "command", command)

	args := []string{"new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	args = appendTmuxEnvironmentArgs(args, environment)
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

// NewSessionWithArgs creates a detached tmux session by passing an executable
// and its arguments separately. tmux executes a multi-argument command
// directly instead of routing a single shell-command string through the
// configured default shell.
func (c *Client) NewSessionWithArgs(ctx context.Context, name, workdir, executable string, commandArgs ...string) error {
	return c.NewSessionWithArgsAndEnvironment(ctx, name, workdir, executable, nil, commandArgs...)
}

// NewSessionWithArgsAndEnvironment installs environment before directly executing executable.
func (c *Client) NewSessionWithArgsAndEnvironment(ctx context.Context, name, workdir, executable string, environment map[string]string, commandArgs ...string) error {
	c.logger.Debug("creating tmux session with argv", "name", name, "workdir", workdir, "executable", executable)
	if strings.TrimSpace(executable) == "" || len(commandArgs) == 0 {
		return &domain.TmuxError{Op: "new-session", Session: name, Err: errors.New("direct command requires an executable and at least one argument")}
	}

	args := []string{"new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	args = appendTmuxEnvironmentArgs(args, environment)
	args = append(args, "--", executable)
	args = append(args, commandArgs...)

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
	return c.ensureWindow(ctx, sessionName, windowName, workdir, "", nil)
}

// EnsureWindowWithCommand creates a named window running command, or replaces
// the existing window's panes with command. Passing the command to tmux avoids
// racing an interactive shell's startup before sending the first line.
func (c *Client) EnsureWindowWithCommand(ctx context.Context, sessionName, windowName, workdir, command string) (bool, error) {
	return c.EnsureWindowWithCommandAndEnvironment(ctx, sessionName, windowName, workdir, command, nil)
}

// EnsureWindowWithCommandAndEnvironment installs environment before creating or respawning command.
func (c *Client) EnsureWindowWithCommandAndEnvironment(ctx context.Context, sessionName, windowName, workdir, command string, environment map[string]string) (bool, error) {
	return c.ensureWindow(ctx, sessionName, windowName, workdir, command, environment)
}

// HasWindow reports whether one exact named window currently exists.
func (c *Client) HasWindow(ctx context.Context, sessionName, windowName string) (bool, error) {
	if strings.TrimSpace(sessionName) == "" || strings.TrimSpace(windowName) == "" {
		return false, &domain.TmuxError{Op: "list-windows", Session: sessionName, Err: errors.New("session and window are required")}
	}
	out, err := c.runner.Run(ctx, "list-windows", "-t", sessionName, "-F", "#{window_name}")
	if err != nil {
		return false, &domain.TmuxError{Op: "list-windows", Session: sessionName, Err: err}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == strings.TrimSpace(windowName) {
			return true, nil
		}
	}
	return false, nil
}

// KillWindow removes one exact window without terminating unrelated panes in the session.
func (c *Client) KillWindow(ctx context.Context, sessionName, windowName string) error {
	target := strings.TrimSpace(sessionName) + ":" + strings.TrimSpace(windowName)
	if strings.TrimSpace(sessionName) == "" || strings.TrimSpace(windowName) == "" {
		return &domain.TmuxError{Op: "kill-window", Session: sessionName, Err: errors.New("session and window are required")}
	}
	if _, err := c.runner.Run(ctx, "kill-window", "-t", target); err != nil {
		return &domain.TmuxError{Op: "kill-window", Session: sessionName, Err: err}
	}
	return nil
}

// RespawnPane replaces exactly one pane process while preserving its session,
// window, layout, and every unrelated pane. paneID is tmux's stable pane target
// (for example %12), never a window or session name.
func (c *Client) RespawnPane(ctx context.Context, paneID, workdir, command string) error {
	return c.RespawnPaneWithEnvironment(ctx, paneID, workdir, command, nil)
}

// RespawnPaneWithEnvironment replaces exactly one pane process and installs
// environment only on that replacement process.
func (c *Client) RespawnPaneWithEnvironment(ctx context.Context, paneID, workdir, command string, environment map[string]string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" || strings.TrimSpace(command) == "" {
		return &domain.TmuxError{Op: "respawn-pane", Session: paneID, Err: errors.New("pane target and command are required")}
	}
	args := []string{"respawn-pane", "-k", "-t", paneID}
	if strings.TrimSpace(workdir) != "" {
		args = append(args, "-c", workdir)
	}
	args = appendTmuxEnvironmentArgs(args, environment)
	args = append(args, command)
	if _, err := c.runner.Run(ctx, args...); err != nil {
		return &domain.TmuxError{Op: "respawn-pane", Session: paneID, Err: err}
	}
	return nil
}

func (c *Client) ensureWindow(ctx context.Context, sessionName, windowName, workdir, command string, environment map[string]string) (bool, error) {
	c.logger.Debug("ensuring tmux window", "session", sessionName, "window", windowName, "workdir", workdir)

	out, err := c.runner.Run(ctx, "list-windows", "-t", sessionName, "-F", "#{window_name}")
	if err != nil {
		return false, &domain.TmuxError{Op: "list-windows", Session: sessionName, Err: err}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == windowName {
			if strings.TrimSpace(command) != "" {
				args := []string{"respawn-window", "-k", "-t", sessionName + ":" + windowName}
				if workdir != "" {
					args = append(args, "-c", workdir)
				}
				args = appendTmuxEnvironmentArgs(args, environment)
				args = append(args, command)
				if _, err := c.runner.Run(ctx, args...); err != nil {
					return true, &domain.TmuxError{Op: "respawn-window", Session: sessionName, Err: err}
				}
			}
			c.logger.Debug("tmux window exists", "session", sessionName, "window", windowName)
			return true, nil
		}
	}

	args := []string{"new-window", "-d", "-t", sessionName, "-n", windowName}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	args = appendTmuxEnvironmentArgs(args, environment)
	if strings.TrimSpace(command) != "" {
		args = append(args, command)
	}
	if _, err := c.runner.Run(ctx, args...); err != nil {
		return false, &domain.TmuxError{Op: "new-window", Session: sessionName, Err: err}
	}

	c.logger.Debug("tmux window created", "session", sessionName, "window", windowName)
	return false, nil
}

func appendTmuxEnvironmentArgs(args []string, environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		if key != "" && key == strings.TrimSpace(key) && !strings.Contains(key, "=") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "-e", key+"="+environment[key])
	}
	return args
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
	inputRunner, ok := c.runner.(InputCommandRunner)
	if !ok {
		return &domain.TmuxError{Op: "load-buffer", Session: name, Err: errors.New("tmux runner does not support stdin payloads")}
	}
	if _, err := inputRunner.RunWithInput(ctx, text, "load-buffer", "-b", bufferName, "-"); err != nil {
		return &domain.TmuxError{Op: "load-buffer", Session: name, Err: err}
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
// Uses: tmux list-panes -a -F "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}"
func (c *Client) ListPaneInfos(ctx context.Context) ([]PaneInfo, error) {
	c.logger.Debug("listing tmux panes")

	out, err := c.runner.Run(ctx, "list-panes", "-a", "-F", "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}")
	return c.parsePaneInfos(out, err)
}

// ListPaneInfosForSession returns panes owned by one exact tmux session. Hot
// bootstrap paths use this targeted probe so their cost and availability do
// not depend on the number of unrelated repository-family sessions.
func (c *Client) ListPaneInfosForSession(ctx context.Context, session string) ([]PaneInfo, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return nil, fmt.Errorf("tmux session is required")
	}
	c.logger.Debug("listing tmux panes for session", "session", session)
	out, err := c.runner.Run(ctx, "list-panes", "-s", "-t", session, "-F", "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}")
	if isTmuxTargetMissingError(err) {
		return []PaneInfo{}, nil
	}
	return c.parsePaneInfos(out, err)
}

func (c *Client) parsePaneInfos(out string, err error) ([]PaneInfo, error) {
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
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 2 {
			continue
		}
		sessionName := strings.TrimSpace(parts[0])
		paneID := sanitizePaneID(parts[1])
		panePID := 0
		if len(parts) == 3 {
			parsedPID, err := strconv.Atoi(strings.TrimSpace(parts[2]))
			if err != nil || parsedPID <= 0 {
				continue
			}
			panePID = parsedPID
		}
		currentCommand := ""
		if len(parts) == 4 {
			parsedPID, err := strconv.Atoi(strings.TrimSpace(parts[2]))
			if err != nil || parsedPID <= 0 {
				continue
			}
			panePID = parsedPID
			currentCommand = strings.TrimSpace(parts[3])
		}
		if sessionName == "" || paneID == "" {
			continue
		}
		panes = append(panes, PaneInfo{SessionName: sessionName, PaneID: paneID, PanePID: panePID, CurrentCommand: currentCommand})
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

func isTmuxTargetMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "can't find session") || strings.Contains(msg, "can't find pane")
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

// EnvironmentValue returns one tmux session-scoped environment value.
// Listing the complete session environment distinguishes an absent variable
// from a missing session without placing the requested value in argv.
func (c *Client) EnvironmentValue(ctx context.Context, name, key string) (string, bool, error) {
	c.logger.Debug("getting tmux environment variable", "name", name, "key", key)

	out, err := c.runner.Run(ctx, "show-environment", "-t", name)
	if err != nil {
		return "", false, &domain.TmuxError{Op: "show-environment", Session: name, Err: err}
	}
	prefix := key + "="
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true, nil
		}
		if line == "-"+key {
			return "", false, nil
		}
	}
	return "", false, nil
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
