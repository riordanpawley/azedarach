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

type Snapshot struct {
	Entries       []InventoryEntry
	Tasks         []domain.Task
	Revision      uint64
	LastCheckedAt time.Time
	Freshness     string
	Enriching     bool
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

type DetailOpener interface {
	OpenDetail(context.Context, string, string) error
}

type DetailOpenerFunc func(context.Context, string, string) error

func (f DetailOpenerFunc) OpenDetail(ctx context.Context, projectPath, issueID string) error {
	return f(ctx, projectPath, issueID)
}

type fullAzSwitcher interface {
	HasSession(context.Context, string) (bool, error)
	NewSessionWithCommand(context.Context, string, string, string) error
	SendKey(context.Context, string, string) error
	SendKeys(context.Context, string, string) error
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

type Model struct {
	loader       SnapshotLoader
	switcher     Switcher
	detailOpener DetailOpener
	styles       *styles.Styles

	snapshot Snapshot
	cursor   int
	width    int
	height   int
	loading  bool
	err      error
	status   string

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

type snapshotLoadedMsg struct {
	snapshot Snapshot
	err      error
}

type switchCompleteMsg = SwitchResultMsg

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
		m.snapshot = msg.Snapshot
		m.normalizeSnapshot()
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
			return m, m.loadCmd()
		case "enter", "a":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			return m, m.switchCmd(entry)
		case " ", "space", "o":
			entry, ok := m.selectedEntry()
			if !ok {
				return m, nil
			}
			m.status = fmt.Sprintf("Opening %s in full az...", entry.IssueID)
			return m, m.openDetailCmd(entry)
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
	if m.cursor >= len(m.snapshot.Entries) {
		m.cursor = len(m.snapshot.Entries) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
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
		columns := gridColumnCount(m.width)
		cardWidth := gridCardWidth(m.width, columns)
		rows := RenderVisibleGridWithLabels(m.snapshot.Entries, m.cursor, columns, cardWidth, maxInt(1, m.height-7), m.styles, m.jumpLabelsByEntry())
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

func (m Model) renderFooter() string {
	right := m.styles.StatusHint.Render(keybinds.RenderPlain([]keybinds.Binding{
		{Key: "h/j/k/l", Description: "move"},
		{Key: "gw", Description: "labels"},
		{Key: "Enter/a", Description: "switch"},
		{Key: "o/Space", Description: "open in az"},
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
	columns := gridColumnCount(m.width)
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

func (m Model) visibleEntryIndices() []int {
	if len(m.snapshot.Entries) == 0 {
		return nil
	}
	columns := gridColumnCount(m.width)
	cardWidth := gridCardWidth(m.width, columns)
	availableHeight := maxInt(1, m.height-7)
	return VisibleGridIndices(m.snapshot.Entries, m.cursor, columns, cardWidth, availableHeight, m.styles)
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

func (m Model) openDetailCmd(entry InventoryEntry) tea.Cmd {
	return func() tea.Msg {
		if m.detailOpener != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return DetailOpenResultMsg{Err: m.detailOpener.OpenDetail(ctx, entry.ProjectPath, entry.IssueID)}
		}
		switcher, ok := m.switcher.(fullAzSwitcher)
		if !ok {
			return DetailOpenResultMsg{Err: fmt.Errorf("detail opener unavailable")}
		}
		return DetailOpenResultMsg{Err: openFullAzDetail(context.Background(), switcher, entry)}
	}
}

func openFullAzDetail(ctx context.Context, switcher fullAzSwitcher, entry InventoryEntry) error {
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
	return fmt.Errorf("open selected issue in running az TUI is not implemented yet; tracked by az issue bxz")
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
	card := board.RenderCardWithRuntimeSignals(task, signals, selected, false, width, s)
	project := firstNonEmpty(row.ProjectPath, row.ProjectID)
	metaParts := []string{}
	if row.SessionID != "" {
		metaParts = append(metaParts, "tmux "+row.SessionID)
	}
	if project != "" {
		metaParts = append(metaParts, project)
	}
	if len(metaParts) == 0 {
		return card
	}
	meta := strings.Join(metaParts, "  ")
	return insertCardMetaLine(card, meta, s)
}

func insertCardMetaLine(card string, meta string, s *styles.Styles) string {
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
	meta = ansi.Truncate(meta, innerWidth, "…")
	padding := maxInt(0, innerWidth-ansi.StringWidth(meta))
	metaLine := "│ " + s.StatusInfo.Render(meta) + strings.Repeat(" ", padding) + " │"
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

func insertJumpLabel(card string, label string, s *styles.Styles) string {
	lines := strings.Split(card, "\n")
	if len(lines) == 0 {
		return card
	}
	label = s.MenuKey.Render(label)
	for i, line := range lines {
		if strings.Contains(line, "│ ") {
			lines[i] = strings.Replace(line, "│ ", "│ "+label+" ", 1)
			return strings.Join(lines, "\n")
		}
	}
	lines[0] = label + " " + lines[0]
	return strings.Join(lines, "\n")
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

func gridCardWidth(width int, columns int) int {
	if columns <= 0 {
		columns = 1
	}
	const gapWidth = 2
	available := maxInt(1, width-4)
	cardWidth := (available - gapWidth*(columns-1)) / columns
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
	prefix, rest, ok := strings.Cut(sessionName, "-")
	if ok && len(prefix) == 2 && strings.TrimSpace(rest) != "" {
		return ParsedSessionName{IssueID: naming.IssueID(rest), Project: prefix}, true
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
