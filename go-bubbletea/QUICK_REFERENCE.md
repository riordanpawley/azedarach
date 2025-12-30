# Go/Bubbletea Quick Reference

## Bubbletea Basics

### Minimal Program

```go
package main

import (
    "fmt"
    "os"

    tea "github.com/charmbracelet/bubbletea"
)

type model struct {
    cursor int
    items  []string
}

func (m model) Init() tea.Cmd {
    return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        switch msg.String() {
        case "q", "ctrl+c":
            return m, tea.Quit
        case "j", "down":
            m.cursor++
        case "k", "up":
            m.cursor--
        }
    }
    return m, nil
}

func (m model) View() string {
    s := "Items:\n\n"
    for i, item := range m.items {
        cursor := " "
        if i == m.cursor {
            cursor = ">"
        }
        s += fmt.Sprintf("%s %s\n", cursor, item)
    }
    return s
}

func main() {
    p := tea.NewProgram(model{items: []string{"one", "two", "three"}})
    if _, err := p.Run(); err != nil {
        fmt.Println("Error:", err)
        os.Exit(1)
    }
}
```

## Common Patterns

### Async Commands

```go
// Define a message type for async result
type dataLoadedMsg struct {
    data []string
    err  error
}

// Command function (runs async)
func loadData() tea.Msg {
    data, err := fetchFromAPI()
    return dataLoadedMsg{data: data, err: err}
}

// Trigger in Update
case tea.KeyMsg:
    if msg.String() == "r" {
        return m, loadData  // Returns tea.Cmd
    }

// Handle result
case dataLoadedMsg:
    if msg.err != nil {
        m.error = msg.err.Error()
    } else {
        m.data = msg.data
    }
    return m, nil
```

### Periodic Ticks

```go
type tickMsg time.Time

func tickEvery(d time.Duration) tea.Cmd {
    return tea.Tick(d, func(t time.Time) tea.Msg {
        return tickMsg(t)
    })
}

func (m model) Init() tea.Cmd {
    return tickEvery(2 * time.Second)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tickMsg:
        // Do periodic work
        return m, tickEvery(2 * time.Second)
    }
    return m, nil
}
```

### Batch Multiple Commands

```go
func (m model) Init() tea.Cmd {
    return tea.Batch(
        loadBeads,
        loadProjects,
        tickEvery(2 * time.Second),
    )
}
```

### Window Size

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.width = msg.Width
        m.height = msg.Height
        return m, nil
    }
    return m, nil
}
```

## Lip Gloss Styling

### Basic Styles

```go
import "github.com/charmbracelet/lipgloss"

var (
    // Colors
    subtle  = lipgloss.AdaptiveColor{Light: "#D9DCCF", Dark: "#383838"}
    special = lipgloss.Color("#FF06B7")

    // Styles
    titleStyle = lipgloss.NewStyle().
        Bold(true).
        Foreground(lipgloss.Color("#FAFAFA")).
        Background(lipgloss.Color("#7D56F4")).
        Padding(0, 1)

    cardStyle = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(subtle).
        Padding(1).
        Width(24)
)

// Use
title := titleStyle.Render("Hello")
card := cardStyle.Render(content)
```

### Layout

```go
// Horizontal join
row := lipgloss.JoinHorizontal(lipgloss.Top, col1, col2, col3)

// Vertical join
page := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)

// Place (absolute positioning)
lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
```

### Catppuccin Macchiato Theme

```go
var theme = struct {
    Base     lipgloss.Color
    Surface0 lipgloss.Color
    Surface1 lipgloss.Color
    Text     lipgloss.Color
    Subtext0 lipgloss.Color
    Blue     lipgloss.Color
    Green    lipgloss.Color
    Yellow   lipgloss.Color
    Red      lipgloss.Color
    Mauve    lipgloss.Color
}{
    Base:     lipgloss.Color("#24273a"),
    Surface0: lipgloss.Color("#363a4f"),
    Surface1: lipgloss.Color("#494d64"),
    Text:     lipgloss.Color("#cad3f5"),
    Subtext0: lipgloss.Color("#a5adcb"),
    Blue:     lipgloss.Color("#8aadf4"),
    Green:    lipgloss.Color("#a6da95"),
    Yellow:   lipgloss.Color("#eed49f"),
    Red:      lipgloss.Color("#ed8796"),
    Mauve:    lipgloss.Color("#c6a0f6"),
}
```

## Bubbles Components

### Text Input

```go
import "github.com/charmbracelet/bubbles/textinput"

type model struct {
    input textinput.Model
}

func newModel() model {
    ti := textinput.New()
    ti.Placeholder = "Search..."
    ti.CharLimit = 156
    ti.Width = 20
    return model{input: ti}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    var cmd tea.Cmd
    m.input, cmd = m.input.Update(msg)
    return m, cmd
}

func (m model) View() string {
    return m.input.View()
}
```

### Viewport (Scrolling)

```go
import "github.com/charmbracelet/bubbles/viewport"

type model struct {
    viewport viewport.Model
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.WindowSizeMsg:
        m.viewport.Width = msg.Width
        m.viewport.Height = msg.Height - 2  // Leave room for header/footer
    }
    var cmd tea.Cmd
    m.viewport, cmd = m.viewport.Update(msg)
    return m, cmd
}
```

### Spinner

```go
import "github.com/charmbracelet/bubbles/spinner"

type model struct {
    spinner  spinner.Model
    loading  bool
}

func newModel() model {
    s := spinner.New()
    s.Spinner = spinner.Dot
    s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
    return model{spinner: s, loading: true}
}

func (m model) Init() tea.Cmd {
    return m.spinner.Tick
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    if m.loading {
        var cmd tea.Cmd
        m.spinner, cmd = m.spinner.Update(msg)
        return m, cmd
    }
    return m, nil
}

func (m model) View() string {
    if m.loading {
        return m.spinner.View() + " Loading..."
    }
    return "Done!"
}
```

## Shell Commands

```go
import "os/exec"

func runCommand(name string, args ...string) (string, error) {
    cmd := exec.Command(name, args...)
    out, err := cmd.Output()
    return string(out), err
}

// Example: tmux
func tmuxCapture(session string) (string, error) {
    return runCommand("tmux", "capture-pane", "-t", session, "-p", "-S", "-50")
}

// Example: bd CLI
func beadsList() ([]Task, error) {
    out, err := runCommand("bd", "list", "--format=json")
    if err != nil {
        return nil, err
    }
    var tasks []Task
    if err := json.Unmarshal([]byte(out), &tasks); err != nil {
        return nil, err
    }
    return tasks, nil
}
```

## Key Handling

```go
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    // Check key string
    switch msg.String() {
    case "ctrl+c", "q":
        return m, tea.Quit
    case "j", "down":
        m.cursor++
    case "k", "up":
        m.cursor--
    case "enter":
        return m.select()
    case " ":  // Space
        return m.toggleAction()
    }

    // Or check key type
    switch msg.Type {
    case tea.KeyEsc:
        return m.cancel()
    case tea.KeyTab:
        return m.nextField()
    }

    return m, nil
}

// With modifiers
if msg.String() == "ctrl+r" {
    return m, refresh
}
if msg.Alt && msg.String() == "x" {
    // Alt+x pressed
}
```

## Debugging

```go
// Log to file (stdout is TUI)
f, _ := tea.LogToFile("debug.log", "debug")
defer f.Close()

// Then use log.Print/Printf
log.Printf("cursor: %d", m.cursor)
```

## Useful Links

- [Bubbletea Examples](https://github.com/charmbracelet/bubbletea/tree/master/examples)
- [Bubbles Source](https://github.com/charmbracelet/bubbles)
- [Lip Gloss Docs](https://pkg.go.dev/github.com/charmbracelet/lipgloss)
- [Building Bubbletea Programs](https://leg100.github.io/en/posts/building-bubbletea-programs/)
