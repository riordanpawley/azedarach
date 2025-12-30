package overlay

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestEpicDrillDown_Init(t *testing.T) {
	epic := domain.Task{
		ID:    "az-1",
		Title: "Test Epic",
		Type:  domain.TypeEpic,
	}
	overlay := NewEpicDrillDown(epic, nil)

	if cmd := overlay.Init(); cmd != nil {
		t.Errorf("Init() should return nil, got %v", cmd)
	}
}

func TestEpicDrillDown_Navigation(t *testing.T) {
	epic := domain.Task{
		ID:    "az-1",
		Title: "Test Epic",
		Type:  domain.TypeEpic,
	}
	children := []domain.Task{
		{ID: "az-2", Title: "Child 1", Status: domain.StatusOpen},
		{ID: "az-3", Title: "Child 2", Status: domain.StatusInProgress},
		{ID: "az-4", Title: "Child 3", Status: domain.StatusDone},
	}
	overlay := NewEpicDrillDown(epic, children)

	tests := []struct {
		name           string
		key            string
		expectedCursor int
	}{
		{"down from start", "j", 1},
		{"down to last", "j", 2},
		{"down at end (no wrap)", "j", 2},
		{"up from end", "k", 1},
		{"up to start", "k", 0},
		{"up at start (no wrap)", "k", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			model, _ := overlay.Update(msg)
			overlay = model.(*EpicDrillDown)

			if overlay.cursor != tt.expectedCursor {
				t.Errorf("cursor = %d, want %d", overlay.cursor, tt.expectedCursor)
			}
		})
	}
}

func TestEpicDrillDown_Selection(t *testing.T) {
	epic := domain.Task{
		ID:    "az-1",
		Title: "Test Epic",
		Type:  domain.TypeEpic,
	}
	children := []domain.Task{
		{ID: "az-2", Title: "Child 1", Status: domain.StatusOpen},
		{ID: "az-3", Title: "Child 2", Status: domain.StatusInProgress},
	}
	overlay := NewEpicDrillDown(epic, children)

	// Move to second child
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}
	model, _ := overlay.Update(msg)
	overlay = model.(*EpicDrillDown)

	// Select
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	_, cmd := overlay.Update(msg)

	if cmd == nil {
		t.Fatal("Expected selection command, got nil")
	}

	result := cmd()
	selMsg, ok := result.(SelectionMsg)
	if !ok {
		t.Fatalf("Expected SelectionMsg, got %T", result)
	}

	if selMsg.Key != "select_child" {
		t.Errorf("Key = %s, want select_child", selMsg.Key)
	}

	if selMsg.Value != "az-3" {
		t.Errorf("Value = %s, want az-3", selMsg.Value)
	}
}

func TestEpicDrillDown_Close(t *testing.T) {
	epic := domain.Task{
		ID:    "az-1",
		Title: "Test Epic",
		Type:  domain.TypeEpic,
	}
	overlay := NewEpicDrillDown(epic, nil)

	tests := []struct {
		name string
		key  string
	}{
		{"q key", "q"},
		{"esc key", "esc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tt.key)}
			if tt.key == "esc" {
				msg.Type = tea.KeyEscape
			}

			_, cmd := overlay.Update(msg)
			if cmd == nil {
				t.Fatal("Expected close command, got nil")
			}

			result := cmd()
			if _, ok := result.(CloseOverlayMsg); !ok {
				t.Errorf("Expected CloseOverlayMsg, got %T", result)
			}
		})
	}
}

func TestEpicDrillDown_ProgressCalculation(t *testing.T) {
	tests := []struct {
		name     string
		children []domain.Task
		wantBar  string // Substring to check for in progress bar
	}{
		{
			name:     "no children",
			children: []domain.Task{},
			wantBar:  "0/0 (0%)",
		},
		{
			name: "no completed",
			children: []domain.Task{
				{Status: domain.StatusOpen},
				{Status: domain.StatusInProgress},
			},
			wantBar: "0/2 (0%)",
		},
		{
			name: "half completed",
			children: []domain.Task{
				{Status: domain.StatusDone},
				{Status: domain.StatusOpen},
			},
			wantBar: "1/2 (50%)",
		},
		{
			name: "all completed",
			children: []domain.Task{
				{Status: domain.StatusDone},
				{Status: domain.StatusDone},
			},
			wantBar: "2/2 (100%)",
		},
		{
			name: "partial progress",
			children: []domain.Task{
				{Status: domain.StatusDone},
				{Status: domain.StatusDone},
				{Status: domain.StatusOpen},
				{Status: domain.StatusInProgress},
			},
			wantBar: "2/4 (50%)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epic := domain.Task{
				ID:    "az-1",
				Title: "Test Epic",
				Type:  domain.TypeEpic,
			}
			overlay := NewEpicDrillDown(epic, tt.children)
			progressBar := overlay.renderProgressBar()

			if !strings.Contains(progressBar, tt.wantBar) {
				t.Errorf("Progress bar should contain %q, got %q", tt.wantBar, progressBar)
			}
		})
	}
}

func TestEpicDrillDown_View(t *testing.T) {
	epic := domain.Task{
		ID:        "az-1",
		Title:     "Implement Authentication",
		Type:      domain.TypeEpic,
		Status:    domain.StatusInProgress,
		Priority:  domain.P1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	session := &domain.Session{
		BeadID: "az-2",
		State:  domain.SessionBusy,
	}

	children := []domain.Task{
		{
			ID:       "az-2",
			Title:    "Create login form",
			Status:   domain.StatusDone,
			Priority: domain.P1,
			Session:  session,
		},
		{
			ID:       "az-3",
			Title:    "Implement JWT validation",
			Status:   domain.StatusInProgress,
			Priority: domain.P0,
		},
		{
			ID:       "az-4",
			Title:    "Add password reset flow",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
		},
	}

	overlay := NewEpicDrillDown(epic, children)
	view := overlay.View()

	// Check epic title is present
	if !strings.Contains(view, "Implement Authentication") {
		t.Error("View should contain epic title")
	}

	// Check child titles are present
	if !strings.Contains(view, "Create login form") {
		t.Error("View should contain first child title")
	}
	if !strings.Contains(view, "Implement JWT validation") {
		t.Error("View should contain second child title")
	}
	if !strings.Contains(view, "Add password reset flow") {
		t.Error("View should contain third child title")
	}

	// Check progress bar is present
	if !strings.Contains(view, "1/3") {
		t.Error("View should contain progress stats")
	}

	// Check child IDs are present
	if !strings.Contains(view, "az-2") {
		t.Error("View should contain child ID az-2")
	}

	// Check footer is present
	if !strings.Contains(view, "Enter: select") {
		t.Error("View should contain footer with keybindings")
	}
}

func TestEpicDrillDown_ViewEmptyChildren(t *testing.T) {
	epic := domain.Task{
		ID:    "az-1",
		Title: "Empty Epic",
		Type:  domain.TypeEpic,
	}

	overlay := NewEpicDrillDown(epic, []domain.Task{})
	view := overlay.View()

	if !strings.Contains(view, "No child tasks") {
		t.Error("View should show 'No child tasks' message")
	}
}

func TestEpicDrillDown_Title(t *testing.T) {
	epic := domain.Task{
		ID:    "az-123",
		Title: "Test Epic",
		Type:  domain.TypeEpic,
	}

	overlay := NewEpicDrillDown(epic, nil)
	title := overlay.Title()

	expected := "Epic: az-123"
	if title != expected {
		t.Errorf("Title() = %s, want %s", title, expected)
	}
}

func TestEpicDrillDown_Size(t *testing.T) {
	epic := domain.Task{
		ID:    "az-1",
		Title: "Test Epic",
		Type:  domain.TypeEpic,
	}

	tests := []struct {
		name           string
		childrenCount  int
		expectedHeight int
	}{
		{"no children", 0, 8},
		{"one child", 1, 7},
		{"three children", 3, 9},
		{"ten children", 10, 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			children := make([]domain.Task, tt.childrenCount)
			for i := range children {
				children[i] = domain.Task{
					ID:     "az-" + string(rune('2'+i)),
					Status: domain.StatusOpen,
				}
			}

			overlay := NewEpicDrillDown(epic, children)
			width, height := overlay.Size()

			if width != 60 {
				t.Errorf("Width = %d, want 60", width)
			}

			if height != tt.expectedHeight {
				t.Errorf("Height = %d, want %d", height, tt.expectedHeight)
			}
		})
	}
}
