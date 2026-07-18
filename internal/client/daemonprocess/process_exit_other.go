//go:build !darwin && !linux

package daemonprocess

import "fmt"

func observePlatformProcessExit(pid int) (<-chan error, error) {
	return nil, fmt.Errorf("kernel-bound non-reaping process exit observation is unsupported for pid %d", pid)
}
