//go:build linux

package daemonprocess

import (
	"syscall"

	"golang.org/x/sys/unix"
)

type pidfdProcessSignalHandle struct {
	fd int
}

func openPlatformProcessSignalHandle(pid int) (processSignalHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &pidfdProcessSignalHandle{fd: fd}, nil
}

func (h *pidfdProcessSignalHandle) Signal(signal syscall.Signal) error {
	return unix.PidfdSendSignal(h.fd, signal, nil, 0)
}

func (h *pidfdProcessSignalHandle) Close() error {
	return unix.Close(h.fd)
}
