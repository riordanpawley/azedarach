# Azedarach Configuration System

Comprehensive configuration system for the Go Bubbletea Azedarach rewrite.

## Features

- **Hierarchical Configuration Loading**: Loads from multiple sources with priority
- **Sensible Defaults**: All fields have reasonable default values
- **Flexible Storage**: Supports `.azedarach.json` or `package.json` "azedarach" key
- **Type Safety**: Strongly typed configuration with Go structs
- **Easy Merging**: Automatically merges partial configs with defaults

## Configuration Loading Priority

1. CLI flags (not implemented yet)
2. `.azedarach.json` in project root
3. `package.json` "azedarach" key
4. Built-in defaults

## Usage

### Load Configuration

```go
import "github.com/riordanpawley/azedarach/internal/config"

// Load from current directory
cfg, err := config.Load()

// Load from specific path
cfg, err := config.LoadConfig("/path/to/project")
```

### Access Configuration Values

```go
// CLI tool to use
cliTool := cfg.CLITool  // "claude" or "opencode"

// Git settings
baseBranch := cfg.Git.BaseBranch
workflowMode := cfg.Git.WorkflowMode

// Session settings
shell := cfg.Session.Shell
timeout := cfg.Session.TimeoutMs

// Dev server settings
port := cfg.DevServer.BasePort
envVars := cfg.DevServer.Environments
```

### Save Configuration

```go
cfg := config.DefaultConfig()
cfg.CLITool = "opencode"
cfg.Git.BaseBranch = "develop"

err := config.SaveConfig(cfg, "/path/to/.azedarach.json")
```

### Create Custom Configuration

```go
cfg := config.DefaultConfig()
cfg.Git.BaseBranch = "develop"
cfg.Session.Shell = "bash"
cfg.DevServer.BasePort = 4000

// Missing fields will be filled with defaults
cfg = config.MergeWithDefaults(cfg)
```

## Configuration Structure

### Main Config

```go
type Config struct {
    CLITool       string          // "claude" or "opencode"
    Git           GitConfig
    Session       SessionConfig
    PR            PRConfig
    Merge         MergeConfig
    Notifications NotifyConfig
    Beads         BeadsConfig
    Network       NetworkConfig
    DevServer     DevServerConfig
    Worktree      WorktreeConfig
}
```

### Git Config

```go
type GitConfig struct {
    BaseBranch           string  // default: "main"
    WorkflowMode         string  // "branch" or "worktree"
    ShowLineChanges      bool
    DefaultMergeStrategy string  // "merge", "rebase", or "squash"
}
```

### Session Config

```go
type SessionConfig struct {
    Shell        string    // default: "zsh"
    TimeoutMs    int       // default: 30000
    LogDir       string    // default: "~/.azedarach/logs"
    InitCommands []string  // commands to run on session start
}
```

### Dev Server Config

```go
type DevServerConfig struct {
    BasePort     int                  // default: 3000
    MaxPort      int                  // default: 3100
    Environments map[string]string    // env vars for dev server
}
```

### Worktree Config

```go
type WorktreeConfig struct {
    BasePath    string  // default: "../"
    NameFormat  string  // default: "{project}-{beadID}"
    AutoCleanup bool
    KeepDays    int     // days to keep old worktrees
}
```

## Configuration Files

### .azedarach.json

Create a `.azedarach.json` file in your project root:

```json
{
  "cliTool": "claude",
  "git": {
    "baseBranch": "main",
    "workflowMode": "worktree"
  },
  "session": {
    "shell": "zsh",
    "timeoutMs": 30000
  },
  "devServer": {
    "basePort": 3000,
    "environments": {
      "NODE_ENV": "development"
    }
  }
}
```

### package.json

Or add an "azedarach" key to your `package.json`:

```json
{
  "name": "my-project",
  "version": "1.0.0",
  "azedarach": {
    "cliTool": "claude",
    "git": {
      "baseBranch": "develop"
    }
  }
}
```

## Defaults

All configuration fields have sensible defaults:

- **CLI Tool**: `claude`
- **Git Base Branch**: `main`
- **Workflow Mode**: `worktree`
- **Shell**: `zsh`
- **Timeout**: `30000ms` (30 seconds)
- **Dev Server Port**: `3000`
- **Beads Path**: `.beads`
- **Worktree Path**: `../`
- **Worktree Format**: `{project}-{beadID}`

See `.azedarach.example.json` for a complete example configuration.

## Testing

The configuration system has comprehensive test coverage:

```bash
go test ./internal/config/...
```

Tests cover:
- Default configuration
- Loading from `.azedarach.json`
- Loading from `package.json`
- Configuration priority
- Saving configuration
- Merging with defaults
- Invalid JSON handling
- Complex configurations
