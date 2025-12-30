package overlay

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBulkCleanupOverlay_Init(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	if overlay == nil {
		t.Fatal("NewBulkCleanupOverlay returned nil")
	}

	if len(overlay.categories) == 0 {
		t.Error("Expected categories to be initialized")
	}

	// Test Init command
	cmd := overlay.Init()
	if cmd != nil {
		t.Error("Init should return nil command")
	}
}

func TestBulkCleanupOverlay_Navigation(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)
	initialCursor := overlay.cursor

	// Test move down
	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	overlay = model.(*BulkCleanupOverlay)
	if overlay.cursor != initialCursor+1 {
		t.Errorf("Expected cursor to move down, got %d", overlay.cursor)
	}

	// Test move up
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	overlay = model.(*BulkCleanupOverlay)
	if overlay.cursor != initialCursor {
		t.Errorf("Expected cursor to move up to initial position, got %d", overlay.cursor)
	}

	// Test bounds - can't go below 0
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	overlay = model.(*BulkCleanupOverlay)
	if overlay.cursor < 0 {
		t.Error("Cursor should not go below 0")
	}
}

func TestBulkCleanupOverlay_Selection(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	// Initially nothing should be selected
	for _, cat := range overlay.categories {
		if cat.Selected {
			t.Error("No categories should be selected initially")
		}
	}

	// Test space to toggle selection
	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	overlay = model.(*BulkCleanupOverlay)

	if !overlay.categories[0].Selected {
		t.Error("First category should be selected after space")
	}

	// Test space again to deselect
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	overlay = model.(*BulkCleanupOverlay)

	if overlay.categories[0].Selected {
		t.Error("First category should be deselected after second space")
	}

	// Test 'a' to select all
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	overlay = model.(*BulkCleanupOverlay)

	for i, cat := range overlay.categories {
		if !cat.Selected {
			t.Errorf("Category %d should be selected after 'a'", i)
		}
	}

	// Test 'A' to deselect all
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	overlay = model.(*BulkCleanupOverlay)

	for i, cat := range overlay.categories {
		if cat.Selected {
			t.Errorf("Category %d should be deselected after 'A'", i)
		}
	}
}

func TestBulkCleanupOverlay_ConfirmMode(t *testing.T) {
	executed := false
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		executed = true
		return CleanupResult{Deleted: len(categoryIDs)}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	// Select first category (which is destructive)
	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	overlay = model.(*BulkCleanupOverlay)

	// Press enter should trigger confirm mode
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	overlay = model.(*BulkCleanupOverlay)

	if !overlay.confirmMode {
		t.Error("Should enter confirm mode for destructive operations")
	}

	// Press 'n' to cancel
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	overlay = model.(*BulkCleanupOverlay)

	if overlay.confirmMode {
		t.Error("Should exit confirm mode after 'n'")
	}

	if executed {
		t.Error("Cleanup should not be executed after cancel")
	}

	// Try again with 'y' to confirm
	// Category should still be selected after canceling confirm dialog
	if !overlay.categories[0].Selected {
		t.Fatal("First category should still be selected after canceling")
	}

	// Enter confirm mode again
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	overlay = model.(*BulkCleanupOverlay)

	if !overlay.confirmMode {
		t.Fatalf("Should be in confirm mode after pressing enter (selected=%v, destructive=%v)",
			overlay.categories[0].Selected, overlay.categories[0].Destructive)
	}

	var cmd tea.Cmd
	model, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}) // Confirm

	if cmd == nil {
		t.Error("Should return cleanup command after 'y'")
	}
}

func TestBulkCleanupOverlay_CloseOnEscape(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if cmd == nil {
		t.Error("Expected close command on escape")
	}

	// Execute the command and check it returns CloseOverlayMsg
	msg := cmd()
	if _, ok := msg.(CloseOverlayMsg); !ok {
		t.Errorf("Expected CloseOverlayMsg, got %T", msg)
	}
}

func TestBulkCleanupOverlay_View(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	view := overlay.View()

	if view == "" {
		t.Error("View should not be empty")
	}

	// Check for key UI elements
	if !strings.Contains(view, "Bulk Cleanup") {
		t.Error("View should contain title")
	}

	if !strings.Contains(view, "j/k") {
		t.Error("View should contain navigation hints")
	}

	// Test confirm mode view
	overlay.categories[0].Selected = true
	overlay.confirmMode = true
	confirmView := overlay.View()

	if !strings.Contains(confirmView, "Confirm") {
		t.Error("Confirm view should contain 'Confirm'")
	}

	if !strings.Contains(confirmView, "[Y] Yes") {
		t.Error("Confirm view should contain yes button")
	}

	if !strings.Contains(confirmView, "[N] No") {
		t.Error("Confirm view should contain no button")
	}
}

func TestBulkCleanupOverlay_TitleAndSize(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	title := overlay.Title()
	if title != "Bulk Cleanup" {
		t.Errorf("Expected title 'Bulk Cleanup', got '%s'", title)
	}

	width, height := overlay.Size()
	if width <= 0 || height <= 0 {
		t.Errorf("Expected positive dimensions, got %dx%d", width, height)
	}

	// Test confirm mode size
	overlay.confirmMode = true
	confWidth, confHeight := overlay.Size()
	if confWidth <= 0 || confHeight <= 0 {
		t.Errorf("Expected positive confirm mode dimensions, got %dx%d", confWidth, confHeight)
	}
}

func TestBulkCleanupOverlay_NoSelection(t *testing.T) {
	mockCleanup := func(ctx context.Context, categoryIDs []string) (CleanupResult, error) {
		return CleanupResult{}, nil
	}

	overlay := NewBulkCleanupOverlay(mockCleanup, 100, 5, 2)

	// Try to execute with no selection
	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	overlay = model.(*BulkCleanupOverlay)

	if overlay.error == "" {
		t.Error("Should show error when no categories are selected")
	}

	if cmd != nil {
		t.Error("Should not execute cleanup when nothing is selected")
	}
}
