# Azedarach Go/Bubbletea Rewrite Plan

> Alternative rewrite exploring Go + Bubbletea as TUI framework

## Executive Summary

This document outlines a potential rewrite of Azedarach in Go using the [Bubbletea](https://github.com/charmbracelet/bubbletea) TUI framework. Like the existing Gleam rewrite, Bubbletea uses The Elm Architecture (TEA), making the conceptual port straightforward.

## Why Go/Bubbletea?

### Advantages

| Aspect | Benefit |
|--------|---------|
| **Single Binary** | Go compiles to a single static binary - no runtime dependencies |
| **Cross-Platform** | Native Windows support (unlike BEAM/Erlang) |
| **Performance** | Fast startup, low memory footprint |
| **Ecosystem** | Charmbracelet ecosystem is mature & production-tested (9,300+ projects use Bubbletea) |
| **Distribution** | Easy to distribute via `go install`, Homebrew, etc. |
| **Same Architecture** | TEA model matches Gleam/Shore - 1:1 conceptual mapping |
| **Familiar** | Go is widely known; easier to find contributors |

### Trade-offs vs Gleam

| Aspect | Gleam/OTP | Go/Bubbletea |
|--------|-----------|--------------|
| Concurrency Model | OTP actors (preemptive) | Goroutines (cooperative) |
| Fault Tolerance | Supervision trees | Manual error handling |
| Hot Code Reload | Erlang VM supports it | Not available |
| Type System | Strong, functional | Strong, structural |
| Pattern Matching | Native | Switch statements |
| Immutability | Default | Manual discipline |

### When Go Makes Sense

1. **Distribution priority** - Need easy installation across platforms
2. **Team familiarity** - Go more common than Gleam
3. **Windows users** - Erlang setup on Windows is painful
4. **Binary size** - Go binaries smaller than BEAM releases

## Architecture Mapping

### The Elm Architecture (TEA)

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

### Gleam → Go Mapping

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

### Component Mapping

| Gleam/Shore | Go/Bubbles |
|-------------|------------|
| `TextField` | `textinput.Model` |
| Scrollable view | `viewport.Model` |
| List rendering | `list.Model` |
| Spinner | `spinner.Model` |
| Progress bar | `progress.Model` |
| Custom colors | `lipgloss.Style` |
| Key bindings | `key.Binding` / `help.Model` |

## Project Structure

```
go-bubbletea/
├── cmd/
│   └── azedarach/
│       └── main.go           # Entry point
├── internal/
│   ├── app/
│   │   ├── model.go          # Main TEA model
│   │   ├── update.go         # Message handlers
│   │   ├── view.go           # Rendering
│   │   └── keybindings.go    # Key mappings
│   ├── ui/
│   │   ├── board.go          # Kanban board component
│   │   ├── card.go           # Task card component
│   │   ├── statusbar.go      # Status bar
│   │   ├── overlay/
│   │   │   ├── action.go     # Action menu
│   │   │   ├── filter.go     # Filter menu
│   │   │   ├── help.go       # Help overlay
│   │   │   └── detail.go     # Detail panel
│   │   └── styles.go         # Lip Gloss styles
│   ├── domain/
│   │   ├── task.go           # Task/Bead types
│   │   ├── session.go        # Session state
│   │   └── project.go        # Project types
│   ├── services/
│   │   ├── beads/
│   │   │   └── client.go     # bd CLI wrapper
│   │   ├── tmux/
│   │   │   └── client.go     # tmux operations
│   │   ├── git/
│   │   │   └── client.go     # git/worktree operations
│   │   └── monitor/
│   │       └── session.go    # Session state polling
│   └── config/
│       └── config.go         # Configuration loading
├── go.mod
├── go.sum
├── Makefile
└── PLAN.md
```

## Implementation Phases

### Phase 1: Core Framework (Week 1)

**Goal**: Basic TEA loop with navigation

- [ ] Project setup (go.mod, dependencies)
- [ ] Main model struct with cursor, mode, basic state
- [ ] Basic keybinding handling (hjkl navigation)
- [ ] Static 4-column Kanban board rendering
- [ ] Lip Gloss theme (Catppuccin Macchiato)

**Files**:
- `cmd/azedarach/main.go`
- `internal/app/model.go`
- `internal/app/update.go`
- `internal/app/view.go`
- `internal/ui/board.go`
- `internal/ui/styles.go`

### Phase 2: Beads Integration (Week 2)

**Goal**: Load and display real bead data

- [ ] Domain types (Task, Session, Project)
- [ ] Beads CLI client (list, search, ready)
- [ ] Async loading with `tea.Cmd`
- [ ] Task cards with status/priority/type
- [ ] Periodic refresh (tea.Tick)
- [ ] Toast notifications

**Files**:
- `internal/domain/*.go`
- `internal/services/beads/client.go`
- `internal/ui/card.go`

### Phase 3: Overlays & Filters (Week 3)

**Goal**: Modal overlays and filtering

- [ ] Overlay stack system
- [ ] Action menu (Space)
- [ ] Filter menu (f) with sub-menus
- [ ] Sort menu (,)
- [ ] Help overlay (?)
- [ ] Search input (/)
- [ ] Status/priority/type/session filters

**Files**:
- `internal/ui/overlay/*.go`
- `internal/app/keybindings.go`

### Phase 4: Session Management (Week 4)

**Goal**: Spawn and manage Claude sessions

- [ ] tmux client (create, attach, send keys, capture)
- [ ] Worktree management (create, delete)
- [ ] Session state detection (polling + pattern matching)
- [ ] Start/stop/pause/resume session
- [ ] Session monitor goroutine
- [ ] Dev server management

**Files**:
- `internal/services/tmux/client.go`
- `internal/services/git/worktree.go`
- `internal/services/monitor/session.go`

### Phase 5: Git Operations (Week 5)

**Goal**: Git workflow support

- [ ] Git client (merge, diff, branch operations)
- [ ] Update from main / merge to main
- [ ] PR creation (gh CLI)
- [ ] Conflict detection and resolution workflow
- [ ] Merge choice dialog

**Files**:
- `internal/services/git/client.go`
- `internal/ui/overlay/merge.go`

### Phase 6: Advanced Features (Week 6)

**Goal**: Full feature parity

- [ ] Epic drill-down view
- [ ] Image attachments (clipboard, file)
- [ ] Multi-project support
- [ ] Detail panel with editing
- [ ] Settings overlay
- [ ] Diagnostics view
- [ ] Planning workflow integration

## Code Examples

### Basic Model Structure

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

### Async Command Pattern

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

### Lip Gloss Styling

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

## Testing Strategy

1. **Unit tests**: Domain logic, filters, sorting
2. **Integration tests**: Beads/tmux/git client with mocks
3. **Snapshot tests**: View rendering with `golden` files
4. **Manual testing**: Full TUI interaction

## Build & Distribution

```makefile
# Makefile
.PHONY: build install release

build:
	go build -o bin/azedarach ./cmd/azedarach

install:
	go install ./cmd/azedarach

release:
	goreleaser release --clean

# Cross-compile
build-all:
	GOOS=darwin GOARCH=amd64 go build -o bin/azedarach-darwin-amd64 ./cmd/azedarach
	GOOS=darwin GOARCH=arm64 go build -o bin/azedarach-darwin-arm64 ./cmd/azedarach
	GOOS=linux GOARCH=amd64 go build -o bin/azedarach-linux-amd64 ./cmd/azedarach
	GOOS=windows GOARCH=amd64 go build -o bin/azedarach-windows-amd64.exe ./cmd/azedarach
```

## Comparison: All Three Implementations

| Aspect | TypeScript | Gleam | Go |
|--------|------------|-------|-----|
| **Lines of Code** | ~33,000 | ~16,500 | ~8,000 (est.) |
| **Architecture** | Effect services | OTP actors | Goroutines |
| **UI Framework** | React + OpenTUI | Shore (TEA) | Bubbletea (TEA) |
| **State** | SubscriptionRef + Atoms | TEA Model | TEA Model |
| **Concurrency** | Effect fibers | OTP processes | Goroutines |
| **Binary Size** | N/A (Node.js) | ~30MB (BEAM) | ~10MB |
| **Startup Time** | ~500ms | ~200ms | ~50ms |
| **Windows** | Yes (Node) | Difficult | Yes (native) |
| **Distribution** | npm | escript/release | go install |

## Next Steps

1. Review this plan and decide if Go rewrite should proceed
2. If yes, create initial project skeleton
3. Begin Phase 1 implementation
4. Maintain both Gleam and Go rewrites in parallel for comparison

## Resources

- [Bubbletea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles) - TUI components
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) - Terminal styling
- [Charm tutorials](https://charm.sh/blog/) - Framework guides
- [gogh-themes](https://github.com/willyv3/gogh-themes/lipgloss) - Theme colors
