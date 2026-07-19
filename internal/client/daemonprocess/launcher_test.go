package daemonprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	"github.com/riordanpawley/azedarach/internal/logging"
)

type trackingWriteCloser struct {
	closed atomic.Bool
}

type recordingDaemonProcess struct {
	stopCalls atomic.Int32
	stopErr   error
	stopFn    func()
	exitCh    chan error
}

func (p *recordingDaemonProcess) exited() <-chan error { return p.exitCh }

func (p *recordingDaemonProcess) stopAndWait(context.Context) error {
	p.stopCalls.Add(1)
	if p.stopFn != nil {
		p.stopFn()
	}
	return p.stopErr
}

type recordingDaemonStarter struct {
	process *recordingDaemonProcess
	specs   []daemonProcessSpec
}

func TestExecDaemonProcessStopAndWaitAcceptsProvenReapAfterKill(t *testing.T) {
	done := make(chan error, 1)
	process := &execDaemonProcess{
		cmd:  &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done: done,
	}
	var signals []syscall.Signal
	process.signalProcessGroup = func(signal syscall.Signal) error {
		signals = append(signals, signal)
		if signal == syscall.SIGKILL {
			done <- errors.New("fixture process exited after KILL")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := process.stopAndWait(ctx); err != nil {
		t.Fatalf("stopAndWait() error = %v, want proven post-KILL reap success", err)
	}
	if !reflect.DeepEqual(signals, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("cleanup signals = %v, want TERM then KILL", signals)
	}
}

func TestExecDaemonProcessStopAndWaitNeverSignalsReusedGroupAfterLeaderReaped(t *testing.T) {
	waitDone := make(chan struct{})
	close(waitDone)
	replacementSignaled := make(chan syscall.Signal, 2)
	process := &execDaemonProcess{
		cmd:      &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done:     make(chan error, 1),
		waitDone: waitDone,
		// The original candidate group is already gone. This positive probe
		// represents an unrelated process group that reused the numeric PGID
		// after cmd.Wait reaped the original leader.
		processGroupAlive: func() (bool, error) { return true, nil },
		signalProcessGroup: func(signal syscall.Signal) error {
			replacementSignaled <- signal
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := process.stopAndWait(ctx)
	if err == nil {
		t.Fatal("stopAndWait() error = nil, want fail-closed loss of candidate group authority")
	}
	select {
	case signal := <-replacementSignaled:
		t.Fatalf("replacement process group received %v after candidate leader reap", signal)
	default:
	}
}

func TestExecDaemonProcessRetainsLeaderUntilGroupSignalsComplete(t *testing.T) {
	waitDone := make(chan struct{})
	reapAllowed := make(chan struct{})
	groupAlive := true
	process := &execDaemonProcess{
		cmd:         &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done:        make(chan error, 1),
		waitDone:    waitDone,
		reapAllowed: reapAllowed,
		processGroupAlive: func() (bool, error) {
			return groupAlive, nil
		},
	}
	var signals []syscall.Signal
	process.signalProcessGroup = func(signal syscall.Signal) error {
		select {
		case <-waitDone:
			t.Fatalf("leader reaped before %v process-group signal", signal)
		default:
		}
		signals = append(signals, signal)
		if signal == syscall.SIGKILL {
			groupAlive = false
		}
		return nil
	}
	go func() {
		<-reapAllowed
		close(waitDone)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := process.stopAndWait(ctx); err != nil {
		t.Fatalf("stopAndWait() error = %v", err)
	}
	if !reflect.DeepEqual(signals, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("cleanup signals = %v, want TERM then KILL before leader reap", signals)
	}
}

const lingeringDaemonHelperEnv = "AZEDARACH_TEST_LINGERING_DAEMON"

const lingeringDaemonModeEnv = "AZEDARACH_TEST_LINGERING_DAEMON_MODE"

const lingeringDaemonParentPIDEnv = "AZEDARACH_TEST_LINGERING_PARENT_PID"

const (
	ownedExecutableHelperEnv         = "AZEDARACH_TEST_OWNED_EXECUTABLE"
	ownedExecutableModeEnv           = "AZEDARACH_TEST_OWNED_EXECUTABLE_MODE"
	ownedExecutableIdentityPathEnv   = "AZEDARACH_TEST_OWNED_EXECUTABLE_IDENTITY"
	ownedExecutableControlSocketEnv  = "AZEDARACH_TEST_OWNED_EXECUTABLE_CONTROL"
	ownedExecutableParentPIDEnv      = "AZEDARACH_TEST_OWNED_EXECUTABLE_PARENT_PID"
	ownedExecutableHelperTestPattern = "^TestLauncherOwnedExecutableHelper$"
)

type ownedExecutableIdentity struct {
	PID        int      `json:"pid"`
	StartToken string   `json:"start_token"`
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
}

func TestLauncherOwnedExecutableHelper(t *testing.T) {
	if os.Getenv(ownedExecutableHelperEnv) != "1" {
		return
	}
	parentPID, err := strconv.Atoi(os.Getenv(ownedExecutableParentPIDEnv))
	if err != nil || parentPID <= 0 {
		os.Exit(20)
	}
	parentExited, err := observePlatformProcessExit(parentPID)
	if err != nil {
		os.Exit(21)
	}
	identity, present, err := captureProcessIdentity(os.Getpid())
	if err != nil || !present {
		os.Exit(22)
	}
	record, err := json.Marshal(ownedExecutableIdentity{
		PID:        identity.pid,
		StartToken: identity.startToken,
		Executable: identity.executable,
		Arguments:  identity.arguments,
	})
	if err != nil || os.WriteFile(os.Getenv(ownedExecutableIdentityPathEnv), record, 0o600) != nil {
		os.Exit(23)
	}

	switch os.Getenv(ownedExecutableModeEnv) {
	case "incompatible":
		for _, argument := range os.Args[1:] {
			switch argument {
			case "--preflight":
				fmt.Printf("{\"daemon_version\":\"incompatible-fixture\",\"min_protocol_version\":%d,\"max_protocol_version\":%d}\n", protocol.CurrentVersion+1, protocol.CurrentVersion+1)
				os.Exit(0)
			case "--version":
				fmt.Println("incompatible-fixture")
				os.Exit(0)
			}
		}
		os.Exit(24)
	case "compatible":
		fmt.Printf("{\"daemon_version\":\"compatible-fixture\",\"min_protocol_version\":%d,\"max_protocol_version\":%d}\n", protocol.CurrentVersion, protocol.CurrentVersion)
		os.Exit(0)
	case "readiness-failure":
		connection, err := net.Dial("unix", os.Getenv(ownedExecutableControlSocketEnv))
		if err != nil {
			os.Exit(25)
		}
		if _, err := connection.Write([]byte{1}); err != nil {
			os.Exit(26)
		}
		_ = connection.Close()
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)
		select {
		case <-signals:
			os.Exit(0)
		case <-parentExited:
			os.Exit(0)
		}
	default:
		os.Exit(27)
	}
}

func writeOwnedExecutableWrapper(t *testing.T, path string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=%s -- \"$@\"\n", os.Args[0], ownedExecutableHelperTestPattern)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func configureOwnedExecutableHelper(t *testing.T, mode string) string {
	t.Helper()
	identityPath := filepath.Join(t.TempDir(), "identity.json")
	t.Setenv(ownedExecutableHelperEnv, "1")
	t.Setenv(ownedExecutableModeEnv, mode)
	t.Setenv(ownedExecutableIdentityPathEnv, identityPath)
	t.Setenv(ownedExecutableParentPIDEnv, strconv.Itoa(os.Getpid()))
	return identityPath
}

func readOwnedExecutableIdentity(t *testing.T, identityPath string) processIdentity {
	t.Helper()
	record, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read owned executable identity: %v", err)
	}
	var stored ownedExecutableIdentity
	if err := json.Unmarshal(record, &stored); err != nil {
		t.Fatalf("decode owned executable identity: %v", err)
	}
	return processIdentity{pid: stored.PID, startToken: stored.StartToken, executable: stored.Executable, arguments: stored.Arguments}
}

func assertOwnedExecutableRetired(t *testing.T, identityPath string) {
	t.Helper()
	identity := readOwnedExecutableIdentity(t, identityPath)
	alive, err := processIdentityAlive(identity)
	if err != nil {
		t.Fatalf("inspect owned executable identity: %v", err)
	}
	if alive {
		t.Fatalf("owned executable remains live after path completion: %+v", identity)
	}
}

// TestLauncherReplaceLingeringDaemonHelper is a real predecessor process for
// replacement tests. On TERM it removes the runtime socket and lock exactly as
// azd does, reports that authority assets are gone, then deliberately remains
// alive on a pipe barrier until the parent authorizes exact process exit.
func TestLauncherReplaceLingeringDaemonHelper(t *testing.T) {
	if os.Getenv(lingeringDaemonHelperEnv) != "1" {
		return
	}
	ready := os.NewFile(3, "ready")
	shutdownObserved := os.NewFile(4, "shutdown-observed")
	exitBarrier := os.NewFile(5, "exit-barrier")
	if ready == nil || shutdownObserved == nil || exitBarrier == nil {
		os.Exit(2)
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	parentPID, err := strconv.Atoi(os.Getenv(lingeringDaemonParentPIDEnv))
	if err != nil || parentPID <= 0 {
		os.Exit(10)
	}
	parentExited, err := observePlatformProcessExit(parentPID)
	if err != nil {
		os.Exit(11)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(7)
	}
	mode := os.Getenv(lingeringDaemonModeEnv)
	var shutdownSignal os.Signal
	select {
	case shutdownSignal = <-signals:
	case <-parentExited:
		os.Exit(0)
	}
	if mode != "" && shutdownSignal != syscall.SIGUSR1 {
		os.Exit(8)
	}
	if err := os.Remove(os.Getenv("AZEDARACH_TEST_DAEMON_SOCKET")); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Exit(3)
	}
	if err := os.Remove(os.Getenv("AZEDARACH_TEST_DAEMON_LOCK")); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Exit(4)
	}
	if _, err := shutdownObserved.Write([]byte{1}); err != nil {
		os.Exit(5)
	}
	if mode == "graceful" {
		os.Exit(0)
	}
	if mode == "term" {
		select {
		case signal := <-signals:
			if signal != syscall.SIGTERM {
				os.Exit(9)
			}
		case <-parentExited:
			os.Exit(0)
		}
		os.Exit(0)
	}
	if mode == "kill" {
		for {
			select {
			case <-signals:
			case <-parentExited:
				os.Exit(0)
			}
		}
	}
	var release [1]byte
	released := make(chan error, 1)
	go func() {
		_, readErr := exitBarrier.Read(release[:])
		released <- readErr
	}()
	select {
	case err := <-released:
		if err != nil {
			os.Exit(6)
		}
	case <-parentExited:
		os.Exit(0)
	}
	os.Exit(0)
}

func TestCaptureProcessIdentityIncludesExecutableForRollback(t *testing.T) {
	identity, present, err := captureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !present || !executableFile(identity.executable) {
		t.Fatalf("process identity = %+v present=%t, want executable rollback identity", identity, present)
	}
}

func TestCaptureProcessIdentityRejectsPIDReuseDuringSnapshot(t *testing.T) {
	identity, present, err := captureProcessIdentityWith(
		4242,
		func(int) (string, string, []string, error) {
			return "old-start", "/managed/generation.old/azd", []string{"azd", "--repo", "/old"}, nil
		},
		func(int) (string, error) { return "reused-start", nil },
		func(int) bool { return true },
	)
	if err == nil || !strings.Contains(err.Error(), "identity changed during executable and argument capture") {
		t.Fatalf("captureProcessIdentityWith() = (%+v, %t, %v), want PID-reuse refusal", identity, present, err)
	}
}

type lingeringDaemonProcess struct {
	cmd              *exec.Cmd
	identity         processIdentity
	shutdownObserved *os.File
	exitBarrier      *os.File
	waitDone         chan error
	exited           chan struct{}
	releaseOnce      sync.Once
}

func startLingeringDaemonProcess(t *testing.T, launcher *Launcher) *lingeringDaemonProcess {
	t.Helper()
	generation := writeManagedTestGeneration(
		t,
		filepath.Join(t.TempDir(), ".azedarach-generations"),
		"generation.predecessor",
	)
	return startLingeringDaemonProcessWith(
		t,
		launcher,
		filepath.Join(generation, "azd"),
		"",
		[]string{"--", "--repo", launcher.RepoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath},
	)
}

func startManagedLingeringDaemonProcess(t *testing.T, launcher *Launcher, mode string) *lingeringDaemonProcess {
	t.Helper()
	installRoot := t.TempDir()
	generationsRoot := filepath.Join(installRoot, ".azedarach-generations")
	predecessorGeneration := writeManagedTestGeneration(t, generationsRoot, "generation.predecessor")
	successorGeneration := writeManagedTestGeneration(t, generationsRoot, "generation.successor")
	launcher.BinPath = filepath.Join(successorGeneration, "azd")
	return startLingeringDaemonProcessWith(
		t,
		launcher,
		filepath.Join(predecessorGeneration, "azd"),
		mode,
		[]string{"--", "--repo", launcher.RepoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath},
	)
}

func writeManagedTestGeneration(t *testing.T, generationsRoot, generationName string) string {
	t.Helper()
	generation := filepath.Join(generationsRoot, generationName)
	testBinary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(generation, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"az", "azd"} {
		if err := os.WriteFile(filepath.Join(generation, binary), testBinary, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return generation
}

func startLingeringDaemonProcessWith(t *testing.T, launcher *Launcher, executable, mode string, extraArgs []string) *lingeringDaemonProcess {
	t.Helper()
	launcher.preflightReplace = func(context.Context, daemonCommand) error { return nil }
	launcher.preflightRollback = func(context.Context, daemonCommand) error { return nil }
	expectedRepo, expectedSocket, expectedLock := launcher.RepoDir, launcher.SocketPath, launcher.LockPath
	launcher.verifyDaemonArguments = func(_ processIdentity, label string) error {
		for _, required := range []struct{ name, got, want string }{
			{name: "repo", got: launcher.RepoDir, want: expectedRepo},
			{name: "socket", got: launcher.SocketPath, want: expectedSocket},
			{name: "lock", got: launcher.LockPath, want: expectedLock},
		} {
			if required.got != required.want {
				return fmt.Errorf("%s executable arguments do not match canonical %s", label, required.name)
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(launcher.LockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.SocketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	observedReader, observedWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	exitReader, exitWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-test.run=^TestLauncherReplaceLingeringDaemonHelper$"}, extraArgs...)
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(),
		lingeringDaemonHelperEnv+"=1",
		lingeringDaemonModeEnv+"="+mode,
		lingeringDaemonParentPIDEnv+"="+strconv.Itoa(os.Getpid()),
		"AZEDARACH_TEST_DAEMON_SOCKET="+launcher.SocketPath,
		"AZEDARACH_TEST_DAEMON_LOCK="+launcher.LockPath,
	)
	cmd.ExtraFiles = []*os.File{readyWriter, observedWriter, exitReader}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &lingeringDaemonProcess{
		cmd:              cmd,
		shutdownObserved: observedReader,
		exitBarrier:      exitWriter,
		waitDone:         make(chan error, 1),
		exited:           make(chan struct{}),
	}
	go func() {
		process.waitDone <- cmd.Wait()
		close(process.exited)
	}()
	t.Cleanup(func() {
		process.release()
		_ = cmd.Process.Kill()
		<-process.waitDone
		_ = observedReader.Close()
		if process.identity.pid == 0 {
			return
		}
		if alive, err := processIdentityAlive(process.identity); err != nil {
			t.Errorf("inspect lingering daemon helper after cleanup: %v", err)
		} else if alive {
			t.Errorf("lingering daemon helper remains live after cleanup: %+v", process.identity)
		}
	})
	_ = readyWriter.Close()
	_ = observedWriter.Close()
	_ = exitReader.Close()
	var ready [1]byte
	if _, err := io.ReadFull(readyReader, ready[:]); err != nil {
		t.Fatalf("await lingering daemon helper readiness: %v", err)
	}
	_ = readyReader.Close()
	identity, present, err := captureProcessIdentity(cmd.Process.Pid)
	if err != nil || !present {
		t.Fatalf("capture lingering daemon helper identity = (%+v, %t, %v)", identity, present, err)
	}
	process.identity = identity
	record, err := json.Marshal(map[string]any{"pid": cmd.Process.Pid, "created_at": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.LockPath, record, 0o600); err != nil {
		t.Fatal(err)
	}
	return process
}

func (p *lingeringDaemonProcess) awaitShutdown(t *testing.T) {
	t.Helper()
	var observed [1]byte
	if _, err := io.ReadFull(p.shutdownObserved, observed[:]); err != nil {
		t.Fatalf("await lingering daemon shutdown barrier: %v", err)
	}
}

func (p *lingeringDaemonProcess) release() {
	p.releaseOnce.Do(func() {
		_, _ = p.exitBarrier.Write([]byte{1})
		_ = p.exitBarrier.Close()
	})
}

func useRecordingDaemonStarter(launcher *Launcher) *recordingDaemonStarter {
	starter := &recordingDaemonStarter{process: &recordingDaemonProcess{exitCh: make(chan error)}}
	launcher.preflightReplace = func(context.Context, daemonCommand) error { return nil }
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		spec.args = append([]string(nil), spec.args...)
		starter.specs = append(starter.specs, spec)
		return starter.process, nil
	}
	return starter
}

func TestLauncherReplacePreflightFailureLeavesPredecessorRunning(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "candidate-azd")
	if err := os.WriteFile(launcher.BinPath, []byte("candidate"), 0o755); err != nil {
		t.Fatal(err)
	}
	shutdownCalls := 0
	launcher.shutdownViaSocket = func(context.Context, string) error {
		shutdownCalls++
		return nil
	}
	launcher.preflightReplace = func(context.Context, daemonCommand) error {
		return errors.New("candidate missing")
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "before stopping predecessor") {
		t.Fatalf("Replace() error = %v, want preflight failure", err)
	}
	if shutdownCalls != 0 {
		t.Fatalf("shutdown calls = %d, want predecessor untouched", shutdownCalls)
	}
}

func TestLauncherReplaceRejectsUnavailableRollbackIdentityBeforeShutdown(t *testing.T) {
	launcher, shutdownCalls := newRollbackPreflightLauncher(t, processIdentity{})

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "executable path was unavailable") {
		t.Fatalf("Replace() error = %v, want unavailable predecessor executable", err)
	}
	if *shutdownCalls != 0 {
		t.Fatalf("shutdown calls = %d, want healthy predecessor untouched", *shutdownCalls)
	}
}

func TestLauncherReplaceRejectsUnmanagedGlobalRollbackIdentityBeforeShutdown(t *testing.T) {
	predecessor := filepath.Join(t.TempDir(), "azd")
	if err := os.WriteFile(predecessor, []byte("predecessor"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher, shutdownCalls := newRollbackPreflightLauncher(t, processIdentity{executable: predecessor})

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a managed azd generation") {
		t.Fatalf("Replace() error = %v, want unmanaged predecessor rejection", err)
	}
	if *shutdownCalls != 0 {
		t.Fatalf("shutdown calls = %d, want healthy predecessor untouched", *shutdownCalls)
	}
}

func newRollbackPreflightLauncher(t *testing.T, owner processIdentity) (*Launcher, *int) {
	t.Helper()
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	candidate := filepath.Join(t.TempDir(), "candidate-azd")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.BinPath = candidate
	launcher.preflightReplace = func(context.Context, daemonCommand) error { return nil }
	launcher.captureOwner = func() (processIdentity, bool, error) { return owner, true, nil }
	shutdownCalls := 0
	launcher.shutdownViaSocket = func(context.Context, string) error {
		shutdownCalls++
		return errors.New("shutdown must not be attempted")
	}
	launcher.terminateLockOwner = func(string) error {
		return errors.New("termination must not be attempted")
	}
	return launcher, &shutdownCalls
}

func TestPreflightReplacementCommandRejectsMissingAndIncompatibleCandidate(t *testing.T) {
	missing := daemonCommand{executable: filepath.Join(t.TempDir(), "missing-azd")}
	if _, err := resolveCommandExecutable(missing); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing preflight error = %v", err)
	}

	incompatible := filepath.Join(t.TempDir(), "azd")
	if err := os.WriteFile(incompatible, []byte("#!/bin/sh\necho '{\"daemon_version\":\"old\",\"min_protocol_version\":999,\"max_protocol_version\":999}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preflightReplacementCommand(context.Background(), daemonCommand{executable: incompatible}); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible preflight error = %v", err)
	}

	compatible := filepath.Join(t.TempDir(), "azd")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"daemon_version\":\"candidate\",\"min_protocol_version\":%d,\"max_protocol_version\":%d}'\n", protocol.CurrentVersion, protocol.CurrentVersion)
	if err := os.WriteFile(compatible, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preflightReplacementCommand(context.Background(), daemonCommand{executable: compatible}); err != nil {
		t.Fatalf("compatible preflight error = %v", err)
	}

	legacyPredecessor := filepath.Join(t.TempDir(), "azd")
	if err := os.WriteFile(legacyPredecessor, []byte("#!/bin/sh\ncase \" $* \" in *' --version '*) echo legacy-current-authority; exit 0;; esac\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preflightPredecessorRollbackCommand(context.Background(), daemonCommand{executable: legacyPredecessor}); err != nil {
		t.Fatalf("legacy predecessor viability preflight error = %v", err)
	}
}

func TestLauncherReplaceRejectsIncompatibleLiveCandidateBeforeShutdown(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := makeScopedDaemonLauncherRepo(t)
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	incompatible := filepath.Join(t.TempDir(), "incompatible-azd")
	writeOwnedExecutableWrapper(t, incompatible)
	identityPath := configureOwnedExecutableHelper(t, "incompatible")
	launcher.BinPath = incompatible
	shutdownCalls := 0
	launcher.shutdownViaSocket = func(context.Context, string) error {
		shutdownCalls++
		return errors.New("no live predecessor")
	}
	startCalls := 0
	ready := false
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		startCalls++
		ready = true
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	launcher.waitForReady = func(context.Context, string) error {
		if ready {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.preflightReplace = preflightReplacementCommand

	// The candidate would remain live if launched, but advertises an
	// intentionally incompatible protocol range from its preflight mode.
	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("Replace() error = %v, want incompatible protocol rejection", err)
	}
	if shutdownCalls != 0 || startCalls != 0 {
		t.Fatalf("shutdown/start calls = %d/%d, want incompatibility rejected before lifecycle mutation", shutdownCalls, startCalls)
	}
	assertOwnedExecutableRetired(t, identityPath)
}

func TestPreflightRollbackVersionProbeOwnsAndReapsIncompatibleHelper(t *testing.T) {
	incompatible := filepath.Join(t.TempDir(), "incompatible-azd")
	writeOwnedExecutableWrapper(t, incompatible)
	identityPath := configureOwnedExecutableHelper(t, "incompatible")

	if err := preflightPredecessorRollbackCommand(context.Background(), daemonCommand{executable: incompatible}); err != nil {
		t.Fatalf("preflightPredecessorRollbackCommand() error = %v", err)
	}
	assertOwnedExecutableRetired(t, identityPath)
}

func TestLauncherReplaceRemovesInactiveRollbackStageOnPreShutdownAbort(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	generationsRoot := filepath.Dir(filepath.Dir(launcher.BinPath))
	preflightCalls := 0
	launcher.preflightReplace = func(context.Context, daemonCommand) error {
		preflightCalls++
		return nil
	}
	launcher.preflightRollback = func(context.Context, daemonCommand) error {
		preflightCalls++
		return errors.New("rollback preflight rejected")
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rollback preflight rejected") {
		t.Fatalf("Replace() error = %v, want predecessor preflight abort", err)
	}
	stages, globErr := filepath.Glob(filepath.Join(generationsRoot, "generation.rollback-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(stages) != 0 {
		t.Fatalf("inactive rollback stages = %v, want cleanup after pre-shutdown abort", stages)
	}
	if _, present, captureErr := captureProcessIdentity(predecessor.cmd.Process.Pid); captureErr != nil || !present {
		t.Fatalf("predecessor present = %t, %v, want healthy authority retained", present, captureErr)
	}
}

func TestLauncherReplaceRetainsRollbackStageAfterPredecessorExitFailure(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "graceful")
	generationsRoot := filepath.Dir(filepath.Dir(launcher.BinPath))
	launcher.shutdownViaSocket = func(context.Context, string) error {
		if err := predecessor.cmd.Process.Signal(syscall.SIGUSR1); err != nil {
			return err
		}
		predecessor.awaitShutdown(t)
		<-predecessor.exited
		return os.WriteFile(launcher.SocketPath, nil, 0o600)
	}
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error { return nil }
	startCalls := 0
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		startCalls++
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "canonical socket remains") {
		t.Fatalf("Replace() error = %v, want post-exit stale-socket failure", err)
	}
	if startCalls != 0 {
		t.Fatalf("candidate starts = %d, want none before cleanup proof", startCalls)
	}
	stages, globErr := filepath.Glob(filepath.Join(generationsRoot, "generation.rollback-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(stages) != 1 || !executableFile(filepath.Join(stages[0], "azd")) {
		t.Fatalf("retained rollback stages = %v, want one verified recovery generation", stages)
	}
}

func TestLauncherReplacementSuccessRemovesInactiveRollbackStage(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	startManagedLingeringDaemonProcess(t, launcher, "kill")
	owner, present, err := launcher.captureLockOwnerIdentity()
	if err != nil || !present {
		t.Fatalf("capture predecessor = (%+v, %t, %v)", owner, present, err)
	}
	predecessorCmd, present, err := launcher.resolvePredecessorRollbackCommand(owner, true)
	if err != nil || !present {
		t.Fatalf("resolve rollback = (%+v, %t, %v)", predecessorCmd, present, err)
	}
	stageDir := filepath.Dir(predecessorCmd.executable)
	launcher.waitForReady = func(context.Context, string) error { return nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	launcher.replacementSuccessorVerifier = func(daemonCommand) error { return nil }

	if err := launcher.startReplacementWithRollback(context.Background(), launcher.commandForExecutable(launcher.BinPath), predecessorCmd, true); err != nil {
		t.Fatalf("startReplacementWithRollback() error = %v", err)
	}
	if _, err := os.Stat(stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inactive rollback stage stat error = %v, want removed", err)
	}
}

func TestLauncherInactiveRollbackCleanupRejectsOutsideCanonicalRoots(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := makeScopedDaemonLauncherRepo(t)
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	stageDir := filepath.Join(t.TempDir(), "predecessor-forged")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	command := daemonCommand{
		executable:    filepath.Join(stageDir, "azd"),
		rollbackStage: stageDir,
	}
	if err := launcher.cleanupInactiveRollbackStage(command); err == nil || !strings.Contains(err.Error(), "outside the scoped executable root") {
		t.Fatalf("cleanup error = %v, want canonical-root refusal", err)
	}
	if _, err := os.Stat(stageDir); err != nil {
		t.Fatalf("forged rollback stage stat error = %v, want untouched", err)
	}
}

func TestLauncherReplacementStartupFailureRestoresPredecessor(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := makeScopedDaemonLauncherRepo(t)
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	candidate := filepath.Join(t.TempDir(), "candidate-azd")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatal(err)
	}
	launcher.BinPath = candidate
	predecessor := startLingeringDaemonProcess(t, launcher)
	launcher.preflightReplace = func(context.Context, daemonCommand) error { return nil }
	launcher.shutdownViaSocket = func(context.Context, string) error {
		if err := predecessor.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		predecessor.awaitShutdown(t)
		predecessor.release()
		return nil
	}
	restored := false
	launcher.waitForReady = func(context.Context, string) error {
		if restored {
			return nil
		}
		return context.DeadlineExceeded
	}
	starts := make([]string, 0, 2)
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		starts = append(starts, spec.command.executable)
		if spec.command.executable == resolvedCandidate {
			return nil, errors.New("candidate startup failed")
		}
		restored = true
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }

	err = launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restored predecessor") {
		t.Fatalf("Replace() error = %v starts=%v restored=%t, want restored predecessor diagnostic", err, starts, restored)
	}
	if len(starts) != 2 || starts[0] != resolvedCandidate || starts[1] == resolvedCandidate {
		t.Fatalf("replacement starts = %v, want candidate then captured predecessor", starts)
	}
}

func TestLauncherGlobalRollbackUsesManagedPredecessorAfterCallerCancellation(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	owner, present, err := launcher.captureLockOwnerIdentity()
	if err != nil || !present {
		t.Fatalf("capture managed predecessor = (%+v, %t, %v)", owner, present, err)
	}
	candidate := launcher.BinPath
	restored := false
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		if restored {
			return ctx.Err()
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		if spec.command.executable == candidate {
			cancel()
			return nil, errors.New("candidate startup failed")
		}
		if filepath.Clean(spec.command.executable) == filepath.Clean(owner.executable) {
			t.Fatalf("rollback executable = %q, want retained process-bound generation", spec.command.executable)
		}
		if _, managed := config.ManagedGenerationBinDir(spec.command.executable, "azd"); !managed {
			t.Fatalf("rollback executable = %q, want coherent managed generation", spec.command.executable)
		}
		restored = true
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}

	predecessorCmd, present, err := launcher.resolvePredecessorRollbackCommand(owner, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := predecessor.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-predecessor.exited
	for _, path := range []string{launcher.SocketPath, launcher.LockPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	err = launcher.startReplacementWithRollback(ctx, daemonCommand{executable: candidate}, predecessorCmd, present)
	if err == nil || !strings.Contains(err.Error(), "restored predecessor") {
		t.Fatalf("rollback error = %v, want restored predecessor after caller cancellation", err)
	}
}

func TestLauncherReplacementStartupFailureReportsNoPredecessor(t *testing.T) {
	launcher := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		return nil, errors.New("candidate startup failed")
	}

	err := launcher.startReplacementWithRollback(
		context.Background(),
		daemonCommand{executable: "candidate-azd"},
		daemonCommand{},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "no predecessor was present to restore") {
		t.Fatalf("startup error = %v, want absent predecessor diagnostic", err)
	}
	if strings.Contains(err.Error(), "executable path was unavailable") {
		t.Fatalf("startup error = %v, unexpectedly reports unavailable predecessor executable", err)
	}
}

func TestLauncherRollbackCleansRejectedCandidateAndVerifiesRestoredPredecessor(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	generationsRoot := filepath.Join(t.TempDir(), ".azedarach-generations")
	candidate := filepath.Join(writeManagedTestGeneration(t, generationsRoot, "generation.candidate"), "azd")
	rollbackStage := writeManagedTestGeneration(t, generationsRoot, "generation.rollback-active")
	predecessor := filepath.Join(rollbackStage, "azd")
	candidateCmd := launcher.commandForExecutable(candidate)
	predecessorCmd := launcher.commandForExecutable(predecessor)
	predecessorCmd.rollbackStage = rollbackStage
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }

	var authority atomic.Int32
	launcher.waitForReady = func(context.Context, string) error {
		if authority.Load() == 1 || authority.Load() == 3 {
			return nil
		}
		return context.DeadlineExceeded
	}
	candidateProcess := &recordingDaemonProcess{
		exitCh: make(chan error),
		stopFn: func() { authority.Store(2) },
	}
	starts := make([]string, 0, 2)
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		starts = append(starts, spec.command.executable)
		switch spec.command.executable {
		case candidate:
			authority.Store(1)
			return candidateProcess, nil
		case predecessor:
			authority.Store(3)
			return &recordingDaemonProcess{exitCh: make(chan error)}, nil
		default:
			return nil, fmt.Errorf("unexpected executable %s", spec.command.executable)
		}
	}
	verified := make([]string, 0, 2)
	launcher.replacementSuccessorVerifier = func(command daemonCommand) error {
		verified = append(verified, command.executable)
		if command.executable == candidate {
			return errors.New("ready socket belongs to wrong candidate owner")
		}
		if command.executable != predecessor || authority.Load() != 3 {
			return errors.New("restored predecessor identity mismatch")
		}
		return nil
	}

	err := launcher.startReplacementWithRollback(context.Background(), candidateCmd, predecessorCmd, true)
	if err == nil || !strings.Contains(err.Error(), "restored predecessor") {
		t.Fatalf("startReplacementWithRollback() error = %v, want verified restoration diagnostic", err)
	}
	if got := candidateProcess.stopCalls.Load(); got != 1 {
		t.Fatalf("rejected candidate cleanup calls = %d, want 1", got)
	}
	if !reflect.DeepEqual(starts, []string{candidate, predecessor}) {
		t.Fatalf("starts = %v, want candidate then predecessor", starts)
	}
	if !reflect.DeepEqual(verified, []string{candidate, predecessor}) {
		t.Fatalf("verified executables = %v, want candidate then predecessor", verified)
	}
	if _, err := os.Stat(rollbackStage); err != nil {
		t.Fatalf("active rollback stage stat error = %v, want retained authority", err)
	}
}

func TestLauncherRollbackRejectsReadyWrongOwnerInsteadOfReportingRestored(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	launcher.waitForReady = func(context.Context, string) error { return nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("ready wrong owner must not spawn another daemon")
		return nil, nil
	}
	verified := 0
	launcher.replacementSuccessorVerifier = func(daemonCommand) error {
		verified++
		return errors.New("ready socket owner does not match requested executable")
	}

	err := launcher.startReplacementWithRollback(
		context.Background(),
		daemonCommand{executable: "candidate-azd"},
		daemonCommand{executable: "predecessor-azd"},
		true,
	)
	if err == nil || strings.Contains(err.Error(), "; restored predecessor") || !strings.Contains(err.Error(), "restore predecessor") {
		t.Fatalf("startReplacementWithRollback() error = %v, want fail-closed restore refusal", err)
	}
	if verified != 2 {
		t.Fatalf("owner verification calls = %d, want candidate and predecessor checks", verified)
	}
}

func TestLauncherRollbackRefusesRestoreWhenExactCandidateCleanupFails(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	ready := false
	launcher.waitForReady = func(context.Context, string) error {
		if ready {
			return nil
		}
		return context.DeadlineExceeded
	}
	candidateProcess := &recordingDaemonProcess{
		exitCh:  make(chan error),
		stopErr: errors.New("candidate cleanup failed"),
	}
	starts := 0
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		starts++
		ready = true
		return candidateProcess, nil
	}
	launcher.replacementSuccessorVerifier = func(daemonCommand) error {
		return errors.New("candidate identity rejected")
	}
	rollbackStage := filepath.Join(t.TempDir(), "generation.rollback-unproven")
	if err := os.MkdirAll(rollbackStage, 0o700); err != nil {
		t.Fatal(err)
	}
	predecessorCmd := daemonCommand{
		executable:    filepath.Join(rollbackStage, "azd"),
		rollbackStage: rollbackStage,
	}

	err := launcher.startReplacementWithRollback(
		context.Background(),
		daemonCommand{executable: "candidate-azd"},
		predecessorCmd,
		true,
	)
	if err == nil || !errors.Is(err, errReplacementCandidateCleanupUnproven) || !strings.Contains(err.Error(), "refusing predecessor restore") {
		t.Fatalf("startReplacementWithRollback() error = %v, want cleanup-proof refusal", err)
	}
	if starts != 1 {
		t.Fatalf("daemon starts = %d, want rejected candidate only", starts)
	}
	if got := candidateProcess.stopCalls.Load(); got != 1 {
		t.Fatalf("rejected candidate cleanup calls = %d, want 1", got)
	}
	if _, err := os.Stat(rollbackStage); err != nil {
		t.Fatalf("unproven-cleanup rollback stage stat error = %v, want retained recovery authority", err)
	}
}

func TestLauncherRollbackNeverTerminatesNewUnreadyOwner(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	if err := os.MkdirAll(filepath.Dir(launcher.LockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("/bin/sleep", "30")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})
	record, err := json.Marshal(map[string]any{"pid": owner.Process.Pid, "created_at": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.LockPath, record, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return errors.New("must not terminate unverified owner")
	}
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("unready owner must prevent daemon spawn")
		return nil, nil
	}

	err = launcher.startReplacementWithRollback(
		context.Background(),
		daemonCommand{executable: "candidate-azd"},
		daemonCommand{executable: "predecessor-azd"},
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "appeared before replacement successor startup") {
		t.Fatalf("startReplacementWithRollback() error = %v, want unverified-owner refusal", err)
	}
	if terminateCalls != 0 {
		t.Fatalf("terminateLockOwner calls = %d, want 0", terminateCalls)
	}
}

func TestLauncherGlobalRollbackRejectsUnmanagedPredecessor(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	predecessor := filepath.Join(t.TempDir(), "azd")
	if err := os.WriteFile(predecessor, []byte("predecessor"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		return nil, errors.New("candidate startup failed")
	}

	_, _, err := launcher.resolvePredecessorRollbackCommand(processIdentity{executable: predecessor}, true)
	if err == nil || !strings.Contains(err.Error(), "not a managed azd generation") {
		t.Fatalf("rollback error = %v, want unmanaged predecessor rejection", err)
	}
}

func (w *trackingWriteCloser) Write(p []byte) (int, error) {
	return len(p), nil
}

func (w *trackingWriteCloser) Close() error {
	w.closed.Store(true)
	return nil
}

func writeLauncherConfig(t *testing.T, repoDir, logDir string) {
	t.Helper()
	configDir := filepath.Join(repoDir, ".azedarach")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir): %v", err)
	}
	body := fmt.Sprintf(`{"session":{"logDir":%q}}`, logDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(config.json): %v", err)
	}
}

func writeTestExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "azd-test")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(test executable): %v", err)
	}
	return path
}

func TestLauncherStartClosesDaemonLog(t *testing.T) {
	repoDir := t.TempDir()
	socketRoot, err := os.MkdirTemp(".", "azd-launcher-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketPath := filepath.Join(socketRoot, "daemon.sock")
	tracker := &trackingWriteCloser{}
	logDir := filepath.Join(t.TempDir(), "logs")
	writeLauncherConfig(t, repoDir, logDir)

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	if launcher.LockPath != filepath.Join(socketRoot, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(socketRoot, "daemon.lock"))
	}
	launcher.BinPath = writeTestExecutable(t)
	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(path string) (io.WriteCloser, error) {
		want := filepath.Join(logDir, logging.DaemonLogFileName)
		if path != want {
			t.Fatalf("daemon log path = %q, want %q", path, want)
		}
		return tracker, nil
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
	if len(starter.specs) != 1 {
		t.Fatalf("daemon starts = %d, want 1", len(starter.specs))
	}
	spec := starter.specs[0]
	wantArgs := []string{"--repo", repoDir, "--socket", socketPath, "--lock", launcher.LockPath}
	if spec.command.executable != launcher.BinPath || !reflect.DeepEqual(spec.args, wantArgs) || spec.command.dir != "" {
		t.Fatalf("daemon start spec = command %+v args %v, want executable %q args %v", spec.command, spec.args, launcher.BinPath, wantArgs)
	}
	if spec.stdout != tracker || spec.stderr != tracker {
		t.Fatalf("daemon start stdio = %T/%T, want shared tracked log", spec.stdout, spec.stderr)
	}
}

func TestLauncherStartUsesWorktreeLocalDaemonLogForScopedRuntime(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	writeLauncherConfig(t, repo, filepath.Join(t.TempDir(), "logs"))
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	socketRoot, err := os.MkdirTemp(".", "azd-launcher-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketPath := filepath.Join(socketRoot, "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(nested, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = writeTestExecutable(t)
	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(path string) (io.WriteCloser, error) {
		want := filepath.Join(worktree, ".azedarach", logging.DaemonLogFileName)
		if path != want {
			t.Fatalf("daemon log path = %q, want %q", path, want)
		}
		return tracker, nil
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestOpenDaemonLogRotatesOversizedLogAndReturnsRawFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), logging.DaemonLogFileName)
	seed, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(seed) error = %v", err)
	}
	if err := seed.Truncate(logging.DefaultMaxLogBytes); err != nil {
		_ = seed.Close()
		t.Fatalf("Truncate(seed) error = %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("Close(seed) error = %v", err)
	}

	logFile, err := openDaemonLog(path)
	if err != nil {
		t.Fatalf("openDaemonLog() error = %v", err)
	}
	rawFile, ok := logFile.(*os.File)
	if !ok {
		_ = logFile.Close()
		t.Fatalf("openDaemonLog() returned %T, want *os.File for daemon stdio handoff", logFile)
	}
	if _, err := rawFile.WriteString("new\n"); err != nil {
		_ = rawFile.Close()
		t.Fatalf("WriteString(new log) error = %v", err)
	}
	if err := rawFile.Close(); err != nil {
		t.Fatalf("Close(rawFile) error = %v", err)
	}

	if info, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("Stat(rotated backup) error = %v", err)
	} else if info.Size() != logging.DefaultMaxLogBytes {
		t.Fatalf("rotated backup size = %d, want %d", info.Size(), logging.DefaultMaxLogBytes)
	}
	if got := mustReadFile(t, path); got != "new\n" {
		t.Fatalf("active daemon log = %q, want new log content only", got)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return string(b)
}

func TestNewLauncherNormalizesWorktreeToBaseRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
	if launcher.LockPath != filepath.Join(base, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(base, "daemon.lock"))
	}
}

func TestNewLauncherKeepsLinkedWorktreeRootForExplicitScopedRuntime(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != worktree {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, worktree)
	}
}

func TestNewLauncherUsesBaseRepoRootForLinkedWorktreeByDefault(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	launcher := NewLauncher(filepath.Join(worktree, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
}

func TestNewLauncherKeepsMainWorktreeAtBaseRepoRoot(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")

	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}

	t.Setenv("PATH", "")
	launcher := NewLauncher(filepath.Join(repo, "go-bubbletea"), filepath.Join(base, "daemon.sock"))
	if launcher.RepoDir != repo {
		t.Fatalf("launcher.RepoDir = %q, want %q", launcher.RepoDir, repo)
	}
	if launcher.LockPath != filepath.Join(base, "daemon.lock") {
		t.Fatalf("launcher.LockPath = %q, want %q", launcher.LockPath, filepath.Join(base, "daemon.lock"))
	}
}

func TestLauncherResolveBinary_UsesMonorepoGoBubbleteaBin(t *testing.T) {
	repoDir := newLauncherTestWorktree(t)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	nestedBin := filepath.Join(repoDir, "go-bubbletea", "bin")
	if err := os.MkdirAll(nestedBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested bin): %v", err)
	}
	azd := filepath.Join(nestedBin, "azd")
	if err := os.WriteFile(azd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != azd {
		t.Fatalf("resolveBinary() = %q, want %q", got, azd)
	}
}

func TestLauncherResolveBinary_UsesWorkingDirBinFallback(t *testing.T) {
	repoDir := newLauncherTestWorktree(t)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	cwd := t.TempDir()
	t.Chdir(cwd)

	binDir := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cwd bin): %v", err)
	}
	azd := filepath.Join(binDir, "azd")
	if err := os.WriteFile(azd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cwd azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != azd {
		t.Fatalf("resolveBinary() = %q, want %q", got, azd)
	}
}

func TestLauncherResolveBinary_PrefersWorkingDirBinOverRepoBin(t *testing.T) {
	repoDir := newLauncherTestWorktree(t)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	cwd := t.TempDir()
	t.Chdir(cwd)

	repoBinDir := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(repoBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo bin): %v", err)
	}
	repoAzd := filepath.Join(repoBinDir, "azd")
	if err := os.WriteFile(repoAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(repo azd): %v", err)
	}

	cwdBinDir := filepath.Join(cwd, "bin")
	if err := os.MkdirAll(cwdBinDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cwd bin): %v", err)
	}
	cwdAzd := filepath.Join(cwdBinDir, "azd")
	if err := os.WriteFile(cwdAzd, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(cwd azd): %v", err)
	}

	launcher := NewLauncher(repoDir, socketPath)
	if got := launcher.resolveBinary(); got != cwdAzd {
		t.Fatalf("resolveBinary() = %q, want %q", got, cwdAzd)
	}
}

func newLauncherTestWorktree(t *testing.T) string {
	return newLauncherTestWorktreeWithModule(t, "github.com/riordanpawley/azedarach")
}

func newLauncherTestWorktreeWithModule(t *testing.T, module string) string {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module "+module+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	return worktree
}

func TestLauncherResolveCommand_UsesLocalGoRunForScopedWorktreeWithoutBinary(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, "cmd", "azd"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree cmd/azd): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	launcher := NewLauncher(filepath.Join(worktree, "nested"), filepath.Join(base, "daemon.sock"))

	got, err := launcher.resolveCommand()
	if err != nil {
		t.Fatalf("resolveCommand() error = %v", err)
	}
	if got.executable != "go" {
		t.Fatalf("resolveCommand().executable = %q, want go", got.executable)
	}
	if strings.Join(got.args, " ") != "run ./cmd/azd" {
		t.Fatalf("resolveCommand().args = %q, want %q", strings.Join(got.args, " "), "run ./cmd/azd")
	}
	if got.dir != worktree {
		t.Fatalf("resolveCommand().dir = %q, want %q", got.dir, worktree)
	}
}

func TestLauncherReplace_ScopedSourceFallbackStagesDirectExecutable(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	repoDir := writeScopedSourceFallbackDaemon(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }

	ready := false
	launcher.waitForReady = func(context.Context, string) error {
		if ready {
			return nil
		}
		return context.DeadlineExceeded
	}
	var started daemonProcessSpec
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		started = spec
		ready = true
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	launcher.replacementSuccessorVerifier = func(command daemonCommand) error {
		if command.executable != started.command.executable {
			return fmt.Errorf("verified executable %q differs from started %q", command.executable, started.command.executable)
		}
		if len(command.args) != 0 || command.dir != "" {
			return fmt.Errorf("replacement command remained wrapped: %+v", command)
		}
		if !executableFile(command.executable) {
			return fmt.Errorf("staged executable is unavailable: %s", command.executable)
		}
		stagingRoot := filepath.Join(config.ScopedDaemonRuntimeDir(repoDir), "executables")
		resolvedStagingRoot, err := filepath.EvalSymlinks(stagingRoot)
		if err != nil {
			return fmt.Errorf("resolve staging root: %w", err)
		}
		if rel, err := filepath.Rel(resolvedStagingRoot, command.executable); err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("staged executable %q is outside %q", command.executable, stagingRoot)
		}
		return nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if started.command.executable == "" {
		t.Fatal("Replace() did not start the staged daemon")
	}
	if _, err := os.Stat(filepath.Dir(started.command.executable)); err != nil {
		t.Fatalf("active candidate stage stat error = %v, want retained live authority", err)
	}
}

func TestLauncherReplace_ScopedSourceFallbackCleansCandidateAfterPreflightAbort(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	repoDir := writeScopedSourceFallbackDaemon(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	launcher.preflightReplace = func(context.Context, daemonCommand) error { return errors.New("reject candidate") }

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reject candidate") {
		t.Fatalf("Replace() error = %v, want candidate preflight abort", err)
	}
	stages, globErr := filepath.Glob(filepath.Join(config.ScopedDaemonRuntimeDir(repoDir), "executables", "candidate-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(stages) != 0 {
		t.Fatalf("inactive candidate stages = %v, want cleanup after preflight abort", stages)
	}
}

func TestLauncherScopedCandidateValidationOwnsAndReapsHelper(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	stageDir, executable, err := launcher.newScopedExecutableStage("candidate")
	if err != nil {
		t.Fatal(err)
	}
	writeOwnedExecutableWrapper(t, executable)
	identityPath := configureOwnedExecutableHelper(t, "compatible")
	resolvedExecutable, err := resolveCommandExecutable(daemonCommand{executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	command := daemonCommand{executable: resolvedExecutable, candidateStage: filepath.Dir(resolvedExecutable)}

	if err := preflightReplacementCommand(context.Background(), command); err != nil {
		t.Fatalf("preflightReplacementCommand() error = %v", err)
	}
	assertOwnedExecutableRetired(t, identityPath)
	if err := launcher.cleanupInactiveCandidateStage(command); err != nil {
		t.Fatalf("cleanupInactiveCandidateStage() error = %v", err)
	}
	if _, err := os.Stat(stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scoped validation candidate stage stat error = %v, want removed", err)
	}
}

func TestLauncherSourceFallbackCandidateStageOwnershipFollowsCleanupProof(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		cleanupErr error
		wantStage  bool
	}{
		{name: "proven retired"},
		{name: "unproven remains authority", cleanupErr: errors.New("candidate cleanup denied"), wantStage: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
			t.Setenv("AZEDARACH_DAEMON_BIN", "")
			repoDir := writeScopedSourceFallbackDaemon(t)
			launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
			command, err := launcher.resolveCommand()
			if err != nil {
				t.Fatal(err)
			}
			command, err = launcher.materializeReplacementCommand(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			candidateStage := command.candidateStage
			launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
			launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
			launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
				if testCase.cleanupErr == nil {
					return nil, errors.New("candidate start rejected")
				}
				return &recordingDaemonProcess{exitCh: make(chan error), stopErr: testCase.cleanupErr}, nil
			}
			if testCase.cleanupErr != nil {
				launcher.replacementSuccessorVerifier = func(daemonCommand) error { return errors.New("candidate identity rejected") }
			}

			err = launcher.startReplacementWithRollback(context.Background(), command, daemonCommand{}, false)
			if err == nil {
				t.Fatal("startReplacementWithRollback() error = nil")
			}
			_, statErr := os.Stat(candidateStage)
			if testCase.wantStage && statErr != nil {
				t.Fatalf("candidate stage stat error = %v, want retained unproven authority", statErr)
			}
			if !testCase.wantStage && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("candidate stage stat error = %v, want removed after proven retirement", statErr)
			}
		})
	}
}

func TestLauncherReplace_ScopedSourceFallbackRollbackUsesStablePredecessor(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	repoDir := writeScopedSourceFallbackDaemon(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.preflightReplace = func(_ context.Context, command daemonCommand) error {
		if len(command.args) != 0 || command.dir != "" || !executableFile(command.executable) {
			return fmt.Errorf("preflight command is not a stable direct executable: %+v", command)
		}
		return nil
	}

	predecessorExecutable := filepath.Join(t.TempDir(), "go-build-predecessor-azd")
	testBinary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(predecessorExecutable, testBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	predecessor := startLingeringDaemonProcessWith(
		t,
		launcher,
		predecessorExecutable,
		"",
		[]string{"--", "--repo", launcher.RepoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath},
	)
	launcher.preflightReplace = func(_ context.Context, command daemonCommand) error {
		if len(command.args) != 0 || command.dir != "" || !executableFile(command.executable) {
			return fmt.Errorf("preflight command is not a stable direct executable: %+v", command)
		}
		return nil
	}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		if err := predecessor.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		predecessor.awaitShutdown(t)
		if err := os.Remove(predecessorExecutable); err != nil {
			return err
		}
		predecessor.release()
		return nil
	}

	restored := false
	launcher.waitForReady = func(context.Context, string) error {
		if restored {
			return nil
		}
		return context.DeadlineExceeded
	}
	starts := make([]daemonProcessSpec, 0, 2)
	launcher.startProcess = func(spec daemonProcessSpec) (daemonProcess, error) {
		starts = append(starts, spec)
		if len(starts) == 1 {
			return nil, errors.New("candidate startup failed")
		}
		if spec.command.executable == predecessorExecutable || !executableFile(spec.command.executable) {
			return nil, fmt.Errorf("rollback executable was not retained: %s", spec.command.executable)
		}
		restored = true
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	launcher.replacementSuccessorVerifier = func(command daemonCommand) error {
		if !restored || !executableFile(command.executable) {
			return errors.New("restored predecessor executable is unavailable")
		}
		return nil
	}

	err = launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restored predecessor") {
		t.Fatalf("Replace() error = %v, want stable predecessor restoration", err)
	}
	if len(starts) != 2 {
		t.Fatalf("replacement starts = %d, want candidate then predecessor", len(starts))
	}
}

func TestLauncherScopedRollbackNeverCopiesSwappedExecutablePath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := writeScopedSourceFallbackDaemon(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))

	originalBytes, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "scoped-predecessor-azd")
	if err := os.WriteFile(executable, originalBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	predecessor := startLingeringDaemonProcessWith(
		t,
		launcher,
		executable,
		"",
		[]string{"--", "--repo", launcher.RepoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath},
	)
	owner, present, err := launcher.captureLockOwnerIdentity()
	if err != nil || !present {
		t.Fatalf("capture predecessor identity = %+v, %t, %v", owner, present, err)
	}

	replacement := filepath.Join(t.TempDir(), "path-swap-azd")
	replacementBytes := []byte("#!/bin/sh\nexit 97\n")
	if err := os.WriteFile(replacement, replacementBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, executable); err != nil {
		t.Fatal(err)
	}

	staged, stageErr := launcher.stageScopedExecutableCopy(owner, "predecessor")
	if stageErr != nil {
		return // Failing closed is valid when the mapped executable cannot be opened safely.
	}
	stagedBytes, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stagedBytes, replacementBytes) || !bytes.Equal(stagedBytes, originalBytes) {
		t.Fatalf("staged rollback bytes came from swapped pathname for live pid %d", predecessor.cmd.Process.Pid)
	}
}

func writeScopedSourceFallbackDaemon(t *testing.T) string {
	t.Helper()
	repoDir := newLauncherTestWorktree(t)
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commandDir := filepath.Join(repoDir, "cmd", "azd")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--preflight" {
			fmt.Println(%q)
			return
		}
	}
	select {}
}
`, fmt.Sprintf("{\"daemon_version\":\"scoped-source-fixture\",\"min_protocol_version\":%d,\"max_protocol_version\":%d}", protocol.CurrentVersion, protocol.CurrentVersion))
	if err := os.WriteFile(filepath.Join(commandDir, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return repoDir
}

func TestLauncherResolveCommand_DaemonBinOverrideWinsOverScopedGoRun(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, "cmd", "azd"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree cmd/azd): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	override := filepath.Join(base, "override-azd")
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", override)
	launcher := NewLauncher(worktree, filepath.Join(base, "daemon.sock"))

	got, err := launcher.resolveCommand()
	if err != nil {
		t.Fatalf("resolveCommand() error = %v", err)
	}
	if got.executable != override {
		t.Fatalf("resolveCommand().executable = %q, want %q", got.executable, override)
	}
	if len(got.args) != 0 || got.dir != "" {
		t.Fatalf("resolveCommand() args=%v dir=%q, want empty override command", got.args, got.dir)
	}
}

func TestLauncherResolveCommand_GlobalDaemonFailsClosedBeforeStalePathAzd(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "cmd", "azd"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo cmd/azd): %v", err)
	}
	staleBin := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(staleBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(stale bin): %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleBin, "azd"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(stale azd): %v", err)
	}
	clientDir := t.TempDir()
	clientPath := filepath.Join(clientDir, "az")
	if err := os.WriteFile(clientPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(client az): %v", err)
	}
	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return clientPath, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })

	t.Setenv("PATH", staleBin)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")
	launcher := NewLauncher(repoDir, socketPath)

	got, err := launcher.resolveCommand()
	if !errors.Is(err, errPairedDaemonUnavailable) {
		t.Fatalf("resolveCommand() error = %v, want %v", err, errPairedDaemonUnavailable)
	}
	if got.executable != "" {
		t.Fatalf("resolveCommand().executable = %q, want fail-closed empty command", got.executable)
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("Start() invoked process starter despite missing paired daemon")
		return nil, nil
	}
	if startErr := launcher.Start(context.Background()); !errors.Is(startErr, errPairedDaemonUnavailable) {
		t.Fatalf("Start() error = %v, want %v", startErr, errPairedDaemonUnavailable)
	}
}

func TestLauncherResolveCommand_GlobalDaemonUsesAzdFromRunningAzGeneration(t *testing.T) {
	repoDir := t.TempDir()
	staleRepoAzd := filepath.Join(repoDir, "bin", "azd")
	if err := os.MkdirAll(filepath.Dir(staleRepoAzd), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo bin): %v", err)
	}
	if err := os.WriteFile(staleRepoAzd, []byte("stale"), 0o755); err != nil {
		t.Fatalf("WriteFile(stale repo azd): %v", err)
	}

	installDir := t.TempDir()
	generationDir := filepath.Join(installDir, ".azedarach-generations", "generation.current")
	if err := os.MkdirAll(generationDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(generation): %v", err)
	}
	az := filepath.Join(generationDir, "az")
	azd := filepath.Join(generationDir, "azd")
	for _, path := range []string{az, azd} {
		if err := os.WriteFile(path, []byte("current"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	publicAz := filepath.Join(installDir, "az")
	if err := os.Symlink(filepath.Join(".azedarach-generations", "generation.current", "az"), publicAz); err != nil {
		t.Fatalf("Symlink(public az): %v", err)
	}

	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return publicAz, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("PATH", filepath.Dir(staleRepoAzd)+string(os.PathListSeparator)+generationDir+string(os.PathListSeparator)+filepath.Dir(staleRepoAzd))
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")

	got, err := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock")).resolveCommand()
	if err != nil {
		t.Fatalf("resolveCommand() error = %v", err)
	}
	wantAzd, err := filepath.EvalSymlinks(azd)
	if err != nil {
		t.Fatalf("EvalSymlinks(azd): %v", err)
	}
	if got.executable != wantAzd {
		t.Fatalf("resolveCommand().executable = %q, want immutable paired generation %q", got.executable, wantAzd)
	}
	joinedEnv := strings.Join(got.env, "\n")
	wantPath := "PATH=" + config.PrependPathEntry(os.Getenv("PATH"), filepath.Dir(wantAzd))
	if !strings.Contains(joinedEnv, wantPath) {
		t.Fatalf("resolveCommand().env missing managed PATH %q:\n%s", wantPath, joinedEnv)
	}
	if strings.Count(strings.TrimPrefix(wantPath, "PATH="), filepath.Dir(wantAzd)) != 1 {
		t.Fatalf("managed PATH was not deduplicated: %q", wantPath)
	}
}

func TestLauncherResolveCommand_ExplicitManagedAzdSeedsOnlyGlobalEnvironment(t *testing.T) {
	installDir := t.TempDir()
	generationDir := filepath.Join(installDir, ".azedarach-generations", "generation.current")
	if err := os.MkdirAll(generationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"az", "azd"} {
		if err := os.WriteFile(filepath.Join(generationDir, binary), []byte("current"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	azd := filepath.Join(generationDir, "azd")
	resolvedGenerationDir, err := filepath.EvalSymlinks(generationDir)
	if err != nil {
		t.Fatal(err)
	}
	staleDir := filepath.Join(t.TempDir(), "repo", "bin")
	t.Setenv("PATH", staleDir+string(os.PathListSeparator)+generationDir)
	t.Setenv("AZEDARACH_DAEMON_BIN", "")

	global := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "global.sock"))
	global.BinPath = azd
	got, err := global.resolveCommand()
	if err != nil {
		t.Fatal(err)
	}
	if pathValue := environmentValue(got.env, "PATH"); !strings.HasPrefix(pathValue, resolvedGenerationDir+string(os.PathListSeparator)) || strings.Count(pathValue, resolvedGenerationDir) != 1 {
		t.Fatalf("global explicit daemon PATH = %q, want one managed prefix", pathValue)
	}

	scopedRepo := makeScopedDaemonLauncherRepo(t)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	scoped := NewLauncher(scopedRepo, filepath.Join(t.TempDir(), "scoped.sock"))
	scoped.BinPath = azd
	got, err = scoped.resolveCommand()
	if err != nil {
		t.Fatal(err)
	}
	if got.env != nil {
		t.Fatalf("scoped explicit daemon environment = %v, want inherited environment", got.env)
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func makeScopedDaemonLauncherRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "worktree")
	gitDir := filepath.Join(repo, ".git", "worktrees", "worktree")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return worktree
}

func TestLauncherResolveCommand_GlobalDaemonRejectsPrimaryRepoBinPair(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git): %v", err)
	}
	repoBin := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(repoBin, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo bin): %v", err)
	}
	az := filepath.Join(repoBin, "az")
	azd := filepath.Join(repoBin, "azd")
	for _, path := range []string{az, azd} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return az, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("PATH", repoBin)
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")

	got, err := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock")).resolveCommand()
	if !errors.Is(err, errPairedDaemonUnavailable) {
		t.Fatalf("resolveCommand() error = %v, want %v", err, errPairedDaemonUnavailable)
	}
	if got.executable != "" {
		t.Fatalf("resolveCommand().executable = %q, want fail-closed empty command", got.executable)
	}
}

func TestLauncherResolveCommand_GlobalDaemonRejectsNonExecutableSibling(t *testing.T) {
	execDir := t.TempDir()
	az := filepath.Join(execDir, "az")
	if err := os.WriteFile(az, []byte("current"), 0o755); err != nil {
		t.Fatalf("WriteFile(az): %v", err)
	}
	if err := os.WriteFile(filepath.Join(execDir, "azd"), []byte("not executable"), 0o644); err != nil {
		t.Fatalf("WriteFile(azd): %v", err)
	}
	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return az, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")

	got, err := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "daemon.sock")).resolveCommand()
	if !errors.Is(err, errPairedDaemonUnavailable) {
		t.Fatalf("resolveCommand() error = %v, want %v", err, errPairedDaemonUnavailable)
	}
	if got.executable != "" {
		t.Fatalf("resolveCommand().executable = %q, want fail-closed empty command", got.executable)
	}
}

func TestLauncherStart_SkipsSpawnWhenLockOwnerAlive(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.waitForReady = func(context.Context, string) error { return nil }
	launcher.sleepFn = func(time.Duration) {}

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil (skip spawn when daemon lock owner alive)", err)
	}
}

func TestLauncherStart_SpawnsWhenLockOwnerAliveButSocketUnready(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(lockPath string) error {
		terminateCalls++
		return os.Remove(lockPath)
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls <= 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after Start() returned")
	}
}

func TestLauncherReplaceRefusesLockOwnerThatAppearsAfterCapture(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	terminated := false
	launcher.terminateLockOwner = func(lockPath string) error {
		terminated = true
		return os.Remove(lockPath)
	}

	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Replace(context.Background()); err == nil || !strings.Contains(err.Error(), "appeared after predecessor capture") {
		t.Fatalf("Replace() error = %v, want unverified-owner refusal", err)
	}
	if terminated {
		t.Fatal("Replace() terminated an owner that appeared after capture")
	}
	if len(starter.specs) != 0 || tracker.closed.Load() {
		t.Fatalf("successor starts/log opens = %d/%t, want 0/false", len(starter.specs), tracker.closed.Load())
	}
}

func TestLauncherReplaceRefusesUnreadyOwnerIdentityChangeWhileQueued(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	replacement := exec.Command("/bin/sleep", "30")
	if err := replacement.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = replacement.Process.Kill()
		_ = replacement.Wait()
	})
	launcher.beforeReplaceLock = func() {
		record, err := json.Marshal(map[string]any{"pid": replacement.Process.Pid, "created_at": time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(launcher.LockPath, record, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.shutdownViaSocket = func(context.Context, string) error {
		t.Fatal("Replace attempted shutdown after owner identity changed")
		return nil
	}
	launcher.openProcessSignalHandle = func(int) (processSignalHandle, error) {
		t.Fatal("Replace signaled a changed owner")
		return nil, nil
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "owner identity changed") {
		t.Fatalf("Replace() error = %v, want changed-owner refusal", err)
	}
	identity, present, captureErr := captureProcessIdentity(predecessor.cmd.Process.Pid)
	if captureErr != nil || !present {
		t.Fatalf("original predecessor after changed-owner refusal = (%+v, %t, %v), want alive", identity, present, captureErr)
	}
}

func TestLauncherReplacementStartRefusesNewUnreadyLockOwner(t *testing.T) {
	launcher := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "daemon.sock"))
	replacement := exec.Command("/bin/sleep", "30")
	if err := replacement.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = replacement.Process.Kill()
		_ = replacement.Wait()
	})
	record, err := json.Marshal(map[string]any{"pid": replacement.Process.Pid, "created_at": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.LockPath, record, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("replacement startup terminated a newly appeared owner")
		return nil
	}
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("replacement startup spawned alongside a newly appeared owner")
		return nil, nil
	}

	err = launcher.startWithLifecycleLockMode(context.Background(), daemonCommand{executable: "true"}, false)
	if err == nil || !strings.Contains(err.Error(), "appeared before replacement successor startup") {
		t.Fatalf("startWithLifecycleLockMode() error = %v, want new-owner refusal", err)
	}
}

func TestLauncherReplaceGracefullyStopsSocketBeforeStart(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if socketUp {
		t.Fatal("Replace() did not request socket shutdown")
	}
	if !spawned {
		t.Fatal("Replace() did not start a replacement daemon")
	}
	if !tracker.closed.Load() {
		t.Fatal("daemon log file was not closed after replacement Start() returned")
	}
}

func TestLauncherReplaceStartsDaemonFromRunningAzGeneration(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}
	generationDir := filepath.Join(t.TempDir(), ".azedarach-generations", "generation.current")
	if err := os.MkdirAll(generationDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(generation): %v", err)
	}
	az := filepath.Join(generationDir, "az")
	azd := filepath.Join(generationDir, "azd")
	for _, path := range []string{az, azd} {
		if err := os.WriteFile(path, []byte("current"), 0o755); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return az, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_BIN", "")

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.sleepFn = func(time.Duration) {}
	socketUp := true
	spawned := false
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketUp = false
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error {
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if len(starter.specs) != 1 {
		t.Fatalf("replacement starts = %d, want 1", len(starter.specs))
	}
	wantAzd, err := filepath.EvalSymlinks(azd)
	if err != nil {
		t.Fatalf("EvalSymlinks(azd): %v", err)
	}
	if got := starter.specs[0].command.executable; got != wantAzd {
		t.Fatalf("replacement executable = %q, want paired generation %q", got, wantAzd)
	}
}

func TestLauncherReplaceAttributesGracefulShutdownReason(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	var gotReason string
	launcher.shutdownWithReason = func(_ context.Context, _ string, reason string) error {
		gotReason = reason
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error {
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if gotReason != "compatibility-replace" {
		t.Fatalf("shutdown reason = %q, want compatibility-replace", gotReason)
	}
}

func TestLauncherReplaceReasonOverride(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath).WithReplaceReason("manual-restart")
	useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}

	socketUp := true
	spawned := false
	var gotReason string
	launcher.shutdownWithReason = func(_ context.Context, _ string, reason string) error {
		gotReason = reason
		socketUp = false
		return nil
	}
	launcher.terminateLockOwner = func(string) error {
		t.Fatal("Replace() should not terminate lock owner after graceful socket shutdown")
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error {
		if socketUp || spawned {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		spawned = true
		return tracker, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if gotReason != "manual-restart" {
		t.Fatalf("shutdown reason = %q, want manual-restart", gotReason)
	}
}

func TestLauncherReplaceWaitsForExactPredecessorExitAfterRuntimeAssetsDisappear(t *testing.T) {
	repoDir := t.TempDir()
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.BinPath = "true"
	predecessor := startLingeringDaemonProcess(t, launcher)

	var successorStarts atomic.Int32
	successorReady := atomic.Bool{}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGTERM)
	}
	launcher.waitForReady = func(context.Context, string) error {
		if successorReady.Load() {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		successorStarts.Add(1)
		successorReady.Store(true)
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}

	replaceDone := make(chan error, 1)
	go func() { replaceDone <- launcher.Replace(context.Background()) }()
	predecessor.awaitShutdown(t)
	if got := successorStarts.Load(); got != 0 {
		t.Fatalf("successor starts while exact predecessor remains alive = %d, want 0", got)
	}
	select {
	case err := <-replaceDone:
		t.Fatalf("Replace returned before exact predecessor exit: %v", err)
	default:
	}
	predecessor.release()
	if err := <-replaceDone; err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if got := successorStarts.Load(); got != 1 {
		t.Fatalf("successor starts after exact predecessor exit = %d, want 1", got)
	}
}

func TestLauncherReplaceFailsClosedWhenExactPredecessorExitCannotBeProven(t *testing.T) {
	repoDir := t.TempDir()
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.BinPath = "true"
	predecessor := startLingeringDaemonProcess(t, launcher)

	var successorStarts atomic.Int32
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGTERM)
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		successorStarts.Add(1)
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	replaceDone := make(chan error, 1)
	go func() { replaceDone <- launcher.Replace(ctx) }()
	predecessor.awaitShutdown(t)
	cancel()
	err := <-replaceDone
	if err == nil || !strings.Contains(err.Error(), "wait for graceful daemon predecessor exit before replace") || !errors.Is(err, context.Canceled) {
		t.Fatalf("Replace() error = %v, want fail-closed exact-exit proof cancellation", err)
	}
	if got := successorStarts.Load(); got != 0 {
		t.Fatalf("successor starts after exact-exit proof failure = %d, want 0", got)
	}
	predecessor.release()
}

func TestRealProcessProfileLauncherReplaceManagedPredecessorExitStages(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		mode        string
		wantSignals []syscall.Signal
	}{
		{name: "graceful exit", mode: "graceful"},
		{name: "TERM exit", mode: "term", wantSignals: []syscall.Signal{syscall.SIGTERM}},
		{name: "KILL escalation", mode: "kill", wantSignals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
			launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
			predecessor := startManagedLingeringDaemonProcess(t, launcher, testCase.mode)
			launcher.shutdownViaSocket = func(context.Context, string) error {
				return predecessor.cmd.Process.Signal(syscall.SIGUSR1)
			}
			var signals []syscall.Signal
			launcher.openProcessSignalHandle = func(pid int) (processSignalHandle, error) {
				if pid != predecessor.cmd.Process.Pid {
					t.Fatalf("opened signal handle for pid = %d, want predecessor %d", pid, predecessor.cmd.Process.Pid)
				}
				handle, err := openPlatformProcessSignalHandle(pid)
				if err != nil {
					return nil, err
				}
				return &recordingProcessSignalHandle{
					signal: func(signal syscall.Signal) error {
						signals = append(signals, signal)
						return handle.Signal(signal)
					},
					close: handle.Close,
				}, nil
			}
			waitCall := 0
			launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
				waitCall++
				if waitCall == 1 {
					predecessor.awaitShutdown(t)
				}
				switch testCase.mode {
				case "graceful":
					<-predecessor.exited
					return nil
				case "term":
					if waitCall == 1 {
						return context.DeadlineExceeded
					}
					<-predecessor.exited
					return nil
				case "kill":
					if waitCall < 3 {
						return context.DeadlineExceeded
					}
					<-predecessor.exited
					return nil
				default:
					t.Fatalf("unknown helper mode %q", testCase.mode)
					return nil
				}
			}
			var successorReady atomic.Bool
			launcher.waitForReady = func(context.Context, string) error {
				if successorReady.Load() {
					return nil
				}
				return context.DeadlineExceeded
			}
			launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
			launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
				successorReady.Store(true)
				return &recordingDaemonProcess{exitCh: make(chan error)}, nil
			}

			if err := launcher.Replace(context.Background()); err != nil {
				t.Fatalf("Replace() error = %v", err)
			}
			if !reflect.DeepEqual(signals, testCase.wantSignals) {
				t.Fatalf("signals = %v, want %v", signals, testCase.wantSignals)
			}
			if !successorReady.Load() {
				t.Fatal("successor was not started after predecessor exit")
			}
		})
	}
}

// Regression for production event 13919: the legacy lock-owner terminator
// returned a terminal installer error while the old daemon was still draining,
// even though the runtime converged to one successor moments later. Replacement
// must own the complete bounded TERM/KILL sequence and report the final
// singleton result, never surface the intermediate legacy timeout.
func TestRealProcessProfileLauncherReplaceDoesNotFailBeforeEventualSingleton(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGUSR1)
	}

	legacyTerminationCalls := 0
	launcher.terminateLockOwner = func(string) error {
		legacyTerminationCalls++
		return lifecycle.ErrLockOwnerTerminationTimeout
	}
	waitCall := 0
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
		waitCall++
		if waitCall == 1 {
			predecessor.awaitShutdown(t)
		}
		if waitCall < 3 {
			return context.DeadlineExceeded
		}
		<-predecessor.exited
		return nil
	}

	var successorStarts atomic.Int32
	var successorReady atomic.Bool
	launcher.waitForReady = func(context.Context, string) error {
		if successorReady.Load() {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		successorStarts.Add(1)
		successorReady.Store(true)
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}

	if err := launcher.Replace(context.Background()); err != nil {
		t.Fatalf("Replace() error = %v, want eventual singleton success", err)
	}
	if legacyTerminationCalls != 0 {
		t.Fatalf("legacy lock-owner termination calls = %d, want verified replacement reaper only", legacyTerminationCalls)
	}
	if got := successorStarts.Load(); got != 1 {
		t.Fatalf("successor starts = %d, want exactly one", got)
	}
	if _, present, err := captureProcessIdentity(predecessor.cmd.Process.Pid); err != nil || present {
		t.Fatalf("predecessor identity after replacement = (_, %t, %v), want exited", present, err)
	}
}

func TestRealProcessProfileLauncherReplaceRejectsLockIdentityChangeBeforeKill(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGUSR1)
	}
	launcher.beforePredecessorSignal = func(signal syscall.Signal) {
		if signal != syscall.SIGKILL {
			return
		}
		replacement, err := json.Marshal(map[string]any{"pid": os.Getpid(), "created_at": time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(launcher.LockPath, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var signals []syscall.Signal
	launcher.openProcessSignalHandle = func(pid int) (processSignalHandle, error) {
		handle, err := openPlatformProcessSignalHandle(pid)
		if err != nil {
			return nil, err
		}
		return &recordingProcessSignalHandle{
			signal: func(signal syscall.Signal) error {
				signals = append(signals, signal)
				return handle.Signal(signal)
			},
			close: handle.Close,
		}, nil
	}
	waitCall := 0
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
		waitCall++
		if waitCall == 1 {
			predecessor.awaitShutdown(t)
		}
		return context.DeadlineExceeded
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("successor started after lock identity changed")
		return nil, nil
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "lock ownership changed") {
		t.Fatalf("Replace() error = %v, want lock-identity refusal", err)
	}
	if !reflect.DeepEqual(signals, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("signals = %v, want TERM only", signals)
	}
	identity, present, captureErr := captureProcessIdentity(predecessor.cmd.Process.Pid)
	if captureErr != nil || !present {
		t.Fatalf("predecessor identity after refused KILL = (%+v, %t, %v), want still alive", identity, present, captureErr)
	}
}

func TestLauncherSignalCapturedPredecessorBindsSignalBeforeFinalIdentityReuseGap(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	owner, present, err := captureProcessIdentity(predecessor.cmd.Process.Pid)
	if err != nil || !present {
		t.Fatalf("capture predecessor = (%+v, %t, %v)", owner, present, err)
	}
	command, err := launcher.resolveCommand()
	if err != nil {
		t.Fatal(err)
	}

	pidOccupant := owner
	handle := &recordingProcessSignalHandle{bound: owner}
	launcher.openProcessSignalHandle = func(pid int) (processSignalHandle, error) {
		if pid != owner.pid {
			t.Fatalf("opened signal handle for pid %d, want %d", pid, owner.pid)
		}
		return handle, nil
	}
	launcher.afterPredecessorVerification = func() {
		pidOccupant = processIdentity{pid: owner.pid, startToken: owner.startToken + "-reused"}
	}
	handle.signal = func(signal syscall.Signal) error {
		if sameProcessIdentity(handle.bound, true, pidOccupant, true) {
			t.Fatal("test barrier did not replace the PID occupant")
		}
		return syscall.ESRCH
	}

	if err := launcher.signalCapturedPredecessor(context.Background(), owner, command, syscall.SIGTERM); err != nil {
		t.Fatalf("signalCapturedPredecessor() error = %v, want recycled PID treated as exited", err)
	}
	if !reflect.DeepEqual(handle.signals, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("identity-bound signals = %v, want TERM", handle.signals)
	}
	if !handle.closed {
		t.Fatal("identity-bound signal handle was not closed")
	}
}

func TestLauncherSignalCapturedPredecessorFailsClosedOnNilIdentityHandle(t *testing.T) {
	launcher := NewLauncher(t.TempDir(), filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.openProcessSignalHandle = func(int) (processSignalHandle, error) {
		return nil, nil
	}
	err := launcher.signalCapturedPredecessor(
		context.Background(),
		processIdentity{pid: 42, startToken: "captured"},
		daemonCommand{},
		syscall.SIGTERM,
	)
	if err == nil || !strings.Contains(err.Error(), "platform returned no handle") {
		t.Fatalf("signalCapturedPredecessor() error = %v, want nil-handle refusal", err)
	}
}

type recordingProcessSignalHandle struct {
	bound   processIdentity
	signal  func(syscall.Signal) error
	close   func() error
	signals []syscall.Signal
	closed  bool
}

func (h *recordingProcessSignalHandle) Signal(signal syscall.Signal) error {
	h.signals = append(h.signals, signal)
	if h.signal != nil {
		return h.signal(signal)
	}
	return nil
}

func (h *recordingProcessSignalHandle) Close() error {
	h.closed = true
	if h.close != nil {
		return h.close()
	}
	return nil
}

func TestRealProcessProfileLauncherReplaceSuccessorStartFailureLeavesRecoverableStoppedState(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	var logs bytes.Buffer
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath()).WithLogger(
		slog.New(slog.NewJSONHandler(&logs, nil)),
	)
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "graceful")
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGUSR1)
	}
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
		predecessor.awaitShutdown(t)
		<-predecessor.exited
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		return nil, errors.New("injected successor start failure")
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "injected successor start failure") {
		t.Fatalf("Replace() error = %v, want successor start failure", err)
	}
	if _, err := os.Lstat(launcher.LockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock after failed successor start = %v, want absent recoverable stopped state", err)
	}
	if _, err := os.Lstat(launcher.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after failed successor start = %v, want absent recoverable stopped state", err)
	}
	for _, field := range []string{`"stage":"successor_start"`, `"outcome":"failed"`, `"reason":"start_failed"`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("replacement logs = %s, want field %s", logs.String(), field)
		}
	}
}

func TestRealProcessProfileLauncherReplaceRefusesStaleSocketAfterPredecessorExit(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "graceful")
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGUSR1)
	}
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
		predecessor.awaitShutdown(t)
		<-predecessor.exited
		if err := os.WriteFile(launcher.SocketPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("successor started while stale socket remained")
		return nil, nil
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "canonical socket remains") {
		t.Fatalf("Replace() error = %v, want stale-socket refusal", err)
	}
}

func TestLauncherVerifyCapturedPredecessorFailsClosed(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mutate  func(*testing.T, *Launcher, *processIdentity, *daemonCommand)
		wantErr string
	}{
		{
			name: "PID reuse start identity",
			mutate: func(_ *testing.T, _ *Launcher, owner *processIdentity, _ *daemonCommand) {
				owner.startToken += "-reused"
			},
			wantErr: "process identity changed",
		},
		{
			name: "predecessor executable mismatch",
			mutate: func(_ *testing.T, _ *Launcher, owner *processIdentity, _ *daemonCommand) {
				owner.executable += "-replacement"
			},
			wantErr: "process identity changed",
		},
		{
			name: "repo mismatch",
			mutate: func(_ *testing.T, launcher *Launcher, _ *processIdentity, _ *daemonCommand) {
				launcher.RepoDir += "-other"
			},
			wantErr: "canonical repo",
		},
		{
			name: "unrelated successor install root",
			mutate: func(t *testing.T, _ *Launcher, _ *processIdentity, command *daemonCommand) {
				root := filepath.Join(t.TempDir(), ".azedarach-generations")
				command.executable = filepath.Join(writeManagedTestGeneration(t, root, "generation.unrelated"), "azd")
			},
			wantErr: "unrelated install roots",
		},
		{
			name: "linked worktree scope",
			mutate: func(t *testing.T, launcher *Launcher, _ *processIdentity, _ *daemonCommand) {
				t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
				worktree, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				launcher.RepoDir = worktree
			},
			wantErr: "outside the canonical global runtime",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
			launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
			predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
			owner, present, err := captureProcessIdentity(predecessor.cmd.Process.Pid)
			if err != nil || !present {
				t.Fatalf("capture predecessor = (%+v, %t, %v)", owner, present, err)
			}
			command, err := launcher.resolveCommand()
			if err != nil {
				t.Fatal(err)
			}
			testCase.mutate(t, launcher, &owner, &command)
			err = launcher.verifyCapturedPredecessor(context.Background(), owner, command)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("verifyCapturedPredecessor() error = %v, want %q", err, testCase.wantErr)
			}
		})
	}

	t.Run("unmanaged predecessor", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
		launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
		root := filepath.Join(t.TempDir(), ".azedarach-generations")
		launcher.BinPath = filepath.Join(writeManagedTestGeneration(t, root, "generation.successor"), "azd")
		predecessor := startLingeringDaemonProcessWith(
			t,
			launcher,
			os.Args[0],
			"kill",
			[]string{"--", "--repo", launcher.RepoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath},
		)
		owner, present, err := captureProcessIdentity(predecessor.cmd.Process.Pid)
		if err != nil || !present {
			t.Fatalf("capture predecessor = (%+v, %t, %v)", owner, present, err)
		}
		command, err := launcher.resolveCommand()
		if err != nil {
			t.Fatal(err)
		}
		err = launcher.verifyCapturedPredecessor(context.Background(), owner, command)
		if err == nil || !strings.Contains(err.Error(), "unmanaged executable") {
			t.Fatalf("verifyCapturedPredecessor() error = %v, want unmanaged refusal", err)
		}
	})
}

func TestLauncherVerifyCanonicalDaemonArgumentsModelsEffectiveFlagSemantics(t *testing.T) {
	launcher := NewLauncher("/project", "/runtime/daemon.sock")
	launcher.LockPath = "/runtime/daemon.lock"
	for _, testCase := range []struct {
		name      string
		arguments []string
		wantErr   string
	}{
		{name: "separate values", arguments: []string{"azd", "--repo", "/project", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock"}},
		{name: "equals values", arguments: []string{"azd", "--repo=/project", "--socket=/runtime/daemon.sock", "--lock=/runtime/daemon.lock"}},
		{name: "single hyphen like Go flag", arguments: []string{"azd", "-repo=/project", "-socket", "/runtime/daemon.sock", "-lock=/runtime/daemon.lock"}},
		{name: "triple hyphen is unknown", arguments: []string{"azd", "---repo=/project", "--socket=/runtime/daemon.sock", "--lock=/runtime/daemon.lock"}, wantErr: "unexpected flag"},
		{name: "wrong value", arguments: []string{"azd", "--repo", "/other", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock"}, wantErr: "canonical repo"},
		{name: "missing value", arguments: []string{"azd", "--repo"}, wantErr: "omit canonical repo value"},
		{name: "duplicate same", arguments: []string{"azd", "--repo", "/project", "--repo", "/project", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock"}, wantErr: "ambiguous duplicate --repo"},
		{name: "mixed override bypass", arguments: []string{"azd", "--repo=/other", "--repo", "/project", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock"}, wantErr: "ambiguous duplicate --repo"},
		{name: "ignored after positional", arguments: []string{"azd", "positional", "--repo", "/project", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock"}, wantErr: "positional or trailing"},
		{name: "trailing positional", arguments: []string{"azd", "--repo", "/project", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock", "trailing"}, wantErr: "positional or trailing"},
		{name: "terminator before flags", arguments: []string{"azd", "--", "--repo", "/project", "--socket", "/runtime/daemon.sock", "--lock", "/runtime/daemon.lock"}, wantErr: "positional or trailing"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := launcher.verifyCanonicalDaemonArguments(processIdentity{arguments: testCase.arguments}, "candidate")
			if testCase.wantErr == "" && err != nil {
				t.Fatalf("verifyCanonicalDaemonArguments(%v) error = %v", testCase.arguments, err)
			}
			if testCase.wantErr != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantErr)) {
				t.Fatalf("verifyCanonicalDaemonArguments(%v) error = %v, want %q", testCase.arguments, err, testCase.wantErr)
			}
		})
	}
}

func TestRealProcessProfileLauncherVerifyReplacementSuccessorRequiresExactInstalledOwner(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	predecessor := startManagedLingeringDaemonProcess(t, launcher, "kill")
	owner, present, err := captureProcessIdentity(predecessor.cmd.Process.Pid)
	if err != nil || !present {
		t.Fatalf("capture managed process = (%+v, %t, %v)", owner, present, err)
	}
	if err := launcher.verifyReplacementSuccessor(daemonCommand{executable: owner.executable}); err != nil {
		t.Fatalf("verifyReplacementSuccessor(exact) error = %v", err)
	}
	if err := launcher.verifyReplacementSuccessor(daemonCommand{executable: launcher.BinPath}); err == nil || !strings.Contains(err.Error(), "installed successor executable") {
		t.Fatalf("verifyReplacementSuccessor(other generation) error = %v, want executable mismatch", err)
	}
	launcher.RepoDir += "-other"
	if err := launcher.verifyReplacementSuccessor(daemonCommand{executable: owner.executable}); err == nil || !strings.Contains(err.Error(), "canonical repo") {
		t.Fatalf("verifyReplacementSuccessor(repo mismatch) error = %v, want repo mismatch", err)
	}
}

func TestLauncherReplaceRejectsLiveCanonicalRuntimeWithoutOwnerIdentity(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	launcher.BinPath = "true"
	launcher.waitForReady = func(context.Context, string) error { return nil }
	launcher.shutdownViaSocket = func(context.Context, string) error {
		t.Fatal("Replace attempted shutdown without an exact canonical owner identity")
		return nil
	}
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		t.Fatal("Replace attempted successor start without an exact canonical owner identity")
		return nil, nil
	}

	err := launcher.Replace(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing daemon lock owner identity") {
		t.Fatalf("Replace() error = %v, want missing canonical owner identity failure", err)
	}
}

func TestLauncherConcurrentReplaceCoalescesToOneSuccessor(t *testing.T) {
	repoDir := t.TempDir()
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	launcher.BinPath = "true"
	predecessor := startLingeringDaemonProcess(t, launcher)

	var successorStarts atomic.Int32
	successorReady := atomic.Bool{}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return predecessor.cmd.Process.Signal(syscall.SIGTERM)
	}
	launcher.waitForReady = func(context.Context, string) error {
		if successorReady.Load() {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		successorStarts.Add(1)
		successorReady.Store(true)
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	beforeLock := make(chan struct{}, 2)
	releaseLockRace := make(chan struct{})
	launcher.beforeReplaceLock = func() {
		beforeLock <- struct{}{}
		<-releaseLockRace
	}

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- launcher.Replace(context.Background()) }()
	}
	<-beforeLock
	<-beforeLock
	close(releaseLockRace)
	predecessor.awaitShutdown(t)
	if got := successorStarts.Load(); got != 0 {
		t.Fatalf("successor starts while predecessor lingers = %d, want 0", got)
	}
	predecessor.release()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Replace() error = %v", err)
		}
	}
	if got := successorStarts.Load(); got != 1 {
		t.Fatalf("concurrent replacement successor starts = %d, want 1", got)
	}
}

func TestLauncherGlobalStopSerializesWithReplace(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	launcher.BinPath = "true"
	predecessor := startLingeringDaemonProcess(t, launcher)

	var shutdownCalls atomic.Int32
	var successorStarts atomic.Int32
	successorReady := atomic.Bool{}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		if shutdownCalls.Add(1) == 1 {
			return predecessor.cmd.Process.Signal(syscall.SIGTERM)
		}
		return errors.New("daemon socket already stopped")
	}
	launcher.waitForReady = func(context.Context, string) error {
		if successorReady.Load() {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
	launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
		successorStarts.Add(1)
		successorReady.Store(true)
		return &recordingDaemonProcess{exitCh: make(chan error)}, nil
	}
	replaceQueued := make(chan struct{}, 1)
	launcher.beforeReplaceLock = func() { replaceQueued <- struct{}{} }

	stopDone := make(chan error, 1)
	go func() { stopDone <- launcher.Stop(context.Background()) }()
	predecessor.awaitShutdown(t)
	replaceDone := make(chan error, 1)
	go func() { replaceDone <- launcher.Replace(context.Background()) }()
	<-replaceQueued
	if got := shutdownCalls.Load(); got != 1 {
		t.Fatalf("shutdown calls while global Stop owns lifecycle lock = %d, want 1", got)
	}
	if got := successorStarts.Load(); got != 0 {
		t.Fatalf("successor starts while global Stop owns lifecycle lock = %d, want 0", got)
	}
	predecessor.release()
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := <-replaceDone; err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if got := successorStarts.Load(); got != 1 {
		t.Fatalf("successor starts after serialized global Stop/Replace = %d, want 1", got)
	}
}

func TestLauncherGlobalStartSerializesWithStopAndReplace(t *testing.T) {
	for _, testCase := range []struct {
		name string
		run  func(*Launcher) error
	}{
		{name: "stop", run: func(launcher *Launcher) error { return launcher.Stop(context.Background()) }},
		{name: "replace", run: func(launcher *Launcher) error { return launcher.Replace(context.Background()) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
			launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
			if testCase.name == "replace" {
				launcher.replacementSuccessorVerifier = func(daemonCommand) error { return nil }
			}
			launcher.BinPath = "true"
			predecessor := startLingeringDaemonProcess(t, launcher)

			lifecycleEntered := make(chan struct{})
			releaseShutdown := make(chan struct{})
			launcher.shutdownViaSocket = func(context.Context, string) error {
				close(lifecycleEntered)
				<-releaseShutdown
				return predecessor.cmd.Process.Signal(syscall.SIGTERM)
			}
			// A ready socket must not let canonical global Start bypass a
			// lifecycle operation that already owns the exact-scope flock.
			launcher.waitForReady = func(context.Context, string) error { return nil }

			lifecycleDone := make(chan error, 1)
			go func() { lifecycleDone <- testCase.run(launcher) }()
			<-lifecycleEntered
			startDone := make(chan error, 1)
			go func() { startDone <- launcher.Start(context.Background()) }()
			select {
			case err := <-startDone:
				t.Fatalf("global Start returned while %s owned lifecycle lock: %v", testCase.name, err)
			default:
			}

			close(releaseShutdown)
			predecessor.awaitShutdown(t)
			predecessor.release()
			if err := <-lifecycleDone; err != nil {
				t.Fatalf("%s error = %v", testCase.name, err)
			}
			if err := <-startDone; err != nil {
				t.Fatalf("Start() after serialized %s error = %v", testCase.name, err)
			}
		})
	}
}

func TestLauncherGlobalStartCancellationDoesNotBypassLifecycleLock(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	launcher := NewLauncher(t.TempDir(), config.GlobalDaemonSocketPath())
	launcher.BinPath = "true"
	launcher.waitForReady = func(context.Context, string) error { return nil }

	lifecyclePath := launcher.scopedLifecycleLockPath()
	if err := os.MkdirAll(filepath.Dir(lifecyclePath), 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycleFile, err := os.OpenFile(lifecyclePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycleFile.Close()
	if err := syscall.Flock(int(lifecycleFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lifecycleFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = launcher.Start(ctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want canonical lifecycle-lock cancellation", err)
	}
}

func TestLauncherStart_ErrorsWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.terminateLockOwner = func(string) error { return errors.New("kill denied") }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	err = launcher.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "recover stale daemon lock owner") {
		t.Fatalf("Start() error = %v, want lock recovery failure", err)
	}
}

func TestLauncherStart_FailsClosedWhenTerminateReturnsEPERM(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return syscall.EPERM
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("Start() error = %v, want EPERM fail-closed error", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); err != nil {
		t.Fatalf("lock file should remain after permission failure, stat err = %v", err)
	}
	if len(starter.specs) != 0 || tracker.closed.Load() {
		t.Fatalf("successor starts/log opens = %d/%t, want 0/false", len(starter.specs), tracker.closed.Load())
	}
}

func TestLauncherStart_FailsClosedWhenTerminateReturnsWrappedPermissionDenied(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return lifecycle.ErrLockOwnerPermissionDenied
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); !errors.Is(err, lifecycle.ErrLockOwnerPermissionDenied) {
		t.Fatalf("Start() error = %v, want permission-denied fail-closed error", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); err != nil {
		t.Fatalf("lock file should remain after permission failure, stat err = %v", err)
	}
	if len(starter.specs) != 0 || tracker.closed.Load() {
		t.Fatalf("successor starts/log opens = %d/%t, want 0/false", len(starter.specs), tracker.closed.Load())
	}
}

func TestLauncherStart_FailsClosedWhenTerminateReturnsTerminationTimeout(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return fmt.Errorf("%w: pid %d", lifecycle.ErrLockOwnerTerminationTimeout, os.Getpid())
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); !errors.Is(err, lifecycle.ErrLockOwnerTerminationTimeout) {
		t.Fatalf("Start() error = %v, want termination-timeout fail-closed error", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); err != nil {
		t.Fatalf("lock file should remain after termination timeout, stat err = %v", err)
	}
	if len(starter.specs) != 0 || tracker.closed.Load() {
		t.Fatalf("successor starts/log opens = %d/%t, want 0/false", len(starter.specs), tracker.closed.Load())
	}
}

func TestLauncherStart_FailsClosedWhenTerminateReturnsPermissionDeniedString(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	tracker := &trackingWriteCloser{}

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	launcher.sleepFn = func(time.Duration) {}
	terminateCalls := 0
	launcher.terminateLockOwner = func(string) error {
		terminateCalls++
		return errors.New("lock owner permission denied: operation not permitted")
	}

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return tracker, nil }

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Start() error = %v, want permission-denied fail-closed error", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
	if terminateCalls != 1 {
		t.Fatalf("terminate lock owner call count = %d, want 1", terminateCalls)
	}
	if _, err := os.Stat(launcher.LockPath); err != nil {
		t.Fatalf("lock file should remain after permission failure, stat err = %v", err)
	}
	if len(starter.specs) != 0 || tracker.closed.Load() {
		t.Fatalf("successor starts/log opens = %d/%t, want 0/false", len(starter.specs), tracker.closed.Load())
	}
}

func TestLauncherStart_RechecksSocketWhenLockRecoveryFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.sleepFn = func(time.Duration) {}
	launcher.terminateLockOwner = func(string) error { return errors.New("kill denied") }

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls < 4 {
			return context.DeadlineExceeded
		}
		return nil
	}

	lockRecordBytes, err := json.Marshal(map[string]any{
		"pid":        os.Getpid(),
		"created_at": time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Marshal(lockRecord): %v", err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecordBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(lock): %v", err)
	}

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil after recheck-ready", err)
	}
	if readyCalls != 4 {
		t.Fatalf("waitForReady call count = %d, want 4", readyCalls)
	}
}

func TestLauncherStartHonorsCallerCancellationForReadyWait(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")

	launcher := NewLauncher(repoDir, socketPath)
	starter := useRecordingDaemonStarter(launcher)
	launcher.BinPath = "true"
	waitEntered := make(chan struct{})
	var readyCalls atomic.Int32
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		if readyCalls.Add(1) <= 2 {
			return context.DeadlineExceeded
		}
		close(waitEntered)
		<-ctx.Done()
		return ctx.Err()
	}
	launcher.sleepFn = func(time.Duration) {}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return &trackingWriteCloser{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- launcher.Start(ctx) }()
	<-waitEntered
	cancel()
	err := <-result
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context canceled", err)
	}
	if got := starter.process.stopCalls.Load(); got != 1 {
		t.Fatalf("spawn cleanup calls = %d, want 1", got)
	}
}

func TestLauncherStartReportsSpawnCleanupFailure(t *testing.T) {
	repoDir := t.TempDir()
	launcher := NewLauncher(repoDir, filepath.Join(t.TempDir(), "daemon.sock"))
	starter := useRecordingDaemonStarter(launcher)
	starter.process.stopErr = errors.New("cleanup denied")
	launcher.BinPath = "azd-test"
	launcher.waitForReady = func(context.Context, string) error { return context.DeadlineExceeded }
	launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }

	err := launcher.Start(context.Background())
	if err == nil || !errors.Is(err, starter.process.stopErr) || !strings.Contains(err.Error(), "cleanup spawned daemon") {
		t.Fatalf("Start() error = %v, want readiness and cleanup failure", err)
	}
	if got := starter.process.stopCalls.Load(); got != 1 {
		t.Fatalf("spawn cleanup calls = %d, want 1", got)
	}
}

func TestLauncherStartProcessSupervisorPreservesExitPublishedAfterInitialProbe(t *testing.T) {
	done := make(chan error)
	process := &execDaemonProcess{
		cmd:  &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done: done,
		signalProcessGroup: func(signal syscall.Signal) error {
			if signal != syscall.SIGTERM {
				t.Fatalf("signal = %v, want SIGTERM", signal)
			}
			go func() { done <- errors.New("exit status 7") }()
			return syscall.ESRCH
		},
	}

	err := process.stopAndWait(context.Background())
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("stopAndWait() error = %v, want observable pre-readiness exit status", err)
	}
}

func TestLauncherStartProcessSupervisorPreservesExitAfterSignalPermissionRace(t *testing.T) {
	done := make(chan error)
	process := &execDaemonProcess{
		cmd:  &exec.Cmd{Process: &os.Process{Pid: 424242}},
		done: done,
		signalProcessGroup: func(signal syscall.Signal) error {
			if signal != syscall.SIGTERM {
				t.Fatalf("signal = %v, want SIGTERM", signal)
			}
			go func() { done <- errors.New("exit status 7") }()
			return syscall.EPERM
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := process.stopAndWait(ctx)
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("stopAndWait() error = %v, want observable pre-readiness exit status", err)
	}
}

func TestRealProcessProfileLauncherReportsExitBeforeReadiness(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "azd-test")
	script := "#!/bin/sh\nexit 7\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := NewLauncher(filepath.Join(root, "repo"), filepath.Join(root, "daemon.sock"))
	launcher.BinPath = executable
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}

	err := launcher.Start(context.Background())
	if err == nil || !errors.Is(err, errSpawnedDaemonExited) || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("Start() error = %v, want observable pre-readiness exit status", err)
	}
}

func TestRealProcessProfileLauncherReadinessFailureCleansExactLaunch(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "azd-test")
	writeOwnedExecutableWrapper(t, executable)
	identityPath := configureOwnedExecutableHelper(t, "readiness-failure")
	controlDir, err := os.MkdirTemp("/tmp", "azedarach-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(controlDir) })
	controlPath := filepath.Join(controlDir, "ready.sock")
	listener, err := net.Listen("unix", controlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	t.Setenv(ownedExecutableControlSocketEnv, controlPath)
	launcher := NewLauncher(repoDir, filepath.Join(root, "daemon.sock"))
	launcher.BinPath = executable
	readyCalls := 0
	launcher.waitForReady = func(ctx context.Context, _ string) error {
		readyCalls++
		if readyCalls < 3 {
			return context.DeadlineExceeded
		}
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		defer connection.Close()
		var ready [1]byte
		if _, err := io.ReadFull(connection, ready[:]); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}
	launcher.openLogFile = func(string) (io.WriteCloser, error) {
		return os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}

	err = launcher.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "spawned daemon cleaned up") {
		t.Fatalf("Start() error = %v, want observable spawned-process cleanup", err)
	}
	identity := readOwnedExecutableIdentity(t, identityPath)
	wantArgs := []string{"--repo", repoDir, "--socket", launcher.SocketPath, "--lock", launcher.LockPath}
	if len(identity.arguments) < len(wantArgs) || !slices.Equal(identity.arguments[len(identity.arguments)-len(wantArgs):], wantArgs) {
		t.Fatalf("spawned args = %q, want canonical suffix %q", identity.arguments, wantArgs)
	}
	assertOwnedExecutableRetired(t, identityPath)
}

func TestLauncherStart_SocketReadySkipsSpawnWithoutLock(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")
	launcher.waitForReady = func(context.Context, string) error { return nil }

	if err := launcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil when socket is already ready", err)
	}
}

func TestLauncherStart_SocketReadyWhileWaitingForStartLockReturnsNil(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.BinPath = filepath.Join(t.TempDir(), "missing-azd")

	startLockPath := launcher.LockPath + ".start"
	if err := os.MkdirAll(filepath.Dir(startLockPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(start lock dir): %v", err)
	}
	lockFile, err := os.OpenFile(startLockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(start lock): %v", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		t.Fatalf("Flock(start lock): %v", err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	readyCalls := 0
	launcher.waitForReady = func(context.Context, string) error {
		readyCalls++
		if readyCalls >= 2 {
			return nil
		}
		return context.DeadlineExceeded
	}
	launcher.sleepFn = func(time.Duration) {}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	if err := launcher.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v, want nil when socket becomes ready under lock contention", err)
	}
	if readyCalls < 2 {
		t.Fatalf("waitForReady call count = %d, want >= 2", readyCalls)
	}
}

func TestLauncherStopUsesTerminateLockOwner(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("socket unavailable") }

	called := false
	var gotLockPath string
	launcher.terminateLockOwner = func(lockPath string) error {
		called = true
		gotLockPath = lockPath
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !called {
		t.Fatal("expected terminateLockOwner to be called")
	}
	if gotLockPath != launcher.LockPath {
		t.Fatalf("lock path = %q, want %q", gotLockPath, launcher.LockPath)
	}
}

func TestProcessIdentityRejectsReusedPID(t *testing.T) {
	identity, present, err := captureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("captureProcessIdentity(current process): %v", err)
	}
	if !present {
		t.Fatal("current process identity was reported absent")
	}

	reused := identity
	reused.startToken += "-different-process"
	alive, err := processIdentityAlive(reused)
	if err != nil {
		t.Fatalf("processIdentityAlive(reused PID): %v", err)
	}
	if alive {
		t.Fatalf("process identity %+v remained alive after start token changed", reused)
	}
}

func TestLauncherStopWaitsForExactProcessAfterLockRelease(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.SocketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	owner := processIdentity{pid: 4242, startToken: "exact-owner"}
	lockRecord, err := json.Marshal(map[string]any{"pid": owner.pid, "created_at": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return errors.Join(os.Remove(launcher.LockPath), os.Remove(launcher.SocketPath))
	}
	launcher.captureOwner = func() (processIdentity, bool, error) { return owner, true, nil }
	waitEntered := make(chan struct{})
	allowExit := make(chan struct{})
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
		close(waitEntered)
		<-allowExit
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- launcher.Stop(context.Background()) }()
	<-waitEntered
	select {
	case err := <-result:
		t.Fatalf("Stop() returned before exact process exit proof: %v", err)
	default:
	}
	close(allowExit)
	if err := <-result; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scoped runtime dir stat error = %v, want cleanup after exit proof", err)
	}
}

func TestLauncherStopPropagatesCancellationDuringExactProcessExitWait(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := processIdentity{pid: 4242, startToken: "exact-owner"}
	lockRecord, err := json.Marshal(map[string]any{"pid": owner.pid, "created_at": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error { return nil }
	launcher.captureOwner = func() (processIdentity, bool, error) { return owner, true, nil }
	waitEntered := make(chan struct{})
	launcher.waitForOwnerExit = func(ctx context.Context, _ processIdentity) error {
		close(waitEntered)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- launcher.Stop(ctx) }()
	<-waitEntered
	cancel()
	err = <-result
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want caller cancellation", err)
	}
}

func TestLauncherStopRejectsLiveCanonicalSocketWithoutOwnerIdentity(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.SocketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return os.Remove(launcher.SocketPath)
	}

	err := launcher.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing daemon lock owner identity") {
		t.Fatalf("Stop() error = %v, want missing owner identity failure", err)
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Fatalf("runtime changed despite missing owner identity: %v", err)
	}
}

func TestScopedLifecycleLockSerializesStopAndConcurrentStarts(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := config.ScopedDaemonSocketPath(repoDir)
	if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ScopedDaemonLockPath(repoDir), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	var ready atomic.Bool
	ready.Store(true)
	stopEntered := make(chan struct{})
	allowStop := make(chan struct{})
	stopLauncher := NewLauncher(repoDir, socketPath)
	stopLauncher.waitForReady = func(context.Context, string) error {
		if ready.Load() {
			return nil
		}
		return context.DeadlineExceeded
	}
	stopLauncher.shutdownViaSocket = func(context.Context, string) error {
		close(stopEntered)
		<-allowStop
		ready.Store(false)
		if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- stopLauncher.Stop(context.Background()) }()
	<-stopEntered

	var spawnCount atomic.Int32
	start := func() <-chan error {
		done := make(chan error, 1)
		launcher := NewLauncher(repoDir, socketPath)
		launcher.BinPath = "fixture-azd"
		launcher.openLogFile = func(string) (io.WriteCloser, error) { return &trackingWriteCloser{}, nil }
		launcher.waitForReady = func(context.Context, string) error {
			if ready.Load() {
				return nil
			}
			return context.DeadlineExceeded
		}
		launcher.startProcess = func(daemonProcessSpec) (daemonProcess, error) {
			spawnCount.Add(1)
			ready.Store(true)
			return &recordingDaemonProcess{exitCh: make(chan error)}, nil
		}
		go func() { done <- launcher.Start(context.Background()) }()
		return done
	}
	startOne := start()
	startTwo := start()
	select {
	case err := <-startOne:
		t.Fatalf("first Start returned before Stop released lifecycle lock: %v", err)
	case err := <-startTwo:
		t.Fatalf("second Start returned before Stop released lifecycle lock: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	close(allowStop)
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	for i, done := range []<-chan error{startOne, startTwo} {
		if err := <-done; err != nil {
			t.Fatalf("Start %d error = %v", i+1, err)
		}
	}
	if got := spawnCount.Load(); got != 1 {
		t.Fatalf("spawn count = %d, want one daemon after serialized Stop/Start race", got)
	}
}

func TestLauncherStopWrapsTerminateError(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)
	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("socket unavailable") }
	launcher.terminateLockOwner = func(string) error { return errors.New("boom") }

	err := launcher.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "terminate daemon lock owner: boom") {
		t.Fatalf("Stop() error = %v, want wrapped terminate error", err)
	}
}

func TestLauncherStopUsesGracefulSocketShutdownWhenAvailable(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	socketShutdownCalls := 0
	launcher.shutdownViaSocket = func(context.Context, string) error {
		socketShutdownCalls++
		return nil
	}
	terminateCalled := false
	launcher.terminateLockOwner = func(string) error {
		terminateCalled = true
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if socketShutdownCalls != 1 {
		t.Fatalf("socket shutdown calls = %d, want 1", socketShutdownCalls)
	}
	if terminateCalled {
		t.Fatal("terminateLockOwner should not be called when graceful socket shutdown succeeds")
	}
}

func TestLauncherStopAttributesGracefulShutdownReason(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	var gotReason string
	launcher.shutdownWithReason = func(_ context.Context, _ string, reason string) error {
		gotReason = reason
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if gotReason != "stop" {
		t.Fatalf("shutdown reason = %q, want stop", gotReason)
	}
}

func TestLauncherStopFallsBackWhenGracefulSocketShutdownFails(t *testing.T) {
	repoDir := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	launcher := NewLauncher(repoDir, socketPath)

	launcher.shutdownViaSocket = func(context.Context, string) error { return errors.New("rpc failed") }
	terminateCalled := false
	launcher.terminateLockOwner = func(string) error {
		terminateCalled = true
		return nil
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !terminateCalled {
		t.Fatal("expected terminateLockOwner fallback when graceful socket shutdown fails")
	}
}

func TestLauncherStopRemovesCanonicalScopedRuntimeAssets(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(filepath.Join(runtimeDir, "session-launch"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{launcher.SocketPath, launcher.LockPath, launcher.LockPath + ".start"} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(launcher.LockPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		if err := os.Remove(launcher.SocketPath); err != nil {
			return err
		}
		return os.Remove(launcher.LockPath)
	}

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scoped runtime dir still exists after stop: %v", err)
	}
}

func TestLauncherStopTreatsAbsentCanonicalScopedRuntimeAsClean(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	launcher.shutdownViaSocket = func(context.Context, string) error { return nil }

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(config.ScopedDaemonRuntimeDir(repoDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent scoped runtime was unexpectedly created: %v", err)
	}
}

func TestLauncherStopWaitsForGracefulScopedProcessExit(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := processIdentity{pid: 4242, startToken: "graceful-owner"}
	lockRecord, err := json.Marshal(map[string]any{"pid": owner.pid})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher.LockPath, lockRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error {
		return os.Remove(launcher.LockPath)
	}
	launcher.captureOwner = func() (processIdentity, bool, error) { return owner, true, nil }
	waitEntered := make(chan struct{})
	allowExit := make(chan struct{})
	launcher.waitForOwnerExit = func(context.Context, processIdentity) error {
		close(waitEntered)
		<-allowExit
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- launcher.Stop(context.Background()) }()
	<-waitEntered
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Fatalf("scoped runtime removed before graceful process exit proof: %v", err)
	}
	close(allowExit)
	if err := <-result; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scoped runtime dir still exists after delayed process exit: %v", err)
	}
}

func TestLauncherStopReportsScopedRuntimeCleanupResidue(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	repoDir := newLauncherTestWorktree(t)
	launcher := NewLauncher(repoDir, config.ScopedDaemonSocketPath(repoDir))
	runtimeDir := config.ScopedDaemonRuntimeDir(repoDir)
	if err := os.MkdirAll(filepath.Join(runtimeDir, "session-launch"), 0o700); err != nil {
		t.Fatal(err)
	}
	residue := filepath.Join(runtimeDir, "unexpected")
	if err := os.WriteFile(residue, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error { return nil }

	err := launcher.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "clean worktree-scoped daemon runtime") {
		t.Fatalf("Stop() error = %v, want scoped cleanup failure", err)
	}
	if data, readErr := os.ReadFile(residue); readErr != nil || string(data) != "preserve" {
		t.Fatalf("unexpected residue was changed: data=%q err=%v", data, readErr)
	}
}

func TestLauncherStopNeverCleansGlobalRuntimeAssets(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	repoDir := t.TempDir()
	launcher := NewLauncher(repoDir, config.GlobalDaemonSocketPath())
	startLock := launcher.LockPath + ".start"
	if err := os.MkdirAll(filepath.Dir(startLock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(startLock, []byte("global sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher.shutdownViaSocket = func(context.Context, string) error { return nil }

	if err := launcher.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if data, err := os.ReadFile(startLock); err != nil || string(data) != "global sentinel" {
		t.Fatalf("global runtime asset changed: data=%q err=%v", data, err)
	}
}
