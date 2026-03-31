package pr

import (
	"context"
	"os/exec"
	"time"
)

// CommandRunner abstracts command execution for testing
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real shell commands using os/exec
type ExecRunner struct{}

// Run executes a command with a 30-second timeout (gh commands can be slow)
func (r *ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	// Add timeout to context if not already present
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}
