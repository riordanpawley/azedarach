//go:build darwin

package daemonprocess

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func platformProcessStartToken(pid int) (string, error) {
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if proc == nil || proc.Proc.P_pid != int32(pid) {
		return "", unix.ESRCH
	}
	started := proc.Proc.P_starttime
	return fmt.Sprintf("%d:%d", started.Sec, started.Usec), nil
}
