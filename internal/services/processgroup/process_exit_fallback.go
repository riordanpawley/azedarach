package processgroup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

type processExitObserver func() (<-chan error, error)

// selectProcessExitObserver falls back only when the primary observer is not a
// usable Linux capability. Unexpected failures remain fail-closed.
func selectProcessExitObserver(primary, fallback processExitObserver) (<-chan error, error) {
	exited, err := primary()
	if err == nil {
		return exited, nil
	}
	if !errors.Is(err, syscall.ENOSYS) && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.EPERM) {
		return nil, err
	}
	return fallback()
}

// observeProcessExitByState observes a terminal process state without calling
// wait, preserving the leader's PID and process-group identity for cleanup.
func observeProcessExitByState(pid int, readState func(int) (byte, error), waitRetry func()) <-chan error {
	exited := make(chan error, 1)
	go func() {
		for {
			state, err := readState(pid)
			if err != nil {
				exited <- fmt.Errorf("read process %d state without reaping: %w", pid, err)
				return
			}
			if state == 'Z' {
				exited <- nil
				return
			}
			waitRetry()
		}
	}()
	return exited
}

func parseLinuxProcStat(stat []byte) (state byte, processGroup int, err error) {
	closeParen := strings.LastIndexByte(string(stat), ')')
	if closeParen < 0 || closeParen+2 >= len(stat) {
		return 0, 0, fmt.Errorf("malformed Linux proc stat")
	}
	fields := strings.Fields(string(stat[closeParen+2:]))
	if len(fields) < 3 || len(fields[0]) != 1 {
		return 0, 0, fmt.Errorf("incomplete Linux proc stat")
	}
	processGroup, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, fmt.Errorf("parse Linux process group: %w", err)
	}
	return fields[0][0], processGroup, nil
}
