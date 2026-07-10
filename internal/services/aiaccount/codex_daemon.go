package aiaccount

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const maxProcessInspectionBytes = 16 << 20

// CodexDaemonController detects persistent Codex processes and, when requested,
// gracefully terminates only those positively attributed to codexHome.
type CodexDaemonController interface {
	Reload(context.Context, string, bool) (protocol.AIAccountCodexDaemonReload, error)
}

type systemCodexDaemonController struct {
	scan          func(context.Context) ([]codexDaemonProcess, bool, error)
	signal        func(int) error
	nativeRunning func(context.Context, string) bool
	nativeRestart func(context.Context, string) error
}

type codexDaemonProcess struct {
	pid        int
	subcommand string
	codexHome  string
	attributed bool
}

func newSystemCodexDaemonController() CodexDaemonController {
	return systemCodexDaemonController{
		scan:          scanCodexDaemonProcesses,
		nativeRunning: nativeCodexDaemonRunning,
		nativeRestart: restartNativeCodexDaemon,
		signal: func(pid int) error {
			process, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return process.Signal(syscall.SIGTERM)
		},
	}
}

func (c systemCodexDaemonController) Reload(ctx context.Context, codexHome string, reload bool) (protocol.AIAccountCodexDaemonReload, error) {
	if c.nativeRunning != nil && c.nativeRunning(ctx, codexHome) {
		result := protocol.AIAccountCodexDaemonReload{Supported: true, NativeDaemon: true}
		if !reload {
			return result, nil
		}
		if c.nativeRestart != nil && c.nativeRestart(ctx, codexHome) == nil {
			result.NativeRestarted = true
			return result, nil
		}
		// A failed native lifecycle operation falls through to the conservative
		// legacy detector rather than making a completed credential swap fail.
	}
	processes, supported, err := c.scan(ctx)
	if err != nil {
		return protocol.AIAccountCodexDaemonReload{}, err
	}
	result := protocol.AIAccountCodexDaemonReload{Supported: supported}
	if !supported {
		return result, nil
	}
	target, err := filepath.Abs(codexHome)
	if err != nil {
		return result, fmt.Errorf("resolve CODEX_HOME: %w", err)
	}
	target = filepath.Clean(target)
	kinds := make(map[string]struct{})
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !process.attributed {
			result.UnattributedCount++
			continue
		}
		processHome, err := filepath.Abs(process.codexHome)
		if err != nil || filepath.Clean(processHome) != target {
			continue
		}
		result.DetectedPIDs = append(result.DetectedPIDs, process.pid)
		kinds[process.subcommand] = struct{}{}
		if !reload {
			continue
		}
		if err := c.signal(process.pid); err != nil {
			result.FailedPIDs = append(result.FailedPIDs, process.pid)
			continue
		}
		result.ReloadedPIDs = append(result.ReloadedPIDs, process.pid)
	}
	for kind := range kinds {
		result.Subcommands = append(result.Subcommands, kind)
	}
	sort.Ints(result.DetectedPIDs)
	sort.Ints(result.ReloadedPIDs)
	sort.Ints(result.FailedPIDs)
	sort.Strings(result.Subcommands)
	return result, nil
}

func nativeCodexDaemonRunning(ctx context.Context, codexHome string) bool {
	command := exec.CommandContext(ctx, "codex", "app-server", "daemon", "version")
	command.Env = environmentWithCodexHome(codexHome)
	return command.Run() == nil
}

func restartNativeCodexDaemon(ctx context.Context, codexHome string) error {
	command := exec.CommandContext(ctx, "codex", "app-server", "daemon", "restart")
	command.Env = environmentWithCodexHome(codexHome)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("native daemon restart failed: %w", err)
	}
	return nil
}

func environmentWithCodexHome(codexHome string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, "CODEX_HOME=") {
			out = append(out, value)
		}
	}
	return append(out, "CODEX_HOME="+codexHome)
}

func scanCodexDaemonProcesses(ctx context.Context) ([]codexDaemonProcess, bool, error) {
	var raw []byte
	var err error
	if runtime.GOOS == "linux" {
		return scanLinuxCodexDaemons(), true, nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" && runtime.GOOS != "openbsd" && runtime.GOOS != "netbsd" {
		return nil, false, nil
	}
	raw, err = runCommandBounded(ctx, "ps", "-axww", "-o", "pid=,command=")
	if err != nil {
		return nil, false, fmt.Errorf("scan processes: %w", err)
	}
	var processes []codexDaemonProcess
	for _, line := range strings.Split(string(raw), "\n") {
		pid, command, ok := parsePSLine(line)
		if !ok {
			continue
		}
		kind, ok := classifyCodexDaemon(command)
		if !ok {
			continue
		}
		home, attributed := readBSDProcessCodexHome(ctx, pid)
		processes = append(processes, codexDaemonProcess{pid: pid, subcommand: kind, codexHome: home, attributed: attributed})
	}
	return processes, true, nil
}

func scanLinuxCodexDaemons() []codexDaemonProcess {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var processes []codexDaemonProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		command, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		kind, ok := classifyCodexDaemon(string(command))
		if !ok {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		home, attributed := codexHomeFromEnvironment(environ, err == nil)
		processes = append(processes, codexDaemonProcess{pid: pid, subcommand: kind, codexHome: home, attributed: attributed})
	}
	return processes
}

func readBSDProcessCodexHome(ctx context.Context, pid int) (string, bool) {
	// BSD ps appends the process environment with `e`. Keep it in memory only;
	// credential-like values are never returned, logged, or included in errors.
	out, err := runCommandBounded(ctx, "ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=")
	if err != nil {
		return "", false
	}
	return codexHomeFromEnvironment(out, true)
}

func runCommandBounded(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxProcessInspectionBytes+1))
	if readErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, readErr
	}
	if len(data) > maxProcessInspectionBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("process inspection output exceeds %d bytes", maxProcessInspectionBytes)
	}
	waitErr := command.Wait()
	if waitErr != nil {
		return nil, waitErr
	}
	return data, nil
}

func codexHomeFromEnvironment(raw []byte, readable bool) (string, bool) {
	if !readable {
		return "", false
	}
	raw = bytes.ReplaceAll(raw, []byte{0}, []byte{' '})
	var home string
	for _, token := range strings.Fields(string(raw)) {
		if value, ok := strings.CutPrefix(token, "CODEX_HOME="); ok && value != "" {
			return filepath.Clean(value), true
		}
		if value, ok := strings.CutPrefix(token, "HOME="); ok && value != "" {
			home = value
		}
	}
	if home == "" {
		return "", false
	}
	return filepath.Clean(filepath.Join(home, ".codex")), true
}

func parsePSLine(line string) (int, string, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	return pid, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0])), true
}

func classifyCodexDaemon(command string) (string, bool) {
	fields := strings.FieldsFunc(command, func(r rune) bool { return r == 0 || r == ' ' || r == '\t' || r == '\n' })
	if len(fields) < 2 || strings.TrimSuffix(strings.ToLower(filepath.Base(fields[0])), ".exe") != "codex" {
		return "", false
	}
	for i := 1; i < len(fields); i++ {
		token := strings.ToLower(fields[i])
		switch token {
		case "app-server", "app_server":
			return "app-server", true
		case "mcp-server", "mcp_server":
			return "mcp-server", true
		case "proto":
			return "proto", true
		case "mcp":
			if i+1 < len(fields) && (strings.ToLower(fields[i+1]) == "serve" || strings.ToLower(fields[i+1]) == "server") {
				return "mcp-server", true
			}
		}
	}
	return "", false
}
