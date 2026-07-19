//go:build linux

package daemonprocess

import (
	"fmt"
	"os"
)

func openPlatformProcessExecutable(owner processIdentity) (*os.File, error) {
	file, err := os.Open(fmt.Sprintf("/proc/%d/exe", owner.pid))
	if err != nil {
		return nil, fmt.Errorf("open executable mapped by process %d: %w", owner.pid, err)
	}
	return file, nil
}
