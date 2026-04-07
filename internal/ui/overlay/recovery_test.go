package overlay

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRecoveryOverlayKeepsSelectedItemVisibleWhenScrolling(t *testing.T) {
	items := make([]RecoveryNotificationItem, 0, 20)
	for i := 0; i < 20; i++ {
		items = append(items, RecoveryNotificationItem{
			ID:      fmt.Sprintf("recovery-%d", i),
			IssueID: fmt.Sprintf("az-%02d", i),
			Title:   fmt.Sprintf("Failure %02d", i),
			Message: fmt.Sprintf("detail %02d", i),
		})
	}
	o := NewRecoveryOverlay(items)
	_, _ = o.Update(tea.WindowSizeMsg{Width: 80, Height: 18})
	for i := 0; i < 15; i++ {
		_, _ = o.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	view := o.View()
	if !strings.Contains(view, "[az-15]") {
		t.Fatalf("expected selected row to be visible after scrolling, view=%q", view)
	}
}
