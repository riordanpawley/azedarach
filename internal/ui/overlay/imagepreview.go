package overlay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// ImagePreviewOverlay displays and manages issue attachments with navigation.
type ImagePreviewOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	issueID            string
	service            *attachment.Service
	images             []attachment.Attachment
	currentIndex       int
	confirmDelete      bool
	error              string
	styles             *Styles
	markdownAttachment string
	markdownRendered   string
	markdownScroll     int
}

// ImageDeletedMsg is sent when an image is deleted
type ImageDeletedMsg struct {
	AttachmentID string
	Error        error
}

// NewImagePreviewOverlay creates a new attachment preview overlay.
func NewImagePreviewOverlay(issueID string, service *attachment.Service, initialIndex int) *ImagePreviewOverlay {
	return &ImagePreviewOverlay{
		issueID:       issueID,
		service:       service,
		currentIndex:  initialIndex,
		confirmDelete: false,
		styles:        New(),
	}
}

// Init initializes the overlay and loads images
func (i *ImagePreviewOverlay) Init() tea.Cmd {
	return i.loadImages()
}

// Update handles messages
func (i *ImagePreviewOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if i.confirmDelete {
			return i.handleConfirmMode(msg)
		}
		return i.handleNormalMode(msg)
	case tea.WindowSizeMsg:
		i.ApplyWindowSize(msg)
		return i, nil

	case imagesLoadedMsg:
		i.images = msg.images
		i.error = ""
		// Clamp current index to valid range
		if i.currentIndex >= len(i.images) && len(i.images) > 0 {
			i.currentIndex = len(i.images) - 1
		}
		if i.currentIndex < 0 && len(i.images) > 0 {
			i.currentIndex = 0
		}
		i.markdownScroll = 0
		return i, i.loadCurrentMarkdown()

	case imageDeletedMsg:
		i.error = ""
		i.confirmDelete = false
		// Reload images after deletion
		return i, tea.Batch(
			i.loadImages(),
			func() tea.Msg {
				return ImageDeletedMsg{
					AttachmentID: msg.attachmentID,
					Error:        nil,
				}
			},
		)

	case imagePreviewErrorMsg:
		i.error = msg.err.Error()
		i.confirmDelete = false
		return i, nil

	case markdownPreviewLoadedMsg:
		if msg.attachmentID != i.currentAttachmentID() {
			return i, nil
		}
		i.markdownAttachment = msg.attachmentID
		i.markdownRendered = msg.rendered
		i.error = ""
		if msg.err != nil {
			i.error = compactOverlayError(msg.err)
		}
		return i, nil
	}

	return i, nil
}

// handleNormalMode handles key presses in normal mode
func (i *ImagePreviewOverlay) handleNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return i, func() tea.Msg { return CloseOverlayMsg{} }

	case "h", "left":
		// Previous image
		if len(i.images) > 0 && i.currentIndex > 0 {
			i.currentIndex--
			i.markdownScroll = 0
			return i, i.loadCurrentMarkdown()
		}
		return i, nil

	case "l", "right":
		// Next image
		if len(i.images) > 0 && i.currentIndex < len(i.images)-1 {
			i.currentIndex++
			i.markdownScroll = 0
			return i, i.loadCurrentMarkdown()
		}
		return i, nil

	case "j", "down":
		if i.currentIsMarkdown() {
			i.markdownScroll++
		}
		return i, nil

	case "k", "up":
		if i.currentIsMarkdown() && i.markdownScroll > 0 {
			i.markdownScroll--
		}
		return i, nil

	case "pgdown", "ctrl+f":
		if i.currentIsMarkdown() {
			i.markdownScroll += 10
		}
		return i, nil

	case "pgup", "ctrl+b":
		if i.currentIsMarkdown() {
			i.markdownScroll = max(0, i.markdownScroll-10)
		}
		return i, nil

	case "g":
		// Go to first image
		if len(i.images) > 0 {
			i.currentIndex = 0
			i.markdownScroll = 0
			return i, i.loadCurrentMarkdown()
		}
		return i, nil

	case "G":
		// Go to last image
		if len(i.images) > 0 {
			i.currentIndex = len(i.images) - 1
			i.markdownScroll = 0
			return i, i.loadCurrentMarkdown()
		}
		return i, nil

	case "o":
		// Open in external viewer
		if len(i.images) > 0 {
			return i, i.openInViewer()
		}
		return i, nil

	case "d":
		// Delete current image (show confirmation)
		if len(i.images) > 0 {
			i.confirmDelete = true
		}
		return i, nil

	case "r":
		// Refresh list
		return i, i.loadImages()
	}

	return i, nil
}

// handleConfirmMode handles key presses in delete confirmation mode
func (i *ImagePreviewOverlay) handleConfirmMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		// Yes - delete image
		i.confirmDelete = false
		return i, i.deleteCurrentImage()

	case "n", "N", "esc":
		// No - cancel
		i.confirmDelete = false
		return i, nil
	}

	return i, nil
}

// View renders the overlay
func (i *ImagePreviewOverlay) View() string {
	width, height := i.Size()
	if i.confirmDelete {
		return renderDialogTwoPane(dialogLayoutConfig{
			styles:            i.styles,
			width:             width,
			height:            height,
			title:             "Confirm Delete",
			rightSectionTitle: "Actions",
			breakpoint:        76,
			gap:               3,
			minLeft:           38,
			minRight:          20,
			leftFocused:       true,
			renderLeft: func(mode dialogLayoutMode, width, height int) string {
				return i.renderDeleteConfirmationContent()
			},
			renderRight: func(mode dialogLayoutMode, width, height int) string {
				return strings.Join([]string{
					"[Y] Yes, delete",
					"[N] No, cancel",
					"Esc Cancel",
				}, "\n")
			},
		})
	}
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            i.styles,
		width:             width,
		height:            height,
		title:             "Attachment Preview",
		rightSectionTitle: "Actions",
		breakpoint:        86,
		gap:               3,
		minLeft:           46,
		minRight:          22,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return i.renderPreviewContent(width, height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return i.renderPreviewActions(width)
		},
	})
}

func (i *ImagePreviewOverlay) renderPreviewContent(width, height int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(14).
		Align(lipgloss.Right)

	valueStyle := i.styles.MenuItem

	// Header with navigation info
	if len(i.images) == 0 {
		b.WriteString(i.styles.Footer.Render("No attachments on this issue."))
		return b.String()
	}

	// Header with position indicator
	position := fmt.Sprintf("Attachment %d/%d", i.currentIndex+1, len(i.images))
	header := fmt.Sprintf("Attachment Preview - %s", position)
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n\n")

	// Current image details
	img := i.images[i.currentIndex]
	if attachment.IsMarkdown(img) {
		return i.renderMarkdownPreviewContent(img, width, height, headerStyle, labelStyle, valueStyle, position)
	}

	b.WriteString(labelStyle.Render("Filename:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(img.Filename))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("MIME Type:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(img.MimeType))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Size:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(formatFileSize(img.Size)))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Created:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(img.Created.Format("2006-01-02 15:04:05")))
	b.WriteString("\n")

	b.WriteString(labelStyle.Render("Path:"))
	b.WriteString("  ")
	pathValue := img.Path
	if len(pathValue) > 55 {
		pathValue = "..." + pathValue[len(pathValue)-52:]
	}
	b.WriteString(valueStyle.Render(pathValue))
	b.WriteString("\n\n")

	// Navigation indicator
	if len(i.images) > 1 {
		navStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#94e2d5"))

		var nav strings.Builder
		nav.WriteString("[")
		for idx := range i.images {
			if idx == i.currentIndex {
				nav.WriteString("●")
			} else {
				nav.WriteString("○")
			}
			if idx < len(i.images)-1 {
				nav.WriteString(" ")
			}
		}
		nav.WriteString("]")

		b.WriteString(navStyle.Render(nav.String()))
		b.WriteString("\n\n")
	}

	// Error display
	if i.error != "" {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f38ba8")).
			Bold(true)
		b.WriteString(errorStyle.Render("Error: " + i.error))
		b.WriteString("\n\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

func (i *ImagePreviewOverlay) renderMarkdownPreviewContent(
	file attachment.Attachment,
	width, height int,
	headerStyle, labelStyle, valueStyle lipgloss.Style,
	position string,
) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Markdown Report - " + position))
	b.WriteString("\n\n")
	b.WriteString(labelStyle.Render("Filename:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(file.Filename))
	b.WriteString("\n")
	b.WriteString(labelStyle.Render("Size:"))
	b.WriteString("  ")
	b.WriteString(valueStyle.Render(formatFileSize(file.Size)))
	b.WriteString("\n\n")

	rendered := strings.TrimRight(i.markdownRendered, "\n")
	if i.markdownAttachment != file.ID {
		rendered = i.styles.Footer.Render("Loading Markdown preview...")
	} else if i.error != "" {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f38ba8")).
			Bold(true)
		rendered = errorStyle.Render("Error: " + i.error)
	}
	if rendered == "" && i.markdownAttachment == file.ID {
		rendered = i.styles.Footer.Render("Markdown document is empty.")
	}

	lines := strings.Split(rendered, "\n")
	visibleHeight := max(1, height-8)
	maxScroll := max(0, len(lines)-visibleHeight)
	scroll := i.markdownScroll
	if scroll > maxScroll {
		scroll = maxScroll
	}
	start := max(0, scroll)
	end := min(len(lines), start+visibleHeight)
	if start < end {
		b.WriteString(strings.Join(lines[start:end], "\n"))
	}
	if maxScroll > 0 {
		b.WriteString("\n")
		scrollLine := fmt.Sprintf("Lines %d-%d/%d", start+1, end, len(lines))
		if width > 0 {
			scrollLine = truncate(scrollLine, max(8, width-2))
		}
		b.WriteString(i.styles.Footer.Render(scrollLine))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (i *ImagePreviewOverlay) renderPreviewActions(width int) string {
	hints := make([]keybinds.Binding, 0, 6)
	if len(i.images) > 1 {
		hints = append(hints,
			keybinds.Binding{Key: "h/l", Description: "Navigate"},
			keybinds.Binding{Key: "g/G", Description: "First/Last"},
		)
	}
	if i.currentIsMarkdown() {
		hints = append(hints, keybinds.Binding{Key: "j/k", Description: "Scroll report"})
	}
	hints = append(hints,
		keybinds.Binding{Key: "o", Description: "Open"},
		keybinds.Binding{Key: "d", Description: "Delete"},
		keybinds.Binding{Key: "r", Description: "Refresh"},
		keybinds.Binding{Key: "Esc", Description: "Close"},
	)
	return renderDialogActions(i.styles, hints, width)
}

func (i *ImagePreviewOverlay) renderDeleteConfirmationContent() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f38ba8")).
		Bold(true)

	b.WriteString(headerStyle.Render("⚠ Confirm Delete"))
	b.WriteString("\n")

	if i.currentIndex >= 0 && i.currentIndex < len(i.images) {
		img := i.images[i.currentIndex]
		b.WriteString(i.styles.MenuItem.Render(fmt.Sprintf("Delete attachment: %s?", img.Filename)))
		b.WriteString("\n\n")
		b.WriteString(i.styles.Footer.Render(fmt.Sprintf("Size: %s", formatFileSize(img.Size))))
		b.WriteString("\n")
	}

	b.WriteString(i.styles.MenuItem.Render("This action cannot be undone."))
	b.WriteString("\n")
	b.WriteString(i.styles.MenuItemActive.Render("[Y] Yes, delete"))
	b.WriteString("\n")
	b.WriteString(i.styles.MenuItem.Render("[N] No, cancel"))
	return strings.TrimRight(b.String(), "\n")
}

// Title returns the overlay title
func (i *ImagePreviewOverlay) Title() string {
	return "Attachment Preview"
}

// Size returns the overlay dimensions
func (i *ImagePreviewOverlay) Size() (width, height int) {
	if i.confirmDelete {
		return i.Clamp(72, 22)
	}
	return i.Clamp(82, 28)
}

// Commands

type imagesLoadedMsg struct {
	images []attachment.Attachment
}

type imageDeletedMsg struct {
	attachmentID string
}

type imagePreviewErrorMsg struct {
	err error
}

type markdownPreviewLoadedMsg struct {
	attachmentID string
	rendered     string
	err          error
}

func (i *ImagePreviewOverlay) loadImages() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		images, err := i.service.List(ctx, i.issueID)
		if err != nil {
			return imagePreviewErrorMsg{err: err}
		}
		return imagesLoadedMsg{images: images}
	}
}

func (i *ImagePreviewOverlay) loadCurrentMarkdown() tea.Cmd {
	if !i.currentIsMarkdown() {
		i.markdownAttachment = ""
		i.markdownRendered = ""
		return nil
	}
	file := i.images[i.currentIndex]
	width, _ := i.Size()
	wrap := max(24, width-12)
	return func() tea.Msg {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return markdownPreviewLoadedMsg{attachmentID: file.ID, err: fmt.Errorf("read markdown: %w", err)}
		}
		rendered, err := renderMarkdownDocument(context.Background(), string(data), wrap)
		if err != nil {
			return markdownPreviewLoadedMsg{attachmentID: file.ID, err: fmt.Errorf("render markdown: %w", err)}
		}
		return markdownPreviewLoadedMsg{attachmentID: file.ID, rendered: rendered}
	}
}

func (i *ImagePreviewOverlay) currentIsMarkdown() bool {
	if i.currentIndex < 0 || i.currentIndex >= len(i.images) {
		return false
	}
	return attachment.IsMarkdown(i.images[i.currentIndex])
}

func (i *ImagePreviewOverlay) currentAttachmentID() string {
	if i.currentIndex < 0 || i.currentIndex >= len(i.images) {
		return ""
	}
	return i.images[i.currentIndex].ID
}

func (i *ImagePreviewOverlay) deleteCurrentImage() tea.Cmd {
	if i.currentIndex < 0 || i.currentIndex >= len(i.images) {
		return nil
	}

	img := i.images[i.currentIndex]
	return func() tea.Msg {
		ctx := context.Background()
		err := i.service.Delete(ctx, i.issueID, img.ID)
		if err != nil {
			return imagePreviewErrorMsg{err: err}
		}
		return imageDeletedMsg{attachmentID: img.ID}
	}
}

func (i *ImagePreviewOverlay) openInViewer() tea.Cmd {
	if i.currentIndex < 0 || i.currentIndex >= len(i.images) {
		return nil
	}

	img := i.images[i.currentIndex]
	return func() tea.Msg {
		// Use xdg-open on Linux, open on macOS
		var cmd string
		if hasCommand("xdg-open") {
			cmd = "xdg-open"
		} else if hasCommand("open") {
			cmd = "open"
		} else {
			return imagePreviewErrorMsg{err: fmt.Errorf("no file opener found")}
		}

		ctx := context.Background()
		ctx, endSpan := latencytrace.StartSpan(ctx, "dependency", "file_viewer",
			"dependency.name", cmd,
			"dependency.operation", "open_attachment",
			"arg_count", 1,
		)
		var spanErr error
		defer func() { endSpan(spanErr) }()
		execCmd := exec.CommandContext(ctx, cmd, img.Path)
		if err := execCmd.Start(); err != nil {
			spanErr = err
			return imagePreviewErrorMsg{err: err}
		}

		// Wait a bit to ensure the viewer opens
		time.Sleep(100 * time.Millisecond)
		return nil
	}
}
