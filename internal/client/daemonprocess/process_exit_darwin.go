//go:build darwin

package daemonprocess

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// observePlatformProcessExit reports process exit without calling wait(2), so
// the leader continues to reserve its process-group ID through cleanup.
func observePlatformProcessExit(pid int) (<-chan error, error) {
	kqueue, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("open kqueue for spawned daemon %d: %w", pid, err)
	}
	change := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(kqueue, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kqueue)
		return nil, fmt.Errorf("register spawned daemon %d exit observation: %w", pid, err)
	}
	observed := make(chan error, 1)
	go func() {
		defer func() { _ = unix.Close(kqueue) }()
		events := make([]unix.Kevent_t, 1)
		for {
			count, waitErr := unix.Kevent(kqueue, nil, events, nil)
			if waitErr == unix.EINTR {
				continue
			}
			if waitErr != nil {
				observed <- fmt.Errorf("observe spawned daemon %d through kqueue: %w", pid, waitErr)
				return
			}
			if count == 1 {
				observed <- nil
				return
			}
		}
	}()
	return observed, nil
}
