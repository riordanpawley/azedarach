package overlay

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

// PlanningStartMsg signals that planning should start
type PlanningStartMsg struct {
	Description string
}

// PlanningCompleteMsg signals that planning is complete
type PlanningCompleteMsg struct {
	Beads []domain.Task
}

// planningPhase represents the current UI phase
type planningPhase string

const (
	phaseInput    planningPhase = "input"
	phaseProgress planningPhase = "progress"
	phaseComplete planningPhase = "complete"
	phaseError    planningPhase = "error"
)

// PlanningOverlay provides a modal for AI-powered task planning
type PlanningOverlay struct {
	phase       planningPhase
	input       textinput.Model
	description textarea.Model
	state       domain.PlanningState
	styles      *Styles
	focusInput  bool // true for title input, false for description textarea
}

// NewPlanningOverlay creates a new planning overlay
func NewPlanningOverlay() *PlanningOverlay {
	// Title input for single-line description
	ti := textinput.New()
	ti.Placeholder = "Describe your feature..."
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 70

	// Description textarea for multi-line description
	ta := textarea.New()
	ta.Placeholder = "Enter detailed feature description..."
	ta.CharLimit = 2000
	ta.SetWidth(70)
	ta.SetHeight(8)

	return &PlanningOverlay{
		phase:       phaseInput,
		input:       ti,
		description: ta,
		state: domain.PlanningState{
			Status: domain.PlanningIdle,
		},
		styles:     New(),
		focusInput: true,
	}
}

// Init initializes the overlay
func (p *PlanningOverlay) Init() tea.Cmd {
	return textinput.Blink
}

// UpdateState updates the planning state (called from parent model)
func (p *PlanningOverlay) UpdateState(state domain.PlanningState) {
	p.state = state

	// Transition phases based on state
	switch state.Status {
	case domain.PlanningIdle:
		p.phase = phaseInput
	case domain.PlanningGenerating, domain.PlanningReviewing, domain.PlanningRefining, domain.PlanningCreatingBeads:
		p.phase = phaseProgress
	case domain.PlanningComplete:
		p.phase = phaseComplete
	case domain.PlanningErrorStatus:
		p.phase = phaseError
	}
}

// Update handles messages
func (p *PlanningOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch p.phase {
		case phaseInput:
			return p.handleInputPhase(msg)
		case phaseProgress:
			return p.handleProgressPhase(msg)
		case phaseComplete:
			return p.handleCompletePhase(msg)
		case phaseError:
			return p.handleErrorPhase(msg)
		}
	}

	// Update active input
	var cmd tea.Cmd
	if p.focusInput {
		p.input, cmd = p.input.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		p.description, cmd = p.description.Update(msg)
		cmds = append(cmds, cmd)
	}

	return p, tea.Batch(cmds...)
}

// handleInputPhase handles input phase keys
func (p *PlanningOverlay) handleInputPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return p, func() tea.Msg { return CloseOverlayMsg{} }

	case "tab":
		// Toggle between input and description
		if p.focusInput {
			p.focusInput = false
			p.input.Blur()
			p.description.Focus()
		} else {
			p.focusInput = true
			p.description.Blur()
			p.input.Focus()
		}
		return p, nil

	case "ctrl+s", "enter":
		// Submit if we have description
		desc := strings.TrimSpace(p.description.Value())
		if desc == "" {
			desc = strings.TrimSpace(p.input.Value())
		}

		if desc != "" {
			p.phase = phaseProgress
			return p, func() tea.Msg {
				return PlanningStartMsg{Description: desc}
			}
		}
		return p, nil

	case "ctrl+u":
		// Clear current field
		if p.focusInput {
			p.input.SetValue("")
		} else {
			p.description.SetValue("")
		}
		return p, nil
	}

	// Let the active field handle the input
	var cmd tea.Cmd
	if p.focusInput {
		p.input, cmd = p.input.Update(msg)
	} else {
		p.description, cmd = p.description.Update(msg)
	}
	return p, cmd
}

// handleProgressPhase handles progress phase keys
func (p *PlanningOverlay) handleProgressPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return p, func() tea.Msg { return CloseOverlayMsg{} }
	}
	return p, nil
}

// handleCompletePhase handles complete phase keys
func (p *PlanningOverlay) handleCompletePhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		return p, tea.Batch(
			func() tea.Msg {
				return PlanningCompleteMsg{Beads: p.state.CreatedBeads}
			},
			func() tea.Msg { return CloseOverlayMsg{} },
		)
	case "r":
		// Reset and plan another
		p.phase = phaseInput
		p.input.SetValue("")
		p.description.SetValue("")
		p.focusInput = true
		p.input.Focus()
		p.description.Blur()
		return p, nil
	}
	return p, nil
}

// handleErrorPhase handles error phase keys
func (p *PlanningOverlay) handleErrorPhase(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return p, func() tea.Msg { return CloseOverlayMsg{} }
	case "r":
		// Retry by going back to input
		p.phase = phaseInput
		return p, nil
	}
	return p, nil
}

// View renders the overlay
func (p *PlanningOverlay) View() string {
	switch p.phase {
	case phaseInput:
		return p.renderInputPhase()
	case phaseProgress:
		return p.renderProgressPhase()
	case phaseComplete:
		return p.renderCompletePhase()
	case phaseError:
		return p.renderErrorPhase()
	}
	return ""
}

// renderInputPhase renders the input phase
func (p *PlanningOverlay) renderInputPhase() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#cba6f7")).
		Bold(true)

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6c7086"))

	b.WriteString(titleStyle.Render("Plan a New Feature"))
	b.WriteString("\n\n")

	b.WriteString(helpStyle.Render("AI will create a plan with:"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("• Small, parallelizable tasks (30min-2hr each)"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("• Proper dependencies between tasks"))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("• An epic to group related work"))
	b.WriteString("\n\n")

	// Quick input (single line)
	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94e2d5")).
		Width(12)

	activeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89b4fa")).
		Bold(true)

	if p.focusInput {
		b.WriteString(activeStyle.Render("Quick:"))
	} else {
		b.WriteString(labelStyle.Render("Quick:"))
	}
	b.WriteString("  ")
	b.WriteString(p.input.View())
	b.WriteString("\n\n")

	// Detailed description (multiline)
	if !p.focusInput {
		b.WriteString(activeStyle.Render("Detailed:"))
	} else {
		b.WriteString(labelStyle.Render("Detailed:"))
	}
	b.WriteString("\n")
	b.WriteString(p.description.View())
	b.WriteString("\n\n")

	// Footer
	hints := []string{
		p.styles.MenuKey.Render("Tab") + " " + p.styles.Footer.Render("Switch fields"),
		p.styles.MenuKey.Render("Enter") + " " + p.styles.Footer.Render("Generate"),
		p.styles.MenuKey.Render("Ctrl+U") + " " + p.styles.Footer.Render("Clear"),
		p.styles.MenuKey.Render("Esc") + " " + p.styles.Footer.Render("Cancel"),
	}
	b.WriteString(p.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderProgressPhase renders the progress phase
func (p *PlanningOverlay) renderProgressPhase() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#cba6f7")).
		Bold(true)

	b.WriteString(titleStyle.Render("Planning in Progress"))
	b.WriteString("\n\n")

	// Status indicator
	b.WriteString(p.renderStatusIndicator())
	b.WriteString("\n\n")

	// Review progress if reviewing/refining
	if p.state.Status == domain.PlanningReviewing || p.state.Status == domain.PlanningRefining {
		b.WriteString(p.renderReviewProgress())
		b.WriteString("\n\n")
	}

	// Current plan if available
	if p.state.CurrentPlan != nil {
		b.WriteString(p.renderPlanSummary(p.state.CurrentPlan))
		b.WriteString("\n\n")
	}

	// Latest review feedback if available
	if len(p.state.ReviewHistory) > 0 {
		latest := p.state.ReviewHistory[len(p.state.ReviewHistory)-1]
		b.WriteString(p.renderReviewFeedback(&latest))
		b.WriteString("\n\n")
	}

	// Footer
	b.WriteString(p.styles.Footer.Render("Esc: cancel"))

	return b.String()
}

// renderCompletePhase renders the complete phase
func (p *PlanningOverlay) renderCompletePhase() string {
	var b strings.Builder

	successStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#a6e3a1")).
		Bold(true)

	b.WriteString(successStyle.Render("Planning Complete!"))
	b.WriteString("\n\n")

	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))
	b.WriteString(textStyle.Render(fmt.Sprintf("Created %d beads:", len(p.state.CreatedBeads))))
	b.WriteString("\n\n")

	// List created beads (limit to 10)
	count := len(p.state.CreatedBeads)
	if count > 10 {
		count = 10
	}

	for i := 0; i < count; i++ {
		bead := p.state.CreatedBeads[i]
		var color lipgloss.Color
		if bead.Type == domain.TypeEpic {
			color = lipgloss.Color("#cba6f7")
		} else {
			color = lipgloss.Color("#89b4fa")
		}

		idStyle := lipgloss.NewStyle().Foreground(color)
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))

		b.WriteString("  ")
		b.WriteString(idStyle.Render(bead.ID + ": "))
		b.WriteString(titleStyle.Render(truncateText(bead.Title, 50)))
		b.WriteString("\n")
	}

	if len(p.state.CreatedBeads) > 10 {
		subtext := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
		b.WriteString("\n")
		b.WriteString(subtext.Render(fmt.Sprintf("  ... and %d more", len(p.state.CreatedBeads)-10)))
	}

	b.WriteString("\n\n")

	// Footer
	hints := []string{
		p.styles.MenuKey.Render("Enter/Esc") + " " + p.styles.Footer.Render("Close"),
		p.styles.MenuKey.Render("r") + " " + p.styles.Footer.Render("Plan another"),
	}
	b.WriteString(p.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderErrorPhase renders the error phase
func (p *PlanningOverlay) renderErrorPhase() string {
	var b strings.Builder

	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f38ba8")).
		Bold(true)

	b.WriteString(errorStyle.Render("Planning Failed"))
	b.WriteString("\n\n")

	b.WriteString(errorStyle.Render(p.state.Error))
	b.WriteString("\n\n")

	// Footer
	hints := []string{
		p.styles.MenuKey.Render("r") + " " + p.styles.Footer.Render("Retry"),
		p.styles.MenuKey.Render("Esc") + " " + p.styles.Footer.Render("Close"),
	}
	b.WriteString(p.styles.Footer.Render(strings.Join(hints, " • ")))

	return b.String()
}

// renderStatusIndicator renders the status indicator
func (p *PlanningOverlay) renderStatusIndicator() string {
	colors := map[domain.PlanningStatus]lipgloss.Color{
		domain.PlanningIdle:           lipgloss.Color("#6c7086"),
		domain.PlanningGenerating:     lipgloss.Color("#f9e2af"),
		domain.PlanningReviewing:      lipgloss.Color("#89b4fa"),
		domain.PlanningRefining:       lipgloss.Color("#cba6f7"),
		domain.PlanningCreatingBeads:  lipgloss.Color("#a6e3a1"),
		domain.PlanningComplete:       lipgloss.Color("#a6e3a1"),
		domain.PlanningErrorStatus:    lipgloss.Color("#f38ba8"),
	}

	labels := map[domain.PlanningStatus]string{
		domain.PlanningIdle:           "Ready",
		domain.PlanningGenerating:     "Generating plan...",
		domain.PlanningReviewing:      "Reviewing plan...",
		domain.PlanningRefining:       "Refining plan...",
		domain.PlanningCreatingBeads:  "Creating beads...",
		domain.PlanningComplete:       "Complete!",
		domain.PlanningErrorStatus:    "Error",
	}

	color := colors[p.state.Status]
	label := labels[p.state.Status]

	style := lipgloss.NewStyle().Foreground(color)
	return style.Render("● " + label)
}

// renderReviewProgress renders the review progress bar
func (p *PlanningOverlay) renderReviewProgress() string {
	current := p.state.ReviewPass
	max := p.state.MaxReviewPasses

	filled := strings.Repeat("█", current)
	empty := strings.Repeat("░", max-current)

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	filledStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#313244"))

	return labelStyle.Render("Review pass: ") +
		filledStyle.Render(filled) +
		emptyStyle.Render(empty) +
		labelStyle.Render(fmt.Sprintf(" %d/%d", current, max))
}

// renderPlanSummary renders a summary of the plan
func (p *PlanningOverlay) renderPlanSummary(plan *domain.Plan) string {
	var b strings.Builder

	epicStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#cba6f7")).
		Bold(true)

	b.WriteString(epicStyle.Render("Epic: " + plan.EpicTitle))
	b.WriteString("\n\n")

	subtextStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	b.WriteString(subtextStyle.Render(truncateText(plan.Summary, 100) + "..."))
	b.WriteString("\n\n")

	blueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	b.WriteString(blueStyle.Render(fmt.Sprintf("%d tasks planned:", len(plan.Tasks))))
	b.WriteString("\n")

	// Show up to 8 tasks
	count := len(plan.Tasks)
	if count > 8 {
		count = 8
	}

	for i := 0; i < count; i++ {
		task := plan.Tasks[i]

		var indicator, color string
		if task.CanParallelize {
			indicator = "║"
			color = "#a6e3a1"
		} else {
			indicator = "│"
			color = "#f9e2af"
		}

		indicatorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))

		b.WriteString("  ")
		b.WriteString(indicatorStyle.Render(indicator))
		b.WriteString(" ")
		b.WriteString(titleStyle.Render(truncateText(task.Title, 50)))

		if len(task.DependsOn) > 0 {
			depStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
			b.WriteString(depStyle.Render(fmt.Sprintf(" (deps: %s)", strings.Join(task.DependsOn, ", "))))
		}

		b.WriteString("\n")
	}

	if len(plan.Tasks) > 8 {
		b.WriteString("\n")
		b.WriteString(subtextStyle.Render(fmt.Sprintf("  ... and %d more", len(plan.Tasks)-8)))
	}

	// Parallelization score
	if plan.ParallelizationScore > 0 {
		b.WriteString("\n\n")
		b.WriteString(subtextStyle.Render("Parallelization score: "))

		var scoreColor lipgloss.Color
		if plan.ParallelizationScore > 70 {
			scoreColor = lipgloss.Color("#a6e3a1")
		} else if plan.ParallelizationScore > 40 {
			scoreColor = lipgloss.Color("#f9e2af")
		} else {
			scoreColor = lipgloss.Color("#f38ba8")
		}

		scoreStyle := lipgloss.NewStyle().Foreground(scoreColor)
		b.WriteString(scoreStyle.Render(fmt.Sprintf("%d%%", plan.ParallelizationScore)))
	}

	return b.String()
}

// renderReviewFeedback renders review feedback
func (p *PlanningOverlay) renderReviewFeedback(feedback *domain.ReviewFeedback) string {
	var b strings.Builder

	subtextStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086"))
	b.WriteString(subtextStyle.Render("Quality score: "))

	var scoreColor lipgloss.Color
	if feedback.Score > 80 {
		scoreColor = lipgloss.Color("#a6e3a1")
	} else if feedback.Score > 50 {
		scoreColor = lipgloss.Color("#f9e2af")
	} else {
		scoreColor = lipgloss.Color("#f38ba8")
	}

	scoreStyle := lipgloss.NewStyle().Foreground(scoreColor)
	b.WriteString(scoreStyle.Render(fmt.Sprintf("%d/100", feedback.Score)))

	if feedback.IsApproved {
		approvedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
		b.WriteString(approvedStyle.Render(" (Approved)"))
	}

	if len(feedback.Issues) > 0 {
		b.WriteString("\n\n")
		yellowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
		b.WriteString(yellowStyle.Render("Issues:"))
		b.WriteString("\n")

		count := len(feedback.Issues)
		if count > 3 {
			count = 3
		}

		for i := 0; i < count; i++ {
			b.WriteString(subtextStyle.Render("  • " + truncateText(feedback.Issues[i], 60)))
			b.WriteString("\n")
		}
	}

	if len(feedback.TasksTooLarge) > 0 {
		b.WriteString("\n")
		redStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f38ba8"))
		b.WriteString(redStyle.Render(fmt.Sprintf("Tasks too large: %s", strings.Join(feedback.TasksTooLarge, ", "))))
	}

	return b.String()
}

// Title returns the overlay title
func (p *PlanningOverlay) Title() string {
	return "AI Planning"
}

// Size returns the overlay dimensions
func (p *PlanningOverlay) Size() (width, height int) {
	switch p.phase {
	case phaseInput:
		return 80, 28
	case phaseProgress:
		return 80, 35
	case phaseComplete:
		return 80, 25
	case phaseError:
		return 80, 15
	}
	return 80, 30
}

// truncateText truncates a string to a maximum length
func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
