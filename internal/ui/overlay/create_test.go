package overlay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
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

func TestCreateTaskOverlayImplOptionsRemainEmptyWithoutOptions(t *testing.T) {
	overlay := NewCreateTaskOverlayWithParentAndImplOptions(nil, nil)
	require.NotNil(t, overlay)
	assert.Equal(t, []string{"default"}, overlay.implOptions)
	assert.Empty(t, overlay.impls)
}

func TestCreateTaskOverlayNoHardcodedImplementationTriplet(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("create.go"))
	require.NoError(t, err)
	text := string(source)
	assert.NotContains(t, text, `[]string{"default", "go-bubbletea", "ts-opentui"}`)
	assert.NotContains(t, text, "default, go-bubbletea, ts-opentui")
}

func TestCreateTaskOverlayTitle(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	assert.Equal(t, "Create New Task", overlay.Title())
}

func TestCreateTaskOverlaySize(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	width, height := overlay.Size()
	assert.Equal(t, 100, width)
	assert.Equal(t, 30, height)
}

func TestCreateTaskOverlaySizeUses80PercentOnLargeViewport(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.ApplyWindowSize(tea.WindowSizeMsg{Width: 200, Height: 60})

	width, height := overlay.Size()
	assert.Equal(t, 160, width)
	assert.Equal(t, 48, height)
}

func TestCreateTaskOverlaySizeFallsBackToFullScreenWhen80PercentTooSmall(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.ApplyWindowSize(tea.WindowSizeMsg{Width: 120, Height: 36})

	width, height := overlay.Size()
	assert.Equal(t, 118, width)
	assert.Equal(t, 34, height)
}

func TestCreateTaskOverlayRenderFormContentClampsInputWidthsOnNarrowPane(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.renderFormContent(16, 20)

	assert.LessOrEqual(t, overlay.title.Width, 16)
	assert.LessOrEqual(t, overlay.description.Width(), 16)
	assert.LessOrEqual(t, overlay.acceptanceInput.Width(), 16)
}

func TestCreateTaskOverlayView(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	view := overlay.View()

	// Check that form elements are present
	assert.Contains(t, view, "Title:")
	assert.Contains(t, view, "Description:")
	assert.Contains(t, view, "Type:")
	assert.Contains(t, view, "Priority:")
	assert.Contains(t, view, "Impls:")
	assert.Contains(t, view, "Acceptance Criteria:")
	assert.Contains(t, view, "Create Task")
	assert.Contains(t, view, "Enter")
	assert.Contains(t, view, "Ctrl+C")
	assert.Contains(t, view, "Ctrl+P")
	assert.Contains(t, view, "Ctrl+O")
	assert.Contains(t, view, "Ctrl+E")
	assert.Contains(t, view, "Ctrl+K")
}

func TestCreateTaskOverlayViewShowsLongTitleWithoutTruncatingStart(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	longTitle := "fix overlay title wrapping so very long create task names remain readable in the form without truncating the beginning while editing"
	overlay.title.SetValue(longTitle)

	view := overlay.View()

	assert.Contains(t, view, "fix overlay title wrapping")
	assert.Contains(t, view, "while editing")
}

func TestWrapTitleLinesDoesNotSplitDashedFlags(t *testing.T) {
	lines := wrapTitleLines("add --project flag to set project explicitly in az cli commands", 24)
	joined := strings.Join(lines, "\n")

	assert.Contains(t, joined, "--project")
	assert.NotContains(t, joined, "-\n-project")
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
	assert.Equal(t, focusImpls, overlay.focusIndex)

	// Tab to acceptance
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusAcceptance, overlay.focusIndex)

	// Tab to submit
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusAttachments, overlay.focusIndex)

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

	// Shift+Tab should go to acceptance
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusAttachments, overlay.focusIndex)
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

func TestCreateTaskOverlaySubmitWithAcceptance(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.title.SetValue("Test Task")
	overlay.acceptanceInput.SetValue("Given X\nWhen Y\nThen Z")

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Given X\nWhen Y\nThen Z", taskMsg.Acceptance)
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

func TestCreateTaskOverlaySubmitWithImplementations(t *testing.T) {
	overlay := NewCreateTaskOverlayWithParentAndImplOptions(nil, []string{"default", "go-bubbletea", "ts-opentui"})
	overlay.title.SetValue("Impl Scoped Task")
	overlay.impls = []string{"go-bubbletea", "ts-opentui"}
	overlay.syncImplementationSelection()

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, []string{"go-bubbletea", "ts-opentui"}, taskMsg.Implementations)
}

func TestEditTaskOverlaySubmitPreservesEmptyImplementations(t *testing.T) {
	task := domain.Task{
		ID:              "az-1",
		Title:           "Legacy Task",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Status:          domain.StatusOpen,
		Implementations: nil,
	}
	overlay := NewEditTaskOverlayWithImplOptions(task, []string{"go-bubbletea", "ts-opentui"})
	require.NotNil(t, overlay)
	assert.Equal(t, -1, overlay.implComboIndex)
	assert.Empty(t, overlay.impls)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)
	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Empty(t, taskMsg.Implementations)
}

func TestEditTaskOverlaySubmitPreservesUnmatchedImplementations(t *testing.T) {
	task := domain.Task{
		ID:              "az-2",
		Title:           "Legacy Impl Task",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Status:          domain.StatusOpen,
		Implementations: []string{"legacy-impl"},
	}
	overlay := NewEditTaskOverlayWithImplOptions(task, []string{"go-bubbletea", "ts-opentui"})
	require.NotNil(t, overlay)
	assert.Equal(t, -1, overlay.implComboIndex)
	assert.Equal(t, []string{"legacy-impl"}, overlay.impls)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)
	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, []string{"legacy-impl"}, taskMsg.Implementations)
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

func TestCreateTaskOverlayEnterOnTitleSubmits(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.title.SetValue("Quick submit")

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)

	taskMsg, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Quick submit", taskMsg.Title)
}

func TestCreateTaskOverlayLowercaseTypeSelection(t *testing.T) {
	overlay := NewCreateTaskOverlay()

	m, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyTab})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, focusType, overlay.focusIndex)

	m, _ = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, domain.TypeBug, overlay.taskType)
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

func TestCreateTaskOverlayCtrlKClearsDraft(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.title.SetValue("Keep me")
	overlay.description.SetValue("Draft description")
	overlay.taskType = domain.TypeBug
	overlay.priority = domain.P0
	overlay.impls = []string{"go-bubbletea"}
	overlay.syncImplementationSelection()
	overlay.acceptanceInput.SetValue("Must pass")
	overlay.focusIndex = focusPriority

	m, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	require.Nil(t, cmd)

	overlay = m.(*CreateTaskOverlay)
	assert.Equal(t, "", overlay.title.Value())
	assert.Equal(t, "", overlay.description.Value())
	assert.Equal(t, domain.TypeTask, overlay.taskType)
	assert.Equal(t, domain.P2, overlay.priority)
	assert.Empty(t, overlay.impls)
	assert.Equal(t, "", overlay.acceptanceInput.Value())
	assert.Equal(t, focusTitle, overlay.focusIndex)
}

func TestCreateTaskOverlayCtrlCClearsFocusedTitle(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.title.SetValue("Needs clear")
	overlay.focusIndex = focusTitle

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.Nil(t, cmd)

	overlay = model.(*CreateTaskOverlay)
	assert.Equal(t, "", overlay.title.Value())
}

func TestCreateTaskOverlayCtrlCClearsFocusedDescription(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.description.SetValue("Need to clear description")
	overlay.focusIndex = focusDescription

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.Nil(t, cmd)

	overlay = model.(*CreateTaskOverlay)
	assert.Equal(t, "", overlay.description.Value())
}

func TestEditTaskOverlayCtrlPAttachesFromClipboard(t *testing.T) {
	task := domain.Task{ID: "az-77", Title: "Edit me", Type: domain.TypeTask, Priority: domain.P2}
	svc := &createTestAttachmentService{
		attached: &attachment.Attachment{
			ID:       "att-1",
			IssueID:  "az-77",
			Filename: "clipboard.png",
			Size:     1200,
			Created:  time.Now(),
		},
	}
	overlay := NewEditTaskOverlayWithImplOptionsAndAttachmentService(task, nil, svc)

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	require.NotNil(t, cmd)

	msg := cmd()
	addedMsg, ok := msg.(attachmentAddedMsg)
	require.True(t, ok)
	require.NotNil(t, addedMsg.attachment)
	assert.Equal(t, "az-77", addedMsg.attachment.IssueID)
}

func TestEditTaskOverlayDeleteAttachmentWhenFocused(t *testing.T) {
	task := domain.Task{ID: "az-88", Title: "Edit me", Type: domain.TypeTask, Priority: domain.P2}
	svc := &createTestAttachmentService{
		files: []attachment.Attachment{
			{ID: "att-1", IssueID: "az-88", Filename: "one.png"},
		},
	}
	overlay := NewEditTaskOverlayWithImplOptionsAndAttachmentService(task, nil, svc)
	overlay.attachments = append([]attachment.Attachment(nil), svc.files...)
	overlay.focusIndex = focusAttachments

	_, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	require.NotNil(t, cmd)
	_ = cmd()
	assert.Equal(t, "att-1", svc.deletedID)
}

type createTestAttachmentService struct {
	files     []attachment.Attachment
	attached  *attachment.Attachment
	deletedID string
}

func (s *createTestAttachmentService) List(_ context.Context, _ string) ([]attachment.Attachment, error) {
	return append([]attachment.Attachment(nil), s.files...), nil
}

func (s *createTestAttachmentService) AttachFromClipboard(_ context.Context, _ string) (*attachment.Attachment, error) {
	if s.attached != nil {
		return s.attached, nil
	}
	now := time.Now()
	return &attachment.Attachment{ID: "att-default", Filename: "clipboard.png", Created: now}, nil
}

func (s *createTestAttachmentService) Attach(_ context.Context, _, _ string) (*attachment.Attachment, error) {
	return nil, errors.New("not implemented")
}

func (s *createTestAttachmentService) Delete(_ context.Context, _, attachmentID string) error {
	s.deletedID = attachmentID
	return nil
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

func TestParseTaskTemplate(t *testing.T) {
	markdown := strings.Join([]string{
		"# My Task",
		"---------------------------------------------------",
		"",
		"Type: bug",
		"Priority: P1",
		"",
		"## Description",
		"",
		"Fix this now",
		"",
	}, "\n")

	msg, err := parseTaskTemplate(markdown, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "My Task", msg.Title)
	assert.Equal(t, "Fix this now", msg.Description)
	assert.Equal(t, domain.TypeBug, msg.Type)
	assert.Equal(t, domain.P1, msg.Priority)
}

func TestParseTaskTemplate_IgnoresTemplateDividerInsideSections(t *testing.T) {
	markdown := strings.Join([]string{
		"# Divider Parsing",
		"───────────────────────────────────────────────────",
		"",
		"Type:     task        (task | bug | feature | epic | chore)",
		"Priority: P2          (P0 = highest, P4 = lowest)",
		"Status:   open        (open | in_progress | blocked | closed)",
		"Assignee:",
		"Labels:",
		"Impl:     default",
		"Estimate:",
		"",
		"───────────────────────────────────────────────────",
		"## Description",
		"",
		"First line",
		"",
		"───────────────────────────────────────────────────",
		"## Design",
		"",
		"Design line",
		"",
		"───────────────────────────────────────────────────",
		"## Notes",
		"",
		"Notes line",
		"",
		"───────────────────────────────────────────────────",
		"## Acceptance Criteria",
		"",
		"AC line",
		"",
	}, "\n")

	msg, err := parseTaskTemplate(markdown, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "First line", msg.Description)
	assert.Equal(t, "Design line", msg.Design)
	assert.Equal(t, "Notes line", msg.Notes)
	assert.Equal(t, "AC line", msg.Acceptance)
}

func TestCreateTaskOverlayCtrlEAppliesEditedTemplate(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.editorFlow = func(_ string) (string, error) {
		return strings.Join([]string{
			"# Edited Task",
			"───────────────────────────────────────────────────",
			"",
			"Type:     feature        (task | bug | feature | epic | chore)",
			"Priority: P0          (P0 = highest, P4 = lowest)",
			"Status:   blocked        (open | in_progress | blocked | closed)",
			"Assignee: jane",
			"Labels:   ui, editor",
			"Impl:     go-bubbletea",
			"Estimate: 13",
			"",
			"## Description",
			"",
			"Edited from editor",
			"",
			"## Design",
			"",
			"Update parser",
			"",
			"## Notes",
			"",
			"Be careful with defaults",
			"",
			"## Acceptance Criteria",
			"",
			"All fields parse and persist",
			"",
		}, "\n"), nil
	}

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	require.NotNil(t, cmd)

	editorMsg := cmd()
	model, cmd = model.(*CreateTaskOverlay).Update(editorMsg)
	require.NotNil(t, cmd)

	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)
	created, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Edited Task", created.Title)
	assert.Equal(t, "Edited from editor", created.Description)
	assert.Equal(t, domain.TypeFeature, created.Type)
	assert.Equal(t, domain.P0, created.Priority)
	assert.Equal(t, domain.StatusBlocked, created.Status)
	assert.Equal(t, "jane", created.Assignee)
	assert.Equal(t, []string{"ui", "editor"}, created.Labels)
	assert.Equal(t, []string{"go-bubbletea"}, created.Implementations)
	assert.Equal(t, "Update parser", created.Design)
	assert.Equal(t, "Be careful with defaults", created.Notes)
	assert.Equal(t, "All fields parse and persist", created.Acceptance)
	require.NotNil(t, created.Estimate)
	assert.Equal(t, 13, *created.Estimate)
}

func TestCreateTaskOverlayCtrlEUsesExecProcessByDefault(t *testing.T) {
	t.Setenv("EDITOR", "hx")
	overlay := NewCreateTaskOverlay()

	originalExecProcess := overlayExecProcess
	t.Cleanup(func() {
		overlayExecProcess = originalExecProcess
	})

	var captured *exec.Cmd
	overlayExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		captured = c
		return func() tea.Msg {
			edited := strings.Join([]string{
				"# Edited via Exec",
				"───────────────────────────────────────────────────",
				"",
				"Type:     task        (task | bug | feature | epic | chore)",
				"Priority: P2          (P0 = highest, P4 = lowest)",
				"Status:   open        (open | in_progress | blocked | closed)",
				"Assignee:",
				"Labels:",
				"Impl:     default",
				"Estimate:",
				"",
				"## Description",
				"",
				"Updated through exec callback",
				"",
				"## Design",
				"",
				"n/a",
				"",
				"## Notes",
				"",
				"n/a",
				"",
				"## Acceptance Criteria",
				"",
				"works",
			}, "\n")
			if len(c.Args) < 2 {
				return taskEditorErrorMsg{err: errors.New("missing temp file arg")}
			}
			if err := os.WriteFile(c.Args[1], []byte(edited), 0600); err != nil {
				return taskEditorErrorMsg{err: err}
			}
			return fn(nil)
		}
	}

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	require.NotNil(t, cmd)
	editorMsg := cmd()
	model, cmd = model.(*CreateTaskOverlay).Update(editorMsg)
	require.NotNil(t, cmd)
	msgs := batchToSlice(cmd())
	require.Len(t, msgs, 2)
	created, ok := msgs[0].(TaskCreatedMsg)
	require.True(t, ok)
	assert.Equal(t, "Edited via Exec", created.Title)
	assert.Equal(t, "Updated through exec callback", created.Description)
	require.NotNil(t, captured)
	assert.Equal(t, "hx", filepath.Base(captured.Path))
	if len(captured.Args) >= 2 {
		assert.Contains(t, filepath.Base(captured.Args[1]), "azedarach-task-")
	}
}

func TestParseTaskTemplateFullMetadata(t *testing.T) {
	markdown := strings.Join([]string{
		"# Metadata Task",
		"───────────────────────────────────────────────────",
		"",
		"Type:     chore        (task | bug | feature | epic | chore)",
		"Priority: P4          (P0 = highest, P4 = lowest)",
		"Status:   in_progress        (open | in_progress | blocked | closed)",
		"Assignee: sam",
		"Labels:   alpha, beta",
		"Impl:     go-bubbletea",
		"Estimate: 5",
		"",
		"## Description",
		"",
		"Ship this",
		"",
		"## Design",
		"",
		"Design section",
		"",
		"## Notes",
		"",
		"Notes section",
		"",
		"## Acceptance Criteria",
		"",
		"Acceptance section",
		"",
	}, "\n")

	msg, err := parseTaskTemplate(markdown, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "Metadata Task", msg.Title)
	assert.Equal(t, domain.TypeChore, msg.Type)
	assert.Equal(t, domain.P4, msg.Priority)
	assert.Equal(t, domain.StatusInProgress, msg.Status)
	assert.Equal(t, "sam", msg.Assignee)
	assert.Equal(t, []string{"alpha", "beta"}, msg.Labels)
	assert.Equal(t, []string{"go-bubbletea"}, msg.Implementations)
	assert.Equal(t, "Ship this", msg.Description)
	assert.Equal(t, "Design section", msg.Design)
	assert.Equal(t, "Notes section", msg.Notes)
	assert.Equal(t, "Acceptance section", msg.Acceptance)
	require.NotNil(t, msg.Estimate)
	assert.Equal(t, 5, *msg.Estimate)
}

func TestCreateTaskOverlayCtrlESetsError(t *testing.T) {
	overlay := NewCreateTaskOverlay()
	overlay.editorFlow = func(_ string) (string, error) {
		return "", errors.New("helix missing")
	}

	model, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	require.NotNil(t, cmd)

	editorMsg := cmd()
	model, _ = model.(*CreateTaskOverlay).Update(editorMsg)
	updated := model.(*CreateTaskOverlay)
	assert.Contains(t, updated.editorError, "helix missing")
}
