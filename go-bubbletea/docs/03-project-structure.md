# Project Structure

## Directory Layout

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
