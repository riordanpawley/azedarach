package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestNewSortMenu(t *testing.T) {
	sort := &domain.Sort{
		Field: domain.SortByPriority,
		Order: domain.SortAsc,
	}

	menu := NewSortMenu(sort)

	if menu == nil {
		t.Fatal("expected menu to be created")
	}

	if menu.sort != sort {
		t.Error("expected menu to hold reference to sort state")
	}

	if len(menu.options) != 3 {
		t.Errorf("expected 3 sort options, got %d", len(menu.options))
	}
}

func TestSortMenu_Title(t *testing.T) {
	sort := &domain.Sort{Field: domain.SortByPriority, Order: domain.SortAsc}
	menu := NewSortMenu(sort)

	title := menu.Title()
	if title != "Sort" {
		t.Errorf("expected title 'Sort', got %s", title)
	}
}

func TestSortMenu_Size(t *testing.T) {
	sort := &domain.Sort{Field: domain.SortByPriority, Order: domain.SortAsc}
	menu := NewSortMenu(sort)

	width, height := menu.Size()
	if width != 70 {
		t.Errorf("expected width 70, got %d", width)
	}

	// Height should be options + footer + padding (3 + 5 = 8)
	expectedHeight := 8
	if height != expectedHeight {
		t.Errorf("expected height %d, got %d", expectedHeight, height)
	}
}

func TestSortMenu_InitialDisplay(t *testing.T) {
	tests := []struct {
		name          string
		field         domain.SortField
		order         domain.SortOrder
		expectArrow   string
		expectActive  string
	}{
		{
			name:         "Session ascending",
			field:        domain.SortBySession,
			order:        domain.SortAsc,
			expectArrow:  "↑",
			expectActive: "Session",
		},
		{
			name:         "Priority descending",
			field:        domain.SortByPriority,
			order:        domain.SortDesc,
			expectArrow:  "↓",
			expectActive: "Priority",
		},
		{
			name:         "Updated ascending",
			field:        domain.SortByUpdated,
			order:        domain.SortAsc,
			expectArrow:  "↑",
			expectActive: "Updated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sort := &domain.Sort{
				Field: tt.field,
				Order: tt.order,
			}

			menu := NewSortMenu(sort)
			view := menu.View()

			if view == "" {
				t.Error("expected non-empty view")
			}

			// Should contain the active field label
			if !strings.Contains(view, tt.expectActive) {
				t.Errorf("expected view to contain active field '%s'", tt.expectActive)
			}

			// Should contain the indicator
			if !strings.Contains(view, "●") {
				t.Error("expected view to contain active indicator '●'")
			}

			// Should contain the direction arrow
			if !strings.Contains(view, tt.expectArrow) {
				t.Errorf("expected view to contain arrow '%s'", tt.expectArrow)
			}

			// Should contain all option keys
			for _, opt := range menu.options {
				if !strings.Contains(view, "["+opt.Key+"]") {
					t.Errorf("expected view to contain key '[%s]'", opt.Key)
				}
			}
		})
	}
}

func TestSortMenu_FieldSelection(t *testing.T) {
	tests := []struct {
		name          string
		initialField  domain.SortField
		pressKey      string
		expectField   domain.SortField
		expectOrder   domain.SortOrder
	}{
		{
			name:         "Change from Priority to Session",
			initialField: domain.SortByPriority,
			pressKey:     "s",
			expectField:  domain.SortBySession,
			expectOrder:  domain.SortAsc, // New field defaults to Asc
		},
		{
			name:         "Change from Session to Priority",
			initialField: domain.SortBySession,
			pressKey:     "p",
			expectField:  domain.SortByPriority,
			expectOrder:  domain.SortAsc,
		},
		{
			name:         "Change from Priority to Updated",
			initialField: domain.SortByPriority,
			pressKey:     "u",
			expectField:  domain.SortByUpdated,
			expectOrder:  domain.SortAsc,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sort := &domain.Sort{
				Field: tt.initialField,
				Order: domain.SortDesc, // Start with Desc to verify it resets
			}

			menu := NewSortMenu(sort)

			// Simulate key press
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(tt.pressKey[0])}}
			_, cmd := menu.Update(msg)

			if cmd == nil {
				t.Fatal("expected command from key press")
			}

			// Execute command to get SelectionMsg
			result := cmd()
			selectionMsg, ok := result.(SelectionMsg)
			if !ok {
				t.Fatalf("expected SelectionMsg, got %T", result)
			}

			if selectionMsg.Key != tt.pressKey {
				t.Errorf("expected key '%s', got '%s'", tt.pressKey, selectionMsg.Key)
			}

			// Check that sort state was updated
			if sort.Field != tt.expectField {
				t.Errorf("expected field %s, got %s", tt.expectField, sort.Field)
			}

			if sort.Order != tt.expectOrder {
				t.Errorf("expected order %d, got %d", tt.expectOrder, sort.Order)
			}
		})
	}
}

func TestSortMenu_DirectionToggle(t *testing.T) {
	tests := []struct {
		name         string
		initialOrder domain.SortOrder
		expectOrder  domain.SortOrder
		expectArrow  string
	}{
		{
			name:         "Toggle from Asc to Desc",
			initialOrder: domain.SortAsc,
			expectOrder:  domain.SortDesc,
			expectArrow:  "↓",
		},
		{
			name:         "Toggle from Desc to Asc",
			initialOrder: domain.SortDesc,
			expectOrder:  domain.SortAsc,
			expectArrow:  "↑",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sort := &domain.Sort{
				Field: domain.SortByPriority,
				Order: tt.initialOrder,
			}

			menu := NewSortMenu(sort)

			// Press the same key (priority) to toggle direction
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}
			_, cmd := menu.Update(msg)

			if cmd == nil {
				t.Fatal("expected command from key press")
			}

			// Execute command
			result := cmd()
			if _, ok := result.(SelectionMsg); !ok {
				t.Fatalf("expected SelectionMsg, got %T", result)
			}

			// Check that order was toggled
			if sort.Order != tt.expectOrder {
				t.Errorf("expected order %d, got %d", tt.expectOrder, sort.Order)
			}

			// Field should remain the same
			if sort.Field != domain.SortByPriority {
				t.Errorf("expected field to remain %s, got %s", domain.SortByPriority, sort.Field)
			}

			// Verify view shows correct arrow
			view := menu.View()
			if !strings.Contains(view, tt.expectArrow) {
				t.Errorf("expected view to contain arrow '%s'", tt.expectArrow)
			}
		})
	}
}

func TestSortMenu_EscapeCloses(t *testing.T) {
	sort := &domain.Sort{Field: domain.SortByPriority, Order: domain.SortAsc}
	menu := NewSortMenu(sort)

	tests := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{
			name: "Escape key",
			msg:  tea.KeyMsg{Type: tea.KeyEsc},
		},
		{
			name: "Q key",
			msg:  tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cmd := menu.Update(tt.msg)

			if cmd == nil {
				t.Fatal("expected command from escape key")
			}

			result := cmd()
			if _, ok := result.(CloseOverlayMsg); !ok {
				t.Errorf("expected CloseOverlayMsg, got %T", result)
			}
		})
	}
}

func TestSortMenu_View_ShowsAllOptions(t *testing.T) {
	sort := &domain.Sort{Field: domain.SortByPriority, Order: domain.SortAsc}
	menu := NewSortMenu(sort)

	view := menu.View()

	// Should show all three options
	expectedOptions := []string{
		"Session",
		"Priority",
		"Updated",
	}

	for _, opt := range expectedOptions {
		if !strings.Contains(view, opt) {
			t.Errorf("expected view to contain option '%s'", opt)
		}
	}

	// Should show footer hint
	if !strings.Contains(view, "Press same key to toggle direction") {
		t.Error("expected view to contain footer hint")
	}

	if !strings.Contains(view, "Esc to close") {
		t.Error("expected view to contain close hint")
	}
}

func TestSortMenu_MultipleToggles(t *testing.T) {
	sort := &domain.Sort{
		Field: domain.SortByPriority,
		Order: domain.SortAsc,
	}

	menu := NewSortMenu(sort)

	// Toggle direction
	msg1 := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}
	menu.Update(msg1)
	if sort.Order != domain.SortDesc {
		t.Error("expected first toggle to set Desc")
	}

	// Toggle again
	msg2 := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}
	menu.Update(msg2)
	if sort.Order != domain.SortAsc {
		t.Error("expected second toggle to return to Asc")
	}

	// Change field
	msg3 := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
	menu.Update(msg3)
	if sort.Field != domain.SortBySession {
		t.Error("expected field to change to Session")
	}
	if sort.Order != domain.SortAsc {
		t.Error("expected new field to default to Asc")
	}
}

func TestSortMenu_Init(t *testing.T) {
	sort := &domain.Sort{Field: domain.SortByPriority, Order: domain.SortAsc}
	menu := NewSortMenu(sort)

	cmd := menu.Init()
	if cmd != nil {
		t.Error("expected Init to return nil")
	}
}
