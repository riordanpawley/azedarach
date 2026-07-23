//go:build unix

package testtiming

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"syscall"
)

const processGroupAnchorEnvironment = "AZEDARACH_TESTTIMING_PROCESS_GROUP_ANCHOR"

type processOutputDrain struct {
	reader *os.File
	writer *os.File
	done   chan error
}

type processGroupLifecycle struct {
	reader        io.ReadCloser
	writer        *os.File
	anchor        *exec.Cmd
	anchorControl *os.File
	drains        []*processOutputDrain
}

var processGroupLifecycles sync.Map

func init() {
	if os.Getenv(processGroupAnchorEnvironment) != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func configureProcessGroup(command *exec.Cmd) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create process-group lifecycle pipe: %w", err)
	}
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
	if err := startProcessGroupAnchor(lifecycle); err != nil {
		processGroupLifecycles.Delete(command)
		return errors.Join(err, closeProcessGroupLifecycleWriter(lifecycle), lifecycle.reader.Close())
	}
	if err := prepareProcessOutputDrains(command, lifecycle); err != nil {
		return errors.Join(err, abandonStartedProcessGroup(command, lifecycle))
	}
	attributes := command.SysProcAttr
	if attributes == nil {
		attributes = &syscall.SysProcAttr{}
	}
	attributes.Setpgid = true
	attributes.Pgid = lifecycle.anchor.Process.Pid
	command.SysProcAttr = attributes
	startErr := start(command)
	closeErr := errors.Join(closeProcessGroupLifecycleWriter(lifecycle), closeProcessOutputWriters(lifecycle.drains))
	if startErr != nil || closeErr != nil {
		return errors.Join(startErr, closeErr, abandonStartedProcessGroup(command, lifecycle))
	}
	return nil
}

func startProcessGroupAnchor(lifecycle *processGroupLifecycle) error {
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create process-group anchor control pipe: %w", err)
	}
	anchor := exec.Command(os.Args[0])
	anchor.Env = withEnv(os.Environ(), processGroupAnchorEnvironment, "1")
	anchor.Stdin = controlReader
	anchor.ExtraFiles = []*os.File{lifecycle.writer}
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := anchor.Start(); err != nil {
		return errors.Join(fmt.Errorf("start process-group anchor: %w", err), controlReader.Close(), controlWriter.Close())
	}
	lifecycle.anchor = anchor
	lifecycle.anchorControl = controlWriter
	if err := controlReader.Close(); err != nil {
		return errors.Join(fmt.Errorf("close parent process-group anchor reader: %w", err), terminateAnchoredProcessGroup(lifecycle))
	}
	return nil
}

func prepareProcessOutputDrains(command *exec.Cmd, lifecycle *processGroupLifecycle) error {
	stdout := command.Stdout
	stderr := command.Stderr
	if stdout != nil && sameWriter(stdout, stderr) {
		drain, err := newProcessOutputDrain(stdout)
		if err != nil {
			return err
		}
		lifecycle.drains = append(lifecycle.drains, drain)
		command.Stdout = drain.writer
		command.Stderr = drain.writer
		return nil
	}
	if stdout != nil {
		drain, err := newProcessOutputDrain(stdout)
		if err != nil {
			return err
		}
		lifecycle.drains = append(lifecycle.drains, drain)
		command.Stdout = drain.writer
	}
	if stderr != nil {
		drain, err := newProcessOutputDrain(stderr)
		if err != nil {
			return err
		}
		lifecycle.drains = append(lifecycle.drains, drain)
		command.Stderr = drain.writer
	}
	return nil
}

func sameWriter(left, right io.Writer) bool {
	if left == nil || right == nil || reflect.TypeOf(left) != reflect.TypeOf(right) || !reflect.TypeOf(left).Comparable() {
		return false
	}
	return left == right
}

func newProcessOutputDrain(destination io.Writer) (*processOutputDrain, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create supervised process output pipe: %w", err)
	}
	drain := &processOutputDrain{reader: reader, writer: writer, done: make(chan error, 1)}
	go func() {
		_, copyErr := io.Copy(destination, reader)
		drain.done <- errors.Join(copyErr, reader.Close())
	}()
	return drain, nil
}

func closeProcessOutputWriters(drains []*processOutputDrain) error {
	var outcomes []error
	for _, drain := range drains {
		if drain.writer != nil {
			outcomes = append(outcomes, drain.writer.Close())
			drain.writer = nil
		}
	}
	return errors.Join(outcomes...)
}

func waitForProcessOutputDrains(drains []*processOutputDrain) error {
	var outcomes []error
	for _, drain := range drains {
		outcomes = append(outcomes, <-drain.done)
	}
	return errors.Join(outcomes...)
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
	return errors.Join(closeProcessGroupLifecycleWriter(lifecycle), lifecycle.reader.Close())
}

func closeProcessGroupLifecycleWriter(lifecycle *processGroupLifecycle) error {
	if lifecycle.writer == nil {
		return nil
	}
	err := lifecycle.writer.Close()
	lifecycle.writer = nil
	return err
}

func terminateProcessGroup(command *exec.Cmd) error {
	if command == nil {
		return nil
	}
	if lifecycleValue, configured := processGroupLifecycles.Load(command); configured {
		lifecycle := lifecycleValue.(*processGroupLifecycle)
		if lifecycle.anchor != nil && lifecycle.anchor.Process != nil {
			return killProcessGroup(lifecycle.anchor.Process.Pid)
		}
	}
	if command.Process == nil {
		return nil
	}
	return killProcessGroup(command.Process.Pid)
}

func killProcessGroup(groupID int) error {
	if err := syscall.Kill(-groupID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", groupID, err)
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
	killErr := terminateAnchoredProcessGroup(lifecycle)
	waitErr := waitForProcessGroupLifetime(lifecycle.reader)
	return errors.Join(killErr, waitErr, lifecycle.reader.Close(), waitForProcessOutputDrains(lifecycle.drains))
}

func terminateAnchoredProcessGroup(lifecycle *processGroupLifecycle) error {
	if lifecycle.anchor == nil || lifecycle.anchor.Process == nil {
		return nil
	}
	killErr := killProcessGroup(lifecycle.anchor.Process.Pid)
	_ = lifecycle.anchor.Wait()
	var controlErr error
	if lifecycle.anchorControl != nil {
		controlErr = lifecycle.anchorControl.Close()
		lifecycle.anchorControl = nil
	}
	return errors.Join(killErr, controlErr)
}

func abandonStartedProcessGroup(command *exec.Cmd, lifecycle *processGroupLifecycle) error {
	processGroupLifecycles.Delete(command)
	killErr := terminateAnchoredProcessGroup(lifecycle)
	closeErr := errors.Join(closeProcessGroupLifecycleWriter(lifecycle), closeProcessOutputWriters(lifecycle.drains))
	waitErr := waitForProcessGroupLifetime(lifecycle.reader)
	return errors.Join(killErr, closeErr, waitErr, lifecycle.reader.Close(), waitForProcessOutputDrains(lifecycle.drains))
}

func waitForProcessGroupLifetime(reader io.Reader) error {
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("wait for process-group lifecycle: %w", err)
	}
	return nil
}
