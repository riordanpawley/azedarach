package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

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
	workDir        string // Working directory for git commands
	outputObserver processOutputObserver
}

type processOutputObserver func(stream string, output []byte)

type processOutputWriter struct {
	stream   string
	dst      *bytes.Buffer
	observer processOutputObserver
}

func (w processOutputWriter) Write(output []byte) (int, error) {
	written, err := w.dst.Write(output)
	if written > 0 && w.observer != nil {
		w.observer(w.stream, output[:written])
	}
	return written, err
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

	stdoutText, stderrText, err := runProcessGroupCommandWithObserver(
		ctx,
		e.workDir,
		gitEnvWithOverrides(sanitizedGitEnv(os.Environ()), extraEnv),
		e.outputObserver,
		"git",
		args...,
	)
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

// runProcessGroupCommand gives every managed subprocess its own process group.
// Cancelling the context terminates the group rather than only the direct
// child, which prevents Git hooks, task runners, and their descendants from
// surviving an obsolete integration attempt.
func runProcessGroupCommand(ctx context.Context, dir string, env []string, name string, args ...string) (stdoutText, stderrText string, err error) {
	return runProcessGroupCommandWithObserver(ctx, dir, env, nil, name, args...)
}

func runProcessGroupCommandWithObserver(ctx context.Context, dir string, env []string, observer processOutputObserver, name string, args ...string) (stdoutText, stderrText string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pid := cmd.Process.Pid
		if signalErr := syscall.Kill(-pid, syscall.SIGTERM); signalErr != nil {
			if errors.Is(signalErr, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return signalErr
		}
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		if syscall.Kill(-pid, 0) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		return nil
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = processOutputWriter{stream: "stdout", dst: &stdout, observer: observer}
	cmd.Stderr = processOutputWriter{stream: "stderr", dst: &stderr, observer: observer}
	err = cmd.Run()
	stdoutText = strings.TrimSpace(stdout.String())
	stderrText = strings.TrimSpace(stderr.String())
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return stdoutText, stderrText, err
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
