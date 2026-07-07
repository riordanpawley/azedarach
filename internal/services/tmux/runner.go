package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandRunner abstracts command execution for testing
type CommandRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// InputCommandRunner executes commands that need stdin payloads.
type InputCommandRunner interface {
	RunWithInput(ctx context.Context, input string, args ...string) (string, error)
}

// ExecRunner runs real tmux commands using os/exec
type ExecRunner struct{}

// Run executes a tmux command with a 5-second timeout
func (r *ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	if refuseUnsafeTmuxCommandFromGoTest(args) {
		return "", fmt.Errorf("refusing to run tmux command %q from go test; use a fake tmux runner or set AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS=1", args[0])
	}

	// Add timeout to context if not already present
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), err
}

// RunWithInput executes a tmux command with a 5-second timeout and stdin.
func (r *ExecRunner) RunWithInput(ctx context.Context, input string, args ...string) (string, error) {
	if refuseUnsafeTmuxCommandFromGoTest(args) {
		return "", fmt.Errorf("refusing to run tmux command %q from go test; use a fake tmux runner or set AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS=1", args[0])
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), err
}

func refuseUnsafeTmuxCommandFromGoTest(args []string) bool {
	if len(args) == 0 || !shouldRefuseUnsafeTmuxCommands() {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS")), "1") {
		return false
	}
	switch strings.TrimSpace(args[0]) {
	case "capture-pane",
		"display-message",
		"has-session",
		"list-panes",
		"list-sessions",
		"list-windows":
		return false
	default:
		return true
	}
}

func runningInGoTestBinary() bool {
	name := filepath.Base(os.Args[0])
	return strings.HasSuffix(name, ".test")
}

func shouldRefuseUnsafeTmuxCommands() bool {
	if runningInGoTestBinary() {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("AZEDARACH_REFUSE_REAL_TMUX_MUTATION")), "1")
}
