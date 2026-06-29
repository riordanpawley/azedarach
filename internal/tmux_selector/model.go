package tmuxselector

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/board"
	"github.com/riordanpawley/azedarach/internal/ui/keybinds"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
	"github.com/riordanpawley/azedarach/internal/ui/styles"
)

const defaultFullAzSession = "az"

type ParsedSessionName struct {
	IssueID naming.IssueID
	Project string
}

type InventoryEntry struct {
	SessionID             string
	IssueID               string
	TaskTitle             string
	State                 domain.SessionState
	IssueStatus           domain.Status
	Priority              domain.Priority
	Type                  domain.TaskType
	ProjectID             string
	ProjectPath           string
	Worktree              string
	Activity              string
	ActivitySource        string
	StartedAt             *time.Time
	LastAttachedAt        *time.Time
	LastUpdatedAt         *time.Time
	TmuxAttached          bool
	TmuxAttachedCount     int
	HasTmuxSession        bool
	HasWorktree           bool
	GitAheadCount         int
	GitBehindCount        int
	HasUncommittedChanges bool
	HasConflicts          bool
	GitAdditions          int
	GitDeletions          int
	GraphAncestors        []string
	Task                  domain.Task
}

type SessionRow = InventoryEntry
type Entry = InventoryEntry

type switchCompleteMsg = SwitchResultMsg

type Snapshot struct {
	Entries          []InventoryEntry
	Tasks            []domain.Task
	TreeTasks        []domain.Task
	Revision         uint64
	LastCheckedAt    time.Time
	Freshness        string
	Enriching        bool
	CurrentSessionID string
}

type SnapshotLoader interface {
	ListTasksSnapshot(context.Context) (Snapshot, error)
}

type LiveSnapshotLoader interface {
	SnapshotLoader
	ListLiveSnapshot(context.Context) (Snapshot, error)
	EnrichSnapshot(context.Context, Snapshot) (Snapshot, error)
}

type SnapshotLoaderFunc func(context.Context) (Snapshot, error)

func (f SnapshotLoaderFunc) ListTasksSnapshot(ctx context.Context) (Snapshot, error) {
	return f(ctx)
}

type Switcher interface {
	SwitchClient(context.Context, string) error
}

type SwitcherFunc func(context.Context, string) error

func (f SwitcherFunc) SwitchClient(ctx context.Context, sessionID string) error {
	return f(ctx, sessionID)
}

type Killer interface {
	KillSession(context.Context, InventoryEntry) error
}

type KillerFunc func(context.Context, InventoryEntry) error

func (f KillerFunc) KillSession(ctx context.Context, entry InventoryEntry) error {
	return f(ctx, entry)
}

type DetailOpener interface {
	OpenDetail(context.Context, InventoryEntry) error
	OpenDrillDown(context.Context, InventoryEntry) error
}

type DetailOpenerFunc func(context.Context, InventoryEntry) error

func (f DetailOpenerFunc) OpenDetail(ctx context.Context, entry InventoryEntry) error {
	return f(ctx, entry)
}

func (f DetailOpenerFunc) OpenDrillDown(ctx context.Context, entry InventoryEntry) error {
	return f(ctx, entry)
}

type UIStateStore interface {
	GetUIStateForProject(context.Context, string, string) (protocol.UIStateResponseBody, error)
	SetUIStateForProject(context.Context, string, string, string) (protocol.UIStateResponseBody, error)
}

type PopupCloser interface {
	ClosePopup(context.Context) error
}

type fullAzSwitcher interface {
	HasSession(context.Context, string) (bool, error)
	SwitchClient(context.Context, string) error
}

type Option func(*Model)

func WithSwitcher(switcher Switcher) Option {
	return func(m *Model) {
		m.switcher = switcher
		if closer, ok := switcher.(PopupCloser); ok {
			m.popupCloser = closer
		}
	}
}

func WithPopupCloser(closer PopupCloser) Option {
	return func(m *Model) {
		m.popupCloser = closer
	}
}

func WithDetailOpener(opener DetailOpener) Option {
	return func(m *Model) {
		m.detailOpener = opener
	}
}

func WithKiller(killer Killer) Option {
	return func(m *Model) {
		m.killer = killer
	}
}

func WithUIStateStore(store UIStateStore) Option {
	return func(m *Model) {
		m.uiStateStore = store
	}
}

type selectorTab int

const (
	selectorTabGrid selectorTab = iota
	selectorTabTree
)

type selectorTreeSort int

const (
	selectorTreeSortOrder selectorTreeSort = iota
	selectorTreeSortUpdated
)

type Model struct {
	loader       SnapshotLoader
	switcher     Switcher
	popupCloser  PopupCloser
	killer       Killer
	detailOpener DetailOpener
	uiStateStore UIStateStore
	styles       *styles.Styles

	snapshot           Snapshot
	cursor             int
	activeTab          selectorTab
	treeSort           selectorTreeSort
	width              int
	height             int
	loading            bool
	err                error
	status             string
	defaultedToCurrent bool
	selectorTabLoaded  bool
	selectorTabUserSet bool
	startedAt          time.Time
	readyLogged        bool

	searchMode  bool
	searchQuery string
	gotoArmed   bool
	jumpMode    *overlay.JumpMode
	jumpTargets []int
}

type LoadedMsg struct {
	Snapshot Snapshot
}

type LoadFailedMsg struct {
	Err error
}

type EnrichedMsg struct {
	Snapshot Snapshot
	Err      error
}

type SwitchResultMsg struct {
	Err error
}

type DetailOpenResultMsg struct {
	Err error
}

type KillResultMsg struct {
	SessionID string
	Err       error
}

type snapshotLoadedMsg struct {
	snapshot Snapshot
	err      error
}

type selectorTabLoadedMsg struct {
	tab   selectorTab
	found bool
	err   error
}

type selectorTabSavedMsg struct {
	tab selectorTab
	err error
}

func New(loader SnapshotLoader, opts ...Option) Model {
	m := Model{
		loader:    loader,
		styles:    styles.New(),
		width:     88,
		height:    24,
		loading:   true,
		startedAt: time.Now(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&m)
		}
	}
	m.selectorTabLoaded = m.uiStateStore == nil
	return m
}

func (m Model) Init() tea.Cmd {
	slog.Default().Info("tmux selector model init",
		"elapsed_ms", m.elapsedSinceStart().Milliseconds(),
		"ui_state_store", m.uiStateStore != nil,
	)
	return m.loadCmd()
}

func (m Model) elapsedSinceStart() time.Duration {
	if m.startedAt.IsZero() {
		return 0
	}
	return time.Since(m.startedAt)
}

func (m *Model) logReadyToRender(trigger string) {
	if m == nil || m.readyLogged || m.loading {
		return
	}
	tab, ok := persistedValueForSelectorTab(m.activeTab)
	if !ok {
		tab = "unknown"
	}
	slog.Default().Info("tmux selector ready to render",
		"elapsed_ms", m.elapsedSinceStart().Milliseconds(),
		"trigger", trigger,
		"session_count", len(m.snapshot.Entries),
		"tree_task_count", len(m.snapshot.TreeTasks),
		"active_tab", tab,
		"selector_tab_loaded", m.selectorTabLoaded,
		"enriching", m.snapshot.Enriching,
	)
	m.readyLogged = true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case LoadedMsg:
		m.loading = false
		m.err = nil
		m.snapshot = msg.Snapshot
		m.normalizeSnapshot()
		m.status = formatSnapshotStatus(m.snapshot, len(m.snapshot.Entries))
		slog.Default().Info("tmux selector snapshot applied",
			"elapsed_ms", m.elapsedSinceStart().Milliseconds(),
			"session_count", len(m.snapshot.Entries),
			"tree_task_count", len(m.snapshot.TreeTasks),
			"loading", m.loading,
			"selector_tab_loaded", m.selectorTabLoaded,
			"enriching", m.snapshot.Enriching,
		)
		m.logReadyToRender("snapshot")
		cmds := []tea.Cmd{}
		if !m.selectorTabLoaded {
			cmds = append(cmds, m.loadSelectorTabCmd())
		}
		if liveLoader, ok := m.loader.(LiveSnapshotLoader); ok && m.snapshot.Enriching {
			cmds = append(cmds, m.enrichCmd(liveLoader, m.snapshot))
		}
		return m, tea.Batch(cmds...)
	case snapshotLoadedMsg:
		if msg.err != nil {
			return m, nilFromLoadFailed(&m, msg.err)
		}
		return m.Update(LoadedMsg{Snapshot: msg.snapshot})
	case LoadFailedMsg:
		m.loading = false
		m.err = msg.Err
		return m, nil
	case EnrichedMsg:
		if msg.Err != nil {
			m.status = strings.TrimSpace(m.status + "  enrichment failed")
			return m, nil
		}
		selectedSessionID := ""
		if selected, ok := m.selectedEntry(); ok {
			selectedSessionID = strings.TrimSpace(selected.SessionID)
		}
		m.snapshot = msg.Snapshot
		m.normalizeSnapshot()
		if selectedSessionID != "" {
			m.selectSessionID(selectedSessionID)
		}
		m.status = formatSnapshotStatus(m.snapshot, len(m.snapshot.Entries))
		return m, nil
	case SwitchResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		return m, tea.Quit
	case DetailOpenResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			m.status = ""
			return m, nil
		}
		return m, tea.Quit
	case KillResultMsg:
		if msg.Err != nil {
			m.err = msg.Err
			m.status = fmt.Sprintf("kill %s failed", msg.SessionID)
			return m, nil
		}
		m.status = fmt.Sprintf("killed %s, refreshing", msg.SessionID)
		m.loading = true
		m.err = nil
		return m, m.loadCmd()
	case selectorTabLoadedMsg:
		if msg.err == nil && msg.found && !m.selectorTabUserSet {
			m.activeTab = msg.tab
		}
		m.selectorTabLoaded = true
		if m.err == nil {
			m.loading = false
		}
		m.logReadyToRender("selector_tab")
		return m, nil
	case selectorTabSavedMsg:
		return m, nil
	case overlay.JumpSelectedMsg:
		if msg.TaskIndex >= 0 && msg.TaskIndex < len(m.jumpTargets) {
			m.cursor = m.jumpTargets[msg.TaskIndex]
		}
		m.clearJumpMode()
		return m, nil
	case overlay.CloseOverlayMsg:
		m.clearJumpMode()
		return m, nil
	case tea.KeyMsg:
		if m.loading {
			switch msg.String() {
			case "ctrl+c", "q", "esc":
				return m, tea.Quit
			default:
				return m, nil
			}
		}
		if m.jumpMode != nil {
			next, cmd := m.jumpMode.Update(msg)
			if jump, ok := next.(*overlay.JumpMode); ok {
				m.jumpMode = jump
			}
			return m, cmd
		}
		if m.searchMode {
			return m.handleSearchKey(msg)
		}
		if m.gotoArmed {
			switch msg.String() {
			case "esc", "ctrl+c", "q":
				m.gotoArmed = false
				m.status = formatSnapshotStatus(m.snapshot, len(m.snapshot.Entries))
				if msg.String() == "ctrl+c" || msg.String() == "q" {
					return m, tea.Quit
				}
				return m, nil
			case "w":
				m.gotoArmed = false
				m.startJumpMode()
				return m, nil
			default:
				m.gotoArmed = false
				m.status = formatSnapshotStatus(m.snapshot, len(m.snapshot.Entries))
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "tab":
			m.toggleActiveTab()
			m.selectorTabLoaded = true
			m.selectorTabUserSet = true
			m.loading = false
			return m, m.persistSelectorTabCmd(m.activeTab)
		case "u":
			if m.activeTab == selectorTabTree {
				m.toggleTreeSort()
				return m, nil
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
			m.selectCardHotkey(msg.String())
			return m, nil
		case "g":
			m.gotoArmed = true
			m.status = "goto: press w for labels"
			return m, nil
		case "/":
			m.searchMode = true
			m.clearJumpMode()
			m.status = ""
			return m, nil
		case "j", "down":
			m.moveCursor(0, 1)
			return m, nil
		case "k", "up":
			m.moveCursor(0, -1)
			return m, nil
		case "h", "left":
			m.moveCursor(-1, 0)
			return m, nil
		case "l", "right":
			m.moveCursor(1, 0)
			return m, nil
		case "r":
			m.loading = true
			m.err = nil
			m.status = "refreshing"
			m.defaultedToCurrent = false
			return m, m.loadCmd()
		case "enter":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			m.status = fmt.Sprintf("Opening %s drill-down in full az...", entry.IssueID)
			return m, m.openDrillDownCmd(entry)
		case "a":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			return m, m.attachCmd(entry)
		case " ", "space", "o":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			m.status = fmt.Sprintf("Opening %s in full az...", entry.IssueID)
			return m, m.openDetailCmd(entry)
		case "x":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			m.status = fmt.Sprintf("killing %s...", killTargetLabel(entry))
			return m, m.killCmd(entry)
		}
	}
	return m, nil
}

func nilFromLoadFailed(m *Model, err error) tea.Cmd {
	m.loading = false
	m.err = err
	return nil
}

func (m *Model) normalizeSnapshot() {
	if len(m.snapshot.Entries) == 0 && len(m.snapshot.Tasks) > 0 {
		m.snapshot.Entries = EntriesFromTasks(m.snapshot.Tasks)
	}
	m.snapshot.Entries = keepTmuxEntries(m.snapshot.Entries)
	m.snapshot.Entries = prioritizeAzSessionFirst(m.snapshot.Entries)
	if !m.defaultedToCurrent {
		if current := strings.TrimSpace(m.snapshot.CurrentSessionID); current != "" && m.selectSessionID(current) {
			m.defaultedToCurrent = true
		} else if m.selectAttachedSnapshotSession() {
			m.defaultedToCurrent = true
		}
	}
	m.clampCursor()
}

func (m *Model) clampCursor() {
	entries := m.filteredEntries()
	if m.cursor >= len(entries) {
		m.cursor = len(entries) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) selectSessionID(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	for i, entry := range m.filteredEntries() {
		if strings.TrimSpace(entry.SessionID) == sessionID {
			m.cursor = i
			return true
		}
	}
	return false
}

func (m *Model) selectAttachedSnapshotSession() bool {
	for i, entry := range m.filteredEntries() {
		if entry.TmuxAttached || entry.TmuxAttachedCount > 0 {
			m.cursor = i
			return true
		}
	}
	return false
}

func (m Model) View() string {
	if m.loading {
		return "Loading tmux sessions...\n"
	}
	var b strings.Builder
	if status := m.statusText(); status != "" {
		b.WriteString(m.styles.StatusInfo.Render(status))
		b.WriteString("\n")
	}
	if m.err != nil {
		b.WriteString(m.styles.ToastError.Render("Error: " + m.err.Error()))
		b.WriteString("\n\n")
	}
	entries := m.filteredEntries()
	if len(entries) == 0 {
		if strings.TrimSpace(m.searchQuery) != "" {
			b.WriteString(fmt.Sprintf("No tmux sessions match %q.\n", m.searchQuery))
		} else {
			b.WriteString("No tmux sessions found.\n")
		}
	} else {
		if m.activeTab == selectorTabTree {
			b.WriteString(m.renderTree(entries))
		} else {
			availableHeight := m.gridAvailableHeight()
			columns := m.gridColumnCount(availableHeight)
			cardWidth := gridCardWidth(m.width, columns)
			rows := RenderVisibleGridWithLabels(
				entries,
				m.cursor,
				columns,
				cardWidth,
				availableHeight,
				m.styles,
				m.labelsByEntry(),
			)
			for _, row := range rows {
				b.WriteString(row)
				b.WriteString("\n")
			}
		}
	}
	contentLines := linesForHeight(b.String(), maxInt(0, m.height-1))
	footer := m.renderFooter()
	return strings.Join(append(contentLines, footer), "\n")
}

func (m Model) jumpLabelsByEntry() map[int]string {
	if m.jumpMode == nil || len(m.jumpTargets) == 0 {
		return nil
	}
	labels := make(map[int]string, len(m.jumpTargets))
	for jumpIndex, entryIndex := range m.jumpTargets {
		label := m.jumpMode.GetLabel(jumpIndex)
		if label != "" {
			labels[entryIndex] = label
		}
	}
	return labels
}

func (m Model) cardSelectionLabels() map[int]string {
	labels := make(map[int]string)
	limit := len(m.filteredEntries())
	if limit > 10 {
		limit = 10
	}
	for i := 0; i < limit; i++ {
		labels[i] = string(rune('0' + i))
	}
	return labels
}

func (m Model) labelsByEntry() map[int]string {
	if m.jumpMode != nil {
		labels := m.jumpLabelsByEntry()
		if labels != nil {
			return labels
		}
	}
	return m.cardSelectionLabels()
}

func (m Model) renderFooter() string {
	left := m.renderTabs()
	bindings := []keybinds.Binding{
		{Key: "Tab", Description: "tab"},
		{Key: "h/j/k/l", Description: "move"},
		{Key: "q/Esc", Description: "close"},
		{Key: "/", Description: "search"},
		{Key: "gw", Description: "labels"},
		{Key: "Enter", Description: "drill"},
		{Key: "a", Description: "switch"},
		{Key: "x", Description: "kill"},
		{Key: "o/Sp", Description: "open"},
	}
	if m.activeTab == selectorTabTree {
		bindings = append([]keybinds.Binding{{Key: "u", Description: "sort"}}, bindings...)
	}
	right := m.styles.StatusHint.Render(keybinds.RenderPlain(bindings, "  "))
	if m.searchMode || strings.TrimSpace(m.searchQuery) != "" {
		right += m.styles.StatusHint.Render("  /" + m.searchQuery)
	}
	leftWidth := ansi.StringWidth(left)
	rightWidth := ansi.StringWidth(right)
	if leftWidth+1+rightWidth <= m.width {
		return left + strings.Repeat(" ", m.width-leftWidth-rightWidth) + right
	}
	if leftWidth < m.width {
		return left + " " + ansi.Truncate(right, maxInt(1, m.width-leftWidth-1), "…")
	}
	return ansi.Truncate(left, maxInt(1, m.width), "…")
}

func (m Model) renderTabs() string {
	tab := func(label string, active bool) string {
		if active {
			return m.styles.StatusInfo.Render("[ " + label + " ]")
		}
		return m.styles.StatusHint.Render("  " + label + "  ")
	}
	treeLabel := "Tree"
	if m.activeTab == selectorTabTree && m.treeSort == selectorTreeSortUpdated {
		treeLabel = "Tree:" + m.treeSortLabel()
	}
	return tab("Cards", m.activeTab == selectorTabGrid) + " " + tab(treeLabel, m.activeTab == selectorTabTree)
}

func (m Model) renderTree(entries []InventoryEntry) string {
	rows := m.activeSessionTreeRows(entries)
	if len(rows) == 0 {
		return ""
	}
	availableHeight := maxInt(1, m.treeAvailableHeight())
	visible := visibleTreeRows(rows, m.cursor, availableHeight)
	lines := make([]string, 0, len(visible))
	for _, row := range visible {
		if row.entryIndex >= len(entries) {
			continue
		}
		lines = append(lines, m.renderTreeRow(row, row.entry))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (m Model) renderTreeRow(row sessionTreeRow, entry InventoryEntry) string {
	cursor := m.styles.StatusHint.Render("  ")
	if row.entryIndex >= 0 && row.entryIndex == m.cursor {
		cursor = lipgloss.NewStyle().Foreground(styles.Blue).Bold(true).Render("> ")
	}
	indent := m.styles.StatusHint.Render(treePrefix(row.ancestorLast, row.last))
	displayID := entryDisplayID(entry)
	if displayID == "" {
		displayID = strings.TrimSpace(entry.SessionID)
	}
	title := strings.TrimSpace(entry.TaskTitle)
	if title == "" {
		title = strings.TrimSpace(entry.Task.Title)
	}
	if title == "" {
		title = strings.TrimSpace(entry.SessionID)
	}
	state := entryDisplayLabel(entry)
	if state == "" {
		state = string(domain.SessionIdle)
	}
	issueStatus := issueStatusLabel(entry)
	metaParts := []string{}
	if entry.SessionID != "" {
		metaParts = append(metaParts, renderTmuxSessionMeta(entry, m.styles))
	}
	if updated := formatEntryUpdatedAge(entry, time.Now()); updated != "" {
		metaParts = append(metaParts, m.styles.StatusHint.Render("updated "+updated))
	}
	if entry.ProjectPath != "" {
		metaParts = append(metaParts, m.styles.StatusHint.Render(entry.ProjectPath))
	} else if entry.ProjectID != "" {
		metaParts = append(metaParts, m.styles.StatusHint.Render(entry.ProjectID))
	}
	meta := ""
	if len(metaParts) > 0 {
		meta = "  " + strings.Join(metaParts, "  ")
	}
	statusSuffix := ""
	if issueStatus != "" {
		statusSuffix = " " + renderBracketed(issueStatus, issueStatusStyle(issueStatus))
	}
	displayID = m.styles.TaskID.Render(displayID)
	stateToken := renderBracketed(state, sessionLabelStyle(state, m.styles))
	title = m.styles.TaskTitle.Render(title)
	line := fmt.Sprintf("%s%s%s %s%s %s%s", cursor, indent, displayID, stateToken, statusSuffix, title, meta)
	line = ansi.Truncate(line, maxInt(1, m.width), "…")
	if row.entryIndex >= 0 && row.entryIndex == m.cursor {
		return lipgloss.NewStyle().Bold(true).Render(line)
	}
	return line
}

func (m Model) treeAvailableHeight() int {
	contentHeight := maxInt(0, m.height-1)
	used := 0
	if status := m.statusText(); status != "" {
		used += lipgloss.Height(m.styles.StatusInfo.Render(status))
	}
	if m.err != nil {
		used += lipgloss.Height(m.styles.ToastError.Render("Error: "+m.err.Error())) + 1
	}
	return maxInt(1, contentHeight-used)
}

func linesForHeight(view string, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (m *Model) startJumpMode() {
	targets := m.visibleEntryIndices()
	if len(targets) == 0 {
		m.clearJumpMode()
		return
	}
	m.jumpTargets = targets
	m.jumpMode = overlay.NewJumpMode(len(targets))
	m.status = "jump: type label"
}

func (m *Model) clearJumpMode() {
	m.jumpMode = nil
	m.jumpTargets = nil
	m.gotoArmed = false
	m.status = formatSnapshotStatus(m.snapshot, len(m.snapshot.Entries))
}

func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searchMode = false
		m.searchQuery = ""
		m.clampCursor()
		return m, nil
	case "enter":
		m.searchMode = false
		m.clampCursor()
		return m, nil
	case "backspace", "ctrl+h":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			m.clampCursor()
		}
		return m, nil
	case "j", "down":
		m.moveCursor(0, 1)
		return m, nil
	case "k", "up":
		m.moveCursor(0, -1)
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	default:
		if len(msg.Runes) > 0 && !msg.Alt {
			m.searchQuery += string(msg.Runes)
			m.clampCursor()
		}
		return m, nil
	}
}

func (m Model) statusText() string {
	query := strings.TrimSpace(m.searchQuery)
	if m.searchMode && query == "" {
		return "search: type to filter tmux sessions"
	}
	if query != "" {
		return fmt.Sprintf("%d/%d sessions match /%s", len(m.filteredEntries()), len(m.snapshot.Entries), query)
	}
	return strings.TrimSpace(m.status)
}

func (m Model) filteredEntries() []InventoryEntry {
	query := strings.TrimSpace(strings.ToLower(m.searchQuery))
	source := m.withGraphAncestors(m.snapshot.Entries)
	var entries []InventoryEntry
	if query == "" {
		entries = source
	} else {
		entries = make([]InventoryEntry, 0, len(source))
		for _, entry := range source {
			if entryMatchesSearch(entry, query) {
				entries = append(entries, entry)
			}
		}
	}
	return m.graphOrderedEntries(entries)
}

func (m Model) graphOrderedEntries(entries []InventoryEntry) []InventoryEntry {
	ordered := entries
	if len(entries) < 2 {
		return m.withGraphAncestors(ordered)
	}
	rows := activeSessionTreeRowsWithOptions(entries, m.treeAncestorTasks(), selectorTreeSortOrder)
	if len(rows) == 0 {
		return m.withGraphAncestors(ordered)
	}
	treeOrdered := make([]InventoryEntry, 0, len(entries))
	seen := make(map[int]struct{}, len(entries))
	for _, row := range rows {
		if row.entryIndex < 0 || row.entryIndex >= len(entries) {
			continue
		}
		if _, ok := seen[row.entryIndex]; ok {
			continue
		}
		seen[row.entryIndex] = struct{}{}
		treeOrdered = append(treeOrdered, entries[row.entryIndex])
	}
	if len(treeOrdered) == len(entries) {
		ordered = treeOrdered
	}
	return m.withGraphAncestors(ordered)
}

func (m Model) withGraphAncestors(entries []InventoryEntry) []InventoryEntry {
	if len(entries) == 0 {
		return entries
	}
	ancestorTasks := m.graphAncestorTasksForEntries(entries)
	var out []InventoryEntry
	for i, entry := range entries {
		ancestors := graphAncestorLabels(entry, ancestorTasks)
		if len(ancestors) == 0 && len(entry.GraphAncestors) == 0 {
			continue
		}
		if out == nil {
			out = append([]InventoryEntry(nil), entries...)
		}
		out[i].GraphAncestors = ancestors
	}
	if out == nil {
		return entries
	}
	return out
}

func (m Model) graphAncestorTasksForEntries(entries []InventoryEntry) map[string]domain.Task {
	tasks := m.snapshotTasksByID()
	for _, entry := range entries {
		issueID := entryIssueID(entry)
		if issueID == "" {
			continue
		}
		if tasks == nil {
			tasks = map[string]domain.Task{}
		}
		task := entry.Task
		if task.ID.String() == "" {
			task.ID = naming.IssueID(issueID)
		}
		if strings.TrimSpace(task.Title) == "" {
			task.Title = strings.TrimSpace(entry.TaskTitle)
		}
		if task.Status == "" {
			task.Status = entry.IssueStatus
		}
		tasks[issueID] = task
	}
	return tasks
}

func (m Model) snapshotTasksByID() map[string]domain.Task {
	tasks := append([]domain.Task(nil), m.snapshot.TreeTasks...)
	tasks = append(tasks, m.snapshot.Tasks...)
	if len(tasks) == 0 {
		return nil
	}
	out := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		issueID := strings.TrimSpace(task.ID.String())
		if issueID == "" {
			continue
		}
		out[issueID] = task
	}
	return out
}

func graphAncestorLabels(entry InventoryEntry, ancestorTasks map[string]domain.Task) []string {
	parentID := entryParentID(entry)
	if parentID == "" {
		return nil
	}
	labels := []string{}
	seen := map[string]struct{}{}
	for parentID != "" {
		if _, ok := seen[parentID]; ok {
			break
		}
		seen[parentID] = struct{}{}
		task, ok := ancestorTasks[parentID]
		if !ok {
			labels = append(labels, parentID)
			break
		}
		labels = append(labels, graphAncestorLabel(task))
		parentID = taskParentID(task)
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return labels
}

func graphAncestorLabel(task domain.Task) string {
	id := strings.TrimSpace(task.ID.String())
	title := strings.TrimSpace(task.Title)
	if id == "" {
		return title
	}
	if title == "" || title == id {
		return id
	}
	return id + " " + title
}

func entryMatchesSearch(entry InventoryEntry, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		entry.SessionID,
		entry.IssueID,
		entry.TaskTitle,
		entry.State.String(),
		entry.Activity,
		entry.ActivitySource,
		entry.IssueStatus.String(),
		entry.Priority.String(),
		entry.Type.String(),
		entry.ProjectID,
		entry.ProjectPath,
		entry.Worktree,
		entry.Task.ID.String(),
		entry.Task.Title,
		entry.Task.Status.String(),
		entry.Task.Priority.String(),
		entry.Task.Type.String(),
		entry.Task.Origin,
		strings.Join(entry.GraphAncestors, " "),
	}, "\n"))
	return strings.Contains(haystack, query)
}

func (m *Model) moveCursor(dx int, dy int) {
	entries := m.filteredEntries()
	if m.activeTab == selectorTabTree {
		m.moveTreeCursor(entries, dx, dy)
		return
	}
	count := len(entries)
	if count == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= count {
		m.cursor = count - 1
	}
	columns := m.gridColumnCount(m.gridAvailableHeight())
	if columns <= 1 {
		next := m.cursor + dy
		if dx < 0 {
			next = m.cursor - 1
		} else if dx > 0 {
			next = m.cursor + 1
		}
		if next >= 0 && next < count {
			m.cursor = next
		}
		return
	}
	row := m.cursor / columns
	col := m.cursor % columns
	nextRow := row + dy
	nextCol := col + dx
	if nextRow < 0 || nextCol < 0 || nextCol >= columns {
		return
	}
	next := nextRow*columns + nextCol
	if next >= 0 && next < count {
		m.cursor = next
	}
}

func (m *Model) moveTreeCursor(entries []InventoryEntry, dx int, dy int) {
	rows := m.activeSessionTreeRows(entries)
	if len(rows) == 0 {
		m.cursor = 0
		return
	}
	selectable := make([]int, 0, len(rows))
	pos := -1
	for i, row := range rows {
		if row.entryIndex < 0 {
			continue
		}
		if row.entryIndex == m.cursor {
			pos = len(selectable)
		}
		selectable = append(selectable, i)
	}
	if len(selectable) == 0 {
		m.cursor = 0
		return
	}
	if pos < 0 {
		pos = 0
	}
	next := pos + dy
	if dx < 0 {
		next = pos - 1
	} else if dx > 0 {
		next = pos + 1
	}
	if next < 0 || next >= len(selectable) {
		return
	}
	m.cursor = rows[selectable[next]].entryIndex
}

func (m *Model) selectCardHotkey(key string) {
	index := -1
	switch key {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		index = int(key[0] - '0')
	}
	if index >= 0 && index < len(m.filteredEntries()) {
		m.cursor = index
	}
}

// keepTmuxEntries enforces the selector invariant: every visible card represents a real tmux session
// (with an optional az issue id). Today every entry source already populates SessionID — entriesFromLive
// skips empty session names, EntriesFromTasks computes a canonical id, and daemon enrichment only mutates
// existing entries — so this filter should be a no-op in production. It exists to document the rule and
// catch malformed entries from tests or any future enrichment path that forgets it.
func keepTmuxEntries(entries []InventoryEntry) []InventoryEntry {
	out := entries[:0]
	for _, entry := range entries {
		if strings.TrimSpace(entry.SessionID) == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func prioritizeAzSessionFirst(entries []InventoryEntry) []InventoryEntry {
	if len(entries) == 0 {
		return entries
	}
	if entries[0].TmuxAttached || entries[0].TmuxAttachedCount > 0 {
		return entries
	}
	azIndex := -1
	for i, entry := range entries {
		if entry.TmuxAttached || entry.TmuxAttachedCount > 0 {
			return entries
		}
		if strings.TrimSpace(entry.SessionID) == defaultFullAzSession {
			azIndex = i
			break
		}
	}
	if azIndex <= 0 {
		return entries
	}
	reordered := make([]InventoryEntry, len(entries))
	reordered[0] = entries[azIndex]
	copy(reordered[1:], entries[:azIndex])
	copy(reordered[azIndex+1:], entries[azIndex+1:])
	return reordered
}

func (m Model) visibleEntryIndices() []int {
	entries := m.filteredEntries()
	if len(entries) == 0 {
		return nil
	}
	availableHeight := m.gridAvailableHeight()
	columns := m.gridColumnCount(availableHeight)
	cardWidth := gridCardWidth(m.width, columns)
	return VisibleGridIndices(entries, m.cursor, columns, cardWidth, availableHeight, m.styles)
}

func (m Model) gridColumnCount(availableHeight int) int {
	return gridColumnCountForViewport(m.width, m.filteredEntries(), m.cursor, availableHeight, m.styles)
}

func (m Model) gridAvailableHeight() int {
	contentHeight := maxInt(0, m.height-1)
	used := 0
	if status := m.statusText(); status != "" {
		used += lipgloss.Height(m.styles.StatusInfo.Render(status))
	}
	if m.err != nil {
		used += lipgloss.Height(m.styles.ToastError.Render("Error: "+m.err.Error())) + 1
	}
	return maxInt(1, contentHeight-used)
}

func (m *Model) toggleActiveTab() {
	if m.activeTab == selectorTabTree {
		m.activeTab = selectorTabGrid
		return
	}
	m.activeTab = selectorTabTree
}

func (m *Model) toggleTreeSort() {
	if m.treeSort == selectorTreeSortUpdated {
		m.treeSort = selectorTreeSortOrder
		m.status = "tree sort: order"
		return
	}
	m.treeSort = selectorTreeSortUpdated
	m.status = "tree sort: updated"
}

func (m Model) treeSortLabel() string {
	if m.treeSort == selectorTreeSortUpdated {
		return "updated"
	}
	return "order"
}

func (m Model) selectedEntry() (InventoryEntry, bool) {
	entries := m.filteredEntries()
	if m.cursor < 0 || m.cursor >= len(entries) {
		return InventoryEntry{}, false
	}
	return entries[m.cursor], true
}

func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		if m.loader == nil {
			return LoadFailedMsg{Err: fmt.Errorf("snapshot loader unavailable")}
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if liveLoader, ok := m.loader.(LiveSnapshotLoader); ok {
			snapshot, err := liveLoader.ListLiveSnapshot(ctx)
			if err != nil {
				slog.Default().Warn("tmux selector live load command failed",
					"elapsed_ms", time.Since(start).Milliseconds(),
					"error", err,
				)
				return LoadFailedMsg{Err: err}
			}
			slog.Default().Info("tmux selector live load command completed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"session_count", len(snapshot.Entries),
				"enriching", snapshot.Enriching,
			)
			return LoadedMsg{Snapshot: snapshot}
		}
		snapshot, err := m.loader.ListTasksSnapshot(ctx)
		if err != nil {
			slog.Default().Warn("tmux selector snapshot load command failed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"error", err,
			)
			return LoadFailedMsg{Err: err}
		}
		slog.Default().Info("tmux selector snapshot load command completed",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"session_count", len(snapshot.Entries),
			"enriching", snapshot.Enriching,
		)
		return LoadedMsg{Snapshot: snapshot}
	}
}

func (m Model) loadSelectorTabCmd() tea.Cmd {
	store := m.uiStateStore
	if store == nil {
		return nil
	}
	return func() tea.Msg {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		resp, err := store.GetUIStateForProject(ctx, protocol.DefaultProjectID, protocol.UIStateKeyTMUXSelectorLastActiveTab)
		if err != nil {
			slog.Default().Info("tmux selector tab state load failed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"error", err,
			)
			return selectorTabLoadedMsg{err: err}
		}
		tab, ok := selectorTabFromPersistedValue(resp.Value)
		if !resp.Found || !ok {
			slog.Default().Info("tmux selector tab state loaded",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"found", resp.Found,
			)
			return selectorTabLoadedMsg{}
		}
		slog.Default().Info("tmux selector tab state loaded",
			"elapsed_ms", time.Since(start).Milliseconds(),
			"found", true,
			"tab", resp.Value,
		)
		return selectorTabLoadedMsg{tab: tab, found: true}
	}
}

func (m Model) persistSelectorTabCmd(tab selectorTab) tea.Cmd {
	store := m.uiStateStore
	if store == nil {
		return nil
	}
	value, ok := persistedValueForSelectorTab(tab)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := store.SetUIStateForProject(ctx, protocol.DefaultProjectID, protocol.UIStateKeyTMUXSelectorLastActiveTab, value)
		if err != nil {
			slog.Default().Debug("tmux selector tab state save failed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"tab", value,
				"error", err,
			)
		} else {
			slog.Default().Info("tmux selector tab state saved",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"tab", value,
			)
		}
		return selectorTabSavedMsg{tab: tab, err: err}
	}
}

func persistedValueForSelectorTab(tab selectorTab) (string, bool) {
	switch tab {
	case selectorTabGrid:
		return "cards", true
	case selectorTabTree:
		return "tree", true
	default:
		return "", false
	}
}

func selectorTabFromPersistedValue(value string) (selectorTab, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "cards", "grid":
		return selectorTabGrid, true
	case "tree":
		return selectorTabTree, true
	default:
		return selectorTabGrid, false
	}
}

func (m Model) enrichCmd(loader LiveSnapshotLoader, snapshot Snapshot) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		enriched, err := loader.EnrichSnapshot(ctx, snapshot)
		if err != nil {
			slog.Default().Debug("tmux selector enrichment command failed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"error", err,
			)
		} else {
			slog.Default().Info("tmux selector enrichment command completed",
				"elapsed_ms", time.Since(start).Milliseconds(),
				"session_count", len(enriched.Entries),
			)
		}
		return EnrichedMsg{Snapshot: enriched, Err: err}
	}
}

func (m Model) switchCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		if m.switcher == nil {
			return SwitchResultMsg{Err: fmt.Errorf("tmux switcher unavailable")}
		}
		target := switchTarget(entry)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return SwitchResultMsg{Err: m.switcher.SwitchClient(ctx, target)}
	}
}

func (m Model) attachCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		if m.switcher == nil {
			return SwitchResultMsg{Err: fmt.Errorf("tmux switcher unavailable")}
		}
		target := switchTarget(entry)
		if target == "" {
			return SwitchResultMsg{Err: fmt.Errorf("selected session has no tmux target")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		switchStarted := time.Now()
		if err := m.switcher.SwitchClient(ctx, target); err != nil {
			slog.Default().Warn("tmux selector attach switch failed",
				"elapsed_ms", time.Since(switchStarted).Milliseconds(),
				"session_id", target,
				"error", err,
			)
			return SwitchResultMsg{Err: err}
		}
		slog.Default().Info("tmux selector attach switch completed",
			"elapsed_ms", time.Since(switchStarted).Milliseconds(),
			"session_id", target,
		)
		m.closePopupAfterAttach(ctx, target)

		if !m.shouldOpenDetailAfterAttach(entry) {
			return SwitchResultMsg{}
		}
		openStarted := time.Now()
		if err := m.detailOpener.OpenDetail(ctx, entry); err != nil {
			slog.Default().Warn("tmux selector attach workspace open failed",
				"elapsed_ms", time.Since(openStarted).Milliseconds(),
				"session_id", target,
				"issue_id", entryIssueID(entry),
				"error", err,
			)
			return SwitchResultMsg{}
		}
		slog.Default().Info("tmux selector attach workspace open completed",
			"elapsed_ms", time.Since(openStarted).Milliseconds(),
			"session_id", target,
			"issue_id", entryIssueID(entry),
		)
		return SwitchResultMsg{}
	}
}

func (m Model) closePopupAfterAttach(ctx context.Context, target string) {
	if m.popupCloser == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if err := m.popupCloser.ClosePopup(closeCtx); err != nil {
		slog.Default().Debug("tmux selector attach popup close failed",
			"elapsed_ms", time.Since(startedAt).Milliseconds(),
			"session_id", target,
			"error", err,
		)
		return
	}
	slog.Default().Info("tmux selector attach popup close completed",
		"elapsed_ms", time.Since(startedAt).Milliseconds(),
		"session_id", target,
	)
}

func switchTarget(entry InventoryEntry) string {
	target := strings.TrimSpace(entry.SessionID)
	if target == "" {
		target = strings.TrimSpace(entry.IssueID)
	}
	return target
}

func (m Model) shouldOpenDetailAfterAttach(entry InventoryEntry) bool {
	return m.detailOpener != nil && strings.TrimSpace(entry.SessionID) != defaultFullAzSession && entryIssueID(entry) != ""
}

func (m Model) killCmd(entry InventoryEntry) tea.Cmd {
	label := killTargetLabel(entry)
	return func() tea.Msg {
		if m.killer == nil {
			return KillResultMsg{SessionID: label, Err: fmt.Errorf("tmux killer unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return KillResultMsg{SessionID: label, Err: m.killer.KillSession(ctx, entry)}
	}
}

func killTargetLabel(entry InventoryEntry) string {
	if id := strings.TrimSpace(entry.SessionID); id != "" {
		return id
	}
	if id := strings.TrimSpace(entry.IssueID); id != "" {
		return id
	}
	return "(unknown)"
}

func (m Model) openDetailCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		switcher, ok := m.switcher.(fullAzSwitcher)
		if !ok {
			return DetailOpenResultMsg{Err: fmt.Errorf("full az tmux switcher unavailable")}
		}
		if m.detailOpener == nil {
			return DetailOpenResultMsg{Err: fmt.Errorf("detail opener unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return DetailOpenResultMsg{Err: openFullAzDetail(ctx, switcher, m.detailOpener, entry)}
	}
}

func (m Model) openDrillDownCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		switcher, ok := m.switcher.(fullAzSwitcher)
		if !ok {
			return DetailOpenResultMsg{Err: fmt.Errorf("full az tmux switcher unavailable")}
		}
		if m.detailOpener == nil {
			return DetailOpenResultMsg{Err: fmt.Errorf("detail opener unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return DetailOpenResultMsg{Err: openFullAzDrillDown(ctx, switcher, m.detailOpener, entry)}
	}
}

func openFullAzDetail(ctx context.Context, switcher fullAzSwitcher, opener DetailOpener, entry InventoryEntry) error {
	return openFullAzIssueCommand(ctx, switcher, entry, func(entry InventoryEntry) error {
		if err := opener.OpenDetail(ctx, entry); err != nil {
			return fmt.Errorf("request full az detail open: %w", err)
		}
		return nil
	})
}

func openFullAzDrillDown(ctx context.Context, switcher fullAzSwitcher, opener DetailOpener, entry InventoryEntry) error {
	return openFullAzIssueCommand(ctx, switcher, entry, func(entry InventoryEntry) error {
		if err := opener.OpenDrillDown(ctx, entry); err != nil {
			return fmt.Errorf("request full az drill-down open: %w", err)
		}
		return nil
	})
}

func openFullAzIssueCommand(ctx context.Context, switcher fullAzSwitcher, entry InventoryEntry, send func(InventoryEntry) error) error {
	issueID := strings.TrimSpace(entry.IssueID)
	if issueID == "" {
		issueID = entry.Task.ID.String()
	}
	if issueID == "" {
		return fmt.Errorf("selected session has no issue id")
	}
	exists, err := switcher.HasSession(ctx, defaultFullAzSession)
	if err != nil {
		return fmt.Errorf("check full az tmux session: %w", err)
	}
	if !exists {
		return fmt.Errorf("full az tmux session %q not found", defaultFullAzSession)
	}
	entry.IssueID = issueID
	if err := send(entry); err != nil {
		return err
	}
	if err := switcher.SwitchClient(ctx, defaultFullAzSession); err != nil {
		return fmt.Errorf("switch to full az tmux session: %w", err)
	}
	return nil
}

func entryIssueID(entry InventoryEntry) string {
	issueID := strings.TrimSpace(entry.IssueID)
	if issueID == "" {
		issueID = strings.TrimSpace(entry.Task.ID.String())
	}
	return issueID
}

func EntriesFromTasks(tasks []domain.Task) []InventoryEntry {
	entries := make([]InventoryEntry, 0, len(tasks))
	for _, task := range tasks {
		if task.Session == nil && !task.HasTmuxSession {
			continue
		}
		entry := InventoryEntry{
			IssueID:               task.ID.String(),
			TaskTitle:             task.Title,
			Task:                  task,
			IssueStatus:           task.Status,
			Priority:              task.Priority,
			Type:                  task.Type,
			HasTmuxSession:        task.HasTmuxSession || task.Session != nil,
			HasWorktree:           task.HasWorktree,
			GitAheadCount:         task.GitAheadCount,
			GitBehindCount:        task.GitBehindCount,
			HasUncommittedChanges: task.HasUncommittedChanges,
			HasConflicts:          task.HasConflicts,
			GitAdditions:          task.GitAdditions,
			GitDeletions:          task.GitDeletions,
		}
		if task.Session != nil {
			entry.State = task.Session.State
			entry.Activity = task.Session.Activity
			entry.ActivitySource = task.Session.ActivitySource
			entry.Worktree = task.Session.Worktree
			entry.StartedAt = task.Session.StartedAt
			entry.HasWorktree = entry.HasWorktree || strings.TrimSpace(task.Session.Worktree) != ""
		}
		if entry.SessionID == "" {
			entry.SessionID = naming.CanonicalSessionID(firstNonEmpty(entry.ProjectID, entry.ProjectPath), task.ID.String())
		}
		entries = append(entries, entry)
	}
	return entries
}

type sessionTreeRow struct {
	entryIndex   int
	entry        InventoryEntry
	last         bool
	ancestorLast []bool
}

func activeSessionTreeRows(entries []InventoryEntry) []sessionTreeRow {
	return activeSessionTreeRowsWithAncestors(entries, nil)
}

func (m Model) activeSessionTreeRows(entries []InventoryEntry) []sessionTreeRow {
	return activeSessionTreeRowsWithOptions(entries, m.treeAncestorTasks(), m.treeSort)
}

func (m Model) treeAncestorTasks() map[string]domain.Task {
	if strings.TrimSpace(m.searchQuery) != "" {
		return nil
	}
	return m.snapshotTasksByID()
}

type treeNode struct {
	key        string
	entry      InventoryEntry
	entryIndex int
	order      int
}

func activeSessionTreeRowsWithAncestors(entries []InventoryEntry, ancestorTasks map[string]domain.Task) []sessionTreeRow {
	return activeSessionTreeRowsWithOptions(entries, ancestorTasks, selectorTreeSortOrder)
}

func activeSessionTreeRowsWithOptions(entries []InventoryEntry, ancestorTasks map[string]domain.Task, treeSort selectorTreeSort) []sessionTreeRow {
	if len(entries) == 0 {
		return nil
	}
	nodes := make(map[string]*treeNode, len(entries)+len(ancestorTasks))
	activeKeys := make([]string, 0, len(entries))
	for i, entry := range entries {
		key := entryTreeKey(entry)
		if key == "" {
			continue
		}
		nodes[key] = &treeNode{key: key, entry: entry, entryIndex: i, order: i}
		activeKeys = append(activeKeys, key)
	}
	for i, entry := range entries {
		for parentID := entryParentID(entry); parentID != ""; {
			if _, ok := nodes[parentID]; ok {
				break
			}
			task, ok := ancestorTasks[parentID]
			if !ok {
				break
			}
			nodes[parentID] = &treeNode{
				key:        parentID,
				entry:      inventoryEntryFromAncestorTask(task),
				entryIndex: -1,
				order:      i,
			}
			parentID = taskParentID(task)
		}
	}
	children := make(map[string][]string, len(nodes))
	rootsByKey := make(map[string]struct{}, len(nodes))
	for key, node := range nodes {
		parentID := entryParentID(node.entry)
		if parentID == "" || parentID == key {
			rootsByKey[key] = struct{}{}
			continue
		}
		if _, ok := nodes[parentID]; !ok {
			rootsByKey[key] = struct{}{}
			continue
		}
		children[parentID] = append(children[parentID], key)
	}
	structuralLess := func(leftKey, rightKey string) bool {
		left, right := nodes[leftKey], nodes[rightKey]
		if left.order != right.order {
			return left.order < right.order
		}
		if left.entryIndex != right.entryIndex {
			return left.entryIndex < right.entryIndex
		}
		return leftKey < rightKey
	}
	latestByKey := map[string]time.Time{}
	var latestForKey func(string) time.Time
	latestForKey = func(key string) time.Time {
		if latest, ok := latestByKey[key]; ok {
			return latest
		}
		node := nodes[key]
		var latest time.Time
		if node != nil && node.entryIndex >= 0 {
			if updatedAt := entryLastUpdatedAt(node.entry); updatedAt != nil && !updatedAt.IsZero() {
				latest = updatedAt.UTC()
			}
		}
		for _, childKey := range children[key] {
			childLatest := latestForKey(childKey)
			if !childLatest.IsZero() && (latest.IsZero() || childLatest.After(latest)) {
				latest = childLatest
			}
		}
		latestByKey[key] = latest
		return latest
	}
	sortTreeKeys := func(keys []string) {
		sort.SliceStable(keys, func(i, j int) bool {
			if treeSort == selectorTreeSortUpdated {
				leftUpdated, rightUpdated := latestForKey(keys[i]), latestForKey(keys[j])
				if leftUpdated.IsZero() != rightUpdated.IsZero() {
					return !leftUpdated.IsZero()
				}
				if !leftUpdated.IsZero() && !rightUpdated.IsZero() && !leftUpdated.Equal(rightUpdated) {
					return leftUpdated.After(rightUpdated)
				}
			}
			return structuralLess(keys[i], keys[j])
		})
	}
	for parentID := range children {
		sortTreeKeys(children[parentID])
	}
	rootFor := func(key string) string {
		seen := map[string]struct{}{}
		for {
			if _, ok := seen[key]; ok {
				return key
			}
			seen[key] = struct{}{}
			parentID := entryParentID(nodes[key].entry)
			if parentID == "" {
				return key
			}
			if _, ok := nodes[parentID]; !ok {
				return key
			}
			key = parentID
		}
	}
	roots := make([]string, 0, len(nodes))
	seenRoots := make(map[string]struct{}, len(nodes))
	for _, key := range activeKeys {
		root := rootFor(key)
		if _, ok := seenRoots[root]; ok {
			continue
		}
		seenRoots[root] = struct{}{}
		roots = append(roots, root)
		delete(rootsByKey, root)
	}
	for key := range rootsByKey {
		if _, ok := seenRoots[key]; ok {
			continue
		}
		roots = append(roots, key)
	}
	sortTreeKeys(roots)
	rows := make([]sessionTreeRow, 0, len(nodes))
	visited := make(map[string]bool, len(nodes))
	var walk func(keys []string, ancestors []bool)
	walk = func(keys []string, ancestors []bool) {
		for pos, key := range keys {
			node := nodes[key]
			if node == nil || visited[key] {
				continue
			}
			visited[key] = true
			last := pos == len(keys)-1
			rows = append(rows, sessionTreeRow{
				entryIndex:   node.entryIndex,
				entry:        node.entry,
				last:         last,
				ancestorLast: append([]bool(nil), ancestors...),
			})
			walk(children[key], append(append([]bool(nil), ancestors...), last))
		}
	}
	walk(roots, nil)
	return rows
}

func entryParentID(entry InventoryEntry) string {
	return taskParentID(entry.Task)
}

func taskParentID(task domain.Task) string {
	if task.ParentID != nil {
		return strings.TrimSpace(task.ParentID.String())
	}
	for _, dep := range task.Dependencies {
		if dep.Type == domain.DependencyParentChild || string(dep.Type) == "parent_child" {
			if parentID := strings.TrimSpace(dep.ID.String()); parentID != "" {
				return parentID
			}
		}
	}
	return ""
}

func entryTreeKey(entry InventoryEntry) string {
	if issueID := entryIssueID(entry); issueID != "" {
		return issueID
	}
	sessionID := strings.TrimSpace(entry.SessionID)
	if sessionID == "" {
		return ""
	}
	return "session:" + sessionID
}

func inventoryEntryFromAncestorTask(task domain.Task) InventoryEntry {
	entry := InventoryEntry{
		IssueID:     strings.TrimSpace(task.ID.String()),
		TaskTitle:   strings.TrimSpace(task.Title),
		IssueStatus: task.Status,
		Priority:    task.Priority,
		Type:        task.Type,
		Task:        task,
	}
	if task.Session != nil {
		entry.State = task.Session.State
		entry.Activity = task.Session.Activity
		entry.ActivitySource = task.Session.ActivitySource
		entry.Worktree = task.Session.Worktree
		entry.StartedAt = task.Session.StartedAt
		entry.HasWorktree = strings.TrimSpace(task.Session.Worktree) != ""
	}
	if entry.State == "" {
		entry.State = domain.SessionIdle
	}
	return entry
}

func treePrefix(ancestorLast []bool, last bool) string {
	if len(ancestorLast) == 0 {
		return ""
	}
	var b strings.Builder
	for _, ancestorIsLast := range ancestorLast[:len(ancestorLast)-1] {
		if ancestorIsLast {
			b.WriteString("   ")
		} else {
			b.WriteString("|  ")
		}
	}
	if last {
		b.WriteString("`- ")
	} else {
		b.WriteString("|- ")
	}
	return b.String()
}

func visibleTreeRows(rows []sessionTreeRow, selectedEntryIndex int, height int) []sessionTreeRow {
	if height <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) <= height {
		return rows
	}
	selected := 0
	for i, row := range rows {
		if row.entryIndex == selectedEntryIndex {
			selected = i
			break
		}
	}
	start := selected - height/2
	if start < 0 {
		start = 0
	}
	if start+height > len(rows) {
		start = len(rows) - height
	}
	return rows[start : start+height]
}

func RenderSessionRow(row SessionRow, selected bool, width int, _ lipgloss.Style, _ lipgloss.Style, _ lipgloss.Style, s *styles.Styles) string {
	if s == nil {
		s = styles.New()
	}
	task := row.Task
	if task.ID.String() == "" {
		task.ID = naming.IssueID(row.IssueID)
	}
	if task.ID.String() == "" {
		task.ID = naming.IssueID(row.SessionID)
	}
	if task.Title == "" {
		task.Title = row.TaskTitle
	}
	if task.Status == "" {
		task.Status = row.IssueStatus
	}
	if task.Priority == 0 && row.Priority != 0 {
		task.Priority = row.Priority
	}
	if task.Type == "" {
		task.Type = row.Type
	}
	if task.Type == "" {
		task.Type = domain.TypeTask
	}
	task.HasTmuxSession = true
	task.HasWorktree = row.HasWorktree
	task.GitAheadCount = row.GitAheadCount
	task.GitBehindCount = row.GitBehindCount
	task.HasUncommittedChanges = row.HasUncommittedChanges
	task.HasConflicts = row.HasConflicts
	task.GitAdditions = row.GitAdditions
	task.GitDeletions = row.GitDeletions
	if row.HasTmuxSession || row.State != "" {
		task.Session = &domain.Session{
			IssueID:        naming.IssueID(row.IssueID),
			State:          rowCardDisplayState(row),
			Activity:       row.Activity,
			ActivitySource: row.ActivitySource,
			StartedAt:      row.StartedAt,
			UpdatedAt:      valueOrZeroTime(entryLastUpdatedAt(row)),
			Worktree:       row.Worktree,
		}
		if task.Session.State == "" {
			task.Session.State = domain.SessionIdle
		}
	}
	signals := &board.RuntimeSignals{
		HasTmuxSession:        true,
		HasWorktree:           row.HasWorktree,
		GitAheadCount:         row.GitAheadCount,
		GitBehindCount:        row.GitBehindCount,
		HasUncommittedChanges: row.HasUncommittedChanges,
		HasConflicts:          row.HasConflicts,
		GitAdditions:          row.GitAdditions,
		GitDeletions:          row.GitDeletions,
	}
	project := firstNonEmpty(row.ProjectPath, row.ProjectID)
	metaParts := []string{}
	if issueStatus := issueStatusLabel(row); issueStatus != "" {
		metaParts = append(metaParts, renderIssueMeta(issueStatus))
	}
	if updated := renderUpdatedMeta(row, time.Now(), s); updated != "" {
		metaParts = append(metaParts, updated)
	}
	if row.SessionID != "" {
		metaParts = append(metaParts, renderTmuxSessionMeta(row, s))
	}
	if project != "" {
		metaParts = append(metaParts, s.StatusHint.Render(project))
	}
	origin := task.Origin
	if len(metaParts) > 0 {
		// Origin badge will be rendered at the bottom-right of the meta line
		// instead of the inner content line, so suppress the in-card overlay.
		task.Origin = ""
	}
	card := board.RenderCardWithRuntimeSignals(task, signals, selected, false, width, s)
	card = compactSelectorCard(card)
	if graph := renderGraphAncestorMeta(row, s); graph != "" {
		card = insertCardMetaLine(card, graph, "", s)
	}
	if len(metaParts) == 0 {
		return card
	}
	meta := strings.Join(metaParts, "  ")
	return insertCardMetaLine(card, meta, origin, s)
}

func entryDisplayLabel(entry InventoryEntry) string {
	session := domain.Session{
		State:    entry.State,
		Activity: entry.Activity,
	}
	if label := session.DisplayLabel(); strings.TrimSpace(label) != "" {
		return label
	}
	return strings.TrimSpace(entry.State.String())
}

func entryDisplayID(entry InventoryEntry) string {
	if issueID := entryIssueID(entry); issueID != "" {
		if sessionID := strings.TrimSpace(entry.SessionID); sessionID != "" {
			return sessionID
		}
		if project := firstNonEmpty(entry.ProjectID, entry.ProjectPath); strings.TrimSpace(project) != "" {
			return naming.CanonicalSessionID(project, issueID)
		}
		return issueID
	}
	return strings.TrimSpace(entry.SessionID)
}

func issueStatusLabel(entry InventoryEntry) string {
	if entryIssueID(entry) == "" {
		return ""
	}
	status := entry.IssueStatus
	if status == "" {
		status = entry.Task.Status
	}
	return strings.TrimSpace(status.String())
}

func renderBracketed(label string, style lipgloss.Style) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	return "[" + style.Render(label) + "]"
}

func sessionLabelStyle(label string, s *styles.Styles) lipgloss.Style {
	if s == nil {
		s = styles.New()
	}
	switch strings.TrimSpace(strings.ToLower(label)) {
	case "busy", "working":
		return s.SessionBusy
	case "waiting":
		return s.SessionWaiting
	case "done":
		return s.SessionDone
	case "error":
		return s.SessionError
	case "paused":
		return s.SessionPaused
	case "idle", "no-agent", "unknown":
		return s.SessionIdle
	default:
		return s.StatusInfo
	}
}

func issueStatusStyle(status string) lipgloss.Style {
	color, ok := styles.StatusColors[strings.TrimSpace(strings.ToLower(status))]
	if !ok {
		color = styles.Subtext0
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

func renderIssueMeta(status string) string {
	return lipgloss.NewStyle().Foreground(styles.Overlay1).Render("issue ") + issueStatusStyle(status).Render(status)
}

func renderUpdatedMeta(entry InventoryEntry, now time.Time, s *styles.Styles) string {
	updated := formatEntryUpdatedAge(entry, now)
	if updated == "" {
		return ""
	}
	if s == nil {
		s = styles.New()
	}
	return lipgloss.NewStyle().Foreground(styles.Overlay1).Render("updated ") + s.StatusHint.Render(updated)
}

func renderGraphAncestorMeta(entry InventoryEntry, s *styles.Styles) string {
	if len(entry.GraphAncestors) == 0 {
		return ""
	}
	if s == nil {
		s = styles.New()
	}
	return lipgloss.NewStyle().Foreground(styles.Overlay1).Render("under ") +
		s.StatusInfo.Render(strings.Join(entry.GraphAncestors, " > "))
}

func rowCardDisplayState(row InventoryEntry) domain.SessionState {
	session := domain.Session{
		State:    row.State,
		Activity: row.Activity,
	}
	if displayState, ok := session.DisplayState(); ok {
		return displayState
	}
	switch session.DisplayActivity() {
	case "unknown", "no-agent":
		return domain.SessionIdle
	}
	return row.State
}

func tmuxSessionMeta(entry InventoryEntry) string {
	sessionID := strings.TrimSpace(entry.SessionID)
	if sessionID == "" {
		return ""
	}
	count := entry.TmuxAttachedCount
	if entry.TmuxAttached && count <= 0 {
		count = 1
	}
	if count > 1 {
		return fmt.Sprintf("tmux %s  attached x%d", sessionID, count)
	}
	if count == 1 {
		return "tmux " + sessionID + "  attached"
	}
	return "tmux " + sessionID
}

func renderTmuxSessionMeta(entry InventoryEntry, s *styles.Styles) string {
	meta := tmuxSessionMeta(entry)
	if meta == "" {
		return ""
	}
	if entry.TmuxAttached || entry.TmuxAttachedCount > 0 {
		return lipgloss.NewStyle().Foreground(styles.Teal).Bold(true).Render(meta)
	}
	if s == nil {
		s = styles.New()
	}
	return s.StatusInfo.Render(meta)
}

func compactSelectorCard(card string) string {
	lines := strings.Split(card, "\n")
	if len(lines) < 4 {
		return card
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[0])
	for i := 1; i < len(lines)-1; i++ {
		if isBlankCardInteriorLine(lines[i]) {
			continue
		}
		out = append(out, lines[i])
	}
	out = append(out, lines[len(lines)-1])
	if len(out) < 3 {
		return card
	}
	return strings.Join(out, "\n")
}

func isBlankCardInteriorLine(line string) bool {
	stripped := ansi.Strip(line)
	if !strings.HasPrefix(stripped, "│") || !strings.HasSuffix(stripped, "│") {
		return false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(stripped, "│"), "│"))
	return inner == ""
}

func insertCardMetaLine(card string, meta string, origin string, s *styles.Styles) string {
	meta = strings.TrimSpace(meta)
	if meta == "" {
		return card
	}
	lines := strings.Split(card, "\n")
	if len(lines) < 2 {
		return lipgloss.JoinVertical(lipgloss.Left, card, s.StatusInfo.Render(meta))
	}
	width := ansi.StringWidth(lines[0])
	innerWidth := maxInt(1, width-4)

	badge := board.RenderOriginBadge(origin)
	badgeWidth := ansi.StringWidth(badge)
	if badgeWidth >= innerWidth-1 {
		// Card is too narrow to fit both meta and badge; drop the badge.
		badge = ""
		badgeWidth = 0
	}
	metaRoom := innerWidth - badgeWidth
	if badgeWidth > 0 {
		metaRoom -= 1 // single-space gap before badge
	}
	if metaRoom < 1 {
		metaRoom = 1
	}
	meta = ansi.Truncate(meta, metaRoom, "…")
	renderedMeta := meta
	padding := maxInt(0, innerWidth-ansi.StringWidth(renderedMeta)-badgeWidth)
	referenceLine := lines[maxInt(0, len(lines)-2)]
	leftBorder := ansi.Cut(referenceLine, 0, 1)
	rightBorder := ansi.Cut(referenceLine, maxInt(0, width-1), width)
	metaLine := leftBorder + " " + renderedMeta + strings.Repeat(" ", padding) + badge + " " + rightBorder
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:len(lines)-1]...)
	out = append(out, metaLine)
	out = append(out, lines[len(lines)-1])
	return strings.Join(out, "\n")
}

type VisibleRow struct {
	Index int
	Row   SessionRow
}

func VisibleRows(rows []SessionRow, cursor int, maxRows int) []VisibleRow {
	if maxRows <= 0 || len(rows) <= maxRows {
		out := make([]VisibleRow, 0, len(rows))
		for i, row := range rows {
			out = append(out, VisibleRow{Index: i, Row: row})
		}
		return out
	}
	start := cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start+maxRows > len(rows) {
		start = len(rows) - maxRows
	}
	out := make([]VisibleRow, 0, maxRows)
	for i := start; i < start+maxRows; i++ {
		out = append(out, VisibleRow{Index: i, Row: rows[i]})
	}
	return out
}

func RenderVisibleRows(rows []SessionRow, cursor int, width int, availableHeight int, s *styles.Styles) []string {
	return RenderVisibleGrid(rows, cursor, 1, width, availableHeight, s)
}

func RenderVisibleGrid(rows []SessionRow, cursor int, columns int, cardWidth int, availableHeight int, s *styles.Styles) []string {
	return RenderVisibleGridWithLabels(rows, cursor, columns, cardWidth, availableHeight, s, nil)
}

func RenderVisibleGridWithLabels(rows []SessionRow, cursor int, columns int, cardWidth int, availableHeight int, s *styles.Styles, labels map[int]string) []string {
	if len(rows) == 0 || availableHeight <= 0 {
		return nil
	}
	if columns <= 0 {
		columns = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	rendered := make([]string, len(rows))
	for i, row := range rows {
		rendered[i] = RenderSessionRow(row, i == cursor, cardWidth, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, s)
		if label := strings.TrimSpace(labels[i]); label != "" {
			rendered[i] = insertJumpLabel(rendered[i], label, s)
		}
	}

	start, end := visibleGridRowRange(rendered, cursor, columns, availableHeight)
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, renderGridRow(rendered, i, columns))
	}
	return out
}

func VisibleGridIndices(rows []SessionRow, cursor int, columns int, cardWidth int, availableHeight int, s *styles.Styles) []int {
	if len(rows) == 0 || availableHeight <= 0 {
		return nil
	}
	if columns <= 0 {
		columns = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	rendered := make([]string, len(rows))
	for i, row := range rows {
		rendered[i] = RenderSessionRow(row, i == cursor, cardWidth, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, s)
	}
	start, end := visibleGridRowRange(rendered, cursor, columns, availableHeight)
	indices := make([]int, 0, (end-start)*columns)
	for gridRow := start; gridRow < end; gridRow++ {
		rowStart := gridRow * columns
		rowEnd := rowStart + columns
		if rowEnd > len(rows) {
			rowEnd = len(rows)
		}
		for i := rowStart; i < rowEnd; i++ {
			indices = append(indices, i)
		}
	}
	return indices
}

func visibleGridRowRange(rendered []string, cursor int, columns int, availableHeight int) (int, int) {
	if len(rendered) == 0 {
		return 0, 0
	}
	if columns <= 0 {
		columns = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rendered) {
		cursor = len(rendered) - 1
	}
	heights := gridRowHeights(rendered, columns)
	return visibleGridRowRangeForHeights(heights, cursor, columns, availableHeight)
}

func gridRowHeights(rendered []string, columns int) []int {
	if len(rendered) == 0 {
		return nil
	}
	if columns <= 0 {
		columns = 1
	}
	gridRows := (len(rendered) + columns - 1) / columns
	heights := make([]int, gridRows)
	for gridRow := 0; gridRow < gridRows; gridRow++ {
		start := gridRow * columns
		end := start + columns
		if end > len(rendered) {
			end = len(rendered)
		}
		maxHeight := 0
		for i := start; i < end; i++ {
			maxHeight = maxInt(maxHeight, lipgloss.Height(rendered[i]))
		}
		heights[gridRow] = maxHeight + 1
	}
	return heights
}

func visibleGridRowRangeForHeights(heights []int, cursor int, columns int, availableHeight int) (int, int) {
	if len(heights) == 0 {
		return 0, 0
	}
	if columns <= 0 {
		columns = 1
	}
	gridRows := len(heights)

	cursorGridRow := cursor / columns
	if cursorGridRow < 0 {
		cursorGridRow = 0
	}
	if cursorGridRow >= gridRows {
		cursorGridRow = gridRows - 1
	}
	start, end := cursorGridRow, cursorGridRow+1
	used := heights[cursorGridRow]
	for {
		added := false
		if start > 0 && used+heights[start-1] <= availableHeight {
			start--
			used += heights[start]
			added = true
		}
		if end < gridRows && used+heights[end] <= availableHeight {
			used += heights[end]
			end++
			added = true
		}
		if !added {
			break
		}
	}
	return start, end
}

func renderGridRow(rendered []string, gridRow int, columns int) string {
	start := gridRow * columns
	end := start + columns
	if end > len(rendered) {
		end = len(rendered)
	}
	cells := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		cell := rendered[i]
		if i < end-1 {
			cell = lipgloss.NewStyle().MarginRight(2).Render(cell)
		}
		cells = append(cells, cell)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

func insertJumpLabel(card string, label string, _ *styles.Styles) string {
	lines := strings.Split(card, "\n")
	if len(lines) == 0 {
		return card
	}
	// Pink background with the dark base foreground keeps the label readable
	// against any cell content and avoids the colors used by priority badges
	// (Red/Peach/Yellow/Green/Overlay0) and type badges (Mauve/Green/Red/Blue/
	// Yellow), so it pops without colliding with existing semantics.
	label = jumpLabelStyle.Render(label)
	for i, line := range lines {
		if next, ok := insertJumpLabelInCardLine(line, label); ok {
			lines[i] = next
			return strings.Join(lines, "\n")
		}
	}
	lines[0] = label + " " + lines[0]
	return strings.Join(lines, "\n")
}

var jumpLabelStyle = lipgloss.NewStyle().
	Foreground(styles.Base).
	Background(styles.Pink).
	Bold(true)

func insertJumpLabelInCardLine(line string, label string) (string, bool) {
	// Cards are styled with BorderForeground(...), so each "│" is wrapped in
	// ANSI escape codes (e.g. "\x1b[38;...m│\x1b[0m"). The literal "│ "
	// substring therefore never appears in colored output, which is why a
	// byte-level Index/LastIndex search would fall through to the
	// outside-the-card fallback in production.
	stripped := ansi.Strip(line)
	if !strings.HasPrefix(stripped, "│") || !strings.HasSuffix(stripped, "│") {
		return "", false
	}
	visWidth := ansi.StringWidth(line)
	if visWidth < 5 {
		return "", false
	}
	contentWidth := visWidth - 4 // strip 1 border + 1 padding on each side
	labelPrefix := label + " "
	labelWidth := ansi.StringWidth(labelPrefix)
	if labelWidth >= contentWidth {
		return "", false
	}

	leftBorder := ansi.Cut(line, 0, 1)
	rightBorder := ansi.Cut(line, visWidth-1, visWidth)
	content := ansi.Cut(line, 2, visWidth-2)

	keepWidth := contentWidth - labelWidth
	truncated := ansi.Truncate(content, keepWidth, "…")
	padding := maxInt(0, contentWidth-labelWidth-ansi.StringWidth(truncated))
	return leftBorder + " " + labelPrefix + truncated + strings.Repeat(" ", padding) + " " + rightBorder, true
}

func gridColumnCount(width int) int {
	const (
		minCardWidth = 42
		gapWidth     = 2
		maxColumns   = 3
	)
	available := maxInt(1, width-4)
	columns := (available + gapWidth) / (minCardWidth + gapWidth)
	return clampInt(columns, 1, maxColumns)
}

func gridColumnCountForViewport(width int, rows []SessionRow, cursor int, availableHeight int, s *styles.Styles) int {
	maxColumns := gridColumnCount(width)
	if maxColumns <= 1 || len(rows) == 0 || availableHeight <= 0 {
		return maxColumns
	}
	bestColumns := maxColumns
	bestVisible := -1
	bestUsedHeight := -1
	for columns := 1; columns <= maxColumns; columns++ {
		cardWidth := gridCardWidth(width, columns)
		visible, usedHeight := visibleGridMetrics(rows, cursor, columns, cardWidth, availableHeight, s)
		if visible == len(rows) {
			bestColumns = columns
			bestVisible = visible
			bestUsedHeight = usedHeight
			continue
		}
		if visible > bestVisible ||
			(visible == bestVisible && usedHeight > bestUsedHeight) ||
			(visible == bestVisible && usedHeight == bestUsedHeight && columns > bestColumns) {
			bestColumns = columns
			bestVisible = visible
			bestUsedHeight = usedHeight
		}
	}
	return bestColumns
}

func visibleGridMetrics(rows []SessionRow, cursor int, columns int, cardWidth int, availableHeight int, s *styles.Styles) (visible int, usedHeight int) {
	if len(rows) == 0 || availableHeight <= 0 {
		return 0, 0
	}
	if columns <= 0 {
		columns = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	rendered := make([]string, len(rows))
	for i, row := range rows {
		rendered[i] = RenderSessionRow(row, i == cursor, cardWidth, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, s)
	}
	heights := gridRowHeights(rendered, columns)
	start, end := visibleGridRowRangeForHeights(heights, cursor, columns, availableHeight)
	for gridRow := start; gridRow < end; gridRow++ {
		rowStart := gridRow * columns
		rowEnd := rowStart + columns
		if rowEnd > len(rows) {
			rowEnd = len(rows)
		}
		visible += rowEnd - rowStart
		usedHeight += heights[gridRow]
	}
	return visible, usedHeight
}

func gridCardWidth(width int, columns int) int {
	if columns <= 0 {
		columns = 1
	}
	const gapWidth = 2
	available := maxInt(1, width-4)
	cardWidth := (available-gapWidth*(columns-1))/columns - 2
	return clampInt(cardWidth, 36, 96)
}

func formatSnapshotStatus(snapshot Snapshot, rows int) string {
	parts := []string{fmt.Sprintf("%d sessions", rows)}
	if snapshot.Revision > 0 {
		parts = append(parts, fmt.Sprintf("rev %d", snapshot.Revision))
	}
	if snapshot.Freshness != "" {
		parts = append(parts, snapshot.Freshness)
	}
	return strings.Join(parts, "  ")
}

func formatEntryUpdatedAge(entry InventoryEntry, now time.Time) string {
	updatedAt := entryLastUpdatedAt(entry)
	if updatedAt == nil || updatedAt.IsZero() {
		return ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.Before(*updatedAt) {
		return "0s"
	}
	return formatShortDuration(now.Sub(*updatedAt))
}

func entryLastUpdatedAt(entry InventoryEntry) *time.Time {
	var latest time.Time
	consider := func(candidate time.Time) {
		if candidate.IsZero() {
			return
		}
		candidate = candidate.UTC()
		if latest.IsZero() || candidate.After(latest) {
			latest = candidate
		}
	}
	if entry.LastUpdatedAt != nil {
		consider(*entry.LastUpdatedAt)
	}
	consider(entry.Task.RuntimeUpdatedAt)
	consider(entry.Task.UpdatedAt)
	if entry.LastAttachedAt != nil {
		consider(*entry.LastAttachedAt)
	}
	if entry.StartedAt != nil {
		consider(*entry.StartedAt)
	}
	if latest.IsZero() {
		return nil
	}
	return &latest
}

func formatShortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
	}
}

func valueOrZeroTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func ParseAzedarachSessionName(sessionName string) (ParsedSessionName, bool) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" || sessionName == defaultFullAzSession {
		return ParsedSessionName{}, false
	}
	prefix, _, ok := strings.Cut(sessionName, "-")
	if ok && len(prefix) == 2 {
		issueID, parsed := naming.ParseIssueIDFromSessionName(sessionName, prefix)
		if parsed && strings.TrimSpace(issueID) != "" {
			return ParsedSessionName{IssueID: naming.IssueID(issueID), Project: prefix}, true
		}
	}
	return ParsedSessionName{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ tea.Model = Model{}
