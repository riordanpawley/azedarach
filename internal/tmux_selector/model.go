package tmuxselector

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	StartedAt             *time.Time
	LastAttachedAt        *time.Time
	HasTmuxSession        bool
	HasWorktree           bool
	GitAheadCount         int
	GitBehindCount        int
	HasUncommittedChanges bool
	HasConflicts          bool
	GitAdditions          int
	GitDeletions          int
	Task                  domain.Task
}

type SessionRow = InventoryEntry
type Entry = InventoryEntry

type switchCompleteMsg = SwitchResultMsg

type Snapshot struct {
	Entries          []InventoryEntry
	Tasks            []domain.Task
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
}

type DetailOpenerFunc func(context.Context, InventoryEntry) error

func (f DetailOpenerFunc) OpenDetail(ctx context.Context, entry InventoryEntry) error {
	return f(ctx, entry)
}

type fullAzSwitcher interface {
	HasSession(context.Context, string) (bool, error)
	SwitchClient(context.Context, string) error
}

type Option func(*Model)

func WithSwitcher(switcher Switcher) Option {
	return func(m *Model) {
		m.switcher = switcher
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

type Model struct {
	loader       SnapshotLoader
	switcher     Switcher
	killer       Killer
	detailOpener DetailOpener
	styles       *styles.Styles

	snapshot           Snapshot
	cursor             int
	width              int
	height             int
	loading            bool
	err                error
	status             string
	defaultedToCurrent bool

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

func New(loader SnapshotLoader, opts ...Option) Model {
	m := Model{
		loader:  loader,
		styles:  styles.New(),
		width:   88,
		height:  24,
		loading: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&m)
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return m.loadCmd()
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
		if liveLoader, ok := m.loader.(LiveSnapshotLoader); ok && m.snapshot.Enriching {
			return m, m.enrichCmd(liveLoader, m.snapshot)
		}
		return m, nil
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
		if m.jumpMode != nil {
			next, cmd := m.jumpMode.Update(msg)
			if jump, ok := next.(*overlay.JumpMode); ok {
				m.jumpMode = jump
			}
			return m, cmd
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
		case "1", "2", "3", "4", "5", "6", "7", "8", "9", "0":
			m.selectCardHotkey(msg.String())
			return m, nil
		case "g":
			m.gotoArmed = true
			m.status = "goto: press w for labels"
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
		case "enter", "a":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			if m.shouldOpenDetailOnSwitch(entry) {
				if issueID := entryIssueID(entry); issueID != "" {
					m.status = fmt.Sprintf("Opening %s in full az...", issueID)
				}
				return m, m.openDetailAndSwitchCmd(entry)
			}
			return m, m.switchCmd(entry)
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
	if current := strings.TrimSpace(m.snapshot.CurrentSessionID); current != "" && !m.defaultedToCurrent {
		for i, entry := range m.snapshot.Entries {
			if strings.TrimSpace(entry.SessionID) == current {
				m.cursor = i
				m.defaultedToCurrent = true
				break
			}
		}
	}
	if m.cursor >= len(m.snapshot.Entries) {
		m.cursor = len(m.snapshot.Entries) - 1
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
	for i, entry := range m.snapshot.Entries {
		if strings.TrimSpace(entry.SessionID) == sessionID {
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
	if strings.TrimSpace(m.status) != "" {
		b.WriteString(m.styles.StatusInfo.Render(m.status))
		b.WriteString("\n")
	}
	if m.err != nil {
		b.WriteString(m.styles.ToastError.Render("Error: " + m.err.Error()))
		b.WriteString("\n\n")
	}
	if len(m.snapshot.Entries) == 0 {
		b.WriteString("No tmux sessions found.\n")
	} else {
		availableHeight := m.gridAvailableHeight()
		columns := m.gridColumnCount(availableHeight)
		cardWidth := gridCardWidth(m.width, columns)
		rows := RenderVisibleGridWithLabels(
			m.snapshot.Entries,
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
	limit := len(m.snapshot.Entries)
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
	right := m.styles.StatusHint.Render(keybinds.RenderPlain([]keybinds.Binding{
		{Key: "h/j/k/l", Description: "move"},
		{Key: "gw", Description: "labels"},
		{Key: "Enter/a", Description: "switch"},
		{Key: "o/Space", Description: "open in az"},
		{Key: "x", Description: "kill"},
		{Key: "r", Description: "refresh"},
		{Key: "q/Esc", Description: "close"},
	}, "  "))
	gap := maxInt(0, m.width-ansi.StringWidth(right))
	if gap > 0 {
		return strings.Repeat(" ", gap) + right
	}
	return ansi.Truncate(right, maxInt(1, m.width), "…")
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

func (m *Model) moveCursor(dx int, dy int) {
	count := len(m.snapshot.Entries)
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

func (m *Model) selectCardHotkey(key string) {
	index := -1
	switch key {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		index = int(key[0] - '0')
	}
	if index >= 0 && index < len(m.snapshot.Entries) {
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
	azIndex := -1
	for i, entry := range entries {
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
	if len(m.snapshot.Entries) == 0 {
		return nil
	}
	availableHeight := m.gridAvailableHeight()
	columns := m.gridColumnCount(availableHeight)
	cardWidth := gridCardWidth(m.width, columns)
	return VisibleGridIndices(m.snapshot.Entries, m.cursor, columns, cardWidth, availableHeight, m.styles)
}

func (m Model) gridColumnCount(availableHeight int) int {
	return gridColumnCountForViewport(m.width, m.snapshot.Entries, m.cursor, availableHeight, m.styles)
}

func (m Model) gridAvailableHeight() int {
	contentHeight := maxInt(0, m.height-1)
	used := 0
	if strings.TrimSpace(m.status) != "" {
		used += lipgloss.Height(m.styles.StatusInfo.Render(m.status))
	}
	if m.err != nil {
		used += lipgloss.Height(m.styles.ToastError.Render("Error: "+m.err.Error())) + 1
	}
	return maxInt(1, contentHeight-used)
}

func (m Model) selectedEntry() (InventoryEntry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.snapshot.Entries) {
		return InventoryEntry{}, false
	}
	return m.snapshot.Entries[m.cursor], true
}

func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		if m.loader == nil {
			return LoadFailedMsg{Err: fmt.Errorf("snapshot loader unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if liveLoader, ok := m.loader.(LiveSnapshotLoader); ok {
			snapshot, err := liveLoader.ListLiveSnapshot(ctx)
			if err != nil {
				return LoadFailedMsg{Err: err}
			}
			return LoadedMsg{Snapshot: snapshot}
		}
		snapshot, err := m.loader.ListTasksSnapshot(ctx)
		if err != nil {
			return LoadFailedMsg{Err: err}
		}
		return LoadedMsg{Snapshot: snapshot}
	}
}

func (m Model) enrichCmd(loader LiveSnapshotLoader, snapshot Snapshot) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		enriched, err := loader.EnrichSnapshot(ctx, snapshot)
		return EnrichedMsg{Snapshot: enriched, Err: err}
	}
}

func (m Model) switchCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		if m.switcher == nil {
			return SwitchResultMsg{Err: fmt.Errorf("tmux switcher unavailable")}
		}
		target := strings.TrimSpace(entry.SessionID)
		if target == "" {
			target = strings.TrimSpace(entry.IssueID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return SwitchResultMsg{Err: m.switcher.SwitchClient(ctx, target)}
	}
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

func (m Model) shouldOpenDetailOnSwitch(entry InventoryEntry) bool {
	return m.detailOpener != nil && strings.TrimSpace(entry.SessionID) != defaultFullAzSession && entryIssueID(entry) != ""
}

func (m Model) openDetailAndSwitchCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		if m.switcher == nil {
			return SwitchResultMsg{Err: fmt.Errorf("tmux switcher unavailable")}
		}
		if m.detailOpener == nil {
			return SwitchResultMsg{Err: fmt.Errorf("detail opener unavailable")}
		}
		issueID := entryIssueID(entry)
		if issueID == "" {
			return SwitchResultMsg{Err: fmt.Errorf("selected session has no issue id")}
		}
		entry.IssueID = issueID
		target := strings.TrimSpace(entry.SessionID)
		if target == "" {
			target = issueID
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.detailOpener.OpenDetail(ctx, entry); err != nil {
			return SwitchResultMsg{Err: fmt.Errorf("request full az detail open: %w", err)}
		}
		return SwitchResultMsg{Err: m.switcher.SwitchClient(ctx, target)}
	}
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

func openFullAzDetail(ctx context.Context, switcher fullAzSwitcher, opener DetailOpener, entry InventoryEntry) error {
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
	if err := opener.OpenDetail(ctx, entry); err != nil {
		return fmt.Errorf("request full az detail open: %w", err)
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
			IssueID:   naming.IssueID(row.IssueID),
			State:     row.State,
			StartedAt: row.StartedAt,
			Worktree:  row.Worktree,
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
	if row.SessionID != "" {
		metaParts = append(metaParts, "tmux "+row.SessionID)
	}
	if project != "" {
		metaParts = append(metaParts, project)
	}
	origin := task.Origin
	if len(metaParts) > 0 {
		// Origin badge will be rendered at the bottom-right of the meta line
		// instead of the inner content line, so suppress the in-card overlay.
		task.Origin = ""
	}
	card := board.RenderCardWithRuntimeSignals(task, signals, selected, false, width, s)
	card = compactSelectorCard(card)
	if len(metaParts) == 0 {
		return card
	}
	meta := strings.Join(metaParts, "  ")
	return insertCardMetaLine(card, meta, origin, s)
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
	renderedMeta := s.StatusInfo.Render(meta)
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
	gridRows := (len(rendered) + columns - 1) / columns
	heights := make([]int, gridRows)
	for gridRow := 0; gridRow < gridRows; gridRow++ {
		heights[gridRow] = lipgloss.Height(renderGridRow(rendered, gridRow, columns)) + 1
	}

	cursorGridRow := cursor / columns
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
	start, end := visibleGridRowRange(rendered, cursor, columns, availableHeight)
	for gridRow := start; gridRow < end; gridRow++ {
		rowStart := gridRow * columns
		rowEnd := rowStart + columns
		if rowEnd > len(rows) {
			rowEnd = len(rows)
		}
		visible += rowEnd - rowStart
		usedHeight += lipgloss.Height(renderGridRow(rendered, gridRow, columns)) + 1
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
