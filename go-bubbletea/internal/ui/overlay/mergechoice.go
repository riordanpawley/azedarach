package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type MergeChoiceOverlay struct {
	beadID        string
	commitsBehind int
	baseBranch    string
	styles        *Styles
}

func NewMergeChoiceOverlay(beadID string, commitsBehind int, baseBranch string) *MergeChoiceOverlay {
	return &MergeChoiceOverlay{
		beadID:        beadID,
		commitsBehind: commitsBehind,
		baseBranch:    baseBranch,
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
					Value: m.beadID,
				}
			}
		case "s", "S":
			return m, func() tea.Msg {
				return SelectionMsg{
					Key:   "skip_attach",
					Value: m.beadID,
				}
			}
		case "esc":
			return m, func() tea.Msg {
				return CloseOverlayMsg{}
			}
		}
	}
	return m, nil
}

func (m *MergeChoiceOverlay) View() string {
	var b strings.Builder

	b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("%d commits behind %s. Merge latest?", m.commitsBehind, m.baseBranch)))
	b.WriteString("\n\n")

	mStyle := m.styles.MenuItem
	sStyle := m.styles.MenuItem

	mOption := mStyle.Render("[M] Merge & Attach")
	sOption := sStyle.Render("[S] Skip & Attach")

	b.WriteString(mOption + "\n")
	b.WriteString(sOption + "\n")

	b.WriteString("\n")
	footer := m.styles.Footer.Render("Esc: Cancel")
	b.WriteString(footer)

	return b.String()
}

func (m *MergeChoiceOverlay) Title() string {
	return "Merge Choice"
}

func (m *MergeChoiceOverlay) Size() (width, height int) {
	return 60, 10
}
