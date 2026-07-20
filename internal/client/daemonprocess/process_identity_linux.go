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
	return linuxProcessStartToken(pid, data)
}

func platformProcessIdentity(pid int) (string, string, []string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil, syscall.ESRCH
		}
		return "", "", nil, err
	}
	startToken, err := linuxProcessStartToken(pid, data)
	if err != nil {
		return "", "", nil, err
	}
	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", "", nil, fmt.Errorf("read /proc/%d/exe: %w", pid, err)
	}
	commandLine, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", "", nil, fmt.Errorf("read /proc/%d/cmdline: %w", pid, err)
	}
	arguments := splitNullTerminatedArguments(commandLine)
	if len(arguments) == 0 {
		return "", "", nil, fmt.Errorf("read /proc/%d/cmdline: empty command line", pid)
	}
	return startToken, executable, arguments, nil
}

func linuxProcessStartToken(pid int, data []byte) (string, error) {
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

func platformProcessMissing(err error) bool {
	return os.IsNotExist(err) || err == syscall.ESRCH
}

func splitNullTerminatedArguments(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
