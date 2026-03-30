package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

type MergeChoiceOverlay struct {
	issueID       string
	commitsBehind int
	baseBranch    string
	styles        *Styles
	viewportWidth  int
	viewportHeight int
}

func NewMergeChoiceOverlay(issueID string, commitsBehind int, baseBranch string) *MergeChoiceOverlay {
	return &MergeChoiceOverlay{
		issueID:       issueID,
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
		case "esc":
			return m, func() tea.Msg {
				return CloseOverlayMsg{}
			}
		}
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.viewportWidth = msg.Width
		}
		if msg.Height > 0 {
			m.viewportHeight = msg.Height
		}
	}
	return m, nil
}

func (m *MergeChoiceOverlay) View() string {
	width, height := clampDialogSize(64, 12, m.viewportWidth, m.viewportHeight)
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            m.styles,
		width:             width,
		height:            height,
		title:             "MERGE CHOICE",
		rightSectionTitle: "Actions",
		breakpoint:        72,
		gap:               3,
		minLeft:           30,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			var b strings.Builder
			b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("%d commits behind %s. Merge latest?", m.commitsBehind, m.baseBranch)))
			b.WriteString("\n\n")
			b.WriteString(m.styles.MenuItem.Render("[M] Merge & Attach"))
			b.WriteString("\n")
			b.WriteString(m.styles.MenuItem.Render("[S] Skip & Attach"))
			return strings.TrimRight(b.String(), "\n")
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return keybinds.RenderKeyTable([]keybinds.Binding{
				{Key: "M", Description: "merge & attach"},
				{Key: "S", Description: "skip & attach"},
				{Key: "Esc", Description: "cancel"},
			}, 0, keybinds.Theme{
				KeyStyle:         m.styles.MenuKey,
				DescriptionStyle: m.styles.Footer,
				FooterStyle:      m.styles.Footer,
			})
		},
	})
}

func (m *MergeChoiceOverlay) Title() string {
	return "Merge Choice"
}

func (m *MergeChoiceOverlay) Size() (width, height int) {
	return 60, 10
}
