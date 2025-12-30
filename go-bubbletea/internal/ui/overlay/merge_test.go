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
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Target 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Target 2", Status: domain.StatusDone, HasWorktree: true},
	}

	overlay := NewMergeSelectOverlay(&source, candidates, nil, nil)

	require.NotNil(t, overlay)
	assert.Equal(t, source.ID, overlay.source.ID)
	assert.Equal(t, 2, len(overlay.candidates))
	assert.Equal(t, 0, overlay.cursor)
}

func TestMergeSelectOverlay_Title(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(&source, []MergeTarget{}, nil, nil)

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
			expectedHeight:  5, // 4 + 1 (no candidates message)
			expectedWidth:   60,
		},
		{
			name:            "few candidates",
			candidatesCount: 5,
			expectedHeight:  9, // 4 + 5
			expectedWidth:   60,
		},
		{
			name:            "many candidates capped at 15",
			candidatesCount: 20,
			expectedHeight:  19, // 4 + 15
			expectedWidth:   60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
			candidates := make([]MergeTarget, tt.candidatesCount)
			for i := 0; i < tt.candidatesCount; i++ {
				candidates[i] = MergeTarget{
					ID:          "az-" + string(rune(i)),
					Label:       "Task",
					Status:      domain.StatusOpen,
					HasWorktree: true,
				}
			}

			overlay := NewMergeSelectOverlay(&source, candidates, nil, nil)
			width, height := overlay.Size()

			assert.Equal(t, tt.expectedWidth, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestMergeSelectOverlay_Navigation(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Target 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Target 2", Status: domain.StatusDone, HasWorktree: true},
		{ID: "az-101", Label: "Target 3", Status: domain.StatusBlocked, HasWorktree: true},
	}

	overlay := NewMergeSelectOverlay(&source, candidates, nil, nil)

	// Move down
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 1, overlay.cursor)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 2, overlay.cursor)

	// Wraps around to start
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 0, overlay.cursor)

	// Move up wraps to end
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyUp})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 2, overlay.cursor)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	overlay = m.(*MergeSelectOverlay)
	assert.Equal(t, 1, overlay.cursor)
}

func TestMergeSelectOverlay_SelectTarget(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusInProgress, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Target 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Target 2", Status: domain.StatusDone, HasWorktree: true},
	}

	overlay := NewMergeSelectOverlay(&source, candidates, nil, nil)

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
	overlay := NewMergeSelectOverlay(&source, []MergeTarget{}, nil, nil)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestMergeSelectOverlay_QuitClose(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(&source, []MergeTarget{}, nil, nil)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestMergeSelectOverlay_View(t *testing.T) {
	source := makeTask("az-123", "Implement feature X", domain.StatusInProgress, domain.TypeFeature)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Related task 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Related task 2", Status: domain.StatusDone, HasWorktree: true},
	}

	overlay := NewMergeSelectOverlay(&source, candidates, nil, nil)
	view := overlay.View()

	// Check that view contains expected elements
	assert.Contains(t, view, "Merge")
	assert.Contains(t, view, source.ID)
	assert.Contains(t, view, "into:")
	assert.Contains(t, view, candidates[0].ID)
	assert.Contains(t, view, candidates[0].Label)
	assert.Contains(t, view, candidates[1].ID)
	assert.Contains(t, view, candidates[1].Label)
	assert.Contains(t, view, "j/k")
	assert.Contains(t, view, "Enter")
}

func TestMergeSelectOverlay_ViewNoCandidates(t *testing.T) {
	source := makeTask("az-123", "Lonely task", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(&source, []MergeTarget{}, nil, nil)

	view := overlay.View()

	assert.Contains(t, view, "Merge")
	assert.Contains(t, view, source.ID)
	assert.Contains(t, view, "No eligible merge targets")
}

func TestMergeSelectOverlay_RenderCandidate(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	target := MergeTarget{
		ID:          "az-456",
		Label:       "Test task",
		Status:      domain.StatusInProgress,
		HasWorktree: true,
	}

	overlay := NewMergeSelectOverlay(&source, []MergeTarget{target}, nil, nil)

	// Test unselected
	formatted := overlay.renderCandidate(target, false)
	assert.Contains(t, formatted, target.ID)
	assert.Contains(t, formatted, target.Label)
	assert.Contains(t, formatted, string(target.Status))

	// Test selected
	formatted = overlay.renderCandidate(target, true)
	assert.Contains(t, formatted, "▸")
	assert.Contains(t, formatted, target.ID)
	assert.Contains(t, formatted, target.Label)
}

func TestMergeSelectOverlay_RenderMainBranch(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	mainTarget := MergeTarget{
		ID:          "main",
		Label:       "main branch",
		IsMain:      true,
		HasWorktree: false,
	}

	overlay := NewMergeSelectOverlay(&source, []MergeTarget{mainTarget}, nil, nil)

	formatted := overlay.renderCandidate(mainTarget, false)
	assert.Contains(t, formatted, "main")
	assert.Contains(t, formatted, "(main branch)")
}

func TestMergeSelectOverlay_Init(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(&source, []MergeTarget{}, nil, nil)

	cmd := overlay.Init()
	assert.Nil(t, cmd)
}

func TestMergeSelectOverlay_EnterWithNoCandidates(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSelectOverlay(&source, []MergeTarget{}, nil, nil)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "should not send message when no candidates")
}

func TestMergeSelectOverlay_WithCallbacks(t *testing.T) {
	source := makeTask("az-123", "Source", domain.StatusInProgress, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Target 1", Status: domain.StatusOpen, HasWorktree: true},
	}

	mergeCalled := false
	cancelCalled := false

	onMerge := func(targetID string) tea.Cmd {
		mergeCalled = true
		assert.Equal(t, "az-456", targetID)
		return nil
	}

	onCancel := func() tea.Cmd {
		cancelCalled = true
		return nil
	}

	overlay := NewMergeSelectOverlay(&source, candidates, onMerge, onCancel)

	// Test merge callback
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		cmd()
	}
	assert.True(t, mergeCalled)

	// Test cancel callback
	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		cmd()
	}
	assert.True(t, cancelCalled)
}
