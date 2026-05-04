package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

// ConflictOverlay displays merge conflicts and resolution options
type ConflictOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	files               []string
	issueID             string
	worktree            string
	cursor              int
	onResolveWithClaude func() tea.Cmd
	onAbort             func() tea.Cmd
	overlayStyles       *Styles
}

// ConflictResolutionMsg is sent when the user chooses a resolution method
type ConflictResolutionMsg struct {
	ResolveWithClaude bool
	Abort             bool
	OpenManually      bool
	IssueID           string
	Worktree          string
}

// NewConflictOverlay creates a new conflict resolution overlay
func NewConflictOverlay(
	files []string,
	onResolveWithClaude func() tea.Cmd,
	onAbort func() tea.Cmd,
) *ConflictOverlay {
	return &ConflictOverlay{
		files:               files,
		cursor:              0,
		onResolveWithClaude: onResolveWithClaude,
		onAbort:             onAbort,
		overlayStyles:       New(),
	}
}

// NewConflictDialog creates a new conflict resolution dialog (deprecated, use NewConflictOverlay)
func NewConflictDialog(files []string) *ConflictOverlay {
	return NewConflictOverlay(files, nil, nil)
}

func NewConflictDialogForIssue(files []string, issueID, worktree string) *ConflictOverlay {
	dialog := NewConflictDialog(files)
	dialog.issueID = strings.TrimSpace(issueID)
	dialog.worktree = strings.TrimSpace(worktree)
	return dialog
}

// Init initializes the overlay
func (c *ConflictOverlay) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (c *ConflictOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.ApplyWindowSize(msg)
		return c, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			// Escape closes without action
			return c, func() tea.Msg { return CloseOverlayMsg{} }

		case "c", "C":
			// Resolve with Claude
			if c.onResolveWithClaude != nil {
				return c, c.onResolveWithClaude()
			}
			return c, func() tea.Msg {
				return SelectionMsg{
					Key: "claude",
					Value: ConflictResolutionMsg{
						ResolveWithClaude: true,
						IssueID:           c.issueID,
						Worktree:          c.worktree,
					},
				}
			}

		case "a", "A":
			// Abort merge
			if c.onAbort != nil {
				return c, c.onAbort()
			}
			return c, func() tea.Msg {
				return SelectionMsg{
					Key: "abort",
					Value: ConflictResolutionMsg{
						Abort:    true,
						IssueID:  c.issueID,
						Worktree: c.worktree,
					},
				}
			}

		case "o", "O":
			// Open manually
			return c, func() tea.Msg {
				return SelectionMsg{
					Key: "manual",
					Value: ConflictResolutionMsg{
						OpenManually: true,
						IssueID:      c.issueID,
						Worktree:     c.worktree,
					},
				}
			}

		case "j", "down":
			if c.cursor < len(c.files)-1 {
				c.cursor++
			}
			return c, nil

		case "k", "up":
			if c.cursor > 0 {
				c.cursor--
			}
			return c, nil
		}
	}

	return c, nil
}

// View renders the overlay
func (c *ConflictOverlay) View() string {
	width, height := c.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            c.overlayStyles,
		width:             width,
		height:            height,
		title:             "MERGE CONFLICTS",
		rightSectionTitle: "Actions",
		breakpoint:        80,
		gap:               3,
		minLeft:           36,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return c.renderConflictList(width)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(c.overlayStyles, []keybinds.Binding{
				{Key: "j/k", Description: "navigate"},
				{Key: "c", Description: "ai resolve"},
				{Key: "o", Description: "open"},
				{Key: "a", Description: "abort"},
				{Key: "Esc", Description: "close"},
			})
		},
	})
}

// Title returns the overlay title
func (c *ConflictOverlay) Title() string {
	return "Merge Conflicts"
}

// Size returns the overlay dimensions
func (c *ConflictOverlay) Size() (width, height int) {
	fileLines := len(c.files)
	if fileLines > 12 {
		fileLines = 12
	}
	return c.ClampResponsive(100, 14+fileLines)
}

func (c *ConflictOverlay) renderConflictList(width int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(styles.Red).
		Bold(true)
	b.WriteString(headerStyle.Render("⚠ Merge conflicts detected!"))
	b.WriteString("\n")
	b.WriteString(c.overlayStyles.Footer.Render("AI resolve now? Press c."))
	b.WriteString("\n")

	if len(c.files) == 0 {
		b.WriteString(c.overlayStyles.Footer.Render("No conflicted files."))
		return strings.TrimRight(b.String(), "\n")
	}

	maxWidth := max(24, width-2)
	for i, file := range c.files {
		prefix := "  "
		fileStyle := c.overlayStyles.MenuItem
		if i == c.cursor {
			prefix = lipgloss.NewStyle().Foreground(styles.Blue).Render("▸ ")
			fileStyle = c.overlayStyles.MenuItemActive
		}
		line := prefix + fileStyle.Render(file)
		b.WriteString(clampOverlayLineWidth(line, maxWidth))
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// StatusBindings returns footer key hints while the overlay is open.
func (c *ConflictOverlay) StatusBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "j/k/↑/↓", Description: "navigate"},
		{Key: "c", Description: "ai resolve"},
		{Key: "o", Description: "open"},
		{Key: "a", Description: "abort"},
		{Key: "Esc/q", Description: "close"},
	}
}
