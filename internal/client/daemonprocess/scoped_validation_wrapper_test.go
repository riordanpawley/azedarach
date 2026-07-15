package daemonprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const scopedValidationHelperEnv = "AZEDARACH_SCOPED_VALIDATION_HELPER"

func TestScopedValidationFakeAZProcess(t *testing.T) {
	if os.Getenv(scopedValidationHelperEnv) != "az" {
		return
	}
	os.Exit(runScopedValidationFakeAZ(helperArgs(os.Args)))
}

func TestScopedValidationDaemonProcess(t *testing.T) {
	if os.Getenv(scopedValidationHelperEnv) != "daemon" {
		return
	}
	readyPath := os.Getenv("AZEDARACH_SCOPED_VALIDATION_READY")
	if err := os.WriteFile(readyPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(91)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestRealProcessProfileScopedValidationWrapperReapsDaemon(t *testing.T) {
	tests := []struct {
		name       string
		payload    []string
		signal     os.Signal
		stopFails  bool
		global     bool
		wantExit   int
		wantReaped bool
	}{
		{name: "success", payload: []string{"/bin/sh", "-c", "exit 0"}, wantExit: 0, wantReaped: true},
		{name: "payload failure", payload: []string{"/bin/sh", "-c", "exit 23"}, wantExit: 23, wantReaped: true},
		{name: "termination signal", payload: []string{"/bin/sh", "-c", "while :; do sleep 1; done"}, signal: syscall.SIGTERM, wantExit: 143, wantReaped: true},
		{name: "cleanup failure is observable", payload: []string{"/bin/sh", "-c", "exit 0"}, stopFails: true, wantExit: 70},
		{name: "global daemon is untouched", payload: []string{"/bin/sh", "-c", "exit 0"}, global: true, wantExit: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScopedValidationFixture(t, test.stopFails, test.global)
			cmd := fixture.wrapperCommand(test.payload...)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := waitForFile(fixture.readyPath, 5*time.Second); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(err)
			}
			pid := readPID(t, fixture.readyPath)
			t.Cleanup(func() { stopTestProcessGroup(pid) })
			if test.signal != nil {
				if err := cmd.Process.Signal(test.signal); err != nil {
					t.Fatal(err)
				}
			}
			err := cmd.Wait()
			if got := processExitCode(err); got != test.wantExit {
				t.Fatalf("wrapper exit = %d (%v), want %d", got, err, test.wantExit)
			}
			alive := processAlive(pid)
			if test.wantReaped && alive {
				t.Fatalf("scoped daemon pid %d survived wrapper completion", pid)
			}
			if !test.wantReaped && !alive {
				t.Fatalf("daemon pid %d was unexpectedly reaped", pid)
			}
			stopCalls, readErr := os.ReadFile(fixture.stopLog)
			if test.wantReaped {
				if readErr != nil || strings.TrimSpace(string(stopCalls)) != "stop" {
					t.Fatalf("stop log = %q err=%v, want one stop", stopCalls, readErr)
				}
				if _, statErr := os.Stat(fixture.runtimeDir); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("scoped runtime still exists: %v", statErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("global/failed cleanup unexpectedly invoked stop: %q err=%v", stopCalls, readErr)
			}
		})
	}
}

type scopedValidationFixture struct {
	t          *testing.T
	root       string
	shim       string
	readyPath  string
	pidPath    string
	stopLog    string
	runtimeDir string
	env        []string
}

func newScopedValidationFixture(t *testing.T, stopFails, global bool) scopedValidationFixture {
	t.Helper()
	root := t.TempDir()
	shim := filepath.Join(root, "az")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run '^TestScopedValidationFakeAZProcess$' -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	scope := "worktree"
	if global {
		scope = "global"
	}
	env := append(withoutEnvironment(os.Environ(),
		"AZEDARACH_VALIDATION_REQUEST_ID",
		"AZEDARACH_VALIDATION_NESTED_FD",
		"AZEDARACH_VALIDATION_LEASE_TOKEN",
		"AZEDARACH_VALIDATION_CLASS",
		"AZEDARACH_VALIDATION_PROFILE",
		"AZEDARACH_VALIDATION_SOURCE_REVISION",
	),
		scopedValidationHelperEnv+"=az",
		"AZEDARACH_VALIDATION_AZ_BIN="+shim,
		"AZEDARACH_DAEMON_SCOPE="+scope,
		"AZEDARACH_DAEMON_SCOPE_SOURCE=",
		"AZEDARACH_SCOPED_VALIDATION_READY="+filepath.Join(root, "ready"),
		"AZEDARACH_SCOPED_VALIDATION_PID="+filepath.Join(root, "pid"),
		"AZEDARACH_SCOPED_VALIDATION_STOP_LOG="+filepath.Join(root, "stop.log"),
		"AZEDARACH_SCOPED_VALIDATION_RUNTIME="+filepath.Join(root, "runtime"),
	)
	if stopFails {
		env = append(env, "AZEDARACH_SCOPED_VALIDATION_STOP_FAIL=1")
	}
	return scopedValidationFixture{
		t: t, root: root, shim: shim,
		readyPath:  filepath.Join(root, "ready"),
		pidPath:    filepath.Join(root, "pid"),
		stopLog:    filepath.Join(root, "stop.log"),
		runtimeDir: filepath.Join(root, "runtime"),
		env:        env,
	}
}

func withoutEnvironment(environment []string, names ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		keep := true
		for _, name := range names {
			if strings.HasPrefix(entry, name+"=") {
				keep = false
				break
			}
		}
		if keep {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (f scopedValidationFixture) wrapperCommand(payload ...string) *exec.Cmd {
	f.t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		f.t.Fatal(err)
	}
	args := []string{filepath.Join(repoRoot, "scripts", "with-machine-validation-lease"), "--class", "shared", "--profile", "scoped-cleanup-test", "--"}
	args = append(args, payload...)
	cmd := exec.Command("perl", args...)
	cmd.Dir = repoRoot
	cmd.Env = f.env
	return cmd
}

func runScopedValidationFakeAZ(args []string) int {
	if len(args) < 2 {
		return 2
	}
	switch args[0] + " " + args[1] {
	case "validation acquire":
		if err := startScopedValidationTestDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		fmt.Println(`{"request":{"request_id":"fixture","state":"active"}}`)
		return 0
	case "validation heartbeat", "validation finish":
		return 0
	case "daemon stop":
		if os.Getenv("AZEDARACH_SCOPED_VALIDATION_STOP_FAIL") == "1" {
			return 19
		}
		return stopScopedValidationTestDaemon()
	default:
		return 4
	}
}

func startScopedValidationTestDaemon() error {
	pidPath := os.Getenv("AZEDARACH_SCOPED_VALIDATION_PID")
	if data, err := os.ReadFile(pidPath); err == nil {
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && processAlive(pid) {
			return nil
		}
	}
	runtimeDir := os.Getenv("AZEDARACH_SCOPED_VALIDATION_RUNTIME")
	if err := os.MkdirAll(filepath.Join(runtimeDir, "session-launch"), 0o700); err != nil {
		return err
	}
	for _, name := range []string{"daemon.sock", "daemon.lock", "daemon.lock.start"} {
		if err := os.WriteFile(filepath.Join(runtimeDir, name), nil, 0o600); err != nil {
			return err
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestScopedValidationDaemonProcess$")
	cmd.Env = append(os.Environ(), scopedValidationHelperEnv+"=daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	}
	return cmd.Process.Release()
}

func stopScopedValidationTestDaemon() int {
	pid, err := strconv.Atoi(strings.TrimSpace(string(readFileBestEffort(os.Getenv("AZEDARACH_SCOPED_VALIDATION_PID")))))
	if err != nil {
		return 5
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return 6
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return 7
	}
	if err := os.RemoveAll(os.Getenv("AZEDARACH_SCOPED_VALIDATION_RUNTIME")); err != nil {
		return 8
	}
	if err := os.WriteFile(os.Getenv("AZEDARACH_SCOPED_VALIDATION_STOP_LOG"), []byte("stop\n"), 0o600); err != nil {
		return 9
	}
	return 0
}

func helperArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func readFileBestEffort(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func stopTestProcessGroup(pid int) {
	if processAlive(pid) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
