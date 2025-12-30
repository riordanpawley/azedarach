# Bubbletea-Specific Best Practices

> Patterns learned from [Glow](https://github.com/charmbracelet/glow), [Soft Serve](https://github.com/charmbracelet/soft-serve), and community best practices.

## Model Architecture: Nested Models Pattern

For non-trivial apps, use nested models with a **top-level router**:

```go
// internal/app/model.go

type Model struct {
    // Shared state accessible to all sub-models
    common *CommonModel

    // Sub-models (each implements tea.Model)
    board    *board.Model
    detail   *detail.Model
    settings *settings.Model
    overlays *overlay.Stack

    // Current state for routing
    state State
}

type CommonModel struct {
    config  *config.Config
    width   int
    height  int
    styles  *styles.Styles
    program *tea.Program // For sending messages from goroutines
}
```

**Key insight from Glow**: Share common state via pointer to avoid duplication across sub-models.

## Init: Batch Sub-Model Initialization

```go
func (m Model) Init() tea.Cmd {
    return tea.Batch(
        m.board.Init(),
        m.detail.Init(),
        m.settings.Init(),
        loadInitialData,  // Your custom init command
    )
}
```

## Update: Message Routing Pattern

**Pass ALL messages to relevant sub-models**, not just the "active" one:

```go
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    var cmds []tea.Cmd

    // Global handlers first (window size, quit, etc.)
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.common.width = msg.Width
        m.common.height = msg.Height
        // Propagate to all sub-models
        m.board.SetSize(msg.Width, msg.Height)
        m.detail.SetSize(msg.Width, msg.Height)
        return m, nil

    case tea.KeyMsg:
        // Handle quit regardless of state
        if msg.String() == "ctrl+c" {
            return m, tea.Quit
        }
    }

    // Route to active overlay first (if any)
    if m.overlays.Current() != nil {
        overlay, cmd := m.overlays.Current().Update(msg)
        m.overlays.SetCurrent(overlay)
        if cmd != nil {
            cmds = append(cmds, cmd)
        }
        // Overlays may consume the message
        if m.overlays.Current() != nil {
            return m, tea.Batch(cmds...)
        }
    }

    // Route to current view
    switch m.state {
    case StateBoard:
        newBoard, cmd := m.board.Update(msg)
        m.board = newBoard.(*board.Model)
        cmds = append(cmds, cmd)
    case StateDetail:
        newDetail, cmd := m.detail.Update(msg)
        m.detail = newDetail.(*detail.Model)
        cmds = append(cmds, cmd)
    }

    return m, tea.Batch(cmds...)
}
```

## View: Composable Rendering

```go
func (m Model) View() string {
    // Render base content
    var content string
    switch m.state {
    case StateBoard:
        content = m.board.View()
    case StateDetail:
        content = m.detail.View()
    }

    // Add status bar
    statusBar := m.renderStatusBar()
    content = lipgloss.JoinVertical(lipgloss.Left, content, statusBar)

    // Render overlay on top (if any)
    if overlay := m.overlays.Current(); overlay != nil {
        content = m.renderOverlayOn(content, overlay.View())
    }

    return content
}
```

## Commands: Async I/O Patterns

**Rule: Use commands for ALL I/O operations**

```go
// BAD: Blocking I/O in Update
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    tasks, _ := beads.List()  // ❌ Blocks the UI!
    m.tasks = tasks
    return m, nil
}

// GOOD: Async command pattern
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case refreshMsg:
        return m, loadBeadsCmd  // ✅ Non-blocking
    case beadsLoadedMsg:
        m.tasks = msg.tasks
        return m, nil
    }
    return m, nil
}

// Command with closure for arguments
func loadBeadsCmd(filter Filter) tea.Cmd {
    return func() tea.Msg {
        tasks, err := beads.List(filter)
        if err != nil {
            return beadsErrorMsg{err}
        }
        return beadsLoadedMsg{tasks}
    }
}
```

## tea.Batch vs tea.Sequence

```go
// tea.Batch: Run commands CONCURRENTLY (no ordering)
return m, tea.Batch(
    fetchBeads,
    fetchProjects,
    pollSessions,
)

// tea.Sequence: Run commands IN ORDER (each waits for previous)
return m, tea.Sequence(
    saveConfig,        // First: save
    reloadConfig,      // Then: reload
    showSuccessToast,  // Finally: notify
)
```

## Model Stack for Navigation

For wizard-like flows or drill-down navigation:

```go
type ModelStack struct {
    stack []tea.Model
}

func (s *ModelStack) Push(m tea.Model) tea.Cmd {
    s.stack = append(s.stack, m)
    return m.Init()
}

func (s *ModelStack) Pop() tea.Model {
    if len(s.stack) <= 1 {
        return nil  // Can't pop root
    }
    s.stack = s.stack[:len(s.stack)-1]
    return s.Current()
}

func (s *ModelStack) Current() tea.Model {
    return s.stack[len(s.stack)-1]
}

// Usage in Update:
case openDetailMsg:
    detailModel := detail.New(msg.task)
    return m, m.stack.Push(detailModel)

case tea.KeyMsg:
    if msg.String() == "esc" {
        m.stack.Pop()
        return m, nil
    }
```

## Subcomponent Communication

**Parent → Child**: Pass data via constructors or setter methods

```go
// Parent creates child with dependencies
m.board = board.New(m.common, m.tasks)

// Parent updates child state
m.board.SetTasks(newTasks)
m.board.SetFilter(filter)
```

**Child → Parent**: Return messages that parent handles

```go
// In child's Update:
case selectTaskMsg:
    return m, func() tea.Msg {
        return taskSelectedMsg{m.selectedTask}  // Parent handles this
    }

// In parent's Update:
case taskSelectedMsg:
    m.state = StateDetail
    m.detail.SetTask(msg.task)
```

## Layout Best Practices

**Golden Rule: Always account for borders**

```go
// Calculate available space for content
contentHeight := m.height - 2  // Status bar + header
columnWidth := (m.width - 4) / 4  // 4 columns with gaps

// When using borders, subtract 2 from BOTH dimensions
innerHeight := panelHeight - 2  // Top + bottom border
innerWidth := panelWidth - 2    // Left + right border
```

**Never auto-wrap in bordered panels**:

```go
// BAD: Text wraps unpredictably
style := lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    Width(40)

// GOOD: Explicit truncation
style := lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    Width(40).
    MaxWidth(40)

text := truncate(content, 38)  // Account for borders
```

## Key Handling Patterns

```go
// Pattern 1: String matching (simpler)
switch msg.String() {
case "q", "ctrl+c":
    return m, tea.Quit
case "j", "down":
    m.cursor++
case " ":  // Space
    return m, m.openActionMenu()
}

// Pattern 2: Type matching (more precise)
switch msg.Type {
case tea.KeyEsc:
    return m, m.closeOverlay()
case tea.KeyEnter:
    return m, m.selectItem()
case tea.KeyCtrlC:
    return m, tea.Quit
}

// Pattern 3: Key sequences (g followed by g)
if m.pendingKey == "g" {
    switch msg.String() {
    case "g":
        m.cursor = 0  // Go to top
    case "e":
        m.cursor = len(m.items) - 1  // Go to bottom
    }
    m.pendingKey = ""
    return m, nil
}
if msg.String() == "g" {
    m.pendingKey = "g"
    return m, nil
}
```

## Debugging

```go
// Log to file (can't use stdout - TUI is using it)
f, _ := tea.LogToFile("debug.log", "debug")
defer f.Close()

// Now use log.Print/Printf
log.Printf("state: %v, cursor: %d", m.state, m.cursor)
log.Printf("received msg: %T %v", msg, msg)
```

## Performance Tips

1. **Pre-compile regex patterns** (don't create in hot paths):
   ```go
   var statePatterns = map[State]*regexp.Regexp{
       StateWaiting: regexp.MustCompile(`\[y/n\]`),
   }
   ```

2. **Cache computed styles**:
   ```go
   type Styles struct {
       cardStyle       lipgloss.Style  // Computed once
       cardActiveStyle lipgloss.Style
   }
   ```

3. **Debounce expensive operations**:
   ```go
   case tea.KeyMsg:
       // Reset debounce timer on each keypress
       return m, tea.Tick(300*time.Millisecond, func(t time.Time) tea.Msg {
           return searchDebounceMsg{query: m.searchInput.Value()}
       })
   ```

4. **Lazy render visible items only**:
   ```go
   func (m Model) View() string {
       // Only render visible cards
       start := m.scrollOffset
       end := min(start + m.visibleCount, len(m.items))
       for _, item := range m.items[start:end] {
           // render...
       }
   }
   ```

## File Organization (Recommended)

```
internal/app/
├── model.go        # Model struct, New(), common methods
├── init.go         # Init() and startup commands
├── update.go       # Update() message router
├── update_keys.go  # Keyboard handling (split if large)
├── update_mouse.go # Mouse handling
├── view.go         # View() and rendering helpers
├── commands.go     # All tea.Cmd functions
├── messages.go     # All custom message types
└── helpers.go      # Utility functions
```

## Testing Bubbletea Apps

```go
// Use teatest for integration testing
func TestAppStartup(t *testing.T) {
    m := New(testConfig)
    tm := teatest.NewTestModel(t, m)

    // Simulate user input
    tm.Send(tea.KeyMsg{Type: tea.KeyDown})
    tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

    // Assert on final state
    finalModel := tm.FinalModel(t).(Model)
    assert.Equal(t, StateDetail, finalModel.state)
}

// Unit test Update directly
func TestUpdateNavigation(t *testing.T) {
    m := Model{cursor: 0, items: make([]Item, 5)}

    msg := tea.KeyMsg{Type: tea.KeyDown}
    newModel, _ := m.Update(msg)

    assert.Equal(t, 1, newModel.(Model).cursor)
}
```

## Clipboard Cross-Platform Strategy

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

## Image Terminal Rendering Strategy

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
