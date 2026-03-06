package overlay

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

// DetailPanel displays full task details with scrollable description.
type DetailPanel struct {
	task          domain.Task
	session       *domain.Session
	scrollY       int
	contentHeight int
	viewHeight    int
	styles        *Styles

	attachmentService *attachment.Service
	attachments       []attachment.Attachment
	attachmentCursor  int
	attachmentWarning string
	attachmentError   string
	openExternal      func(path string) error
}

// NewDetailPanel creates a new detail panel for the given task and optional session.
func NewDetailPanel(task domain.Task, session *domain.Session) *DetailPanel {
	return newDetailPanel(task, session, nil)
}

// NewDetailPanelWithAttachments creates a detail panel with attachment-aware behavior.
func NewDetailPanelWithAttachments(task domain.Task, session *domain.Session, service *attachment.Service) *DetailPanel {
	panel := newDetailPanel(task, session, service)
	panel.loadAttachments()
	return panel
}

func newDetailPanel(task domain.Task, session *domain.Session, service *attachment.Service) *DetailPanel {
	// Calculate contentHeight based on description.
	contentHeight := 0
	if task.Description != "" {
		contentHeight = len(strings.Split(task.Description, "\n"))
	}

	return &DetailPanel{
		task:              task,
		session:           session,
		scrollY:           0,
		contentHeight:     contentHeight,
		viewHeight:        20, // Default, will be updated in Size().
		styles:            New(),
		attachmentService: service,
		attachments:       make([]attachment.Attachment, 0),
		attachmentCursor:  0,
		openExternal:      openAttachmentInViewer,
	}
}

// Init initializes the detail panel.
func (d *DetailPanel) Init() tea.Cmd {
	return nil
}

// Update handles messages.
func (d *DetailPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return d, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if d.hasAttachmentSelection() {
				d.moveAttachmentDown()
				return d, nil
			}
			if d.scrollY < d.maxScroll() {
				d.scrollY++
			}
			return d, nil

		case "k", "up":
			if d.hasAttachmentSelection() {
				d.moveAttachmentUp()
				return d, nil
			}
			if d.scrollY > 0 {
				d.scrollY--
			}
			return d, nil

		case "g":
			// Jump to top.
			d.scrollY = 0
			return d, nil

		case "G":
			// Jump to bottom.
			d.scrollY = d.maxScroll()
			return d, nil

		case "v":
			if !d.hasAttachmentSelection() {
				return d, nil
			}
			return d, func() tea.Msg {
				return OpenImagePreviewMsg{
					IssueID:      d.task.ID,
					InitialIndex: d.attachmentCursor,
				}
			}

		case "o":
			if d.hasAttachmentSelection() {
				d.openSelectedAttachment()
			}
			return d, nil

		case "x":
			if d.hasAttachmentSelection() {
				d.deleteSelectedAttachment()
			}
			return d, nil

		case "r":
			d.loadAttachments()
			return d, nil
		}
	}

	return d, nil
}

// View renders the detail panel.
func (d *DetailPanel) View() string {
	var b strings.Builder

	// Section style for headers.
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(12).
		Align(lipgloss.Right)

	valueStyle := d.styles.MenuItem

	// Task ID and title.
	b.WriteString(headerStyle.Render(fmt.Sprintf("[%s] %s", d.task.ID, d.task.Title)))
	b.WriteString("\n\n")

	// Status, Priority, Type.
	b.WriteString(labelStyle.Render("Status:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.formatStatus(d.task.Status)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Priority:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.task.Priority.String()))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Type:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(string(d.task.Type)))
	b.WriteString("\n")

	// Parent ID if present.
	if d.task.ParentID != nil {
		b.WriteString(labelStyle.Render("Parent:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(*d.task.ParentID))
		b.WriteString("\n")
	}

	// Timestamps.
	b.WriteString(labelStyle.Render("Created:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.formatTime(d.task.CreatedAt)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Updated:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(d.formatTime(d.task.UpdatedAt)))
	b.WriteString("\n")

	// Session info if present.
	if d.session != nil {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Session"))
		b.WriteString("\n")

		b.WriteString(labelStyle.Render("State:"))
		b.WriteString("  ")
		b.WriteString(valueStyle.Render(fmt.Sprintf("%s %s", d.session.State.Icon(), string(d.session.State))))
		b.WriteString("\n")

		if d.session.StartedAt != nil {
			b.WriteString(labelStyle.Render("Started:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatTime(*d.session.StartedAt)))
			b.WriteString("\n")

			// Calculate elapsed time.
			elapsed := time.Since(*d.session.StartedAt)
			b.WriteString(labelStyle.Render("Elapsed:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.formatDuration(elapsed)))
			b.WriteString("\n")
		}

		if d.session.Worktree != "" {
			b.WriteString(labelStyle.Render("Worktree:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(d.session.Worktree))
			b.WriteString("\n")
		}

		if d.session.DevServer != nil && d.session.DevServer.Running {
			b.WriteString(labelStyle.Render("Dev Server:"))
			b.WriteString("  ")
			b.WriteString(valueStyle.Render(fmt.Sprintf(":%d (%s)", d.session.DevServer.Port, d.session.DevServer.Command)))
			b.WriteString("\n")
		}
	}

	// Attachments section.
	d.renderAttachmentSection(&b, headerStyle)

	// Description section with scrolling.
	if d.task.Description != "" {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Description"))
		b.WriteString("\n")

		// Split description into lines and apply scroll.
		descLines := strings.Split(d.task.Description, "\n")
		d.contentHeight = len(descLines)

		start := d.scrollY
		end := min(d.scrollY+d.viewHeight, len(descLines))

		for i := start; i < end; i++ {
			b.WriteString(valueStyle.Render(descLines[i]))
			b.WriteString("\n")
		}

		// Scroll indicator if needed.
		if d.maxScroll() > 0 {
			scrollInfo := d.styles.Footer.Render(
				fmt.Sprintf("[j/k to scroll, g/G to jump] (line %d/%d)", d.scrollY+1, d.contentHeight),
			)
			b.WriteString("\n")
			b.WriteString(scrollInfo)
		}
	}

	return b.String()
}

// Title returns the overlay title.
func (d *DetailPanel) Title() string {
	return "Task Details"
}

// Size returns the overlay dimensions.
func (d *DetailPanel) Size() (width, height int) {
	d.viewHeight = 15 // Description viewing area.
	return 70, 30     // Total overlay size.
}

// formatStatus formats a status for display.
func (d *DetailPanel) formatStatus(status domain.Status) string {
	switch status {
	case domain.StatusOpen:
		return "Open"
	case domain.StatusInProgress:
		return "In Progress"
	case domain.StatusBlocked:
		return "Blocked"
	case domain.StatusDone:
		return "Done"
	default:
		return string(status)
	}
}

// formatTime formats a timestamp for display.
func (d *DetailPanel) formatTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

// formatDuration formats a duration for display.
func (d *DetailPanel) formatDuration(dur time.Duration) string {
	hours := int(dur.Hours())
	minutes := int(dur.Minutes()) % 60
	seconds := int(dur.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// maxScroll returns the maximum scroll position.
func (d *DetailPanel) maxScroll() int {
	return max(0, d.contentHeight-d.viewHeight)
}

func (d *DetailPanel) renderAttachmentSection(b *strings.Builder, headerStyle lipgloss.Style) {
	if d.attachmentService == nil {
		return
	}

	b.WriteString("\n")
	b.WriteString(headerStyle.Render("Attachments"))
	b.WriteString("\n")

	if len(d.attachments) == 0 {
		b.WriteString(d.styles.Footer.Render("No attachments linked to this issue."))
		b.WriteString("\n")
	} else {
		for index, file := range d.attachments {
			lineStyle := d.styles.MenuItem
			indicator := "  "
			if index == d.attachmentCursor {
				lineStyle = d.styles.MenuItemActive
				indicator = "▶ "
			}

			corruptSuffix := ""
			if file.Corrupt {
				corruptSuffix = " [corrupt metadata]"
			}

			line := fmt.Sprintf("%s%-36s %8s%s", indicator, truncate(file.Filename, 36), formatFileSize(file.Size), corruptSuffix)
			b.WriteString(lineStyle.Render(line))
			b.WriteString("\n")
		}
	}

	if d.attachmentWarning != "" {
		warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af")).Bold(true)
		b.WriteString(warningStyle.Render("Warning: " + d.attachmentWarning))
		b.WriteString("\n")
	}

	if d.attachmentError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Bold(true)
		b.WriteString(errorStyle.Render("Error: " + d.attachmentError))
		b.WriteString("\n")
	}

	b.WriteString(d.styles.Footer.Render("j/k: select attachment • v/o/x: preview/open/remove • r: refresh"))
	b.WriteString("\n")
}

func (d *DetailPanel) hasAttachmentSelection() bool {
	return d.attachmentService != nil && len(d.attachments) > 0
}

func (d *DetailPanel) moveAttachmentDown() {
	d.attachmentCursor = min(d.attachmentCursor+1, len(d.attachments)-1)
}

func (d *DetailPanel) moveAttachmentUp() {
	d.attachmentCursor = max(0, d.attachmentCursor-1)
}

func (d *DetailPanel) currentAttachment() *attachment.Attachment {
	if !d.hasAttachmentSelection() {
		return nil
	}
	if d.attachmentCursor < 0 || d.attachmentCursor >= len(d.attachments) {
		return nil
	}
	return &d.attachments[d.attachmentCursor]
}

func (d *DetailPanel) loadAttachments() {
	d.attachmentWarning = ""
	d.attachmentError = ""
	d.attachments = d.attachments[:0]
	d.attachmentCursor = 0

	if d.attachmentService == nil {
		return
	}

	files, err := d.attachmentService.List(context.Background(), d.task.ID)
	if err != nil {
		d.attachmentError = fmt.Sprintf("failed to load attachments: %v (try Space i to refresh)", err)
		return
	}

	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Filename < files[j].Filename
	})
	d.attachments = files

	corruptCount := 0
	for _, file := range d.attachments {
		if file.Corrupt {
			corruptCount++
		}
	}
	if corruptCount > 0 {
		d.attachmentWarning = fmt.Sprintf("Corrupt attachment metadata detected (%d). Use Space i to re-attach or remove invalid files.", corruptCount)
	}
}

func (d *DetailPanel) openSelectedAttachment() {
	selected := d.currentAttachment()
	if selected == nil {
		return
	}

	if d.openExternal == nil {
		d.openExternal = openAttachmentInViewer
	}
	if err := d.openExternal(selected.Path); err != nil {
		d.attachmentError = fmt.Sprintf("failed to open attachment: %v (verify system opener availability)", err)
		return
	}
	d.attachmentError = ""
}

func (d *DetailPanel) deleteSelectedAttachment() {
	selected := d.currentAttachment()
	if selected == nil || d.attachmentService == nil {
		return
	}

	var err error
	if selected.Corrupt || strings.TrimSpace(selected.ID) == "" {
		err = d.attachmentService.DeleteByFilename(context.Background(), d.task.ID, selected.Filename)
	} else {
		err = d.attachmentService.Delete(context.Background(), d.task.ID, selected.ID)
	}

	if err != nil {
		d.attachmentError = fmt.Sprintf("failed to remove attachment: %v (use Space i to inspect attachments)", err)
		return
	}

	d.loadAttachments()
}

func openAttachmentInViewer(path string) error {
	var cmd string
	if hasCommand("xdg-open") {
		cmd = "xdg-open"
	} else if hasCommand("open") {
		cmd = "open"
	} else {
		return fmt.Errorf("no file opener found")
	}

	execCmd := exec.CommandContext(context.Background(), cmd, path)
	if err := execCmd.Start(); err != nil {
		return err
	}
	return nil
}
