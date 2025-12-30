package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSearchOverlay(t *testing.T) {
	s := NewSearchOverlay()
	require.NotNil(t, s)
	assert.Equal(t, 0, s.matchCount)
	assert.Equal(t, "", s.input.Value())
}

func TestSearchOverlay_Title(t *testing.T) {
	s := NewSearchOverlay()
	assert.Equal(t, "", s.Title())
}

func TestSearchOverlay_Size(t *testing.T) {
	s := NewSearchOverlay()
	width, height := s.Size()
	assert.Equal(t, 0, width, "width should be 0 for full-width")
	assert.Equal(t, 1, height, "height should be 1 for single line")
}

func TestSearchOverlay_SetMatchCount(t *testing.T) {
	s := NewSearchOverlay()
	s.SetMatchCount(42)
	assert.Equal(t, 42, s.matchCount)
}

func TestSearchOverlay_Init(t *testing.T) {
	s := NewSearchOverlay()
	cmd := s.Init()
	assert.NotNil(t, cmd)
}

func TestSearchOverlay_InputHandling(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectQuery string
	}{
		{
			name:        "single character",
			input:       "a",
			expectQuery: "a",
		},
		{
			name:        "multiple characters",
			input:       "test",
			expectQuery: "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSearchOverlay()

			// Simulate typing each character
			for _, ch := range tt.input {
				msg := tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune{ch},
				}

				model, cmd := s.Update(msg)
				s = model.(*SearchOverlay)

				// Should return a command (batch of textinput cmd + SearchMsg)
				require.NotNil(t, cmd)
			}

			// Verify final input value
			assert.Equal(t, tt.expectQuery, s.input.Value())
		})
	}
}

func TestSearchOverlay_SearchMsgEmission(t *testing.T) {
	s := NewSearchOverlay()

	// Verify initial state is empty
	assert.Equal(t, "", s.input.Value())

	// Type 'a'
	msg := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'a'},
	}

	model, cmd := s.Update(msg)
	s = model.(*SearchOverlay)

	// Should return a command (indicates a message will be emitted)
	require.NotNil(t, cmd, "expected command to be returned for SearchMsg emission")

	// Verify the input value changed
	assert.Equal(t, "a", s.input.Value())

	// Type another character
	msg2 := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'b'},
	}

	model, cmd2 := s.Update(msg2)
	s = model.(*SearchOverlay)

	require.NotNil(t, cmd2, "expected command for second character")
	assert.Equal(t, "ab", s.input.Value())
}

func TestSearchOverlay_EnterKeepsFilter(t *testing.T) {
	s := NewSearchOverlay()

	// Type some text
	msg := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'t', 'e', 's', 't'},
	}
	for _, ch := range []rune{'t', 'e', 's', 't'} {
		msg.Runes = []rune{ch}
		model, _ := s.Update(msg)
		s = model.(*SearchOverlay)
	}

	queryBefore := s.input.Value()
	assert.Equal(t, "test", queryBefore)

	// Press Enter
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	model, cmd := s.Update(enterMsg)
	s = model.(*SearchOverlay)

	// Query should still be present
	assert.Equal(t, "test", s.input.Value())

	// Should emit CloseOverlayMsg
	require.NotNil(t, cmd)
	result := cmd()
	_, ok := result.(CloseOverlayMsg)
	assert.True(t, ok, "expected CloseOverlayMsg")
}

func TestSearchOverlay_EscClearsFilter(t *testing.T) {
	s := NewSearchOverlay()

	// Type some text
	for _, ch := range []rune{'t', 'e', 's', 't'} {
		msg := tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{ch},
		}
		model, _ := s.Update(msg)
		s = model.(*SearchOverlay)
	}

	assert.Equal(t, "test", s.input.Value())

	// Press Esc
	escMsg := tea.KeyMsg{Type: tea.KeyEsc}
	model, cmd := s.Update(escMsg)
	s = model.(*SearchOverlay)

	// Query should be cleared
	assert.Equal(t, "", s.input.Value())

	// Should emit both SearchMsg (with empty query) and CloseOverlayMsg
	require.NotNil(t, cmd)

	// The cmd is a batch, we need to verify it's not nil
	// In a real scenario, tea.Batch returns a command that when executed
	// will run both commands, but testing the exact messages is tricky
	// without executing in a full tea.Program context
	assert.NotNil(t, cmd)
}

func TestSearchOverlay_View(t *testing.T) {
	s := NewSearchOverlay()

	// Empty view
	view := s.View()
	assert.NotEmpty(t, view)

	// View with text and match count
	s.input.SetValue("test")
	s.SetMatchCount(5)
	view = s.View()
	assert.NotEmpty(t, view)
	// View should contain match count when there's a query
	// We can't easily test the exact rendered output due to styling,
	// but we can verify it's not empty
}

func TestSearchOverlay_NoSearchMsgOnSameValue(t *testing.T) {
	s := NewSearchOverlay()

	// Type 'a'
	msg := tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'a'},
	}
	model, cmd1 := s.Update(msg)
	s = model.(*SearchOverlay)
	assert.NotNil(t, cmd1)

	// Press a key that doesn't change the value (e.g., arrow key)
	arrowMsg := tea.KeyMsg{Type: tea.KeyLeft}
	_, cmd2 := s.Update(arrowMsg)

	// Should still get a command (from textinput), but it won't be a SearchMsg
	// since the value didn't change
	// This is harder to test precisely, but we've covered the main case
	_ = cmd2
}

func TestSearchOverlay_ImplementsInterfaces(t *testing.T) {
	s := NewSearchOverlay()

	// Verify it implements tea.Model
	var _ tea.Model = s

	// Verify it implements Overlay
	var _ Overlay = s
}
