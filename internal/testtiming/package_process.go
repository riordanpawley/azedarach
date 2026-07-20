package testtiming

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

type processExecution struct {
	ExitCode         int
	UserCPUSeconds   float64
	SystemCPUSeconds float64
	PeakRSSBytes     int64
}

func runProcessGroup(command *exec.Cmd) error {
	if err := startProcessGroup(command, func(command *exec.Cmd) error { return command.Start() }); err != nil {
		return err
	}
	return errors.Join(command.Wait(), waitForProcessGroupExit(command))
}

func waitForStartedProcessGroups(commands []*exec.Cmd) []error {
	outcomes := make([]error, len(commands))
	var waiters sync.WaitGroup
	waiters.Add(len(commands))
	for index, command := range commands {
		go func() {
			defer waiters.Done()
			outcomes[index] = errors.Join(command.Wait(), waitForProcessGroupExit(command))
		}()
	}
	waiters.Wait()
	return outcomes
}

// runConcurrentCommands starts every command before waiting for any of them.
// This ordering is the package-overlap contract used by migration-clone runs.

func runConcurrentCommands(ctx context.Context, commands []*exec.Cmd, stdout, stderr io.Writer) (processExecution, error) {
	return runConcurrentCommandsWithStarter(ctx, commands, stdout, stderr, func(command *exec.Cmd) error { return command.Start() })
}

func runConcurrentCommandsWithStarter(ctx context.Context, commands []*exec.Cmd, stdout, stderr io.Writer, start func(*exec.Cmd) error) (processExecution, error) {
	started := make([]*exec.Cmd, 0, len(commands))
	stdoutWriters := make([]*atomicLineWriter, 0, len(commands))
	stderrWriters := make([]*atomicLineWriter, 0, len(commands))
	for index, command := range commands {
		stdoutWriter := &atomicLineWriter{destination: stdout}
		stderrWriter := &atomicLineWriter{destination: stderr}
		command.Stdout = stdoutWriter
		command.Stderr = stderrWriter
		if err := startProcessGroup(command, start); err != nil {
			var outcomes []error
			outcomes = append(outcomes, fmt.Errorf("start package command %d: %w", index, err))
			for _, notStarted := range commands[index+1:] {
				outcomes = append(outcomes, discardProcessGroup(notStarted))
			}
			for _, running := range started {
				outcomes = append(outcomes, terminateProcessGroup(running))
			}
			waitOutcomes := waitForStartedProcessGroups(started)
			for startedIndex := range started {
				outcomes = append(outcomes, waitOutcomes[startedIndex])
				outcomes = append(outcomes, stdoutWriters[startedIndex].Finish(), stderrWriters[startedIndex].Finish())
			}
			return processExecution{ExitCode: 1}, errors.Join(outcomes...)
		}
		started = append(started, command)
		stdoutWriters = append(stdoutWriters, stdoutWriter)
		stderrWriters = append(stderrWriters, stderrWriter)
	}
	var execution processExecution
	var outcomes []error
	waitOutcomes := waitForStartedProcessGroups(started)
	for index, command := range started {
		if flushErr := stdoutWriters[index].Finish(); flushErr != nil {
			outcomes = append(outcomes, fmt.Errorf("flush package command %d stdout: %w", index, flushErr))
		}
		if flushErr := stderrWriters[index].Finish(); flushErr != nil {
			outcomes = append(outcomes, fmt.Errorf("flush package command %d stderr: %w", index, flushErr))
		}
		if command.ProcessState != nil {
			execution.UserCPUSeconds += command.ProcessState.UserTime().Seconds()
			execution.SystemCPUSeconds += command.ProcessState.SystemTime().Seconds()
			execution.PeakRSSBytes = max(execution.PeakRSSBytes, peakRSSBytes(command.ProcessState))
			if code := command.ProcessState.ExitCode(); code > execution.ExitCode {
				execution.ExitCode = code
			}
		}
		if waitOutcomes[index] != nil {
			outcomes = append(outcomes, fmt.Errorf("package command %d: %w", index, waitOutcomes[index]))
		}
	}
	if ctx.Err() != nil {
		outcomes = append(outcomes, ctx.Err())
	}
	if len(outcomes) > 0 && execution.ExitCode == 0 {
		execution.ExitCode = 1
	}
	return execution, errors.Join(outcomes...)
}

// atomicLineWriter prevents partial writes from independent package processes
// from corrupting one another in the shared go-test JSON stream.
type atomicLineWriter struct {
	mu          sync.Mutex
	destination io.Writer
	partial     []byte
}

func (w *atomicLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		index := bytes.IndexByte(w.partial, '\n')
		if index < 0 {
			break
		}
		line := append([]byte(nil), w.partial[:index+1]...)
		w.partial = w.partial[index+1:]
		if _, err := w.destination.Write(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *atomicLineWriter) Finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) == 0 {
		return nil
	}
	_, err := w.destination.Write(w.partial)
	w.partial = nil
	return err
}
