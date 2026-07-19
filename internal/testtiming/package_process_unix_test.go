//go:build unix

package testtiming

import (
	"bufio"
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
	require.ErrorContains(t, runErr, startFailure.Error())
	require.Positive(t, descendantPID)
	assert.False(t, processExists(descendantPID), "descendant survived partial-start rollback")
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
	}
}
