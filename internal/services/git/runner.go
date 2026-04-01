package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommandRunner executes git commands and returns their output.
type CommandRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ExecRunner implements CommandRunner using os/exec.
type ExecRunner struct {
	workDir string // Working directory for git commands
}

// NewExecRunner creates a new ExecRunner that runs commands in the given working directory.
func NewExecRunner(workDir string) *ExecRunner {
	return &ExecRunner{
		workDir: workDir,
	}
}

// Run executes a git command with the given arguments.
func (e *ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = e.workDir
	cmd.Env = sanitizedGitEnv(os.Environ())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

func sanitizedGitEnv(env []string) []string {
	if len(env) == 0 {
		return env
	}
	blocked := map[string]struct{}{
		"GIT_DIR":        {},
		"GIT_WORK_TREE":  {},
		"GIT_INDEX_FILE": {},
		"GIT_COMMON_DIR": {},
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			filtered = append(filtered, entry)
			continue
		}
		if _, shouldDrop := blocked[key]; shouldDrop {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
