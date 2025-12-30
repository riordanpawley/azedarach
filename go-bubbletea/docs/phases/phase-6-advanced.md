# Phase 6: Advanced Features

**Goal**: Full feature parity with TypeScript implementation

**Status**: 🔲 Not Started

## Deliverables

### Epic Drill-Down
- [ ] Enter on epic shows only children
- [ ] Progress bar (closed/total)
- [ ] Back navigation (`q`/`Esc`)

### Jump Labels
- [ ] Jump labels mode (`g` `w`)
- [ ] 2-char labels from home row
- [ ] Type label to jump

### Multi-Project Support
- [ ] Project selector overlay (`g` `p`)
- [ ] Project auto-detection from cwd
- [ ] CLI: `az project add/list/remove/switch`

### Create/Edit Beads
- [ ] Manual create (`c`) via $EDITOR
- [ ] Claude create (`C`) with prompt overlay
- [ ] Manual edit (`Space` `e`)
- [ ] Claude edit (`Space` `E`)

### External Editors
- [ ] Open Helix editor (`Space` `H`)
- [ ] Chat with Haiku (`Space` `c`)

### Image Attachments
- [ ] Attach overlay (`Space` `i`)
- [ ] Paste from clipboard (`p`/`v`)
- [ ] Attach from file path (`f`)
- [ ] Preview in terminal
- [ ] Open in external viewer (`o`)
- [ ] Navigate/delete in detail panel

### Detail Panel
- [ ] Scrollable description
- [ ] Attachment list
- [ ] Edit actions

### Settings & Diagnostics
- [ ] Settings overlay (`s`) with all toggleable settings
- [ ] Edit in $EDITOR option
- [ ] Diagnostics overlay
- [ ] Logs viewer (`L`)
- [ ] Planning workflow integration

## Acceptance Criteria

- [ ] Epic drill-down filters to children only
- [ ] Jump labels work across visible tasks
- [ ] Project switch refreshes board
- [ ] Image preview works in major terminals
- [ ] Settings persist to `.azedarach.json`

## Dependencies

- All previous phases (1-5)

## Testing Strategy

| Type | Scope |
|------|-------|
| Unit | Jump label generation, image detection |
| Golden | Epic header, settings overlay |
| Manual | Full workflow testing |

## Key Implementation Notes

### Jump Labels

```go
var homeRow = []rune("asdfghjkl;")

func GenerateLabels(count int) []string {
    labels := make([]string, 0, count)

    // Single char first
    for _, c := range homeRow {
        if len(labels) >= count {
            break
        }
        labels = append(labels, string(c))
    }

    // Then double char
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

type JumpMode struct {
    labels   map[string]int  // label -> task index
    input    string          // accumulated input
    taskPos  []image.Point   // screen positions
}
```

### Epic Drill-Down

```go
type EpicDrillDown struct {
    epic     *Task
    children []Task
    cursor   int
}

func (e EpicDrillDown) View() string {
    // Progress bar
    closed := 0
    for _, c := range e.children {
        if c.Status == StatusDone {
            closed++
        }
    }
    progress := float64(closed) / float64(len(e.children))

    header := fmt.Sprintf("📦 %s [%d/%d] %.0f%%\n",
        e.epic.Title, closed, len(e.children), progress*100)

    // Render children as mini-board
    return header + renderChildList(e.children, e.cursor)
}
```

### Image Terminal Rendering

```go
import "github.com/charmbracelet/x/term"

func RenderImage(path string, width, height int) string {
    info := term.GetTerminalInfo()

    switch {
    case info.KittyGraphics:
        return renderKittyImage(path, width, height)
    case info.ITerm2:
        return renderITerm2Image(path, width, height)
    default:
        return renderBlocksImage(path, width, height)
    }
}
```

### Clipboard Image

```go
func ReadImageFromClipboard() ([]byte, error) {
    switch runtime.GOOS {
    case "darwin":
        return exec.Command("osascript", "-e",
            `set png to (the clipboard as «class PNGf»)
             return png`).Output()
    case "linux":
        if out, err := exec.Command("wl-paste", "-t", "image/png").Output(); err == nil {
            return out, nil
        }
        return exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
    }
    return nil, fmt.Errorf("unsupported platform")
}
```

### Multi-Project

```go
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

    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return &ProjectsRegistry{}, nil
        }
        return nil, err
    }

    var reg ProjectsRegistry
    if err := json.Unmarshal(data, &reg); err != nil {
        return nil, err
    }
    return &reg, nil
}
```

### Settings Overlay

```go
type SettingsOverlay struct {
    config *Config
    cursor int
    items  []SettingItem
}

type SettingItem struct {
    Key     string
    Label   string
    Type    SettingType  // Toggle, Choice, Input
    Value   any
    OnChange func(any)
}

func (s SettingsOverlay) View() string {
    var b strings.Builder
    b.WriteString("Settings\n\n")

    for i, item := range s.items {
        prefix := "  "
        if i == s.cursor {
            prefix = "> "
        }

        switch item.Type {
        case SettingToggle:
            checked := "[ ]"
            if item.Value.(bool) {
                checked = "[x]"
            }
            b.WriteString(fmt.Sprintf("%s%s %s\n", prefix, checked, item.Label))
        case SettingChoice:
            b.WriteString(fmt.Sprintf("%s%s: %v\n", prefix, item.Label, item.Value))
        }
    }

    return b.String()
}
```

## Files to Create

```
internal/ui/overlay/epic.go
internal/ui/overlay/jump.go
internal/ui/overlay/project.go
internal/ui/overlay/settings.go
internal/ui/overlay/detail.go
internal/ui/overlay/diagnostics.go
internal/ui/overlay/logs.go
internal/ui/overlay/planning.go
internal/services/clipboard/clipboard.go
internal/services/image/attach.go
internal/services/image/render.go
internal/config/projects.go
```

## Definition of Done

- [ ] All deliverables implemented
- [ ] All acceptance criteria pass
- [ ] Unit tests for jump labels
- [ ] Unit tests for image detection
- [ ] Golden tests for overlays
- [ ] Manual testing across terminals
- [ ] Full feature parity verified
- [ ] Code reviewed and merged
