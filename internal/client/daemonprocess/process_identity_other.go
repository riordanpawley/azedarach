//go:build !darwin && !linux

package daemonprocess

import "fmt"

func platformProcessStartToken(pid int) (string, error) {
	return "", fmt.Errorf("PID-reuse-safe process identity is unsupported on this platform for pid %d", pid)
}
