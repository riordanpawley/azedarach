package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConflictDialog(t *testing.T) {
	files := []string{"file1.go", "file2.go"}
	dialog := NewConflictDialog(files)

	require.NotNil(t, dialog)
	assert.Equal(t, files, dialog.files)
	assert.Equal(t, 0, dialog.cursor)
}

func TestConflictDialog_Title(t *testing.T) {
	dialog := NewConflictDialog([]string{})
	assert.Equal(t, "Merge Conflicts", dialog.Title())
}

func TestConflictDialog_Size(t *testing.T) {
	tests := []struct {
		name           string
		files          []string
		expectedHeight int
	}{
		{
			name:           "no files",
			files:          []string{},
			expectedHeight: 14,
		},
		{
			name:           "few files",
			files:          []string{"file1.go", "file2.go", "file3.go"},
			expectedHeight: 17,
		},
		{
			name:           "many files capped at 12",
			files:          make([]string, 20),
			expectedHeight: 26,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialog := NewConflictDialog(tt.files)
			width, height := dialog.Size()

			assert.Equal(t, 100, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestConflictDialog_Navigation(t *testing.T) {
	files := []string{"file1.go", "file2.go", "file3.go"}
	dialog := NewConflictDialog(files)

	// Move down
	m, _ := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	dialog = m.(*ConflictOverlay)
	assert.Equal(t, 1, dialog.cursor)

	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	dialog = m.(*ConflictOverlay)
	assert.Equal(t, 2, dialog.cursor)

	// Can't go past end
	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	dialog = m.(*ConflictOverlay)
	assert.Equal(t, 2, dialog.cursor)

	// Move up
	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	dialog = m.(*ConflictOverlay)
	assert.Equal(t, 1, dialog.cursor)

	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyUp})
	dialog = m.(*ConflictOverlay)
	assert.Equal(t, 0, dialog.cursor)

	// Can't go past start
	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyUp})
	dialog = m.(*ConflictOverlay)
	assert.Equal(t, 0, dialog.cursor)
}

func TestConflictDialog_ResolveWithAgent(t *testing.T) {
	dialog := NewConflictDialogForIssue([]string{"file1.go"}, "az-1", "/tmp/az-1")

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "agent", selMsg.Key)

	result, ok := selMsg.Value.(ConflictResolutionMsg)
	require.True(t, ok)
	assert.True(t, result.ResolveWithAgent)
	assert.False(t, result.Abort)
	assert.False(t, result.OpenManually)
	assert.Equal(t, "az-1", result.IssueID)
	assert.Equal(t, "/tmp/az-1", result.Worktree)
}

func TestConflictDialog_Abort(t *testing.T) {
	dialog := NewConflictDialog([]string{"file1.go"})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "abort", selMsg.Key)

	result, ok := selMsg.Value.(ConflictResolutionMsg)
	require.True(t, ok)
	assert.True(t, result.Abort)
	assert.False(t, result.ResolveWithAgent)
	assert.False(t, result.OpenManually)
}

func TestConflictDialog_OpenManually(t *testing.T) {
	dialog := NewConflictDialogForIssue([]string{"file1.go"}, "az-1", "/tmp/az-1")

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "manual", selMsg.Key)

	result, ok := selMsg.Value.(ConflictResolutionMsg)
	require.True(t, ok)
	assert.True(t, result.OpenManually)
	assert.False(t, result.ResolveWithAgent)
	assert.False(t, result.Abort)
	assert.Equal(t, "az-1", result.IssueID)
	assert.Equal(t, "/tmp/az-1", result.Worktree)
}

func TestConflictDialog_EscapeClose(t *testing.T) {
	dialog := NewConflictDialog([]string{"file1.go"})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestConflictDialog_View(t *testing.T) {
	files := []string{"internal/ui/app.go", "internal/domain/task.go"}
	dialog := NewConflictDialog(files)

	view := dialog.View()

	// Check that view contains expected elements
	assert.Contains(t, view, "MERGE CONFLICTS")
	assert.Contains(t, view, "Actions")
	assert.Contains(t, view, "Agent resolve now? Press c.")
	assert.Contains(t, view, files[0])
	assert.Contains(t, view, files[1])
	assert.Contains(t, view, "agent resolve")
	assert.Contains(t, view, "open")
	assert.Contains(t, view, "abort")
	assert.Contains(t, view, "j/k")
}

func TestConflictDialog_Init(t *testing.T) {
	dialog := NewConflictDialog([]string{})
	cmd := dialog.Init()
	assert.Nil(t, cmd)
}

func TestConflictDialog_StatusBindings(t *testing.T) {
	dialog := NewConflictDialog([]string{"file1.go"})
	bindings := dialog.StatusBindings()

	assert.Contains(t, bindings, keybinds.Binding{Key: "c", Description: "agent resolve"})
	assert.Contains(t, bindings, keybinds.Binding{Key: "a", Description: "abort"})
	assert.Contains(t, bindings, keybinds.Binding{Key: "Esc/q", Description: "close"})
}
