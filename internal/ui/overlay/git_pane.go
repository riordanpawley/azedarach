package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

const GitPaneHotkey = "G"

type GitPaneOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	styles  *Styles
	branch  string
	status  git.GitStatus
	loading bool
	err     error
}

func NewGitPaneOverlay(branch string) *GitPaneOverlay {
	return &GitPaneOverlay{styles: New(), branch: strings.TrimSpace(branch), loading: true}
}
func (g *GitPaneOverlay) Init() tea.Cmd { return nil }
func (g *GitPaneOverlay) SetStatus(status git.GitStatus, err error) {
	g.status, g.err, g.loading = status, err, false
}
func (g *GitPaneOverlay) SetLoading() { g.loading, g.err = true, nil }
func (g *GitPaneOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		g.ApplyWindowSize(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", GitPaneHotkey:
			return g, func() tea.Msg { return CloseOverlayMsg{} }
		case "r":
			g.SetLoading()
			return g, func() tea.Msg { return SelectionMsg{Key: "git_pane_refresh"} }
		case "p":
			g.SetLoading()
			return g, func() tea.Msg { return SelectionMsg{Key: "git_pane_pull"} }
		case "P":
			g.SetLoading()
			return g, func() tea.Msg { return SelectionMsg{Key: "git_pane_push"} }
		}
	}
	return g, nil
}
func (g *GitPaneOverlay) View() string {
	w, h := g.Size()
	return renderDialogTwoPane(dialogLayoutConfig{styles: g.styles, width: w, height: h, title: "GIT · PROJECT ROOT", rightSectionTitle: "Actions", breakpoint: 72, gap: 3, minLeft: 34, minRight: 20, leftFocused: true,
		renderLeft: func(_ dialogLayoutMode, _, _ int) string {
			var b strings.Builder
			branch := g.branch
			if branch == "" {
				branch = "unknown"
			}
			fmt.Fprintf(&b, "Branch  %s\nAhead   %d\nBehind  %d\n", branch, g.status.GitAheadCount, g.status.GitBehindCount)
			if g.loading {
				b.WriteString("\nRefreshing…")
			} else if g.err != nil {
				fmt.Fprintf(&b, "\nError: %v", g.err)
			} else if !g.status.HasChanges {
				b.WriteString("\n✓ Working tree clean")
			} else {
				fmt.Fprintf(&b, "\nWorking tree changes\n  modified %d · added %d · deleted %d · untracked %d · staged %d", len(g.status.Modified), len(g.status.Added), len(g.status.Deleted), len(g.status.Untracked), len(g.status.Staged))
				if g.status.HasConflicts {
					fmt.Fprintf(&b, "\n  conflicts %d", len(g.status.Conflicted))
				}
			}
			return b.String()
		},
		renderRight: func(_ dialogLayoutMode, _, _ int) string {
			return renderDialogActions(g.styles, []keybinds.Binding{{Key: "r", Description: "refresh"}, {Key: "p", Description: "pull base"}, {Key: "P", Description: "push base"}, {Key: "Esc/q", Description: "close"}})
		},
	})
}
func (g *GitPaneOverlay) Title() string    { return "Git" }
func (g *GitPaneOverlay) Size() (int, int) { return g.ClampResponsive(76, 18) }
