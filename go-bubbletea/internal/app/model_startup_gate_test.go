package app

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func TestApplyStartupGate_AddsMandatoryToolToast(t *testing.T) {
	m := Model{}
	m.applyStartupGate(diagnostics.StartupGate{
		OverallState:     diagnostics.HealthCritical,
		MissingMandatory: []string{"tmux", "git"},
		BlockedActions:   map[string]string{"s": "requires tmux"},
		Errors:           []string{"Missing mandatory tool: git", "Missing mandatory tool: tmux"},
	})

	if len(m.toasts) == 0 {
		t.Fatalf("applyStartupGate() expected startup toast, got none")
	}

	last := m.toasts[len(m.toasts)-1]
	if last.Level != ToastError {
		t.Fatalf("applyStartupGate() toast level = %v, want %v", last.Level, ToastError)
	}

	if !strings.Contains(last.Message, "git, tmux") {
		t.Fatalf("applyStartupGate() toast message = %q, want sorted tool list", last.Message)
	}

	if m.startupGate.OverallState != diagnostics.HealthCritical {
		t.Fatalf("applyStartupGate() startup state = %v, want %v", m.startupGate.OverallState, diagnostics.HealthCritical)
	}
}

func TestHandleSelection_StartupBlockedActionPreservesNavigation(t *testing.T) {
	m := newTestModel()
	m.nav.SelectTask("az-1", 0)
	before := getCursorPosition(m)

	m.startupGate = diagnostics.StartupGate{
		BlockedActions: map[string]string{
			"s": "requires tmux",
		},
	}

	updated, cmd := m.handleSelection(overlay.SelectionMsg{Key: "s"})
	newModel := updated.(Model)

	if cmd != nil {
		t.Fatalf("handleSelection() command = %v, want nil when action is startup-blocked", cmd)
	}

	if len(newModel.toasts) == 0 {
		t.Fatalf("handleSelection() expected blocked-action toast, got none")
	}

	last := newModel.toasts[len(newModel.toasts)-1]
	if last.Level != ToastWarning {
		t.Fatalf("handleSelection() toast level = %v, want %v", last.Level, ToastWarning)
	}

	if !strings.Contains(last.Message, "requires tmux") {
		t.Fatalf("handleSelection() toast message = %q, want startup reason", last.Message)
	}

	after := getCursorPosition(newModel)
	if before.Column != after.Column || before.Task != after.Task {
		t.Fatalf("handleSelection() cursor moved from (%d,%d) to (%d,%d)", before.Column, before.Task, after.Column, after.Task)
	}
}
