package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/services/diagnostics"
)

// ReconciliationAction identifies an explicit session reconciliation operation.
type ReconciliationAction string

const (
	ReconciliationActionAdoptIndicator      ReconciliationAction = "adopt_indicator"
	ReconciliationActionClearIndicator      ReconciliationAction = "clear_indicator"
	ReconciliationActionTerminateOrphanTmux ReconciliationAction = "terminate_orphan_tmux"
)

// OpenSessionReconciliationOverlayMsg requests opening a reconciliation overlay.
type OpenSessionReconciliationOverlayMsg struct {
	Mismatch diagnostics.SessionMismatch
	Trigger  string
}

// SessionReconciliationActionMsg requests applying a reconciliation operation.
type SessionReconciliationActionMsg struct {
	Mismatch diagnostics.SessionMismatch
	Action   ReconciliationAction
	Trigger  string
}

type reconciliationActionRow struct {
	Key     string
	Action  ReconciliationAction
	Label   string
	Enabled bool
}

// SessionReconciliationOverlay renders explicit reconciliation actions for one mismatch.
type SessionReconciliationOverlay struct {
	mismatch diagnostics.SessionMismatch
	trigger  string
	actions  []reconciliationActionRow
	cursor   int
	styles   *Styles
}

// NewSessionReconciliationOverlay creates a reconciliation chooser for one mismatch.
func NewSessionReconciliationOverlay(mismatch diagnostics.SessionMismatch, trigger string) *SessionReconciliationOverlay {
	overlay := &SessionReconciliationOverlay{
		mismatch: mismatch,
		trigger:  strings.TrimSpace(trigger),
		styles:   New(),
	}
	overlay.actions = overlay.buildActions()
	overlay.ensureCursorOnEnabled()
	return overlay
}

// Init implements tea.Model.
func (o *SessionReconciliationOverlay) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (o *SessionReconciliationOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		case "j", "down":
			o.moveCursor(1)
			return o, nil
		case "k", "up":
			o.moveCursor(-1)
			return o, nil
		case "enter":
			return o, o.selectCurrentAction()
		default:
			return o, o.selectByKey(msg.String())
		}
	}
	return o, nil
}

// View implements tea.Model.
func (o *SessionReconciliationOverlay) View() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa")).Bold(true)
	infoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#94e2d5"))

	b.WriteString(headerStyle.Render("Session/Tmux Reconciliation"))
	b.WriteString("\n\n")
	b.WriteString(infoStyle.Render(fmt.Sprintf("Issue: %s", o.mismatch.IssueID)))
	b.WriteString("\n")
	b.WriteString(infoStyle.Render(fmt.Sprintf("Mismatch: %s", o.describeMismatch())))
	b.WriteString("\n\n")

	for i, action := range o.actions {
		style := o.styles.MenuItem
		keyStyle := o.styles.MenuKey
		if !action.Enabled {
			style = o.styles.MenuItemDisabled
			keyStyle = o.styles.MenuKeyDisabled
		} else if i == o.cursor {
			style = o.styles.MenuItemActive
		}

		b.WriteString(keyStyle.Render("[" + action.Key + "]"))
		b.WriteString(" ")
		b.WriteString(style.Render(action.Label))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#6c7086")).Render("j/k: select • Enter: apply • q/Esc: close"))
	return b.String()
}

// Title identifies the overlay in the header.
func (o *SessionReconciliationOverlay) Title() string {
	return fmt.Sprintf("Reconcile Session - %s", o.mismatch.IssueID)
}

// Size returns preferred dimensions.
func (o *SessionReconciliationOverlay) Size() (width, height int) {
	return 72, 16
}

func (o *SessionReconciliationOverlay) buildActions() []reconciliationActionRow {
	canAdopt := o.mismatch.TmuxPresent && !o.mismatch.IndicatorPresent
	canClear := o.mismatch.IndicatorPresent && !o.mismatch.TmuxPresent
	canTerminate := o.mismatch.TmuxPresent && !o.mismatch.IndicatorPresent

	return []reconciliationActionRow{
		{
			Key:     "a",
			Action:  ReconciliationActionAdoptIndicator,
			Label:   "Adopt indicator (trust tmux session as source of truth)",
			Enabled: canAdopt,
		},
		{
			Key:     "c",
			Action:  ReconciliationActionClearIndicator,
			Label:   "Clear indicator (drop stale board session indicator)",
			Enabled: canClear,
		},
		{
			Key:     "x",
			Action:  ReconciliationActionTerminateOrphanTmux,
			Label:   "Terminate orphan tmux session",
			Enabled: canTerminate,
		},
	}
}

func (o *SessionReconciliationOverlay) describeMismatch() string {
	switch o.mismatch.Kind {
	case diagnostics.SessionMismatchKindOrphanTmux:
		return "tmux session exists but board indicator is missing"
	case diagnostics.SessionMismatchKindStaleIndicator:
		return "board indicator exists but tmux session is missing"
	default:
		return "session indicator and tmux state diverged"
	}
}

func (o *SessionReconciliationOverlay) ensureCursorOnEnabled() {
	for i, action := range o.actions {
		if action.Enabled {
			o.cursor = i
			return
		}
	}
	o.cursor = 0
}

func (o *SessionReconciliationOverlay) moveCursor(delta int) {
	if len(o.actions) == 0 {
		return
	}
	for i := 1; i <= len(o.actions); i++ {
		next := (o.cursor + delta*i + len(o.actions)*2) % len(o.actions)
		if o.actions[next].Enabled {
			o.cursor = next
			return
		}
	}
}

func (o *SessionReconciliationOverlay) selectCurrentAction() tea.Cmd {
	if o.cursor < 0 || o.cursor >= len(o.actions) {
		return nil
	}
	selected := o.actions[o.cursor]
	if !selected.Enabled {
		return nil
	}
	return o.selectAction(selected)
}

func (o *SessionReconciliationOverlay) selectByKey(key string) tea.Cmd {
	for _, action := range o.actions {
		if action.Key == key && action.Enabled {
			return o.selectAction(action)
		}
	}
	return nil
}

func (o *SessionReconciliationOverlay) selectAction(action reconciliationActionRow) tea.Cmd {
	return func() tea.Msg {
		return SessionReconciliationActionMsg{
			Mismatch: o.mismatch,
			Action:   action.Action,
			Trigger:  o.trigger,
		}
	}
}

func (o *SessionReconciliationOverlay) isActionEnabled(action ReconciliationAction) bool {
	for _, row := range o.actions {
		if row.Action == action {
			return row.Enabled
		}
	}
	return false
}
