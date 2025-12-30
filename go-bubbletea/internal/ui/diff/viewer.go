package diff

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

// DiffViewer displays git diff output with file navigation and syntax highlighting
type DiffViewer struct {
	worktree   string
	diffOutput string
	files      []DiffFile
	cursor     int
	scrollY    int
	expanded   map[int]bool // Which files are expanded to show hunks
	styles     *Styles
	width      int
	height     int
	viewHeight int // Available height for content display
	loading    bool
	err        error
}

// NewDiffViewer creates a new diff viewer for the specified worktree
func NewDiffViewer(worktree string) *DiffViewer {
	return &DiffViewer{
		worktree:   worktree,
		files:      []DiffFile{},
		cursor:     0,
		scrollY:    0,
		expanded:   make(map[int]bool),
		styles:     New(),
		width:      80,
		height:     30,
		viewHeight: 20,
		loading:    false,
	}
}

// loadDiffMsg is sent when diff loading completes
type loadDiffMsg struct {
	output string
	err    error
}

// LoadDiff loads the git diff for the worktree
func (d *DiffViewer) LoadDiff(ctx context.Context, gitClient *git.Client) tea.Cmd {
	return func() tea.Msg {
		output, err := gitClient.Diff(ctx, d.worktree)
		return loadDiffMsg{output: output, err: err}
	}
}

// Init initializes the diff viewer
func (d *DiffViewer) Init() tea.Cmd {
	d.loading = true
	return nil
}

// Update handles messages
func (d *DiffViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loadDiffMsg:
		d.loading = false
		if msg.err != nil {
			d.err = msg.err
			return d, nil
		}

		d.diffOutput = msg.output
		d.files = ParseUnifiedDiff(msg.output)
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
			// Jump to top
			d.cursor = 0
			d.scrollY = 0
			return d, nil

		case "G":
			// Jump to bottom
			if len(d.files) > 0 {
				d.cursor = len(d.files) - 1
				d.ensureCursorVisible()
			}
			return d, nil

		case "enter", " ":
			// Toggle file expansion
			if d.cursor >= 0 && d.cursor < len(d.files) {
				d.expanded[d.cursor] = !d.expanded[d.cursor]
			}
			return d, nil

		case "E":
			// Expand all
			for i := range d.files {
				d.expanded[i] = true
			}
			return d, nil

		case "C":
			// Collapse all
			d.expanded = make(map[int]bool)
			return d, nil
		}
	}

	return d, nil
}

// View renders the diff viewer
func (d *DiffViewer) View() string {
	if d.loading {
		return d.styles.Dimmed.Render("Loading diff...")
	}

	if d.err != nil {
		return d.styles.DeleteLine.Render(fmt.Sprintf("Error loading diff: %v", d.err))
	}

	if len(d.files) == 0 {
		return d.styles.Dimmed.Render("No changes to display")
	}

	var content strings.Builder

	// Render files with proper scrolling
	visibleLines := d.renderFiles()
	lines := strings.Split(visibleLines, "\n")

	// Apply scroll window
	start := d.scrollY
	end := min(d.scrollY+d.viewHeight, len(lines))

	for i := start; i < end; i++ {
		if i < len(lines) {
			content.WriteString(lines[i])
			content.WriteString("\n")
		}
	}

	// Add footer with navigation hints
	footer := d.renderFooter()
	content.WriteString("\n")
	content.WriteString(footer)

	return content.String()
}

// Title returns the overlay title
func (d *DiffViewer) Title() string {
	if len(d.files) == 0 {
		return "Git Diff"
	}
	return fmt.Sprintf("Git Diff (%d file%s)", len(d.files), plural(len(d.files)))
}

// Size returns the overlay dimensions
func (d *DiffViewer) Size() (width, height int) {
	d.viewHeight = 20 // Content viewing area
	return 100, 30    // Total overlay size
}

// renderFiles renders all files with their diffs
func (d *DiffViewer) renderFiles() string {
	var b strings.Builder

	for i, file := range d.files {
		isSelected := i == d.cursor
		isExpanded := d.expanded[i]

		// File header with status badge
		var headerStyle lipgloss.Style
		if isSelected {
			if isExpanded {
				headerStyle = d.styles.FileHeaderExpanded
			} else {
				headerStyle = d.styles.FileHeaderSelected
			}
		} else {
			headerStyle = d.styles.FileHeader
		}

		// Render file header line
		cursor := " "
		if isSelected {
			cursor = "▶"
		}

		expandMarker := "►"
		if isExpanded {
			expandMarker = "▼"
		}

		badge := d.styles.FileStatusBadge(file.Status)
		path := file.Path
		if file.Status == FileRenamed && file.OldPath != file.Path {
			path = fmt.Sprintf("%s → %s", file.OldPath, file.Path)
		}

		statsRendered := lipgloss.JoinHorizontal(
			lipgloss.Left,
			d.styles.FileStatsAdd.Render(fmt.Sprintf("+%d", file.Additions)),
			" ",
			d.styles.FileStatsDel.Render(fmt.Sprintf("-%d", file.Deletions)),
		)

		headerLine := lipgloss.JoinHorizontal(
			lipgloss.Left,
			cursor,
			" ",
			expandMarker,
			" ",
			badge,
			" ",
			headerStyle.Render(path),
			" ",
			d.styles.FileStats.Render("("),
			statsRendered,
			d.styles.FileStats.Render(")"),
		)

		b.WriteString(headerLine)
		b.WriteString("\n")

		// Render hunks if expanded
		if isExpanded {
			for _, hunk := range file.Hunks {
				b.WriteString(d.renderHunk(hunk))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderHunk renders a single diff hunk
func (d *DiffViewer) renderHunk(hunk DiffHunk) string {
	var b strings.Builder

	// Hunk header
	b.WriteString("  ")
	b.WriteString(d.styles.HunkHeader.Render(hunk.Header))
	b.WriteString("\n")

	// Hunk lines
	for _, line := range hunk.Lines {
		b.WriteString(d.renderLine(line))
		b.WriteString("\n")
	}

	return b.String()
}

// renderLine renders a single diff line
func (d *DiffViewer) renderLine(line DiffLine) string {
	var prefix, lineNum, content string
	var style lipgloss.Style

	switch line.Type {
	case LineAdd:
		prefix = "+"
		lineNum = fmt.Sprintf("%5d", line.NewLine)
		content = line.Content
		style = d.styles.AddLine

	case LineDelete:
		prefix = "-"
		lineNum = fmt.Sprintf("%5d", line.OldLine)
		content = line.Content
		style = d.styles.DeleteLine

	case LineContext:
		prefix = " "
		lineNum = fmt.Sprintf("%5d", line.NewLine)
		content = line.Content
		style = d.styles.ContextLine
	}

	lineNumRendered := d.styles.LineNumber.Render(lineNum)

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		"    ",
		lineNumRendered,
		" ",
		style.Render(prefix),
		" ",
		style.Render(content),
	)
}

// renderFooter renders navigation hints
func (d *DiffViewer) renderFooter() string {
	hints := []string{
		d.styles.KeyHint.Render("j/k") + d.styles.Footer.Render(" navigate"),
		d.styles.KeyHint.Render("g/G") + d.styles.Footer.Render(" jump top/bottom"),
		d.styles.KeyHint.Render("Enter") + d.styles.Footer.Render(" expand/collapse"),
		d.styles.KeyHint.Render("E/C") + d.styles.Footer.Render(" expand/collapse all"),
		d.styles.KeyHint.Render("q/Esc") + d.styles.Footer.Render(" close"),
	}

	fileInfo := ""
	if len(d.files) > 0 {
		fileInfo = d.styles.Footer.Render(fmt.Sprintf("  [File %d/%d]", d.cursor+1, len(d.files)))
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		strings.Join(hints, d.styles.Footer.Render(" • ")),
		fileInfo,
	)
}

// ensureCursorVisible adjusts scrollY to keep cursor in view
func (d *DiffViewer) ensureCursorVisible() {
	// Calculate the line position of the cursor
	linePos := 0
	for i := 0; i < d.cursor && i < len(d.files); i++ {
		linePos++ // File header line
		if d.expanded[i] {
			// Count hunk lines
			for _, hunk := range d.files[i].Hunks {
				linePos++ // Hunk header
				linePos += len(hunk.Lines)
			}
			linePos++ // Blank line after expanded file
		}
	}

	// Adjust scroll to keep cursor visible
	if linePos < d.scrollY {
		d.scrollY = linePos
	} else if linePos >= d.scrollY+d.viewHeight {
		d.scrollY = linePos - d.viewHeight + 1
	}
}

// Helper functions

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
