package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
			expectedHeight: 13, // 8 + 0 + 5
		},
		{
			name:           "few files",
			files:          []string{"file1.go", "file2.go", "file3.go"},
			expectedHeight: 16, // 8 + 3 + 5
		},
		{
			name:           "many files capped at 10",
			files:          make([]string, 20),
			expectedHeight: 23, // 8 + 10 + 5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialog := NewConflictDialog(tt.files)
			width, height := dialog.Size()

			assert.Equal(t, 70, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestConflictDialog_Navigation(t *testing.T) {
	files := []string{"file1.go", "file2.go", "file3.go"}
	dialog := NewConflictDialog(files)

	// Move down
	m, _ := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	dialog = m.(*ConflictDialog)
	assert.Equal(t, 1, dialog.cursor)

	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	dialog = m.(*ConflictDialog)
	assert.Equal(t, 2, dialog.cursor)

	// Can't go past end
	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	dialog = m.(*ConflictDialog)
	assert.Equal(t, 2, dialog.cursor)

	// Move up
	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	dialog = m.(*ConflictDialog)
	assert.Equal(t, 1, dialog.cursor)

	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyUp})
	dialog = m.(*ConflictDialog)
	assert.Equal(t, 0, dialog.cursor)

	// Can't go past start
	m, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyUp})
	dialog = m.(*ConflictDialog)
	assert.Equal(t, 0, dialog.cursor)
}

func TestConflictDialog_ResolveWithClaude(t *testing.T) {
	dialog := NewConflictDialog([]string{"file1.go"})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "claude", selMsg.Key)

	result, ok := selMsg.Value.(ConflictResolutionMsg)
	require.True(t, ok)
	assert.True(t, result.ResolveWithClaude)
	assert.False(t, result.Abort)
	assert.False(t, result.OpenManually)
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
	assert.False(t, result.ResolveWithClaude)
	assert.False(t, result.OpenManually)
}

func TestConflictDialog_OpenManually(t *testing.T) {
	dialog := NewConflictDialog([]string{"file1.go"})

	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	require.NotNil(t, cmd)

	msg := cmd()
	selMsg, ok := msg.(SelectionMsg)
	require.True(t, ok)
	assert.Equal(t, "manual", selMsg.Key)

	result, ok := selMsg.Value.(ConflictResolutionMsg)
	require.True(t, ok)
	assert.True(t, result.OpenManually)
	assert.False(t, result.ResolveWithClaude)
	assert.False(t, result.Abort)
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
	assert.Contains(t, view, "Merge conflicts detected!")
	assert.Contains(t, view, "Conflicted files:")
	assert.Contains(t, view, files[0])
	assert.Contains(t, view, files[1])
	assert.Contains(t, view, "[c]")
	assert.Contains(t, view, "Resolve with Claude")
	assert.Contains(t, view, "[o]")
	assert.Contains(t, view, "Open manually")
	assert.Contains(t, view, "[a]")
	assert.Contains(t, view, "Abort merge")
	assert.Contains(t, view, "j/k: Navigate")
}

func TestConflictDialog_Init(t *testing.T) {
	dialog := NewConflictDialog([]string{})
	cmd := dialog.Init()
	assert.Nil(t, cmd)
}
