package daemonprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const scopedValidationHelperEnv = "AZEDARACH_SCOPED_VALIDATION_HELPER"

func TestScopedValidationFakeAZProcess(t *testing.T) {
	channel := os.Getenv(scopedValidationHelperEnv)
	if channel != "control" && channel != "cleanup" {
		return
	}
	os.Exit(runScopedValidationFakeAZ(channel, helperArgs(os.Args)))
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
		name          string
		payload       []string
		signal        os.Signal
		stopFails     bool
		global        bool
		wantHeartbeat bool
		slowCleanup   bool
		recoverGo     bool
		missingGo     bool
		wantExit      int
		wantReaped    bool
	}{
		{name: "success with protocol-skewed heartbeat during cleanup", payload: []string{"/bin/sh", "-c", "exit 0"}, wantExit: 0, wantReaped: true, wantHeartbeat: true, slowCleanup: true},
		{name: "missing explicit Go binding recovers before cleanup", recoverGo: true, wantExit: 0, wantReaped: true},
		{name: "missing Go toolchain reports durable payload phase before cleanup", missingGo: true, wantExit: 78, wantReaped: true},
		{name: "payload failure", payload: []string{"/bin/sh", "-c", "exit 23"}, wantExit: 23, wantReaped: true},
		{name: "termination signal", payload: []string{"/bin/sh", "-c", "touch \"$AZEDARACH_SCOPED_VALIDATION_PAYLOAD_READY\"; while :; do sleep 1; done"}, signal: syscall.SIGTERM, wantExit: 143, wantReaped: true},
		{name: "cleanup failure after success is durable", payload: []string{"/bin/sh", "-c", "exit 0"}, stopFails: true, wantExit: 78},
		{name: "cleanup failure after payload failure is durable", payload: []string{"/bin/sh", "-c", "exit 23"}, stopFails: true, wantExit: 78},
		{name: "global daemon is untouched", payload: []string{"/bin/sh", "-c", "exit 0"}, global: true, wantExit: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScopedValidationFixture(t, test.stopFails, test.global, test.slowCleanup)
			payload := test.payload
			if test.recoverGo || test.missingGo {
				repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(repoRoot, "scripts", "validation-bin") + string(os.PathListSeparator) + os.Getenv("PATH")
				if test.missingGo {
					bash, lookupErr := exec.LookPath("bash")
					if lookupErr != nil {
						t.Fatal(lookupErr)
					}
					path = filepath.Join(repoRoot, "scripts", "validation-bin") + string(os.PathListSeparator) + filepath.Dir(bash)
				}
				payload = []string{
					"/usr/bin/env", "-u", "AZEDARACH_REAL_GO_BIN",
					"PATH=" + path,
					"go", "version",
				}
			}
			var pid int
			if !test.global {
				pid = fixture.startDaemon()
				t.Cleanup(func() { stopTestProcessGroup(pid) })
			}
			cmd := fixture.wrapperCommand(payload...)
			var wrapperOutput bytes.Buffer
			cmd.Stdout = &wrapperOutput
			cmd.Stderr = &wrapperOutput
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if test.signal != nil {
				if err := waitForFile(fixture.payloadReady, 5*time.Second); err != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal(err)
				}
				if err := cmd.Process.Signal(test.signal); err != nil {
					t.Fatal(err)
				}
			}
			err := cmd.Wait()
			if got := processExitCode(err); got != test.wantExit {
				t.Fatalf("wrapper exit = %d (%v), want %d; output:\n%s", got, err, test.wantExit, wrapperOutput.String())
			}
			alive := pid != 0 && processAlive(pid)
			if test.wantReaped && alive {
				t.Fatalf("scoped daemon pid %d survived wrapper completion", pid)
			}
			if !test.wantReaped && pid != 0 && !alive {
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
			fixture.assertChannelSeparationAndFinishOrder(test.stopFails, test.global, test.signal != nil, test.wantHeartbeat, test.slowCleanup, test.missingGo, payload, wrapperOutput.String())
		})
	}
}

func TestScopedValidationWrapperRequiresDedicatedCleanupClient(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("perl", filepath.Join(repoRoot, "scripts", "with-machine-validation-lease"), "--class", "shared", "--profile", "missing-cleanup-client", "--", "true")
	cmd.Dir = repoRoot
	cmd.Env = append(withoutEnvironment(os.Environ(),
		"AZEDARACH_VALIDATION_REQUEST_ID",
		"AZEDARACH_VALIDATION_CLEANUP_AZ_BIN",
	),
		"AZEDARACH_DAEMON_SCOPE=worktree",
		"AZEDARACH_DAEMON_SCOPE_SOURCE=",
	)
	output, err := cmd.CombinedOutput()
	if got := processExitCode(err); got == 0 {
		t.Fatalf("wrapper unexpectedly accepted missing cleanup client: %s", output)
	}
	if !strings.Contains(string(output), "requires AZEDARACH_VALIDATION_CLEANUP_AZ_BIN") {
		t.Fatalf("diagnostic = %q, want dedicated cleanup-client requirement", output)
	}
}

type scopedValidationFixture struct {
	t            *testing.T
	root         string
	controlShim  string
	cleanupShim  string
	readyPath    string
	pidPath      string
	stopLog      string
	controlLog   string
	orderLog     string
	payloadReady string
	runtimeDir   string
	env          []string
}

func newScopedValidationFixture(t *testing.T, stopFails, global, slowCleanup bool) scopedValidationFixture {
	t.Helper()
	root := t.TempDir()
	controlShim := filepath.Join(root, "production-az")
	cleanupShim := filepath.Join(root, "candidate-az")
	daemonShim := filepath.Join(root, "azd")
	for path, channel := range map[string]string{controlShim: "control", cleanupShim: "cleanup"} {
		script := fmt.Sprintf("#!/bin/sh\nexport %s=%s\nexec %q -test.run '^TestScopedValidationFakeAZProcess$' -- \"$@\"\n", scopedValidationHelperEnv, channel, os.Args[0])
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(os.Args[0], daemonShim); err != nil {
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
		"AZEDARACH_VALIDATION_CONTROL_AZ_BIN="+controlShim,
		"AZEDARACH_VALIDATION_CLEANUP_AZ_BIN="+cleanupShim,
		"AZEDARACH_DAEMON_SCOPE="+scope,
		"AZEDARACH_DAEMON_SCOPE_SOURCE=",
		"AZEDARACH_VALIDATION_HEARTBEAT_INTERVAL_SECONDS=0.1",
		"AZEDARACH_SCOPED_VALIDATION_READY="+filepath.Join(root, "ready"),
		"AZEDARACH_SCOPED_VALIDATION_PID="+filepath.Join(root, "pid"),
		"AZEDARACH_SCOPED_VALIDATION_DAEMON_BIN="+daemonShim,
		"AZEDARACH_SCOPED_VALIDATION_STOP_LOG="+filepath.Join(root, "stop.log"),
		"AZEDARACH_SCOPED_VALIDATION_CONTROL_LOG="+filepath.Join(root, "control.log"),
		"AZEDARACH_SCOPED_VALIDATION_ORDER_LOG="+filepath.Join(root, "order.log"),
		"AZEDARACH_SCOPED_VALIDATION_PAYLOAD_READY="+filepath.Join(root, "payload.ready"),
		"AZEDARACH_SCOPED_VALIDATION_RUNTIME="+filepath.Join(root, "runtime"),
	)
	if stopFails {
		env = append(env, "AZEDARACH_SCOPED_VALIDATION_STOP_FAIL=1")
	}
	if slowCleanup {
		env = append(env, "AZEDARACH_SCOPED_VALIDATION_SLOW_STOP=1")
	}
	return scopedValidationFixture{
		t: t, root: root, controlShim: controlShim, cleanupShim: cleanupShim,
		readyPath:    filepath.Join(root, "ready"),
		pidPath:      filepath.Join(root, "pid"),
		stopLog:      filepath.Join(root, "stop.log"),
		controlLog:   filepath.Join(root, "control.log"),
		orderLog:     filepath.Join(root, "order.log"),
		payloadReady: filepath.Join(root, "payload.ready"),
		runtimeDir:   filepath.Join(root, "runtime"),
		env:          env,
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
	args := []string{filepath.Join(repoRoot, "scripts", "with-machine-validation-lease"), "--class", "shared", "--purpose", "capacity", "--profile", "scoped-cleanup-test", "--"}
	args = append(args, payload...)
	cmd := exec.Command("perl", args...)
	cmd.Dir = repoRoot
	cmd.Env = f.env
	return cmd
}

func runScopedValidationFakeAZ(channel string, args []string) int {
	if len(args) < 2 {
		return 2
	}
	operation := args[0] + " " + args[1]
	if channel == "control" {
		if operation == "daemon stop" ||
			os.Getenv("AZEDARACH_DAEMON_SCOPE") != "" ||
			os.Getenv("AZEDARACH_VALIDATION_CLEANUP_AZ_BIN") != "" ||
			os.Getenv("AZEDARACH_VALIDATION_CANDIDATE_RUNTIME") != "" ||
			os.Getenv("AZEDARACH_VALIDATION_CLEANUP_HANDLE") != "" {
			return 41
		}
		_ = appendLine(os.Getenv("AZEDARACH_SCOPED_VALIDATION_CONTROL_LOG"), operation)
	} else if operation != "daemon stop" && operation != "daemon start" {
		return 42
	} else if os.Getenv("AZEDARACH_VALIDATION_LEASE_TOKEN") != "" || os.Getenv("AZEDARACH_VALIDATION_REQUEST_ID") != "" {
		return 44
	}
	switch operation {
	case "validation acquire":
		fmt.Println(`{"request":{"request_id":"fixture","state":"active"}}`)
		return 0
	case "validation heartbeat":
		_ = appendLine(os.Getenv("AZEDARACH_SCOPED_VALIDATION_ORDER_LOG"), "heartbeat")
		return 0
	case "validation finish":
		state := argumentValue(args, "--state")
		outcome := argumentValue(args, "--outcome")
		evidence := argumentValue(args, "--evidence-json")
		cleanupSucceeded := "missing"
		payloadExit := "missing"
		var decoded map[string]any
		if json.Unmarshal([]byte(evidence), &decoded) == nil {
			cleanupSucceeded = fmt.Sprint(decoded["cleanup_succeeded"])
			payloadExit = fmt.Sprint(decoded["payload_exit_code"])
		}
		if err := appendLine(os.Getenv("AZEDARACH_SCOPED_VALIDATION_ORDER_LOG"), fmt.Sprintf("finish state=%s outcome=%s cleanup=%s payload=%s", state, outcome, cleanupSucceeded, payloadExit)); err != nil {
			return 43
		}
		return 0
	case "daemon stop":
		if os.Getenv("AZEDARACH_SCOPED_VALIDATION_STOP_FAIL") == "1" {
			_ = appendLine(os.Getenv("AZEDARACH_SCOPED_VALIDATION_ORDER_LOG"), "stop-failed")
			return 19
		}
		return stopScopedValidationTestDaemon()
	case "daemon start":
		if err := startScopedValidationTestDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return 0
	default:
		return 4
	}
}

func (f scopedValidationFixture) startDaemon() int {
	f.t.Helper()
	cmd := exec.Command(f.cleanupShim, "daemon", "start")
	cmd.Env = f.env
	if output, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("start fixture daemon: %v: %s", err, output)
	}
	return waitForPID(f.t, f.readyPath, 5*time.Second)
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
	cmd := exec.Command(os.Getenv("AZEDARACH_SCOPED_VALIDATION_DAEMON_BIN"), "-test.run", "^TestScopedValidationDaemonProcess$")
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
	if os.Getenv("AZEDARACH_SCOPED_VALIDATION_SLOW_STOP") == "1" {
		orderLog := os.Getenv("AZEDARACH_SCOPED_VALIDATION_ORDER_LOG")
		_ = appendLine(orderLog, "stop-begin")
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(string(readFileBestEffort(orderLog)), "stop-begin\nheartbeat\n") && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if !strings.Contains(string(readFileBestEffort(orderLog)), "stop-begin\nheartbeat\n") {
			return 18
		}
	}
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
	if err := appendLine(os.Getenv("AZEDARACH_SCOPED_VALIDATION_ORDER_LOG"), "stop"); err != nil {
		return 10
	}
	return 0
}

func (f scopedValidationFixture) assertChannelSeparationAndFinishOrder(stopFails, global, interrupted, wantHeartbeat, slowCleanup, missingGo bool, payload []string, wrapperOutput string) {
	f.t.Helper()
	control := strings.Fields(string(readFileBestEffort(f.controlLog)))
	joinedControl := strings.Join(control, " ")
	if !strings.Contains(joinedControl, "validation acquire") || !strings.Contains(joinedControl, "validation finish") || strings.Contains(joinedControl, "daemon stop") {
		f.t.Fatalf("production control log = %q, want validation-only acquire/finish", joinedControl)
	}
	if wantHeartbeat && !strings.Contains(joinedControl, "validation heartbeat") {
		f.t.Fatalf("heartbeat diagnostic: control=%q order=%q stop=%q wrapper_output=%q", string(readFileBestEffort(f.controlLog)), string(readFileBestEffort(f.orderLog)), string(readFileBestEffort(f.stopLog)), wrapperOutput)
	}
	order := strings.Split(strings.TrimSpace(string(readFileBestEffort(f.orderLog))), "\n")
	semanticOrder := make([]string, 0, len(order))
	for _, event := range order {
		if event != "heartbeat" {
			semanticOrder = append(semanticOrder, event)
		}
	}
	if global {
		if len(semanticOrder) != 1 || !strings.HasPrefix(semanticOrder[0], "finish state=completed") {
			f.t.Fatalf("global order log = %q, want finish only", order)
		}
		return
	}
	wantSemantic := 2
	if slowCleanup {
		wantSemantic = 3
	}
	if len(semanticOrder) != wantSemantic || !(semanticOrder[len(semanticOrder)-2] == "stop" || semanticOrder[len(semanticOrder)-2] == "stop-failed") || !strings.HasPrefix(semanticOrder[len(semanticOrder)-1], "finish ") {
		f.t.Fatalf("cleanup/finish order = %q, want stop then finish", order)
	}
	if slowCleanup {
		begin := slices.Index(order, "stop-begin")
		stop := slices.Index(order, "stop")
		heartbeatDuringCleanup := begin >= 0 && stop > begin && slices.Contains(order[begin+1:stop], "heartbeat")
		if !heartbeatDuringCleanup {
			f.t.Fatalf("cleanup/heartbeat order = %q, want heartbeat between stop-begin and stop", order)
		}
	}
	finish := semanticOrder[len(semanticOrder)-1]
	wantCleanup := "cleanup=true"
	wantState := "state=completed"
	if stopFails {
		wantCleanup = "cleanup=false"
		wantState = "state=failed"
	} else if interrupted {
		wantState = "state=cancelled"
	} else if missingGo || strings.Contains(strings.Join(payload, " "), "exit 23") {
		wantState = "state=failed"
	}
	if !strings.Contains(finish, wantCleanup) || !strings.Contains(finish, wantState) {
		f.t.Fatalf("finish record = %q, want %s and %s", finish, wantCleanup, wantState)
	}
	wantPayload := "payload=0"
	if interrupted {
		wantPayload = "payload=143"
	} else if strings.Contains(strings.Join(payload, " "), "exit 23") {
		wantPayload = "payload=23"
	} else if missingGo {
		wantPayload = "payload=78"
	}
	if !strings.Contains(finish, wantPayload) {
		f.t.Fatalf("finish record = %q, want original %s evidence", finish, wantPayload)
	}
	wantOutcomePayload := strings.Replace(wantPayload, "payload=", "payload_exit=", 1)
	if stopFails && (!strings.Contains(finish, "outcome=exit 78 phase=cleanup") || !strings.Contains(finish, wantOutcomePayload)) {
		f.t.Fatalf("finish record = %q, want typed cleanup failure with original payload exit", finish)
	}
	if missingGo && !strings.Contains(finish, "outcome=exit 78 phase=toolchain_configuration") {
		f.t.Fatalf("finish record = %q, want durable toolchain-configuration phase", finish)
	}
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

func argumentValue(args []string, name string) string {
	for i := range args {
		if args[i] == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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

func waitForPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PID in %s", path)
	return 0
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
