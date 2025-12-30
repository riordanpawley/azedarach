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
│   └── az/
│       └── main.go           # Entry point + CLI commands
├── internal/
│   ├── app/
│   │   ├── model.go          # Main TEA model
│   │   ├── update.go         # Message handlers (by mode)
│   │   ├── view.go           # View composition
│   │   ├── keybindings.go    # Key mappings + help text
│   │   └── messages.go       # All message types
│   ├── ui/
│   │   ├── board/
│   │   │   ├── board.go      # Kanban board layout
│   │   │   ├── column.go     # Single column
│   │   │   └── card.go       # Task card
│   │   ├── compact/
│   │   │   └── list.go       # Compact list view
│   │   ├── overlay/
│   │   │   ├── stack.go      # Overlay stack manager
│   │   │   ├── action.go     # Action menu
│   │   │   ├── filter.go     # Filter menu + sub-menus
│   │   │   ├── sort.go       # Sort menu
│   │   │   ├── search.go     # Search input
│   │   │   ├── help.go       # Help overlay
│   │   │   ├── detail.go     # Detail panel
│   │   │   ├── settings.go   # Settings overlay
│   │   │   ├── confirm.go    # Confirm dialog
│   │   │   ├── project.go    # Project selector
│   │   │   └── planning.go   # Planning workflow
│   │   ├── statusbar.go      # Status bar
│   │   ├── toast.go          # Toast notifications
│   │   └── styles/
│   │       ├── theme.go      # Catppuccin colors
│   │       └── styles.go     # Component styles
│   ├── domain/
│   │   ├── task.go           # Task/Bead types
│   │   ├── session.go        # Session state machine
│   │   ├── project.go        # Project types
│   │   ├── filter.go         # Filter state
│   │   └── sort.go           # Sort state
│   ├── services/
│   │   ├── beads/
│   │   │   ├── client.go     # bd CLI wrapper
│   │   │   └── parser.go     # JSON parsing
│   │   ├── tmux/
│   │   │   ├── client.go     # tmux operations
│   │   │   ├── session.go    # Session management
│   │   │   └── bindings.go   # Global keybinding registration
│   │   ├── git/
│   │   │   ├── client.go     # git operations
│   │   │   ├── worktree.go   # Worktree lifecycle
│   │   │   └── diff.go       # Difftastic integration
│   │   ├── claude/
│   │   │   └── session.go    # Claude session spawning
│   │   ├── devserver/
│   │   │   ├── manager.go    # Dev server lifecycle
│   │   │   └── ports.go      # Port allocation
│   │   ├── monitor/
│   │   │   ├── session.go    # Session state polling
│   │   │   └── patterns.go   # State detection regex
│   │   ├── clipboard/
│   │   │   └── clipboard.go  # Cross-platform clipboard
│   │   ├── image/
│   │   │   └── attach.go     # Image attachment handling
│   │   └── network/
│   │       └── status.go     # Network connectivity check
│   └── config/
│       ├── config.go         # Configuration types + loading
│       ├── projects.go       # Global projects registry
│       └── defaults.go       # Default values
├── pkg/
│   └── option/
│       └── option.go         # Option[T] type for Go
├── testdata/                  # Golden files for snapshot tests
├── go.mod
├── go.sum
├── Makefile
├── .goreleaser.yaml          # Release automation
├── PLAN.md
├── ARCHITECTURE.md
└── QUICK_REFERENCE.md
```

## Go-Specific Library Choices

| Feature | Library | Notes |
|---------|---------|-------|
| TUI Framework | `charmbracelet/bubbletea` | Core TEA loop |
| Components | `charmbracelet/bubbles` | textinput, viewport, list, spinner, progress |
| Styling | `charmbracelet/lipgloss` | Terminal styling |
| CLI Parsing | `spf13/cobra` | Subcommands (project add/list/etc) |
| Config Loading | `spf13/viper` | JSON/YAML config with env overrides |
| JSON | `encoding/json` | Standard library sufficient |
| Clipboard | `atotto/clipboard` | Cross-platform (macOS pbcopy, Linux xclip/wl-copy) |
| Image Render | `charmbracelet/x/term` + raw ANSI | Kitty/iTerm2 protocols |
| Logging | `charmbracelet/log` | Styled logging to file |
| Testing | `stretchr/testify` | Assertions + mocks |
| Golden Tests | `sebdah/goldie` | Snapshot testing for views |

## Go Best Practices

### Project Layout Rationale

Following the [Standard Go Project Layout](https://github.com/golang-standards/project-layout):

```
cmd/           # Main applications - minimal, just wiring
internal/      # Private code - can't be imported by other projects
pkg/           # Public libraries - reusable by others (use sparingly)
testdata/      # Test fixtures - ignored by go build
```

**Why `internal/`?**
- Compiler-enforced encapsulation
- Free to refactor without breaking external consumers
- Clear signal: "this is private implementation"

**Why thin `cmd/`?**
```go
// cmd/az/main.go - GOOD: minimal wiring only
func main() {
    cfg := config.Load()
    app := app.New(cfg)
    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}

// cmd/az/main.go - BAD: business logic in main
func main() {
    tasks, _ := beads.List()  // Don't do this
    // ...
}
```

### Dependency Injection via Interfaces

**Accept interfaces, return structs:**

```go
// internal/services/beads/client.go

// CommandRunner abstracts exec.Command for testing
type CommandRunner interface {
    Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Client wraps the bd CLI
type Client struct {
    runner CommandRunner
    logger *slog.Logger
}

// NewClient creates a Client with the given dependencies
func NewClient(runner CommandRunner, logger *slog.Logger) *Client {
    return &Client{
        runner: runner,
        logger: logger,
    }
}

// List fetches all beads
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
    out, err := c.runner.Run(ctx, "bd", "list", "--format=json")
    if err != nil {
        return nil, &domain.BeadsError{Op: "list", Err: err}
    }
    return parseTasksJSON(out)
}
```

**Test with mock:**

```go
// internal/services/beads/client_test.go

type mockRunner struct {
    output []byte
    err    error
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
    return m.output, m.err
}

func TestClient_List(t *testing.T) {
    runner := &mockRunner{output: []byte(`[{"id":"az-1"}]`)}
    client := NewClient(runner, slog.Default())

    tasks, err := client.List(context.Background())

    require.NoError(t, err)
    assert.Len(t, tasks, 1)
}
```

### Functional Options Pattern

For complex constructors with optional configuration:

```go
// internal/services/monitor/session.go

type Monitor struct {
    pollInterval time.Duration
    patterns     map[SessionState][]*regexp.Regexp
    logger       *slog.Logger
}

type Option func(*Monitor)

func WithPollInterval(d time.Duration) Option {
    return func(m *Monitor) {
        m.pollInterval = d
    }
}

func WithLogger(l *slog.Logger) Option {
    return func(m *Monitor) {
        m.logger = l
    }
}

func NewMonitor(opts ...Option) *Monitor {
    m := &Monitor{
        pollInterval: 500 * time.Millisecond, // sensible default
        patterns:     defaultPatterns(),
        logger:       slog.Default(),
    }
    for _, opt := range opts {
        opt(m)
    }
    return m
}

// Usage:
monitor := NewMonitor(
    WithPollInterval(1 * time.Second),
    WithLogger(customLogger),
)
```

### Context Propagation

**Always pass context as first parameter:**

```go
// GOOD: Context flows through entire call chain
func (c *Client) List(ctx context.Context) ([]domain.Task, error) {
    out, err := c.runner.Run(ctx, "bd", "list")
    // ...
}

func (c *Client) Create(ctx context.Context, task domain.Task) error {
    // ...
}

// BAD: No context - can't cancel, no deadline
func (c *Client) List() ([]domain.Task, error) {
    // ...
}
```

**Bubbletea context pattern:**

```go
// Pass context via message for cancellation
type startMonitorMsg struct {
    ctx context.Context
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case startMonitorMsg:
        return m, m.startPolling(msg.ctx)
    }
    // ...
}
```

### Error Handling Idioms

**Wrap errors with context:**

```go
import "fmt"

func (c *Client) Create(ctx context.Context, task domain.Task) error {
    out, err := c.runner.Run(ctx, "bd", "create", "--title", task.Title)
    if err != nil {
        // Wrap with context about what we were doing
        return fmt.Errorf("creating bead %q: %w", task.Title, err)
    }
    return nil
}
```

**Sentinel errors for expected conditions:**

```go
// internal/domain/errors.go

var (
    ErrNotFound     = errors.New("not found")
    ErrConflict     = errors.New("conflict")
    ErrOffline      = errors.New("offline")
    ErrUserCanceled = errors.New("user canceled")
)

// Usage:
if errors.Is(err, domain.ErrNotFound) {
    // Handle not found case
}
```

**Custom error types with `Is()` and `As()`:**

```go
type BeadsError struct {
    Op      string
    BeadID  string
    Message string
    Err     error
}

func (e *BeadsError) Error() string {
    if e.BeadID != "" {
        return fmt.Sprintf("beads %s [%s]: %s", e.Op, e.BeadID, e.Message)
    }
    return fmt.Sprintf("beads %s: %s", e.Op, e.Message)
}

func (e *BeadsError) Unwrap() error { return e.Err }

// Is allows errors.Is(err, target) to work
func (e *BeadsError) Is(target error) bool {
    t, ok := target.(*BeadsError)
    if !ok {
        return false
    }
    return e.Op == t.Op // Match on operation type
}
```

### Concurrency Patterns

**Goroutine lifecycle management:**

```go
// internal/services/monitor/session.go

type SessionMonitor struct {
    mu       sync.RWMutex
    sessions map[string]*monitoredSession
    done     chan struct{}
    wg       sync.WaitGroup
}

type monitoredSession struct {
    beadID string
    cancel context.CancelFunc
}

func (m *SessionMonitor) Start(ctx context.Context, beadID string, program *tea.Program) {
    m.mu.Lock()
    defer m.mu.Unlock()

    // Don't start duplicate monitors
    if _, exists := m.sessions[beadID]; exists {
        return
    }

    ctx, cancel := context.WithCancel(ctx)
    m.sessions[beadID] = &monitoredSession{beadID: beadID, cancel: cancel}

    m.wg.Add(1)
    go func() {
        defer m.wg.Done()
        m.poll(ctx, beadID, program)
    }()
}

func (m *SessionMonitor) Stop(beadID string) {
    m.mu.Lock()
    defer m.mu.Unlock()

    if session, ok := m.sessions[beadID]; ok {
        session.cancel()
        delete(m.sessions, beadID)
    }
}

func (m *SessionMonitor) Shutdown() {
    close(m.done)
    m.mu.Lock()
    for _, s := range m.sessions {
        s.cancel()
    }
    m.mu.Unlock()
    m.wg.Wait() // Wait for all goroutines to finish
}
```

**Channel patterns:**

```go
// Fan-out: Multiple goroutines reading from one channel
func pollSessions(ctx context.Context, beadIDs <-chan string) {
    for beadID := range beadIDs {
        go pollOne(ctx, beadID)
    }
}

// Fan-in: Multiple goroutines writing to one channel
func collectResults(ctx context.Context, results chan<- SessionState) {
    var wg sync.WaitGroup
    for _, id := range activeSessionIDs {
        wg.Add(1)
        go func(id string) {
            defer wg.Done()
            state := detectState(id)
            select {
            case results <- state:
            case <-ctx.Done():
            }
        }(id)
    }
    go func() {
        wg.Wait()
        close(results)
    }()
}
```

### Package Design Principles

**1. Small, focused packages:**

```
internal/services/beads/     # Only beads CLI interaction
internal/services/git/       # Only git operations
internal/domain/             # Only data types, no I/O
```

**2. Avoid circular dependencies:**

```
domain/  ←──  services/  ←──  app/
   ↑             ↑              ↑
   └─────────────┴──────────────┘
              ui/

# domain/ imports nothing internal
# services/ imports domain/
# app/ imports services/, domain/
# ui/ imports domain/ (for types), never services/
```

**3. Package by feature, not layer (for complex features):**

```
# Instead of:
handlers/
  beads.go
  git.go
services/
  beads.go
  git.go

# Consider:
beads/
  client.go      # Service
  handlers.go    # UI handlers
  types.go       # Domain types
git/
  client.go
  worktree.go
  handlers.go
```

### Table-Driven Tests

```go
func TestDetectState(t *testing.T) {
    tests := []struct {
        name   string
        output string
        want   SessionState
    }{
        {
            name:   "waiting for confirmation",
            output: "Do you want to proceed? [y/n]",
            want:   StateWaiting,
        },
        {
            name:   "task completed",
            output: "Task completed successfully",
            want:   StateDone,
        },
        {
            name:   "error state",
            output: "Error: something went wrong",
            want:   StateError,
        },
        {
            name:   "active output",
            output: "Building project...",
            want:   StateBusy,
        },
        {
            name:   "empty output",
            output: "",
            want:   StateIdle,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := DetectState(tt.output)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### Structured Logging

```go
import "log/slog"

// Create logger with context
logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))

// Add fields progressively
logger = logger.With("component", "beads")

// Log with structured fields
logger.Info("fetching beads",
    "count", len(tasks),
    "filter", filter.String(),
)

logger.Error("beads command failed",
    "error", err,
    "command", "bd list",
)
```

### Graceful Shutdown

```go
// cmd/az/main.go

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(),
        syscall.SIGINT, syscall.SIGTERM)
    defer cancel()

    app := app.New(cfg)

    // Run with graceful shutdown
    errCh := make(chan error, 1)
    go func() {
        errCh <- app.Run(ctx)
    }()

    select {
    case err := <-errCh:
        if err != nil {
            log.Fatal(err)
        }
    case <-ctx.Done():
        // Give app time to cleanup
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        app.Shutdown(shutdownCtx)
    }
}
```

### Configuration Best Practices

**Environment variable precedence:**

```go
// 1. Defaults (in code)
// 2. Config file (.azedarach.json)
// 3. Environment variables (AZ_*)
// 4. CLI flags

func Load() (*Config, error) {
    cfg := DefaultConfig()

    // Load from file
    if err := loadFromFile(cfg); err != nil && !os.IsNotExist(err) {
        return nil, err
    }

    // Override with env vars
    if v := os.Getenv("AZ_CLI_TOOL"); v != "" {
        cfg.CLITool = v
    }
    if v := os.Getenv("AZ_SKIP_PERMISSIONS"); v == "true" {
        cfg.Session.DangerouslySkipPermissions = true
    }

    return cfg, nil
}
```

**Validate configuration early:**

```go
func (c *Config) Validate() error {
    var errs []error

    if c.CLITool != "claude" && c.CLITool != "opencode" {
        errs = append(errs, fmt.Errorf("invalid cliTool: %s", c.CLITool))
    }

    if c.DevServer.Ports == nil {
        c.DevServer.Ports = make(map[string]PortConfig)
    }

    return errors.Join(errs...)
}
```

### Clipboard Cross-Platform Strategy

```go
// internal/services/clipboard/clipboard.go
package clipboard

import (
    "os/exec"
    "runtime"
)

// ReadImage reads image data from clipboard
func ReadImage() ([]byte, error) {
    switch runtime.GOOS {
    case "darwin":
        // macOS: Use osascript to get clipboard as PNG
        return exec.Command("osascript", "-e",
            `set png to (the clipboard as «class PNGf»)
             return png`).Output()
    case "linux":
        // Try wl-paste (Wayland) first, then xclip (X11)
        if out, err := exec.Command("wl-paste", "-t", "image/png").Output(); err == nil {
            return out, nil
        }
        return exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
    default:
        return nil, fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
    }
}
```

### Image Terminal Rendering Strategy

```go
// For terminals supporting Kitty graphics protocol (Kitty, WezTerm)
// Fall back to Unicode half-blocks for others

import "github.com/charmbracelet/x/term"

func RenderImage(path string, width, height int) string {
    info := term.GetTerminalInfo()

    switch {
    case info.KittyGraphics:
        return renderKittyImage(path, width, height)
    case info.ITerm2:
        return renderITerm2Image(path, width, height)
    default:
        // Unicode half-blocks fallback
        return renderBlocksImage(path, width, height)
    }
}
```

## Error Handling Patterns

### Typed Errors

```go
// internal/domain/errors.go
package domain

import "fmt"

// BeadsError for beads CLI failures
type BeadsError struct {
    Op      string // "list", "create", "update"
    BeadID  string // optional
    Message string
    Err     error
}

func (e *BeadsError) Error() string {
    if e.BeadID != "" {
        return fmt.Sprintf("beads %s [%s]: %s", e.Op, e.BeadID, e.Message)
    }
    return fmt.Sprintf("beads %s: %s", e.Op, e.Message)
}

func (e *BeadsError) Unwrap() error { return e.Err }

// TmuxError for tmux failures
type TmuxError struct {
    Op      string
    Session string
    Err     error
}

// GitError for git/worktree failures
type GitError struct {
    Op       string
    Worktree string
    Err      error
}
```

### Error Flow to UI

```go
// Errors become toast notifications
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case errorMsg:
        m.toasts = append(m.toasts, Toast{
            Level:     ToastError,
            Message:   formatError(msg.err),
            ExpiresAt: time.Now().Add(8 * time.Second),
        })
        return m, nil
    }
    // ...
}

func formatError(err error) string {
    switch e := err.(type) {
    case *domain.BeadsError:
        return fmt.Sprintf("Beads %s failed: %s", e.Op, e.Message)
    case *domain.TmuxError:
        return fmt.Sprintf("tmux error: %s", e.Err)
    case *domain.GitError:
        return fmt.Sprintf("Git %s failed: %s", e.Op, e.Err)
    default:
        return err.Error()
    }
}
```

## Configuration Schema

### `.azedarach.json` (Project-level)

```go
// internal/config/config.go
type Config struct {
    CLITool  string        `json:"cliTool"`  // "claude" | "opencode"
    Session  SessionConfig `json:"session"`
    Git      GitConfig     `json:"git"`
    PR       PRConfig      `json:"pr"`
    DevServer DevServerConfig `json:"devServer"`
    Notifications NotifyConfig `json:"notifications"`
    Network  NetworkConfig `json:"network"`
    Beads    BeadsConfig   `json:"beads"`
    StateDetection StateConfig `json:"stateDetection"`
}

type SessionConfig struct {
    DangerouslySkipPermissions bool   `json:"dangerouslySkipPermissions"`
    Shell                      string `json:"shell"` // default: $SHELL or "zsh"
}

type GitConfig struct {
    PushBranchOnCreate bool `json:"pushBranchOnCreate"`
    PushEnabled        bool `json:"pushEnabled"`
    FetchEnabled       bool `json:"fetchEnabled"`
    ShowLineChanges    bool `json:"showLineChanges"`
    BaseBranch         string `json:"baseBranch"` // default: "main"
}

type PRConfig struct {
    Enabled   bool `json:"enabled"`
    AutoDraft bool `json:"autoDraft"`
    AutoMerge bool `json:"autoMerge"`
}

type DevServerConfig struct {
    Command string                     `json:"command"` // default: "bun run dev"
    Ports   map[string]PortConfig      `json:"ports"`
}

type PortConfig struct {
    Default int      `json:"default"`
    Aliases []string `json:"aliases"` // env var names
}
```

### `~/.config/azedarach/projects.json` (Global)

```go
// internal/config/projects.go
type ProjectsRegistry struct {
    Projects       []Project `json:"projects"`
    DefaultProject string    `json:"defaultProject"`
}

type Project struct {
    Name string `json:"name"`
    Path string `json:"path"`
}

func LoadProjectsRegistry() (*ProjectsRegistry, error) {
    home, _ := os.UserHomeDir()
    path := filepath.Join(home, ".config", "azedarach", "projects.json")
    // ...
}
```

## Implementation Phases (Detailed)

### Phase 1: Core Framework

**Goal**: Basic TEA loop with navigation

**Deliverables**:
- [ ] Project setup (go.mod, Makefile, .goreleaser.yaml)
- [ ] Main model struct with cursor, mode, basic state
- [ ] Basic keybinding handling (hjkl + arrows navigation)
- [ ] Half-page scroll (`Ctrl-Shift-d/u`)
- [ ] Force redraw (`Ctrl-l`)
- [ ] Static 4-column Kanban board rendering
- [ ] Lip Gloss theme (Catppuccin Macchiato)
- [ ] StatusBar with mode indicator + keybinding hints
- [ ] Quit (`q`, `Ctrl-c`)

**Acceptance Criteria**:
- `go build` produces working binary
- Navigate between columns with h/l
- Navigate within columns with j/k
- StatusBar shows current mode
- Half-page scroll works in tall columns

**Dependencies**: None

**Testing**:
- Unit: None (pure UI)
- Golden: Board rendering snapshots
- Manual: Navigation feel

---

### Phase 2: Beads Integration

**Goal**: Load and display real bead data

**Deliverables**:
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

**Acceptance Criteria**:
- Board shows real beads from `bd list`
- Cards show correct status colors
- Priority badges (P0-P4) visible
- Toasts appear and auto-dismiss
- Periodic refresh doesn't flicker

**Dependencies**: Phase 1

**Testing**:
- Unit: Beads client parsing, filter logic
- Golden: Card rendering with various states
- Integration: Mock `bd` CLI responses

---

### Phase 3: Overlays & Filters

**Goal**: Modal overlays and filtering

**Deliverables**:
- [ ] Overlay stack system (push/pop)
- [ ] Action menu (`Space`) with available actions
- [ ] Filter menu (`f`) with sub-menus:
  - Status (`f` `s`): o/i/b/d toggles
  - Priority (`f` `p`): 0-4 toggles
  - Type (`f` `t`): B/F/T/E/C toggles
  - Session (`f` `S`): I/U/W/D/X/P toggles
  - Hide epic children (`f` `e`)
  - Age filter (`f` `1/7/3/0`)
  - Clear all (`f` `c`)
- [ ] Sort menu (`,`):
  - Sort by session (`,` `s`)
  - Sort by priority (`,` `p`)
  - Sort by updated (`,` `u`)
  - Toggle direction (repeat key)
- [ ] Help overlay (`?`)
- [ ] Search input (`/`) with live filtering
- [ ] Select mode (`v`) with visual highlighting
- [ ] Select all (`%`) and clear (`A`)
- [ ] Goto mode (`g`):
  - Column top (`g` `g`)
  - Column bottom (`g` `e`)
  - First/last column (`g` `h`/`l`)
- [ ] Compact/list view toggle (`Tab`)
- [ ] Move task left/right (`Space` `h/l`)
- [ ] StatusBar selection count

**Acceptance Criteria**:
- All overlays render centered with proper styling
- Filter combinations work (AND between types, OR within)
- Search is case-insensitive, matches title + ID
- Selected tasks visually distinct
- Compact view shows all tasks in priority order

**Dependencies**: Phase 2

**Testing**:
- Unit: Filter logic, sort comparisons
- Golden: All overlay renderings
- Manual: Mode transitions feel snappy

---

### Phase 4: Session Management

**Goal**: Spawn and manage Claude sessions

**Deliverables**:
- [ ] tmux client (new-session, attach, send-keys, capture-pane, kill-session)
- [ ] Worktree management (create, delete, list)
- [ ] Session state detection (polling + pattern matching)
- [ ] Session actions:
  - Start session (`Space` `s`)
  - Start + work (`Space` `S`)
  - Start yolo (`Space` `!`)
  - Attach (`Space` `a`)
  - Pause (`Space` `p`)
  - Resume (`Space` `R`)
  - Stop (`Space` `x`)
- [ ] Dev server management:
  - Toggle (`Space` `r`)
  - View (`Space` `v`)
  - Restart (`Space` `Ctrl+r`)
  - Port allocation with conflict resolution
  - StatusBar port indicator
  - Dev server menu overlay
- [ ] Delete worktree/cleanup (`Space` `d`)
- [ ] Confirm dialog for destructive actions
- [ ] Bulk cleanup dialog (worktrees only / full)
- [ ] Bulk stop sessions
- [ ] Register tmux global bindings:
  - `Ctrl-a Ctrl-a`: Return to az
  - `Ctrl-a Tab`: Toggle Claude/Dev
- [ ] Session monitor goroutine (500ms polling)

**Acceptance Criteria**:
- `Space+s` creates worktree + tmux session + launches claude
- Session state updates within 1s of change
- `Space+a` attaches to correct session
- Port allocation avoids conflicts
- Cleanup removes worktree and closes bead (if selected)

**Dependencies**: Phase 3 (for confirm dialog, bulk operations)

**Testing**:
- Unit: State pattern matching, port allocation
- Integration: Mock tmux/git commands
- Manual: Full session lifecycle

---

### Phase 5: Git Operations

**Goal**: Git workflow support

**Deliverables**:
- [ ] Git client (status, fetch, merge, diff, branch, push)
- [ ] Update from main (`Space` `u`)
- [ ] Merge to main (`Space` `m`) with conflict detection
- [ ] Create PR (`Space` `P`) via `gh` CLI
- [ ] Show diff with difftastic (`Space` `f`)
- [ ] Abort merge (`Space` `M`)
- [ ] Merge bead into... (`Space` `b`) with merge select mode
- [ ] Refresh git stats (`r`)
- [ ] Merge choice dialog
- [ ] Network status detection
- [ ] Offline mode / graceful degradation
- [ ] Connection status in StatusBar

**Acceptance Criteria**:
- Merge detects conflicts and shows affected files
- Conflict resolution starts Claude session automatically
- PR creation syncs with main first
- Diff viewer shows side-by-side difftastic output
- Offline mode disables push/fetch gracefully

**Dependencies**: Phase 4 (session management for conflict resolution)

**Testing**:
- Unit: Git output parsing
- Integration: Mock git/gh commands
- Manual: Full merge workflow

---

### Phase 6: Advanced Features

**Goal**: Full feature parity

**Deliverables**:
- [ ] Epic drill-down view:
  - Enter on epic shows only children
  - Progress bar (closed/total)
  - Back navigation (`q`/`Esc`)
- [ ] Jump labels (`g` `w`):
  - 2-char labels from home row
  - Type label to jump
- [ ] Multi-project support:
  - Project selector overlay (`g` `p`)
  - Project auto-detection from cwd
  - CLI: `az project add/list/remove/switch`
- [ ] Create/edit beads:
  - Manual create (`c`) via $EDITOR
  - Claude create (`C`) with prompt overlay
  - Manual edit (`Space` `e`)
  - Claude edit (`Space` `E`)
- [ ] Open Helix editor (`Space` `H`)
- [ ] Chat with Haiku (`Space` `c`)
- [ ] Image attachments:
  - Attach overlay (`Space` `i`)
  - Paste from clipboard (`p`/`v`)
  - Attach from file path (`f`)
  - Preview in terminal
  - Open in external viewer (`o`)
  - Navigate/delete in detail panel
- [ ] Detail panel:
  - Scrollable description
  - Attachment list
  - Edit actions
- [ ] Settings overlay (`s`):
  - All toggleable settings
  - Edit in $EDITOR option
- [ ] Diagnostics overlay
- [ ] Logs viewer (`L`)
- [ ] Planning workflow integration

**Acceptance Criteria**:
- Epic drill-down filters to children only
- Jump labels work across visible tasks
- Project switch refreshes board
- Image preview works in major terminals
- Settings persist to `.azedarach.json`

**Dependencies**: All previous phases

**Testing**:
- Unit: Jump label generation, image detection
- Golden: Epic header, settings overlay
- Manual: Full workflow testing

## Phase Dependencies Graph

```
Phase 1 (Core)
    │
    ▼
Phase 2 (Beads)
    │
    ▼
Phase 3 (Overlays) ─────────────────┐
    │                               │
    ▼                               ▼
Phase 4 (Sessions) ──────────► Phase 5 (Git)
    │                               │
    └───────────────┬───────────────┘
                    ▼
              Phase 6 (Advanced)
```

## Technical Challenges & Solutions

### 1. Session State Detection

**Challenge**: Detecting Claude session state (busy/waiting/done/error) from tmux output.

**Solution**: Regex pattern matching on captured pane content:

```go
var statePatterns = map[SessionState][]*regexp.Regexp{
    StateWaiting: {
        regexp.MustCompile(`\[y/n\]`),
        regexp.MustCompile(`Do you want to`),
        regexp.MustCompile(`AskUserQuestion`),
        regexp.MustCompile(`Press Enter`),
        regexp.MustCompile(`\? \[Y/n\]`),
    },
    StateDone: {
        regexp.MustCompile(`Task completed`),
        regexp.MustCompile(`Successfully completed`),
        regexp.MustCompile(`✓.*done`),
    },
    StateError: {
        regexp.MustCompile(`Error:`),
        regexp.MustCompile(`Exception:`),
        regexp.MustCompile(`FATAL`),
        regexp.MustCompile(`panic:`),
    },
}

func DetectState(output string) SessionState {
    // Check last 100 lines only (performance)
    lines := strings.Split(output, "\n")
    if len(lines) > 100 {
        lines = lines[len(lines)-100:]
    }
    recent := strings.Join(lines, "\n")

    for state, patterns := range statePatterns {
        for _, p := range patterns {
            if p.MatchString(recent) {
                return state
            }
        }
    }
    if strings.TrimSpace(recent) != "" {
        return StateBusy
    }
    return StateIdle
}
```

### 2. Port Allocation for Dev Servers

**Challenge**: Multiple dev servers need unique ports without conflicts.

**Solution**: Track allocated ports, find next available:

```go
type PortAllocator struct {
    mu        sync.Mutex
    allocated map[int]string // port -> beadID
    basePort  int
}

func (p *PortAllocator) Allocate(beadID string, config PortConfig) (int, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    // Check if already allocated
    for port, id := range p.allocated {
        if id == beadID {
            return port, nil
        }
    }

    // Find next available port
    port := config.Default
    for {
        if _, used := p.allocated[port]; !used {
            if isPortAvailable(port) {
                p.allocated[port] = beadID
                return port, nil
            }
        }
        port++
        if port > config.Default+100 {
            return 0, fmt.Errorf("no available ports in range %d-%d", config.Default, port)
        }
    }
}

func isPortAvailable(port int) bool {
    ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
    if err != nil {
        return false
    }
    ln.Close()
    return true
}
```

### 3. Jump Labels Generation

**Challenge**: Generate unique 2-char labels for visible tasks using home row.

**Solution**: Generate labels from alphabet, assign to visible tasks:

```go
var homeRow = []rune("asdfghjkl;")

func GenerateLabels(count int) []string {
    labels := make([]string, 0, count)

    // Single char first (a, s, d, ...)
    for _, c := range homeRow {
        if len(labels) >= count {
            break
        }
        labels = append(labels, string(c))
    }

    // Then double char (aa, as, ad, ...)
    for _, c1 := range homeRow {
        for _, c2 := range homeRow {
            if len(labels) >= count {
                return labels
            }
            labels = append(labels, string([]rune{c1, c2}))
        }
    }

    return labels
}
```

### 4. Overlay Stack

**Challenge**: Multiple overlays can stack (e.g., filter menu inside action menu).

**Solution**: Stack-based overlay management:

```go
type OverlayStack struct {
    stack []Overlay
}

func (s *OverlayStack) Push(o Overlay) {
    s.stack = append(s.stack, o)
}

func (s *OverlayStack) Pop() Overlay {
    if len(s.stack) == 0 {
        return nil
    }
    o := s.stack[len(s.stack)-1]
    s.stack = s.stack[:len(s.stack)-1]
    return o
}

func (s *OverlayStack) Current() Overlay {
    if len(s.stack) == 0 {
        return nil
    }
    return s.stack[len(s.stack)-1]
}

// In view, render topmost overlay
func (m Model) View() string {
    base := m.renderBoard()
    if overlay := m.overlays.Current(); overlay != nil {
        return renderOverlayOn(base, overlay.View(), m.width, m.height)
    }
    return base
}
```

### 5. Network Status Detection

**Challenge**: Detect network connectivity without blocking UI.

**Solution**: Background goroutine with periodic checks:

```go
func (m *Model) startNetworkMonitor(program *tea.Program) {
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()

        for range ticker.C {
            online := checkConnectivity()
            program.Send(networkStatusMsg{online: online})
        }
    }()
}

func checkConnectivity() bool {
    // Try to reach GitHub API (or similar reliable endpoint)
    client := &http.Client{Timeout: 5 * time.Second}
    resp, err := client.Head("https://api.github.com")
    if err != nil {
        return false
    }
    resp.Body.Close()
    return resp.StatusCode == 200
}
```

## Performance Targets

| Metric | Target | Notes |
|--------|--------|-------|
| Startup time | < 100ms | Cold start to first render |
| Binary size | < 15MB | Single static binary |
| Memory usage | < 50MB | With 100+ tasks loaded |
| Refresh rate | 60 FPS | Smooth scrolling |
| State detection | < 500ms | From Claude output to UI update |
| Beads refresh | < 200ms | `bd list` round trip |

### Optimization Strategies

1. **Lazy rendering**: Only render visible cards, not entire column
2. **Debounced refresh**: Don't re-fetch beads on every tick if nothing changed
3. **Cached styles**: Pre-compute Lip Gloss styles, don't recreate per render
4. **Parallel I/O**: Fetch beads and session states concurrently
5. **Incremental updates**: Only update changed cards, not full board

## Migration Strategy

### Running Both Versions

During development, both TypeScript and Go versions can coexist:

```bash
# TypeScript version (current)
bun run dev          # or: az (if installed globally)

# Go version (new)
go run ./cmd/az      # or: az-go (different binary name)
```

### Shared Configuration

Both versions read the same config files:
- `.azedarach.json` - Project config
- `~/.config/azedarach/projects.json` - Global projects

### Shared State

Both versions interact with:
- `.beads/` directory - Bead tracker data
- tmux sessions - Named consistently (`az-{beadId}`)
- Git worktrees - Same naming convention

### Feature Flag for Transition

```bash
# Set preferred version globally
export AZ_RUNTIME=go  # or: ts

# az wrapper script detects and launches correct version
```

### Gradual Rollout Plan

1. **Alpha**: Go version usable for basic workflows (Phase 1-3)
2. **Beta**: Session management works (Phase 4-5)
3. **RC**: Full feature parity (Phase 6)
4. **GA**: Go becomes default, TS deprecated

## Testing Strategy (Detailed)

### Unit Tests

```go
// internal/domain/filter_test.go
func TestFilterTasks(t *testing.T) {
    tasks := []Task{
        {ID: "az-1", Status: StatusOpen, Priority: P1},
        {ID: "az-2", Status: StatusInProgress, Priority: P2},
        {ID: "az-3", Status: StatusOpen, Priority: P1},
    }

    filter := Filter{
        Status:   map[Status]bool{StatusOpen: true},
        Priority: map[Priority]bool{P1: true},
    }

    result := filter.Apply(tasks)
    assert.Len(t, result, 2)
    assert.Equal(t, "az-1", result[0].ID)
    assert.Equal(t, "az-3", result[1].ID)
}
```

### Golden/Snapshot Tests

```go
// internal/ui/board/board_test.go
func TestBoardRendering(t *testing.T) {
    board := NewBoard(testTasks, testStyles)
    board.SetSize(80, 24)
    board.SetCursor(1, 2) // Column 1, Task 2

    output := board.View()

    golden.Assert(t, output, "board_with_cursor.golden")
}
```

### Integration Tests

```go
// internal/services/beads/client_test.go
func TestBeadsClient(t *testing.T) {
    // Mock bd CLI
    execCommand = fakeExecCommand
    defer func() { execCommand = exec.Command }()

    client := NewClient()
    tasks, err := client.List()

    assert.NoError(t, err)
    assert.Len(t, tasks, 3)
}

func fakeExecCommand(name string, args ...string) *exec.Cmd {
    // Return mock data based on args
}
```

### Manual Test Checklist

```markdown
## Phase 1 Smoke Test
- [ ] `go build && ./bin/az` starts without error
- [ ] hjkl navigation works
- [ ] Arrow keys work
- [ ] Half-page scroll works in tall column
- [ ] `q` quits cleanly
- [ ] StatusBar shows mode

## Phase 2 Smoke Test
- [ ] Board loads real beads
- [ ] Cards show correct colors
- [ ] Priority badges visible
- [ ] Toast appears on error
- [ ] Periodic refresh works (edit bead externally, see update)

... (continue for each phase)
```

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
