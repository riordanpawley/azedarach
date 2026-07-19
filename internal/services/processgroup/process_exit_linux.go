//go:build linux

package processgroup

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func observeProcessExit(pid int) (<-chan error, error) {
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
		closeParen := strings.LastIndexByte(string(stat), ')')
		if closeParen < 0 || closeParen+2 >= len(stat) {
			continue
		}
		fields := strings.Fields(string(stat[closeParen+2:]))
		if len(fields) < 3 {
			continue
		}
		processGroup, err := strconv.Atoi(fields[2])
		if err == nil && processGroup == pgid && fields[0] != "Z" {
			return true, nil
		}
	}
	return false, nil
}
