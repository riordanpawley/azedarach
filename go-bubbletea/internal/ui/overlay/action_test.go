package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestNewActionMenu(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Title:  "Test task",
		Status: domain.StatusOpen,
	}

	menu := NewActionMenu(task, nil)

	if menu == nil {
		t.Fatal("expected menu to be created")
	}

	if menu.task.ID != task.ID {
		t.Errorf("expected task ID %s, got %s", task.ID, menu.task.ID)
	}
}

func TestActionMenu_Title(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	title := menu.Title()
	if title != "Actions" {
		t.Errorf("expected title 'Actions', got %s", title)
	}
}

func TestActionMenu_Size(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	width, height := menu.Size()
	if width != 36 {
		t.Errorf("expected width 36, got %d", width)
	}

	if height <= 0 {
		t.Errorf("expected positive height, got %d", height)
	}
}

func TestActionMenu_BuildActions_NoSession(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Status: domain.StatusOpen,
	}

	menu := NewActionMenu(task, nil)

	// Should have start session actions
	hasStartSession := false
	for _, action := range menu.actions {
		if action.Key == "s" && action.Label == "Start session" {
			hasStartSession = true
		}
	}

	if !hasStartSession {
		t.Error("expected 'Start session' action when no session exists")
	}

	// Git actions should be disabled
	for _, action := range menu.actions {
		if action.Key == "u" || action.Key == "m" || action.Key == "P" {
			if action.Enabled {
				t.Errorf("expected git action '%s' to be disabled without session", action.Key)
			}
		}
	}
}

func TestActionMenu_BuildActions_ActiveSession(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Status: domain.StatusInProgress,
	}

	session := &domain.Session{
		BeadID:   "az-123",
		State:    domain.SessionBusy,
		Worktree: "/path/to/worktree",
	}

	menu := NewActionMenu(task, session)

	// Should have pause/stop actions
	hasPause := false
	hasStop := false
	for _, action := range menu.actions {
		if action.Key == "p" && action.Enabled {
			hasPause = true
		}
		if action.Key == "x" && action.Enabled {
			hasStop = true
		}
	}

	if !hasPause {
		t.Error("expected 'Pause session' action for busy session")
	}

	if !hasStop {
		t.Error("expected 'Stop session' action for busy session")
	}

	// Git actions should be enabled with worktree
	for _, action := range menu.actions {
		if action.Key == "u" || action.Key == "m" || action.Key == "P" || action.Key == "f" {
			if !action.Enabled {
				t.Errorf("expected git action '%s' to be enabled with worktree", action.Key)
			}
		}
	}
}

func TestActionMenu_BuildActions_PausedSession(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Status: domain.StatusInProgress,
	}

	session := &domain.Session{
		BeadID:   "az-123",
		State:    domain.SessionPaused,
		Worktree: "/path/to/worktree",
	}

	menu := NewActionMenu(task, session)

	// Should have resume action
	hasResume := false
	for _, action := range menu.actions {
		if action.Key == "R" && action.Enabled {
			hasResume = true
		}
	}

	if !hasResume {
		t.Error("expected 'Resume session' action for paused session")
	}
}

func TestActionMenu_MoveActions(t *testing.T) {
	tests := []struct {
		name           string
		status         domain.Status
		expectMoveLeft bool
		expectMoveRight bool
	}{
		{
			name:            "Open task can only move right",
			status:          domain.StatusOpen,
			expectMoveLeft:  false,
			expectMoveRight: true,
		},
		{
			name:            "In progress task can move both ways",
			status:          domain.StatusInProgress,
			expectMoveLeft:  true,
			expectMoveRight: true,
		},
		{
			name:            "Done task can only move left",
			status:          domain.StatusDone,
			expectMoveLeft:  true,
			expectMoveRight: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := domain.Task{
				ID:     "az-123",
				Status: tt.status,
			}

			menu := NewActionMenu(task, nil)

			var moveLeft, moveRight *Action
			for i := range menu.actions {
				if menu.actions[i].Key == "h" {
					moveLeft = &menu.actions[i]
				}
				if menu.actions[i].Key == "l" {
					moveRight = &menu.actions[i]
				}
			}

			if moveLeft == nil {
				t.Fatal("expected move left action")
			}
			if moveRight == nil {
				t.Fatal("expected move right action")
			}

			if moveLeft.Enabled != tt.expectMoveLeft {
				t.Errorf("expected move left enabled=%v, got %v", tt.expectMoveLeft, moveLeft.Enabled)
			}

			if moveRight.Enabled != tt.expectMoveRight {
				t.Errorf("expected move right enabled=%v, got %v", tt.expectMoveRight, moveRight.Enabled)
			}
		})
	}
}

func TestActionMenu_Navigation(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Status: domain.StatusOpen,
	}

	menu := NewActionMenu(task, nil)
	initialCursor := menu.cursor

	// Move down
	menu.moveCursorDown()
	if menu.cursor == initialCursor {
		t.Error("expected cursor to move down")
	}

	secondCursor := menu.cursor

	// Move up
	menu.moveCursorUp()
	if menu.cursor != initialCursor {
		t.Errorf("expected cursor to return to %d, got %d", initialCursor, secondCursor)
	}
}

func TestActionMenu_Update_Escape(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	_, cmd := menu.Update(msg)

	if cmd == nil {
		t.Fatal("expected command from escape key")
	}

	result := cmd()
	if _, ok := result.(CloseOverlayMsg); !ok {
		t.Errorf("expected CloseOverlayMsg, got %T", result)
	}
}

func TestActionMenu_Update_Navigation(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	initialCursor := menu.cursor

	// Test down
	msgDown := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	menu.Update(msgDown)
	if menu.cursor == initialCursor {
		t.Error("expected cursor to move down")
	}

	// Test up
	msgUp := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}
	menu.Update(msgUp)
	if menu.cursor != initialCursor {
		t.Error("expected cursor to return to initial position")
	}
}

func TestActionMenu_Update_DirectSelection(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	// Try selecting start session with 's'
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
	_, cmd := menu.Update(msg)

	if cmd == nil {
		t.Fatal("expected command from direct key selection")
	}

	result := cmd()
	selectionMsg, ok := result.(SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg, got %T", result)
	}

	if selectionMsg.Key != "s" {
		t.Errorf("expected key 's', got %s", selectionMsg.Key)
	}
}

func TestActionMenu_Update_Enter(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	// Move cursor to an enabled action
	for menu.cursor < len(menu.actions) && (!menu.actions[menu.cursor].Enabled || menu.actions[menu.cursor].Key == "") {
		menu.moveCursorDown()
	}

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	_, cmd := menu.Update(msg)

	if cmd == nil {
		t.Fatal("expected command from enter key")
	}

	result := cmd()
	if _, ok := result.(SelectionMsg); !ok {
		t.Errorf("expected SelectionMsg, got %T", result)
	}
}

func TestActionMenu_View(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	view := menu.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain at least some action keys
	if len(menu.actions) == 0 {
		t.Error("expected menu to have actions")
	}
}

func TestActionMenu_SelectByKey_Disabled(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	session := &domain.Session{
		BeadID: "az-123",
		State:  domain.SessionBusy,
		// No worktree, so git actions disabled
	}
	menu := NewActionMenu(task, session)

	// Try to select a disabled git action
	cmd := menu.selectByKey("u")

	if cmd != nil {
		t.Error("expected nil command when selecting disabled action")
	}
}
