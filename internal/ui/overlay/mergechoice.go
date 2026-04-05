package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

type MergeChoiceOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	issueID       string
	commitsBehind int
	baseBranch    string
	selectedMerge bool
	styles        *Styles
}

func NewMergeChoiceOverlay(issueID string, commitsBehind int, baseBranch string) *MergeChoiceOverlay {
	return &MergeChoiceOverlay{
		issueID:       issueID,
		commitsBehind: commitsBehind,
		baseBranch:    baseBranch,
		selectedMerge: true,
		styles:        New(),
	}
}

func (m *MergeChoiceOverlay) Init() tea.Cmd {
	return nil
}

func (m *MergeChoiceOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "m", "M":
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "merge_attach",
					Value: m.issueID,
				}
			}
		case "s", "S":
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "skip_attach",
					Value: m.issueID,
				}
			}
		case "enter":
			if m.selectedMerge {
				return m, func() tea.Msg {
					return SelectionMsg{
						Key:   "merge_attach",
						Value: m.issueID,
					}
				}
			}
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "skip_attach",
					Value: m.issueID,
				}
			}
		case "left", "h":
			m.selectedMerge = true
			return m, nil
		case "right", "l", "tab":
			m.selectedMerge = false
			return m, nil
		case "esc":
			return m, func() tea.Msg {
				return CloseOverlayMsg{}
			}
		}
	case tea.WindowSizeMsg:
		m.ApplyWindowSize(msg)
	}
	return m, nil
}

func (m *MergeChoiceOverlay) View() string {
	width, height := m.dialogSize()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             "MERGE CHOICE",
		rightSectionTitle: "Actions",
		breakpoint:        58,
		gap:               2,
		minLeft:           28,
		minRight:          18,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			var b strings.Builder
			b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("%d commits behind base branch (%s). Merge latest?", m.commitsBehind, m.baseBranch)))
			b.WriteString("\n\n")
			mergeStyle := m.styles.MenuItem
			skipStyle := m.styles.MenuItem
			if m.selectedMerge {
				mergeStyle = m.styles.MenuItemActive
			} else {
				skipStyle = m.styles.MenuItemActive
			}
			b.WriteString(mergeStyle.Render("[M] Merge & Attach"))
			b.WriteString("\n")
			b.WriteString(skipStyle.Render("[S] Skip & Attach"))
			return strings.TrimRight(b.String(), "\n")
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(m.styles, []keybinds.Binding{
				{Key: "←/→/Tab", Description: "switch"},
				{Key: "Enter", Description: "confirm"},
				{Key: "M", Description: "merge & attach"},
				{Key: "S", Description: "skip & attach"},
				{Key: "Esc", Description: "cancel"},
			})
		},
	})
}

func (m *MergeChoiceOverlay) Title() string {
	return "Merge Choice"
}

func (m *MergeChoiceOverlay) Size() (width, height int) {
	return m.dialogSize()
}

func (m *MergeChoiceOverlay) dialogSize() (width, height int) {
	width, height = 64, 12
	if m.width <= 0 || m.height <= 0 {
		return width, height
	}

	maxWidth := max(44, m.width-2)
	maxHeight := max(10, m.height-2)
	if width > maxWidth {
		width = maxWidth
	}
	if height > maxHeight {
		height = maxHeight
	}

	// Keep this lightweight prompt compact on medium viewports instead of
	// promoting to near-fullscreen dialog sizing.
	if m.width <= 100 {
		targetWidth := m.width - 10
		if targetWidth < 44 {
			targetWidth = m.width - 2
		}
		if targetWidth > 0 && targetWidth < width {
			width = targetWidth
		}
	}
	if m.height <= 24 {
		targetHeight := m.height - 8
		if targetHeight < 10 {
			targetHeight = m.height - 2
		}
		if targetHeight > 0 && targetHeight < height {
			height = targetHeight
		}
	}

	return max(1, width), max(1, height)
}
