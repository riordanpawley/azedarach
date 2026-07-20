//go:build darwin

package daemonprocess

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unsafe"

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

func platformProcessIdentity(pid int) (string, string, []string, error) {
	startToken, err := platformProcessStartToken(pid)
	if err != nil {
		return "", "", nil, err
	}
	executable, arguments, err := darwinProcessArguments(pid)
	if err != nil {
		return "", "", nil, err
	}
	return startToken, executable, arguments, nil
}

func platformProcessMissing(err error) bool {
	return err == unix.ESRCH || err == unix.EINVAL
}

// darwinProcessArguments reads KERN_PROCARGS2 so verification compares the
// kernel's exact argv vector rather than parsing the lossy, space-delimited ps
// rendering. KERN_PROCARGS2 is stable Darwin ABI (sys/sysctl.h value 49).
func darwinProcessArguments(pid int) (string, []string, error) {
	mib := []int32{unix.CTL_KERN, 49, int32(pid)}
	var size uintptr
	if _, _, errno := unix.Syscall6(
		unix.SYS_SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		0,
		uintptr(unsafe.Pointer(&size)),
		0,
		0,
	); errno != 0 {
		return "", nil, errno
	}
	if size < 4 {
		return "", nil, fmt.Errorf("kern.procargs2 for pid %d returned %d bytes", pid, size)
	}
	buffer := make([]byte, size)
	if _, _, errno := unix.Syscall6(
		unix.SYS_SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		0,
	); errno != 0 {
		return "", nil, errno
	}
	buffer = buffer[:size]
	argc := int(binary.NativeEndian.Uint32(buffer[:4]))
	if argc <= 0 {
		return "", nil, fmt.Errorf("kern.procargs2 for pid %d returned invalid argc %d", pid, argc)
	}
	data := buffer[4:]
	executableEnd := strings.IndexByte(string(data), 0)
	if executableEnd <= 0 {
		return "", nil, fmt.Errorf("kern.procargs2 for pid %d omitted executable", pid)
	}
	executable := string(data[:executableEnd])
	data = data[executableEnd+1:]
	for len(data) > 0 && data[0] == 0 {
		data = data[1:]
	}
	arguments := make([]string, 0, argc)
	for len(arguments) < argc && len(data) > 0 {
		end := strings.IndexByte(string(data), 0)
		if end < 0 {
			end = len(data)
		}
		arguments = append(arguments, string(data[:end]))
		if end == len(data) {
			data = nil
		} else {
			data = data[end+1:]
		}
	}
	if len(arguments) != argc {
		return "", nil, fmt.Errorf("kern.procargs2 for pid %d returned %d of %d arguments", pid, len(arguments), argc)
	}
	return executable, arguments, nil
}
