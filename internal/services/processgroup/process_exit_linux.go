//go:build linux

package processgroup

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func observeProcessExit(pid int) (<-chan error, error) {
	return selectProcessExitObserver(
		func() (<-chan error, error) { return observeProcessExitByPIDFD(pid) },
		func() (<-chan error, error) {
			// /proc state observation preserves the unreaped leader when pidfds
			// are unavailable on an otherwise supported Linux host.
			return observeProcessExitByState(pid, readLinuxProcessState, func() {
				time.Sleep(terminationPoll)
			}), nil
		},
	)
}

func observeProcessExitByPIDFD(pid int) (<-chan error, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() {
		defer func() { _ = unix.Close(fd) }()
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			_, pollErr := unix.Poll(pollFDs, -1)
			if pollErr == unix.EINTR {
				continue
			}
			if pollErr != nil {
				exited <- fmt.Errorf("observe process %d exit: %w", pid, pollErr)
				return
			}
			if pollFDs[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
				exited <- nil
				return
			}
		}
	}()
	return exited, nil
}

func readLinuxProcessState(pid int) (byte, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	state, _, err := parseLinuxProcStat(stat)
	return state, err
}

func processGroupHasLiveDescendants(pgid, leaderPID int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == leaderPID {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			if os.IsNotExist(err) || os.IsPermission(err) {
				continue
			}
			return false, err
		}
		state, processGroup, err := parseLinuxProcStat(stat)
		if err != nil {
			continue
		}
		if processGroup == pgid && state != 'Z' {
			return true, nil
		}
	}
	return false, nil
}
