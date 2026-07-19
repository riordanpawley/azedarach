//go:build !darwin && !linux

package daemonprocess

import "fmt"

func platformProcessStartToken(pid int) (string, error) {
	return "", fmt.Errorf("PID-reuse-safe process identity is unsupported on this platform for pid %d", pid)
}

func platformProcessIdentity(pid int) (string, string, []string, error) {
	return "", "", nil, fmt.Errorf("verified process identity is unsupported on this platform for pid %d", pid)
}

func platformProcessMissing(error) bool { return false }
