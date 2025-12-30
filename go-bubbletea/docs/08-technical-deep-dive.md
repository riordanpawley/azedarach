# Technical Deep Dive

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

---

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

---

## Optimization Strategies

1. **Lazy rendering**: Only render visible cards, not entire column
2. **Debounced refresh**: Don't re-fetch beads on every tick if nothing changed
3. **Cached styles**: Pre-compute Lip Gloss styles, don't recreate per render
4. **Parallel I/O**: Fetch beads and session states concurrently
5. **Incremental updates**: Only update changed cards, not full board

---

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

---

## Testing Strategy

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
