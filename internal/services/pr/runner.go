package pr

import (
	"context"
	"os/exec"
	"time"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
)

// CommandRunner abstracts command execution for testing
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real shell commands using os/exec
type ExecRunner struct {
	Dir string
}

// NewExecRunner returns a command runner scoped to one repository.
func NewExecRunner(dir string) *ExecRunner {
	return &ExecRunner{Dir: dir}
}

// Run executes a command with a 30-second timeout (gh commands can be slow)
func (r *ExecRunner) Run(ctx context.Context, name string, args ...string) (out []byte, err error) {
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "process",
		"dependency.name", name,
		"dependency.operation", firstArg(args),
		"arg_count", len(args),
	)
	defer func() { endSpan(err) }()

	// Add timeout to context if not already present
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.Dir
	return cmd.CombinedOutput()
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
