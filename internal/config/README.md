# Azedarach Configuration System

Comprehensive configuration system for the Go Bubbletea Azedarach rewrite.

## Features

- **Layered Configuration Loading**: Loads committed workspace config plus optional local overrides
- **Sensible Defaults**: All fields have reasonable default values
- **Flexible Storage**: Supports `.azedarach/config.json` and optional `.azedarach/config.local.json`
- **Type Safety**: Strongly typed configuration with Go structs
- **Easy Merging**: Automatically merges partial configs with defaults and local overrides

## Configuration Loading Priority

1. CLI flags (not implemented yet)
2. `.azedarach/config.local.json` in project root
3. `.azedarach/config.json` in project root
4. Built-in defaults

Objects are deep-merged between layers. Scalars and arrays replace the value
from the lower-priority layer.

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
cliTool := cfg.CLITool  // "claude", "opencode", or "codex"

// Git settings
baseBranch := cfg.Git.BaseBranch
workflowMode := cfg.Git.WorkflowMode

// Session settings
shell := cfg.Session.Shell
timeout := cfg.Session.TimeoutMs
skipPermissions := cfg.Session.DangerouslySkipPermissions

// Dev server settings
port := cfg.DevServer.BasePort
envVars := cfg.DevServer.Environments
```

### Save Configuration

```go
cfg := config.DefaultConfig()
cfg.CLITool = "opencode"
cfg.Git.BaseBranch = "develop"
cfg.Session.DangerouslySkipPermissions = true

err := config.SaveConfig(cfg, "/path/to/.azedarach/config.json")
```

### CLI Updates

The Go CLI exposes a focused config command for feature gating:

```bash
az config set spec.enabled false
```

This writes the project config to `.azedarach/config.json`.

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
    CLITool       string          // "claude", "opencode", or "codex"
    Git           GitConfig
    Session       SessionConfig
    PR            PRConfig
    Merge         MergeConfig
    Notifications NotifyConfig
    Linear         BeadsConfig
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
    DangerouslySkipPermissions bool   // default: false
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

### .azedarach/config.json

Create a committed `.azedarach/config.json` file in your project root:

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

### .azedarach/config.local.json

Create an optional local-only `.azedarach/config.local.json` file for machine or
developer-specific overrides. It is loaded after `.azedarach/config.json`:

```json
{
  "session": {
    "shell": "bash"
  },
  "devServer": {
    "environments": {
      "LOCAL_ONLY": "true"
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
- **Linear Path**: `.linear`
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
- Loading from `.azedarach/config.json`
- Loading from `.azedarach/config.local.json`
- Configuration priority
- Saving configuration
- Merging with defaults
- Invalid JSON handling
- Complex configurations
