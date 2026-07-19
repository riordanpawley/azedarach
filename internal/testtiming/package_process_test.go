package testtiming

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicLineWritersKeepConcurrentJSONStreamsDistinct(t *testing.T) {
	var raw bytes.Buffer
	collector := NewEventCollector(&raw)
	first := &atomicLineWriter{destination: collector}
	second := &atomicLineWriter{destination: collector}
	require.NoError(t, writeChunks(first, `{"Action":"pass",`, `"Package":"example/one","Elapsed":1}`+"\n"))
	require.NoError(t, writeChunks(second, `{"Action":"pass",`, `"Package":"example/two","Elapsed":1}`+"\n"))
	require.NoError(t, first.Finish())
	require.NoError(t, second.Finish())
	collector.Finish()
	packages, _, _, invalid := collector.Results()
	assert.Zero(t, invalid)
	require.Len(t, packages, 2)
	assert.Contains(t, raw.String(), `"Package":"example/one"`)
	assert.Contains(t, raw.String(), `"Package":"example/two"`)
}

func writeChunks(w io.Writer, chunks ...string) error {
	for _, chunk := range chunks {
		if _, err := io.WriteString(w, chunk); err != nil {
			return err
		}
	}
	return nil
}

func TestRunConcurrentCommandsStartsEveryPackageBeforeWaiting(t *testing.T) {
	type gate struct {
		readyRead    *os.File
		releaseWrite *os.File
	}
	commands := make([]*exec.Cmd, 0, 2)
	gates := make([]gate, 0, 2)
	for _, identity := range []string{"clone-one", "clone-two"} {
		readyRead, readyWrite, err := os.Pipe()
		require.NoError(t, err)
		releaseRead, releaseWrite, err := os.Pipe()
		require.NoError(t, err)
		command := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestPackageProcessBarrierHelper$")
		command.Env = append(os.Environ(), "AZEDARACH_TEST_PACKAGE_PROCESS_HELPER=1", "AZEDARACH_USER_DB_CLONE="+identity)
		command.ExtraFiles = []*os.File{readyWrite, releaseRead}
		commands = append(commands, command)
		gates = append(gates, gate{readyRead: readyRead, releaseWrite: releaseWrite})
		t.Cleanup(func() {
			_ = readyRead.Close()
			_ = readyWrite.Close()
			_ = releaseRead.Close()
			_ = releaseWrite.Close()
		})
	}
	type outcome struct {
		execution processExecution
		err       error
	}
	done := make(chan outcome, 1)
	go func() {
		execution, err := runConcurrentCommands(context.Background(), commands, io.Discard, io.Discard)
		done <- outcome{execution: execution, err: err}
	}()

	identities := make([]string, 0, len(gates))
	for _, packageGate := range gates {
		identity, err := bufio.NewReader(packageGate.readyRead).ReadString('\n')
		require.NoError(t, err)
		identities = append(identities, strings.TrimSpace(identity))
	}
	assert.ElementsMatch(t, []string{"clone-one", "clone-two"}, identities)
	for _, packageGate := range gates {
		require.NoError(t, packageGate.releaseWrite.Close())
	}
	result := <-done
	require.NoError(t, result.err)
	assert.Zero(t, result.execution.ExitCode)
}

func TestPackageProcessBarrierHelper(t *testing.T) {
	if os.Getenv("AZEDARACH_TEST_PACKAGE_PROCESS_HELPER") != "1" {
		return
	}
	ready := os.NewFile(3, "ready")
	release := os.NewFile(4, "release")
	if _, err := fmt.Fprintln(ready, os.Getenv("AZEDARACH_USER_DB_CLONE")); err != nil {
		t.Fatal(err)
	}
	if err := ready.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, release); err != nil {
		t.Fatal(err)
	}
}
