# Architecture Mapping

## The Elm Architecture (TEA)

Both Gleam/Shore and Go/Bubbletea use TEA:

```
┌─────────────────────────────────────────────────────────────┐
│                    TEA Pattern (Same in Both)                │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│    Model (State) ──────────────────────────────────────┐    │
│         ▲                                               │    │
│         │ Update                                        │    │
│         │ (msg Msg, model Model) -> (Model, Cmd)       │    │
│         │                                               │    │
│    Messages ◄────────────────────────────────────────  │    │
│         ▲                                     View      │    │
│         │                                     (model)   │    │
│         │                                        │      │    │
│    User Input / Commands                         ▼      │    │
│                                              Terminal    │    │
│                                                         │    │
└─────────────────────────────────────────────────────────────┘
```

## Gleam → Go Mapping

| Gleam Concept | Go/Bubbletea Equivalent |
|---------------|-------------------------|
| `Model` type | `model` struct |
| `Msg` union type | `tea.Msg` interface + concrete types |
| `update(model, msg)` | `func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd)` |
| `view(model)` | `func (m model) View() string` |
| `init()` | `func (m model) Init() tea.Cmd` |
| OTP Actor | Goroutine + channels |
| Subject | `chan T` |
| `process.spawn` | `go func()` |
| Result type | `(T, error)` tuple |
| Option type | Pointer `*T` or custom `Option[T]` |
| Pattern matching | Type switch / if-else |

## Component Mapping

| Gleam/Shore | Go/Bubbles |
|-------------|------------|
| `TextField` | `textinput.Model` |
| Scrollable view | `viewport.Model` |
| List rendering | `list.Model` |
| Spinner | `spinner.Model` |
| Progress bar | `progress.Model` |
| Custom colors | `lipgloss.Style` |
| Key bindings | `key.Binding` / `help.Model` |

## Basic Model Structure

```go
package app

import (
    "github.com/charmbracelet/bubbles/textinput"
    tea "github.com/charmbracelet/bubbletea"
    "github.com/charmbracelet/lipgloss"
)

type Mode int

const (
    ModeNormal Mode = iota
    ModeSelect
    ModeSearch
    ModeGoto
)

type Cursor struct {
    Column int
    Task   int
}

type Model struct {
    // Core data
    tasks    []Task
    sessions map[string]SessionState

    // Navigation
    cursor Cursor
    mode   Mode

    // UI state
    overlay     Overlay
    searchInput textinput.Model

    // Filters
    statusFilter   map[Status]bool
    priorityFilter map[Priority]bool

    // Config
    config Config
    styles Styles

    // Terminal
    width  int
    height int
}

func (m Model) Init() tea.Cmd {
    return tea.Batch(
        loadBeads,
        tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
            return tickMsg(t)
        }),
    )
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        return m.handleKey(msg)
    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        return m, nil
    case beadsLoadedMsg:
        m.tasks = msg.tasks
        return m, nil
    case tickMsg:
        return m, tea.Batch(
            loadBeads,
            tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
                return tickMsg(t)
            }),
        )
    }
    return m, nil
}

func (m Model) View() string {
    // Render board
    board := m.renderBoard()

    // Render status bar
    statusBar := m.renderStatusBar()

    // Compose layout
    return lipgloss.JoinVertical(
        lipgloss.Left,
        board,
        statusBar,
    )
}
```

## Async Command Pattern

```go
// Commands return tea.Cmd functions
func loadBeads() tea.Msg {
    tasks, err := beads.ListAll()
    if err != nil {
        return beadsErrorMsg{err}
    }
    return beadsLoadedMsg{tasks}
}

// Messages for async results
type beadsLoadedMsg struct {
    tasks []Task
}

type beadsErrorMsg struct {
    err error
}

// In Update, handle results
case beadsLoadedMsg:
    m.tasks = msg.tasks
    return m, nil
case beadsErrorMsg:
    m.toast = Toast{Level: Error, Message: msg.err.Error()}
    return m, nil
```

## Lip Gloss Styling

```go
package ui

import "github.com/charmbracelet/lipgloss"

// Catppuccin Macchiato colors
var (
    base      = lipgloss.Color("#24273a")
    surface0  = lipgloss.Color("#363a4f")
    blue      = lipgloss.Color("#8aadf4")
    green     = lipgloss.Color("#a6da95")
    yellow    = lipgloss.Color("#eed49f")
    red       = lipgloss.Color("#ed8796")
    text      = lipgloss.Color("#cad3f5")
    subtext0  = lipgloss.Color("#a5adcb")
)

type Styles struct {
    Board      lipgloss.Style
    Column     lipgloss.Style
    Card       lipgloss.Style
    CardActive lipgloss.Style
    StatusBar  lipgloss.Style
}

func NewStyles() Styles {
    return Styles{
        Board: lipgloss.NewStyle().
            Background(base),
        Column: lipgloss.NewStyle().
            Width(25).
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(surface0),
        Card: lipgloss.NewStyle().
            Padding(0, 1).
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(surface0),
        CardActive: lipgloss.NewStyle().
            Padding(0, 1).
            BorderStyle(lipgloss.RoundedBorder()).
            BorderForeground(blue),
        StatusBar: lipgloss.NewStyle().
            Background(surface0).
            Foreground(subtext0).
            Padding(0, 1),
    }
}
```

## Dependencies

```go
// go.mod
module github.com/yourusername/azedarach

go 1.22

require (
    github.com/charmbracelet/bubbletea v1.2.4
    github.com/charmbracelet/bubbles v0.20.0
    github.com/charmbracelet/lipgloss v1.0.0
    github.com/charmbracelet/x/term v0.1.1
)
```
