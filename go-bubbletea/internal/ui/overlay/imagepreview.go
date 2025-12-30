package overlay

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/services/attachment"
)

// ImagePreviewOverlay displays and manages image attachments with navigation
type ImagePreviewOverlay struct {
	beadID        string
	service       *attachment.Service
	images        []attachment.Attachment
	currentIndex  int
	confirmDelete bool
	error         string
	styles        *Styles
}

// ImageDeletedMsg is sent when an image is deleted
type ImageDeletedMsg struct {
	AttachmentID string
	Error        error
}

// NewImagePreviewOverlay creates a new image preview overlay
func NewImagePreviewOverlay(beadID string, service *attachment.Service, initialIndex int) *ImagePreviewOverlay {
	return &ImagePreviewOverlay{
		beadID:        beadID,
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
		return i, nil

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
		}
		return i, nil

	case "l", "right":
		// Next image
		if len(i.images) > 0 && i.currentIndex < len(i.images)-1 {
			i.currentIndex++
		}
		return i, nil

	case "g":
		// Go to first image
		if len(i.images) > 0 {
			i.currentIndex = 0
		}
		return i, nil

	case "G":
		// Go to last image
		if len(i.images) > 0 {
			i.currentIndex = len(i.images) - 1
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
	if i.confirmDelete {
		return i.renderDeleteConfirmation()
	}
	return i.renderPreview()
}

// renderPreview renders the image preview screen
func (i *ImagePreviewOverlay) renderPreview() string {
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
		b.WriteString(headerStyle.Render("Image Preview"))
		b.WriteString("\n\n")
		b.WriteString(i.styles.Footer.Render("No images attached to this task."))
		b.WriteString("\n\n")
		b.WriteString(i.styles.Separator.Render(strings.Repeat("─", 70)))
		b.WriteString("\n\n")
		b.WriteString(i.styles.Footer.Render(i.styles.MenuKey.Render("Esc") + " Close"))
		return b.String()
	}

	// Header with position indicator
	position := fmt.Sprintf("Image %d/%d", i.currentIndex+1, len(i.images))
	header := fmt.Sprintf("Image Preview - %s", position)
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n\n")

	// Current image details
	img := i.images[i.currentIndex]

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

	// Separator
	b.WriteString(i.styles.Separator.Render(strings.Repeat("─", 70)))
	b.WriteString("\n\n")

	// Help text
	hints := []string{}
	if len(i.images) > 1 {
		hints = append(hints,
			i.styles.MenuKey.Render("h/l") + " " + i.styles.Footer.Render("Navigate"),
			i.styles.MenuKey.Render("g/G") + " " + i.styles.Footer.Render("First/Last"),
		)
	}
	hints = append(hints,
		i.styles.MenuKey.Render("o") + " " + i.styles.Footer.Render("Open"),
		i.styles.MenuKey.Render("d") + " " + i.styles.Footer.Render("Delete"),
		i.styles.MenuKey.Render("r") + " " + i.styles.Footer.Render("Refresh"),
		i.styles.MenuKey.Render("Esc") + " " + i.styles.Footer.Render("Close"),
	)

	b.WriteString(i.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderDeleteConfirmation renders the delete confirmation dialog
func (i *ImagePreviewOverlay) renderDeleteConfirmation() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f38ba8")).
		Bold(true)

	b.WriteString(headerStyle.Render("⚠ Confirm Delete"))
	b.WriteString("\n\n")

	if i.currentIndex >= 0 && i.currentIndex < len(i.images) {
		img := i.images[i.currentIndex]
		b.WriteString(i.styles.MenuItem.Render(fmt.Sprintf("Delete image: %s?", img.Filename)))
		b.WriteString("\n\n")
		b.WriteString(i.styles.Footer.Render(fmt.Sprintf("Size: %s", formatFileSize(img.Size))))
		b.WriteString("\n\n")
	}

	b.WriteString(i.styles.MenuItem.Render("This action cannot be undone."))
	b.WriteString("\n\n")

	// Buttons
	yesStyle := i.styles.MenuItemActive
	noStyle := i.styles.MenuItem

	yes := yesStyle.Render("[Y] Yes, delete")
	no := noStyle.Render("[N] No, cancel")

	buttons := yes + "    " + no
	b.WriteString(buttons)
	b.WriteString("\n\n")

	// Footer hint
	footer := i.styles.Footer.Render("Y: Delete • N/Esc: Cancel")
	b.WriteString(footer)

	return b.String()
}

// Title returns the overlay title
func (i *ImagePreviewOverlay) Title() string {
	return "Image Preview"
}

// Size returns the overlay dimensions
func (i *ImagePreviewOverlay) Size() (width, height int) {
	if i.confirmDelete {
		return 60, 15
	}
	return 75, 25
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

func (i *ImagePreviewOverlay) loadImages() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		images, err := i.service.List(ctx, i.beadID)
		if err != nil {
			return imagePreviewErrorMsg{err: err}
		}
		return imagesLoadedMsg{images: images}
	}
}

func (i *ImagePreviewOverlay) deleteCurrentImage() tea.Cmd {
	if i.currentIndex < 0 || i.currentIndex >= len(i.images) {
		return nil
	}

	img := i.images[i.currentIndex]
	return func() tea.Msg {
		ctx := context.Background()
		err := i.service.Delete(ctx, i.beadID, img.ID)
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
		execCmd := exec.CommandContext(ctx, cmd, img.Path)
		if err := execCmd.Start(); err != nil {
			return imagePreviewErrorMsg{err: err}
		}

		// Wait a bit to ensure the viewer opens
		time.Sleep(100 * time.Millisecond)
		return nil
	}
}
