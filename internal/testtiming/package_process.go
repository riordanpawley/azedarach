package testtiming

import (
	"bytes"
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

// runConcurrentCommands starts every command before waiting for any of them.
// This ordering is the package-overlap contract used by migration-clone runs.
func runConcurrentCommands(commands []*exec.Cmd, stdout, stderr io.Writer) (processExecution, error) {
	started := make([]*exec.Cmd, 0, len(commands))
	stdoutWriters := make([]*atomicLineWriter, 0, len(commands))
	stderrWriters := make([]*atomicLineWriter, 0, len(commands))
	for index, command := range commands {
		stdoutWriter := &atomicLineWriter{destination: stdout}
		stderrWriter := &atomicLineWriter{destination: stderr}
		command.Stdout = stdoutWriter
		command.Stderr = stderrWriter
		if err := command.Start(); err != nil {
			for _, running := range started {
				_ = running.Process.Kill()
			}
			for _, running := range started {
				_ = running.Wait()
			}
			return processExecution{ExitCode: 1}, fmt.Errorf("start package command %d: %w", index, err)
		}
		started = append(started, command)
		stdoutWriters = append(stdoutWriters, stdoutWriter)
		stderrWriters = append(stderrWriters, stderrWriter)
	}
	var execution processExecution
	var outcomes []error
	for index, command := range started {
		err := command.Wait()
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
		if err != nil {
			outcomes = append(outcomes, fmt.Errorf("package command %d: %w", index, err))
		}
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
