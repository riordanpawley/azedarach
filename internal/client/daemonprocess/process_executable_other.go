//go:build !darwin && !linux

package daemonprocess

import (
	"fmt"
	"os"
)

func openPlatformProcessExecutable(owner processIdentity) (*os.File, error) {
	return nil, fmt.Errorf("process-bound executable capture is unsupported on this platform for pid %d", owner.pid)
}
