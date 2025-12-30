# Phase 6 Advanced Features Wiring Summary

## Overview
Successfully wired Phase 6 advanced features into the app model for the Go Bubbletea Azedarach rewrite. All changes compile successfully and existing tests pass.

## Changes Made to `/internal/app/model.go`

### 1. Added Project Registry to Model (Line 125)
```go
// Project registry
projectRegistry *config.ProjectsRegistry
```

### 2. Updated New() Function to Load Project Registry (Lines 176-185)
```go
// Load project registry
registry, err := config.LoadProjectsRegistry()
if err != nil {
    logger.Error("failed to load project registry", "error", err)
    // Continue with empty registry
    registry = &config.ProjectsRegistry{
        Projects:       []config.Project{},
        DefaultProject: "",
    }
}
```

Added `projectRegistry: registry` to the Model initialization (line 208).

### 3. Added Message Handlers in Update() (Lines 408-469)

#### Jump Mode Handler
```go
case overlay.JumpSelectedMsg:
    // Close overlay
    m.overlayStack.Pop()

    // Jump to selected task by flattening all tasks and finding the index
    flatIndex := msg.TaskIndex
    columns := m.buildColumns()
    currentIndex := 0
    for colIdx, col := range columns {
        for taskIdx := range col.Tasks {
            if currentIndex == flatIndex {
                m.cursor.Column = colIdx
                m.cursor.Task = taskIdx
                return m, nil
            }
            currentIndex++
        }
    }
    return m, nil
```

#### Project Selector Handler
```go
case overlay.ProjectSelectedMsg:
    // Close overlay
    m.overlayStack.Pop()

    // Switch to selected project
    m.currentProject = msg.Project.Name
    m.toasts = append(m.toasts, Toast{
        Level:   ToastSuccess,
        Message: fmt.Sprintf("Switched to project: %s", msg.Project.Name),
        Expires: time.Now().Add(3 * time.Second),
    })

    // Reload beads for new project
    return m, m.loadBeadsCmd()
```

#### Task Creation Handler
```go
case overlay.TaskCreatedMsg:
    // Close overlay
    m.overlayStack.Pop()

    // Create task via beads client
    return m, m.createTaskCmd(msg)

case taskCreatedResultMsg:
    if msg.err != nil {
        m.toasts = append(m.toasts, Toast{
            Level:   ToastError,
            Message: fmt.Sprintf("Failed to create task: %v", msg.err),
            Expires: time.Now().Add(5 * time.Second),
        })
        return m, nil
    }

    m.toasts = append(m.toasts, Toast{
        Level:   ToastSuccess,
        Message: fmt.Sprintf("Task created: %s", msg.taskID),
        Expires: time.Now().Add(3 * time.Second),
    })

    // Reload beads to show new task
    return m, m.loadBeadsCmd()
```

### 4. Enhanced handleNormalMode() with New Keybindings (Lines 704-722)

#### Enter Key - View Details or Epic Drill-Down
```go
case "enter": // View task details or drill into epic
    task, session := m.getCurrentTaskAndSession()
    if task != nil {
        if m.isCurrentTaskEpic() {
            // Epic drill-down
            children := m.getEpicChildren(task.ID)
            return m, m.overlayStack.Push(overlay.NewEpicDrillDown(*task, children))
        } else {
            // Regular task detail panel
            return m, m.overlayStack.Push(overlay.NewDetailPanel(*task, session))
        }
    }
    return m, nil
```

#### c Key - Create Task
```go
case "c": // Create task
    return m, m.overlayStack.Push(overlay.NewCreateTaskOverlay())
```

#### s Key - Settings
```go
case "s": // Settings
    return m, m.overlayStack.Push(overlay.NewDefaultSettingsOverlay())
```

### 5. Extended handleGotoMode() with Jump and Project Shortcuts (Lines 754-763)

#### gw - Jump Mode
```go
case "w":
    // Jump mode - quick navigation with labels
    taskCount := 0
    for _, col := range columns {
        taskCount += len(col.Tasks)
    }
    return m, m.overlayStack.Push(overlay.NewJumpMode(taskCount))
```

#### gp - Project Selector
```go
case "p":
    // Project selector
    return m, m.overlayStack.Push(overlay.NewProjectSelector(m.projectRegistry))
```

### 6. Enhanced handleSelection() for Overlay Integration (Lines 1010-1066)

Added handlers for:
- **projects**: Settings → Manage projects navigation
- **editor-error/editor-closed**: Config editor state
- **select_child**: Epic drill-down child selection
- **set-default-success/remove-success/detect-success**: Project registry success toasts
- **set-default-error/remove-error/add-error/save-error/detect-error**: Project registry error toasts

### 7. Added Helper Methods (Lines 1375-1426)

#### isCurrentTaskEpic()
```go
// isCurrentTaskEpic returns true if the currently selected task is an epic
func (m Model) isCurrentTaskEpic() bool {
    task, _ := m.getCurrentTaskAndSession()
    if task == nil {
        return false
    }
    return task.Type == domain.TypeEpic
}
```

#### getEpicChildren()
```go
// getEpicChildren returns all tasks that are children of the given epic
func (m Model) getEpicChildren(epicID string) []domain.Task {
    var children []domain.Task
    for _, task := range m.tasks {
        if task.ParentID != nil && *task.ParentID == epicID {
            children = append(children, task)
        }
    }
    return children
}
```

#### createTaskCmd()
```go
type taskCreatedResultMsg struct {
    taskID string
    err    error
}

// createTaskCmd creates a new task via the beads client
func (m Model) createTaskCmd(msg overlay.TaskCreatedMsg) tea.Cmd {
    return func() tea.Msg {
        // TODO: Implement beads.Client.Create() method
        // For now, return a placeholder error message

        return taskCreatedResultMsg{
            err: fmt.Errorf("task creation not yet implemented - need to add Create() method to beads.Client"),
        }
    }
}
```

## Keybindings Summary

### Normal Mode
| Key | Action |
|-----|--------|
| `Enter` | View task details (or drill into epic if epic task) |
| `c` | Create new task |
| `s` | Open settings |
| `g` | Enter goto mode |

### Goto Mode
| Key | Action |
|-----|--------|
| `g` | Go to top of column |
| `e` | Go to end of column |
| `h` | Go to first column |
| `l` | Go to last column |
| `w` | Open jump mode (quick navigation) |
| `p` | Open project selector |

## Build and Test Results

### Build Status
```bash
$ go build ./...
✓ SUCCESS - All packages compile without errors
```

### Test Results
```bash
$ go test ./internal/app/... -v
=== RUN   TestHelperMethods
=== RUN   TestNormalModeNavigation
=== RUN   TestHalfPageScroll
=== RUN   TestGotoMode
=== RUN   TestModeTransitions
=== RUN   TestModeStrings
PASS
ok  	github.com/riordanpawley/azedarach/internal/app	0.022s

✓ All tests passing
```

## Known Limitations & Next Steps

### 1. Task Creation Not Yet Implemented
The `beadsClient.Create()` method doesn't exist yet. Current implementation returns a placeholder error.

**Required implementation:**
- Add `Create()` method to `/internal/services/beads/client.go`
- Expected signature:
  ```go
  func (c *Client) Create(ctx context.Context, params CreateTaskParams) (string, error)
  ```
- Should call `bd create --title="..." --type=... --priority=...`

### 2. Project Switching Not Fully Functional
While project switching is wired in, the beads client doesn't currently support project-specific operations. Future work:
- Pass current project context to beads commands
- Add project-specific beads filtering
- Handle multi-project worktree scenarios

### 3. Settings Persistence
Settings overlay works but changes aren't persisted to config file yet. Need to:
- Wire setting changes to config updates
- Add config save functionality
- Reload config on settings change

## Files Modified

- `/internal/app/model.go` - Main model file with all Phase 6 wiring

## Files Referenced (Dependencies)

- `/internal/config/projects.go` - Project registry implementation
- `/internal/ui/overlay/epic.go` - Epic drill-down overlay
- `/internal/ui/overlay/jump.go` - Jump mode overlay
- `/internal/ui/overlay/project.go` - Project selector overlay
- `/internal/ui/overlay/create.go` - Task creation overlay
- `/internal/ui/overlay/settings.go` - Settings overlay
- `/internal/ui/overlay/detail.go` - Task detail panel overlay
- `/internal/ui/overlay/overlay.go` - Base overlay types and messages
- `/internal/domain/task.go` - Task domain types

## Integration Points

All Phase 6 overlays are properly integrated:

1. ✅ **Epic Drill-Down** - Press `Enter` on epic tasks
2. ✅ **Detail Panel** - Press `Enter` on regular tasks
3. ✅ **Jump Mode** - Press `g` then `w`
4. ✅ **Project Selector** - Press `g` then `p`
5. ✅ **Create Task** - Press `c`
6. ✅ **Settings** - Press `s`

All message flows are properly wired with toast notifications and overlay state management.
