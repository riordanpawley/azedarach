//go:build darwin

package processgroup

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const darwinZombieStatus = 5 // SZOMB from Darwin sys/proc.h.

func observeProcessExit(pid int) (<-chan error, error) {
	kqueue, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	change := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(kqueue, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kqueue)
		if errors.Is(err, unix.ESRCH) {
			exited := make(chan error, 1)
			exited <- nil
			return exited, nil
		}
		return nil, err
	}
	exited := make(chan error, 1)
	go func() {
		defer func() { _ = unix.Close(kqueue) }()
		events := make([]unix.Kevent_t, 1)
		for {
			count, waitErr := unix.Kevent(kqueue, nil, events, nil)
			if waitErr == unix.EINTR {
				continue
			}
			if waitErr != nil {
				exited <- fmt.Errorf("observe process %d exit: %w", pid, waitErr)
				return
			}
			if count == 1 {
				exited <- nil
				return
			}
		}
	}()
	return exited, nil
}

func processGroupHasLiveDescendants(pgid, leaderPID int) (bool, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		if int(process.Proc.P_pid) != leaderPID && process.Proc.P_stat != darwinZombieStatus {
			return true, nil
		}
	}
	return false, nil
}
