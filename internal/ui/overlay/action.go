package overlay

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
)

// Action represents a menu action
type Action struct {
	Key     string
	Label   string
	Enabled bool
}

// ActionMenu is a menu overlay for task actions
type ActionMenu struct {
	task         domain.Task
	relatedTasks []domain.Task
	session      *domain.Session
	actions      []Action
	cursor       int
	scrollOffset int
	viewportRows int
	styles       *Styles
}

// NewActionMenu creates a new action menu for the given task
func NewActionMenu(task domain.Task, session *domain.Session) *ActionMenu {
	s := New()
	menu := &ActionMenu{
		task:    task,
		session: session,
		styles:  s,
	}
	menu.actions = menu.buildActions()
	return menu
}

// WithRelatedTasks attaches the task list so details can show dependency and
// child summary information for the selected task.
func (m *ActionMenu) WithRelatedTasks(tasks []domain.Task) *ActionMenu {
	m.relatedTasks = append([]domain.Task(nil), tasks...)
	m.actions = m.buildActions()
	return m
}

// buildActions creates the action list based on task and session state
func (m *ActionMenu) buildActions() []Action {
	actions := []Action{}

	// Session actions
	hasTmuxSession := m.task.HasTmuxSession || m.session != nil
	if m.session == nil {
		// Keep start actions available when there is no projected session.
		// Runtime/tmux presence can be stale, and users still need a direct
		// start path from the task workspace.
		if hasTmuxSession {
			actions = append(actions, Action{Key: "a", Label: "Attach to session", Enabled: true})
		}
		actions = append(actions, Action{Key: "s", Label: "Start session", Enabled: true})
		actions = append(actions, Action{Key: "S", Label: "Start session + work", Enabled: true})
		actions = append(actions, Action{Key: "!", Label: "Start session (yolo)", Enabled: true})
	} else {
		// Attach action when a tmux session is known to exist.
		actions = append(actions, Action{Key: "a", Label: "Attach to session", Enabled: true})

		// State-specific actions
		switch m.session.State {
		case domain.SessionIdle:
			actions = append(actions, Action{Key: "s", Label: "Start session", Enabled: true})
		case domain.SessionBusy, domain.SessionWaiting:
			actions = append(actions, Action{Key: "p", Label: "Pause session", Enabled: true})
			actions = append(actions, Action{Key: "x", Label: "Stop session", Enabled: true})
		case domain.SessionPaused:
			actions = append(actions, Action{Key: "R", Label: "Resume session", Enabled: true})
			actions = append(actions, Action{Key: "x", Label: "Stop session", Enabled: true})
		case domain.SessionDone, domain.SessionError:
			actions = append(actions, Action{Key: "x", Label: "Stop session", Enabled: true})
		}
	}

	// Git actions separator
	if len(actions) > 0 {
		actions = append(actions, Action{Key: "", Label: "───────────────────", Enabled: false})
	}

	// Git actions (enabled when session exists and has worktree)
	hasWorktree := false
	if m.session != nil && m.session.Worktree != "" {
		hasWorktree = true
	} else if m.task.Session != nil && m.task.Session.Worktree != "" {
		hasWorktree = true
	} else if m.task.HasWorktree {
		hasWorktree = true
	}
	mergeLabel := "Follow-on merge"
	if m.task.ParentID == nil && len(m.relatedTasks) > 0 && !m.hasEligibleUpstreamSource() {
		mergeLabel = "Merge into base branch"
	}
	// Cleanup can route by issue id through the daemon even when worktree
	// metadata is stale in the current projection.
	hasIssueScopedGitTarget := hasWorktree || hasTmuxSession
	hasIssueScopedCleanupTarget := strings.TrimSpace(m.task.ID.String()) != ""
	actions = append(actions,
		Action{Key: "u", Label: "Update from base branch", Enabled: hasIssueScopedGitTarget},
		Action{Key: "m", Label: mergeLabel, Enabled: hasWorktree},
		Action{Key: "b", Label: "Merge into...", Enabled: true},
		Action{Key: "P", Label: "Create PR", Enabled: hasWorktree},
		Action{Key: "O", Label: "Open PR", Enabled: hasWorktree},
		Action{Key: "M", Label: "Abort merge", Enabled: hasWorktree},
		Action{Key: "H", Label: "Open Helix", Enabled: hasWorktree},
		Action{Key: "i", Label: "Attachments", Enabled: true},
		Action{Key: "r", Label: "Dev servers", Enabled: true},
		Action{Key: "f", Label: "Show diff", Enabled: hasWorktree},
		Action{Key: "w", Label: "Cleanup worktree", Enabled: hasIssueScopedCleanupTarget},
		Action{Key: "W", Label: "Delete task + cleanup worktree", Enabled: hasIssueScopedCleanupTarget},
	)

	actions = append(actions, Action{Key: "i", Label: "Image attachments", Enabled: true})

	// Task actions separator
	actions = append(actions, Action{Key: "", Label: "───────────────────", Enabled: false})

	// Task actions (always available)
	actions = append(actions,
		Action{Key: "1", Label: "Set status: Open", Enabled: m.task.Status != domain.StatusOpen},
		Action{Key: "2", Label: "Set status: In Progress", Enabled: m.task.Status != domain.StatusInProgress},
		Action{Key: "3", Label: "Set status: Blocked", Enabled: m.task.Status != domain.StatusBlocked},
		Action{Key: "4", Label: "Set status: Done", Enabled: m.task.Status != domain.StatusDone},
		Action{Key: "h", Label: "Move left", Enabled: m.task.Status != domain.StatusOpen},
		Action{Key: "l", Label: "Move right", Enabled: m.task.Status != domain.StatusDone},
		Action{Key: "c", Label: "Create child task", Enabled: true},
		Action{Key: "e", Label: "Edit task", Enabled: true},
		Action{Key: "T", Label: "Tombstone task", Enabled: true},
		Action{Key: "d", Label: "Delete task", Enabled: true},
	)

	return actions
}

func (m *ActionMenu) hasEligibleUpstreamSource() bool {
	isReady := func(status domain.Status) bool {
		return status == domain.StatusInProgress || status == domain.StatusDone
	}

	findRelated := func(id string) (domain.Task, bool) {
		for _, task := range m.relatedTasks {
			if task.ID.String() == id {
				return task, true
			}
		}
		return domain.Task{}, false
	}

	if m.task.ParentID != nil {
		if parent, ok := findRelated(m.task.ParentID.String()); ok && isReady(parent.Status) {
			return true
		}
	}
	for _, dep := range m.task.Dependencies {
		if dep.Type != domain.DependencyBlocks && dep.Type != domain.DependencyBlockedBy {
			continue
		}
		if upstream, ok := findRelated(dep.ID.String()); ok && isReady(upstream.Status) {
			return true
		}
	}
	return false
}

// Init initializes the menu
func (m *ActionMenu) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *ActionMenu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			m.moveCursorDown()
			return m, nil

		case "k", "up":
			m.moveCursorUp()
			return m, nil

		case "enter":
			return m, m.selectCurrentAction()

		case "left":
			return m, m.selectByKey("h")

		case "right":
			return m, m.selectByKey("l")

		default:
			// Try direct key selection
			return m, m.selectByKey(msg.String())
		}
	}

	return m, nil
}

// View renders the menu
func (m *ActionMenu) View() string {
	return m.renderActionList(true)
}

func (m *ActionMenu) viewActionsOnly() string {
	return m.renderActionList(false)
}

func (m *ActionMenu) viewActionsOnlyWidth(maxWidth int) string {
	return m.renderActionListWithWidth(false, maxWidth)
}

func (m *ActionMenu) setViewportRows(rows int) {
	if rows < 0 {
		rows = 0
	}
	m.viewportRows = rows
	m.ensureCursorVisible()
}

func (m *ActionMenu) renderActionList(includeTaskSummary bool) string {
	return m.renderActionListWithWidth(includeTaskSummary, 0)
}

func (m *ActionMenu) renderActionListWithWidth(includeTaskSummary bool, maxWidth int) string {
	var b strings.Builder

	if includeTaskSummary {
		b.WriteString(m.styles.MenuItemActive.Render(fmt.Sprintf("[%s] %s", m.task.ID, m.task.Title)))
		b.WriteString("\n")

		b.WriteString(m.styles.MenuItem.Render(
			fmt.Sprintf("status: %s  priority: %s  type: %s", m.task.Status, m.task.Priority, m.task.Type),
		))
		b.WriteString("\n")
		if m.task.ParentID != nil {
			b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("parent: %s", *m.task.ParentID)))
			b.WriteString("\n")
		}
		if total, done := m.childProgress(); total > 0 {
			b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("children: %d total (%d done)", total, done)))
			b.WriteString("\n")
		}
		outgoing := len(m.task.Dependencies)
		incoming := len(m.incomingDependencies())
		b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("dependencies: out %d / in %d", outgoing, incoming)))
		b.WriteString("\n")
		if m.session != nil {
			b.WriteString(m.styles.MenuItem.Render(fmt.Sprintf("session: %s %s", m.session.State.Icon(), m.session.State)))
			b.WriteString("\n")
		}
		b.WriteString(m.styles.Separator.Render("───────────────────"))
		b.WriteString("\n")
	}

	start, end := 0, len(m.actions)
	if !includeTaskSummary {
		start, end = m.visibleActionRange()
	}

	for i := start; i < end; i++ {
		action := m.actions[i]
		// Skip rendering logic for separators
		if action.Key == "" {
			b.WriteString(m.styles.Separator.Render(action.Label))
			b.WriteString("\n")
			continue
		}

		// Determine style based on state
		var style, keyStyle = m.styles.MenuItem, m.styles.MenuKey
		if !action.Enabled {
			style = m.styles.MenuItemDisabled
			keyStyle = m.styles.MenuKeyDisabled
		} else if i == m.cursor {
			style = m.styles.MenuItemActive
		}

		// Format: [key] label
		line := keyStyle.Render("["+action.Key+"]") + " " + style.Render(action.Label)
		b.WriteString(clampOverlayLineWidth(line, maxWidth))
		b.WriteString("\n")
	}

	return b.String()
}

func clampOverlayLineWidth(line string, maxWidth int) string {
	if maxWidth <= 0 {
		return line
	}
	if ansi.StringWidth(line) <= maxWidth {
		return line
	}
	return ansi.Truncate(line, maxWidth, "…")
}

// Title returns the overlay title
func (m *ActionMenu) Title() string {
	return "Task"
}

func (m *ActionMenu) StatusBindings() []keybinds.Binding {
	return []keybinds.Binding{
		{Key: "j/k/↑/↓", Description: "move"},
		{Key: "Enter", Description: "run action"},
		{Key: "1/2/3/4", Description: "set status"},
		{Key: "h/l", Description: "move status"},
		{Key: "Esc/q", Description: "close"},
	}
}

// Size returns the overlay dimensions
func (m *ActionMenu) Size() (width, height int) {
	baseLines := 5 // title+meta+deps+separator (parent/session/children may add more)
	if m.task.ParentID != nil {
		baseLines++
	}
	if total, _ := m.childProgress(); total > 0 {
		baseLines++
	}
	if m.session != nil {
		baseLines++
	}
	return 72, baseLines + len(m.actions) + 4
}

// moveCursorDown moves the cursor to the next enabled action
func (m *ActionMenu) moveCursorDown() {
	for i := 1; i <= len(m.actions); i++ {
		next := (m.cursor + i) % len(m.actions)
		if m.actions[next].Enabled && m.actions[next].Key != "" {
			m.cursor = next
			m.ensureCursorVisible()
			return
		}
	}
}

// moveCursorUp moves the cursor to the previous enabled action
func (m *ActionMenu) moveCursorUp() {
	for i := 1; i <= len(m.actions); i++ {
		prev := (m.cursor - i + len(m.actions)) % len(m.actions)
		if m.actions[prev].Enabled && m.actions[prev].Key != "" {
			m.cursor = prev
			m.ensureCursorVisible()
			return
		}
	}
}

func (m *ActionMenu) visibleActionRange() (start, end int) {
	total := len(m.actions)
	if total == 0 {
		return 0, 0
	}
	if m.viewportRows <= 0 || m.viewportRows >= total {
		m.scrollOffset = 0
		return 0, total
	}

	m.ensureCursorVisible()
	start = m.scrollOffset
	end = min(total, start+m.viewportRows)
	return start, end
}

func (m *ActionMenu) ensureCursorVisible() {
	total := len(m.actions)
	if total == 0 {
		m.cursor = 0
		m.scrollOffset = 0
		return
	}

	if m.cursor < 0 {
		m.cursor = 0
	} else if m.cursor >= total {
		m.cursor = total - 1
	}

	if m.viewportRows <= 0 || m.viewportRows >= total {
		m.scrollOffset = 0
		return
	}

	maxStart := total - m.viewportRows
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	} else if m.scrollOffset > maxStart {
		m.scrollOffset = maxStart
	}

	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+m.viewportRows {
		m.scrollOffset = m.cursor - m.viewportRows + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	} else if m.scrollOffset > maxStart {
		m.scrollOffset = maxStart
	}
}

// selectCurrentAction selects the action at the cursor
func (m *ActionMenu) selectCurrentAction() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.actions) {
		return nil
	}

	action := m.actions[m.cursor]
	if !action.Enabled || action.Key == "" {
		return nil
	}

	return func() tea.Msg {
		return SelectionMsg{
			Key:   action.Key,
			Value: action,
		}
	}
}

// selectByKey selects an action by its key binding
func (m *ActionMenu) selectByKey(key string) tea.Cmd {
	for _, action := range m.actions {
		if action.Key == key && action.Enabled {
			return func() tea.Msg {
				return SelectionMsg{
					Key:   action.Key,
					Value: action,
				}
			}
		}
	}
	return nil
}

// BulkActionMenu is a menu overlay for bulk task actions
type BulkActionMenu struct {
	selectedIDs []string
	count       int
	actions     []Action
	cursor      int
	styles      *Styles
}

// BulkActionMsg represents a bulk action selection
type BulkActionMsg struct {
	Action      string   // Action key (e.g., "h", "l", "d")
	SelectedIDs []string // IDs of selected tasks
}

// NewBulkActionMenu creates a new bulk action menu for selected tasks
func NewBulkActionMenu(selectedIDs []string, count int) *BulkActionMenu {
	s := New()
	menu := &BulkActionMenu{
		selectedIDs: append([]string(nil), selectedIDs...),
		count:       count,
		styles:      s,
	}
	menu.actions = menu.buildActions()
	return menu
}

// buildActions creates the bulk action list
func (m *BulkActionMenu) buildActions() []Action {
	actions := []Action{
		// Status transitions
		{Key: "h", Label: "Move left (previous status)", Enabled: true},
		{Key: "l", Label: "Move right (next status)", Enabled: true},
		{Key: "", Label: "───────────────────", Enabled: false},
		// Specific status
		{Key: "o", Label: "Set to Open", Enabled: true},
		{Key: "i", Label: "Set to In Progress", Enabled: true},
		{Key: "b", Label: "Set to Blocked", Enabled: true},
		{Key: "D", Label: "Set to Done", Enabled: true},
		{Key: "", Label: "───────────────────", Enabled: false},
		// Other actions
		{Key: "w", Label: "Cleanup worktrees", Enabled: true},
		{Key: "W", Label: "Delete + cleanup worktrees", Enabled: true},
		{Key: "d", Label: "Delete selected", Enabled: true},
		{Key: "x", Label: "Clear selection", Enabled: true},
	}
	return actions
}

// Init initializes the menu
func (m *BulkActionMenu) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m *BulkActionMenu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return m, func() tea.Msg { return CloseOverlayMsg{} }

		case "j", "down":
			m.moveCursorDown()
			return m, nil

		case "k", "up":
			m.moveCursorUp()
			return m, nil

		case "enter":
			return m, m.selectCurrentAction()

		case "left":
			return m, m.selectByKey("h")

		case "right":
			return m, m.selectByKey("l")

		default:
			// Try direct key selection
			return m, m.selectByKey(msg.String())
		}
	}

	return m, nil
}

// View renders the menu
func (m *BulkActionMenu) View() string {
	var b strings.Builder

	// Show selection count header
	b.WriteString(m.styles.MenuHeader.Render("Selected: "))
	b.WriteString(m.styles.MenuCount.Render(strings.Repeat("●", min(m.count, 10))))
	if m.count > 10 {
		b.WriteString(m.styles.MenuCount.Render("..."))
	}
	b.WriteString("\n\n")
	b.WriteString(m.styles.MenuHeader.Render(fmt.Sprintf("Scope: %d frozen selected task(s)", m.count)))
	b.WriteString("\n\n")

	for i, action := range m.actions {
		// Skip rendering logic for separators
		if action.Key == "" {
			b.WriteString(m.styles.Separator.Render(action.Label))
			b.WriteString("\n")
			continue
		}

		// Determine style based on state
		var style, keyStyle = m.styles.MenuItem, m.styles.MenuKey
		if !action.Enabled {
			style = m.styles.MenuItemDisabled
			keyStyle = m.styles.MenuKeyDisabled
		} else if i == m.cursor {
			style = m.styles.MenuItemActive
		}

		// Format: [key] label
		line := keyStyle.Render("["+action.Key+"]") + " " + style.Render(action.Label)
		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String()
}

// Title returns the overlay title
func (m *BulkActionMenu) Title() string {
	return "Bulk Actions"
}

// Size returns the overlay dimensions
func (m *BulkActionMenu) Size() (width, height int) {
	return 40, len(m.actions) + 6
}

// moveCursorDown moves the cursor to the next enabled action
func (m *BulkActionMenu) moveCursorDown() {
	for i := 1; i <= len(m.actions); i++ {
		next := (m.cursor + i) % len(m.actions)
		if m.actions[next].Enabled && m.actions[next].Key != "" {
			m.cursor = next
			return
		}
	}
}

// moveCursorUp moves the cursor to the previous enabled action
func (m *BulkActionMenu) moveCursorUp() {
	for i := 1; i <= len(m.actions); i++ {
		prev := (m.cursor - i + len(m.actions)) % len(m.actions)
		if m.actions[prev].Enabled && m.actions[prev].Key != "" {
			m.cursor = prev
			return
		}
	}
}

// selectCurrentAction selects the action at the cursor
func (m *BulkActionMenu) selectCurrentAction() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.actions) {
		return nil
	}

	action := m.actions[m.cursor]
	if !action.Enabled || action.Key == "" {
		return nil
	}

	return func() tea.Msg {
		return BulkActionMsg{
			Action:      action.Key,
			SelectedIDs: m.selectedIDs,
		}
	}
}

// selectByKey selects an action by its key binding
func (m *BulkActionMenu) selectByKey(key string) tea.Cmd {
	for _, action := range m.actions {
		if action.Key == key && action.Enabled {
			return func() tea.Msg {
				return BulkActionMsg{
					Action:      action.Key,
					SelectedIDs: m.selectedIDs,
				}
			}
		}
	}
	return nil
}

func (m *ActionMenu) incomingDependencies() []domain.Dependency {
	if len(m.relatedTasks) == 0 {
		return nil
	}
	var incoming []domain.Dependency
	for _, task := range m.relatedTasks {
		if task.ID == m.task.ID {
			continue
		}
		for _, dep := range task.Dependencies {
			if dep.ID == m.task.ID {
				incoming = append(incoming, domain.Dependency{ID: task.ID, Type: dep.Type})
			}
		}
	}
	return incoming
}

func (m *ActionMenu) childProgress() (total int, done int) {
	if len(m.relatedTasks) == 0 {
		return 0, 0
	}
	for _, task := range m.relatedTasks {
		if task.ID == m.task.ID {
			continue
		}
		if task.ParentID != nil && *task.ParentID == m.task.ID {
			total++
			if task.Status == domain.StatusDone {
				done++
			}
			continue
		}
		for _, dep := range task.Dependencies {
			if dep.Type == domain.DependencyParentChild && dep.ID == m.task.ID {
				total++
				if task.Status == domain.StatusDone {
					done++
				}
				break
			}
		}
	}
	return total, done
}
