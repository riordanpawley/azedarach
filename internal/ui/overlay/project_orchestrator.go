package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

var (
	projectOrchestratorProjectStyle = lipgloss.NewStyle().Foreground(styles.Mauve).Bold(true)
	projectOrchestratorStatusStyle  = lipgloss.NewStyle().Foreground(styles.Text)
	projectOrchestratorLiveStyle    = lipgloss.NewStyle().Foreground(styles.Green).Bold(true)
	projectOrchestratorIdleStyle    = lipgloss.NewStyle().Foreground(styles.Yellow).Bold(true)
	projectOrchestratorErrorStyle   = lipgloss.NewStyle().Foreground(styles.Red).Bold(true)
	projectOrchestratorLoadingStyle = lipgloss.NewStyle().Foreground(styles.Blue)
	projectOrchestratorReadyStyle   = lipgloss.NewStyle().Foreground(styles.Green)
	projectOrchestratorReviewStyle  = lipgloss.NewStyle().Foreground(styles.Mauve)
	projectOrchestratorWaitingStyle = lipgloss.NewStyle().Foreground(styles.Yellow)
	projectOrchestratorOwnedStyle   = lipgloss.NewStyle().Foreground(styles.Red)
	projectOrchestratorKeyStyle     = lipgloss.NewStyle().Foreground(styles.Blue).Bold(true)
	projectOrchestratorHintStyle    = lipgloss.NewStyle().Foreground(styles.Subtext0)
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
		case "x":
			if o.action != nil {
				return o, o.action("stop")
			}
		}
	}
	return o, nil
}
func (o *ProjectOrchestratorOverlay) View() string {
	w, h := o.Size()
	inner := max(1, w-4)
	lines := []string{
		projectOrchestratorProjectStyle.Render(o.details.Project),
		renderProjectOrchestratorStatus(o.details.Status),
		projectOrchestratorReadyStyle.Render(fmt.Sprintf("ready %d", o.details.Ready)) + "  " +
			projectOrchestratorReviewStyle.Render(fmt.Sprintf("review %d", o.details.Review)),
		projectOrchestratorWaitingStyle.Render(fmt.Sprintf("waiting-human %d", o.details.WaitingHuman)) + "  " +
			projectOrchestratorOwnedStyle.Render(fmt.Sprintf("owned-elsewhere %d", o.details.OwnedElsewhere)),
		"",
		renderProjectOrchestratorActions(),
	}
	return lipgloss.NewStyle().Width(inner).Height(max(1, h-2)).Render(strings.Join(lines, "\n"))
}

func renderProjectOrchestratorStatus(status string) string {
	status = strings.TrimSpace(status)
	lower := strings.ToLower(status)
	switch {
	case strings.Contains(lower, "unavailable"):
		return projectOrchestratorErrorStyle.Render(status)
	case strings.Contains(lower, "loading"):
		return projectOrchestratorLoadingStyle.Render(status)
	case strings.Contains(lower, "live=false"):
		return projectOrchestratorIdleStyle.Render(status)
	case strings.Contains(lower, "live=true") || strings.Contains(lower, "working"):
		return projectOrchestratorLiveStyle.Render(status)
	case strings.Contains(lower, "paused") || strings.Contains(lower, "inactive"):
		return projectOrchestratorIdleStyle.Render(status)
	default:
		return projectOrchestratorStatusStyle.Render(status)
	}
}

func renderProjectOrchestratorActions() string {
	action := func(key, label string) string {
		return projectOrchestratorKeyStyle.Render(key) + " " + projectOrchestratorHintStyle.Render(label)
	}
	return strings.Join([]string{
		action("s", "start"),
		action("a/Enter", "attach"),
		action("x", "stop"),
		action("Esc", "close"),
	}, "   ")
}
