# Phase 3: Overlays & Filters

**Goal**: Modal overlays and filtering

**Status**: 🔲 Not Started

**Estimated Effort**: 4-5 days

---

## Deliverables

### Overlay System
- [ ] Overlay stack system (push/pop)
- [ ] Action menu (`Space`) with available actions
- [ ] Help overlay (`?`)

### Filter Menu (`f`)
- [ ] Status filter (`f` `s`): o/i/b/d toggles
- [ ] Priority filter (`f` `p`): 0-4 toggles
- [ ] Type filter (`f` `t`): B/F/T/E/C toggles
- [ ] Session filter (`f` `S`): I/U/W/D/X/P toggles
- [ ] Hide epic children (`f` `e`)
- [ ] Age filter (`f` `1/7/3/0`)
- [ ] Clear all (`f` `c`)

### Sort Menu (`,`)
- [ ] Sort by session (`,` `s`)
- [ ] Sort by priority (`,` `p`)
- [ ] Sort by updated (`,` `u`)
- [ ] Toggle direction (repeat key)

### Search & Selection
- [ ] Search input (`/`) with live filtering
- [ ] Select mode (`v`) with visual highlighting
- [ ] Select all (`%`) and clear (`A`)

### Navigation
- [ ] Goto mode (`g`):
  - Column top (`g` `g`)
  - Column bottom (`g` `e`)
  - First/last column (`g` `h`/`l`)

### Views
- [ ] Compact/list view toggle (`Tab`)
- [ ] Move task left/right (`Space` `h/l`)
- [ ] StatusBar selection count

---

## Acceptance Criteria

- [ ] All overlays render centered with proper styling
- [ ] Filter combinations work (AND between types, OR within)
- [ ] Search is case-insensitive, matches title + ID
- [ ] Selected tasks visually distinct
- [ ] Compact view shows all tasks in priority order

---

## UI Wireframes

### Action Menu

```
                    ┌─ Actions ──────────────────────┐
                    │                                │
                    │  s  Start session              │
                    │  S  Start + work               │
                    │  a  Attach to session          │
                    │  ─────────────────────────     │
                    │  u  Update from main           │
                    │  m  Merge to main              │
                    │  P  Create PR                  │
                    │  ─────────────────────────     │
                    │  h  Move left                  │
                    │  l  Move right                 │
                    │  e  Edit issue                  │
                    │  d  Delete/cleanup             │
                    │                                │
                    │            Esc to close        │
                    └────────────────────────────────┘
```

### Filter Menu

```
                    ┌─ Filter ───────────────────────┐
                    │                                │
                    │  s  Status  →   [o] [i] [b] d  │
                    │  p  Priority →  [0] 1  2  3  4 │
                    │  t  Type    →   T  B  [F] E  C │
                    │  S  Session →   I  U  W  D  X  │
                    │  ─────────────────────────     │
                    │  e  Hide epic children   [ ]   │
                    │  ─────────────────────────     │
                    │  1  Last 24 hours              │
                    │  7  Last 7 days                │
                    │  3  Last 30 days               │
                    │  0  All time            [●]    │
                    │  ─────────────────────────     │
                    │  c  Clear all filters          │
                    │                                │
                    └────────────────────────────────┘
```

### Search Mode

```
┌─ Open (3) ──────┐┌─ In Progress ───┐┌─ Blocked ───────┐┌─ Done ──────────┐
│                 ││                 ││                 ││                 │
│ (dimmed cards   ││ (dimmed cards   ││ (dimmed cards   ││ (dimmed cards   │
│  that don't     ││  that don't     ││  that don't     ││  that don't     │
│  match)         ││  match)         ││  match)         ││  match)         │
│                 ││                 ││                 ││                 │
│ ┌─────────────┐ ││                 ││                 ││ ┌─────────────┐ │
│ │▶az-12       │ ││                 ││                 ││ │ az-45       │ │
│ │ auth login  │ ││                 ││                 ││ │ login fix   │ │
│ │ (MATCH)     │ ││                 ││                 ││ │ (MATCH)     │ │
│ └─────────────┘ ││                 ││                 ││ └─────────────┘ │
└─────────────────┘└─────────────────┘└─────────────────┘└─────────────────┘
/ login█                                               2 matches │ Enter: go
```

### Compact View

```
┌──────────────────────────────────────────────────────────────────────────┐
│  #   │ ID      │ Title                          │ Status │ Pri │ Session │
├──────┼─────────┼────────────────────────────────┼────────┼─────┼─────────┤
│  1   │ az-15   │ API refactor                   │ prog   │ P0  │ ● 2h    │
│  2   │ az-12   │ Add authentication flow        │ open   │ P1  │ ●       │
│▶ 3   │ az-18   │ Database migration script      │ block  │ P1  │ ⏸       │
│  4   │ az-13   │ Fix login redirect bug         │ open   │ P0  │         │
│  5   │ az-16   │ Epic: UI Redesign              │ prog   │ P2  │         │
│  6   │ az-1    │ Setup CI/CD pipeline           │ done   │ P3  │ ✓       │
│  7   │ az-2    │ Update documentation           │ done   │ P4  │         │
└──────┴─────────┴────────────────────────────────┴────────┴─────┴─────────┘
 LIST │ 7 issues │ Sorted by: Priority ↓ │ Tab: Kanban view
```

---

## Dependencies

- [Phase 2: Issue Tracker Integration](phase-2-tracker-integration.md)

---

## Testing Strategy

| Type | Scope |
|------|-------|
| Unit | Filter logic, sort comparisons |
| Golden | All overlay renderings |
| Manual | Mode transitions feel snappy |

---

## Key Implementation Notes

### Overlay Interface

```go
// internal/ui/overlay/overlay.go
package overlay

import tea "github.com/charmbracelet/bubbletea"

// Overlay represents a modal UI element
type Overlay interface {
    tea.Model

    // Title returns the overlay title for the header
    Title() string

    // Size returns preferred width and height (0 = auto)
    Size() (width, height int)
}

// CloseOverlayMsg signals the parent to pop this overlay
type CloseOverlayMsg struct{}

// SelectionMsg carries the selected value back to parent
type SelectionMsg struct {
    Key   string
    Value any
}
```

### Overlay Stack

```go
// internal/ui/overlay/stack.go
package overlay

import tea "github.com/charmbracelet/bubbletea"

type Stack struct {
    overlays []Overlay
}

func NewStack() *Stack {
    return &Stack{
        overlays: make([]Overlay, 0),
    }
}

func (s *Stack) Push(o Overlay) tea.Cmd {
    s.overlays = append(s.overlays, o)
    return o.Init()
}

func (s *Stack) Pop() Overlay {
    if len(s.overlays) == 0 {
        return nil
    }
    o := s.overlays[len(s.overlays)-1]
    s.overlays = s.overlays[:len(s.overlays)-1]
    return o
}

func (s *Stack) Current() Overlay {
    if len(s.overlays) == 0 {
        return nil
    }
    return s.overlays[len(s.overlays)-1]
}

func (s *Stack) IsEmpty() bool {
    return len(s.overlays) == 0
}

func (s *Stack) Clear() {
    s.overlays = s.overlays[:0]
}

// Update routes messages to current overlay
func (s *Stack) Update(msg tea.Msg) tea.Cmd {
    if s.IsEmpty() {
        return nil
    }

    current := s.Current()
    newOverlay, cmd := current.Update(msg)
    s.overlays[len(s.overlays)-1] = newOverlay.(Overlay)

    return cmd
}
```

### Action Menu

```go
// internal/ui/overlay/action.go
package overlay

import (
    "github.com/charmbracelet/lipgloss"
    tea "github.com/charmbracelet/bubbletea"
)

type Action struct {
    Key      string
    Label    string
    Enabled  bool
    Shortcut string
}

type ActionMenu struct {
    actions []Action
    cursor  int
    styles  *Styles
}

func NewActionMenu(task domain.Task, session *domain.Session) *ActionMenu {
    actions := buildActions(task, session)
    return &ActionMenu{
        actions: actions,
        cursor:  0,
        styles:  NewStyles(),
    }
}

func buildActions(task domain.Task, session *domain.Session) []Action {
    hasSession := session != nil && session.State != domain.SessionIdle

    return []Action{
        // Session actions
        {Key: "s", Label: "Start session", Enabled: !hasSession},
        {Key: "S", Label: "Start + work", Enabled: !hasSession},
        {Key: "!", Label: "Start yolo", Enabled: !hasSession},
        {Key: "a", Label: "Attach to session", Enabled: hasSession},
        {Key: "p", Label: "Pause session", Enabled: hasSession && session.State == domain.SessionBusy},
        {Key: "R", Label: "Resume session", Enabled: hasSession && session.State == domain.SessionPaused},
        {Key: "x", Label: "Stop session", Enabled: hasSession},
        {Key: "", Label: "─────────────────", Enabled: false},  // Separator
        // Git actions
        {Key: "u", Label: "Update from main", Enabled: true},
        {Key: "m", Label: "Merge to main", Enabled: hasSession},
        {Key: "P", Label: "Create PR", Enabled: hasSession},
        {Key: "f", Label: "Show diff", Enabled: hasSession},
        {Key: "", Label: "─────────────────", Enabled: false},
        // Task actions
        {Key: "h", Label: "Move left", Enabled: task.Status != domain.StatusOpen},
        {Key: "l", Label: "Move right", Enabled: task.Status != domain.StatusDone},
        {Key: "e", Label: "Edit issue", Enabled: true},
        {Key: "d", Label: "Delete/cleanup", Enabled: true},
    }
}

func (m *ActionMenu) Title() string {
    return "Actions"
}

func (m *ActionMenu) Size() (int, int) {
    return 36, len(m.actions) + 4
}

func (m *ActionMenu) Init() tea.Cmd {
    return nil
}

func (m *ActionMenu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        switch msg.String() {
        case "esc", "q":
            return m, func() tea.Msg { return CloseOverlayMsg{} }

        case "j", "down":
            m.cursor = (m.cursor + 1) % len(m.actions)
            // Skip separators
            for !m.actions[m.cursor].Enabled && m.actions[m.cursor].Key == "" {
                m.cursor = (m.cursor + 1) % len(m.actions)
            }

        case "k", "up":
            m.cursor = (m.cursor - 1 + len(m.actions)) % len(m.actions)
            for !m.actions[m.cursor].Enabled && m.actions[m.cursor].Key == "" {
                m.cursor = (m.cursor - 1 + len(m.actions)) % len(m.actions)
            }

        case "enter":
            if m.actions[m.cursor].Enabled {
                return m, func() tea.Msg {
                    return SelectionMsg{Key: m.actions[m.cursor].Key}
                }
            }

        default:
            // Direct key selection
            for _, a := range m.actions {
                if a.Key == msg.String() && a.Enabled {
                    return m, func() tea.Msg {
                        return SelectionMsg{Key: a.Key}
                    }
                }
            }
        }
    }
    return m, nil
}

func (m *ActionMenu) View() string {
    var rows []string

    for i, a := range m.actions {
        if a.Key == "" {
            // Separator
            rows = append(rows, m.styles.Separator.Render(a.Label))
            continue
        }

        style := m.styles.MenuItem
        if !a.Enabled {
            style = m.styles.MenuItemDisabled
        }
        if i == m.cursor {
            style = m.styles.MenuItemActive
        }

        keyStyle := m.styles.MenuKey
        if !a.Enabled {
            keyStyle = m.styles.MenuKeyDisabled
        }

        row := lipgloss.JoinHorizontal(lipgloss.Left,
            keyStyle.Render(a.Key),
            "  ",
            style.Render(a.Label),
        )
        rows = append(rows, row)
    }

    content := lipgloss.JoinVertical(lipgloss.Left, rows...)
    footer := m.styles.Footer.Render("Esc to close")

    return lipgloss.JoinVertical(lipgloss.Left, content, "", footer)
}
```

### Filter State

```go
// internal/domain/filter.go
package domain

import (
    "strings"
    "time"
)

type Filter struct {
    // Status filter (OR within)
    Status map[Status]bool

    // Priority filter (OR within)
    Priority map[Priority]bool

    // Type filter (OR within)
    Type map[TaskType]bool

    // Session state filter (OR within)
    SessionState map[SessionState]bool

    // Epic children visibility
    HideEpicChildren bool

    // Age filter
    AgeMaxDays *int  // nil = no filter

    // Search query
    SearchQuery string
}

func NewFilter() *Filter {
    return &Filter{
        Status:       make(map[Status]bool),
        Priority:     make(map[Priority]bool),
        Type:         make(map[TaskType]bool),
        SessionState: make(map[SessionState]bool),
    }
}

func (f *Filter) IsActive() bool {
    return len(f.Status) > 0 ||
        len(f.Priority) > 0 ||
        len(f.Type) > 0 ||
        len(f.SessionState) > 0 ||
        f.HideEpicChildren ||
        f.AgeMaxDays != nil ||
        f.SearchQuery != ""
}

func (f *Filter) Apply(tasks []Task) []Task {
    if !f.IsActive() {
        return tasks
    }

    var result []Task
    for _, t := range tasks {
        if f.Matches(t) {
            result = append(result, t)
        }
    }
    return result
}

func (f *Filter) Matches(t Task) bool {
    // Status filter (if any set, must match one)
    if len(f.Status) > 0 && !f.Status[t.Status] {
        return false
    }

    // Priority filter
    if len(f.Priority) > 0 && !f.Priority[t.Priority] {
        return false
    }

    // Type filter
    if len(f.Type) > 0 && !f.Type[t.Type] {
        return false
    }

    // Session state filter
    if len(f.SessionState) > 0 {
        state := SessionIdle
        if t.Session != nil {
            state = t.Session.State
        }
        if !f.SessionState[state] {
            return false
        }
    }

    // Hide epic children
    if f.HideEpicChildren && t.ParentID != nil {
        return false
    }

    // Age filter
    if f.AgeMaxDays != nil {
        maxAge := time.Now().AddDate(0, 0, -*f.AgeMaxDays)
        if t.UpdatedAt.Before(maxAge) {
            return false
        }
    }

    // Search query
    if f.SearchQuery != "" {
        query := strings.ToLower(f.SearchQuery)
        title := strings.ToLower(t.Title)
        id := strings.ToLower(t.ID)
        if !strings.Contains(title, query) && !strings.Contains(id, query) {
            return false
        }
    }

    return true
}

func (f *Filter) Clear() {
    f.Status = make(map[Status]bool)
    f.Priority = make(map[Priority]bool)
    f.Type = make(map[TaskType]bool)
    f.SessionState = make(map[SessionState]bool)
    f.HideEpicChildren = false
    f.AgeMaxDays = nil
    f.SearchQuery = ""
}

func (f *Filter) ToggleStatus(s Status) {
    if f.Status[s] {
        delete(f.Status, s)
    } else {
        f.Status[s] = true
    }
}

// Similar toggle methods for Priority, Type, SessionState...
```

### Sort State

```go
// internal/domain/sort.go
package domain

import "sort"

type SortField string

const (
    SortBySession  SortField = "session"
    SortByPriority SortField = "priority"
    SortByUpdated  SortField = "updated"
)

type SortOrder int

const (
    SortAsc SortOrder = iota
    SortDesc
)

type Sort struct {
    Field SortField
    Order SortOrder
}

func (s *Sort) Toggle(field SortField) {
    if s.Field == field {
        // Toggle direction
        if s.Order == SortAsc {
            s.Order = SortDesc
        } else {
            s.Order = SortAsc
        }
    } else {
        s.Field = field
        s.Order = SortDesc  // Default to descending
    }
}

func (s *Sort) Apply(tasks []Task) []Task {
    result := make([]Task, len(tasks))
    copy(result, tasks)

    sort.Slice(result, func(i, j int) bool {
        cmp := s.compare(result[i], result[j])
        if s.Order == SortDesc {
            return cmp > 0
        }
        return cmp < 0
    })

    return result
}

func (s *Sort) compare(a, b Task) int {
    switch s.Field {
    case SortBySession:
        return s.compareSession(a, b)
    case SortByPriority:
        return int(a.Priority) - int(b.Priority)
    case SortByUpdated:
        return a.UpdatedAt.Compare(b.UpdatedAt)
    default:
        return 0
    }
}

func (s *Sort) compareSession(a, b Task) int {
    aState := getSessionPriority(a.Session)
    bState := getSessionPriority(b.Session)
    return aState - bState
}

func getSessionPriority(session *Session) int {
    if session == nil {
        return 100  // No session = lowest priority
    }
    switch session.State {
    case SessionWaiting:
        return 0  // Needs attention
    case SessionBusy:
        return 1
    case SessionPaused:
        return 2
    case SessionError:
        return 3
    default:
        return 50
    }
}
```

### Search Input

```go
// internal/ui/overlay/search.go
package overlay

import (
    "github.com/charmbracelet/bubbles/textinput"
    tea "github.com/charmbracelet/bubbletea"
)

type SearchMsg struct {
    Query string
}

type SearchOverlay struct {
    input      textinput.Model
    matchCount int
}

func NewSearchOverlay() *SearchOverlay {
    ti := textinput.New()
    ti.Placeholder = "Search issues..."
    ti.Prompt = "/ "
    ti.Focus()

    return &SearchOverlay{
        input: ti,
    }
}

func (m *SearchOverlay) Title() string {
    return ""  // No title for search bar
}

func (m *SearchOverlay) Size() (int, int) {
    return 0, 1  // Full width, 1 line
}

func (m *SearchOverlay) SetMatchCount(count int) {
    m.matchCount = count
}

func (m *SearchOverlay) Init() tea.Cmd {
    return textinput.Blink
}

func (m *SearchOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        switch msg.String() {
        case "esc":
            return m, func() tea.Msg {
                return CloseOverlayMsg{}
            }
        case "enter":
            // Close search but keep filter
            return m, func() tea.Msg {
                return CloseOverlayMsg{}
            }
        }
    }

    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)

    // Emit search message on every change
    return m, tea.Batch(cmd, func() tea.Msg {
        return SearchMsg{Query: m.input.Value()}
    })
}

func (m *SearchOverlay) View() string {
    countStr := ""
    if m.matchCount > 0 {
        countStr = fmt.Sprintf(" │ %d matches", m.matchCount)
    }
    return m.input.View() + countStr + " │ Enter: go  Esc: cancel"
}
```

### Compact List View

```go
// internal/ui/compact/list.go
package compact

import (
    "fmt"
    "strings"

    "github.com/charmbracelet/lipgloss"
    "github.com/riordanpawley/azedarach/internal/domain"
)

type ListView struct {
    tasks    []domain.Task
    cursor   int
    selected map[string]bool
    styles   *Styles
    width    int
    height   int
}

func (v *ListView) Render() string {
    // Header
    header := v.renderHeader()

    // Rows
    var rows []string
    for i, task := range v.tasks {
        rows = append(rows, v.renderRow(i, task))
    }

    // Join with newlines
    content := lipgloss.JoinVertical(lipgloss.Left,
        append([]string{header, v.styles.Separator.Render(strings.Repeat("─", v.width))},
            rows...)...,
    )

    return content
}

func (v *ListView) renderHeader() string {
    cols := []string{
        v.styles.HeaderCell.Width(4).Render("#"),
        v.styles.HeaderCell.Width(8).Render("ID"),
        v.styles.HeaderCell.Width(32).Render("Title"),
        v.styles.HeaderCell.Width(8).Render("Status"),
        v.styles.HeaderCell.Width(4).Render("Pri"),
        v.styles.HeaderCell.Width(8).Render("Session"),
    }
    return lipgloss.JoinHorizontal(lipgloss.Left, cols...)
}

func (v *ListView) renderRow(index int, task domain.Task) string {
    isCursor := index == v.cursor
    isSelected := v.selected[task.ID]

    style := v.styles.Row
    if isCursor {
        style = v.styles.RowActive
    }
    if isSelected {
        style = v.styles.RowSelected
    }

    // Cursor indicator
    cursor := "  "
    if isCursor {
        cursor = "▶ "
    }

    // Selection indicator
    selection := " "
    if isSelected {
        selection = "●"
    }

    // Session icon
    sessionIcon := " "
    if task.Session != nil {
        sessionIcon = task.Session.State.Icon()
    }

    // Truncate title
    title := task.Title
    if len(title) > 30 {
        title = title[:27] + "..."
    }

    cols := []string{
        style.Width(4).Render(fmt.Sprintf("%s%s", cursor, selection)),
        style.Width(8).Render(task.ID),
        style.Width(32).Render(title),
        style.Width(8).Render(string(task.Status)[:4]),
        style.Width(4).Render(fmt.Sprintf("P%d", task.Priority)),
        style.Width(8).Render(sessionIcon),
    }

    return lipgloss.JoinHorizontal(lipgloss.Left, cols...)
}
```

---

## Files to Create

```
internal/ui/overlay/overlay.go    # Overlay interface
internal/ui/overlay/stack.go      # Overlay stack
internal/ui/overlay/styles.go     # Overlay styles
internal/ui/overlay/action.go     # Action menu
internal/ui/overlay/filter.go     # Filter menu
internal/ui/overlay/sort.go       # Sort menu
internal/ui/overlay/search.go     # Search input
internal/ui/overlay/help.go       # Help overlay
internal/ui/compact/list.go       # Compact list view
internal/ui/compact/styles.go     # List styles
internal/domain/filter.go         # Filter state and logic
internal/domain/sort.go           # Sort state and logic
internal/app/update_overlay.go    # Overlay message handling
```

---

## Definition of Done

- [ ] All deliverables implemented
- [ ] All acceptance criteria pass
- [ ] Unit tests for filter logic
- [ ] Unit tests for sort comparisons
- [ ] Golden tests for all overlays
- [ ] Manual testing confirms snappy transitions
- [ ] Code reviewed and merged
