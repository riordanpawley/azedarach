package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// MergePreflightOverlay displays merge-preflight failures and recovery actions.
type MergePreflightOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
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
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
		return m, nil
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
	width, height := m.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             m.Title(),
		rightSectionTitle: "Actions",
		breakpoint:        84,
		gap:               3,
		minLeft:           44,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return m.renderDetails(width)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, m.actionBindings(), width)
		},
	})
}

func (m *MergePreflightOverlay) renderDetails(width int) string {
	var b strings.Builder

	b.WriteString(m.styles.MenuItem.Render("Merge preflight requires clean git status on source and target."))
	b.WriteString("\n\n")

	title := fmt.Sprintf("Merge blocked: %s -> %s", m.sourceID, m.targetID)
	b.WriteString(m.styles.MenuKey.Render(title))
	b.WriteString("\n")
	b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("Source worktree: %s", safeWorktreeLabel(m.sourceWorktree))))
	b.WriteString("\n")
	b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("Target worktree: %s", safeWorktreeLabel(m.targetWorktree))))
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
	if strings.TrimSpace(m.sourceWorktree) == "" {
		b.WriteString("\n")
		b.WriteString(m.styles.MenuItemDisabled.Render("Source actions unavailable: no source worktree path"))
	}
	if strings.TrimSpace(m.targetWorktree) == "" {
		b.WriteString("\n")
		b.WriteString(m.styles.MenuItemDisabled.Render("Target actions unavailable: no target worktree path"))
	}

	return strings.TrimRight(b.String(), "\n")
}

func safeWorktreeLabel(worktree string) string {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return "(unavailable)"
	}
	return worktree
}

func (m *MergePreflightOverlay) Title() string {
	return "Merge Preconditions"
}

func (m *MergePreflightOverlay) Size() (width, height int) {
	return m.ClampResponsive(90, m.sizeHeight())
}

func (m *MergePreflightOverlay) sizeHeight() int {
	reasonLines := min(8, len(m.reasons))
	sourceFileLines := min(8, len(m.sourceFiles))
	targetFileLines := min(8, len(m.targetFiles))
	h := 15 + reasonLines + sourceFileLines + targetFileLines
	if m.canAbortTarget {
		h++
	}
	return min(28, max(14, h))
}

func (m *MergePreflightOverlay) actionBindings() []keybinds.Binding {
	bindings := []keybinds.Binding{
		{Key: "R", Description: "refresh"},
		{Key: "c/d", Description: "commit/discard source"},
		{Key: "C/D", Description: "commit/discard target"},
	}
	if m.canAbortTarget {
		bindings = append(bindings, keybinds.Binding{Key: "A", Description: "abort target merge"})
	}
	bindings = append(bindings, keybinds.Binding{Key: "Esc/q/Enter", Description: "close"})
	return bindings
}
