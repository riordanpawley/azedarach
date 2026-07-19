//go:build !darwin && !linux

package daemonprocess

import "fmt"

func observePlatformProcessExit(pid int) (<-chan error, error) {
	return nil, fmt.Errorf("%w for pid %d", errProcessExitObservationUnsupported, pid)
}
