//go:build unix

package testtiming

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCancellationKillsDescendantsAndRemovesPrivateCloneRoot(t *testing.T) {
	module := t.TempDir()
	configureTestCacheFamily(t, module)
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/cancellation\n\ngo 1.24.2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(module, "cancellation_test.go"), []byte(cancellationFixtureSource), 0o644))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	t.Setenv("AZEDARACH_TEST_CANCELLATION_BARRIER", listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, runErr := Run(ctx, RunOptions{
			Profile:                   Profile{Name: "cancellation-fixture", Packages: []string{"./..."}, GoTestArgs: []string{"-json", "-count=1"}, PackageIsolatedDBClones: true},
			OutputDir:                 filepath.Join(t.TempDir(), "artifacts"),
			WorkingDir:                module,
			PublishValidationEvidence: false,
		})
		done <- outcome{err: runErr}
	}()
	connection, err := listener.Accept()
	require.NoError(t, err)
	var observed struct {
		Root          string `json:"root"`
		DescendantPID int    `json:"descendant_pid"`
	}
	require.NoError(t, json.NewDecoder(connection).Decode(&observed))
	require.NoError(t, connection.Close())
	require.NotEmpty(t, observed.Root)
	require.Positive(t, observed.DescendantPID)
	assert.DirExists(t, observed.Root)
	cancel()
	result := <-done
	require.Error(t, result.err)
	assert.True(t, errors.Is(result.err, context.Canceled), "error = %v", result.err)
	assert.False(t, processExists(observed.DescendantPID), "descendant survived runner cancellation")
	assert.NoDirExists(t, observed.Root)
}

const cancellationFixtureSource = `package cancellation

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"testing"
)

func TestBlockUntilRunnerCancellation(t *testing.T) {
	connection, err := net.Dial("tcp", os.Getenv("AZEDARACH_TEST_CANCELLATION_BARRIER"))
	if err != nil { t.Fatal(err) }
	descendant := exec.Command(os.Args[0], "-test.run=^TestCancellationLeaf$")
	descendant.Env = append(os.Environ(), "AZEDARACH_TEST_CANCELLATION_LEAF=1")
	if err := descendant.Start(); err != nil { t.Fatal(err) }
	if err := json.NewEncoder(connection).Encode(map[string]any{
		"root": os.Getenv("AZEDARACH_TEST_ISOLATION_ROOT"),
		"descendant_pid": descendant.Process.Pid,
	}); err != nil { t.Fatal(err) }
	if err := connection.Close(); err != nil { t.Fatal(err) }
	if err := descendant.Wait(); err != nil { t.Fatal(err) }
}

func TestCancellationLeaf(t *testing.T) {
	if os.Getenv("AZEDARACH_TEST_CANCELLATION_LEAF") != "1" { return }
	select {}
}
`
