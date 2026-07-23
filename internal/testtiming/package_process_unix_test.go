//go:build unix

package testtiming

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

type barrierLifecycleReader struct {
	entered chan struct{}
	release chan struct{}
	reads   atomic.Int32
}

func (reader *barrierLifecycleReader) Read([]byte) (int, error) {
	reader.reads.Add(1)
	close(reader.entered)
	<-reader.release
	return 0, io.EOF
}

type gatedLifecycleReadCloser struct {
	io.ReadCloser
	exited  chan struct{}
	release chan struct{}
}

func (reader *gatedLifecycleReadCloser) Read(p []byte) (int, error) {
	count, err := reader.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		close(reader.exited)
		<-reader.release
	}
	return count, err
}

func TestWaitForProcessGroupLifetimeBlocksOnOSLifecycleBarrier(t *testing.T) {
	reader := &barrierLifecycleReader{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- waitForProcessGroupLifetime(reader)
	}()
	<-reader.entered
	close(reader.release)
	require.NoError(t, <-done)
	assert.Equal(t, int32(1), reader.reads.Load(), "lifecycle wait must block in one OS-backed read instead of polling")
}

func TestRunConcurrentCommandsStartFailureKillsStartedDescendants(t *testing.T) {
	readyRead, readyWrite, err := os.Pipe()
	require.NoError(t, err)
	releaseRead, releaseWrite, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		_ = releaseRead.Close()
		_ = releaseWrite.Close()
	})
	started := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestPackageProcessDescendantHelper$")
	started.Env = append(os.Environ(), "AZEDARACH_TEST_PACKAGE_DESCENDANT_HELPER=parent")
	started.ExtraFiles = []*os.File{readyWrite, releaseRead}
	require.NoError(t, configureProcessGroup(started))
	notStarted := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestPackageProcessDescendantHelper$")
	require.NoError(t, configureProcessGroup(notStarted))
	var descendantPID int
	startFailure := errors.New("deterministic second-command start failure")
	lifecycleValue, configured := processGroupLifecycles.Load(started)
	require.True(t, configured)
	lifecycle := lifecycleValue.(*processGroupLifecycle)
	exited := make(chan struct{})
	releaseExit := make(chan struct{})
	lifecycle.reader = &gatedLifecycleReadCloser{
		ReadCloser: lifecycle.reader,
		exited:     exited,
		release:    releaseExit,
	}

	done := make(chan error, 1)
	go func() {
		_, runErr := runConcurrentCommandsWithStarter(context.Background(), []*exec.Cmd{started, notStarted}, io.Discard, io.Discard, func(command *exec.Cmd) error {
			if command == started {
				return command.Start()
			}
			pidText, readErr := bufio.NewReader(readyRead).ReadString('\n')
			if readErr != nil {
				return readErr
			}
			descendantPID, readErr = strconv.Atoi(strings.TrimSpace(pidText))
			if readErr != nil {
				return readErr
			}
			return startFailure
		})
		done <- runErr
	}()

	<-exited
	select {
	case runErr := <-done:
		require.Fail(t, "partial-start rollback returned before descendant-exit barrier", "%v", runErr)
	default:
	}
	close(releaseExit)
	runErr := <-done
	require.ErrorContains(t, runErr, startFailure.Error())
	require.Positive(t, descendantPID)
	_, startedRetained := processGroupLifecycles.Load(started)
	assert.False(t, startedRetained, "started command retained lifecycle state after rollback")
	_, notStartedRetained := processGroupLifecycles.Load(notStarted)
	assert.False(t, notStartedRetained, "never-started command retained lifecycle state after rollback")
}

func TestRunProcessGroupSupervisesLeaderExitWithSurvivingDescendant(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mode      string
		wantError bool
	}{
		{name: "successful leader", mode: "leader-success"},
		{name: "failing leader", mode: "leader-error", wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			command, readyRead := survivingDescendantCommand(t, testCase.mode)
			var output bytes.Buffer
			command.Stdout = &output
			command.Stderr = &output
			done := make(chan error, 1)
			go func() { done <- runProcessGroup(command) }()
			descendantPID := readDescendantPID(t, readyRead)
			runErr := <-done
			if testCase.wantError {
				require.Error(t, runErr)
			} else {
				require.NoError(t, runErr)
			}
			assert.False(t, processExists(descendantPID), "descendant survived leader-exit supervision")
		})
	}
}

func TestTerminateProcessGroupAfterLeaderExitKillsAnchoredDescendant(t *testing.T) {
	command, readyRead := survivingDescendantCommand(t, "leader-success")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	require.NoError(t, startProcessGroup(command, func(command *exec.Cmd) error { return command.Start() }))
	descendantPID := readDescendantPID(t, readyRead)
	require.NoError(t, command.Wait())
	require.NoError(t, terminateProcessGroup(command))
	require.NoError(t, waitForProcessGroupExit(command))
	assert.False(t, processExists(descendantPID), "post-leader signal did not kill anchored descendant")
}

func survivingDescendantCommand(t *testing.T, mode string) (*exec.Cmd, *os.File) {
	t.Helper()
	readyRead, readyWrite, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = readyRead.Close()
		_ = readyWrite.Close()
	})
	command := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestPackageProcessDescendantHelper$")
	command.Env = append(os.Environ(), "AZEDARACH_TEST_PACKAGE_DESCENDANT_HELPER="+mode)
	command.ExtraFiles = []*os.File{readyWrite}
	require.NoError(t, configureProcessGroup(command))
	return command, readyRead
}

func readDescendantPID(t *testing.T, ready *os.File) int {
	t.Helper()
	pidText, err := bufio.NewReader(ready).ReadString('\n')
	require.NoError(t, err)
	descendantPID, err := strconv.Atoi(strings.TrimSpace(pidText))
	require.NoError(t, err)
	require.Positive(t, descendantPID)
	return descendantPID
}

func TestPackageProcessDescendantHelper(t *testing.T) {
	switch os.Getenv("AZEDARACH_TEST_PACKAGE_DESCENDANT_HELPER") {
	case "parent":
		ready := os.NewFile(3, "ready")
		release := os.NewFile(4, "release")
		descendant := exec.Command(os.Args[0], "-test.run=^TestPackageProcessDescendantHelper$")
		descendant.Env = append(os.Environ(), "AZEDARACH_TEST_PACKAGE_DESCENDANT_HELPER=leaf")
		descendant.ExtraFiles = []*os.File{release}
		if err := descendant.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(ready, descendant.Process.Pid); err != nil {
			t.Fatal(err)
		}
		_ = ready.Close()
		if err := descendant.Wait(); err != nil {
			t.Fatal(err)
		}
	case "leaf":
		release := os.NewFile(3, "release")
		if _, err := io.Copy(io.Discard, release); err != nil {
			t.Fatal(err)
		}
	case "leader-success", "leader-error":
		mode := os.Getenv("AZEDARACH_TEST_PACKAGE_DESCENDANT_HELPER")
		ready := os.NewFile(3, "ready")
		descendant := exec.Command(os.Args[0], "-test.run=^TestPackageProcessDescendantHelper$")
		descendant.Env = append(os.Environ(), "AZEDARACH_TEST_PACKAGE_DESCENDANT_HELPER=surviving-leaf")
		descendant.Stdout = os.Stdout
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(ready, descendant.Process.Pid); err != nil {
			t.Fatal(err)
		}
		_ = ready.Close()
		if mode == "leader-error" {
			os.Exit(7)
		}
	case "surviving-leaf":
		select {}
	}
}
