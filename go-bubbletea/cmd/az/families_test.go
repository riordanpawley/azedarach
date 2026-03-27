package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
)

func TestRunHooksCommandHelpAndDispatch(t *testing.T) {
	output := captureMainStdout(t, func() error {
		return runHooksCommand(config.DefaultConfig(), []string{"--help"})
	})
	if !strings.Contains(output, "Usage: az hooks install <issue-id>") {
		t.Fatalf("help output = %q", output)
	}

	projectDir := t.TempDir()
	output = captureMainStdout(t, func() error {
		return runHooksCommand(config.DefaultConfig(), []string{"install", "--project-dir", projectDir, "az-123"})
	})
	if !strings.Contains(output, "Installed hooks for issue az-123") {
		t.Fatalf("dispatch output = %q", output)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("expected hooks file: %v", err)
	}
}

func TestRunDevGateAndNotifyCommands(t *testing.T) {
	gateOut := captureMainStdout(t, func() error {
		return runDevCommand(config.DefaultConfig(), []string{"gate", "--verbose", "--project-dir", "/tmp/dev", "az-123"})
	})
	if !strings.Contains(gateOut, "Running quality gates for: az-123") {
		t.Fatalf("gate output = %q", gateOut)
	}

	notifyOut := captureMainStdout(t, func() error {
		return runNotifyCommand(config.DefaultConfig(), []string{"--verbose", "stop", "az-123"})
	})
	if !strings.Contains(notifyOut, "Hook notification: stop for az-123 -> stopped") {
		t.Fatalf("notify output = %q", notifyOut)
	}
}

func TestRunOpenCodeCommandDispatch(t *testing.T) {
	projectDir := t.TempDir()
	output := captureMainStdout(t, func() error {
		return runOpenCodeCommand(config.DefaultConfig(), []string{"init", "--project-dir", projectDir})
	})
	if !strings.Contains(output, "Initialized OpenCode support in") {
		t.Fatalf("init output = %q", output)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "opencode.json")); err != nil {
		t.Fatalf("expected opencode.json: %v", err)
	}
}

func TestRunOpenCodeCommandInitWithoutArgs(t *testing.T) {
	projectDir := t.TempDir()
	withWorkingDir(t, projectDir, func() {
		output := captureMainStdout(t, func() error {
			return runOpenCodeCommand(config.DefaultConfig(), []string{"init"})
		})
		if !strings.Contains(output, "Initialized OpenCode support in") {
			t.Fatalf("init output = %q", output)
		}
		if _, err := os.Stat(filepath.Join(projectDir, "opencode.json")); err != nil {
			t.Fatalf("expected opencode.json: %v", err)
		}
	})
}

func captureMainStdout(t *testing.T, fn func() error) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- fn()
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)

	os.Stdout = oldStdout
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stdout: %v", copyErr)
	}
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	return buf.String()
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	fn()
}
