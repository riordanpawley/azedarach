# Phase 4: Session Management

**Goal**: Spawn and manage Claude sessions

**Status**: 🔲 Not Started

## Deliverables

### tmux Integration
- [ ] tmux client (new-session, attach, send-keys, capture-pane, kill-session)
- [ ] Register tmux global bindings:
  - `Ctrl-a Ctrl-a`: Return to az
  - `Ctrl-a Tab`: Toggle Claude/Dev

### Worktree Management
- [ ] Create worktree for task
- [ ] Delete worktree on cleanup
- [ ] List active worktrees

### Session State Detection
- [ ] Polling goroutine (500ms interval)
- [ ] Pattern matching for states (busy/waiting/done/error)

### Session Actions
- [ ] Start session (`Space` `s`)
- [ ] Start + work (`Space` `S`)
- [ ] Start yolo (`Space` `!`)
- [ ] Attach (`Space` `a`)
- [ ] Pause (`Space` `p`)
- [ ] Resume (`Space` `R`)
- [ ] Stop (`Space` `x`)

### Dev Server Management
- [ ] Toggle (`Space` `r`)
- [ ] View (`Space` `v`)
- [ ] Restart (`Space` `Ctrl+r`)
- [ ] Port allocation with conflict resolution
- [ ] StatusBar port indicator
- [ ] Dev server menu overlay

### Cleanup
- [ ] Delete worktree/cleanup (`Space` `d`)
- [ ] Confirm dialog for destructive actions
- [ ] Bulk cleanup dialog (worktrees only / full)
- [ ] Bulk stop sessions

## Acceptance Criteria

- [ ] `Space+s` creates worktree + tmux session + launches claude
- [ ] Session state updates within 1s of change
- [ ] `Space+a` attaches to correct session
- [ ] Port allocation avoids conflicts
- [ ] Cleanup removes worktree and closes bead (if selected)

## Dependencies

- [Phase 3: Overlays & Filters](phase-3-overlays.md) (for confirm dialog, bulk operations)

## Testing Strategy

| Type | Scope |
|------|-------|
| Unit | State pattern matching, port allocation |
| Integration | Mock tmux/git commands |
| Manual | Full session lifecycle |

## Key Implementation Notes

### Session State Detection

```go
var statePatterns = map[SessionState][]*regexp.Regexp{
    StateWaiting: {
        regexp.MustCompile(`\[y/n\]`),
        regexp.MustCompile(`Do you want to`),
        regexp.MustCompile(`AskUserQuestion`),
        regexp.MustCompile(`Press Enter`),
    },
    StateDone: {
        regexp.MustCompile(`Task completed`),
        regexp.MustCompile(`Successfully completed`),
    },
    StateError: {
        regexp.MustCompile(`Error:`),
        regexp.MustCompile(`Exception:`),
        regexp.MustCompile(`panic:`),
    },
}

func DetectState(output string) SessionState {
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
    return StateBusy
}
```

### Port Allocator

```go
type PortAllocator struct {
    mu        sync.Mutex
    allocated map[int]string // port -> beadID
}

func (p *PortAllocator) Allocate(beadID string, basePort int) (int, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    port := basePort
    for {
        if _, used := p.allocated[port]; !used {
            if isPortAvailable(port) {
                p.allocated[port] = beadID
                return port, nil
            }
        }
        port++
        if port > basePort+100 {
            return 0, fmt.Errorf("no available ports")
        }
    }
}
```

### Session Monitor Goroutine

```go
func (m *SessionMonitor) Start(ctx context.Context, beadID string, program *tea.Program) {
    m.mu.Lock()
    defer m.mu.Unlock()

    ctx, cancel := context.WithCancel(ctx)
    m.sessions[beadID] = &monitoredSession{cancel: cancel}

    m.wg.Add(1)
    go func() {
        defer m.wg.Done()
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                output := m.capturePane(beadID)
                state := DetectState(output)
                program.Send(sessionStateMsg{beadID, state})
            }
        }
    }()
}
```

### Confirm Dialog

```go
type ConfirmDialog struct {
    title   string
    message string
    onYes   tea.Cmd
    onNo    tea.Cmd
}

func (d ConfirmDialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    if key, ok := msg.(tea.KeyMsg); ok {
        switch key.String() {
        case "y", "Y":
            return d, d.onYes
        case "n", "N", "esc":
            return d, d.onNo
        }
    }
    return d, nil
}
```

## Files to Create

```
internal/services/tmux/client.go
internal/services/tmux/session.go
internal/services/tmux/bindings.go
internal/services/git/worktree.go
internal/services/claude/session.go
internal/services/devserver/manager.go
internal/services/devserver/ports.go
internal/services/monitor/session.go
internal/services/monitor/patterns.go
internal/ui/overlay/confirm.go
internal/ui/overlay/devserver.go
```

## Definition of Done

- [ ] All deliverables implemented
- [ ] All acceptance criteria pass
- [ ] Unit tests for state detection
- [ ] Unit tests for port allocation
- [ ] Integration tests with mock commands
- [ ] Manual testing of full lifecycle
- [ ] Code reviewed and merged
