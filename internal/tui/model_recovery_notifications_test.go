package app

import (
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

func TestOpenRecoveryOverlayCmdEmptyQueueAddsToast(t *testing.T) {
	m := newTestModel()
	m.loading = false
	initialToasts := len(m.toasts)

	cmd := (&m).openRecoveryOverlayCmd()
	if cmd != nil {
		t.Fatalf("openRecoveryOverlayCmd() cmd = %v, want nil for empty queue", cmd)
	}
	if len(m.toasts) != initialToasts+1 {
		t.Fatalf("toasts len = %d, want %d", len(m.toasts), initialToasts+1)
	}
	if got := m.toasts[len(m.toasts)-1].Message; !strings.Contains(got, "No recoverable async failures queued") {
		t.Fatalf("empty queue toast = %q", got)
	}
}

func TestAsyncRecoveryRecoverKeepsNotificationWhenActionUnavailable(t *testing.T) {
	m := newTestModel()
	m.loading = false
	m.recoveryNotifications = []asyncRecoveryNotification{
		{
			ID:      "recovery-1",
			IssueID: "az-1",
			Title:   "Broken action",
			Message: "unknown action",
			Action:  asyncRecoveryAction("unsupported"),
		},
	}

	updated, _ := m.Update(overlay.SelectionMsg{Key: "async_recovery_recover", Value: "recovery-1"})
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want app.Model", updated)
	}
	if len(next.recoveryNotifications) != 1 {
		t.Fatalf("recovery notifications len = %d, want 1", len(next.recoveryNotifications))
	}
	if next.recoveryNotifications[0].ID != "recovery-1" {
		t.Fatalf("remaining recovery notification id = %q, want recovery-1", next.recoveryNotifications[0].ID)
	}
}
