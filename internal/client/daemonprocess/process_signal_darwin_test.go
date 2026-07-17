//go:build darwin

package daemonprocess

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestDarwinAuditTokenSignalRejectsPIDVersionMismatch(t *testing.T) {
	handle, err := openPlatformProcessSignalHandle(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	reused := *(handle.(*darwinProcessSignalHandle))
	// Darwin documents the last audit-token word as PID-version. Changing it
	// deterministically models the exact PID with a different incarnation.
	reused.token[len(reused.token)-1]++
	if err := reused.Signal(syscall.SIGCONT); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("signal with mismatched PID-version error = %v, want ESRCH", err)
	}
}
