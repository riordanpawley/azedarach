package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
)

// CommandRunner executes git commands and returns their output.
type CommandRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type envCommandRunner interface {
	RunWithEnv(ctx context.Context, extraEnv []string, args ...string) (string, error)
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
func (e *ExecRunner) Run(ctx context.Context, args ...string) (out string, err error) {
	return e.run(ctx, nil, args...)
}

func (e *ExecRunner) RunWithEnv(ctx context.Context, extraEnv []string, args ...string) (out string, err error) {
	return e.run(ctx, extraEnv, args...)
}

func (e *ExecRunner) run(ctx context.Context, extraEnv []string, args ...string) (out string, err error) {
	operation := gitOperation(args)
	ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "git",
		"dependency.name", "git",
		"dependency.operation", operation,
		"arg_count", len(args),
	)
	defer func() { endSpan(err) }()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = e.workDir
	cmd.Env = gitEnvWithOverrides(sanitizedGitEnv(os.Environ()), extraEnv)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	stdoutText := strings.TrimSpace(stdout.String())
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		detail := stderrText
		if detail == "" {
			detail = stdoutText
		}
		if detail == "" {
			return stdoutText, fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
		}
		return stdoutText, fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, detail)
	}

	return stdoutText, nil
}

func gitOperation(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		switch arg {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			i++
			continue
		}
		if strings.HasPrefix(arg, "--git-dir=") ||
			strings.HasPrefix(arg, "--work-tree=") ||
			strings.HasPrefix(arg, "--namespace=") {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
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

func gitEnvWithOverrides(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	keys := make(map[string]struct{}, len(extra))
	for _, entry := range extra {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			keys[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, overridden := keys[key]; overridden {
				continue
			}
		}
		out = append(out, entry)
	}
	out = append(out, extra...)
	return out
}
