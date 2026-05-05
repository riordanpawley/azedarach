package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestTaskWorkspaceOverlay_View_NoLocalFooterHints(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	view := overlay.View()

	if strings.Contains(view, "Tab/h/l") {
		t.Fatalf("expected workspace overlay to omit local footer hints, got: %q", view)
	}
	if strings.Contains(view, "Enter/action key") {
		t.Fatalf("expected workspace overlay to omit local footer hints, got: %q", view)
	}
}

func TestTaskWorkspaceOverlay_View_HasOuterBorder(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	view := overlay.View()

	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") {
		t.Fatalf("expected workspace overlay to render an outer rounded border, got: %q", view)
	}
}

func TestTaskWorkspaceOverlay_UsesFullScreen(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	if !overlay.UsesFullScreen() {
		t.Fatalf("expected task workspace overlay to request full-screen rendering")
	}
}

func TestTaskWorkspaceOverlay_DetailScrollKeybinds(t *testing.T) {
	task := domain.Task{
		ID:          "az-1",
		Title:       "Task",
		Status:      domain.StatusOpen,
		Description: strings.Repeat("line\n", 200),
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	overlay.focus = taskWorkspaceFocusDetail

	initial := overlay.detail.scrollY
	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.detail.scrollY <= initial {
		t.Fatalf("expected j to scroll down; got %d from %d", overlay.detail.scrollY, initial)
	}

	beforeDown := overlay.detail.scrollY
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.detail.scrollY <= beforeDown {
		t.Fatalf("expected down arrow to scroll down; got %d from %d", overlay.detail.scrollY, beforeDown)
	}

	beforeHalfDown := overlay.detail.scrollY
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.detail.scrollY <= beforeHalfDown {
		t.Fatalf("expected ctrl+d to half-page down; got %d from %d", overlay.detail.scrollY, beforeHalfDown)
	}

	beforeUp := overlay.detail.scrollY
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyUp})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.detail.scrollY >= beforeUp {
		t.Fatalf("expected up arrow to scroll up; got %d from %d", overlay.detail.scrollY, beforeUp)
	}

	beforeHalfUp := overlay.detail.scrollY
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.detail.scrollY >= beforeHalfUp {
		t.Fatalf("expected ctrl+u to half-page up; got %d from %d", overlay.detail.scrollY, beforeHalfUp)
	}
}

func TestTaskWorkspaceOverlay_StatusBindingsIncludeScroll(t *testing.T) {
	task := domain.Task{
		ID:          "az-1",
		Title:       "Task",
		Status:      domain.StatusOpen,
		Description: strings.Repeat("line\n", 50),
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	bindings := overlay.StatusBindings()
	joined := ""
	for _, b := range bindings {
		joined += b.Key + " " + b.Description + " "
	}
	if !strings.Contains(joined, "select relation") {
		t.Fatalf("expected status bindings to include graph selection hint, got %q", joined)
	}
	if !strings.Contains(joined, "open relation") {
		t.Fatalf("expected status bindings to include graph open hint, got %q", joined)
	}
	if !strings.Contains(joined, "ctrl+u/d") {
		t.Fatalf("expected status bindings to include ctrl+u/d hint, got %q", joined)
	}
	if !strings.Contains(joined, "1/2/3/4") {
		t.Fatalf("expected status bindings to include exact status hint, got %q", joined)
	}
	if !strings.Contains(joined, "Tab focus") {
		t.Fatalf("expected status bindings to show Tab as pane focus switch, got %q", joined)
	}
	if strings.Contains(joined, "Tab/h/l") {
		t.Fatalf("expected h/l not to be advertised as pane focus switches, got %q", joined)
	}
	if !strings.Contains(joined, "h/l/") {
		t.Fatalf("expected status bindings to advertise h/l graph navigation, got %q", joined)
	}
}

func TestTaskWorkspaceOverlay_ActionFocusUsesArrowNavigation(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	overlay.focus = taskWorkspaceFocusActions

	initial := overlay.actions.cursor
	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.actions.cursor == initial {
		t.Fatalf("expected down arrow to move actions cursor when actions focused")
	}

	beforeUp := overlay.actions.cursor
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyUp})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.actions.cursor == beforeUp {
		t.Fatalf("expected up arrow to move actions cursor when actions focused")
	}

	beforeRight := overlay.actions.cursor
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRight})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.actions.cursor == beforeRight {
		t.Fatalf("expected right arrow to move actions cursor down when actions focused")
	}

	beforeLeft := overlay.actions.cursor
	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyLeft})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.actions.cursor == beforeLeft {
		t.Fatalf("expected left arrow to move actions cursor up when actions focused")
	}
}

func TestTaskWorkspaceOverlay_HJKLDoNotSwitchPaneFocus(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	overlay.focus = taskWorkspaceFocusDetail

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.focus != taskWorkspaceFocusDetail {
		t.Fatalf("expected l to keep detail focus, got %v", overlay.focus)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.focus != taskWorkspaceFocusActions {
		t.Fatalf("expected Tab to switch to actions, got %v", overlay.focus)
	}

	model, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	overlay = model.(*TaskWorkspaceOverlay)
	if overlay.focus != taskWorkspaceFocusActions {
		t.Fatalf("expected h to keep actions focus, got %v", overlay.focus)
	}
}

func TestTaskWorkspaceOverlay_DetailGraphUsesVerticalSelectionAndHorizontalOpen(t *testing.T) {
	parentID := domain.Task{ID: "az-parent", Title: "Parent", Status: domain.StatusOpen}
	task := domain.Task{
		ID:       "az-current",
		Title:    "Current",
		Status:   domain.StatusInProgress,
		ParentID: &parentID.ID,
		Dependencies: []domain.Dependency{
			{ID: "az-child", Type: domain.DependencyBlocks},
		},
	}
	related := []domain.Task{
		parentID,
		task,
		{ID: "az-child", Title: "Child", Status: domain.StatusOpen},
	}
	overlay := NewTaskWorkspaceOverlay(task, related, nil, 120, 30)
	overlay.focus = taskWorkspaceFocusDetail

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if cmd == nil {
		t.Fatal("expected left arrow to open selected ancestor")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok || msg.Key != "task_workspace_open_task" || msg.Value != "az-parent" {
		t.Fatalf("left command emitted %+v, want az-parent graph selection", msg)
	}

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = model.(*TaskWorkspaceOverlay)
	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRight})
	if cmd == nil {
		t.Fatal("expected right arrow to open selected descendant")
	}
	msg, ok = cmd().(SelectionMsg)
	if !ok || msg.Key != "task_workspace_open_task" || msg.Value != "az-child" {
		t.Fatalf("right command emitted %+v, want az-child graph selection", msg)
	}
}

func TestTaskWorkspaceOverlay_StatusKeysWorkFromDetailFocus(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	overlay.focus = taskWorkspaceFocusDetail

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if cmd == nil {
		t.Fatal("expected status key command from detail focus")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("command emitted %T, want SelectionMsg", cmd())
	}
	if msg.Key != "3" {
		t.Fatalf("selection key = %q, want 3", msg.Key)
	}
}

func TestTaskWorkspaceOverlay_ActionsPaneHidesReservedMoveRows(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusInProgress,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	view := overlay.View()

	if strings.Contains(view, "[h] Move left") || strings.Contains(view, "[l] Move right") {
		t.Fatalf("workspace actions should not advertise reserved h/l status movement, got %q", view)
	}
	if !strings.Contains(view, "[1] Set status: Open") || !strings.Contains(view, "[4] Set status: Done") {
		t.Fatalf("workspace actions should keep explicit status keys, got %q", view)
	}
}

func TestTaskWorkspaceOverlay_EnterOnDetailOpensSelectedGraphTask(t *testing.T) {
	task := domain.Task{
		ID:     "az-parent",
		Title:  "Parent",
		Status: domain.StatusOpen,
		Dependencies: []domain.Dependency{
			{ID: "az-child", Type: domain.DependencyBlocks},
		},
	}
	related := []domain.Task{
		task,
		{ID: "az-child", Title: "Child", Status: domain.StatusInProgress},
	}
	overlay := NewTaskWorkspaceOverlay(task, related, nil, 120, 30)
	overlay.focus = taskWorkspaceFocusDetail

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	overlay = model.(*TaskWorkspaceOverlay)
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected graph navigation command")
	}
	msg, ok := cmd().(SelectionMsg)
	if !ok {
		t.Fatalf("command emitted %T, want SelectionMsg", cmd())
	}
	if msg.Key != "task_workspace_open_task" || msg.Value != "az-child" {
		t.Fatalf("selection = %+v, want task_workspace_open_task az-child", msg)
	}
}

func TestTaskWorkspaceOverlay_WindowResizeUpdatesDimensions(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 64, Height: 20})
	overlay = model.(*TaskWorkspaceOverlay)

	width, height := overlay.Size()
	if width != 64 {
		t.Fatalf("expected overlay width to track window size, got %d", width)
	}
	if height != 19 {
		t.Fatalf("expected overlay height to reserve status line, got %d", height)
	}
}

func TestTaskWorkspaceOverlay_ActionsScrollWithCursorInShortViewport(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 76, 14)
	overlay.focus = taskWorkspaceFocusActions

	model, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyEnd})
	overlay = model.(*TaskWorkspaceOverlay)

	_ = overlay.View()

	if overlay.actions.viewportRows < 1 {
		t.Fatalf("expected action viewport rows to be set, got %d", overlay.actions.viewportRows)
	}
	if overlay.actions.scrollOffset <= 0 {
		t.Fatalf("expected action list to scroll for end navigation, got offset %d", overlay.actions.scrollOffset)
	}
	if overlay.actions.cursor < overlay.actions.scrollOffset ||
		overlay.actions.cursor >= overlay.actions.scrollOffset+overlay.actions.viewportRows {
		t.Fatalf(
			"expected cursor %d to remain in visible window [%d, %d)",
			overlay.actions.cursor,
			overlay.actions.scrollOffset,
			overlay.actions.scrollOffset+overlay.actions.viewportRows,
		)
	}
}

func TestTaskWorkspaceOverlay_View_ShowsMutationProgress(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusInProgress,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, &TaskMutationProgress{
		OperationID: "op-status", State: "queued", ProgressPercent: 40,
		ProgressMessage: "queued task.update_status",
		PreviousStatus:  domain.StatusOpen,
		TargetStatus:    domain.StatusInProgress,
	}, 120, 30)

	view := overlay.View()
	if !strings.Contains(view, "Issue Ops:") {
		t.Fatalf("expected mutation row in detail panel, got: %q", view)
	}
	if !strings.Contains(view, "queued") || !strings.Contains(view, "op-status") {
		t.Fatalf("expected mutation state and operation id in detail panel, got: %q", view)
	}
	if !strings.Contains(view, "40%") || !strings.Contains(view, "queued task.update_status") {
		t.Fatalf("expected mutation progress payload in detail panel, got: %q", view)
	}
}

func TestTaskWorkspaceOverlay_SyncTaskRefreshesMutationProgress(t *testing.T) {
	task := domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusOpen,
	}
	overlay := NewTaskWorkspaceOverlay(task, nil, nil, 120, 30)
	overlay.SyncTask(domain.Task{
		ID:     "az-1",
		Title:  "Task",
		Status: domain.StatusInProgress,
	}, nil, &TaskMutationProgress{
		OperationID:     "op-sync",
		State:           "running",
		ProgressPercent: 77,
		ProgressMessage: "running git.merge",
		PreviousStatus:  domain.StatusOpen,
		TargetStatus:    domain.StatusInProgress,
	})

	view := overlay.View()
	if !strings.Contains(view, "running") || !strings.Contains(view, "op-sync") {
		t.Fatalf("expected synced mutation progress in detail panel, got: %q", view)
	}
	if !strings.Contains(view, "77%") || !strings.Contains(view, "running git.merge") {
		t.Fatalf("expected synced mutation progress payload in detail panel, got: %q", view)
	}
}
