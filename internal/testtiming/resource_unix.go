//go:build unix

package testtiming

import (
	"os"
	"runtime"
	"syscall"
)

func peakRSSBytes(state *os.ProcessState) int64 {
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil || usage.Maxrss <= 0 {
		return 0
	}
	peak := int64(usage.Maxrss)
	if runtime.GOOS == "darwin" {
		return peak
	}
	return peak * 1024
}
