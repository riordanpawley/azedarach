# Phase 2: Beads Integration

**Goal**: Load and display real bead data

**Status**: 🔲 Not Started

**Estimated Effort**: 3-4 days

---

## Deliverables

- [ ] Domain types (Task, Session, DevServer, Project)
- [ ] Beads CLI client (list, search, ready, create, update, close)
- [ ] JSON parsing with proper error handling
- [ ] Async loading with `tea.Cmd`
- [ ] Task cards with status/priority/type badges
- [ ] Elapsed timer on cards (session duration)
- [ ] Periodic refresh (tea.Tick every 2s)
- [ ] Toast notifications (info, success, warning, error)
- [ ] Loading spinner during initial load
- [ ] Connection status indicator placeholder

---

## Acceptance Criteria

- [ ] Board shows real beads from `bd list`
- [ ] Cards show correct status colors
- [ ] Priority badges (P0-P4) visible
- [ ] Toasts appear and auto-dismiss
- [ ] Periodic refresh doesn't flicker

---

## UI Wireframe

```
┌─ Open (3) ──────┐┌─ In Progress (2)┐┌─ Blocked (1) ──┐┌─ Done (5) ──────┐
│                 ││                 ││                ││                 │
│ ┌─────────────┐ ││ ┌─────────────┐ ││ ┌────────────┐ ││ ┌─────────────┐ │
│ │ az-12       │ ││ │▶az-15       │ ││ │ az-18      │ ││ │ az-1        │ │
│ │ Add auth    │ ││ │ API refactor│ ││ │ DB migrate │ ││ │ Setup CI    │ │
│ │ P1 • Feature│ ││ │ P0 • Task   │ ││ │ P1 • Task  │ ││ │ P3 • Task   │ │
│ │ 🟢 Active   │ ││ │ 🟡 2h 34m   │ ││ │ ⏸️ Paused  │ ││ │ ✓ Complete  │ │
│ └─────────────┘ ││ └─────────────┘ ││ └────────────┘ ││ └─────────────┘ │
│                 ││                 ││                ││                 │
│ ┌─────────────┐ ││ ┌─────────────┐ ││                ││ ┌─────────────┐ │
│ │ az-13       │ ││ │ az-16       │ ││                ││ │ az-2        │ │
│ │ Fix login   │ ││ │ Epic: UI    │ ││                ││ │ Docs update │ │
│ │ P0 • Bug 🔴 │ ││ │ P2 • Epic   │ ││                ││ │ P4 • Task   │ │
│ └─────────────┘ ││ │ [3/5] ████░░│ ││                ││ └─────────────┘ │
└─────────────────┘└─────────────────┘└────────────────┘└─────────────────┘
────────────────────────────────────────────────────────────────────────────
 NORMAL │ 11 beads │ ● Online │ ⟳ 2s ago          ╔═════════════════════╗
                                                   ║ ✓ Beads loaded (11) ║
                                                   ╚═════════════════════╝
```

---

## Dependencies

- [Phase 1: Core Framework](phase-1-core.md)

---

## Testing Strategy

| Type | Scope |
|------|-------|
| Unit | Beads client parsing, JSON unmarshaling |
| Golden | Card rendering with various states |
| Integration | Mock `bd` CLI responses |

---

## Key Implementation Notes

### Domain Types

```go
// internal/domain/task.go
package domain

import "time"

type Task struct {
    ID          string     `json:"id"`
    Title       string     `json:"title"`
    Description string     `json:"description"`
    Status      Status     `json:"status"`
    Priority    Priority   `json:"priority"`
    Type        TaskType   `json:"type"`
    ParentID    *string    `json:"parent_id"`
    Session     *Session   `json:"session"`
    CreatedAt   time.Time  `json:"created_at"`
    UpdatedAt   time.Time  `json:"updated_at"`
}

type Status string

const (
    StatusOpen       Status = "open"
    StatusInProgress Status = "in_progress"
    StatusBlocked    Status = "blocked"
    StatusDone       Status = "done"
)

func (s Status) Column() int {
    switch s {
    case StatusOpen:
        return 0
    case StatusInProgress:
        return 1
    case StatusBlocked:
        return 2
    case StatusDone:
        return 3
    default:
        return 0
    }
}

type Priority int

const (
    P0 Priority = iota // Critical
    P1                 // High
    P2                 // Medium
    P3                 // Low
    P4                 // Backlog
)

type TaskType string

const (
    TypeTask    TaskType = "task"
    TypeBug     TaskType = "bug"
    TypeFeature TaskType = "feature"
    TypeEpic    TaskType = "epic"
    TypeChore   TaskType = "chore"
)

func (t TaskType) Short() string {
    switch t {
    case TypeTask:
        return "T"
    case TypeBug:
        return "B"
    case TypeFeature:
        return "F"
    case TypeEpic:
        return "E"
    case TypeChore:
        return "C"
    default:
        return "?"
    }
}
```

### Session State

```go
// internal/domain/session.go
package domain

import "time"

type Session struct {
    BeadID    string       `json:"bead_id"`
    State     SessionState `json:"state"`
    StartedAt *time.Time   `json:"started_at"`
    Worktree  string       `json:"worktree"`
    DevServer *DevServer   `json:"dev_server"`
}

type SessionState string

const (
    SessionIdle    SessionState = "idle"
    SessionBusy    SessionState = "busy"
    SessionWaiting SessionState = "waiting"
    SessionDone    SessionState = "done"
    SessionError   SessionState = "error"
    SessionPaused  SessionState = "paused"
)

func (s SessionState) Icon() string {
    switch s {
    case SessionIdle:
        return "○"
    case SessionBusy:
        return "●"
    case SessionWaiting:
        return "◐"
    case SessionDone:
        return "✓"
    case SessionError:
        return "✗"
    case SessionPaused:
        return "⏸"
    default:
        return "?"
    }
}

type DevServer struct {
    Port    int    `json:"port"`
    Command string `json:"command"`
    Running bool   `json:"running"`
}
```

### Beads Client with Dependency Injection

```go
// internal/services/beads/client.go
package beads

import (
    "context"
    "encoding/json"
    "log/slog"

    "github.com/riordanpawley/azedarach/internal/domain"
)

// CommandRunner abstracts command execution for testing
type CommandRunner interface {
    Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type Client struct {
    runner CommandRunner
    logger *slog.Logger
}

func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
    return &Client{
        runner: runner,
        logger: logger,
    }
}

func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
    c.logger.Debug("fetching beads list")

    out, err := c.runner.Run(ctx, "bd", "list", "--format=json")
    if err != nil {
        return nil, &domain.BeadsError{Op: "list", Err: err}
    }

    var tasks []domain.Task
    if err := json.Unmarshal(out, &tasks); err != nil {
        return nil, &domain.BeadsError{Op: "list", Message: "failed to parse JSON", Err: err}
    }

    c.logger.Debug("fetched beads", "count", len(tasks))
    return tasks, nil
}

func (c *Client) Search(ctx context.Context, query string) ([]domain.Task, error) {
    out, err := c.runner.Run(ctx, "bd", "search", query, "--format=json")
    if err != nil {
        return nil, &domain.BeadsError{Op: "search", Message: query, Err: err}
    }

    var tasks []domain.Task
    if err := json.Unmarshal(out, &tasks); err != nil {
        return nil, &domain.BeadsError{Op: "search", Err: err}
    }

    return tasks, nil
}

func (c *Client) Ready(ctx context.Context) ([]domain.Task, error) {
    out, err := c.runner.Run(ctx, "bd", "ready", "--format=json")
    if err != nil {
        return nil, &domain.BeadsError{Op: "ready", Err: err}
    }

    var tasks []domain.Task
    if err := json.Unmarshal(out, &tasks); err != nil {
        return nil, &domain.BeadsError{Op: "ready", Err: err}
    }

    return tasks, nil
}

func (c *Client) Update(ctx context.Context, id string, status domain.Status) error {
    _, err := c.runner.Run(ctx, "bd", "update", id, "--status="+string(status))
    if err != nil {
        return &domain.BeadsError{Op: "update", BeadID: id, Err: err}
    }
    return nil
}

func (c *Client) Close(ctx context.Context, id string, reason string) error {
    args := []string{"close", id}
    if reason != "" {
        args = append(args, "--reason="+reason)
    }
    _, err := c.runner.Run(ctx, "bd", args...)
    if err != nil {
        return &domain.BeadsError{Op: "close", BeadID: id, Err: err}
    }
    return nil
}
```

### Real Command Runner

```go
// internal/services/beads/runner.go
package beads

import (
    "context"
    "os/exec"
)

type ExecRunner struct{}

func (r *ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
    cmd := exec.CommandContext(ctx, name, args...)
    return cmd.Output()
}
```

### Async Commands for TEA

```go
// internal/app/commands.go
package app

import (
    "context"
    "time"

    "github.com/riordanpawley/azedarach/internal/domain"
    tea "github.com/charmbracelet/bubbletea"
)

// Messages
type beadsLoadedMsg struct {
    tasks []domain.Task
}

type beadsErrorMsg struct {
    err error
}

type tickMsg time.Time

// Commands
func (m Model) loadBeadsCmd() tea.Cmd {
    return func() tea.Msg {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()

        tasks, err := m.beadsClient.List(ctx)
        if err != nil {
            return beadsErrorMsg{err}
        }
        return beadsLoadedMsg{tasks}
    }
}

func tickCmd(d time.Duration) tea.Cmd {
    return tea.Tick(d, func(t time.Time) tea.Msg {
        return tickMsg(t)
    })
}

// Update handler
func (m Model) handleBeadsLoaded(msg beadsLoadedMsg) (Model, tea.Cmd) {
    m.tasks = msg.tasks
    m.loading = false
    m.lastRefresh = time.Now()

    // Group tasks by status into columns
    m.columns = m.groupTasksByStatus(msg.tasks)

    // Clamp cursor if needed
    m.cursor.Task = m.clampTaskIndex()

    return m, nil
}

func (m Model) handleBeadsError(msg beadsErrorMsg) (Model, tea.Cmd) {
    m.loading = false

    // Add error toast
    m.addToast(Toast{
        Level:     ToastError,
        Message:   formatError(msg.err),
        ExpiresAt: time.Now().Add(8 * time.Second),
    })

    return m, nil
}

func (m Model) handleTick(msg tickMsg) (Model, tea.Cmd) {
    // Expire old toasts
    m.expireToasts()

    // Trigger refresh
    return m, tea.Batch(
        m.loadBeadsCmd(),
        tickCmd(2 * time.Second),
    )
}
```

### Toast System

```go
// internal/ui/toast.go
package ui

import (
    "time"

    "github.com/charmbracelet/lipgloss"
)

type ToastLevel int

const (
    ToastInfo ToastLevel = iota
    ToastSuccess
    ToastWarning
    ToastError
)

type Toast struct {
    Level     ToastLevel
    Message   string
    ExpiresAt time.Time
}

func (t Toast) IsExpired() bool {
    return time.Now().After(t.ExpiresAt)
}

type ToastRenderer struct {
    styles *Styles
}

func (r *ToastRenderer) Render(toasts []Toast, width int) string {
    if len(toasts) == 0 {
        return ""
    }

    var rendered []string
    for _, t := range toasts {
        style := r.styleForLevel(t.Level)
        rendered = append(rendered, style.Width(width/3).Render(t.Message))
    }

    // Stack from bottom-right
    container := lipgloss.NewStyle().
        Position(lipgloss.Right).
        MarginRight(2).
        MarginBottom(1)

    return container.Render(lipgloss.JoinVertical(lipgloss.Right, rendered...))
}

func (r *ToastRenderer) styleForLevel(level ToastLevel) lipgloss.Style {
    base := lipgloss.NewStyle().
        Padding(0, 1).
        BorderStyle(lipgloss.RoundedBorder())

    switch level {
    case ToastSuccess:
        return base.BorderForeground(Green).Foreground(Green)
    case ToastWarning:
        return base.BorderForeground(Yellow).Foreground(Yellow)
    case ToastError:
        return base.BorderForeground(Red).Foreground(Red)
    default:
        return base.BorderForeground(Blue).Foreground(Blue)
    }
}
```

### Card with Session Info

```go
// internal/ui/board/card.go
package board

import (
    "fmt"
    "time"

    "github.com/charmbracelet/lipgloss"
    "github.com/riordanpawley/azedarach/internal/domain"
)

func renderCard(task domain.Task, isCursor bool, width int, styles *Styles) string {
    cardStyle := styles.Card.Width(width)
    if isCursor {
        cardStyle = styles.CardActive.Width(width)
    }

    // ID
    id := styles.TaskID.Render(task.ID)

    // Title (truncated)
    maxTitleLen := width - 4
    title := task.Title
    if len(title) > maxTitleLen {
        title = title[:maxTitleLen-1] + "…"
    }

    // Badges row
    priorityBadge := styles.PriorityBadge(int(task.Priority)).Render(fmt.Sprintf("P%d", task.Priority))
    typeBadge := styles.TypeBadge.Render(task.Type.Short())

    badges := lipgloss.JoinHorizontal(lipgloss.Left, priorityBadge, " ", typeBadge)

    // Session status row (if session exists)
    var sessionRow string
    if task.Session != nil {
        sessionRow = renderSessionStatus(task.Session, styles)
    }

    // Epic progress (if epic type)
    var epicProgress string
    if task.Type == domain.TypeEpic {
        epicProgress = renderEpicProgress(task, styles)
    }

    // Compose card content
    content := lipgloss.JoinVertical(lipgloss.Left,
        id,
        title,
        badges,
    )

    if sessionRow != "" {
        content = lipgloss.JoinVertical(lipgloss.Left, content, sessionRow)
    }
    if epicProgress != "" {
        content = lipgloss.JoinVertical(lipgloss.Left, content, epicProgress)
    }

    return cardStyle.Render(content)
}

func renderSessionStatus(session *domain.Session, styles *Styles) string {
    icon := session.State.Icon()

    // Elapsed time if active
    var elapsed string
    if session.StartedAt != nil && session.State == domain.SessionBusy {
        d := time.Since(*session.StartedAt)
        elapsed = formatDuration(d)
    }

    stateStyle := styles.SessionState(session.State)
    if elapsed != "" {
        return stateStyle.Render(fmt.Sprintf("%s %s", icon, elapsed))
    }
    return stateStyle.Render(icon)
}

func formatDuration(d time.Duration) string {
    h := int(d.Hours())
    m := int(d.Minutes()) % 60

    if h > 0 {
        return fmt.Sprintf("%dh %dm", h, m)
    }
    return fmt.Sprintf("%dm", m)
}

func renderEpicProgress(task domain.Task, styles *Styles) string {
    // TODO: Get child counts from task metadata
    completed := 3
    total := 5

    percent := float64(completed) / float64(total)
    filled := int(percent * 6)
    empty := 6 - filled

    bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
    return styles.EpicProgress.Render(fmt.Sprintf("[%d/%d] %s", completed, total, bar))
}
```

### Loading Spinner

```go
// internal/app/model.go (additions)

import "github.com/charmbracelet/bubbles/spinner"

type Model struct {
    // ... existing fields

    loading bool
    spinner spinner.Model
}

func New(cfg *config.Config) Model {
    s := spinner.New()
    s.Spinner = spinner.Dot
    s.Style = lipgloss.NewStyle().Foreground(Blue)

    return Model{
        // ... existing init
        loading: true,
        spinner: s,
    }
}

func (m Model) Init() tea.Cmd {
    return tea.Batch(
        m.spinner.Tick,
        m.loadBeadsCmd(),
        tickCmd(2 * time.Second),
    )
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case spinner.TickMsg:
        var cmd tea.Cmd
        m.spinner, cmd = m.spinner.Update(msg)
        return m, cmd

    // ... other cases
    }
}

func (m Model) View() string {
    if m.loading {
        return m.renderLoading()
    }
    // ... normal rendering
}

func (m Model) renderLoading() string {
    return lipgloss.Place(m.width, m.height,
        lipgloss.Center, lipgloss.Center,
        lipgloss.JoinVertical(lipgloss.Center,
            m.spinner.View(),
            "Loading beads...",
        ),
    )
}
```

---

## Files to Create

```
internal/domain/task.go           # Task, Status, Priority, Type
internal/domain/session.go        # Session, SessionState, DevServer
internal/domain/project.go        # Project type
internal/domain/errors.go         # BeadsError, TmuxError, etc.
internal/services/beads/client.go # Beads CLI wrapper
internal/services/beads/runner.go # Real command runner
internal/services/beads/parser.go # JSON parsing helpers
internal/app/commands.go          # tea.Cmd functions
internal/app/messages.go          # Message types
internal/ui/board/card.go         # Enhanced card rendering
internal/ui/toast.go              # Toast system
```

---

## Error Types

```go
// internal/domain/errors.go
package domain

import "fmt"

type BeadsError struct {
    Op      string // Operation: "list", "create", "update", etc.
    BeadID  string // Optional: specific bead ID
    Message string // Human-readable context
    Err     error  // Underlying error
}

func (e *BeadsError) Error() string {
    if e.BeadID != "" {
        return fmt.Sprintf("beads %s [%s]: %s", e.Op, e.BeadID, e.Message)
    }
    if e.Message != "" {
        return fmt.Sprintf("beads %s: %s", e.Op, e.Message)
    }
    if e.Err != nil {
        return fmt.Sprintf("beads %s: %v", e.Op, e.Err)
    }
    return fmt.Sprintf("beads %s failed", e.Op)
}

func (e *BeadsError) Unwrap() error {
    return e.Err
}
```

---

## Unit Tests

```go
// internal/services/beads/client_test.go
package beads

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

type mockRunner struct {
    output []byte
    err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
    return m.output, m.err
}

func TestClient_List(t *testing.T) {
    tests := []struct {
        name       string
        output     string
        wantCount  int
        wantErr    bool
    }{
        {
            name: "valid response",
            output: `[
                {"id": "az-1", "title": "Task 1", "status": "open", "priority": 1, "type": "task"},
                {"id": "az-2", "title": "Task 2", "status": "in_progress", "priority": 0, "type": "bug"}
            ]`,
            wantCount: 2,
        },
        {
            name:      "empty response",
            output:    `[]`,
            wantCount: 0,
        },
        {
            name:    "invalid json",
            output:  `not json`,
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            runner := &mockRunner{output: []byte(tt.output)}
            client := NewClient(runner, slog.Default())

            tasks, err := client.List(context.Background())

            if tt.wantErr {
                require.Error(t, err)
                return
            }

            require.NoError(t, err)
            assert.Len(t, tasks, tt.wantCount)
        })
    }
}
```

---

## Definition of Done

- [ ] All deliverables implemented
- [ ] All acceptance criteria pass
- [ ] Unit tests for beads client parsing
- [ ] Golden tests for card rendering
- [ ] Integration tests with mock CLI
- [ ] Error handling covers all edge cases
- [ ] Code reviewed and merged
