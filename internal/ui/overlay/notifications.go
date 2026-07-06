package overlay

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// NotificationHistoryItem is one retained TUI notification.
type NotificationHistoryItem struct {
	ID             string
	DaemonNoticeID string
	CreatedAt      time.Time
	Level          string
	Category       string
	State          string
	Reference      string
	ScopeType      string
	ScopeID        string
	OperationID    string
	Message        string
	Detail         string
	Read           bool
	Dismissed      bool
	Actions        []protocol.NoticeAction
}

// NotificationActionCenterMsg is emitted when the user chooses a notification action.
type NotificationActionCenterMsg struct {
	NoticeID       string
	DaemonNoticeID string
	ActionID       string
	Kind           string
	Label          string
	ScopeType      string
	ScopeID        string
	OperationID    string
	Details        string
	Read           bool
}

// NotificationHistoryOverlay shows retained notifications newest-first.
type NotificationHistoryOverlay struct {
	twoPaneDialogChrome
	dialogViewportState
	items    []NotificationHistoryItem
	selected int
	scroll   int
	filter   notificationActionCenterFilter
	styles   *Styles
}

type notificationActionCenterFilter int

const (
	notificationActionFilterAll notificationActionCenterFilter = iota
	notificationActionFilterUnread
	notificationActionFilterErrors
	notificationActionFilterActionable
	notificationActionFilterDismissed
	notificationActionFilterCount
)

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
		case "tab":
			o.cycleFilter(1)
			return o, nil
		case "shift+tab":
			o.cycleFilter(-1)
			return o, nil
		case "j", "down":
			if o.selected < len(o.filteredItems())-1 {
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
			if count := len(o.filteredItems()); count > 0 {
				o.selected = count - 1
				o.scroll = o.maxScroll()
			}
			return o, nil
		case "enter", "a":
			if action, ok := o.primaryAction(); ok {
				return o, func() tea.Msg { return action }
			}
			return o, nil
		case "m":
			if item, ok := o.current(); ok {
				action := o.actionMsg(item, "mark_read", "mark_read", "Mark read")
				action.Read = true
				if item.Read {
					action.ActionID = "mark_unread"
					action.Kind = "mark_unread"
					action.Label = "Mark unread"
					action.Read = false
				}
				return o, func() tea.Msg { return overlaySelection("notification_mark_read", action) }
			}
			return o, nil
		case "d":
			if item, ok := o.current(); ok {
				return o, func() tea.Msg {
					return overlaySelection("notification_dismiss", o.actionMsg(item, "dismiss", "dismiss", "Dismiss"))
				}
			}
			return o, nil
		case "D":
			return o, func() tea.Msg { return overlaySelection("notification_dismiss_all", o.dismissAllMsg()) }
		case "o":
			return o.emitLocalAction("open_task", "client.open_task", "Open task")
		case "w":
			return o.emitLocalAction("open_workspace", "client.open_workspace", "Open workspace")
		case "l":
			return o.emitLocalAction("open_logs", "client.open_logs", "Open logs")
		case "c":
			return o.emitLocalAction("copy_details", "client.copy_details", "Copy details")
		case "r":
			if action, ok := o.actionByKind("retry"); ok {
				return o, func() tea.Msg { return action }
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
	filtered := o.filteredItems()
	if len(filtered) == 0 {
		return o.styles.MenuItemDisabled.Render("No notifications yet")
	}

	detailLines := o.renderSelectedDetails(width)
	visibleRows := max(1, height-5-len(detailLines))
	o.ensureSelectionVisible(visibleRows)
	start := o.scroll
	end := min(len(filtered), start+visibleRows)

	var b strings.Builder
	b.WriteString(o.styles.MenuItem.Render(fmt.Sprintf("%s: %d", o.filterLabel(), len(filtered))))
	b.WriteString("\n\n")
	lastGroup := ""
	for i := start; i < end; i++ {
		item := filtered[i]
		group := notificationGroupLabel(item)
		if group != lastGroup {
			b.WriteString(o.styles.MenuKey.Render(ansi.Truncate(group, max(8, width), "...")))
			b.WriteString("\n")
			lastGroup = group
		}
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
		actionMark := " "
		if notificationItemActionable(item) {
			actionMark = "*"
		}
		line := fmt.Sprintf("%s%s %s %-7s  %-14s  %s",
			prefix,
			shortNotificationTime(item.CreatedAt),
			actionMark,
			item.Level,
			emptyDefault(item.Reference, "-"),
			state,
		)
		b.WriteString(style.Render(ansi.Truncate(line, max(8, width), "...")))
		b.WriteString("\n")
	}

	if len(detailLines) > 0 {
		b.WriteString("\n")
		for _, line := range detailLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (o *NotificationHistoryOverlay) renderSelectedDetails(width int) []string {
	item, ok := o.current()
	if !ok {
		return nil
	}
	lines := []string{o.styles.MenuKey.Render("Selected:")}
	for _, line := range wrapNotificationMessage(item.Message, width) {
		lines = append(lines, o.styles.MenuItem.Render(line))
	}
	if strings.TrimSpace(item.Detail) != "" {
		for _, line := range wrapNotificationMessage(item.Detail, width) {
			lines = append(lines, o.styles.MenuItemDisabled.Render(line))
		}
	}
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return lines
}

func (o *NotificationHistoryOverlay) Title() string {
	return "Notification Action Center"
}

func (o *NotificationHistoryOverlay) Size() (width, height int) {
	baseHeight := 15 + min(10, len(o.items))
	return o.ClampResponsive(92, max(16, min(30, baseHeight)))
}

func (o *NotificationHistoryOverlay) StatusBindings() []keybinds.Binding {
	return o.actionBindings()
}

func (o *NotificationHistoryOverlay) actionBindings() []keybinds.Binding {
	bindings := []keybinds.Binding{
		{Key: "j/k", Description: "move"},
		{Key: "Tab", Description: "filter"},
		{Key: "ctrl+u/d", Description: "half-page"},
		{Key: "g/G", Description: "top/bottom"},
		{Key: "m", Description: "read/unread"},
		{Key: "d/D", Description: "dismiss"},
		{Key: "Enter/a", Description: "action"},
		{Key: "o/w", Description: "open"},
		{Key: "l/c", Description: "logs/copy"},
		{Key: "Esc/q", Description: "close"},
	}
	if item, ok := o.current(); ok {
		for _, action := range item.Actions {
			label := strings.TrimSpace(action.Label)
			if label == "" {
				label = strings.TrimSpace(action.ActionID)
			}
			if label == "" {
				continue
			}
			key := actionKeyHint(action)
			if key == "" {
				key = "a"
			}
			if !action.Enabled && strings.TrimSpace(action.DisabledReason) != "" {
				label += " unavailable"
			}
			bindings = append(bindings, keybinds.Binding{Key: key, Description: label})
		}
	}
	return bindings
}

func (o *NotificationHistoryOverlay) current() (NotificationHistoryItem, bool) {
	filtered := o.filteredItems()
	if len(filtered) == 0 {
		return NotificationHistoryItem{}, false
	}
	if o.selected < 0 {
		o.selected = 0
	}
	if o.selected >= len(filtered) {
		o.selected = len(filtered) - 1
	}
	return filtered[o.selected], true
}

func (o *NotificationHistoryOverlay) ensureSelectionVisible(visibleRows int) {
	count := len(o.filteredItems())
	if count == 0 {
		o.selected = 0
		o.scroll = 0
		return
	}
	if o.selected < 0 {
		o.selected = 0
	}
	if o.selected >= count {
		o.selected = count - 1
	}
	if visibleRows < 1 {
		visibleRows = 1
	}
	maxScroll := max(0, count-visibleRows)
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
	return max(0, len(o.filteredItems())-visibleRows)
}

func (o *NotificationHistoryOverlay) halfPageStep() int {
	_, height := o.Size()
	step := height / 2
	if step < 1 {
		return 1
	}
	return step
}

func (o *NotificationHistoryOverlay) filteredItems() []NotificationHistoryItem {
	out := make([]NotificationHistoryItem, 0, len(o.items))
	for _, item := range o.items {
		switch o.filter {
		case notificationActionFilterUnread:
			if item.Read || item.Dismissed {
				continue
			}
		case notificationActionFilterErrors:
			if strings.TrimSpace(strings.ToLower(item.Level)) != "error" {
				continue
			}
		case notificationActionFilterActionable:
			if !notificationItemActionable(item) {
				continue
			}
		case notificationActionFilterDismissed:
			if !item.Dismissed {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func (o *NotificationHistoryOverlay) cycleFilter(delta int) {
	next := int(o.filter) + delta
	for next < 0 {
		next += int(notificationActionFilterCount)
	}
	o.filter = notificationActionCenterFilter(next % int(notificationActionFilterCount))
	o.selected = 0
	o.scroll = 0
}

func (o *NotificationHistoryOverlay) filterLabel() string {
	switch o.filter {
	case notificationActionFilterUnread:
		return "Unread"
	case notificationActionFilterErrors:
		return "Errors"
	case notificationActionFilterActionable:
		return "Actionable"
	case notificationActionFilterDismissed:
		return "Dismissed"
	default:
		return "All notifications"
	}
}

func (o *NotificationHistoryOverlay) primaryAction() (tea.Msg, bool) {
	if action, ok := o.actionByKind("retry"); ok {
		return action, true
	}
	for _, kind := range []string{"client.open_workspace", "client.open_task", "client.open_logs", "client.copy_details"} {
		if action, ok := o.actionByKind(kind); ok {
			return action, true
		}
	}
	item, ok := o.current()
	if !ok {
		return nil, false
	}
	for _, action := range item.Actions {
		if action.Enabled && strings.TrimSpace(action.ActionID) != "" {
			return overlaySelection("notification_action", o.actionMsg(item, action.ActionID, action.Kind, action.Label)), true
		}
	}
	return nil, false
}

func (o *NotificationHistoryOverlay) actionByKind(kind string) (tea.Msg, bool) {
	item, ok := o.current()
	if !ok {
		return nil, false
	}
	kind = strings.TrimSpace(kind)
	for _, action := range item.Actions {
		if !action.Enabled {
			continue
		}
		actionID := strings.TrimSpace(action.ActionID)
		actionKind := strings.TrimSpace(action.Kind)
		if strings.EqualFold(actionID, kind) ||
			strings.EqualFold(actionKind, kind) ||
			(kind == "retry" && (strings.HasPrefix(actionID, "retry") || strings.HasPrefix(actionKind, "retry"))) {
			return overlaySelection("notification_action", o.actionMsg(item, action.ActionID, action.Kind, action.Label)), true
		}
	}
	return nil, false
}

func (o *NotificationHistoryOverlay) emitLocalAction(actionID, kind, label string) (tea.Model, tea.Cmd) {
	if action, ok := o.actionByKind(kind); ok {
		return o, func() tea.Msg { return action }
	}
	if item, ok := o.current(); ok {
		return o, func() tea.Msg {
			return overlaySelection("notification_action", o.actionMsg(item, actionID, kind, label))
		}
	}
	return o, nil
}

func (o *NotificationHistoryOverlay) actionMsg(item NotificationHistoryItem, actionID, kind, label string) NotificationActionCenterMsg {
	return NotificationActionCenterMsg{
		NoticeID:       strings.TrimSpace(item.ID),
		DaemonNoticeID: strings.TrimSpace(item.DaemonNoticeID),
		ActionID:       strings.TrimSpace(actionID),
		Kind:           strings.TrimSpace(kind),
		Label:          strings.TrimSpace(label),
		ScopeType:      strings.TrimSpace(item.ScopeType),
		ScopeID:        strings.TrimSpace(item.ScopeID),
		OperationID:    strings.TrimSpace(item.OperationID),
		Details:        notificationDetails(item),
	}
}

func (o *NotificationHistoryOverlay) dismissAllMsg() []NotificationActionCenterMsg {
	filtered := o.filteredItems()
	out := make([]NotificationActionCenterMsg, 0, len(filtered))
	for _, item := range filtered {
		if item.Dismissed {
			continue
		}
		out = append(out, o.actionMsg(item, "dismiss", "dismiss", "Dismiss"))
	}
	return out
}

func overlaySelection(key string, value any) SelectionMsg {
	return SelectionMsg{Key: key, Value: value}
}

func notificationItemActionable(item NotificationHistoryItem) bool {
	if !item.Dismissed {
		return true
	}
	for _, action := range item.Actions {
		if action.Enabled {
			return true
		}
	}
	return false
}

func notificationGroupLabel(item NotificationHistoryItem) string {
	state := strings.TrimSpace(item.State)
	if state == "" {
		if item.Dismissed {
			state = "dismissed"
		} else {
			state = "active"
		}
	}
	category := strings.TrimSpace(item.Category)
	if category == "" {
		category = "notice"
	}
	return strings.ToUpper(state[:1]) + state[1:] + " / " + strings.ReplaceAll(category, "_", " ")
}

func actionKeyHint(action protocol.NoticeAction) string {
	switch strings.TrimSpace(action.ActionID) {
	case "dismiss":
		return "d"
	case "mark_read", "mark_unread":
		return "m"
	case "copy_details":
		return "c"
	case "open_task":
		return "o"
	case "open_workspace":
		return "w"
	case "open_logs":
		return "l"
	case "retry":
		return "r"
	default:
		return ""
	}
}

func notificationDetails(item NotificationHistoryItem) string {
	parts := make([]string, 0, 4)
	for _, value := range []string{item.Message, item.Detail} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if item.OperationID != "" {
		parts = append(parts, "operation: "+item.OperationID)
	}
	if item.ScopeID != "" {
		parts = append(parts, "scope: "+item.ScopeID)
	}
	return strings.Join(parts, "\n")
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
	words := strings.Fields(message)
	if len(words) == 0 {
		return []string{"No details provided"}
	}
	lines := make([]string, 0, 3)
	var current string
	for _, word := range words {
		if ansi.StringWidth(word) > width {
			word = ansi.Truncate(word, width, "...")
		}
		if current == "" {
			current = word
			continue
		}
		candidate := current + " " + word
		if ansi.StringWidth(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
		if len(lines) >= 6 {
			break
		}
	}
	if current != "" && len(lines) < 6 {
		lines = append(lines, current)
	}
	if len(lines) == 6 && strings.Join(lines, " ") != message {
		lines[len(lines)-1] = ansi.Truncate(lines[len(lines)-1]+" ...", width, "...")
	}
	return lines
}
