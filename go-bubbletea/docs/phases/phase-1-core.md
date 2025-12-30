# Phase 1: Core Framework

**Goal**: Basic TEA loop with navigation

**Status**: 🔲 Not Started

**Estimated Effort**: 2-3 days

---

## Deliverables

- [ ] Project setup (go.mod, Makefile, .goreleaser.yaml)
- [ ] Main model struct with cursor, mode, basic state
- [ ] Basic keybinding handling (hjkl + arrows navigation)
- [ ] Half-page scroll (`Ctrl-Shift-d/u`)
- [ ] Force redraw (`Ctrl-l`)
- [ ] Static 4-column Kanban board rendering
- [ ] Lip Gloss theme (Catppuccin Macchiato)
- [ ] StatusBar with mode indicator + keybinding hints
- [ ] Quit (`q`, `Ctrl-c`)

---

## Acceptance Criteria

- [ ] `go build` produces working binary
- [ ] Navigate between columns with h/l
- [ ] Navigate within columns with j/k
- [ ] StatusBar shows current mode
- [ ] Half-page scroll works in tall columns

---

## UI Wireframe

```
┌─ Open ──────────┐┌─ In Progress ──┐┌─ Blocked ───────┐┌─ Done ──────────┐
│                 ││                ││                 ││                 │
│ ┌─────────────┐ ││ ┌─────────────┐││ ┌─────────────┐ ││ ┌─────────────┐ │
│ │ Task 1      │ ││ │ Task 3      │││ │ Task 5      │ ││ │ Task 7      │ │
│ │ P2 • Task   │ ││ │ P1 • Bug    │││ │ P0 • Task   │ ││ │ P3 • Task   │ │
│ └─────────────┘ ││ └─────────────┘││ └─────────────┘ ││ └─────────────┘ │
│                 ││                ││                 ││                 │
│ ┌─────────────┐ ││ ┌─────────────┐││                 ││ ┌─────────────┐ │
│ │▶Task 2      │ ││ │ Task 4      │││                 ││ │ Task 8      │ │
│ │ P1 • Feature│ ││ │ P2 • Epic   │││                 ││ │ P4 • Task   │ │
│ └─────────────┘ ││ └─────────────┘││                 ││ └─────────────┘ │
│                 ││                ││                 ││                 │
└─────────────────┘└────────────────┘└─────────────────┘└─────────────────┘
─────────────────────────────────────────────────────────────────────────────
 NORMAL │ h/l: columns  j/k: tasks  Space: action  ?: help  q: quit
```

---

## Dependencies

None - this is the foundation phase.

---

## Testing Strategy

| Type | Scope |
|------|-------|
| Unit | None (pure UI in this phase) |
| Golden | Board rendering snapshots |
| Manual | Navigation feel, responsiveness |

---

## Key Implementation Notes

### Entry Point

```go
// cmd/az/main.go
package main

import (
    "fmt"
    "os"

    "github.com/riordanpawley/azedarach/internal/app"
    "github.com/riordanpawley/azedarach/internal/config"
    tea "github.com/charmbracelet/bubbletea"
)

func main() {
    cfg, err := config.Load()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
        os.Exit(1)
    }

    model := app.New(cfg)
    p := tea.NewProgram(model, tea.WithAltScreen())

    if _, err := p.Run(); err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }
}
```

### Model Structure

```go
// internal/app/model.go
package app

import (
    "github.com/riordanpawley/azedarach/internal/config"
    "github.com/riordanpawley/azedarach/internal/ui/styles"
    tea "github.com/charmbracelet/bubbletea"
)

type Model struct {
    // Navigation
    cursor Cursor
    mode   Mode

    // Terminal dimensions
    width  int
    height int

    // Styling
    styles *styles.Styles

    // Configuration
    config *config.Config

    // Placeholder data for Phase 1
    columns []Column
}

type Cursor struct {
    Column int  // 0-3 (Open, In Progress, Blocked, Done)
    Task   int  // Index within column
}

type Mode int

const (
    ModeNormal Mode = iota
    ModeSelect
    ModeSearch
    ModeGoto
    ModeAction
)

func (m Mode) String() string {
    switch m {
    case ModeNormal:
        return "NORMAL"
    case ModeSelect:
        return "SELECT"
    case ModeSearch:
        return "SEARCH"
    case ModeGoto:
        return "GOTO"
    case ModeAction:
        return "ACTION"
    default:
        return "UNKNOWN"
    }
}

type Column struct {
    Title string
    Tasks []PlaceholderTask
}

type PlaceholderTask struct {
    ID       string
    Title    string
    Priority int
    Type     string
}

func New(cfg *config.Config) Model {
    return Model{
        cursor:  Cursor{Column: 0, Task: 0},
        mode:    ModeNormal,
        styles:  styles.New(),
        config:  cfg,
        columns: placeholderColumns(),
    }
}

func placeholderColumns() []Column {
    return []Column{
        {Title: "Open", Tasks: []PlaceholderTask{
            {ID: "az-1", Title: "Implement user auth", Priority: 2, Type: "Feature"},
            {ID: "az-2", Title: "Fix login bug", Priority: 1, Type: "Bug"},
        }},
        {Title: "In Progress", Tasks: []PlaceholderTask{
            {ID: "az-3", Title: "API refactor", Priority: 1, Type: "Task"},
        }},
        {Title: "Blocked", Tasks: []PlaceholderTask{}},
        {Title: "Done", Tasks: []PlaceholderTask{
            {ID: "az-4", Title: "Setup CI/CD", Priority: 3, Type: "Task"},
        }},
    }
}
```

### TEA Interface Implementation

```go
// internal/app/init.go
func (m Model) Init() tea.Cmd {
    return nil  // No initial commands in Phase 1
}

// internal/app/update.go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        return m.handleKey(msg)

    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        return m, nil
    }

    return m, nil
}

// internal/app/view.go
func (m Model) View() string {
    if m.width == 0 || m.height == 0 {
        return "Loading..."
    }

    // Render board
    board := m.renderBoard()

    // Render status bar
    statusBar := m.renderStatusBar()

    // Compose layout
    return lipgloss.JoinVertical(lipgloss.Left, board, statusBar)
}
```

### Keyboard Handling

```go
// internal/app/keybindings.go
package app

import tea "github.com/charmbracelet/bubbletea"

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    // Global keys (work in any mode)
    switch msg.String() {
    case "ctrl+c":
        return m, tea.Quit
    case "ctrl+l":
        // Force redraw
        return m, tea.ClearScreen
    }

    // Mode-specific handling
    switch m.mode {
    case ModeNormal:
        return m.handleNormalMode(msg)
    case ModeGoto:
        return m.handleGotoMode(msg)
    default:
        return m, nil
    }
}

func (m Model) handleNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch msg.String() {
    // Navigation
    case "h", "left":
        if m.cursor.Column > 0 {
            m.cursor.Column--
            m.cursor.Task = m.clampTaskIndex()
        }
    case "l", "right":
        if m.cursor.Column < len(m.columns)-1 {
            m.cursor.Column++
            m.cursor.Task = m.clampTaskIndex()
        }
    case "j", "down":
        col := m.columns[m.cursor.Column]
        if m.cursor.Task < len(col.Tasks)-1 {
            m.cursor.Task++
        }
    case "k", "up":
        if m.cursor.Task > 0 {
            m.cursor.Task--
        }

    // Half-page scroll
    case "ctrl+shift+d":
        m.cursor.Task = min(m.cursor.Task+m.halfPage(), len(m.currentColumn().Tasks)-1)
    case "ctrl+shift+u":
        m.cursor.Task = max(0, m.cursor.Task-m.halfPage())

    // Mode switches
    case "g":
        m.mode = ModeGoto
    case "q":
        return m, tea.Quit
    }

    return m, nil
}

func (m Model) handleGotoMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    defer func() { m.mode = ModeNormal }()

    switch msg.String() {
    case "g":
        m.cursor.Task = 0
    case "e":
        m.cursor.Task = len(m.currentColumn().Tasks) - 1
    case "h":
        m.cursor.Column = 0
        m.cursor.Task = m.clampTaskIndex()
    case "l":
        m.cursor.Column = len(m.columns) - 1
        m.cursor.Task = m.clampTaskIndex()
    case "esc":
        // Cancel goto mode
    }

    return m, nil
}

func (m Model) currentColumn() Column {
    return m.columns[m.cursor.Column]
}

func (m Model) clampTaskIndex() int {
    col := m.columns[m.cursor.Column]
    if len(col.Tasks) == 0 {
        return 0
    }
    return min(m.cursor.Task, len(col.Tasks)-1)
}

func (m Model) halfPage() int {
    visibleRows := (m.height - 3) / 4  // Approximate cards per column
    return max(1, visibleRows/2)
}
```

### Board Rendering

```go
// internal/ui/board/board.go
package board

import (
    "strings"

    "github.com/charmbracelet/lipgloss"
)

func Render(columns []Column, cursor Cursor, styles *Styles, width, height int) string {
    columnWidth := (width - 4) / 4  // 4 columns with gaps

    var cols []string
    for i, col := range columns {
        isActive := i == cursor.Column
        cols = append(cols, renderColumn(col, cursor.Task, isActive, columnWidth, height-2, styles))
    }

    return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

func renderColumn(col Column, cursorTask int, isActive bool, width, height int, styles *Styles) string {
    // Column header
    headerStyle := styles.ColumnHeader
    if isActive {
        headerStyle = styles.ColumnHeaderActive
    }
    header := headerStyle.Width(width).Render(col.Title)

    // Cards
    var cards []string
    for i, task := range col.Tasks {
        isCursor := isActive && i == cursorTask
        cards = append(cards, renderCard(task, isCursor, width-2, styles))
    }

    content := strings.Join(cards, "\n")

    // Column container
    columnStyle := styles.Column.Width(width).Height(height)
    return lipgloss.JoinVertical(lipgloss.Left, header, columnStyle.Render(content))
}

func renderCard(task PlaceholderTask, isCursor bool, width int, styles *Styles) string {
    cardStyle := styles.Card.Width(width)
    if isCursor {
        cardStyle = styles.CardActive.Width(width)
    }

    // Priority badge
    priorityBadge := styles.PriorityBadge(task.Priority).Render(fmt.Sprintf("P%d", task.Priority))

    // Type badge
    typeBadge := styles.TypeBadge.Render(task.Type[:1])  // First letter

    // Title (truncated)
    maxTitleLen := width - 10
    title := task.Title
    if len(title) > maxTitleLen {
        title = title[:maxTitleLen-1] + "…"
    }

    return cardStyle.Render(
        lipgloss.JoinVertical(lipgloss.Left,
            title,
            lipgloss.JoinHorizontal(lipgloss.Left, priorityBadge, " ", typeBadge),
        ),
    )
}
```

### Catppuccin Macchiato Theme

```go
// internal/ui/styles/theme.go
package styles

import "github.com/charmbracelet/lipgloss"

// Catppuccin Macchiato palette
var (
    Base     = lipgloss.Color("#24273a")
    Mantle   = lipgloss.Color("#1e2030")
    Crust    = lipgloss.Color("#181926")
    Surface0 = lipgloss.Color("#363a4f")
    Surface1 = lipgloss.Color("#494d64")
    Surface2 = lipgloss.Color("#5b6078")
    Overlay0 = lipgloss.Color("#6e738d")
    Overlay1 = lipgloss.Color("#8087a2")
    Overlay2 = lipgloss.Color("#939ab7")
    Subtext0 = lipgloss.Color("#a5adcb")
    Subtext1 = lipgloss.Color("#b8c0e0")
    Text     = lipgloss.Color("#cad3f5")

    // Accent colors
    Rosewater = lipgloss.Color("#f4dbd6")
    Flamingo  = lipgloss.Color("#f0c6c6")
    Pink      = lipgloss.Color("#f5bde6")
    Mauve     = lipgloss.Color("#c6a0f6")
    Red       = lipgloss.Color("#ed8796")
    Maroon    = lipgloss.Color("#ee99a0")
    Peach     = lipgloss.Color("#f5a97f")
    Yellow    = lipgloss.Color("#eed49f")
    Green     = lipgloss.Color("#a6da95")
    Teal      = lipgloss.Color("#8bd5ca")
    Sky       = lipgloss.Color("#91d7e3")
    Sapphire  = lipgloss.Color("#7dc4e4")
    Blue      = lipgloss.Color("#8aadf4")
    Lavender  = lipgloss.Color("#b7bdf8")
)

// Priority colors
var PriorityColors = []lipgloss.Color{
    Red,    // P0 - Critical
    Peach,  // P1 - High
    Yellow, // P2 - Medium
    Green,  // P3 - Low
    Overlay0, // P4 - Backlog
}

// Status colors
var StatusColors = map[string]lipgloss.Color{
    "open":        Blue,
    "in_progress": Yellow,
    "blocked":     Red,
    "done":        Green,
}
```

### Styles Definition

```go
// internal/ui/styles/styles.go
package styles

import "github.com/charmbracelet/lipgloss"

type Styles struct {
    // Board
    Column             lipgloss.Style
    ColumnHeader       lipgloss.Style
    ColumnHeaderActive lipgloss.Style

    // Cards
    Card       lipgloss.Style
    CardActive lipgloss.Style

    // Status bar
    StatusBar     lipgloss.Style
    StatusMode    lipgloss.Style
    StatusHint    lipgloss.Style

    // Badges
    TypeBadge lipgloss.Style
}

func New() *Styles {
    return &Styles{
        Column: lipgloss.NewStyle().
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(Surface1).
            Padding(0, 1),

        ColumnHeader: lipgloss.NewStyle().
            Foreground(Subtext0).
            Bold(true).
            Padding(0, 1),

        ColumnHeaderActive: lipgloss.NewStyle().
            Foreground(Blue).
            Bold(true).
            Padding(0, 1),

        Card: lipgloss.NewStyle().
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(Surface1).
            Padding(0, 1).
            MarginBottom(1),

        CardActive: lipgloss.NewStyle().
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(Blue).
            Padding(0, 1).
            MarginBottom(1),

        StatusBar: lipgloss.NewStyle().
            Background(Surface0).
            Foreground(Subtext0).
            Padding(0, 1),

        StatusMode: lipgloss.NewStyle().
            Background(Blue).
            Foreground(Base).
            Bold(true).
            Padding(0, 1),

        StatusHint: lipgloss.NewStyle().
            Foreground(Overlay1),

        TypeBadge: lipgloss.NewStyle().
            Foreground(Overlay0).
            Background(Surface1).
            Padding(0, 1),
    }
}

func (s *Styles) PriorityBadge(priority int) lipgloss.Style {
    color := PriorityColors[min(priority, len(PriorityColors)-1)]
    return lipgloss.NewStyle().
        Foreground(Base).
        Background(color).
        Padding(0, 1)
}
```

### StatusBar Rendering

```go
// internal/ui/statusbar.go
func (m Model) renderStatusBar() string {
    // Mode indicator
    mode := m.styles.StatusMode.Render(m.mode.String())

    // Context hints based on mode
    hints := m.getHints()
    hintsStr := m.styles.StatusHint.Render(hints)

    // Combine
    left := lipgloss.JoinHorizontal(lipgloss.Left, mode, "  ", hintsStr)

    return m.styles.StatusBar.Width(m.width).Render(left)
}

func (m Model) getHints() string {
    switch m.mode {
    case ModeNormal:
        return "h/l: columns  j/k: tasks  Space: action  ?: help  q: quit"
    case ModeGoto:
        return "g: top  e: end  h: first col  l: last col  Esc: cancel"
    default:
        return ""
    }
}
```

---

## Files to Create

```
cmd/az/main.go                    # Entry point
internal/app/model.go             # Model struct
internal/app/init.go              # Init() implementation
internal/app/update.go            # Update() message router
internal/app/view.go              # View() rendering
internal/app/keybindings.go       # Key handling
internal/app/helpers.go           # Utility methods
internal/config/config.go         # Configuration (stub)
internal/ui/board/board.go        # Board rendering
internal/ui/board/column.go       # Column rendering
internal/ui/board/card.go         # Card rendering
internal/ui/statusbar/statusbar.go
internal/ui/styles/theme.go       # Catppuccin colors
internal/ui/styles/styles.go      # Component styles
go.mod
go.sum
Makefile
.goreleaser.yaml
```

---

## Makefile

```makefile
.PHONY: build run test clean

build:
	go build -o bin/az ./cmd/az

run:
	go run ./cmd/az

test:
	go test ./...

test-golden:
	go test ./... -update

clean:
	rm -rf bin/

install:
	go install ./cmd/az
```

---

## Definition of Done

- [ ] All deliverables implemented
- [ ] All acceptance criteria pass
- [ ] Golden tests created for board rendering
- [ ] Manual testing confirms smooth navigation
- [ ] Code reviewed and merged
- [ ] README updated with build instructions
