//go:build !darwin && !linux

package daemonprocess

import "fmt"

func openPlatformProcessSignalHandle(pid int) (processSignalHandle, error) {
	return nil, fmt.Errorf("identity-bound signaling is unsupported on this platform for pid %d", pid)
}
