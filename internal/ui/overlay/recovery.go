package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// RecoveryNotificationItem is a single recoverable async-failure notification.
type RecoveryNotificationItem struct {
	ID           string
	IssueID      string
	Title        string
	Message      string
	RecoverLabel string
	CreatedAt    time.Time
}

// RecoveryOverlay shows queued recoverable async failures and lets users recover or ignore them.
type RecoveryOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	items    []RecoveryNotificationItem
	selected int
	styles   *Styles
}

func NewRecoveryOverlay(items []RecoveryNotificationItem) *RecoveryOverlay {
	copied := append([]RecoveryNotificationItem(nil), items...)
	return &RecoveryOverlay{
		items:    copied,
		selected: 0,
		styles:   New(),
	}
}

func (r *RecoveryOverlay) Init() tea.Cmd {
	return nil
}

func (r *RecoveryOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.ApplyWindowSize(msg)
		return r, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if r.selected < len(r.items)-1 {
				r.selected++
			}
			return r, nil
		case "k", "up":
			if r.selected > 0 {
				r.selected--
			}
			return r, nil
		case "r", "enter":
			item, ok := r.current()
			if !ok {
				return r, nil
			}
			return r, func() tea.Msg {
				return SelectionMsg{Key: "async_recovery_recover", Value: item.ID}
			}
		case "x":
			item, ok := r.current()
			if !ok {
				return r, nil
			}
			return r, func() tea.Msg {
				return SelectionMsg{Key: "async_recovery_ignore", Value: item.ID}
			}
		case "X":
			return r, func() tea.Msg {
				return SelectionMsg{Key: "async_recovery_ignore_all"}
			}
		case "esc", "q":
			return r, func() tea.Msg { return CloseOverlayMsg{} }
		}
	}
	return r, nil
}

func (r *RecoveryOverlay) View() string {
	width, height := r.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            r.styles,
		width:             width,
		height:            height,
		title:             r.Title(),
		rightSectionTitle: "Actions",
		breakpoint:        84,
		gap:               2,
		minLeft:           44,
		minRight:          22,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return r.renderItems(width)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(r.styles, []keybinds.Binding{
				{Key: "j/k", Description: "move"},
				{Key: "R/Enter", Description: "recover selected"},
				{Key: "x", Description: "ignore selected"},
				{Key: "X", Description: "ignore all"},
				{Key: "Esc/q", Description: "close"},
			}, width)
		},
	})
}

func (r *RecoveryOverlay) renderItems(width int) string {
	if len(r.items) == 0 {
		return r.styles.MenuItemDisabled.Render("No queued async failure notifications")
	}

	var b strings.Builder
	b.WriteString(r.styles.MenuItem.Render(fmt.Sprintf("Queued recoverable async failures: %d", len(r.items))))
	b.WriteString("\n\n")
	for i, item := range r.items {
		prefix := "  "
		style := r.styles.MenuItem
		if i == r.selected {
			prefix = "> "
			style = r.styles.MenuItemActive
		}
		issue := strings.TrimSpace(item.IssueID)
		if issue == "" {
			issue = "(unknown issue)"
		}
		stamp := ""
		if !item.CreatedAt.IsZero() {
			stamp = item.CreatedAt.Local().Format("15:04:05")
		}
		summary := strings.TrimSpace(item.Title)
		if summary == "" {
			summary = "Async failure"
		}
		line := fmt.Sprintf("%s[%s] %s", prefix, issue, summary)
		if stamp != "" {
			line += " @" + stamp
		}
		b.WriteString(style.Render(ansi.Truncate(line, max(8, width), "...")))
		b.WriteString("\n")
	}

	if item, ok := r.current(); ok {
		b.WriteString("\n")
		details := strings.TrimSpace(item.Message)
		if details == "" {
			details = "No details provided"
		}
		recoverLabel := strings.TrimSpace(item.RecoverLabel)
		if recoverLabel == "" {
			recoverLabel = "retry"
		}
		b.WriteString(r.styles.MenuKey.Render("Selected:"))
		b.WriteString("\n")
		b.WriteString(r.styles.MenuItem.Render(ansi.Truncate(details, max(8, width), "...")))
		b.WriteString("\n")
		b.WriteString(r.styles.MenuItem.Render("Recover action: " + recoverLabel))
	}

	return strings.TrimRight(b.String(), "\n")
}

func (r *RecoveryOverlay) current() (RecoveryNotificationItem, bool) {
	if len(r.items) == 0 {
		return RecoveryNotificationItem{}, false
	}
	if r.selected < 0 {
		r.selected = 0
	}
	if r.selected >= len(r.items) {
		r.selected = len(r.items) - 1
	}
	return r.items[r.selected], true
}

func (r *RecoveryOverlay) Title() string {
	return "Recovery"
}

func (r *RecoveryOverlay) Size() (width, height int) {
	baseHeight := 16 + min(8, len(r.items))
	return r.ClampResponsive(92, max(16, min(30, baseHeight)))
}

func (r *RecoveryOverlay) StatusBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "j/k", Description: "move"},
		{Key: "R", Description: "recover"},
		{Key: "x/X", Description: "ignore"},
		{Key: "Esc", Description: "close"},
	}
}
