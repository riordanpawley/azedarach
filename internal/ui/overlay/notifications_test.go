package overlay

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	overlay = model.(*NotificationHistoryOverlay)

	view := overlay.View()

	for _, want := range []string{
		"Notification History",
		"Task ctk failed because the daemon rejected the status update.",
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
