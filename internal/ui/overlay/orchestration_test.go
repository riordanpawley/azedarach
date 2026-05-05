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

func TestOrchestrationOverlay_Title(t *testing.T) {
	overlay := NewOrchestrationOverlay(nil, nil, nil, nil)
	assert.Equal(t, "Tmux Sessions", overlay.Title())
}

func TestOrchestrationOverlay_Size(t *testing.T) {
	started := time.Date(2026, time.March, 30, 8, 0, 0, 0, time.UTC)
	overlay := NewOrchestrationOverlay([]SessionInfo{
		{
			IssueID:        "az-123",
			TaskTitle:      "Implement feature X",
			IssueStatus:    domain.StatusInProgress,
			State:          domain.SessionBusy,
			StartedAt:      &started,
			Worktree:       "/Users/riordan/prog/azedarach",
			HasTmuxSession: true,
			HasWorktree:    true,
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
			IssueID:        "az-123",
			TaskTitle:      "Implement feature X with careful layout handling",
			IssueStatus:    domain.StatusInProgress,
			State:          domain.SessionBusy,
			StartedAt:      &started,
			Worktree:       "/Users/riordan/prog/azedarach",
			HasTmuxSession: true,
			HasWorktree:    true,
			GitAheadCount:  1,
			RecentOutput:   "build finished\nview rendered\nrenderDialogTwoPane ok",
		},
	}, nil, nil, nil)

	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	overlay = model.(*OrchestrationOverlay)

	view := overlay.View()
	require.NotEmpty(t, view)
	assert.Contains(t, view, "Tmux Sessions")
	assert.Contains(t, view, "Azedarach tmux sessions")
	assert.Contains(t, view, "Keys")
	assert.Contains(t, view, "git clean")
	assert.Contains(t, view, "Enter/a")
}

func TestOrchestrationOverlay_WindowSizeFitsNarrowViewport(t *testing.T) {
	started := time.Date(2026, time.March, 30, 8, 0, 0, 0, time.UTC)
	overlay := NewOrchestrationOverlay([]SessionInfo{
		{
			IssueID:        "az-123",
			TaskTitle:      "Implement feature X with careful layout handling",
			IssueStatus:    domain.StatusInProgress,
			State:          domain.SessionBusy,
			StartedAt:      &started,
			Worktree:       "/Users/riordan/prog/azedarach",
			HasTmuxSession: true,
			HasWorktree:    true,
		},
	}, nil, nil, nil)

	model, _ := overlay.Update(tea.WindowSizeMsg{Width: 72, Height: 22})
	overlay = model.(*OrchestrationOverlay)

	width, _ := overlay.Size()
	assert.LessOrEqual(t, width, 72)
}

func TestOrchestrationOverlay_RowRenderingIsStandalone(t *testing.T) {
	overlay := NewOrchestrationOverlay([]SessionInfo{{
		IssueID:               "bxl",
		TaskTitle:             "Add selector tests without booting full TUI",
		IssueStatus:           domain.StatusInProgress,
		State:                 domain.SessionBusy,
		Worktree:              "/Users/riordan/prog/azedarach-bxf/worktrees/bxl",
		HasTmuxSession:        true,
		HasWorktree:           true,
		GitAheadCount:         2,
		GitBehindCount:        1,
		HasUncommittedChanges: true,
		GitAdditions:          5,
		GitDeletions:          3,
	}}, nil, nil, nil)

	row := overlay.renderSession(0, overlay.sessions[0], 64)

	assert.Contains(t, row, "bxl")
	assert.Contains(t, row, "Add selector tests")
	assert.Contains(t, row, "tmux yes")
	assert.Contains(t, row, "worktree yes")
	assert.Contains(t, row, "git dirty")
	assert.NotContains(t, row, "No tmux sessions")
}

func TestOrchestrationOverlay_EnterAAndRefreshCallbacks(t *testing.T) {
	overlay := NewOrchestrationOverlay(
		[]SessionInfo{
			{IssueID: "bxl", TaskTitle: "Selector tests", HasTmuxSession: true},
			{IssueID: "bxf", TaskTitle: "Root epic", HasTmuxSession: true},
		},
		func(issueID string) tea.Cmd {
			return func() tea.Msg { return "attach:" + issueID }
		},
		nil,
		func() tea.Cmd {
			return func() tea.Msg { return "refresh" }
		},
	)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	assert.Equal(t, "attach:bxl", cmd())

	_, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	require.NotNil(t, cmd)
	assert.Equal(t, "attach:bxf", cmd())

	_, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	require.NotNil(t, cmd)
	assert.Equal(t, "refresh", cmd())

	view := overlay.View()
	if strings.Count(view, "Selector tests") != 1 {
		t.Fatalf("view should render selector row once, got %q", view)
	}
}
