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

## Complete Feature Matrix (TypeScript → Go)

This matrix ensures no features are lost in the rewrite. Checked items are covered in phases above.

### Navigation & Modes

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| hjkl navigation | `h/j/k/l` | ✅ Covered | 1 |
| Arrow key alternatives | `←↓↑→` | ✅ Covered | 1 |
| Half-page scroll | `Ctrl-Shift-d/u` | ⚠️ Missing | 1 |
| Normal mode | default | ✅ Covered | 1 |
| Select mode | `v` | ⚠️ Missing | 3 |
| Select all | `%` | ⚠️ Missing | 3 |
| Clear selections | `A` | ⚠️ Missing | 3 |
| Search mode | `/` | ✅ Covered | 3 |
| Goto mode | `g` | ⚠️ Missing | 3 |
| Jump labels | `g` `w` | ⚠️ Missing | 6 |
| Goto column top | `g` `g` | ⚠️ Missing | 3 |
| Goto column bottom | `g` `e` | ⚠️ Missing | 3 |
| Goto first/last column | `g` `h`/`l` | ⚠️ Missing | 3 |
| Project selector | `g` `p` | ⚠️ Missing | 6 |
| Action mode | `Space` | ✅ Covered | 3 |
| Merge select mode | `Space` `b` | ⚠️ Missing | 5 |

### View Modes

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Kanban view (default) | - | ✅ Covered | 1 |
| Compact/list view | `Tab` | ⚠️ Missing | 3 |
| Epic drill-down | `Enter` on epic | ⚠️ Partial | 6 |
| Epic progress bar | - | ⚠️ Missing | 6 |
| Force redraw | `Ctrl-l` | ⚠️ Missing | 1 |

### Session Management

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Start session | `Space` `s` | ✅ Covered | 4 |
| Start + work | `Space` `S` | ⚠️ Missing | 4 |
| Start yolo (skip perms) | `Space` `!` | ⚠️ Missing | 4 |
| Chat with Haiku | `Space` `c` | ⚠️ Missing | 6 |
| Attach to session | `Space` `a` | ✅ Covered | 4 |
| Pause session | `Space` `p` | ✅ Covered | 4 |
| Resume session | `Space` `R` | ✅ Covered | 4 |
| Stop session | `Space` `x` | ✅ Covered | 4 |
| Session state detection | - | ✅ Covered | 4 |
| Elapsed timer on cards | - | ⚠️ Missing | 2 |

### Dev Server

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Toggle dev server | `Space` `r` | ✅ Covered | 4 |
| View dev server | `Space` `v` | ⚠️ Missing | 4 |
| Restart dev server | `Space` `Ctrl+r` | ⚠️ Missing | 4 |
| Port allocation | - | ⚠️ Missing | 4 |
| Port conflict resolution | - | ⚠️ Missing | 4 |
| StatusBar port indicator | - | ⚠️ Missing | 4 |

### Git Operations

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Update from main | `Space` `u` | ✅ Covered | 5 |
| Merge to main | `Space` `m` | ✅ Covered | 5 |
| Create PR | `Space` `P` | ✅ Covered | 5 |
| Show diff (difftastic) | `Space` `f` | ⚠️ Missing | 5 |
| Abort merge | `Space` `M` | ⚠️ Missing | 5 |
| Merge bead into... | `Space` `b` | ⚠️ Missing | 5 |
| Delete worktree/cleanup | `Space` `d` | ⚠️ Missing | 4 |
| Refresh git stats | `r` | ⚠️ Missing | 5 |
| Conflict detection | - | ✅ Covered | 5 |
| Conflict resolution flow | - | ✅ Covered | 5 |

### Editor/Create Actions

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Open Helix editor | `Space` `H` | ⚠️ Missing | 6 |
| Manual edit bead | `Space` `e` | ⚠️ Missing | 6 |
| Claude edit bead | `Space` `E` | ⚠️ Missing | 6 |
| Manual create bead | `c` | ⚠️ Missing | 6 |
| Claude create bead | `C` | ⚠️ Missing | 6 |
| Move task left/right | `Space` `h`/`l` | ⚠️ Missing | 3 |

### Filters

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Filter menu | `f` | ✅ Covered | 3 |
| Status filter (sub-menu) | `f` `s` | ✅ Covered | 3 |
| Priority filter (sub-menu) | `f` `p` | ✅ Covered | 3 |
| Type filter (sub-menu) | `f` `t` | ✅ Covered | 3 |
| Session filter (sub-menu) | `f` `S` | ✅ Covered | 3 |
| Hide epic children toggle | `f` `e` | ⚠️ Missing | 3 |
| Age filter | `f` `1/7/3/0` | ⚠️ Missing | 3 |
| Clear all filters | `f` `c` | ⚠️ Missing | 3 |

### Sort

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Sort menu | `,` | ✅ Covered | 3 |
| Sort by session | `,` `s` | ⚠️ Missing | 3 |
| Sort by priority | `,` `p` | ⚠️ Missing | 3 |
| Sort by updated | `,` `u` | ⚠️ Missing | 3 |
| Toggle direction | repeat key | ⚠️ Missing | 3 |

### Overlays

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Action menu | `Space` | ✅ Covered | 3 |
| Filter menu | `f` | ✅ Covered | 3 |
| Sort menu | `,` | ✅ Covered | 3 |
| Help overlay | `?` | ✅ Covered | 3 |
| Detail panel | `Enter` | ✅ Covered | 6 |
| Settings overlay | `s` | ✅ Covered | 6 |
| Diagnostics overlay | `d` | ⚠️ Missing | 6 |
| Logs viewer | `L` | ⚠️ Missing | 6 |
| Planning overlay | `p` | ⚠️ Partial | 6 |
| Merge choice dialog | - | ✅ Covered | 5 |
| Confirm dialog | - | ⚠️ Missing | 4 |
| Bulk cleanup dialog | - | ⚠️ Missing | 4 |
| Project selector | `g` `p` | ⚠️ Missing | 6 |
| Claude create prompt | `C` | ⚠️ Missing | 6 |
| Dev server menu | - | ⚠️ Missing | 4 |

### Image Attachments

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Image attach overlay | `Space` `i` | ⚠️ Partial | 6 |
| Paste from clipboard | `p`/`v` | ⚠️ Partial | 6 |
| Attach from file | `f` | ⚠️ Partial | 6 |
| Preview in terminal | `v` in detail | ⚠️ Missing | 6 |
| Open in external viewer | `o` | ⚠️ Missing | 6 |
| Delete attachment | `x` | ⚠️ Missing | 6 |
| Navigate attachments | `j`/`k` in detail | ⚠️ Missing | 6 |

### Bulk Operations

| Feature | Key | Status | Phase |
|---------|-----|--------|-------|
| Select all | `%` | ⚠️ Missing | 3 |
| Bulk stop sessions | selections + `Space` `x` | ⚠️ Missing | 4 |
| Bulk cleanup | selections + `Space` `d` | ⚠️ Missing | 4 |
| Bulk move | selections + `Space` `h/l` | ⚠️ Missing | 3 |
| Cleanup choice (worktrees/full) | - | ⚠️ Missing | 4 |

### Network/Offline

| Feature | Status | Phase |
|---------|--------|-------|
| Network status detection | ⚠️ Missing | 5 |
| Offline mode | ⚠️ Missing | 5 |
| Graceful degradation | ⚠️ Missing | 5 |
| Connection indicator | ⚠️ Missing | 2 |

### Settings (all via `s` overlay)

| Setting | Status | Phase |
|---------|--------|-------|
| CLI Tool (claude/opencode) | ⚠️ Missing | 6 |
| Skip Permissions | ⚠️ Missing | 6 |
| Push on Create | ⚠️ Missing | 6 |
| Git Push/Fetch enabled | ⚠️ Missing | 6 |
| Line Changes in diff | ⚠️ Missing | 6 |
| PR Enabled/Auto Draft/Auto Merge | ⚠️ Missing | 6 |
| Bell/System Notifications | ⚠️ Missing | 6 |
| Auto Detect Network | ⚠️ Missing | 6 |
| Beads Sync | ⚠️ Missing | 6 |
| Pattern Matching state detection | ⚠️ Missing | 6 |

### tmux Integration

| Feature | Status | Phase |
|---------|--------|-------|
| Return to az (Ctrl-a Ctrl-a) | ⚠️ Missing | 4 |
| Toggle Claude/Dev (Ctrl-a Tab) | ⚠️ Missing | 4 |
| Register global tmux bindings | ⚠️ Missing | 4 |

### Multi-Project

| Feature | Status | Phase |
|---------|--------|-------|
| Project selector overlay | ⚠️ Missing | 6 |
| Project auto-detection | ⚠️ Missing | 6 |
| Global projects.json | ⚠️ Missing | 6 |
| CLI: az project add/list/remove/switch | ⚠️ Missing | 6 |

### Misc

| Feature | Status | Phase |
|---------|--------|-------|
| Toast notifications | ✅ Covered | 2 |
| StatusBar mode indicator | ⚠️ Missing | 1 |
| StatusBar keybinding hints | ⚠️ Missing | 1 |
| StatusBar selection count | ⚠️ Missing | 3 |
| StatusBar connection status | ⚠️ Missing | 5 |

---

## Gap Summary

**Total Features: ~100**
- ✅ Covered: ~35 (35%)
- ⚠️ Missing/Partial: ~65 (65%)

### Critical Missing (must have for v1.0)

1. **Select mode & bulk operations** - Core workflow
2. **Goto mode with jump labels** - Fast navigation
3. **Compact view toggle** - Essential for large boards
4. **Start+work & yolo modes** - Common session patterns
5. **Delete/cleanup workflow** - Resource management
6. **Diff viewer** - Code review before merge
7. **Manual/Claude create & edit** - Bead management
8. **Port allocation for dev servers** - Multi-session support
9. **Confirm dialogs** - Destructive action safety
10. **Move tasks left/right** - Status transitions

### Nice to Have (v1.5+)

- Logs viewer
- Diagnostics overlay
- System notifications
- Network status detection
- Image preview in terminal
- Helix editor integration
- Chat with Haiku mode

---

## Updated Implementation Phases

### Phase 1: Core Framework ✅ (mostly covered)

Add:
- [ ] Half-page scroll (`Ctrl-Shift-d/u`)
- [ ] Force redraw (`Ctrl-l`)
- [ ] StatusBar mode indicator + keybinding hints

### Phase 2: Beads Integration ✅ (mostly covered)

Add:
- [ ] Elapsed timer on task cards (session duration)
- [ ] Connection status indicator in StatusBar

### Phase 3: Overlays & Filters (needs expansion)

Add:
- [ ] Select mode (`v`) with visual highlighting
- [ ] Select all (`%`) and clear (`A`)
- [ ] Goto mode (`g`) with `gg`, `ge`, `gh`, `gl`
- [ ] Compact/list view toggle (`Tab`)
- [ ] Move task left/right (`Space` `h/l`)
- [ ] Hide epic children toggle (`f` `e`)
- [ ] Age filter (`f` `1/7/3/0`)
- [ ] Clear all filters (`f` `c`)
- [ ] Sort directions with toggle
- [ ] StatusBar selection count

### Phase 4: Session Management (needs expansion)

Add:
- [ ] Start+work mode (`Space` `S`)
- [ ] Start yolo mode (`Space` `!`)
- [ ] View dev server (`Space` `v`)
- [ ] Restart dev server (`Space` `Ctrl+r`)
- [ ] Port allocation with conflict resolution
- [ ] StatusBar dev server port indicator
- [ ] Delete worktree/cleanup (`Space` `d`)
- [ ] Confirm dialog for destructive actions
- [ ] Bulk cleanup dialog (worktrees vs full)
- [ ] Bulk stop sessions
- [ ] Register tmux global bindings (Ctrl-a Ctrl-a, Ctrl-a Tab)
- [ ] Dev server menu overlay

### Phase 5: Git Operations (needs expansion)

Add:
- [ ] Show diff with difftastic (`Space` `f`)
- [ ] Abort merge (`Space` `M`)
- [ ] Merge bead into... (`Space` `b`) with merge select mode
- [ ] Refresh git stats (`r`)
- [ ] Network status detection
- [ ] Offline mode / graceful degradation
- [ ] Connection status in StatusBar

### Phase 6: Advanced Features (needs expansion)

Add:
- [ ] Jump labels (`g` `w`)
- [ ] Project selector overlay (`g` `p`)
- [ ] Project auto-detection from cwd
- [ ] CLI: az project add/list/remove/switch
- [ ] Manual create bead (`c`) via $EDITOR
- [ ] Claude create bead (`C`) with prompt overlay
- [ ] Manual edit bead (`Space` `e`)
- [ ] Claude edit bead (`Space` `E`)
- [ ] Open Helix editor (`Space` `H`)
- [ ] Chat with Haiku (`Space` `c`)
- [ ] Diagnostics overlay
- [ ] Logs viewer (`L`)
- [ ] Image preview in terminal
- [ ] Open image in external viewer
- [ ] Navigate/delete attachments in detail panel
- [ ] Full settings overlay with all toggles

---

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
