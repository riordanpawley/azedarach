//go:build unix

package testtiming

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type processGroupLifecycle struct {
	reader *os.File
	writer *os.File
}

var processGroupLifecycles sync.Map

func configureProcessGroup(command *exec.Cmd) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create process-group lifecycle pipe: %w", err)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return terminateProcessGroup(command) }
	command.ExtraFiles = append(command.ExtraFiles, writer)
	processGroupLifecycles.Store(command, &processGroupLifecycle{reader: reader, writer: writer})
	return nil
}

func startProcessGroup(command *exec.Cmd, start func(*exec.Cmd) error) error {
	lifecycleValue, configured := processGroupLifecycles.Load(command)
	if !configured {
		return start(command)
	}
	lifecycle := lifecycleValue.(*processGroupLifecycle)
	startErr := start(command)
	closeErr := lifecycle.writer.Close()
	if startErr != nil {
		processGroupLifecycles.Delete(command)
		return errors.Join(startErr, closeErr, lifecycle.reader.Close())
	}
	if closeErr == nil {
		return nil
	}
	terminateErr := terminateProcessGroup(command)
	waitErr := command.Wait()
	groupErr := waitForProcessGroupExit(command)
	return errors.Join(fmt.Errorf("close parent process-group lifecycle writer: %w", closeErr), terminateErr, waitErr, groupErr)
}

func discardProcessGroup(command *exec.Cmd) error {
	if command == nil {
		return nil
	}
	lifecycleValue, configured := processGroupLifecycles.LoadAndDelete(command)
	if !configured {
		return nil
	}
	lifecycle := lifecycleValue.(*processGroupLifecycle)
	return errors.Join(lifecycle.writer.Close(), lifecycle.reader.Close())
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
	if command == nil {
		return nil
	}
	lifecycleValue, configured := processGroupLifecycles.LoadAndDelete(command)
	if !configured {
		return nil
	}
	lifecycle := lifecycleValue.(*processGroupLifecycle)
	waitErr := waitForProcessGroupLifetime(lifecycle.reader)
	return errors.Join(waitErr, lifecycle.reader.Close())
}

func waitForProcessGroupLifetime(reader io.Reader) error {
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("wait for process-group lifecycle: %w", err)
	}
	return nil
}
