package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

type CloseFailureAction string

const (
	CloseFailureActionRetry              CloseFailureAction = "retry"
	CloseFailureActionAIMerge            CloseFailureAction = "ai_merge"
	CloseFailureActionCreatePR           CloseFailureAction = "create_pr"
	CloseFailureActionCreateAncestor     CloseFailureAction = "create_ancestor"
	CloseFailureActionForceWorktree      CloseFailureAction = "force_worktree"
	CloseFailureActionAllowActiveSession CloseFailureAction = "allow_active_session"
	CloseFailureActionCloseCleanChildren CloseFailureAction = "close_clean_children"
	CloseFailureActionCancel             CloseFailureAction = "cancel"
)

type CloseFailureActionMsg struct {
	TaskID             string
	ProjectID          string
	ProjectName        string
	ProjectPath        string
	DaemonSocket       string
	BaseBranch         string
	ParentID           string
	SourceWorktree     string
	PreviousStatus     string
	TargetStatus       string
	Action             CloseFailureAction
	ForceWorktree      bool
	CloseCleanChildren bool
	AllowActiveSession bool
}

type CloseFailureDialogOptions struct {
	ProjectID               string
	ProjectName             string
	ProjectPath             string
	DaemonSocket            string
	BaseBranch              string
	ParentID                string
	SourceWorktree          string
	PreviousStatus          string
	TargetStatus            string
	ForceWorktree           bool
	CloseCleanChildren      bool
	AllowActiveSession      bool
	AllowAIMerge            bool
	AllowForceWorktree      bool
	AllowActiveSessionRetry bool
	AllowCloseCleanChildren bool
	AllowCreateAncestor     bool
}

type CloseFailureDialog struct {
	twoPaneDialogChrome
	dialogViewportState
	taskID  string
	reason  string
	options CloseFailureDialogOptions
	cursor  int
	styles  *Styles
	actions []closeFailureActionItem
}

type closeFailureActionItem struct {
	key         string
	label       string
	description string
	action      CloseFailureAction
	force       bool
	cleanKids   bool
	activeClose bool
}

func NewCloseFailureDialog(taskID, reason string, options CloseFailureDialogOptions) *CloseFailureDialog {
	dialog := &CloseFailureDialog{
		taskID:  strings.TrimSpace(taskID),
		reason:  strings.TrimSpace(reason),
		options: options,
		styles:  New(),
	}
	dialog.actions = dialog.closeFailureActions()
	return dialog
}

func (c *CloseFailureDialog) Init() tea.Cmd {
	return nil
}

func (c *CloseFailureDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.ApplyWindowSize(msg)
		return c, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if c.cursor < len(c.actions)-1 {
				c.cursor++
			}
			return c, nil
		case "k", "up":
			if c.cursor > 0 {
				c.cursor--
			}
			return c, nil
		case "enter":
			return c, c.selectionCmd(c.actions[c.cursor])
		case "r", "R":
			return c, c.selectionForAction(CloseFailureActionRetry)
		case "a", "A":
			return c, c.selectionForAction(CloseFailureActionAIMerge)
		case "p", "P":
			return c, c.selectionForAction(CloseFailureActionCreatePR)
		case "w", "W":
			return c, c.selectionForAction(CloseFailureActionCreateAncestor)
		case "f", "F":
			if c.options.AllowActiveSessionRetry {
				return c, c.selectionForAction(CloseFailureActionAllowActiveSession)
			}
			return c, c.selectionForAction(CloseFailureActionForceWorktree)
		case "c", "C":
			return c, c.selectionForAction(CloseFailureActionCloseCleanChildren)
		case "esc", "q":
			return c, c.selectionForAction(CloseFailureActionCancel)
		}
	}
	return c, nil
}

func (c *CloseFailureDialog) View() string {
	width, height := c.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            c.styles,
		width:             width,
		height:            height,
		title:             "CLOSE BLOCKED",
		rightSectionTitle: "Actions",
		breakpoint:        80,
		gap:               3,
		minLeft:           36,
		minRight:          24,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return c.renderBody(mode, width, height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return c.renderActions(width)
		},
	})
}

func (c *CloseFailureDialog) Title() string {
	return "Close Blocked"
}

func (c *CloseFailureDialog) Size() (width, height int) {
	reasonLines := len(wrapDescriptionLines(c.reasonOrFallback(), 64))
	return c.ClampResponsive(92, max(18, min(32, reasonLines+17)))
}

func (c *CloseFailureDialog) StatusBindings() []keybinds.Binding {
	bindings := []keybinds.Binding{
		{Key: "j/k", Description: "navigate"},
		{Key: "r", Description: "retry"},
	}
	if c.allowAIMerge() {
		bindings = append(bindings, keybinds.Binding{Key: "a", Description: "AI merge"})
	}
	if c.allowCreatePR() {
		bindings = append(bindings, keybinds.Binding{Key: "p", Description: "create PR"})
	}
	if c.options.AllowCreateAncestor {
		bindings = append(bindings, keybinds.Binding{Key: "w", Description: "create ancestor"})
	}
	if c.options.AllowActiveSessionRetry {
		bindings = append(bindings, keybinds.Binding{Key: "f", Description: "force active close"})
	} else if c.options.AllowForceWorktree {
		bindings = append(bindings, keybinds.Binding{Key: "f", Description: "force cleanup"})
	}
	if c.options.AllowCloseCleanChildren {
		bindings = append(bindings, keybinds.Binding{Key: "c", Description: "close clean children"})
	}
	bindings = append(bindings,
		keybinds.Binding{Key: "Enter", Description: "select"},
		keybinds.Binding{Key: "Esc/q", Description: "cancel"},
	)
	return bindings
}

func (c *CloseFailureDialog) renderBody(mode dialogLayoutMode, width, height int) string {
	maxWidth := max(24, width-2)
	if mode == dialogLayoutStacked {
		return c.renderCompactBody(maxWidth, height)
	}

	var b strings.Builder
	if c.taskID != "" {
		b.WriteString(c.styles.Footer.Render("Issue: " + c.taskID))
		b.WriteString("\n\n")
	}
	b.WriteString(c.styles.MenuItemActive.Render("The issue was not closed."))
	b.WriteString("\n\n")
	b.WriteString(c.styles.Footer.Render("Problem"))
	b.WriteString("\n")
	for _, line := range wrapDescriptionLines(c.reasonOrFallback(), maxWidth) {
		b.WriteString(c.styles.MenuItem.Render(line))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(c.styles.Footer.Render("Next steps"))
	b.WriteString("\n")
	for _, line := range c.nextStepLines() {
		b.WriteString(c.styles.MenuItem.Render(clampOverlayLineWidth(line, maxWidth)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *CloseFailureDialog) renderCompactBody(width, height int) string {
	var b strings.Builder
	if c.taskID != "" {
		b.WriteString(c.styles.MenuItem.Render("Issue: " + c.taskID))
		b.WriteString("\n")
	}
	b.WriteString(c.styles.MenuItemActive.Render("The issue was not closed."))
	b.WriteString("\n")

	lines := wrapDescriptionLines(c.reasonOrFallback(), width)
	maxReasonLines := max(1, height-2)
	if c.taskID != "" {
		maxReasonLines = max(1, height-3)
	}
	if len(lines) > maxReasonLines {
		lines = append(append([]string(nil), lines[:maxReasonLines]...), "...")
	}
	for _, line := range lines {
		b.WriteString(c.styles.MenuItem.Render(line))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *CloseFailureDialog) renderActions(width int) string {
	maxWidth := max(20, width-2)
	var b strings.Builder
	for i, action := range c.actions {
		prefix := "  "
		labelStyle := c.styles.MenuItem
		descStyle := c.styles.MenuItemDisabled
		if i == c.cursor {
			prefix = lipgloss.NewStyle().Foreground(styles.Blue).Render("> ")
			labelStyle = c.styles.MenuItemActive
			descStyle = c.styles.MenuItem
		}
		line := prefix + c.styles.MenuKey.Render(action.key) + " " + labelStyle.Render(action.label)
		b.WriteString(clampOverlayLineWidth(line, maxWidth))
		if action.description != "" {
			b.WriteString("\n")
			b.WriteString("  ")
			b.WriteString(descStyle.Render(clampOverlayLineWidth(action.description, max(8, maxWidth-2))))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *CloseFailureDialog) selectionForAction(action CloseFailureAction) tea.Cmd {
	for _, item := range c.actions {
		if item.action == action {
			return c.selectionCmd(item)
		}
	}
	return nil
}

func (c *CloseFailureDialog) selectionCmd(item closeFailureActionItem) tea.Cmd {
	return func() tea.Msg {
		return SelectionMsg{
			Key: "close_failure_action",
			Value: CloseFailureActionMsg{
				TaskID:             c.taskID,
				ProjectID:          strings.TrimSpace(c.options.ProjectID),
				ProjectName:        strings.TrimSpace(c.options.ProjectName),
				ProjectPath:        strings.TrimSpace(c.options.ProjectPath),
				DaemonSocket:       strings.TrimSpace(c.options.DaemonSocket),
				BaseBranch:         strings.TrimSpace(c.options.BaseBranch),
				ParentID:           strings.TrimSpace(c.options.ParentID),
				SourceWorktree:     strings.TrimSpace(c.options.SourceWorktree),
				PreviousStatus:     strings.TrimSpace(c.options.PreviousStatus),
				TargetStatus:       strings.TrimSpace(c.options.TargetStatus),
				Action:             item.action,
				ForceWorktree:      item.force,
				CloseCleanChildren: item.cleanKids,
				AllowActiveSession: item.activeClose,
			},
		}
	}
}

func (c *CloseFailureDialog) closeFailureActions() []closeFailureActionItem {
	actions := []closeFailureActionItem{{
		key:         "r",
		label:       "Retry close",
		description: "Run the close again after fixing the blocker.",
		action:      CloseFailureActionRetry,
		force:       c.options.ForceWorktree,
		cleanKids:   c.options.CloseCleanChildren,
		activeClose: c.options.AllowActiveSession,
	}}
	if c.allowAIMerge() {
		actions = append(actions, closeFailureActionItem{
			key:         "a",
			label:       "AI merge",
			description: "Launch an agent to resolve the blocked integration.",
			action:      CloseFailureActionAIMerge,
			force:       c.options.ForceWorktree,
			cleanKids:   c.options.CloseCleanChildren,
			activeClose: c.options.AllowActiveSession,
		})
	}
	if c.allowCreatePR() {
		actions = append(actions, closeFailureActionItem{
			key:         "p",
			label:       "Create PR",
			description: "Start the remote integration PR flow.",
			action:      CloseFailureActionCreatePR,
			force:       c.options.ForceWorktree,
			cleanKids:   c.options.CloseCleanChildren,
			activeClose: c.options.AllowActiveSession,
		})
	}
	if c.options.AllowCreateAncestor {
		actions = append(actions, closeFailureActionItem{
			key:         "w",
			label:       "Create ancestor & retry",
			description: "Create the missing parent worktree, then retry close.",
			action:      CloseFailureActionCreateAncestor,
			force:       c.options.ForceWorktree,
			cleanKids:   c.options.CloseCleanChildren,
			activeClose: c.options.AllowActiveSession,
		})
	}
	if c.options.AllowActiveSessionRetry {
		actions = append(actions, closeFailureActionItem{
			key:         "f",
			label:       "Force active close",
			description: "Retry and allow cleanup of the active AI session.",
			action:      CloseFailureActionAllowActiveSession,
			force:       c.options.ForceWorktree,
			cleanKids:   c.options.CloseCleanChildren,
			activeClose: true,
		})
	}
	if c.options.AllowForceWorktree && !c.options.AllowActiveSessionRetry {
		actions = append(actions, closeFailureActionItem{
			key:         "f",
			label:       "Force cleanup",
			description: "Retry and allow worktree removal to discard local files.",
			action:      CloseFailureActionForceWorktree,
			force:       true,
			cleanKids:   c.options.CloseCleanChildren,
			activeClose: c.options.AllowActiveSession,
		})
	}
	if c.options.AllowCloseCleanChildren {
		actions = append(actions, closeFailureActionItem{
			key:         "c",
			label:       "Close clean children",
			description: "Retry and include unresolved children with no local state.",
			action:      CloseFailureActionCloseCleanChildren,
			force:       c.options.ForceWorktree,
			cleanKids:   true,
			activeClose: c.options.AllowActiveSession,
		})
	}
	actions = append(actions, closeFailureActionItem{
		key:         "esc",
		label:       "Cancel",
		description: "Leave the issue in its previous status.",
		action:      CloseFailureActionCancel,
	})
	return actions
}

func (c *CloseFailureDialog) reasonOrFallback() string {
	if strings.TrimSpace(c.reason) != "" {
		return closeFailureReadableReason(c.reason)
	}
	return "The close request failed without details."
}

func (c *CloseFailureDialog) nextStepLines() []string {
	if c.allowCreatePR() {
		return []string{
			"- Create and merge the PR, or otherwise land the branch remotely.",
			"- Retry close after the refreshed remote base contains the branch tree.",
		}
	}
	lines := []string{"- Fix the blocker described above, then retry."}
	if c.allowAIMerge() {
		lines = append(lines, "- AI merge starts an agent when integration recovery needs help.")
	}
	if c.options.AllowActiveSessionRetry {
		lines = append(lines, "- Force active close stops the active session during close cleanup.")
	} else if c.options.AllowForceWorktree {
		lines = append(lines, "- Force cleanup discards modified or untracked worktree files.")
	}
	if c.options.AllowCloseCleanChildren {
		lines = append(lines, "- Close clean children only affects descendants without sessions, dirt, conflicts, or branch diff.")
	}
	if c.options.AllowCreateAncestor {
		lines = append(lines, "- Create ancestor & retry preserves child-to-parent integration.")
	}
	return lines
}

func (c *CloseFailureDialog) allowAIMerge() bool {
	return c.options.AllowAIMerge && closeFailureReasonSupportsAIMerge(c.reasonOrFallback())
}

func (c *CloseFailureDialog) allowCreatePR() bool {
	return closeFailureReasonSupportsCreatePR(c.reasonOrFallback())
}

func closeFailureReadableReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	const phasePrefix = "phase integrate_before_close for issue "
	phaseIndex := strings.Index(reason, phasePrefix)
	if phaseIndex == -1 {
		return reason
	}
	afterPhase := reason[phaseIndex+len(phasePrefix):]
	colonIndex := strings.Index(afterPhase, ":")
	if colonIndex == -1 {
		return reason
	}
	trimmed := strings.TrimSpace(afterPhase[colonIndex+1:])
	if trimmed == "" {
		return reason
	}
	return trimmed
}

func closeFailureReasonSupportsAIMerge(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "merge preflight would conflict") ||
		strings.Contains(reason, "predicted conflict") ||
		strings.Contains(reason, "merge would conflict") ||
		strings.Contains(reason, "merge conflict") ||
		strings.Contains(reason, "automatic merge failed") ||
		strings.Contains(reason, "conflict while merging") ||
		strings.Contains(reason, "conflicts:")
}

func closeFailureReasonSupportsCreatePR(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(reason, "origin workflow close will not merge") &&
		strings.Contains(reason, "still differs from origin/")
}
