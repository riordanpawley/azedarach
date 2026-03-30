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
	if !strings.Contains(joined, "scroll") {
		t.Fatalf("expected status bindings to include scroll hint, got %q", joined)
	}
	if !strings.Contains(joined, "ctrl+u/d") {
		t.Fatalf("expected status bindings to include ctrl+u/d hint, got %q", joined)
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
