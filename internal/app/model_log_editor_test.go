package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func TestOpenLogEditorCmd_UsesExecProcessAndReturnsOpened(t *testing.T) {
	t.Setenv("EDITOR", "hx")
	logPath := filepath.Join(t.TempDir(), "az.log")
	if err := os.WriteFile(logPath, []byte("hello"), 0600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	originalExecProcess := execProcess
	t.Cleanup(func() {
		execProcess = originalExecProcess
	})

	var captured *exec.Cmd
	execProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		captured = c
		return func() tea.Msg {
			return fn(nil)
		}
	}

	cmd := (Model{}).openLogEditorCmd(logPath)
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	got, ok := msg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("message type = %T, want overlay.SelectionMsg", msg)
	}
	if got.Key != "event-log-opened" {
		t.Fatalf("message key = %q, want %q", got.Key, "event-log-opened")
	}
	if gotPath, _ := got.Value.(string); gotPath != logPath {
		t.Fatalf("message value = %v, want %q", got.Value, logPath)
	}
	if captured == nil {
		t.Fatal("expected exec command to be captured")
	}
	if filepath.Base(captured.Path) != "hx" {
		t.Fatalf("editor command path = %q, want basename %q", captured.Path, "hx")
	}
	if len(captured.Args) != 2 || captured.Args[1] != logPath {
		t.Fatalf("editor args = %#v, want [hx %s]", captured.Args, logPath)
	}
}

func TestOpenLogEditorCmd_ExecProcessErrorReturnsEventLogError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "az.log")
	if err := os.WriteFile(logPath, []byte("hello"), 0600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	originalExecProcess := execProcess
	t.Cleanup(func() {
		execProcess = originalExecProcess
	})

	runErr := errors.New("boom")
	execProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg {
			return fn(runErr)
		}
	}

	msg := (Model{}).openLogEditorCmd(logPath)()
	got, ok := msg.(overlay.SelectionMsg)
	if !ok {
		t.Fatalf("message type = %T, want overlay.SelectionMsg", msg)
	}
	if got.Key != "event-log-error" {
		t.Fatalf("message key = %q, want %q", got.Key, "event-log-error")
	}
	err, ok := got.Value.(error)
	if !ok {
		t.Fatalf("message value type = %T, want error", got.Value)
	}
	if !errors.Is(err, runErr) {
		t.Fatalf("error = %v, want wrapped %v", err, runErr)
	}
}
