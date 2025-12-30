# Go/Bubbletea Architecture

## System Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              TERMINAL (Bubbletea)                            │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                           Bubbletea Program                            │  │
│  │                    TEA: Model → Update → View                          │  │
│  │                                                                        │  │
│  │  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐                   │  │
│  │  │ backlog │  │in_prog  │  │ blocked │  │  done   │  ← Kanban Board   │  │
│  │  │ ┌─────┐ │  │ ┌─────┐ │  │ ┌─────┐ │  │ ┌─────┐ │                   │  │
│  │  │ │Card │ │  │ │Card │ │  │ │Card │ │  │ │Card │ │  (Bubbles list)   │  │
│  │  │ └─────┘ │  │ └─────┘ │  │ └─────┘ │  │ └─────┘ │                   │  │
│  │  └─────────┘  └─────────┘  └─────────┘  └─────────┘                   │  │
│  │                                                                        │  │
│  │  [Status Bar: mode | project | session state | dev server port]       │  │
│  │                                      (Lip Gloss styled)               │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      │ tea.Cmd (async messages)
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            Service Layer (Goroutines)                        │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                         Session Monitor                                │  │
│  │                    (goroutine per active session)                      │  │
│  │                                                                        │  │
│  │  - Polls tmux output (500ms interval)                                 │  │
│  │  - Pattern matches for state (Busy/Waiting/Done/Error)                │  │
│  │  - Sends sessionStateMsg to tea.Program                               │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│           │                    │                    │                        │
│           ▼                    ▼                    ▼                        │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐              │
│  │  Beads Client   │  │  Tmux Client    │  │   Git Client    │              │
│  │                 │  │                 │  │                 │              │
│  │ • ListAll()     │  │ • NewSession()  │  │ • CreateWorktree│              │
│  │ • Create()      │  │ • Attach()      │  │ • MergeMain()   │              │
│  │ • Update()      │  │ • SendKeys()    │  │ • CreatePR()    │              │
│  │ • Search()      │  │ • CapturPane()  │  │ • Diff()        │              │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘              │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      │ exec.Command (shell calls)
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           External Systems                                   │
│                                                                              │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐ │
│  │   tmux    │  │    git    │  │  bd CLI   │  │  gh CLI   │  │  claude   │ │
│  │           │  │           │  │  (beads)  │  │ (GitHub)  │  │           │ │
│  └───────────┘  └───────────┘  └───────────┘  └───────────┘  └───────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

## TEA Message Flow

```
┌──────────────────────────────────────────────────────────────────────────┐
│                           Message Flow                                    │
├──────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  1. User presses key                                                     │
│     │                                                                    │
│     ▼                                                                    │
│  2. tea.KeyMsg delivered to Update()                                     │
│     │                                                                    │
│     ▼                                                                    │
│  3. Update() pattern matches on msg type                                 │
│     │                                                                    │
│     ├─→ Navigation key (hjkl) → Update cursor                           │
│     ├─→ Mode key (space/g//) → Change mode                              │
│     ├─→ Action key (s/a/x)   → Return tea.Cmd for async work            │
│     │                                                                    │
│     ▼                                                                    │
│  4. Return (newModel, cmd)                                               │
│     │                                                                    │
│     ├─→ Model changes trigger View() re-render                          │
│     └─→ Cmd executes async, returns new Msg                             │
│                                                                          │
│  5. Cmd result arrives as new Msg                                        │
│     │                                                                    │
│     ▼                                                                    │
│  6. Update() handles result Msg                                          │
│     │                                                                    │
│     └─→ Update model with result, re-render                             │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

## Concurrency Model

### Go Approach (vs OTP)

```go
// Session monitor runs in a goroutine
func (m *SessionMonitor) Start(program *tea.Program) {
    go func() {
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                output := m.tmux.CapturePanne(m.sessionName)
                state := m.detectState(output)
                if state != m.lastState {
                    m.lastState = state
                    // Send message back to TEA loop
                    program.Send(sessionStateMsg{
                        beadID: m.beadID,
                        state:  state,
                    })
                }
            case <-m.quit:
                return
            }
        }
    }()
}
```

### Key Differences from OTP

| Aspect | OTP (Gleam) | Goroutines (Go) |
|--------|-------------|-----------------|
| Crash handling | Supervisors restart | Manual recovery/defer |
| Communication | Actor mailbox | Channels or tea.Program.Send |
| Isolation | Full process isolation | Shared memory (careful!) |
| Linking | process.spawn_link | Context cancellation |
| State | Actor state | Struct fields (mutex if shared) |

## Component Model

```
┌─────────────────────────────────────────────────────────────────────┐
│                       Component Hierarchy                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  Model (main)                                                       │
│  ├── Board                                                          │
│  │   ├── Column[4]                                                  │
│  │   │   └── Card[n]                                                │
│  │   │       ├── Title (styled)                                     │
│  │   │       ├── SessionState indicator                             │
│  │   │       └── Priority/Type badges                               │
│  │   └── (Lip Gloss layout)                                         │
│  │                                                                  │
│  ├── StatusBar                                                      │
│  │   ├── Mode indicator                                             │
│  │   ├── Project name                                               │
│  │   ├── Session count                                              │
│  │   └── Dev server status                                          │
│  │                                                                  │
│  ├── Overlay (optional)                                             │
│  │   ├── ActionMenu      (Space)                                    │
│  │   ├── FilterMenu      (f)                                        │
│  │   ├── SortMenu        (,)                                        │
│  │   ├── HelpOverlay     (?)                                        │
│  │   ├── DetailPanel     (Enter)                                    │
│  │   └── SearchInput     (/)                                        │
│  │       └── textinput.Model (Bubbles)                              │
│  │                                                                  │
│  └── Toast (notification stack)                                     │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## Overlay System

```go
type Overlay interface {
    Update(msg tea.Msg) (Overlay, tea.Cmd)
    View() string
}

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
```

## State Detection Patterns

```go
var statePatterns = map[SessionState][]string{
    StateWaiting: {
        `\[y/n\]`,
        `Do you want to`,
        `AskUserQuestion`,
        `Press Enter`,
        `\? \[Y/n\]`,
    },
    StateDone: {
        `Task completed`,
        `Successfully completed`,
        `✓.*done`,
    },
    StateError: {
        `Error:`,
        `Exception:`,
        `Failed:`,
        `FATAL`,
        `panic:`,
    },
}

func detectState(output string) SessionState {
    for state, patterns := range statePatterns {
        for _, pattern := range patterns {
            if matched, _ := regexp.MatchString(pattern, output); matched {
                return state
            }
        }
    }
    if strings.TrimSpace(output) != "" {
        return StateBusy
    }
    return StateIdle
}
```

## Key Bindings Architecture

```go
type KeyMap struct {
    // Navigation
    Up    key.Binding
    Down  key.Binding
    Left  key.Binding
    Right key.Binding

    // Modes
    Action key.Binding
    Search key.Binding
    Goto   key.Binding

    // Actions
    StartSession key.Binding
    Attach       key.Binding
    Stop         key.Binding
    // ...
}

func DefaultKeyMap() KeyMap {
    return KeyMap{
        Up:    key.NewBinding(key.WithKeys("k", "up")),
        Down:  key.NewBinding(key.WithKeys("j", "down")),
        Left:  key.NewBinding(key.WithKeys("h", "left")),
        Right: key.NewBinding(key.WithKeys("l", "right")),

        Action: key.NewBinding(key.WithKeys(" ")),
        Search: key.NewBinding(key.WithKeys("/")),
        Goto:   key.NewBinding(key.WithKeys("g")),

        StartSession: key.NewBinding(key.WithKeys("s")),
        // ...
    }
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch m.mode {
    case ModeNormal:
        return m.handleNormalMode(msg)
    case ModeAction:
        return m.handleActionMode(msg)
    case ModeSearch:
        return m.handleSearchMode(msg)
    // ...
    }
    return m, nil
}
```

## Configuration

```go
type Config struct {
    Worktree   WorktreeConfig   `json:"worktree"`
    Session    SessionConfig    `json:"session"`
    DevServer  DevServerConfig  `json:"devServer"`
    Git        GitConfig        `json:"git"`
    Polling    PollingConfig    `json:"polling"`
}

type GitConfig struct {
    WorkflowMode string `json:"workflowMode"` // "local" or "origin"
    BaseBranch   string `json:"baseBranch"`
    PushEnabled  bool   `json:"pushEnabled"`
    FetchEnabled bool   `json:"fetchEnabled"`
}

func LoadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return DefaultConfig(), nil // Fall back to defaults
    }
    var config Config
    if err := json.Unmarshal(data, &config); err != nil {
        return nil, fmt.Errorf("invalid config: %w", err)
    }
    return &config, nil
}
```

## Error Handling Pattern

```go
// Use typed errors for specific handling
type BeadsError struct {
    Op  string // "list", "create", "update", etc.
    Err error
}

func (e BeadsError) Error() string {
    return fmt.Sprintf("beads %s: %v", e.Op, e.Err)
}

// In Update, show toast on error
case beadsErrorMsg:
    m.toasts = append(m.toasts, Toast{
        Level:   ToastError,
        Message: msg.err.Error(),
        Expires: time.Now().Add(8 * time.Second),
    })
    return m, nil
```
