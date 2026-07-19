package daemonprocess

import (
	"errors"
	"fmt"
	"syscall"
)

// processIdentity binds a PID to the kernel-reported process start token so a
// later process that reuses the same PID cannot satisfy a daemon-exit wait.
type processIdentity struct {
	pid        int
	startToken string
	executable string
	arguments  []string
}

// processSignalHandle is bound by the kernel to one process incarnation. Its
// Signal method must never fall back to addressing a process by bare PID.
type processSignalHandle interface {
	Signal(syscall.Signal) error
	Close() error
}

func captureProcessIdentity(pid int) (processIdentity, bool, error) {
	return captureProcessIdentityWith(pid, platformProcessIdentity, platformProcessStartToken, processAlive)
}

func captureProcessIdentityWith(
	pid int,
	capture func(int) (string, string, []string, error),
	recaptureStartToken func(int) (string, error),
	alive func(int) bool,
) (processIdentity, bool, error) {
	if pid <= 0 {
		return processIdentity{}, false, nil
	}
	startToken, executable, arguments, err := capture(pid)
	if err != nil {
		if platformProcessMissing(err) || errors.Is(err, syscall.ESRCH) || !alive(pid) {
			return processIdentity{}, false, nil
		}
		return processIdentity{}, false, fmt.Errorf("read process %d start token: %w", pid, err)
	}
	confirmedStartToken, err := recaptureStartToken(pid)
	if err != nil {
		if platformProcessMissing(err) || errors.Is(err, syscall.ESRCH) || !alive(pid) {
			return processIdentity{}, false, nil
		}
		return processIdentity{}, false, fmt.Errorf("confirm process %d start token after identity capture: %w", pid, err)
	}
	if confirmedStartToken != startToken {
		return processIdentity{}, false, fmt.Errorf("process %d identity changed during executable and argument capture", pid)
	}
	return processIdentity{
		pid:        pid,
		startToken: startToken,
		executable: executable,
		arguments:  arguments,
	}, true, nil
}

func processIdentityAlive(identity processIdentity) (bool, error) {
	if identity.pid <= 0 || identity.startToken == "" {
		return false, nil
	}
	startToken, err := platformProcessStartToken(identity.pid)
	if err != nil {
		if platformProcessMissing(err) || errors.Is(err, syscall.ESRCH) || !processAlive(identity.pid) {
			return false, nil
		}
		return false, fmt.Errorf("read process %d start token: %w", identity.pid, err)
	}
	return startToken == identity.startToken, nil
}
