package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// MergePreflightOverlay displays merge-preflight failures and recovery actions.
type MergePreflightOverlay struct {
	sourceID       string
	targetID       string
	sourceWorktree string
	targetWorktree string
	reasons        []string
	sourceFiles    []string
	targetFiles    []string
	canAbortTarget bool
	styles         *Styles
}

func NewMergePreflightOverlay(
	sourceID, targetID, sourceWorktree, targetWorktree string,
	reasons, sourceFiles, targetFiles []string,
	canAbortTarget bool,
) *MergePreflightOverlay {
	return &MergePreflightOverlay{
		sourceID:       sourceID,
		targetID:       targetID,
		sourceWorktree: sourceWorktree,
		targetWorktree: targetWorktree,
		reasons:        append([]string(nil), reasons...),
		sourceFiles:    append([]string(nil), sourceFiles...),
		targetFiles:    append([]string(nil), targetFiles...),
		canAbortTarget: canAbortTarget,
		styles:         New(),
	}
}

func (m *MergePreflightOverlay) Init() tea.Cmd {
	return nil
}

func (m *MergePreflightOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "r", "R":
			return m, func() tea.Msg { return SelectionMsg{Key: "merge_preflight_refresh"} }
		case "a", "A":
			if !m.canAbortTarget {
				return m, nil
			}
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "merge_preflight_abort",
					Value: m.targetWorktree,
				}
			}
		case "c":
			if strings.TrimSpace(m.sourceWorktree) == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "merge_preflight_commit_source",
					Value: m.sourceWorktree,
				}
			}
		case "d":
			if strings.TrimSpace(m.sourceWorktree) == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "merge_preflight_discard_source",
					Value: m.sourceWorktree,
				}
			}
		case "C":
			if strings.TrimSpace(m.targetWorktree) == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "merge_preflight_commit_target",
					Value: m.targetWorktree,
				}
			}
		case "D":
			if strings.TrimSpace(m.targetWorktree) == "" {
				return m, nil
			}
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "merge_preflight_discard_target",
					Value: m.targetWorktree,
				}
			}
		case "esc", "q", "enter":
			return m, func() tea.Msg { return CloseOverlayMsg{} }
		}
	}
	return m, nil
}

func (m *MergePreflightOverlay) View() string {
	var b strings.Builder

	title := fmt.Sprintf("Merge blocked: %s -> %s", m.sourceID, m.targetID)
	b.WriteString(m.styles.Title.Render(title))
	b.WriteString("\n\n")
	b.WriteString(m.styles.MenuItem.Render("Merge preflight requires clean git status on source and target."))
	b.WriteString("\n")
	if len(m.reasons) > 0 {
		b.WriteString("\n")
		for _, reason := range m.reasons {
			b.WriteString(m.styles.MenuItem.Render("• " + reason))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(m.styles.MenuKey.Render("Source dirty files:"))
	b.WriteString("\n")
	if len(m.sourceFiles) == 0 {
		b.WriteString(m.styles.MenuItemDisabled.Render("  (clean or unavailable)"))
		b.WriteString("\n")
	} else {
		for _, file := range m.sourceFiles {
			b.WriteString(m.styles.MenuItem.Render("  • " + file))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(m.styles.MenuKey.Render("Target dirty files:"))
	b.WriteString("\n")
	if len(m.targetFiles) == 0 {
		b.WriteString(m.styles.MenuItemDisabled.Render("  (clean or unavailable)"))
		b.WriteString("\n")
	} else {
		for _, file := range m.targetFiles {
			b.WriteString(m.styles.MenuItem.Render("  • " + file))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(m.styles.MenuItem.Render("[R] Refresh task/worktree state"))
	b.WriteString("\n")
	b.WriteString(m.styles.MenuItem.Render("[c/d] Commit/Discard source changes"))
	b.WriteString("\n")
	b.WriteString(m.styles.MenuItem.Render("[C/D] Commit/Discard target changes"))
	b.WriteString("\n")
	if m.canAbortTarget {
		b.WriteString(m.styles.MenuItem.Render("[A] Abort merge in target worktree"))
		b.WriteString("\n")
	}
	b.WriteString(m.styles.MenuItem.Render("[Esc/q/Enter] Close"))
	b.WriteString("\n\n")
	b.WriteString(keybinds.RenderKeyTable([]keybinds.Binding{
		{Key: "R", Description: "refresh"},
		{Key: "c/d", Description: "commit/discard source"},
		{Key: "C/D", Description: "commit/discard target"},
		{Key: "A", Description: "abort target merge"},
		{Key: "Esc/q/Enter", Description: "close"},
	}, 0, keybinds.Theme{
		KeyStyle:         m.styles.MenuKey,
		DescriptionStyle: m.styles.Footer,
		FooterStyle:      m.styles.Footer,
	}))

	return b.String()
}

func (m *MergePreflightOverlay) Title() string {
	return "Merge Preconditions"
}

func (m *MergePreflightOverlay) Size() (width, height int) {
	h := 16 + len(m.reasons) + len(m.sourceFiles) + len(m.targetFiles)
	if m.canAbortTarget {
		h++
	}
	return 86, h
}
