package overlay

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

const (
	taskTemplateAnchorTitle       = "TITLE"
	taskTemplateAnchorDescription = "DESCRIPTION"
	taskTemplateAnchorDesign      = "DESIGN"
	taskTemplateAnchorNotes       = "NOTES"
	taskTemplateAnchorAcceptance  = "ACCEPTANCE"
)

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
}

// CreateTaskOverlay provides a form to create a new task
type CreateTaskOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	id          string
	title       textinput.Model
	description textarea.Model
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
	parentID    *string
	focusIndex  int
	styles      *Styles
	editorError string
	editorFlow  func(string) (string, error)
	defaults    createTaskDefaults
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
	focusSubmit
	focusCount
)

// NewCreateTaskOverlay creates a new task creation overlay
func NewCreateTaskOverlay() *CreateTaskOverlay {
	return NewCreateTaskOverlayWithParent(nil)
}

func NewEditTaskOverlay(task domain.Task) *CreateTaskOverlay {
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

	return &CreateTaskOverlay{
		id:          task.ID,
		title:       ti,
		description: ta,
		taskType:    task.Type,
		priority:    task.Priority,
		status:      task.Status,
		impls:       task.Implementations,
		parentID:    task.ParentID,
		focusIndex:  focusTitle,
		styles:      New(),
		editorFlow:  runTaskTemplateInEditor,
		defaults: createTaskDefaults{
			title:       task.Title,
			description: task.Description,
			taskType:    task.Type,
			priority:    task.Priority,
			status:      task.Status,
			impls:       append([]string(nil), task.Implementations...),
		},
	}
}

func NewCreateTaskOverlayWithParent(parentID *string) *CreateTaskOverlay {
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

	return &CreateTaskOverlay{
		title:       ti,
		description: ta,
		taskType:    domain.TypeTask,
		priority:    domain.P2,
		status:      domain.StatusOpen,
		parentID:    parentID,
		focusIndex:  focusTitle,
		styles:      New(),
		editorFlow:  runTaskTemplateInEditor,
		defaults: createTaskDefaults{
			taskType: domain.TypeTask,
			priority: domain.P2,
			status:   domain.StatusOpen,
		},
	}
}

type taskEditorAppliedMsg struct {
	msg TaskCreatedMsg
}

type taskEditorErrorMsg struct {
	err error
}

// Init initializes the overlay
func (c *CreateTaskOverlay) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages
func (c *CreateTaskOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.ApplyWindowSize(msg)
		return c, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return c, func() tea.Msg { return CloseOverlayMsg{} }

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
			} else if c.focusIndex == focusDescription {
				c.title.Blur()
				c.description.Focus()
			} else {
				c.title.Blur()
				c.description.Blur()
			}

			return c, nil

		case "enter":
			if c.focusIndex == focusDescription {
				break
			}
			// Enter submits from non-description fields so submit does not depend on Ctrl+S.
			if c.focusIndex == focusSubmit || c.focusIndex == focusTitle || c.focusIndex == focusType || c.focusIndex == focusPriority {
				return c, c.submit()
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
		c.design = msg.msg.Design
		c.notes = msg.msg.Notes
		c.acceptance = msg.msg.Acceptance
		c.estimate = msg.msg.Estimate
		return c, tea.Batch(
			func() tea.Msg { return msg.msg },
			func() tea.Msg { return CloseOverlayMsg{} },
		)
	case taskEditorErrorMsg:
		c.editorError = msg.err.Error()
		return c, nil
	}

	// Update active field
	var cmd tea.Cmd
	if c.focusIndex == focusTitle {
		c.title, cmd = c.title.Update(msg)
		cmds = append(cmds, cmd)
	} else if c.focusIndex == focusDescription {
		c.description, cmd = c.description.Update(msg)
		cmds = append(cmds, cmd)
	}

	return c, tea.Batch(cmds...)
}

// View renders the form
func (c *CreateTaskOverlay) View() string {
	width, height := c.Clamp(72, 22)
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
				{Key: "T/B/F/E/C", Description: "Set type"},
				{Key: "0/1/2/3/4", Description: "Set priority"},
				{Key: "Enter", Description: "Create task"},
				{Key: "Ctrl+E", Description: "Edit in $EDITOR"},
				{Key: "Ctrl+K", Description: "Clear form"},
				{Key: "Esc", Description: "Cancel"},
			})
		},
	})
}

func (c *CreateTaskOverlay) clearToDefaults() {
	c.title.SetValue(c.defaults.title)
	c.description.SetValue(c.defaults.description)
	c.taskType = c.defaults.taskType
	c.priority = c.defaults.priority
	c.status = c.defaults.status
	c.assignee = c.defaults.assignee
	c.labels = append([]string(nil), c.defaults.labels...)
	c.impls = append([]string(nil), c.defaults.impls...)
	c.design = c.defaults.design
	c.notes = c.defaults.notes
	c.acceptance = c.defaults.acceptance
	c.estimate = c.defaults.estimate
	c.editorError = ""
	c.focusIndex = focusTitle
	c.title.Focus()
	c.description.Blur()
}

func (c *CreateTaskOverlay) renderFormContent(width, height int) string {
	stacked := width < 52
	titleWidth := max(20, width-6)
	if stacked {
		titleWidth = max(20, width-4)
	}
	descriptionWidth := max(24, width-4)
	descriptionHeight := max(4, height-12)
	if stacked {
		descriptionHeight = max(4, height-16)
	}
	c.title.Width = titleWidth
	c.description.SetWidth(descriptionWidth)
	c.description.SetHeight(descriptionHeight)

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

// submit creates a TaskCreatedMsg and closes the overlay
func (c *CreateTaskOverlay) submit() tea.Cmd {
	// Validate title is not empty
	title := strings.TrimSpace(c.title.Value())
	if title == "" {
		return nil // Don't submit if title is empty
	}

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
				Implementations: append([]string(nil), c.impls...),
				Design:          strings.TrimSpace(c.design),
				Notes:           strings.TrimSpace(c.notes),
				Acceptance:      strings.TrimSpace(c.acceptance),
				Estimate:        c.estimate,
				ParentID:        c.parentID,
			}
		},
		func() tea.Msg { return CloseOverlayMsg{} },
	)
}

func (c *CreateTaskOverlay) editInEditorCmd() tea.Cmd {
	return func() tea.Msg {
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
			c.acceptance,
			c.estimate,
		)
		editorFlow := c.editorFlow
		if editorFlow == nil {
			editorFlow = runTaskTemplateInEditor
		}
		edited, err := editorFlow(template)
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
		"───────────────────────────────────────────────────",
		"",
		fmt.Sprintf("Type:     %s        (task | bug | feature | epic | chore)", string(taskType)),
		fmt.Sprintf("Priority: P%d          (P0 = highest, P4 = lowest)", int(priority)),
		fmt.Sprintf("Status:   %s        (open | in_progress | blocked | closed)", string(status)),
		"Assignee: " + strings.TrimSpace(assignee),
		"Labels:   " + labelsValue,
		"Impl:     " + implValue,
		"Estimate: " + estimateValue,
		"",
		"───────────────────────────────────────────────────",
		"## Description",
		"",
		description,
		"",
		"───────────────────────────────────────────────────",
		"## Design",
		"",
		design,
		"",
		"───────────────────────────────────────────────────",
		"## Notes",
		"",
		notes,
		"",
		"───────────────────────────────────────────────────",
		"## Acceptance Criteria",
		"",
		acceptance,
		"",
	}
	if id != "" {
		lines = append(lines,
			"───────────────────────────────────────────────────",
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
			content = append(content, line)
		}
	}
	return strings.TrimSpace(strings.Join(content, "\n"))
}

func runTaskTemplateInEditor(template string) (string, error) {
	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("azedarach-task-%d.md", time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, []byte(template), 0600); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}
	defer os.Remove(tempFile)

	editorName := strings.TrimSpace(os.Getenv("EDITOR"))
	if editorName == "" {
		editorName = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editorName == "" {
		editorName = "vim"
	}

	cmd := exec.Command("sh", "-c", fmt.Sprintf(`%s %q`, editorName, tempFile))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("open editor: %w", err)
	}

	edited, err := os.ReadFile(tempFile)
	if err != nil {
		return "", fmt.Errorf("read edited file: %w", err)
	}
	return string(edited), nil
}

// Title returns the overlay title
func (c *CreateTaskOverlay) Title() string {
	return "Create New Task"
}

// Size returns the overlay dimensions
func (c *CreateTaskOverlay) Size() (width, height int) {
	return c.Clamp(72, 22)
}
