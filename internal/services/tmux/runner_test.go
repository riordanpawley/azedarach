package tmux

import (
	"context"
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
