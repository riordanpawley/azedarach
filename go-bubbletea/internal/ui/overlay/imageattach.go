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
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
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
	issueID     string
	service     ImageAttachmentService
	mode        imageAttachMode
	files       []attachment.Attachment
	cursor      int
	pathInput   textinput.Model
	inputActive bool
	error       string
	styles      *Styles
}

type ImageAttachmentService interface {
	List(ctx context.Context, issueID string) ([]attachment.Attachment, error)
	AttachFromClipboard(ctx context.Context, issueID string) (*attachment.Attachment, error)
	Attach(ctx context.Context, issueID, imagePath string) (*attachment.Attachment, error)
	Delete(ctx context.Context, issueID, attachmentID string) error
}

// AttachmentActionMsg is sent when an attachment action is performed
type AttachmentActionMsg struct {
	Action     string // "attached", "deleted"
	Attachment *attachment.Attachment
	Error      error
}

// OpenImagePreviewMsg is sent to open the image preview overlay
type OpenImagePreviewMsg struct {
	IssueID      string
	InitialIndex int
}

// NewImageAttachOverlay creates a new image attachment overlay
func NewImageAttachOverlay(issueID string, service ImageAttachmentService) *ImageAttachOverlay {
	ti := textinput.New()
	ti.Placeholder = "Enter file path..."
	ti.CharLimit = 500
	ti.Width = 60

	return &ImageAttachOverlay{
		issueID:     issueID,
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
				if msg.String() == "esc" {
					i.mode = imageAttachModeList
					return i, nil
				}
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

		case "p":
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

		case "d", "x":
			// Delete attachment
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				return i, i.deleteAttachment()
			}
			return i, nil

		case "enter", "v":
			// Open full image preview overlay
			if i.mode == imageAttachModeList && len(i.files) > 0 {
				return i, func() tea.Msg {
					return OpenImagePreviewMsg{
						IssueID:      i.issueID,
						InitialIndex: i.cursor,
					}
				}
			}
			return i, nil

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
		i.error = compactOverlayError(msg.err)
		return i, func() tea.Msg {
			return AttachmentActionMsg{
				Action: "error",
				Error:  msg.err,
			}
		}
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
		return renderDialogTwoPane(dialogLayoutConfig{
			styles:            i.styles,
			width:             84,
			height:            18,
			title:             "ATTACH FROM FILE",
			rightSectionTitle: "Actions",
			breakpoint:        1,
			gap:               3,
			minLeft:           44,
			minRight:          20,
			leftFocused:       true,
			renderLeft: func(mode dialogLayoutMode, width, height int) string {
				return i.renderFileInputContent()
			},
			renderRight: func(mode dialogLayoutMode, width, height int) string {
				return keybinds.RenderKeyTable([]keybinds.Binding{
					{Key: "Enter", Description: "attach"},
					{Key: "Esc", Description: "cancel"},
				}, 0, keybinds.Theme{
					KeyStyle:         i.styles.MenuKey,
					DescriptionStyle: i.styles.Footer,
					FooterStyle:      i.styles.Footer,
				})
			},
		})
	}

	switch i.mode {
	case imageAttachModeList:
		return renderDialogTwoPane(dialogLayoutConfig{
			styles:            i.styles,
			width:             88,
			height:            30,
			title:             fmt.Sprintf("ATTACHMENTS FOR %s", i.issueID),
			rightSectionTitle: "Actions",
			breakpoint:        1,
			gap:               3,
			minLeft:           50,
			minRight:          22,
			leftFocused:       true,
			renderLeft: func(mode dialogLayoutMode, width, height int) string {
				return i.renderListContent()
			},
			renderRight: func(mode dialogLayoutMode, width, height int) string {
				return i.renderListActions()
			},
		})
	case imageAttachModePreview:
		return renderDialogTwoPane(dialogLayoutConfig{
			styles:            i.styles,
			width:             88,
			height:            30,
			title:             "ATTACHMENT DETAILS",
			rightSectionTitle: "Actions",
			breakpoint:        1,
			gap:               3,
			minLeft:           50,
			minRight:          22,
			leftFocused:       true,
			renderLeft: func(mode dialogLayoutMode, width, height int) string {
				return i.renderPreviewContent()
			},
			renderRight: func(mode dialogLayoutMode, width, height int) string {
				return keybinds.RenderKeyTable([]keybinds.Binding{
					{Key: "o", Description: "open in viewer"},
					{Key: "Esc", Description: "back to list"},
				}, 0, keybinds.Theme{
					KeyStyle:         i.styles.MenuKey,
					DescriptionStyle: i.styles.Footer,
					FooterStyle:      i.styles.Footer,
				})
			},
		})
	default:
		return i.renderListContent()
	}
}

func (i *ImageAttachOverlay) renderListContent() string {
	var content strings.Builder

	if len(i.files) == 0 {
		content.WriteString(i.styles.Footer.Render("No attachments yet."))
		content.WriteString("\n")
	} else {
		for idx, file := range i.files {
			style := i.styles.MenuItem
			indicator := "  "
			if idx == i.cursor {
				style = i.styles.MenuItemActive
				indicator = "▶ "
			}
			sizeStr := formatFileSize(file.Size)
			typeStr := strings.TrimPrefix(file.MimeType, "image/")
			line := fmt.Sprintf("%s%-40s %8s  %s", indicator, truncate(file.Filename, 40), sizeStr, typeStr)
			content.WriteString(style.Render(line))
			content.WriteString("\n")
		}
	}

	if i.error != "" {
		errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8")).Bold(true)
		if content.Len() > 0 {
			content.WriteString("\n")
		}
		content.WriteString(errorStyle.Render("Error: " + i.error))
	}
	return strings.TrimRight(content.String(), "\n")
}

func (i *ImageAttachOverlay) renderListActions() string {
	hints := []keybinds.Binding{
		{Key: "p", Description: "paste from clipboard"},
		{Key: "f", Description: "attach from file"},
	}
	if len(i.files) > 0 {
		hints = append(hints,
			keybinds.Binding{Key: "o", Description: "open"},
			keybinds.Binding{Key: "d/x", Description: "delete"},
			keybinds.Binding{Key: "Enter/v", Description: "preview"},
		)
	}
	hints = append(hints, keybinds.Binding{Key: "Esc", Description: "close"})
	return keybinds.RenderKeyTable(hints, 0, keybinds.Theme{
		KeyStyle:         i.styles.MenuKey,
		DescriptionStyle: i.styles.Footer,
		FooterStyle:      i.styles.Footer,
	})
}

func (i *ImageAttachOverlay) renderPreviewContent() string {
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

	return strings.TrimRight(b.String(), "\n")
}

func (i *ImageAttachOverlay) renderFileInputContent() string {
	var b strings.Builder

	b.WriteString(i.pathInput.View())
	return strings.TrimRight(b.String(), "\n")
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
		files, err := i.service.List(ctx, i.issueID)
		if err != nil {
			return errorMsg{err}
		}
		return attachmentsLoadedMsg{attachments: files}
	}
}

func (i *ImageAttachOverlay) pasteFromClipboard() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		attachment, err := i.service.AttachFromClipboard(ctx, i.issueID)
		if err != nil {
			return errorMsg{err}
		}
		return attachmentAddedMsg{attachment: attachment}
	}
}

func (i *ImageAttachOverlay) attachFromFile(path string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		attachment, err := i.service.Attach(ctx, i.issueID, path)
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
		err := i.service.Delete(ctx, i.issueID, file.ID)
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

func compactOverlayError(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
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
