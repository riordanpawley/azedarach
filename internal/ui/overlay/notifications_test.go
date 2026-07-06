package overlay

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestNotificationHistoryOverlayRendersFullMessageAndState(t *testing.T) {
	items := []NotificationHistoryItem{
		{
			ID:        "notice-1",
			CreatedAt: time.Date(2026, 7, 5, 14, 0, 0, 0, time.UTC),
			Level:     "error",
			Reference: "ctk",
			Message:   "Task ctk failed because the daemon rejected the status update. Next: inspect the task workspace.",
			Read:      false,
			Dismissed: true,
		},
	}
	overlay := NewNotificationHistoryOverlay(items)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	overlay = model.(*NotificationHistoryOverlay)

	view := overlay.View()

	for _, want := range []string{
		"Notification Action Center",
		"All notifications: 1",
		"Dismissed / notice",
		"Task ctk failed because the daemon rejected the status",
		"update.",
		"Next: inspect the task workspace.",
		"ctk",
		"error",
		"unread",
		"dismissed",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestNotificationActionCenterFiltersAndEmitsActions(t *testing.T) {
	items := []NotificationHistoryItem{
		{
			ID:             "notice-1",
			DaemonNoticeID: "notice-1",
			CreatedAt:      time.Date(2026, 7, 5, 14, 0, 0, 0, time.UTC),
			Level:          "error",
			Category:       "operation_failed",
			State:          "active",
			Reference:      "az-1",
			ScopeType:      "issue",
			ScopeID:        "az-1",
			OperationID:    "op-1",
			Message:        "Status update failed",
			Actions: []protocol.NoticeAction{
				{ActionID: "copy_details", Kind: "client.copy_details", Label: "Copy details", Enabled: true},
			},
		},
		{
			ID:        "notice-2",
			CreatedAt: time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC),
			Level:     "info",
			Category:  "local",
			State:     "active",
			Message:   "Local notice",
			Read:      true,
		},
	}
	overlay := NewNotificationHistoryOverlay(items)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	overlay = model.(*NotificationHistoryOverlay)

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = model.(*NotificationHistoryOverlay)
	view := overlay.View()
	if !strings.Contains(view, "Unread: 1") || strings.Contains(view, "Local notice") {
		t.Fatalf("filtered view =\n%s", view)
	}

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if cmd == nil {
		t.Fatal("expected copy details action command")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok || msg.Key != "notification_action" {
		t.Fatalf("message = %T/%+v, want notification_action SelectionMsg", cmd(), cmd())
	}
	action, ok := msg.Value.(NotificationActionCenterMsg)
	if !ok {
		t.Fatalf("selection value = %T, want NotificationActionCenterMsg", msg.Value)
	}
	if action.DaemonNoticeID != "notice-1" || action.Kind != "client.copy_details" || action.ScopeID != "az-1" || action.OperationID != "op-1" {
		t.Fatalf("action = %+v, want daemon-backed copy action with scope", action)
	}
}

func TestNotificationActionCenterDismissAllIncludesFilteredRows(t *testing.T) {
	overlay := NewNotificationHistoryOverlay([]NotificationHistoryItem{
		{ID: "notice-1", DaemonNoticeID: "notice-1", Level: "error", Message: "one"},
		{ID: "notice-2", Level: "info", Message: "two"},
		{ID: "notice-3", DaemonNoticeID: "notice-3", Level: "warning", Message: "three", Dismissed: true},
	})
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	overlay = model.(*NotificationHistoryOverlay)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if cmd == nil {
		t.Fatal("expected dismiss-all command")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok || msg.Key != "notification_dismiss_all" {
		t.Fatalf("message = %T/%+v, want notification_dismiss_all SelectionMsg", cmd(), cmd())
	}
	actions, ok := msg.Value.([]NotificationActionCenterMsg)
	if !ok {
		t.Fatalf("selection value = %T, want action slice", msg.Value)
	}
	if len(actions) != 2 {
		t.Fatalf("actions len = %d, want non-dismissed rows only: %+v", len(actions), actions)
	}
}

func TestNotificationActionCenterRetryShortcutAcceptsRetryPrefixedActions(t *testing.T) {
	overlay := NewNotificationHistoryOverlay([]NotificationHistoryItem{
		{
			ID:             "notice-1",
			DaemonNoticeID: "notice-1",
			Message:        "retryable failure",
			Actions: []protocol.NoticeAction{
				{ActionID: "retry_operation", Kind: "retry.operation", Label: "Retry", Enabled: true},
			},
		},
	})
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	overlay = model.(*NotificationHistoryOverlay)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("expected retry action command")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok || msg.Key != "notification_action" {
		t.Fatalf("message = %T/%+v, want notification_action SelectionMsg", cmd(), cmd())
	}
	action, ok := msg.Value.(NotificationActionCenterMsg)
	if !ok {
		t.Fatalf("selection value = %T, want NotificationActionCenterMsg", msg.Value)
	}
	if action.ActionID != "retry_operation" || action.Kind != "retry.operation" {
		t.Fatalf("action = %+v, want retry_operation payload", action)
	}
}

func TestNotificationHistoryOverlayFitsSmallViewport(t *testing.T) {
	overlay := NewNotificationHistoryOverlay([]NotificationHistoryItem{
		{
			ID:        "notice-1",
			CreatedAt: time.Now(),
			Level:     "warning",
			Reference: "op-123",
			Message:   strings.Repeat("long notification message ", 8),
		},
	})
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 48, Height: 14})
	overlay = model.(*NotificationHistoryOverlay)

	width, height := overlay.Size()
	if width > 48 || height > 14 {
		t.Fatalf("Size() = %dx%d, want within 48x14", width, height)
	}
}
