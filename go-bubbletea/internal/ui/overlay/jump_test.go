package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestGenerateLabelsDeterministicOrder(t *testing.T) {
	labels := GenerateLabels(12)
	assert.Equal(t, []string{"a", "s", "d", "f", "g", "h", "j", "k", "l", ";", "aa", "ab"}, labels)
}

func TestGenerateLabelsAreUnique(t *testing.T) {
	labels := GenerateLabels(80)
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		_, exists := seen[label]
		assert.False(t, exists, "duplicate label: %q", label)
		seen[label] = struct{}{}
	}
	assert.Len(t, seen, len(labels))
}

func TestJumpModeSelectSingleCharLabel(t *testing.T) {
	jump := NewJumpMode(5)

	_, cmd := jump.Update(keyRune('a'))
	require.NotNil(t, cmd)

	msg := cmd()
	selected, ok := msg.(JumpSelectedMsg)
	require.True(t, ok)
	assert.Equal(t, 0, selected.TaskIndex)
}

func TestJumpModeSelectTwoCharAlphabetLabel(t *testing.T) {
	jump := NewJumpMode(12)

	_, cmd := jump.Update(keyRune('a'))
	assert.Nil(t, cmd)

	_, cmd = jump.Update(keyRune('b'))
	require.NotNil(t, cmd)

	msg := cmd()
	selected, ok := msg.(JumpSelectedMsg)
	require.True(t, ok)
	assert.Equal(t, 11, selected.TaskIndex)
}

func TestJumpModeInvalidLabelFailsSafely(t *testing.T) {
	jump := NewJumpMode(12)

	_, cmd := jump.Update(keyRune('a'))
	assert.Nil(t, cmd)

	_, cmd = jump.Update(keyRune(';'))
	assert.Nil(t, cmd)
	assert.Equal(t, "", jump.input)
}
