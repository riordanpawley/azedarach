# Azedarach Hooks Installation

> How Azedarach installs session state detection hooks into Claude Code

## Overview

Azedarach needs to know when Claude sessions change state (idle, waiting for permission, stopped, ended). It does this by installing hooks into `.claude/settings.local.json` that call `az notify`.

## Quick Start

```bash
# Install hooks for a specific bead
az hooks install <bead-id>

# Example
az hooks install az-4vp
```

This creates/updates `.claude/settings.local.json` with the required hooks.

## Automatic Installation (Worktrees)

When Azedarach creates a worktree via `WorktreeManager.create()`, it automatically:

1. Copies `.claude/settings.local.json` from the parent project (preserving user permissions)
2. Merges in session-specific hooks with the bead ID

**Source:** `src/core/hooks.ts` (shared hook generation) and `src/core/WorktreeManager.ts` (worktree setup)

## Hook Configuration

The following hooks are injected into `.claude/settings.local.json`:

```json
{
  "hooks": {
    "Notification": [
      {
        "matcher": "idle_prompt",
        "hooks": [
          {
            "type": "command",
            "command": "az notify idle_prompt <bead-id>"
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "az notify permission_request <bead-id>"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "az notify stop <bead-id>"
          }
        ]
      }
    ],
    "SessionEnd": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "az notify session_end <bead-id>"
          }
        ]
      }
    ]
  }
}
```

## What Each Hook Does

| Event | When it fires | What `az notify` does |
|-------|---------------|----------------------|
| `Notification` (idle_prompt) | Claude is waiting for user input at the prompt | Updates session state to "waiting" |
| `PermissionRequest` | Claude is waiting for permission approval | Updates session state to "waiting" |
| `Stop` | Claude session stops (Ctrl+C, completion, etc.) | Updates session state to "stopped" |
| `SessionEnd` | Claude session fully ends | Cleans up session, updates state to "ended" |

## CLI Commands

### `az hooks install <bead-id>`

Installs session state hooks for a specific bead into the current project.

```bash
# Basic usage
az hooks install az-4vp

# With verbose output (flags before positional args)
az hooks install --verbose az-4vp

# For a specific project directory
az hooks install az-4vp /path/to/project
```

**Note:** Flags like `--verbose` must come before positional arguments (this is @effect/cli convention).

**What it does:**
1. Creates `.claude/` directory if needed
2. Reads existing `.claude/settings.local.json` (if any)
3. Merges in hook configuration for the bead ID
4. Writes the merged settings

**Output:**
```
✓ Installed hooks for bead az-4vp
  File: /path/to/project/.claude/settings.local.json
  Events: idle_prompt, permission_request, stop, session_end
```

## How `az notify` Works

The `az notify` command sets a tmux session option (`@az_status`) that the `TmuxSessionMonitor` service in the main Azedarach TUI process polls. This enables real-time state updates.

```
Claude Code → hooks fire → az notify → sets tmux option → TmuxSessionMonitor → updates TUI
```

**Tmux session option:** `@az_status` on the `claude-<bead-id>` tmux session

## Requirements

- `az` must be in PATH (or use full path in hook commands)
- `.claude/settings.local.json` must be valid JSON
- Hooks are merged with existing settings, not replaced

## Troubleshooting

**"az: command not found"**
- Run `bun link` in the azedarach directory, or
- Use full path: `/path/to/azedarach/bin/az.ts notify ...`

**Hooks not firing**
- Verify `.claude/settings.local.json` is valid JSON
- Check Claude Code is using the correct project directory
- Test manually: `az notify idle_prompt test-id`

**Hooks for wrong bead ID**
- Re-run `az hooks install <correct-bead-id>` to update
- Or manually edit `.claude/settings.local.json`
