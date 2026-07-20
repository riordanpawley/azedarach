//go:build linux

package daemonprocess

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// observePlatformProcessExit reports process exit without reaping it. Keeping
// the group leader unreaped preserves its PID as the process-group identity
// fence until rejected-candidate cleanup has finished signaling descendants.
func observePlatformProcessExit(pid int) (<-chan error, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("open pidfd for spawned daemon %d: %w", pid, err)
	}
	observed := make(chan error, 1)
	go func() {
		defer func() { _ = unix.Close(fd) }()
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			_, pollErr := unix.Poll(pollFDs, -1)
			if pollErr == unix.EINTR {
				continue
			}
			if pollErr != nil {
				observed <- fmt.Errorf("observe spawned daemon %d through pidfd: %w", pid, pollErr)
				return
			}
			if pollFDs[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
				observed <- nil
				return
			}
			if pollFDs[0].Revents != 0 {
				observed <- fmt.Errorf("observe spawned daemon %d through pidfd: unexpected poll events %#x", pid, pollFDs[0].Revents)
				return
			}
		}
	}()
	return observed, nil
}
