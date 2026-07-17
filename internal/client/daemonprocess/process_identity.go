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

func captureProcessIdentity(pid int) (processIdentity, bool, error) {
	if pid <= 0 {
		return processIdentity{}, false, nil
	}
	startToken, executable, arguments, err := platformProcessIdentity(pid)
	if err != nil {
		if platformProcessMissing(err) || errors.Is(err, syscall.ESRCH) || !processAlive(pid) {
			return processIdentity{}, false, nil
		}
		return processIdentity{}, false, fmt.Errorf("read process %d start token: %w", pid, err)
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
