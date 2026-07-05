package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// NotificationHistoryItem is one retained TUI notification.
type NotificationHistoryItem struct {
	ID        string
	CreatedAt time.Time
	Level     string
	Reference string
	Message   string
	Read      bool
	Dismissed bool
}

// NotificationHistoryOverlay shows retained notifications newest-first.
type NotificationHistoryOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	items    []NotificationHistoryItem
	selected int
	scroll   int
	styles   *Styles
}

func NewNotificationHistoryOverlay(items []NotificationHistoryItem) *NotificationHistoryOverlay {
	copied := append([]NotificationHistoryItem(nil), items...)
	return &NotificationHistoryOverlay{
		items:  copied,
		styles: New(),
	}
}

func (o *NotificationHistoryOverlay) Init() tea.Cmd {
	return nil
}

func (o *NotificationHistoryOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		o.ApplyWindowSize(msg)
		return o, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlD:
			o.scroll = min(o.maxScroll(), o.scroll+o.halfPageStep())
			return o, nil
		case tea.KeyCtrlU:
			o.scroll = max(0, o.scroll-o.halfPageStep())
			return o, nil
		}
		switch msg.String() {
		case "j", "down":
			if o.selected < len(o.items)-1 {
				o.selected++
			}
			return o, nil
		case "k", "up":
			if o.selected > 0 {
				o.selected--
			}
			return o, nil
		case "g":
			o.selected = 0
			o.scroll = 0
			return o, nil
		case "G":
			if len(o.items) > 0 {
				o.selected = len(o.items) - 1
				o.scroll = o.maxScroll()
			}
			return o, nil
		case "esc", "q", "backspace":
			return o, func() tea.Msg { return CloseOverlayMsg{} }
		}
	}
	return o, nil
}

func (o *NotificationHistoryOverlay) View() string {
	width, height := o.Size()
	return renderDialogTwoPane(dialogLayoutConfig{
		styles:            o.styles,
		width:             width,
		height:            height,
		title:             o.Title(),
		rightSectionTitle: "Actions",
		breakpoint:        84,
		gap:               2,
		minLeft:           48,
		minRight:          20,
		leftFocused:       true,
		renderLeft: func(mode dialogLayoutMode, width, height int) string {
			return o.renderItems(width, height)
		},
		renderRight: func(mode dialogLayoutMode, width, height int) string {
			return renderDialogActions(o.styles, o.actionBindings(), width)
		},
	})
}

func (o *NotificationHistoryOverlay) renderItems(width, height int) string {
	if len(o.items) == 0 {
		return o.styles.MenuItemDisabled.Render("No notifications yet")
	}

	visibleRows := max(1, height-7)
	o.ensureSelectionVisible(visibleRows)
	start := o.scroll
	end := min(len(o.items), start+visibleRows)

	var b strings.Builder
	b.WriteString(o.styles.MenuItem.Render(fmt.Sprintf("Recent notifications: %d", len(o.items))))
	b.WriteString("\n\n")
	for i := start; i < end; i++ {
		item := o.items[i]
		prefix := "  "
		style := o.styles.MenuItem
		if i == o.selected {
			prefix = "> "
			style = o.styles.MenuItemActive
		}
		state := "unread"
		if item.Read {
			state = "read"
		}
		if item.Dismissed {
			state += ", dismissed"
		}
		line := fmt.Sprintf("%s%s  %-7s  %-14s  %s",
			prefix,
			shortNotificationTime(item.CreatedAt),
			item.Level,
			emptyDefault(item.Reference, "-"),
			state,
		)
		b.WriteString(style.Render(ansi.Truncate(line, max(8, width), "...")))
		b.WriteString("\n")
	}

	if item, ok := o.current(); ok {
		b.WriteString("\n")
		b.WriteString(o.styles.MenuKey.Render("Selected:"))
		b.WriteString("\n")
		for _, line := range wrapNotificationMessage(item.Message, width) {
			b.WriteString(o.styles.MenuItem.Render(line))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (o *NotificationHistoryOverlay) Title() string {
	return "Notification History"
}

func (o *NotificationHistoryOverlay) Size() (width, height int) {
	baseHeight := 15 + min(10, len(o.items))
	return o.ClampResponsive(92, max(16, min(30, baseHeight)))
}

func (o *NotificationHistoryOverlay) StatusBindings() []keybinds.Binding {
	return o.actionBindings()
}

func (o *NotificationHistoryOverlay) actionBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "j/k", Description: "move"},
		{Key: "ctrl+u/d", Description: "half-page"},
		{Key: "g/G", Description: "top/bottom"},
		{Key: "Esc/q", Description: "close"},
	}
}

func (o *NotificationHistoryOverlay) current() (NotificationHistoryItem, bool) {
	if len(o.items) == 0 {
		return NotificationHistoryItem{}, false
	}
	if o.selected < 0 {
		o.selected = 0
	}
	if o.selected >= len(o.items) {
		o.selected = len(o.items) - 1
	}
	return o.items[o.selected], true
}

func (o *NotificationHistoryOverlay) ensureSelectionVisible(visibleRows int) {
	if len(o.items) == 0 {
		o.selected = 0
		o.scroll = 0
		return
	}
	if o.selected < 0 {
		o.selected = 0
	}
	if o.selected >= len(o.items) {
		o.selected = len(o.items) - 1
	}
	if visibleRows < 1 {
		visibleRows = 1
	}
	maxScroll := max(0, len(o.items)-visibleRows)
	if o.scroll > maxScroll {
		o.scroll = maxScroll
	}
	if o.scroll < 0 {
		o.scroll = 0
	}
	if o.selected < o.scroll {
		o.scroll = o.selected
	}
	if o.selected >= o.scroll+visibleRows {
		o.scroll = o.selected - visibleRows + 1
	}
}

func (o *NotificationHistoryOverlay) maxScroll() int {
	_, height := o.Size()
	visibleRows := max(1, height-11)
	return max(0, len(o.items)-visibleRows)
}

func (o *NotificationHistoryOverlay) halfPageStep() int {
	_, height := o.Size()
	step := height / 2
	if step < 1 {
		return 1
	}
	return step
}

func shortNotificationTime(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.Local().Format("15:04:05")
}

func emptyDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func wrapNotificationMessage(message string, width int) []string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "No details provided"
	}
	width = max(8, width)
	var lines []string
	for len(message) > 0 {
		if ansi.StringWidth(message) <= width {
			lines = append(lines, message)
			break
		}
		line := ansi.Truncate(message, width, "")
		line = strings.TrimRight(line, " ")
		if line == "" {
			line = ansi.Truncate(message, width, "...")
		}
		lines = append(lines, line)
		message = strings.TrimSpace(strings.TrimPrefix(message, line))
		if len(lines) >= 6 {
			if message != "" {
				lines[len(lines)-1] = ansi.Truncate(lines[len(lines)-1]+" ...", width, "...")
			}
			break
		}
	}
	return lines
}
