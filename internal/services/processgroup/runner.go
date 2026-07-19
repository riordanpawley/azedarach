package processgroup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	terminationGrace = 500 * time.Millisecond
	pipeWaitDelay    = 2 * time.Second
)

// OutputObserver receives subprocess output as it is written. Calls for
// stdout and stderr may occur concurrently.
type OutputObserver func(stream string, output []byte)

type observingWriter struct {
	stream   string
	dst      *bytes.Buffer
	observer OutputObserver
}

func (w observingWriter) Write(output []byte) (int, error) {
	written, err := w.dst.Write(output)
	if written > 0 && w.observer != nil {
		w.observer(w.stream, output[:written])
	}
	return written, err
}

// Run executes a subprocess in an owned process group. Cancellation first
// terminates the group, then force-kills survivors. WaitDelay bounds pipe
// draining if a descendant retains stdout or stderr despite cancellation.
func Run(ctx context.Context, dir string, env []string, observer OutputObserver, name string, args ...string) (stdoutText, stderrText string, err error) {
	ctx = commandContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd := newCommand(ctx, dir, env, name, args...)
	cmd.Stdout = observingWriter{stream: "stdout", dst: &stdout, observer: observer}
	cmd.Stderr = observingWriter{stream: "stderr", dst: &stderr, observer: observer}
	err = cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return stdout.String(), stderr.String(), err
}

// RunCombined preserves exec.Cmd.CombinedOutput semantics while applying the
// managed process-group lifecycle used by Run.
func RunCombined(ctx context.Context, dir string, env []string, name string, args ...string) (output []byte, err error) {
	ctx = commandContext(ctx)
	var combined bytes.Buffer
	cmd := newCommand(ctx, dir, env, name, args...)
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err = cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return combined.Bytes(), err
}

func newCommand(ctx context.Context, dir string, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = pipeWaitDelay
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
		timer := time.NewTimer(terminationGrace)
		defer timer.Stop()
		<-timer.C
		if syscall.Kill(-pid, 0) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		return nil
	}
	return cmd
}

func commandContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
