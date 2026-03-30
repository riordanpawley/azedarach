package diff

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type DiffClient interface {
	ChangedFiles(ctx context.Context, worktree, baseBranch string) ([]gitservice.ChangedFile, error)
	MergeBase(ctx context.Context, worktree, baseBranch string) (string, error)
}

type PopupOpener func(ctx context.Context, title, command string) error

type loadChangedFilesMsg struct {
	Files []gitservice.ChangedFile
	Err   error
}

type popupResultMsg struct {
	Err error
}

// DiffViewer displays changed files and opens difftastic popups for selected diffs.
type DiffViewer struct {
	worktree    string
	baseBranch  string
	gitClient   DiffClient
	openPopup   PopupOpener
	files       []gitservice.ChangedFile
	cursor      int
	scrollY     int
	styles      *Styles
	viewHeight  int
	loading     bool
	err         error
	popupStatus string
}

// NewDiffViewer creates a new diff viewer for the specified worktree.
func NewDiffViewer(worktree, baseBranch string, gitClient DiffClient, openPopup PopupOpener) *DiffViewer {
	return &DiffViewer{
		worktree:   strings.TrimSpace(worktree),
		baseBranch: strings.TrimSpace(baseBranch),
		gitClient:  gitClient,
		openPopup:  openPopup,
		files:      []gitservice.ChangedFile{},
		cursor:     0,
		scrollY:    0,
		styles:     New(),
		viewHeight: 20,
	}
}

func (d *DiffViewer) loadChangedFilesCmd() tea.Cmd {
	return func() tea.Msg {
		if d.gitClient == nil {
			return loadChangedFilesMsg{Err: fmt.Errorf("git client unavailable")}
		}
		files, err := d.gitClient.ChangedFiles(context.Background(), d.worktree, d.effectiveBaseBranch())
		return loadChangedFilesMsg{Files: files, Err: err}
	}
}

func (d *DiffViewer) openSelectedDiffCmd() tea.Cmd {
	if d.cursor < 0 || d.cursor >= len(d.files) {
		return nil
	}
	filePath := strings.TrimSpace(d.files[d.cursor].Path)
	if filePath == "" {
		return nil
	}
	return d.openPopupCmd(filePath, false)
}

func (d *DiffViewer) openPopupCmd(filePath string, all bool) tea.Cmd {
	return func() tea.Msg {
		if d.gitClient == nil {
			return popupResultMsg{Err: fmt.Errorf("git client unavailable")}
		}
		if d.openPopup == nil {
			return popupResultMsg{Err: fmt.Errorf("diff popup unavailable")}
		}

		mergeBase, err := d.gitClient.MergeBase(context.Background(), d.worktree, d.effectiveBaseBranch())
		if err != nil {
			return popupResultMsg{Err: err}
		}

		var title string
		var command string
		if all {
			title = " All Changes "
			command = fmt.Sprintf(
				"git diff %s --stat --color=always -- ':^.azedarach' && echo \"\" && DFT_COLOR=always GIT_EXTERNAL_DIFF=\"difft --display=side-by-side\" git diff %s -- ':^.azedarach' | less -RS",
				shellSingleQuote(mergeBase),
				shellSingleQuote(mergeBase),
			)
		} else {
			title = " " + filePath + " "
			command = fmt.Sprintf(
				"DFT_COLOR=always GIT_EXTERNAL_DIFF=\"difft --display=side-by-side\" git diff %s -- %s | less -RS",
				shellSingleQuote(mergeBase),
				shellSingleQuote(filePath),
			)
		}

		if err := d.openPopup(context.Background(), title, command); err != nil {
			return popupResultMsg{Err: err}
		}
		return popupResultMsg{}
	}
}

func (d *DiffViewer) effectiveBaseBranch() string {
	baseBranch := strings.TrimSpace(d.baseBranch)
	if baseBranch == "" {
		return "main"
	}
	return baseBranch
}

func (d *DiffViewer) Init() tea.Cmd {
	d.loading = true
	return d.loadChangedFilesCmd()
}

func (d *DiffViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loadChangedFilesMsg:
		d.loading = false
		d.cursor = 0
		d.scrollY = 0
		d.files = nil
		d.popupStatus = ""
		if msg.Err != nil || msg.Files == nil {
			if msg.Err != nil {
				d.err = msg.Err
			} else {
				d.err = fmt.Errorf("failed to load changed files")
			}
			return d, nil
		}
		d.err = nil
		d.files = msg.Files
		return d, nil

	case popupResultMsg:
		if msg.Err != nil {
			d.popupStatus = "Popup error: " + msg.Err.Error()
		} else {
			d.popupStatus = "Opened diff popup"
		}
		return d, nil

	case tea.KeyMsg:
		if d.loading {
			return d, nil
		}

		switch msg.String() {
		case "esc", "q":
			return d, func() tea.Msg { return overlay.CloseOverlayMsg{} }

		case "j", "down":
			if d.cursor < len(d.files)-1 {
				d.cursor++
				d.ensureCursorVisible()
			}
			return d, nil

		case "k", "up":
			if d.cursor > 0 {
				d.cursor--
				d.ensureCursorVisible()
			}
			return d, nil

		case "g":
			d.cursor = 0
			d.scrollY = 0
			return d, nil

		case "G":
			if len(d.files) > 0 {
				d.cursor = len(d.files) - 1
				d.ensureCursorVisible()
			}
			return d, nil

		case "r":
			d.loading = true
			d.err = nil
			d.popupStatus = ""
			return d, d.loadChangedFilesCmd()

		case "enter":
			return d, d.openSelectedDiffCmd()

		case "a":
			return d, d.openPopupCmd("", true)
		}
	}

	return d, nil
}

func (d *DiffViewer) View() string {
	if d.loading {
		return d.styles.Dimmed.Render("Loading changed files...")
	}

	if d.err != nil {
		return d.styles.DeleteLine.Render(fmt.Sprintf("Error loading changed files: %v", d.err))
	}

	if len(d.files) == 0 {
		return d.styles.Dimmed.Render(fmt.Sprintf("No changes vs %s", d.effectiveBaseBranch()))
	}

	var content strings.Builder

	lines := d.renderFiles()

	start := d.scrollY
	end := min(d.scrollY+d.viewHeight, len(lines))

	for i := start; i < end; i++ {
		content.WriteString(lines[i])
		content.WriteString("\n")
	}

	if d.popupStatus != "" {
		content.WriteString(d.styles.Footer.Render(d.popupStatus))
		content.WriteString("\n\n")
	}

	footer := d.renderFooter()
	content.WriteString(footer)

	return content.String()
}

func (d *DiffViewer) Title() string {
	if len(d.files) == 0 {
		return fmt.Sprintf("Diff vs %s", d.effectiveBaseBranch())
	}
	return fmt.Sprintf("Diff vs %s (%d file%s)", d.effectiveBaseBranch(), len(d.files), plural(len(d.files)))
}

func (d *DiffViewer) Size() (width, height int) {
	d.viewHeight = 20
	return 100, 30
}

func (d *DiffViewer) renderFiles() []string {
	lines := make([]string, 0, len(d.files))
	for i, file := range d.files {
		isSelected := i == d.cursor

		var headerStyle lipgloss.Style
		if isSelected {
			headerStyle = d.styles.FileHeaderSelected
		} else {
			headerStyle = d.styles.FileHeader
		}

		cursorMarker := " "
		if isSelected {
			cursorMarker = "▶"
		}

		fileStatus := toViewerFileStatus(file.Status)
		badge := d.styles.FileStatusBadge(fileStatus)
		path := file.Path
		if fileStatus == FileRenamed && file.OldPath != file.Path {
			path = fmt.Sprintf("%s → %s", file.OldPath, file.Path)
		}

		line := lipgloss.JoinHorizontal(
			lipgloss.Left,
			cursorMarker,
			" ",
			badge,
			" ",
			headerStyle.Render(path),
		)
		lines = append(lines, line)
	}
	return lines
}

func (d *DiffViewer) renderFooter() string {
	hints := []keybinds.Binding{
		{Key: "j/k", Description: "navigate files"},
		{Key: "Enter", Description: "popup selected"},
		{Key: "a", Description: "popup all"},
		{Key: "r", Description: "refresh"},
		{Key: "q/Esc", Description: "close"},
	}

	status := ""
	if len(d.files) > 0 {
		status = d.styles.Footer.Render(fmt.Sprintf("  [File %d/%d]", d.cursor+1, len(d.files)))
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		keybinds.RenderInline(hints, " • ", keybinds.Theme{
			KeyStyle:         d.styles.KeyHint,
			DescriptionStyle: d.styles.Footer,
			FooterStyle:      d.styles.Footer,
		}),
		status,
	)
}

func (d *DiffViewer) ensureCursorVisible() {
	if d.cursor < d.scrollY {
		d.scrollY = d.cursor
	} else if d.cursor >= d.scrollY+d.viewHeight {
		d.scrollY = d.cursor - d.viewHeight + 1
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func toViewerFileStatus(status gitservice.DiffFileStatus) FileStatus {
	switch status {
	case gitservice.DiffFileAdded:
		return FileAdded
	case gitservice.DiffFileDeleted:
		return FileDeleted
	case gitservice.DiffFileRenamed:
		return FileRenamed
	default:
		return FileModified
	}
}
