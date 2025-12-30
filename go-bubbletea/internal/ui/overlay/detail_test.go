package overlay

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDetailPanel(t *testing.T) {
	task := domain.Task{
		ID:          "test-123",
		Title:       "Test Task",
		Description: "Test description",
		Status:      domain.StatusOpen,
		Priority:    domain.P0,
		Type:        domain.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	panel := NewDetailPanel(task, nil)
	require.NotNil(t, panel)
	assert.Equal(t, task.ID, panel.task.ID)
	assert.Nil(t, panel.session)
	assert.Equal(t, 0, panel.scrollY)
}

func TestDetailPanelTitle(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task, nil)

	assert.Equal(t, "Task Details", panel.Title())
}

func TestDetailPanelSize(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task, nil)

	width, height := panel.Size()
	assert.Equal(t, 70, width)
	assert.Equal(t, 30, height)
}

func TestDetailPanelView(t *testing.T) {
	task := domain.Task{
		ID:          "az-123",
		Title:       "Implement feature",
		Description: "This is a test description",
		Status:      domain.StatusInProgress,
		Priority:    domain.P1,
		Type:        domain.TypeFeature,
		CreatedAt:   time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2025, 1, 2, 14, 30, 0, 0, time.UTC),
	}

	panel := NewDetailPanel(task, nil)
	view := panel.View()

	// Check that key information is present
	assert.Contains(t, view, "az-123")
	assert.Contains(t, view, "Implement feature")
	assert.Contains(t, view, "In Progress")
	assert.Contains(t, view, "P1")
	assert.Contains(t, view, "feature")
	assert.Contains(t, view, "This is a test description")
}

func TestDetailPanelViewWithSession(t *testing.T) {
	startTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	task := domain.Task{
		ID:     "az-456",
		Title:  "Task with session",
		Status: domain.StatusInProgress,
	}

	session := &domain.Session{
		BeadID:    "az-456",
		State:     domain.SessionBusy,
		StartedAt: &startTime,
		Worktree:  "/path/to/worktree",
		DevServer: &domain.DevServer{
			Port:    3000,
			Command: "npm run dev",
			Running: true,
		},
	}

	panel := NewDetailPanel(task, session)
	view := panel.View()

	// Check session info is present
	assert.Contains(t, view, "Session")
	assert.Contains(t, view, "busy")
	assert.Contains(t, view, "/path/to/worktree")
	assert.Contains(t, view, ":3000")
	assert.Contains(t, view, "npm run dev")
}

func TestDetailPanelViewWithParent(t *testing.T) {
	parentID := "az-parent"
	task := domain.Task{
		ID:       "az-child",
		Title:    "Child task",
		ParentID: &parentID,
		Status:   domain.StatusOpen,
	}

	panel := NewDetailPanel(task, nil)
	view := panel.View()

	assert.Contains(t, view, "Parent:")
	assert.Contains(t, view, "az-parent")
}

func TestDetailPanelScrolling(t *testing.T) {
	// Create a task with a long description
	lines := make([]string, 50)
	for i := 0; i < 50; i++ {
		lines[i] = "Line " + string(rune('A'+i%26))
	}
	description := strings.Join(lines, "\n")

	task := domain.Task{
		ID:          "test",
		Description: description,
	}

	panel := NewDetailPanel(task, nil)

	// Initial scroll position should be 0
	assert.Equal(t, 0, panel.scrollY)

	// Scroll down
	m, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 1, panel.scrollY)

	// Scroll down multiple times
	for i := 0; i < 5; i++ {
		m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		panel = m.(*DetailPanel)
	}
	assert.Equal(t, 6, panel.scrollY)

	// Scroll up
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 5, panel.scrollY)

	// Jump to top
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 0, panel.scrollY)

	// Jump to bottom
	m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	panel = m.(*DetailPanel)
	assert.Greater(t, panel.scrollY, 0)
}

func TestDetailPanelScrollLimits(t *testing.T) {
	task := domain.Task{
		ID:          "test",
		Description: "Short description",
	}

	panel := NewDetailPanel(task, nil)

	// Should not scroll below 0
	m, _ := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	panel = m.(*DetailPanel)
	assert.Equal(t, 0, panel.scrollY)

	// Should not scroll past maxScroll
	for i := 0; i < 100; i++ {
		m, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		panel = m.(*DetailPanel)
	}
	assert.LessOrEqual(t, panel.scrollY, panel.maxScroll())
}

func TestDetailPanelEscapeCloses(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task, nil)

	// Test Esc key
	_, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	closeMsg, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
	assert.NotNil(t, closeMsg)

	// Test q key
	_, cmd = panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)

	msg = cmd()
	closeMsg, ok = msg.(CloseOverlayMsg)
	assert.True(t, ok)
	assert.NotNil(t, closeMsg)
}

func TestDetailPanelFormatStatus(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task, nil)

	tests := []struct {
		status   domain.Status
		expected string
	}{
		{domain.StatusOpen, "Open"},
		{domain.StatusInProgress, "In Progress"},
		{domain.StatusBlocked, "Blocked"},
		{domain.StatusDone, "Done"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := panel.formatStatus(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetailPanelFormatDuration(t *testing.T) {
	task := domain.Task{ID: "test"}
	panel := NewDetailPanel(task, nil)

	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{3665 * time.Second, "1h 1m 5s"},
		{7200 * time.Second, "2h 0m 0s"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := panel.formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}
