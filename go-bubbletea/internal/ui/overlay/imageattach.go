package overlay

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

// imageAttachMode represents the current mode of the overlay
type imageAttachMode int

const (
	imageAttachModeList imageAttachMode = iota
	imageAttachModeAttach
	imageAttachModePreview
)

// ImageAttachOverlay manages image attachments for a task
type ImageAttachOverlay struct {
	beadID      string
	service     *attachment.Service
	mode        imageAttachMode
	files       []attachment.Attachment
	cursor      int
	pathInput   textinput.Model
	inputActive bool
	error       string
	styles      *Styles
}

// AttachmentActionMsg is sent when an attachment action is performed
type AttachmentActionMsg struct {
	Action     string // "attached", "deleted"
	Attachment *attachment.Attachment
}

// OpenImagePreviewMsg is sent to open the image preview overlay
type OpenImagePreviewMsg struct {
	BeadID       string
	InitialIndex int
}

// NewImageAttachOverlay creates a new image attachment overlay
func NewImageAttachOverlay(beadID string, service *attachment.Service) *ImageAttachOverlay {
	ti := textinput.New()
	ti.Placeholder = "Enter file path..."
	ti.CharLimit = 500
	ti.Width = 60

	return &ImageAttachOverlay{
		beadID:      beadID,
		service:     service,
		mode:        imageAttachModeList,
		cursor:      0,
		pathInput:   ti,
		inputActive: false,
		styles:      New(),
	}
}

// Init initializes the overlay and loads attachments
func (i *ImageAttachOverlay) Init() tea.Cmd {
	return i.loadAttachments()
}

// Update handles messages
func (i *ImageAttachOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle input mode separately
		if i.inputActive {
			return i.handleInputMode(msg)
		}

		switch msg.String() {
		case "esc", "q":
			if i.mode == imageAttachModePreview {
				i.mode = imageAttachModeList
				return i, nil
			}
			return i, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				i.cursor = min(i.cursor+1, len(i.files)-1)
			}
			return i, nil

		case "k", "up":
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				i.cursor = max(0, i.cursor-1)
			}
			return i, nil

		case "v":
			// Paste from clipboard
			return i, i.pasteFromClipboard()

		case "f":
			// Attach from file path
			i.inputActive = true
			i.pathInput.Focus()
			i.error = ""
			return i, textinput.Blink

		case "o":
			// Open in external viewer
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				return i, i.openInViewer()
			}
			return i, nil

		case "d":
			// Delete attachment
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				return i, i.deleteAttachment()
			}
			return i, nil

		case "enter", "p":
			// Open full image preview overlay
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				return i, func() tea.Msg {
					return OpenImagePreviewMsg{
						BeadID:       i.beadID,
						InitialIndex: i.cursor,
					}
				}
			}
			return i, nil

		case "r":
			// Refresh list
			return i, i.loadAttachments()
		}

	case attachmentsLoadedMsg:
		i.files = msg.attachments
		i.error = ""
		if i.cursor >= len(i.files) && len(i.files) > 0 {
			i.cursor = len(i.files) - 1
		}
		return i, nil

	case attachmentAddedMsg:
		i.error = ""
		// Reload attachments
		return i, tea.Batch(
			i.loadAttachments(),
			func() tea.Msg {
				return AttachmentActionMsg{
					Action:     "attached",
					Attachment: msg.attachment,
				}
			},
		)

	case attachmentDeletedMsg:
		i.error = ""
		// Reload attachments
		return i, i.loadAttachments()

	case errorMsg:
		i.error = msg.err.Error()
		return i, nil
	}

	return i, nil
}

// handleInputMode handles key presses when input is active
func (i *ImageAttachOverlay) handleInputMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		i.inputActive = false
		i.pathInput.Blur()
		i.pathInput.SetValue("")
		return i, nil

	case "enter":
		path := strings.TrimSpace(i.pathInput.Value())
		if path != "" {
			i.inputActive = false
			i.pathInput.Blur()
			i.pathInput.SetValue("")
			return i, i.attachFromFile(path)
		}
		return i, nil
	}

	var cmd tea.Cmd
	i.pathInput, cmd = i.pathInput.Update(msg)
	return i, cmd
}

// View renders the overlay
func (i *ImageAttachOverlay) View() string {
	if i.inputActive {
		return i.renderFileInput()
	}

	switch i.mode {
	case imageAttachModeList:
		return i.renderList()
	case imageAttachModePreview:
		return i.renderPreview()
	default:
		return i.renderList()
	}
}

// renderList renders the attachment list
func (i *ImageAttachOverlay) renderList() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	b.WriteString(headerStyle.Render(fmt.Sprintf("Attachments for %s", i.beadID)))
	b.WriteString("\n\n")

	if len(i.files) == 0 {
		b.WriteString(i.styles.Footer.Render("No attachments yet."))
		b.WriteString("\n\n")
	} else {
		// Render file list
		for idx, file := range i.files {
			style := i.styles.MenuItem
			indicator := "  "
			if idx == i.cursor {
				style = i.styles.MenuItemActive
				indicator = "▶ "
			}

			// Format size
			sizeStr := formatFileSize(file.Size)
			typeStr := strings.TrimPrefix(file.MimeType, "image/")

			line := fmt.Sprintf("%s%-40s %8s  %s", indicator, truncate(file.Filename, 40), sizeStr, typeStr)
			b.WriteString(style.Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Error display
	if i.error != "" {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f38ba8")).
			Bold(true)
		b.WriteString(errorStyle.Render("Error: " + i.error))
		b.WriteString("\n\n")
	}

	// Separator
	b.WriteString(i.styles.Separator.Render(strings.Repeat("─", 70)))
	b.WriteString("\n\n")

	// Help text
	hints := []string{
		i.styles.MenuKey.Render("p/v") + " " + i.styles.Footer.Render("Paste from clipboard"),
		i.styles.MenuKey.Render("f") + " " + i.styles.Footer.Render("Attach from file"),
	}
	if len(i.files) > 0 {
		hints = append(hints,
			i.styles.MenuKey.Render("o") + " " + i.styles.Footer.Render("Open"),
			i.styles.MenuKey.Render("d") + " " + i.styles.Footer.Render("Delete"),
			i.styles.MenuKey.Render("Enter") + " " + i.styles.Footer.Render("Preview"),
		)
	}
	hints = append(hints,
		i.styles.MenuKey.Render("r") + " " + i.styles.Footer.Render("Refresh"),
		i.styles.MenuKey.Render("Esc") + " " + i.styles.Footer.Render("Close"),
	)

	b.WriteString(i.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderPreview renders attachment details
func (i *ImageAttachOverlay) renderPreview() string {
	var b strings.Builder

	if i.cursor >= len(i.files) {
		return "No attachment selected"
	}

	file := i.files[i.cursor]

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(12).
		Align(lipgloss.Right)

	valueStyle := i.styles.MenuItem

	b.WriteString(headerStyle.Render("Attachment Details"))
	b.WriteString("\n\n")

	b.WriteString(labelStyle.Render("ID:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(file.ID))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Filename:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(file.Filename))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("MIME Type:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(file.MimeType))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Size:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(formatFileSize(file.Size)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Created:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(file.Created.Format("2006-01-02 15:04:05")))
	b.WriteString("\n\n")

	b.WriteString(labelStyle.Render("Path:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(file.Path))
	b.WriteString("\n\n")

	// Separator
	b.WriteString(i.styles.Separator.Render(strings.Repeat("─", 70)))
	b.WriteString("\n\n")

	// Help
	hints := []string{
		i.styles.MenuKey.Render("o") + " " + i.styles.Footer.Render("Open in viewer"),
		i.styles.MenuKey.Render("Esc") + " " + i.styles.Footer.Render("Back to list"),
	}
	b.WriteString(i.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderFileInput renders the file path input screen
func (i *ImageAttachOverlay) renderFileInput() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	b.WriteString(headerStyle.Render("Attach from File"))
	b.WriteString("\n\n")

	b.WriteString(i.pathInput.View())
	b.WriteString("\n\n")

	// Help
	hints := []string{
		i.styles.MenuKey.Render("Enter") + " " + i.styles.Footer.Render("Attach"),
		i.styles.MenuKey.Render("Esc") + " " + i.styles.Footer.Render("Cancel"),
	}
	b.WriteString(i.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// Title returns the overlay title
func (i *ImageAttachOverlay) Title() string {
	return "Image Attachments"
}

// Size returns the overlay dimensions
func (i *ImageAttachOverlay) Size() (width, height int) {
	return 80, 30
}

// Commands

type attachmentsLoadedMsg struct {
	attachments []attachment.Attachment
}

type attachmentAddedMsg struct {
	attachment *attachment.Attachment
}

type attachmentDeletedMsg struct{}

type errorMsg struct {
	err error
}

func (i *ImageAttachOverlay) loadAttachments() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		files, err := i.service.List(ctx, i.beadID)
		if err != nil {
			return errorMsg{err}
		}
		return attachmentsLoadedMsg{attachments: files}
	}
}

func (i *ImageAttachOverlay) pasteFromClipboard() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		attachment, err := i.service.AttachFromClipboard(ctx, i.beadID)
		if err != nil {
			return errorMsg{err}
		}
		return attachmentAddedMsg{attachment: attachment}
	}
}

func (i *ImageAttachOverlay) attachFromFile(path string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		attachment, err := i.service.Attach(ctx, i.beadID, path)
		if err != nil {
			return errorMsg{err}
		}
		return attachmentAddedMsg{attachment: attachment}
	}
}

func (i *ImageAttachOverlay) deleteAttachment() tea.Cmd {
	if i.cursor >= len(i.files) {
		return nil
	}

	file := i.files[i.cursor]
	return func() tea.Msg {
		ctx := context.Background()
		err := i.service.Delete(ctx, i.beadID, file.ID)
		if err != nil {
			return errorMsg{err}
		}
		return attachmentDeletedMsg{}
	}
}

func (i *ImageAttachOverlay) openInViewer() tea.Cmd {
	if i.cursor >= len(i.files) {
		return nil
	}

	file := i.files[i.cursor]
	return func() tea.Msg {
		// Use xdg-open on Linux, open on macOS
		var cmd string
		if hasCommand("xdg-open") {
			cmd = "xdg-open"
		} else if hasCommand("open") {
			cmd = "open"
		} else {
			return errorMsg{err: fmt.Errorf("no file opener found")}
		}

		ctx := context.Background()
		execCmd := exec.CommandContext(ctx, cmd, file.Path)
		if err := execCmd.Start(); err != nil {
			return errorMsg{err}
		}
		return nil
	}
}

// Helper functions

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func formatFileSize(size int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", float64(size)/float64(GB))
	case size >= MB:
		return fmt.Sprintf("%.2f MB", float64(size)/float64(MB))
	case size >= KB:
		return fmt.Sprintf("%.2f KB", float64(size)/float64(KB))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
