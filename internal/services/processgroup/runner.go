package processgroup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	terminationGrace = 500 * time.Millisecond
	terminationPoll  = 10 * time.Millisecond
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
	err = runCommand(cmd)
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
	err = runCommand(cmd)
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
		return terminateProcessGroup(cmd.Process.Pid)
	}
	return cmd
}

func runCommand(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	exited, observeErr := observeProcessExit(pid)
	if observeErr != nil {
		cleanupErr := terminateProcessGroup(pid)
		waitErr := cmd.Wait()
		return errors.Join(fmt.Errorf("observe process-group leader %d exit: %w", pid, observeErr), cleanupErr, waitErr)
	}
	observeErr = <-exited
	cleanupErr := cleanupRetainedProcessGroup(pid)
	waitErr := cmd.Wait()
	return errors.Join(observeErr, cleanupErr, waitErr)
}

// cleanupRetainedProcessGroup runs before Wait reaps the direct child. The
// unreaped leader therefore reserves both its PID and PGID while descendants
// are inspected and signaled, preventing a recycled group from being killed.
func cleanupRetainedProcessGroup(pgid int) error {
	retained, err := processGroupHasLiveDescendants(pgid, pgid)
	if err != nil {
		return errors.Join(fmt.Errorf("inspect process group %d: %w", pgid, err), terminateProcessGroup(pgid))
	}
	if !retained {
		return nil
	}
	if err := signalProcessGroup(pgid, syscall.SIGTERM); err != nil {
		return err
	}

	timer := time.NewTimer(terminationGrace)
	defer timer.Stop()
	ticker := time.NewTicker(terminationPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			retained, inspectErr := processGroupHasLiveDescendants(pgid, pgid)
			if inspectErr != nil {
				return errors.Join(fmt.Errorf("inspect terminating process group %d: %w", pgid, inspectErr), signalProcessGroup(pgid, syscall.SIGKILL))
			}
			if !retained {
				return nil
			}
		case <-timer.C:
			return signalProcessGroup(pgid, syscall.SIGKILL)
		}
	}
}

func terminateProcessGroup(pgid int) error {
	if err := signalProcessGroup(pgid, syscall.SIGTERM); err != nil {
		return err
	}
	timer := time.NewTimer(terminationGrace)
	defer timer.Stop()
	<-timer.C
	if syscall.Kill(-pgid, 0) == nil {
		return signalProcessGroup(pgid, syscall.SIGKILL)
	}
	return nil
}

func signalProcessGroup(pgid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pgid, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}

func commandContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
