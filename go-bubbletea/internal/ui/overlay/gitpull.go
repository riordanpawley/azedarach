package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

type GitPullOverlay struct {
	commitsBehind int
	selected      bool
	styles        *Styles
	viewportWidth  int
	viewportHeight int
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
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			g.viewportWidth = msg.Width
		}
		if msg.Height > 0 {
			g.viewportHeight = msg.Height
		}
	}
	return g, nil
}

func (g *GitPullOverlay) View() string {
	width, height := clampDialogSize(64, 12, g.viewportWidth, g.viewportHeight)
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            g.styles,
		width:             width,
		height:            height,
		title:             "GIT SYNC",
		rightSectionTitle: "Actions",
		breakpoint:        72,
		gap:               3,
		minLeft:           30,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
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
			b.WriteString(pullStyle.Render("[P] Pull Now"))
			b.WriteString("\n")
			b.WriteString(noStyle.Render("[N] Not Now"))
			return strings.TrimRight(b.String(), "\n")
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return keybinds.RenderKeyTable([]keybinds.Binding{
				{Key: "←/→/Tab", Description: "switch"},
				{Key: "Enter", Description: "confirm"},
				{Key: "Esc", Description: "cancel"},
			}, 0, keybinds.Theme{
				KeyStyle:         g.styles.MenuKey,
				DescriptionStyle: g.styles.Footer,
				FooterStyle:      g.styles.Footer,
			})
		},
	})
}

func (g *GitPullOverlay) Title() string {
	return "Git Sync"
}

func (g *GitPullOverlay) Size() (width, height int) {
	return 60, 8
}
