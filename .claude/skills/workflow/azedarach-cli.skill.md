# Azedarach CLI Skill

**Version:** 1.0
**Purpose:** CLI commands for AI agents running in Azedarach-managed worktrees

## Overview

When running in an Azedarach worktree, you have access to the `az` CLI for controlling dev servers and interacting with the orchestrating TUI. These commands sync state via tmux metadata so the TUI stays consistent.

## Detecting Azedarach Worktree

You're in an Azedarach worktree if:
- Branch name matches a bead ID pattern (e.g., `az-29ey`)
- Parent directory name ends with `-<bead-id>` (e.g., `project-az-29ey`)

## Dev Server Commands

Control dev servers without breaking TUI state tracking:

| Command | Description |
|---------|-------------|
| `az dev start <bead-id>` | Start the dev server for a bead |
| `az dev stop <bead-id>` | Stop the dev server |
| `az dev restart <bead-id>` | Stop + start (preserves config) |
| `az dev status <bead-id>` | Show server status |
| `az dev list` | List all running dev servers |

### Options

- `--server=<name>` - Target a specific server (default: "default")
- `--json` - Output as JSON for parsing

### Examples

```bash
# Start the dev server for current bead
az dev start az-29ey

# Check if server is running
az dev status az-29ey --json

# Restart after config changes
az dev restart az-29ey

# List all running servers across beads
az dev list
```

## Why Use az CLI Instead of Direct Commands?

**Problem**: If you manually run `npm run dev` or `ctrl-c` the process, Azedarach's TUI loses track of server state.

**Solution**: Use `az dev` commands which update tmux metadata. The TUI discovers state changes on its next poll.

## Getting Your Bead ID

The bead ID is typically your branch name:

```bash
git rev-parse --abbrev-ref HEAD
# Output: az-29ey
```

Or extract from directory name:

```bash
basename "$(pwd)" | sed 's/.*-//'
# Output: az-29ey
```

## Session Management

The Azedarach TUI monitors your tmux session. Key patterns it detects:

| Pattern | TUI Shows |
|---------|-----------|
| `[Y/n]` prompts | "waiting" state |
| Task completion messages | "done" state |
| Error/Exception output | "error" state |

Write clear status messages to help the TUI track your progress.

## Workflow Integration

When working in an Azedarach session:

1. **Start work**: TUI spawned your session, you're ready to go
2. **Dev servers**: Use `az dev start/stop/restart` for server control
3. **Complete work**: Clean exit triggers TUI's completion workflow (PR creation, etc.)
4. **Beads sync**: Run `bd sync` before finishing to persist task state

## Troubleshooting

### Server state out of sync

```bash
# Force TUI to re-poll by restarting
az dev restart <bead-id>
```

### Find your bead ID

```bash
# From branch
git branch --show-current

# From tmux session name
echo $TMUX | grep -oE 'az-[a-z0-9]+'
```
