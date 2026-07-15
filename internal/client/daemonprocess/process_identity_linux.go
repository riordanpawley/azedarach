//go:build linux

package daemonprocess

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func platformProcessStartToken(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return "", syscall.ESRCH
		}
		return "", err
	}
	// The command name in field 2 is parenthesized and may itself contain
	// spaces or parentheses. Fields after its final ')' begin with state (3),
	// making index 19 the process start time (22).
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return "", fmt.Errorf("parse /proc/%d/stat: missing command terminator", pid)
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return "", fmt.Errorf("parse /proc/%d/stat: got %d trailing fields", pid, len(fields))
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return fields[19], nil
}
