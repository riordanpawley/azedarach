package overlay

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTask(id, title string, status domain.Status, taskType domain.TaskType) domain.Task {
	now := time.Now()
	return domain.Task{
		ID:        naming.IssueID(id),
		Title:     title,
		Status:    status,
		Type:      taskType,
		Priority:  domain.P1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestNewMergeSourceSelectOverlay(t *testing.T) {
	target := makeTask("az-123", "Target task", domain.StatusInProgress, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Source 1", Status: domain.StatusInProgress, HasWorktree: true},
		{ID: "az-789", Label: "Source 2", Status: domain.StatusDone, HasWorktree: true},
	}

	overlay := NewMergeSourceSelectOverlay(&target, candidates, nil, nil)

	require.NotNil(t, overlay)
	assert.Equal(t, target.ID, overlay.target.ID)
	assert.Equal(t, 2, len(overlay.candidates))
	assert.Equal(t, 0, overlay.cursor)
}

func TestMergeSourceSelectOverlay_Title(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{}, nil, nil)

	assert.Equal(t, "Select Upstream Source", overlay.Title())
}

func TestMergeSourceSelectOverlay_Size(t *testing.T) {
	tests := []struct {
		name            string
		candidatesCount int
		expectedHeight  int
		expectedWidth   int
	}{
		{name: "no candidates", candidatesCount: 0, expectedHeight: 10, expectedWidth: 60},
		{name: "few candidates", candidatesCount: 5, expectedHeight: 13, expectedWidth: 60},
		{name: "many candidates capped at 15", candidatesCount: 20, expectedHeight: 23, expectedWidth: 60},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
			candidates := make([]MergeTarget, tt.candidatesCount)
			for i := 0; i < tt.candidatesCount; i++ {
				candidates[i] = MergeTarget{
					ID:          "az-" + string(rune(i)),
					Label:       "Task",
					Status:      domain.StatusOpen,
					HasWorktree: true,
				}
			}

			overlay := NewMergeSourceSelectOverlay(&target, candidates, nil, nil)
			width, height := overlay.Size()

			assert.Equal(t, tt.expectedWidth, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestMergeSourceSelectOverlay_Navigation(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Source 1", Status: domain.StatusOpen, HasWorktree: true},
		{ID: "az-789", Label: "Source 2", Status: domain.StatusDone, HasWorktree: true},
		{ID: "az-101", Label: "Source 3", Status: domain.StatusInReview, HasWorktree: true},
	}

	overlay := NewMergeSourceSelectOverlay(&target, candidates, nil, nil)

	// Move down
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	overlay = m.(*MergeSourceSelectOverlay)
	assert.Equal(t, 1, overlay.cursor)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSourceSelectOverlay)
	assert.Equal(t, 2, overlay.cursor)

	// Wraps around to start
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSourceSelectOverlay)
	assert.Equal(t, 0, overlay.cursor)

	// Move up wraps to end
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyUp})
	overlay = m.(*MergeSourceSelectOverlay)
	assert.Equal(t, 2, overlay.cursor)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	overlay = m.(*MergeSourceSelectOverlay)
	assert.Equal(t, 1, overlay.cursor)
}

func TestMergeSourceSelectOverlay_SelectEmitsSourceAndTarget(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusInProgress, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Source 1", Status: domain.StatusInProgress, HasWorktree: true},
		{ID: "az-789", Label: "Source 2", Status: domain.StatusDone, HasWorktree: true},
	}

	overlay := NewMergeSourceSelectOverlay(&target, candidates, nil, nil)
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyDown})
	overlay = m.(*MergeSourceSelectOverlay)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "merge", selMsg.Key)

	result, ok := selMsg.Value.(MergeTargetSelectedMsg)
	require.True(t, ok)
	// The selected upstream candidate becomes the SOURCE; the focused task is
	// the TARGET that receives the merge.
	assert.Equal(t, "az-789", result.SourceID)
	assert.Equal(t, "az-123", result.TargetID)
}

func TestMergeSourceSelectOverlay_EscapeClose(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{}, nil, nil)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestMergeSourceSelectOverlay_QuitClose(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{}, nil, nil)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestMergeSourceSelectOverlay_View(t *testing.T) {
	target := makeTask("az-123", "Implement feature X", domain.StatusInProgress, domain.TypeFeature)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Upstream 1", Status: domain.StatusInProgress, HasWorktree: true},
		{ID: "az-789", Label: "Upstream 2", Status: domain.StatusDone, HasWorktree: true},
	}

	overlay := NewMergeSourceSelectOverlay(&target, candidates, nil, nil)
	view := overlay.View()

	assert.Contains(t, view, "Merge into")
	assert.Contains(t, view, target.ID)
	assert.Contains(t, view, "from:")
	assert.Contains(t, view, candidates[0].ID)
	assert.Contains(t, view, candidates[0].Label)
	assert.Contains(t, view, candidates[1].ID)
	assert.Contains(t, view, candidates[1].Label)
	assert.Contains(t, view, "j/k")
	assert.Contains(t, view, "Enter")
}

func TestMergeSourceSelectOverlay_ViewNoCandidates(t *testing.T) {
	target := makeTask("az-123", "Lonely task", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{}, nil, nil)

	view := overlay.View()

	assert.Contains(t, view, "Merge into")
	assert.Contains(t, view, target.ID)
	assert.Contains(t, view, "No eligible upstream sources")
}

func TestMergeSourceSelectOverlay_RenderCandidate(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	src := MergeTarget{
		ID:          "az-456",
		Label:       "Test task",
		Status:      domain.StatusInProgress,
		HasWorktree: true,
	}

	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{src}, nil, nil)

	formatted := overlay.renderCandidate(src, false)
	assert.Contains(t, formatted, src.ID)
	assert.Contains(t, formatted, src.Label)
	assert.Contains(t, formatted, string(src.Status))

	formatted = overlay.renderCandidate(src, true)
	assert.Contains(t, formatted, "▸")
	assert.Contains(t, formatted, src.ID)
	assert.Contains(t, formatted, src.Label)
}

func TestMergeSourceSelectOverlay_RenderMainBranch(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	mainSource := MergeTarget{
		ID:          "main",
		Label:       "develop",
		IsMain:      true,
		HasWorktree: false,
	}

	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{mainSource}, nil, nil)

	formatted := overlay.renderCandidate(mainSource, false)
	assert.Contains(t, formatted, "develop")
	assert.Contains(t, formatted, "(base branch)")
}

func TestMergeSourceSelectOverlay_Init(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{}, nil, nil)

	cmd := overlay.Init()
	assert.Nil(t, cmd)
}

func TestMergeSourceSelectOverlay_EnterWithNoCandidates(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusOpen, domain.TypeTask)
	overlay := NewMergeSourceSelectOverlay(&target, []MergeTarget{}, nil, nil)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "should not send message when no candidates")
}

func TestMergeSourceSelectOverlay_WithCallbacks(t *testing.T) {
	target := makeTask("az-123", "Target", domain.StatusInProgress, domain.TypeTask)
	candidates := []MergeTarget{
		{ID: "az-456", Label: "Source 1", Status: domain.StatusOpen, HasWorktree: true},
	}

	mergeCalled := false
	cancelCalled := false

	onMerge := func(sourceID string) tea.Cmd {
		mergeCalled = true
		assert.Equal(t, "az-456", sourceID)
		return nil
	}

	onCancel := func() tea.Cmd {
		cancelCalled = true
		return nil
	}

	overlay := NewMergeSourceSelectOverlay(&target, candidates, onMerge, onCancel)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		cmd()
	}
	assert.True(t, mergeCalled)

	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		cmd()
	}
	assert.True(t, cancelCalled)
}
