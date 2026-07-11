package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ProjectOrchestratorDetails struct {
	Project        string
	Status         string
	Ready          int
	Review         int
	WaitingHuman   int
	OwnedElsewhere int
}

type ProjectOrchestratorOverlay struct {
	details ProjectOrchestratorDetails
	width   int
	height  int
	action  func(string) tea.Cmd
}

func NewProjectOrchestratorOverlay(details ProjectOrchestratorDetails, action func(string) tea.Cmd) *ProjectOrchestratorOverlay {
	return &ProjectOrchestratorOverlay{details: details, width: 80, height: 24, action: action}
}

func (o *ProjectOrchestratorOverlay) Sync(details ProjectOrchestratorDetails) {
	o.details = details
}
func (o *ProjectOrchestratorOverlay) Init() tea.Cmd { return nil }
func (o *ProjectOrchestratorOverlay) Title() string { return "Project Orchestrator" }
func (o *ProjectOrchestratorOverlay) Size() (int, int) {
	return ClampResponsiveDialogSize(36, 10, o.width, o.height)
}
func (o *ProjectOrchestratorOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		o.width, o.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case "s":
			if o.action != nil {
				return o, o.action("start")
			}
		case "a", "enter":
			if o.action != nil {
				return o, o.action("attach")
			}
		}
	}
	return o, nil
}
func (o *ProjectOrchestratorOverlay) View() string {
	w, h := o.Size()
	inner := max(1, w-4)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(o.details.Project),
		strings.TrimSpace(o.details.Status),
		fmt.Sprintf("ready %d  review %d", o.details.Ready, o.details.Review),
		fmt.Sprintf("waiting-human %d  owned-elsewhere %d", o.details.WaitingHuman, o.details.OwnedElsewhere),
		"",
		"s start   a/Enter attach   Esc close",
	}
	return lipgloss.NewStyle().Width(inner).Height(max(1, h-2)).Render(strings.Join(lines, "\n"))
}
