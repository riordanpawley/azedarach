package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecRunnerRefusesMutatingTmuxCommandsInGoTests(t *testing.T) {
	t.Setenv("AZEDARACH_ALLOW_REAL_TMUX_IN_TESTS", "")

	var runner ExecRunner
	_, err := runner.Run(context.Background(), "new-session", "-d", "-s", "azedarach-test-guard")
	if err == nil {
		t.Fatal("expected mutating tmux command to be refused in go test")
	}
	if !strings.Contains(err.Error(), "refusing to run tmux command") {
		t.Fatalf("error = %q, want test tmux mutation guard", err)
	}
}

func TestExecRunnerPreservesCombinedOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nprintf 'stdout detail\\n'\nprintf 'stderr detail\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir)

	var runner ExecRunner
	out, err := runner.Run(context.Background(), "display-message", "-p", "test")
	if err == nil {
		t.Fatal("expected fake tmux failure")
	}
	if !strings.Contains(out, "stdout detail") || !strings.Contains(out, "stderr detail") {
		t.Fatalf("output = %q, want combined stdout/stderr", out)
	}
	if !strings.Contains(err.Error(), "stdout detail") || !strings.Contains(err.Error(), "stderr detail") {
		t.Fatalf("error = %q, want combined stdout/stderr", err.Error())
	}
}
