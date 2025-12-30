package tmux

import (
	"context"
	"os/exec"
	"time"
)

// CommandRunner abstracts command execution for testing
type CommandRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ExecRunner runs real tmux commands using os/exec
type ExecRunner struct{}

// Run executes a tmux command with a 5-second timeout
func (r *ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	// Add timeout to context if not already present
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.Output()
	return string(out), err
}
