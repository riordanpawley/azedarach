//go:build unix

package testtiming

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return terminateProcessGroup(command) }
}

func terminateProcessGroup(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", command.Process.Pid, err)
	}
	return nil
}

func waitForProcessGroupExit(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	pid := command.Process.Pid
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("observe process group %d exit: %w", pid, err)
		}
		runtime.Gosched()
	}
}
