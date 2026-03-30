package overlay

import (
	"strings"
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
	if title != "Task" {
		t.Errorf("expected title 'Task', got %s", title)
	}
}

func TestActionMenu_Size(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	width, height := menu.Size()
	if width < 60 {
		t.Errorf("expected width >= 60 for combined task panel, got %d", width)
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
	hasYoloStart := false
	for _, action := range menu.actions {
		if action.Key == "s" && action.Label == "Start session" {
			hasStartSession = true
		}
		if action.Key == "!" && action.Label == "Start session (yolo)" {
			hasYoloStart = true
		}
	}

	if !hasStartSession {
		t.Error("expected 'Start session' action when no session exists")
	}
	if !hasYoloStart {
		t.Error("expected 'Start session (yolo)' action when no session exists")
	}

	hasCreateChild := false
	hasImageAttachments := false
	for _, action := range menu.actions {
		if action.Key == "c" && action.Label == "Create child task" && action.Enabled {
			hasCreateChild = true
		}
		if action.Key == "i" && action.Label == "Image attachments" && action.Enabled {
			hasImageAttachments = true
		}
	}
	if !hasCreateChild {
		t.Error("expected 'Create child task' action in action menu")
	}
	if !hasImageAttachments {
		t.Error("expected 'Image attachments' action in action menu")
	}

	// Worktree-gated actions should be disabled
	for _, action := range menu.actions {
		if action.Key == "u" || action.Key == "m" || action.Key == "P" || action.Key == "O" || action.Key == "M" || action.Key == "H" || action.Key == "w" || action.Key == "W" {
			if action.Enabled {
				t.Errorf("expected worktree-gated action '%s' to be disabled without session", action.Key)
			}
		}
	}

	// Always-available task-workspace actions should remain enabled.
	for _, action := range menu.actions {
		if action.Key == "b" || action.Key == "i" || action.Key == "r" {
			if !action.Enabled {
				t.Errorf("expected action '%s' to be enabled without session", action.Key)
			}
		}
	}
}

func TestActionMenu_BuildActions_TmuxPresenceWithoutProjectedSession(t *testing.T) {
	task := domain.Task{
		ID:             "az-123",
		Status:         domain.StatusInProgress,
		HasTmuxSession: true,
	}

	menu := NewActionMenu(task, nil)
	hasAttach := false
	for _, action := range menu.actions {
		if action.Key == "a" && action.Enabled {
			hasAttach = true
		}
	}
	if !hasAttach {
		t.Fatal("expected attach action when task has tmux presence")
	}
}

func TestActionMenu_BuildActions_ActiveSession(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Status: domain.StatusInProgress,
	}

	session := &domain.Session{
		IssueID:  "az-123",
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

	// Worktree-gated actions should be enabled with worktree.
	for _, action := range menu.actions {
		if action.Key == "u" || action.Key == "m" || action.Key == "P" || action.Key == "O" || action.Key == "M" || action.Key == "H" || action.Key == "f" || action.Key == "w" || action.Key == "W" {
			if !action.Enabled {
				t.Errorf("expected worktree-gated action '%s' to be enabled with worktree", action.Key)
			}
		}
	}

	// Workspace actions should still be enabled with an active session.
	for _, action := range menu.actions {
		if action.Key == "b" || action.Key == "i" || action.Key == "r" {
			if !action.Enabled {
				t.Errorf("expected workspace action '%s' to be enabled with session", action.Key)
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
		IssueID:  "az-123",
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

func TestActionMenu_FollowOnMergeAvailabilityBySessionState(t *testing.T) {
	task := domain.Task{
		ID:     "az-123",
		Title:  "Task",
		Status: domain.StatusInProgress,
	}

	tests := []struct {
		name    string
		session *domain.Session
		want    bool
	}{
		{
			name: "idle with worktree enabled",
			session: &domain.Session{
				IssueID:  "az-123",
				State:    domain.SessionIdle,
				Worktree: "/tmp/az-123",
			},
			want: true,
		},
		{
			name: "busy with worktree enabled",
			session: &domain.Session{
				IssueID:  "az-123",
				State:    domain.SessionBusy,
				Worktree: "/tmp/az-123",
			},
			want: true,
		},
		{
			name: "waiting with worktree enabled",
			session: &domain.Session{
				IssueID:  "az-123",
				State:    domain.SessionWaiting,
				Worktree: "/tmp/az-123",
			},
			want: true,
		},
		{
			name: "paused with worktree enabled",
			session: &domain.Session{
				IssueID:  "az-123",
				State:    domain.SessionPaused,
				Worktree: "/tmp/az-123",
			},
			want: true,
		},
		{
			name: "session without worktree disabled",
			session: &domain.Session{
				IssueID: "az-123",
				State:   domain.SessionBusy,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			menu := NewActionMenu(task, tt.session)
			found := false
			for _, action := range menu.actions {
				if action.Key == "m" {
					found = true
					if action.Enabled != tt.want {
						t.Fatalf("follow-on merge enabled = %v, want %v", action.Enabled, tt.want)
					}
				}
			}
			if !found {
				t.Fatal("expected follow-on merge action")
			}
		})
	}
}

func TestActionMenu_FollowOnMergeAvailability_TaskWorktreeFallback(t *testing.T) {
	taskWithSessionWorktree := domain.Task{
		ID:      "az-123",
		Title:   "Task",
		Status:  domain.StatusInProgress,
		Session: &domain.Session{IssueID: "az-123", Worktree: "/tmp/az-123"},
	}
	menu := NewActionMenu(taskWithSessionWorktree, nil)
	if cmd := menu.selectByKey("m"); cmd == nil {
		t.Fatal("expected follow-on merge enabled from task session worktree fallback")
	}

	taskWithRuntimeWorktree := domain.Task{
		ID:          "az-456",
		Title:       "Task",
		Status:      domain.StatusInProgress,
		HasWorktree: true,
	}
	menu = NewActionMenu(taskWithRuntimeWorktree, nil)
	if cmd := menu.selectByKey("m"); cmd == nil {
		t.Fatal("expected follow-on merge enabled from task hasWorktree fallback")
	}
}

func TestActionMenu_MergeLabelTopLevelUsesMain(t *testing.T) {
	topLevelTask := domain.Task{
		ID:          "az-top",
		Title:       "Top level",
		Status:      domain.StatusInProgress,
		HasWorktree: true,
	}
	menu := NewActionMenu(topLevelTask, nil)
	for _, action := range menu.actions {
		if action.Key == "m" {
			if action.Label != "Merge into main" {
				t.Fatalf("merge label = %q, want %q", action.Label, "Merge into main")
			}
			return
		}
	}
	t.Fatal("expected merge action")
}

func TestActionMenu_MoveActions(t *testing.T) {
	tests := []struct {
		name            string
		status          domain.Status
		expectMoveLeft  bool
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

	// Try selecting image attachments with 'i'
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}}
	_, cmd := menu.Update(msg)

	if cmd == nil {
		t.Fatal("expected command from direct key selection")
	}

	result := cmd()
	selectionMsg, ok := result.(SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg, got %T", result)
	}

	if selectionMsg.Key != "i" {
		t.Errorf("expected key 'i', got %s", selectionMsg.Key)
	}
}

func TestActionMenu_Update_ArrowSelection(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusInProgress}
	menu := NewActionMenu(task, nil)

	_, leftCmd := menu.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if leftCmd == nil {
		t.Fatal("expected command from left arrow key")
	}
	leftMsg, ok := leftCmd().(SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg for left arrow, got %T", leftCmd())
	}
	if leftMsg.Key != "h" {
		t.Fatalf("left arrow selected %q, want %q", leftMsg.Key, "h")
	}

	_, rightCmd := menu.Update(tea.KeyMsg{Type: tea.KeyRight})
	if rightCmd == nil {
		t.Fatal("expected command from right arrow key")
	}
	rightMsg, ok := rightCmd().(SelectionMsg)
	if !ok {
		t.Fatalf("expected SelectionMsg for right arrow, got %T", rightCmd())
	}
	if rightMsg.Key != "l" {
		t.Fatalf("right arrow selected %q, want %q", rightMsg.Key, "l")
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
	task := domain.Task{ID: "az-123", Title: "Test Task", Status: domain.StatusOpen}
	menu := NewActionMenu(task, nil)

	view := menu.View()

	if view == "" {
		t.Error("expected non-empty view")
	}

	// Should contain at least some action keys
	if len(menu.actions) == 0 {
		t.Error("expected menu to have actions")
	}
	if !strings.Contains(view, "[az-123]") ||
		!strings.Contains(view, "Test Task") ||
		!strings.Contains(view, "status:") ||
		!strings.Contains(view, "dependencies:") {
		t.Errorf("expected task detail section in view, got: %s", view)
	}
}

func TestActionMenu_SelectByKey_Disabled(t *testing.T) {
	task := domain.Task{ID: "az-123", Status: domain.StatusOpen}
	session := &domain.Session{
		IssueID: "az-123",
		State:   domain.SessionBusy,
		// No worktree, so git actions disabled
	}
	menu := NewActionMenu(task, session)

	// Try to select a disabled git action
	cmd := menu.selectByKey("u")

	if cmd != nil {
		t.Error("expected nil command when selecting disabled action")
	}
}

func TestBulkActionMenu_Update_ArrowSelection(t *testing.T) {
	menu := NewBulkActionMenu([]string{"az-1", "az-2"}, 2)

	_, leftCmd := menu.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if leftCmd == nil {
		t.Fatal("expected command from left arrow key")
	}
	leftMsg, ok := leftCmd().(BulkActionMsg)
	if !ok {
		t.Fatalf("expected BulkActionMsg for left arrow, got %T", leftCmd())
	}
	if leftMsg.Action != "h" {
		t.Fatalf("left arrow action = %q, want %q", leftMsg.Action, "h")
	}

	_, rightCmd := menu.Update(tea.KeyMsg{Type: tea.KeyRight})
	if rightCmd == nil {
		t.Fatal("expected command from right arrow key")
	}
	rightMsg, ok := rightCmd().(BulkActionMsg)
	if !ok {
		t.Fatalf("expected BulkActionMsg for right arrow, got %T", rightCmd())
	}
	if rightMsg.Action != "l" {
		t.Fatalf("right arrow action = %q, want %q", rightMsg.Action, "l")
	}
}
