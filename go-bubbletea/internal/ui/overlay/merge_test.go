package overlay

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTask(id, title string, status domain.Status, taskType domain.TaskType) domain.Task {
	now := time.Now()
	return domain.Task{
		ID:        id,
		Title:     title,
		Status:    status,
		Type:      taskType,
		Priority:  domain.P1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestNewMergeSelectOverlay(t *testing.T) {
	source := makeTask("az-123", "Source task", domain.StatusInProgress, domain.TypeTask)
	candidates := []domain.Task{
		makeTask("az-456", "Target 1", domain.StatusOpen, domain.TypeTask),
		makeTask("az-789", "Target 2", domain.StatusDone, domain.TypeFeature),
	}

	overlay := NewMergeSelectOverlay(source, candidates)

	require.NotNil(t, overlay)
	assert.Equal(t, source.ID, overlay.sourceTask.ID)
	assert.Equal(t, 2, len(overlay.candidates))
	assert.Equal(t, 0, overlay.cursor)
}

func TestMergeSelectOverlay_Title(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	assert.Equal(t, "Select Merge Target", overlay.Title())
}

func TestMergeSelectOverlay_Size(t *testing.T) {
	tests := []struct {
		name             string
		candidatesCount  int
		expectedHeight   int
		expectedWidth    int
	}{
		{
			name:            "no candidates",
			candidatesCount: 0,
			expectedHeight:  6, // 6 + 0
			expectedWidth:   80,
		},
		{
			name:            "few candidates",
			candidatesCount: 5,
			expectedHeight:  11, // 6 + 5
			expectedWidth:   80,
		},
		{
			name:            "many candidates capped at 15",
			candidatesCount: 20,
			expectedHeight:  21, // 6 + 15
			expectedWidth:   80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
			candidates := make([]domain.Task, tt.candidatesCount)
			for i := 0; i < tt.candidatesCount; i++ {
				candidates[i] = makeTask("az-"+string(rune(i)), "Task", domain.StatusOpen, domain.TypeTask)
			}

			overlay := NewMergeSelectOverlay(source, candidates)
			width, height := overlay.Size()

			assert.Equal(t, tt.expectedWidth, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestMergeSelectOverlay_Navigation(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	candidates := []domain.Task{
		makeTask("az-456", "Target 1", domain.StatusOpen, domain.TypeTask),
		makeTask("az-789", "Target 2", domain.StatusDone, domain.TypeFeature),
		makeTask("az-101", "Target 3", domain.StatusBlocked, domain.TypeBug),
	}

	overlay := NewMergeSelectOverlay(source, candidates)

	// Move down
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 1, overlay.cursor)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 2, overlay.cursor)

	// Can't go past end
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 2, overlay.cursor)

	// Move up
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 1, overlay.cursor)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyUp})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 0, overlay.cursor)

	// Can't go past start
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyUp})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 0, overlay.cursor)
}

func TestMergeSelectOverlay_SelectTarget(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusInProgress, domain.TypeTask)
	candidates := []domain.Task{
		makeTask("az-456", "Target 1", domain.StatusOpen, domain.TypeTask),
		makeTask("az-789", "Target 2", domain.StatusDone, domain.TypeFeature),
	}

	overlay := NewMergeSelectOverlay(source, candidates)

	// Move to second candidate
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSelectOverlay)

	// Select it
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "merge", selMsg.Key)

	result, ok := selMsg.Value.(MergeTargetSelectedMsg)
	require.True(t, ok)
	assert.Equal(t, "az-123", result.SourceID)
	assert.Equal(t, "az-789", result.TargetID)
}

func TestMergeSelectOverlay_EscapeClose(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestMergeSelectOverlay_QuitClose(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestMergeSelectOverlay_View(t *testing.T) {
	source := makeTask("az-123", "Implement feature X", domain.StatusInProgress, domain.TypeFeature)
	candidates := []domain.Task{
		makeTask("az-456", "Related task 1", domain.StatusOpen, domain.TypeTask),
		makeTask("az-789", "Related task 2", domain.StatusDone, domain.TypeBug),
	}

	overlay := NewMergeSelectOverlay(source, candidates)
	view := overlay.View()

	// Check that view contains expected elements
	assert.Contains(t, view, "Merge from:")
	assert.Contains(t, view, source.ID)
	assert.Contains(t, view, source.Title)
	assert.Contains(t, view, "Select merge target:")
	assert.Contains(t, view, candidates[0].ID)
	assert.Contains(t, view, candidates[0].Title)
	assert.Contains(t, view, candidates[1].ID)
	assert.Contains(t, view, candidates[1].Title)
	assert.Contains(t, view, "j/k: Navigate")
	assert.Contains(t, view, "Enter: Select")
}

func TestMergeSelectOverlay_ViewNoCandidates(t *testing.T) {
	source := makeTask("az-123", "Lonely task", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	view := overlay.View()

	assert.Contains(t, view, "Merge from:")
	assert.Contains(t, view, source.ID)
	assert.Contains(t, view, "No eligible tasks found")
}

func TestMergeSelectOverlay_FormatTask(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	task := makeTask("az-456", "Test task", domain.StatusInProgress, domain.TypeFeature)

	overlay := NewMergeSelectOverlay(source, []domain.Task{task})

	// Test unselected
	formatted := overlay.formatTask(task, false)
	assert.Contains(t, formatted, task.ID)
	assert.Contains(t, formatted, task.Title)
	assert.Contains(t, formatted, "[F]") // Feature type

	// Test selected
	formatted = overlay.formatTask(task, true)
	assert.Contains(t, formatted, "▸")
	assert.Contains(t, formatted, task.ID)
	assert.Contains(t, formatted, task.Title)
}

func TestMergeSelectOverlay_FormatStatus(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	statuses := []domain.Status{
		domain.StatusOpen,
		domain.StatusInProgress,
		domain.StatusBlocked,
		domain.StatusDone,
	}

	for _, status := range statuses {
		formatted := overlay.formatStatus(status, false)
		assert.Contains(t, formatted, string(status))

		formattedSelected := overlay.formatStatus(status, true)
		assert.Contains(t, formattedSelected, string(status))
	}
}

func TestMergeSelectOverlay_Init(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	cmd := overlay.Init()
	assert.Nil(t, cmd)
}

func TestMergeSelectOverlay_EnterWithNoCandidates(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(source, []domain.Task{})

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "should not send message when no candidates")
}
