package overlay

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrchestrationOverlay_Title(t *testing.T) {
	overlay := NewOrchestrationOverlay(nil, nil, nil, nil)
	assert.Equal(t, "Session Orchestration", overlay.Title())
}

func TestOrchestrationOverlay_Size(t *testing.T) {
	started := time.Date(2026, time.March, 30, 8, 0, 0, 0, time.UTC)
	overlay := NewOrchestrationOverlay([]SessionInfo{
		{
			IssueID:   "az-123",
			TaskTitle: "Implement feature X",
			State:     domain.SessionBusy,
			StartedAt: &started,
			Worktree:  "/Users/riordan/prog/azedarach",
		},
	}, nil, nil, nil)

	width, height := overlay.Size()
	assert.Greater(t, width, 0)
	assert.Greater(t, height, 0)
}

func TestOrchestrationOverlay_View(t *testing.T) {
	started := time.Date(2026, time.March, 30, 8, 0, 0, 0, time.UTC)
	overlay := NewOrchestrationOverlay([]SessionInfo{
		{
			IssueID:      "az-123",
			TaskTitle:    "Implement feature X with careful layout handling",
			State:        domain.SessionBusy,
			StartedAt:    &started,
			Worktree:     "/Users/riordan/prog/azedarach",
			RecentOutput: "build finished\nview rendered\nrenderDialogTwoPane ok",
		},
	}, nil, nil, nil)

	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	overlay = model.(*OrchestrationOverlay)

	view := overlay.View()
	require.NotEmpty(t, view)
	assert.Contains(t, view, "Session Orchestration")
	assert.Contains(t, view, "Active Sessions")
	assert.Contains(t, view, "Actions")
	assert.Contains(t, view, "Enter/a")
}

func TestOrchestrationOverlay_WindowSizeFitsNarrowViewport(t *testing.T) {
	started := time.Date(2026, time.March, 30, 8, 0, 0, 0, time.UTC)
	overlay := NewOrchestrationOverlay([]SessionInfo{
		{
			IssueID:   "az-123",
			TaskTitle: "Implement feature X with careful layout handling",
			State:     domain.SessionBusy,
			StartedAt: &started,
			Worktree:  "/Users/riordan/prog/azedarach",
		},
	}, nil, nil, nil)

	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	overlay = model.(*OrchestrationOverlay)

	width, _ := overlay.Size()
	assert.LessOrEqual(t, width, 72)
}
