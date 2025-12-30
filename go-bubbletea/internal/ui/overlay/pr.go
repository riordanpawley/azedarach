package overlay

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PRCreatedMsg is emitted when a PR is created
type PRCreatedMsg struct {
	Title      string
	Body       string
	Branch     string
	BaseBranch string
	Draft      bool
	BeadID     string
}

// PRCreateOverlay provides a form to create a pull request
type PRCreateOverlay struct {
	title      textinput.Model
	body       textarea.Model
	draft      bool
	branch     string
	baseBranch string
	beadID     string
	focusIndex int
	styles     *Styles
}

const (
	prFocusTitle = iota
	prFocusBody
	prFocusDraft
	prFocusSubmit
)

// NewPRCreateOverlay creates a new PR creation overlay
func NewPRCreateOverlay(branch, baseBranch, beadID string) *PRCreateOverlay {
	// Initialize title input
	ti := textinput.New()
	ti.Placeholder = "Pull request title..."
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 70

	// Initialize body textarea
	ta := textarea.New()
	ta.Placeholder = "Describe your changes (supports markdown)..."
	ta.CharLimit = 5000
	ta.SetWidth(70)
	ta.SetHeight(8)

	return &PRCreateOverlay{
		title:      ti,
		body:       ta,
		draft:      true, // Default to draft
		branch:     branch,
		baseBranch: baseBranch,
		beadID:     beadID,
		focusIndex: prFocusTitle,
		styles:     New(),
	}
}

// Init initializes the overlay
func (p *PRCreateOverlay) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages
func (p *PRCreateOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return p, func() tea.Msg { return CloseOverlayMsg{} }

		case "ctrl+s":
			// Submit the form
			return p, p.submit()

		case "tab", "shift+tab":
			// Tab through fields
			if msg.String() == "tab" {
				p.focusIndex = (p.focusIndex + 1) % 4
			} else {
				p.focusIndex = (p.focusIndex - 1 + 4) % 4
			}

			// Update focus
			if p.focusIndex == prFocusTitle {
				p.title.Focus()
				p.body.Blur()
			} else if p.focusIndex == prFocusBody {
				p.title.Blur()
				p.body.Focus()
			} else {
				p.title.Blur()
				p.body.Blur()
			}

			return p, nil

		case "enter":
			// Submit if on submit button
			if p.focusIndex == prFocusSubmit {
				return p, p.submit()
			}
			// Otherwise let the active field handle enter

		case "d":
			// Toggle draft when focused on draft field
			if p.focusIndex == prFocusDraft {
				p.draft = !p.draft
				return p, nil
			}
		}
	}

	// Update active field
	var cmd tea.Cmd
	if p.focusIndex == prFocusTitle {
		p.title, cmd = p.title.Update(msg)
		cmds = append(cmds, cmd)
	} else if p.focusIndex == prFocusBody {
		p.body, cmd = p.body.Update(msg)
		cmds = append(cmds, cmd)
	}

	return p, tea.Batch(cmds...)
}

// View renders the form
func (p *PRCreateOverlay) View() string {
	var b strings.Builder

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(14).
		Align(lipgloss.Right)

	focusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	infoStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6c7086"))

	// Branch info header
	b.WriteString(infoStyle.Render(fmt.Sprintf(
		"Creating PR: %s → %s (Bead: %s)",
		p.branch, p.baseBranch, p.beadID,
	)))
	b.WriteString("\n\n")

	// Title field
	if p.focusIndex == prFocusTitle {
		b.WriteString(focusStyle.Render("Title:"))
	} else {
		b.WriteString(labelStyle.Render("Title:"))
	}
	b.WriteString("  ")
	b.WriteString(p.title.View())
	b.WriteString("\n\n")

	// Body field
	if p.focusIndex == prFocusBody {
		b.WriteString(focusStyle.Render("Description:"))
	} else {
		b.WriteString(labelStyle.Render("Description:"))
	}
	b.WriteString("\n")
	b.WriteString(p.body.View())
	b.WriteString("\n\n")

	// Draft toggle
	if p.focusIndex == prFocusDraft {
		b.WriteString(focusStyle.Render("Draft:"))
	} else {
		b.WriteString(labelStyle.Render("Draft:"))
	}
	b.WriteString("  ")
	b.WriteString(p.renderDraftToggle())
	b.WriteString("\n\n")

	// Separator
	b.WriteString(p.styles.Separator.Render(strings.Repeat("─", 70)))
	b.WriteString("\n\n")

	// Submit button
	submitStyle := p.styles.MenuItem
	if p.focusIndex == prFocusSubmit {
		submitStyle = p.styles.MenuItemActive
	}
	b.WriteString(submitStyle.Render("[ Create Pull Request ]"))
	b.WriteString("\n\n")

	// Footer hints
	hints := []string{
		p.styles.MenuKey.Render("Tab") + " " + p.styles.Footer.Render("Switch fields"),
		p.styles.MenuKey.Render("d") + " " + p.styles.Footer.Render("Toggle draft"),
		p.styles.MenuKey.Render("Ctrl+S") + " " + p.styles.Footer.Render("Submit"),
		p.styles.MenuKey.Render("Esc") + " " + p.styles.Footer.Render("Cancel"),
	}
	b.WriteString(p.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderDraftToggle renders the draft checkbox
func (p *PRCreateOverlay) renderDraftToggle() string {
	activeStyle := p.styles.MenuItemActive
	inactiveStyle := p.styles.MenuItem

	var checkbox string
	var label string

	if p.draft {
		checkbox = activeStyle.Render("[✓]")
		label = activeStyle.Render("Draft PR (ready for review later)")
	} else {
		checkbox = inactiveStyle.Render("[ ]")
		label = inactiveStyle.Render("Ready for review")
	}

	return fmt.Sprintf("%s %s", checkbox, label)
}

// submit creates a PRCreatedMsg and closes the overlay
func (p *PRCreateOverlay) submit() tea.Cmd {
	// Validate title is not empty
	title := strings.TrimSpace(p.title.Value())
	if title == "" {
		return nil // Don't submit if title is empty
	}

	return tea.Batch(
		func() tea.Msg {
			return PRCreatedMsg{
				Title:      title,
				Body:       strings.TrimSpace(p.body.Value()),
				Branch:     p.branch,
				BaseBranch: p.baseBranch,
				Draft:      p.draft,
				BeadID:     p.beadID,
			}
		},
		func() tea.Msg { return CloseOverlayMsg{} },
	)
}

// Title returns the overlay title
func (p *PRCreateOverlay) Title() string {
	return "Create Pull Request"
}

// Size returns the overlay dimensions
func (p *PRCreateOverlay) Size() (width, height int) {
	return 80, 28
}
