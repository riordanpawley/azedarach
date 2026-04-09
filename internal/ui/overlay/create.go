package overlay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

const (
	taskTemplateAnchorTitle       = "TITLE"
	taskTemplateAnchorDescription = "DESCRIPTION"
	taskTemplateAnchorDesign      = "DESIGN"
	taskTemplateAnchorNotes       = "NOTES"
	taskTemplateAnchorAcceptance  = "ACCEPTANCE"
	taskTemplateDivider           = "───────────────────────────────────────────────────"
	createTaskOverlayWidth        = 100
	createTaskOverlayHeight       = 30
)

var overlayExecProcess = tea.ExecProcess

// TaskCreatedMsg is emitted when a new task is created
type TaskCreatedMsg struct {
	ID              string
	Title           string
	Description     string
	Type            domain.TaskType
	Priority        domain.Priority
	Status          domain.Status
	Assignee        string
	Labels          []string
	Implementations []string
	Design          string
	Notes           string
	Acceptance      string
	Estimate        *int
	ParentID        *string
	AttachmentPaths []string
}

// CreateTaskOverlay provides a form to create a new task
type CreateTaskOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	id              string
	title           textinput.Model
	description     textarea.Model
	acceptanceInput textarea.Model
	implOptions     []string
	implCombos      [][]string
	implComboIndex  int
	taskType        domain.TaskType
	priority        domain.Priority
	status          domain.Status
	assignee        string
	labels          []string
	impls           []string
	design          string
	notes           string
	acceptance      string
	estimate        *int
	parentID        *string
	focusIndex      int
	styles          *Styles
	editorError     string
	editorFlow      func(string) (string, error)
	defaults        createTaskDefaults
	attachmentSvc   ImageAttachmentService
	attachments     []attachment.Attachment
	attachmentIndex int
	attachmentError string
	draftIssueID    string
}

type createTaskDefaults struct {
	title       string
	description string
	taskType    domain.TaskType
	priority    domain.Priority
	status      domain.Status
	assignee    string
	labels      []string
	impls       []string
	design      string
	notes       string
	acceptance  string
	estimate    *int
}

const (
	focusTitle = iota
	focusDescription
	focusType
	focusPriority
	focusImpls
	focusAcceptance
	focusAttachments
	focusSubmit
	focusCount
)

// OpenTaskImageAttachMsg requests opening image-attachment overlay for a task.
type OpenTaskImageAttachMsg struct {
	IssueID string
}

// NewCreateTaskOverlay creates a new task creation overlay
func NewCreateTaskOverlay() *CreateTaskOverlay {
	return NewCreateTaskOverlayWithParentAndImplOptions(nil, nil)
}

func NewEditTaskOverlay(task domain.Task) *CreateTaskOverlay {
	return NewEditTaskOverlayWithImplOptionsAndAttachmentService(task, nil, nil)
}

func NewEditTaskOverlayWithImplOptions(task domain.Task, implOptions []string) *CreateTaskOverlay {
	return NewEditTaskOverlayWithImplOptionsAndAttachmentService(task, implOptions, nil)
}

func NewEditTaskOverlayWithImplOptionsAndAttachmentService(task domain.Task, implOptions []string, attachmentSvc ImageAttachmentService) *CreateTaskOverlay {
	ti := textinput.New()
	ti.SetValue(task.Title)
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 56

	ta := textarea.New()
	ta.SetValue(task.Description)
	ta.CharLimit = 2000
	ta.SetWidth(56)
	ta.SetHeight(4)

	acceptance := textarea.New()
	acceptance.SetValue("")
	acceptance.CharLimit = 4000
	acceptance.SetWidth(56)
	acceptance.SetHeight(3)

	overlay := &CreateTaskOverlay{
		id:              task.ID.String(),
		title:           ti,
		description:     ta,
		acceptanceInput: acceptance,
		implOptions:     append([]string(nil), implOptions...),
		taskType:        task.Type,
		priority:        task.Priority,
		status:          task.Status,
		impls:           task.Implementations,
		parentID:        issueIDPtrToStringPtr(task.ParentID),
		focusIndex:      focusTitle,
		styles:          New(),
		defaults: createTaskDefaults{
			title:       task.Title,
			description: task.Description,
			taskType:    task.Type,
			priority:    task.Priority,
			status:      task.Status,
			impls:       append([]string(nil), task.Implementations...),
		},
		attachmentSvc: attachmentSvc,
	}
	overlay.syncImplementationSelection()
	return overlay
}

func issueIDPtrToStringPtr(id *naming.IssueID) *string {
	if id == nil {
		return nil
	}
	value := id.String()
	return &value
}

func NewCreateTaskOverlayWithParent(parentID *string) *CreateTaskOverlay {
	return NewCreateTaskOverlayWithParentAndImplOptions(parentID, nil)
}

func NewCreateTaskOverlayWithParentAndImplOptions(parentID *string, implOptions []string) *CreateTaskOverlay {
	return NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(parentID, implOptions, nil)
}

func NewCreateTaskOverlayWithParentImplOptionsAndAttachmentService(parentID *string, implOptions []string, attachmentSvc ImageAttachmentService) *CreateTaskOverlay {
	// Initialize title input
	ti := textinput.New()
	ti.Placeholder = "Task title..."
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 56

	// Initialize description textarea
	ta := textarea.New()
	ta.Placeholder = "Task description (optional)..."
	ta.CharLimit = 2000
	ta.SetWidth(56)
	ta.SetHeight(4)

	acceptance := textarea.New()
	acceptance.Placeholder = "Acceptance criteria (optional)..."
	acceptance.CharLimit = 4000
	acceptance.SetWidth(56)
	acceptance.SetHeight(3)

	overlay := &CreateTaskOverlay{
		title:           ti,
		description:     ta,
		acceptanceInput: acceptance,
		implOptions:     append([]string(nil), implOptions...),
		taskType:        domain.TypeTask,
		priority:        domain.P2,
		status:          domain.StatusOpen,
		parentID:        parentID,
		focusIndex:      focusTitle,
		styles:          New(),
		defaults: createTaskDefaults{
			taskType: domain.TypeTask,
			priority: domain.P2,
			status:   domain.StatusOpen,
		},
		attachmentSvc: attachmentSvc,
	}
	overlay.syncImplementationSelection()
	overlay.defaults.impls = append([]string(nil), overlay.impls...)
	return overlay
}

func normalizeImplementationOptions(options []string) []string {
	seen := make(map[string]struct{}, len(options)+1)
	normalized := make([]string, 0, len(options)+1)
	for _, option := range options {
		value := strings.TrimSpace(option)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		normalized = append(normalized, "default")
	}
	sort.Strings(normalized)
	return normalized
}

func generateImplementationCombos(options []string) [][]string {
	normalized := normalizeImplementationOptions(options)
	total := 1 << len(normalized)
	combos := make([][]string, 0, total-1)
	for mask := 1; mask < total; mask++ {
		combo := make([]string, 0, len(normalized))
		for idx, option := range normalized {
			if mask&(1<<idx) != 0 {
				combo = append(combo, option)
			}
		}
		combos = append(combos, combo)
	}
	sort.SliceStable(combos, func(i, j int) bool {
		if len(combos[i]) != len(combos[j]) {
			return len(combos[i]) < len(combos[j])
		}
		return strings.Join(combos[i], ",") < strings.Join(combos[j], ",")
	})
	return combos
}

func normalizeImplementationSet(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	return normalized
}

func implementationSetKey(values []string) string {
	normalized := normalizeImplementationSet(values)
	return strings.Join(normalized, "\x00")
}

func (c *CreateTaskOverlay) syncImplementationSelection() {
	c.implOptions = normalizeImplementationOptions(c.implOptions)
	c.implCombos = generateImplementationCombos(c.implOptions)
	targetSet := normalizeImplementationSet(c.impls)
	if len(targetSet) == 0 {
		defaultSet := normalizeImplementationSet(c.defaults.impls)
		if len(defaultSet) == 0 {
			c.implComboIndex = -1
			c.impls = nil
			return
		}
		targetSet = defaultSet
	}
	target := implementationSetKey(targetSet)
	for idx, combo := range c.implCombos {
		if implementationSetKey(combo) == target {
			c.implComboIndex = idx
			c.impls = append([]string(nil), combo...)
			return
		}
	}
	c.implComboIndex = -1
	c.impls = append([]string(nil), targetSet...)
}

type taskEditorAppliedMsg struct {
	msg TaskCreatedMsg
}

type taskEditorErrorMsg struct {
	err error
}

// Init initializes the overlay
func (c *CreateTaskOverlay) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if strings.TrimSpace(c.id) != "" && c.attachmentSvc != nil {
		cmds = append(cmds, c.loadAttachments())
	}
	return tea.Batch(cmds...)
}

// Update handles messages
func (c *CreateTaskOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.ApplyWindowSize(msg)
		return c, nil
	case tea.KeyMsg:
		if isPasteAttachmentKey(msg) {
			if c.attachmentSvc == nil {
				return c, func() tea.Msg {
					return errorMsg{err: fmt.Errorf("image attachment service unavailable")}
				}
			}
			return c, c.pasteAttachment()
		}
		switch msg.String() {
		case "esc":
			return c, func() tea.Msg { return CloseOverlayMsg{} }

		case "ctrl+c":
			c.clearFocusedField()
			return c, nil

		case "ctrl+s":
			// Submit the form
			return c, c.submit()

		case "ctrl+e":
			return c, c.editInEditorCmd()

		case "ctrl+k":
			c.clearToDefaults()
			return c, nil

		case "tab", "shift+tab":
			// Tab through fields
			if msg.String() == "tab" {
				c.focusIndex = (c.focusIndex + 1) % focusCount
			} else {
				c.focusIndex = (c.focusIndex - 1 + focusCount) % focusCount
			}

			// Update focus
			if c.focusIndex == focusTitle {
				c.title.Focus()
				c.description.Blur()
				c.acceptanceInput.Blur()
			} else if c.focusIndex == focusDescription {
				c.title.Blur()
				c.description.Focus()
				c.acceptanceInput.Blur()
			} else if c.focusIndex == focusAcceptance {
				c.title.Blur()
				c.description.Blur()
				c.acceptanceInput.Focus()
			} else if c.focusIndex == focusAttachments {
				c.title.Blur()
				c.description.Blur()
				c.acceptanceInput.Blur()
			} else if c.focusIndex == focusImpls {
				c.title.Blur()
				c.description.Blur()
				c.acceptanceInput.Blur()
			} else {
				c.title.Blur()
				c.description.Blur()
				c.acceptanceInput.Blur()
			}

			return c, nil

		case "enter":
			if c.focusIndex == focusDescription || c.focusIndex == focusAcceptance {
				break
			}
			// Enter submits from non-description fields so submit does not depend on Ctrl+S.
			if c.focusIndex == focusSubmit || c.focusIndex == focusTitle || c.focusIndex == focusType || c.focusIndex == focusPriority || c.focusIndex == focusImpls || c.focusIndex == focusAttachments {
				return c, c.submit()
			}
		case "left", "h":
			if c.focusIndex == focusImpls {
				c.cycleImplementationCombo(-1)
				return c, nil
			}
		case "right", "l":
			if c.focusIndex == focusImpls {
				c.cycleImplementationCombo(1)
				return c, nil
			}
		case "j", "down":
			if c.focusIndex == focusAttachments && len(c.attachments) > 0 {
				c.attachmentIndex = min(c.attachmentIndex+1, len(c.attachments)-1)
				return c, nil
			}
		case "k", "up":
			if c.focusIndex == focusAttachments && len(c.attachments) > 0 {
				c.attachmentIndex = max(0, c.attachmentIndex-1)
				return c, nil
			}
		case "d", "x":
			if c.focusIndex == focusAttachments && len(c.attachments) > 0 && c.attachmentSvc != nil {
				return c, c.deleteSelectedAttachment()
			}
		}

		// Handle type selection when focused
		if c.focusIndex == focusType {
			switch msg.String() {
			case "t", "T":
				c.taskType = domain.TypeTask
				return c, nil
			case "b", "B":
				c.taskType = domain.TypeBug
				return c, nil
			case "f", "F":
				c.taskType = domain.TypeFeature
				return c, nil
			case "e", "E":
				c.taskType = domain.TypeEpic
				return c, nil
			case "c", "C":
				c.taskType = domain.TypeChore
				return c, nil
			}
		}

		// Handle priority selection when focused
		if c.focusIndex == focusPriority {
			switch msg.String() {
			case "0":
				c.priority = domain.P0
				return c, nil
			case "1":
				c.priority = domain.P1
				return c, nil
			case "2":
				c.priority = domain.P2
				return c, nil
			case "3":
				c.priority = domain.P3
				return c, nil
			case "4":
				c.priority = domain.P4
				return c, nil
			}
		}
	}
	switch msg := msg.(type) {
	case taskEditorAppliedMsg:
		c.editorError = ""
		c.title.SetValue(msg.msg.Title)
		c.description.SetValue(msg.msg.Description)
		c.taskType = msg.msg.Type
		c.priority = msg.msg.Priority
		c.status = msg.msg.Status
		c.assignee = msg.msg.Assignee
		c.labels = append([]string(nil), msg.msg.Labels...)
		c.impls = append([]string(nil), msg.msg.Implementations...)
		c.syncImplementationSelection()
		c.design = msg.msg.Design
		c.notes = msg.msg.Notes
		c.acceptance = msg.msg.Acceptance
		c.acceptanceInput.SetValue(msg.msg.Acceptance)
		c.estimate = msg.msg.Estimate
		return c, tea.Batch(
			func() tea.Msg { return msg.msg },
			func() tea.Msg { return CloseOverlayMsg{} },
		)
	case taskEditorErrorMsg:
		c.editorError = msg.err.Error()
		return c, nil
	case attachmentsLoadedMsg:
		c.attachments = append([]attachment.Attachment(nil), msg.attachments...)
		c.attachmentError = ""
		if c.attachmentIndex >= len(c.attachments) && len(c.attachments) > 0 {
			c.attachmentIndex = len(c.attachments) - 1
		}
		if len(c.attachments) == 0 {
			c.attachmentIndex = 0
		}
		return c, nil
	case attachmentAddedMsg:
		c.attachmentError = ""
		if strings.TrimSpace(c.id) == "" {
			return c, c.loadAttachments()
		}
		return c, tea.Batch(
			c.loadAttachments(),
			func() tea.Msg {
				return AttachmentActionMsg{
					Action:     "attached",
					Attachment: msg.attachment,
				}
			},
		)
	case attachmentDeletedMsg:
		c.attachmentError = ""
		if strings.TrimSpace(c.id) == "" {
			return c, c.loadAttachments()
		}
		return c, tea.Batch(
			c.loadAttachments(),
			func() tea.Msg {
				return AttachmentActionMsg{
					Action: "deleted",
				}
			},
		)
	case errorMsg:
		c.attachmentError = compactOverlayError(msg.err)
		return c, func() tea.Msg {
			return AttachmentActionMsg{
				Action: "error",
				Error:  msg.err,
			}
		}
	}

	// Update active field
	var cmd tea.Cmd
	if c.focusIndex == focusTitle {
		c.title, cmd = c.title.Update(msg)
		cmds = append(cmds, cmd)
	} else if c.focusIndex == focusDescription {
		c.description, cmd = c.description.Update(msg)
		cmds = append(cmds, cmd)
	} else if c.focusIndex == focusAcceptance {
		c.acceptanceInput, cmd = c.acceptanceInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return c, tea.Batch(cmds...)
}

func isPasteAttachmentKey(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyCtrlP:
		return true
	}

	switch strings.ToLower(strings.TrimSpace(msg.String())) {
	case "ctrl+p":
		return true
	}

	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case rune(0x10): // ^P
			return true
		}
	}

	return false
}

// View renders the form
func (c *CreateTaskOverlay) View() string {
	width, height := c.ClampResponsive(createTaskOverlayWidth, createTaskOverlayHeight)
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            c.styles,
		width:             width,
		height:            height,
		title:             c.Title(),
		rightSectionTitle: "Actions",
		breakpoint:        60,
		gap:               3,
		minLeft:           38,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return c.renderFormContent(width, height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(c.styles, []keybinds.Binding{
				{Key: "Tab / Shift+Tab", Description: "Switch fields"},
				{Key: "Ctrl+C", Description: "Clear focused field"},
				{Key: "T/B/F/E/C", Description: "Set type"},
				{Key: "0/1/2/3/4", Description: "Set priority"},
				{Key: "h/l or ←/→", Description: "Cycle impl combinations"},
				{Key: "Ctrl+P", Description: "Paste image"},
				{Key: "j/k + d", Description: "Manage attachments"},
				{Key: "Enter", Description: "Create task"},
				{Key: "Ctrl+E", Description: "Edit in $EDITOR"},
				{Key: "Ctrl+K", Description: "Clear form"},
				{Key: "Esc", Description: "Cancel"},
			})
		},
	})
}

func (c *CreateTaskOverlay) SetAttachmentService(svc ImageAttachmentService) {
	c.attachmentSvc = svc
}

func (c *CreateTaskOverlay) HasAttachmentService() bool {
	return c.attachmentSvc != nil
}

func (c *CreateTaskOverlay) clearToDefaults() {
	c.title.SetValue(c.defaults.title)
	c.description.SetValue(c.defaults.description)
	c.acceptanceInput.SetValue(c.defaults.acceptance)
	c.taskType = c.defaults.taskType
	c.priority = c.defaults.priority
	c.status = c.defaults.status
	c.assignee = c.defaults.assignee
	c.labels = append([]string(nil), c.defaults.labels...)
	c.impls = append([]string(nil), c.defaults.impls...)
	c.syncImplementationSelection()
	c.design = c.defaults.design
	c.notes = c.defaults.notes
	c.acceptance = c.defaults.acceptance
	c.estimate = c.defaults.estimate
	c.editorError = ""
	c.focusIndex = focusTitle
	c.title.Focus()
	c.description.Blur()
	c.acceptanceInput.Blur()
	c.attachmentIndex = 0
	c.attachmentError = ""
}

func (c *CreateTaskOverlay) clearFocusedField() {
	switch c.focusIndex {
	case focusTitle:
		c.title.SetValue("")
	case focusDescription:
		c.description.SetValue("")
	case focusImpls:
		c.impls = append([]string(nil), c.defaults.impls...)
		c.syncImplementationSelection()
	case focusAcceptance:
		c.acceptanceInput.SetValue("")
		c.acceptance = ""
	}
}

func (c *CreateTaskOverlay) cycleImplementationCombo(direction int) {
	if len(c.implCombos) == 0 {
		return
	}
	if c.implComboIndex < 0 {
		if direction > 0 {
			c.implComboIndex = 0
		} else {
			c.implComboIndex = len(c.implCombos) - 1
		}
	} else if direction > 0 {
		c.implComboIndex = (c.implComboIndex + 1) % len(c.implCombos)
	} else if direction < 0 {
		c.implComboIndex = (c.implComboIndex - 1 + len(c.implCombos)) % len(c.implCombos)
	}
	c.impls = append([]string(nil), c.implCombos[c.implComboIndex]...)
}

func (c *CreateTaskOverlay) renderFormContent(width, height int) string {
	stacked := width < 52
	titleLabelWidth := lipgloss.Width("Title: ")
	titleWidth := max(8, width-titleLabelWidth-3)
	if stacked {
		titleWidth = max(8, width-4)
	}
	descriptionWidth := max(10, width-4)
	descriptionHeight := max(4, height-16)
	if stacked {
		descriptionHeight = max(4, height-20)
	}
	acceptanceHeight := max(3, height-20)
	if stacked {
		acceptanceHeight = max(3, height-24)
	}
	c.title.Width = titleWidth
	c.description.SetWidth(descriptionWidth)
	c.description.SetHeight(descriptionHeight)
	c.acceptanceInput.SetWidth(descriptionWidth)
	c.acceptanceInput.SetHeight(acceptanceHeight)

	var b strings.Builder

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5"))

	focusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	// Title field
	if c.focusIndex == focusTitle {
		b.WriteString(focusStyle.Render("Title:"))
	} else {
		b.WriteString(labelStyle.Render("Title:"))
	}
	if stacked {
		b.WriteString("\n")
		b.WriteString(c.title.View())
		b.WriteString("\n")
	} else {
		b.WriteString(" ")
		b.WriteString(c.title.View())
		b.WriteString("\n")
	}
	titlePreviewWidth := max(1, titleWidth-2)
	titlePreviewLines := wrapTitleLines(c.title.Value(), titlePreviewWidth)
	if c.focusIndex == focusTitle && len(titlePreviewLines) > 1 {
		titlePreviewStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8"))
		b.WriteString(titlePreviewStyle.Render(strings.Join(titlePreviewLines, "\n")))
		b.WriteString("\n")
	}

	// Description field
	if c.focusIndex == focusDescription {
		b.WriteString(focusStyle.Render("Description:"))
	} else {
		b.WriteString(labelStyle.Render("Description:"))
	}
	b.WriteString("\n")
	b.WriteString(c.description.View())
	b.WriteString("\n")

	// Type selector
	if c.focusIndex == focusType {
		b.WriteString(focusStyle.Render("Type:"))
	} else {
		b.WriteString(labelStyle.Render("Type:"))
	}
	b.WriteString(" ")
	b.WriteString(c.renderTypeSelector())
	b.WriteString("\n")

	// Priority selector
	if c.focusIndex == focusPriority {
		b.WriteString(focusStyle.Render("Priority:"))
	} else {
		b.WriteString(labelStyle.Render("Priority:"))
	}
	b.WriteString(" ")
	b.WriteString(c.renderPrioritySelector())
	b.WriteString("\n")

	if c.focusIndex == focusImpls {
		b.WriteString(focusStyle.Render("Impls:"))
	} else {
		b.WriteString(labelStyle.Render("Impls:"))
	}
	b.WriteString(" ")
	b.WriteString(c.renderImplementationSelector())
	b.WriteString("\n")

	if c.focusIndex == focusAcceptance {
		b.WriteString(focusStyle.Render("Acceptance Criteria:"))
	} else {
		b.WriteString(labelStyle.Render("Acceptance Criteria:"))
	}
	b.WriteString("\n")
	b.WriteString(c.acceptanceInput.View())
	b.WriteString("\n")
	if strings.TrimSpace(c.id) != "" || c.attachmentSvc != nil {
		if c.focusIndex == focusAttachments {
			b.WriteString(focusStyle.Render("Image Attachments:"))
		} else {
			b.WriteString(labelStyle.Render("Image Attachments:"))
		}
		b.WriteString("\n")
		b.WriteString(c.renderAttachmentList())
		b.WriteString("\n\n")
	}
	if !stacked {
		b.WriteString("\n")
		b.WriteString(c.styles.Separator.Render(strings.Repeat("─", max(6, width-2))))
		b.WriteString("\n\n")
	} else {
		b.WriteString("\n")
	}

	// Submit button
	submitStyle := c.styles.MenuItem
	if c.focusIndex == focusSubmit {
		submitStyle = c.styles.MenuItemActive
	}
	b.WriteString(submitStyle.Render("[ Create Task ]"))
	b.WriteString("\n")
	if c.editorError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("Editor error: " + c.editorError))
	}

	return b.String()
}

// renderTypeSelector renders the type selector with current selection
func (c *CreateTaskOverlay) renderTypeSelector() string {
	types := []struct {
		key  string
		typ  domain.TaskType
		name string
	}{
		{"T", domain.TypeTask, "Task"},
		{"B", domain.TypeBug, "Bug"},
		{"F", domain.TypeFeature, "Feature"},
		{"E", domain.TypeEpic, "Epic"},
		{"C", domain.TypeChore, "Chore"},
	}

	var parts []string
	for _, t := range types {
		style := c.styles.MenuItem
		indicator := " "
		if t.typ == c.taskType {
			style = c.styles.MenuItemActive
			indicator = "●"
		}

		parts = append(parts, style.Render(fmt.Sprintf("[%s%s]", indicator, t.key)))
	}

	return strings.Join(parts, " ")
}

// renderPrioritySelector renders the priority selector with current selection
func (c *CreateTaskOverlay) renderPrioritySelector() string {
	priorities := []struct {
		key string
		pri domain.Priority
	}{
		{"0", domain.P0},
		{"1", domain.P1},
		{"2", domain.P2},
		{"3", domain.P3},
		{"4", domain.P4},
	}

	var parts []string
	for _, p := range priorities {
		style := c.styles.MenuItem
		indicator := " "
		if p.pri == c.priority {
			style = c.styles.MenuItemActive
			indicator = "●"
		}

		parts = append(parts, style.Render(fmt.Sprintf("[%s%s]", indicator, p.key)))
	}

	return strings.Join(parts, " ")
}

func (c *CreateTaskOverlay) renderImplementationSelector() string {
	current := strings.Join(c.impls, " + ")
	if strings.TrimSpace(current) == "" {
		current = "(none)"
	}
	style := c.styles.MenuItem
	if c.focusIndex == focusImpls {
		style = c.styles.MenuItemActive
	}
	return style.Render(fmt.Sprintf("[<] %s [>]", current))
}

func (c *CreateTaskOverlay) renderAttachmentList() string {
	if c.attachmentSvc == nil {
		return c.styles.Footer.Render("Attachment service unavailable.")
	}
	if len(c.attachments) == 0 {
		empty := "No attachments yet. Ctrl+P to paste from clipboard."
		if strings.TrimSpace(c.id) == "" {
			empty = "No staged attachments yet. Ctrl+P to paste before creating."
		}
		if c.attachmentError != "" {
			return c.styles.Footer.Render(empty + " Error: " + c.attachmentError)
		}
		return c.styles.Footer.Render(empty)
	}
	lines := make([]string, 0, len(c.attachments)+1)
	for idx, file := range c.attachments {
		indicator := "  "
		style := c.styles.MenuItem
		if idx == c.attachmentIndex {
			indicator = "▶ "
			style = c.styles.MenuItemActive
		}
		entry := fmt.Sprintf("%s%-30s %8s", indicator, truncate(file.Filename, 30), formatFileSize(file.Size))
		lines = append(lines, style.Render(entry))
	}
	if c.attachmentError != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Render("Error: "+c.attachmentError))
	}
	return strings.Join(lines, "\n")
}

func (c *CreateTaskOverlay) loadAttachments() tea.Cmd {
	targetID := c.attachmentTargetID()
	if targetID == "" || c.attachmentSvc == nil {
		return nil
	}
	return func() tea.Msg {
		files, err := c.attachmentSvc.List(context.Background(), targetID)
		if err != nil {
			return errorMsg{err: err}
		}
		return attachmentsLoadedMsg{attachments: files}
	}
}

func (c *CreateTaskOverlay) pasteAttachment() tea.Cmd {
	targetID := c.attachmentTargetID()
	if targetID == "" || c.attachmentSvc == nil {
		return nil
	}
	return func() tea.Msg {
		attached, err := c.attachmentSvc.AttachFromClipboard(context.Background(), targetID)
		if err != nil {
			return errorMsg{err: err}
		}
		return attachmentAddedMsg{attachment: attached}
	}
}

func (c *CreateTaskOverlay) deleteSelectedAttachment() tea.Cmd {
	targetID := c.attachmentTargetID()
	if targetID == "" || c.attachmentSvc == nil || c.attachmentIndex < 0 || c.attachmentIndex >= len(c.attachments) {
		return nil
	}
	selected := c.attachments[c.attachmentIndex]
	return func() tea.Msg {
		if err := c.attachmentSvc.Delete(context.Background(), targetID, selected.ID); err != nil {
			return errorMsg{err: err}
		}
		return attachmentDeletedMsg{}
	}
}

func (c *CreateTaskOverlay) attachmentTargetID() string {
	if id := strings.TrimSpace(c.id); id != "" {
		return id
	}
	if c.attachmentSvc == nil {
		return ""
	}
	if strings.TrimSpace(c.draftIssueID) == "" {
		c.draftIssueID = fmt.Sprintf("draft-%d", time.Now().UnixNano())
	}
	return c.draftIssueID
}

func wrapTitleLines(value string, width int) []string {
	if width < 1 {
		return strings.Split(value, "\n")
	}
	// For titles, wrap on spaces only to avoid splitting tokens like --project.
	wordWrapped := ansi.Wrap(value, width, " ")
	lines := strings.Split(wordWrapped, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return out
}

// submit creates a TaskCreatedMsg and closes the overlay
func (c *CreateTaskOverlay) submit() tea.Cmd {
	// Validate title is not empty
	title := strings.TrimSpace(c.title.Value())
	if title == "" {
		return nil // Don't submit if title is empty
	}
	implementations := append([]string(nil), c.impls...)
	acceptance := strings.TrimSpace(c.acceptanceInput.Value())
	c.acceptance = acceptance

	return tea.Batch(
		func() tea.Msg {
			return TaskCreatedMsg{
				ID:              c.id,
				Title:           title,
				Description:     strings.TrimSpace(c.description.Value()),
				Type:            c.taskType,
				Priority:        c.priority,
				Status:          domain.StatusOpen,
				Assignee:        strings.TrimSpace(c.assignee),
				Labels:          append([]string(nil), c.labels...),
				Implementations: append([]string(nil), implementations...),
				Design:          strings.TrimSpace(c.design),
				Notes:           strings.TrimSpace(c.notes),
				Acceptance:      acceptance,
				Estimate:        c.estimate,
				ParentID:        c.parentID,
				AttachmentPaths: c.stagedAttachmentPaths(),
			}
		},
		func() tea.Msg { return CloseOverlayMsg{} },
	)
}

func (c *CreateTaskOverlay) stagedAttachmentPaths() []string {
	if strings.TrimSpace(c.id) != "" || len(c.attachments) == 0 {
		return nil
	}
	paths := make([]string, 0, len(c.attachments))
	for _, file := range c.attachments {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
}

func (c *CreateTaskOverlay) editInEditorCmd() tea.Cmd {
	template := serializeTaskTemplate(
		c.id,
		c.title.Value(),
		c.description.Value(),
		c.taskType,
		c.priority,
		c.status,
		c.assignee,
		c.labels,
		c.impls,
		c.design,
		c.notes,
		c.acceptanceInput.Value(),
		c.estimate,
	)

	if c.editorFlow != nil {
		return func() tea.Msg {
			edited, err := c.editorFlow(template)
			if err != nil {
				return taskEditorErrorMsg{err: err}
			}
			created, err := parseTaskTemplate(edited, c.id, c.parentID)
			if err != nil {
				return taskEditorErrorMsg{err: err}
			}
			return taskEditorAppliedMsg{msg: created}
		}
	}

	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("azedarach-task-%d.md", time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, []byte(template), 0600); err != nil {
		return func() tea.Msg {
			return taskEditorErrorMsg{err: fmt.Errorf("write temp file: %w", err)}
		}
	}

	editorName := strings.TrimSpace(os.Getenv("EDITOR"))
	if editorName == "" {
		editorName = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editorName == "" {
		editorName = "vim"
	}

	cmd := exec.Command(editorName, tempFile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return overlayExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(tempFile)
		if err != nil {
			return taskEditorErrorMsg{err: fmt.Errorf("open editor: %w", err)}
		}
		edited, readErr := os.ReadFile(tempFile)
		if readErr != nil {
			return taskEditorErrorMsg{err: fmt.Errorf("read edited file: %w", readErr)}
		}
		created, parseErr := parseTaskTemplate(string(edited), c.id, c.parentID)
		if parseErr != nil {
			return taskEditorErrorMsg{err: parseErr}
		}
		return taskEditorAppliedMsg{msg: created}
	})
}

func serializeTaskTemplate(
	id, title, description string,
	taskType domain.TaskType,
	priority domain.Priority,
	status domain.Status,
	assignee string,
	labels []string,
	implementations []string,
	design string,
	notes string,
	acceptance string,
	estimate *int,
) string {
	if strings.TrimSpace(title) == "" {
		title = taskTemplateAnchorTitle
	}
	if strings.TrimSpace(description) == "" {
		description = taskTemplateAnchorDescription
	}
	if strings.TrimSpace(string(taskType)) == "" {
		taskType = domain.TypeTask
	}
	if strings.TrimSpace(string(status)) == "" {
		status = domain.StatusOpen
	}
	if strings.TrimSpace(design) == "" {
		design = taskTemplateAnchorDesign
	}
	if strings.TrimSpace(notes) == "" {
		notes = taskTemplateAnchorNotes
	}
	if strings.TrimSpace(acceptance) == "" {
		acceptance = taskTemplateAnchorAcceptance
	}
	labelsValue := strings.Join(labels, ", ")
	implValue := strings.Join(implementations, ", ")
	if strings.TrimSpace(implValue) == "" {
		implValue = "default"
	}
	estimateValue := ""
	if estimate != nil {
		estimateValue = strconv.Itoa(*estimate)
	}
	lines := []string{
		"# " + title,
		taskTemplateDivider,
		"",
		fmt.Sprintf("Type:     %s        (task | bug | feature | epic | chore)", string(taskType)),
		fmt.Sprintf("Priority: P%d          (P0 = highest, P4 = lowest)", int(priority)),
		fmt.Sprintf("Status:   %s        (open | in_progress | blocked | closed)", string(status)),
		"Assignee: " + strings.TrimSpace(assignee),
		"Labels:   " + labelsValue,
		"Impl:     " + implValue,
		"Estimate: " + estimateValue,
		"",
		taskTemplateDivider,
		"## Description",
		"",
		description,
		"",
		taskTemplateDivider,
		"## Design",
		"",
		design,
		"",
		taskTemplateDivider,
		"## Notes",
		"",
		notes,
		"",
		taskTemplateDivider,
		"## Acceptance Criteria",
		"",
		acceptance,
		"",
	}
	if id != "" {
		lines = append(lines,
			taskTemplateDivider,
			fmt.Sprintf("ID: %s (read-only)", id),
			"",
		)
	}
	return strings.Join(lines, "\n")
}

func parseTaskTemplate(markdown, id string, parentID *string) (TaskCreatedMsg, error) {
	lines := strings.Split(markdown, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "# ") {
		return TaskCreatedMsg{}, fmt.Errorf("missing title header (# ...)")
	}
	title := strings.TrimSpace(strings.TrimPrefix(lines[0], "# "))
	if title == "" || title == taskTemplateAnchorTitle {
		return TaskCreatedMsg{}, fmt.Errorf("title is required")
	}

	taskType := domain.TypeTask
	priority := domain.P2
	status := domain.StatusOpen
	var estimate *int
	var assignee string
	var labels []string
	var implementations []string

	for _, line := range lines {
		if strings.HasPrefix(line, "Type:") {
			raw := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "Type:")))
			raw = strings.SplitN(raw, "(", 2)[0]
			raw = strings.TrimSpace(raw)
			switch raw {
			case "task":
				taskType = domain.TypeTask
			case "bug":
				taskType = domain.TypeBug
			case "feature":
				taskType = domain.TypeFeature
			case "epic":
				taskType = domain.TypeEpic
			case "chore":
				taskType = domain.TypeChore
			default:
				return TaskCreatedMsg{}, fmt.Errorf("invalid type %q", raw)
			}
		}
		if strings.HasPrefix(line, "Priority:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "Priority:"))
			raw = strings.SplitN(raw, "(", 2)[0]
			raw = strings.TrimSpace(raw)
			raw = strings.TrimPrefix(strings.ToUpper(raw), "P")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 4 {
				return TaskCreatedMsg{}, fmt.Errorf("invalid priority %q", raw)
			}
			priority = domain.Priority(n)
		}
		if strings.HasPrefix(line, "Status:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
			raw = strings.SplitN(raw, "(", 2)[0]
			raw = strings.TrimSpace(raw)
			switch raw {
			case string(domain.StatusOpen):
				status = domain.StatusOpen
			case string(domain.StatusInProgress):
				status = domain.StatusInProgress
			case string(domain.StatusBlocked):
				status = domain.StatusBlocked
			case string(domain.StatusDone):
				status = domain.StatusDone
			default:
				return TaskCreatedMsg{}, fmt.Errorf("invalid status %q", raw)
			}
		}
		if strings.HasPrefix(line, "Assignee:") {
			assignee = strings.TrimSpace(strings.TrimPrefix(line, "Assignee:"))
		}
		if strings.HasPrefix(line, "Labels:") {
			labels = splitCSV(strings.TrimSpace(strings.TrimPrefix(line, "Labels:")))
		}
		if strings.HasPrefix(line, "Impl:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "Impl:"))
			value = strings.SplitN(value, "(", 2)[0]
			implementations = splitCSV(strings.TrimSpace(value))
		}
		if strings.HasPrefix(line, "Estimate:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "Estimate:"))
			if raw != "" {
				n, err := strconv.Atoi(raw)
				if err != nil {
					return TaskCreatedMsg{}, fmt.Errorf("invalid estimate %q", raw)
				}
				estimate = &n
			}
		}
	}

	description := parseSection(markdown, "Description")
	if description == taskTemplateAnchorDescription {
		description = ""
	}
	design := parseSection(markdown, "Design")
	if design == taskTemplateAnchorDesign {
		design = ""
	}
	notes := parseSection(markdown, "Notes")
	if notes == taskTemplateAnchorNotes {
		notes = ""
	}
	acceptance := parseSection(markdown, "Acceptance Criteria")
	if acceptance == taskTemplateAnchorAcceptance {
		acceptance = ""
	}

	return TaskCreatedMsg{
		ID:              id,
		Title:           title,
		Description:     strings.TrimSpace(description),
		Type:            taskType,
		Priority:        priority,
		Status:          status,
		Assignee:        assignee,
		Labels:          labels,
		Implementations: implementations,
		Design:          strings.TrimSpace(design),
		Notes:           strings.TrimSpace(notes),
		Acceptance:      strings.TrimSpace(acceptance),
		Estimate:        estimate,
		ParentID:        parentID,
	}, nil
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			values = append(values, trimmed)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func parseSection(markdown, sectionName string) string {
	lines := strings.Split(markdown, "\n")
	inSection := false
	var content []string
	header := "## " + sectionName
	for _, line := range lines {
		if strings.TrimSpace(line) == header {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(strings.TrimSpace(line), "## ") {
			break
		}
		if inSection {
			if strings.TrimSpace(line) == taskTemplateDivider {
				continue
			}
			content = append(content, line)
		}
	}
	return strings.TrimSpace(strings.Join(content, "\n"))
}

// Title returns the overlay title
func (c *CreateTaskOverlay) Title() string {
	return "Create New Task"
}

// Size returns the overlay dimensions
func (c *CreateTaskOverlay) Size() (width, height int) {
	return c.ClampResponsive(createTaskOverlayWidth, createTaskOverlayHeight)
}
