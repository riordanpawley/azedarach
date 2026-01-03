package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type GitPullOverlay struct {
	commitsBehind int
	selected      bool
	styles        *Styles
}

func NewGitPullOverlay(count int) *GitPullOverlay {
	return &GitPullOverlay{
		commitsBehind: count,
		selected:      true,
		styles:        New(),
	}
}

func (g *GitPullOverlay) Init() tea.Cmd {
	return nil
}

func (g *GitPullOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "p", "P", "enter":
			if g.selected {
				return g, func() tea.Msg {
					return SelectionMsg{
						Key: "git_pull",
					}
				}
			}
			return g, func() tea.Msg {
				return CloseOverlayMsg{}
			}
		case "n", "N", "esc":
			return g, func() tea.Msg {
				return CloseOverlayMsg{}
			}
		case "left", "h", "right", "l", "tab":
			g.selected = !g.selected
			return g, nil
		}
	}
	return g, nil
}

func (g *GitPullOverlay) View() string {
	var b strings.Builder

	message := fmt.Sprintf("Your local main branch is behind by %d commits.", g.commitsBehind)
	b.WriteString(g.styles.MenuItem.Render(message))
	b.WriteString("\n\n")

	pullStyle := g.styles.MenuItem
	noStyle := g.styles.MenuItem

	if g.selected {
		pullStyle = g.styles.MenuItemActive
	} else {
		noStyle = g.styles.MenuItemActive
	}

	pull := pullStyle.Render("[P] Pull Now")
	no := noStyle.Render("[N] Not Now")

	b.WriteString(pull + "    " + no)
	b.WriteString("\n")

	footer := g.styles.Footer.Render("← → / Tab: Switch • Enter: Confirm • Esc: Cancel")
	b.WriteString("\n")
	b.WriteString(footer)

	return b.String()
}

func (g *GitPullOverlay) Title() string {
	return "Git Sync"
}

func (g *GitPullOverlay) Size() (width, height int) {
	return 60, 8
}
