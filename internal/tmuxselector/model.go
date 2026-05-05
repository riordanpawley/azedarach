package tmuxselector

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/ui/board"
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
}

type SnapshotLoader interface {
	ListTasksSnapshot(context.Context) (Snapshot, error)
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
}

type LoadedMsg struct {
	Snapshot Snapshot
}

type LoadFailedMsg struct {
	Err error
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
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.snapshot.Entries)-1 {
				m.cursor++
			}
			return m, nil
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
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
		case " ", "o":
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
	b.WriteString(m.styles.ColumnHeaderActive.Render("Az tmux sessions"))
	b.WriteString("\n")
	if strings.TrimSpace(m.status) != "" {
		b.WriteString(m.styles.StatusInfo.Render(m.status))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(m.styles.ToastError.Render("Error: " + m.err.Error()))
		b.WriteString("\n\n")
	}
	if len(m.snapshot.Entries) == 0 {
		b.WriteString("No Azedarach issue sessions found.\n\n")
	} else {
		cardWidth := clampInt(m.width-4, 36, 96)
		maxRows := maxInt(1, (m.height-6)/6)
		for _, visible := range VisibleRows(m.snapshot.Entries, m.cursor, maxRows) {
			b.WriteString(RenderSessionRow(visible.Row, visible.Index == m.cursor, cardWidth, lipgloss.Style{}, lipgloss.Style{}, lipgloss.Style{}, m.styles))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(m.styles.StatusHint.Render("j/k move  enter/a switch  o/space open in az  r refresh  q close"))
	b.WriteString("\n")
	return b.String()
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
		snapshot, err := m.loader.ListTasksSnapshot(ctx)
		if err != nil {
			return LoadFailedMsg{Err: err}
		}
		return LoadedMsg{Snapshot: snapshot}
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
	projectDir := firstNonEmpty(entry.ProjectPath, entry.Worktree)
	if projectDir == "" && entry.Task.Session != nil {
		projectDir = strings.TrimSpace(entry.Task.Session.Worktree)
	}
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve cwd for full az session: %w", err)
		}
		projectDir = cwd
	}
	command := fmt.Sprintf("cd %s && az --open-issue %s", shellQuote(projectDir), shellQuote(issueID))
	exists, err := switcher.HasSession(ctx, defaultFullAzSession)
	if err != nil {
		return fmt.Errorf("check full az tmux session: %w", err)
	}
	if !exists {
		if err := switcher.NewSessionWithCommand(ctx, defaultFullAzSession, projectDir, command); err != nil {
			return fmt.Errorf("create full az tmux session: %w", err)
		}
	} else {
		if err := switcher.SendKey(ctx, defaultFullAzSession, "C-c"); err != nil {
			return fmt.Errorf("reset full az tmux pane: %w", err)
		}
		if err := switcher.SendKeys(ctx, defaultFullAzSession, command); err != nil {
			return fmt.Errorf("open issue in full az tmux session: %w", err)
		}
	}
	if err := switcher.SwitchClient(ctx, defaultFullAzSession); err != nil {
		return fmt.Errorf("switch to full az tmux session: %w", err)
	}
	return nil
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
	meta := ansi.Truncate("  "+strings.Join(metaParts, "  "), maxInt(1, width), "")
	return lipgloss.JoinVertical(lipgloss.Left, card, s.StatusInfo.Render(meta))
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

func formatSnapshotStatus(snapshot Snapshot, rows int) string {
	parts := []string{fmt.Sprintf("%d active sessions", rows)}
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

func shellQuote(value string) string {
	return strconv.Quote(value)
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
