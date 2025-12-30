package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPRCreateOverlay(t *testing.T) {
	overlay := NewPRCreateOverlay("feature/x", "main", "az-123")
	require.NotNil(t, overlay)
	assert.Equal(t, "feature/x", overlay.branch)
	assert.Equal(t, "main", overlay.baseBranch)
	assert.Equal(t, "az-123", overlay.beadID)
	assert.True(t, overlay.draft) // Default to draft
	assert.Equal(t, prFocusTitle, overlay.focusIndex)
}

func TestPRCreateOverlayTitle(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")
	assert.Equal(t, "Create Pull Request", overlay.Title())
}

func TestPRCreateOverlaySize(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")
	width, height := overlay.Size()
	assert.Equal(t, 80, width)
	assert.Equal(t, 28, height)
}

func TestPRCreateOverlayView(t *testing.T) {
	overlay := NewPRCreateOverlay("feature/auth", "main", "az-42")
	view := overlay.View()

	// Check that form elements are present
	assert.Contains(t, view, "Title:")
	assert.Contains(t, view, "Description:")
	assert.Contains(t, view, "Draft:")
	assert.Contains(t, view, "Create Pull Request")
	assert.Contains(t, view, "feature/auth → main")
	assert.Contains(t, view, "az-42")
}

func TestPRCreateOverlayEscapeCloses(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	closeMsg, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
	assert.NotNil(t, closeMsg)
}

func TestPRCreateOverlayTabNavigation(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	// Start at title
	assert.Equal(t, prFocusTitle, overlay.focusIndex)

	// Tab to body
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusBody, overlay.focusIndex)

	// Tab to draft
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusDraft, overlay.focusIndex)

	// Tab to submit
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusSubmit, overlay.focusIndex)

	// Tab back to title
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusTitle, overlay.focusIndex)
}

func TestPRCreateOverlayShiftTabNavigation(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	// Start at title (0)
	assert.Equal(t, prFocusTitle, overlay.focusIndex)

	// Shift+Tab should go to submit (3)
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusSubmit, overlay.focusIndex)

	// Shift+Tab should go to draft (2)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusDraft, overlay.focusIndex)
}

func TestPRCreateOverlayDraftToggle(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	// Default is draft
	assert.True(t, overlay.draft)

	// Navigate to draft field
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab}) // to body
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})        // to draft
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusDraft, overlay.focusIndex)

	// Toggle with 'd' key
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	overlay = m.(*PRCreateOverlay)
	assert.False(t, overlay.draft)

	// Toggle again
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	overlay = m.(*PRCreateOverlay)
	assert.True(t, overlay.draft)
}

func TestPRCreateOverlayDraftToggleOnlyWhenFocused(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	// Start at title (not draft)
	assert.Equal(t, prFocusTitle, overlay.focusIndex)
	initialDraft := overlay.draft

	// Pressing 'd' should not toggle when not focused on draft
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, initialDraft, overlay.draft)
}

func TestPRCreateOverlaySubmitWithCtrlS(t *testing.T) {
	overlay := NewPRCreateOverlay("feature/test", "main", "az-99")

	// Set a title
	overlay.title.SetValue("Test PR")

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	// Should return batch of messages
	msg := cmd()

	// First message should be PRCreatedMsg
	prMsg, ok := msg.(PRCreatedMsg)
	assert.True(t, ok)
	assert.Equal(t, "Test PR", prMsg.Title)
	assert.Equal(t, "feature/test", prMsg.Branch)
	assert.Equal(t, "main", prMsg.BaseBranch)
	assert.Equal(t, "az-99", prMsg.BeadID)
	assert.True(t, prMsg.Draft)
}

func TestPRCreateOverlaySubmitWithEnter(t *testing.T) {
	overlay := NewPRCreateOverlay("fix/bug", "main", "az-50")

	// Set a title
	overlay.title.SetValue("Fix critical bug")

	// Navigate to submit button
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab}) // to body
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})        // to draft
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})        // to submit
	overlay = m.(*PRCreateOverlay)
	assert.Equal(t, prFocusSubmit, overlay.focusIndex)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msg := cmd()
	prMsg, ok := msg.(PRCreatedMsg)
	assert.True(t, ok)
	assert.Equal(t, "Fix critical bug", prMsg.Title)
}

func TestPRCreateOverlaySubmitEmptyTitleIgnored(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	// Don't set a title (leave it empty)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})

	// Should return nil when title is empty
	assert.Nil(t, cmd)
}

func TestPRCreateOverlaySubmitWithBody(t *testing.T) {
	overlay := NewPRCreateOverlay("feature/y", "develop", "az-77")

	// Set title and body
	overlay.title.SetValue("Add feature Y")
	overlay.body.SetValue("This PR implements feature Y\n\nCloses #123")

	// Toggle draft off
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab}) // to body
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})        // to draft
	overlay = m.(*PRCreateOverlay)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	overlay = m.(*PRCreateOverlay)
	assert.False(t, overlay.draft)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msg := cmd()
	prMsg, ok := msg.(PRCreatedMsg)
	assert.True(t, ok)
	assert.Equal(t, "Add feature Y", prMsg.Title)
	assert.Contains(t, prMsg.Body, "implements feature Y")
	assert.Contains(t, prMsg.Body, "Closes #123")
	assert.Equal(t, "feature/y", prMsg.Branch)
	assert.Equal(t, "develop", prMsg.BaseBranch)
	assert.False(t, prMsg.Draft)
}

func TestPRCreateOverlayRenderDraftToggle(t *testing.T) {
	overlay := NewPRCreateOverlay("test", "main", "az-1")

	// Draft is true by default
	assert.True(t, overlay.draft)
	view := overlay.View()
	assert.Contains(t, view, "Draft PR")

	// Toggle draft off
	overlay.draft = false
	view = overlay.View()
	assert.Contains(t, view, "Ready for review")
}
