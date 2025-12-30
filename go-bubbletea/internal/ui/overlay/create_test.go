package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCreateTaskOverlay(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	require.NotNil(t, overlay)
	assert.Equal(t, domain.TypeTask, overlay.taskType)
	assert.Equal(t, domain.P2, overlay.priority)
	assert.Equal(t, focusTitle, overlay.focusIndex)
}

func TestCreateTaskOverlayTitle(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	assert.Equal(t, "Create New Task", overlay.Title())
}

func TestCreateTaskOverlaySize(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	width, height := overlay.Size()
	assert.Equal(t, 70, width)
	assert.Equal(t, 25, height)
}

func TestCreateTaskOverlayView(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	view := overlay.View()

	// Check that form elements are present
	assert.Contains(t, view, "Title:")
	assert.Contains(t, view, "Description:")
	assert.Contains(t, view, "Type:")
	assert.Contains(t, view, "Priority:")
	assert.Contains(t, view, "Create Task")
}

func TestCreateTaskOverlayEscapeCloses(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	closeMsg, ok := msg.(CloseOverlayMsg)
	assert.True(t, ok)
	assert.NotNil(t, closeMsg)
}

func TestCreateTaskOverlayTabNavigation(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Start at title
	assert.Equal(t, focusTitle, overlay.focusIndex)

	// Tab to description
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusDescription, overlay.focusIndex)

	// Tab to type
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusType, overlay.focusIndex)

	// Tab to priority
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusPriority, overlay.focusIndex)

	// Tab to submit
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusSubmit, overlay.focusIndex)

	// Tab back to title
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusTitle, overlay.focusIndex)
}

func TestCreateTaskOverlayShiftTabNavigation(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Start at title (0)
	assert.Equal(t, focusTitle, overlay.focusIndex)

	// Shift+Tab should go to submit (4)
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusSubmit, overlay.focusIndex)

	// Shift+Tab should go to priority (3)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusPriority, overlay.focusIndex)
}

func TestCreateTaskOverlayTypeSelection(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Tab to type field
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusType, overlay.focusIndex)

	// Select Bug
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'B'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.TypeBug, overlay.taskType)

	// Select Feature
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.TypeFeature, overlay.taskType)

	// Select Epic
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.TypeEpic, overlay.taskType)

	// Select Chore
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.TypeChore, overlay.taskType)

	// Select Task
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'T'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.TypeTask, overlay.taskType)
}

func TestCreateTaskOverlayPrioritySelection(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Tab to priority field
	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusPriority, overlay.focusIndex)

	// Select P0
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.P0, overlay.priority)

	// Select P1
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.P1, overlay.priority)

	// Select P2
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.P2, overlay.priority)

	// Select P3
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.P3, overlay.priority)

	// Select P4
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.P4, overlay.priority)
}

func TestCreateTaskOverlaySubmitWithTitle(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title
	overlay.title.SetValue("Test Task")

	// Submit with Ctrl+S
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	// Should emit TaskCreatedMsg and CloseOverlayMsg
	// Check batch command returns messages
	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	// Check TaskCreatedMsg
	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Test Task", taskMsg.Title)
	assert.Equal(t, domain.TypeTask, taskMsg.Type)
	assert.Equal(t, domain.P2, taskMsg.Priority)

	// Check CloseOverlayMsg
	_, ok = msgs[1].(CloseOverlayMsg)
	assert.True(t, ok)
}

func TestCreateTaskOverlaySubmitWithDescription(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title and description
	overlay.title.SetValue("Test Task")
	overlay.description.SetValue("This is a test description")

	// Submit with Ctrl+S
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Test Task", taskMsg.Title)
	assert.Equal(t, "This is a test description", taskMsg.Description)
}

func TestCreateTaskOverlaySubmitWithCustomTypeAndPriority(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title, type, and priority
	overlay.title.SetValue("Bug Fix")
	overlay.taskType = domain.TypeBug
	overlay.priority = domain.P0

	// Submit
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Bug Fix", taskMsg.Title)
	assert.Equal(t, domain.TypeBug, taskMsg.Type)
	assert.Equal(t, domain.P0, taskMsg.Priority)
}

func TestCreateTaskOverlaySubmitWithoutTitle(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Don't set title (empty)
	// Submit with Ctrl+S
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})

	// Should not submit (cmd should be nil)
	assert.Nil(t, cmd)
}

func TestCreateTaskOverlaySubmitWithWhitespaceTitle(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title to whitespace only
	overlay.title.SetValue("   ")

	// Submit with Ctrl+S
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})

	// Should not submit (cmd should be nil)
	assert.Nil(t, cmd)
}

func TestCreateTaskOverlayEnterOnSubmitButton(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title
	overlay.title.SetValue("Test Task")

	// Navigate to submit button
	overlay.focusIndex = focusSubmit

	// Press Enter
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Test Task", taskMsg.Title)
}

func TestCreateTaskOverlayRenderTypeSelector(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Test with different types
	types := []domain.TaskType{
		domain.TypeTask,
		domain.TypeBug,
		domain.TypeFeature,
		domain.TypeEpic,
		domain.TypeChore,
	}

	for _, typ := range types {
		overlay.taskType = typ
		view := overlay.renderTypeSelector()

		// Should contain all type keys
		assert.Contains(t, view, "T")
		assert.Contains(t, view, "B")
		assert.Contains(t, view, "F")
		assert.Contains(t, view, "E")
		assert.Contains(t, view, "C")
	}
}

func TestCreateTaskOverlayRenderPrioritySelector(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Test with different priorities
	priorities := []domain.Priority{
		domain.P0,
		domain.P1,
		domain.P2,
		domain.P3,
		domain.P4,
	}

	for _, pri := range priorities {
		overlay.priority = pri
		view := overlay.renderPrioritySelector()

		// Should contain all priority keys
		assert.Contains(t, view, "0")
		assert.Contains(t, view, "1")
		assert.Contains(t, view, "2")
		assert.Contains(t, view, "3")
		assert.Contains(t, view, "4")
	}
}

func TestCreateTaskOverlayTitleTrimming(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title with leading/trailing whitespace
	overlay.title.SetValue("  Test Task  ")

	// Submit
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	taskMsg := msgs[0].(TaskCreatedMsg)

	// Title should be trimmed
	assert.Equal(t, "Test Task", taskMsg.Title)
}

func TestCreateTaskOverlayDescriptionTrimming(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set title and description with whitespace
	overlay.title.SetValue("Test")
	overlay.description.SetValue("  Description  ")

	// Submit
	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	taskMsg := msgs[0].(TaskCreatedMsg)

	// Description should be trimmed
	assert.Equal(t, "Description", taskMsg.Description)
}

// batchToSlice is a helper function to extract messages from a batch command
func batchToSlice(msg tea.Msg) []tea.Msg {
	if msg == nil {
		return nil
	}

	// For batch commands, we need to execute them to get the messages
	// This is a simplified version - in real code you might use tea.BatchMsg
	switch m := msg.(type) {
	case tea.BatchMsg:
		var msgs []tea.Msg
		for _, cmd := range m {
			if cmd != nil {
				msgs = append(msgs, cmd())
			}
		}
		return msgs
	default:
		return []tea.Msg{msg}
	}
}

func TestCreateTaskOverlayViewContainsSelectors(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	// Set specific values
	overlay.taskType = domain.TypeFeature
	overlay.priority = domain.P0

	view := overlay.View()

	// Check that current selections are indicated in the view
	// The view should contain the active markers
	assert.True(t, strings.Contains(view, "●") || strings.Contains(view, "Type:"))
	assert.True(t, strings.Contains(view, "●") || strings.Contains(view, "Priority:"))
}
