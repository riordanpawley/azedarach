# Azedarach User Guide

> TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

## Table of Contents

1. [Quick Start](#quick-start)
2. [Keyboard Navigation](#keyboard-navigation)
3. [Modes](#modes)
4. [Features](#features)
   - [Creating Tasks](#creating-tasks)
   - [Detail Panel](#detail-panel)
   - [Task Movement](#task-movement)
   - [VC Integration](#vc-integration-vibecoder)
   - [Session Attachment](#session-attachment-requires-active-sessions)
5. [Testing Features](#testing-features)
6. [Troubleshooting](#troubleshooting)

## Additional Guides

| Guide | Description |
|-------|-------------|
| [Keybindings Reference](keybindings.md) | Complete keyboard shortcut reference |
| [Hooks Installation](hooks-installation.md) | How az installs session state hooks |
| [tmux Guide](tmux-guide.md) | **New to tmux?** Start here! |
| [tmux Config](tmux-config.md) | Recommended tmux configuration + cheatsheet |
| [Services Architecture](services.md) | Effect services and architecture |
| [Testing Guide](testing.md) | How to test each feature |

---

## Quick Start

```bash
# Start the TUI
bun run dev

# Or run directly
bun run bin/az.ts
```

The TUI displays a Kanban board with your beads issues organized by status:
- **Open** - Tasks ready to start
- **In Progress** - Active work
- **Blocked** - Waiting on dependencies
- **Closed** - Completed tasks

---

## Keyboard Navigation

Azedarach uses **Helix-style modal keybindings** for efficient navigation.

### Basic Navigation (Normal Mode)

| Key | Action |
|-----|--------|
| `h` / `←` | Move to previous column |
| `l` / `→` | Move to next column |
| `j` / `↓` | Move to next task in column |
| `k` / `↑` | Move to previous task in column |
| `Ctrl-Shift-d` | Half-page down |
| `Ctrl-Shift-u` | Half-page up |

### Task Actions

| Key | Action |
|-----|--------|
| `Enter` | Show detail panel for selected task |
| `c` | Create new task (opens modal prompt) |
| `a` | Toggle VC auto-pilot (start/stop VC executor) |
| `/` | Enter Search mode (filter tasks) |
| `:` | Enter Command mode (send commands to VC REPL) |
| `Space` | Enter Action mode (then press action key) |
| `?` | Show help overlay |
| `q` | Quit application |
| `Esc` | Return to Normal mode / dismiss overlay |

### Goto Mode (press `g` first)

| Key Sequence | Action |
|--------------|--------|
| `g` `g` | Jump to first task |
| `g` `e` | Jump to last task |
| `g` `h` | Jump to first task in column |
| `g` `l` | Jump to last task in column |
| `g` `w` | Show jump labels (2-char codes) |

### Select Mode (press `v` first)

| Key | Action |
|-----|--------|
| `v` | Enter select mode |
| `Space` | Toggle selection on current task |
| `h/j/k/l` | Navigate (selections persist) |
| `Esc` | Clear selections, return to Normal |

### Action Mode (press `Space` first)

| Key Sequence | Action |
|--------------|--------|
| `Space` `h` | Move selected task(s) to previous column |
| `Space` `l` | Move selected task(s) to next column |
| `Space` `a` | Attach to session externally (new terminal) |
| `Space` `A` | Attach to session inline (not yet implemented) |

---

## Modes

The status bar shows the current mode:

| Mode | Indicator | Description |
|------|-----------|-------------|
| Normal | `NOR` | Default navigation mode |
| Select | `SEL` | Multi-selection mode |
| Goto | `GTO` | Jump/goto prefix mode |
| Action | `ACT` | Command/action mode |

---

## Features

### Creating Tasks

Press `c` in Normal mode to open the task creation prompt:

1. **Title Field**: Type your task title
   - Use `Backspace` to delete characters
   - Press `Tab` to move to next field

2. **Type Selector**: Choose task type (task, bug, feature, epic, chore)
   - Use `h`/`l` or arrow keys to cycle through options
   - Press `Tab` to move to next field

3. **Priority Selector**: Choose priority (P1-P4)
   - Use `h`/`l` or arrow keys to cycle through options
   - Press `Tab` to return to title field

4. **Submit**: Press `Enter` to create the task
5. **Cancel**: Press `Esc` to cancel

The newly created task will appear in the "Open" column and a success toast will confirm creation.

### Detail Panel

Press `Enter` on any task to see full details:
- Title, ID, type, priority
- Description and design notes
- Session state and timestamps
- Available actions for current state

Press `Enter` or `Esc` to dismiss.

### Virtual Scrolling

Columns automatically scroll when tasks exceed terminal height. Scroll indicators (`▲`/`▼`) show when more content exists above/below.

### Task Movement

Use Action mode (`Space` + `h`/`l`) to move tasks between columns:
- Moving to "In Progress" starts work on a task
- Moving to "Closed" completes a task
- Changes are immediately synced to beads

### VC Integration (VibeCoder)

Azedarach integrates with [steveyegge/vc](https://github.com/steveyegge/vc) - an AI-supervised orchestration engine that autonomously executes tasks from your beads backlog.

#### What is VC?

**VC (VibeCoder)** is an AI orchestration layer that sits above Claude Code. While Claude Code executes individual coding tasks, VC provides:

- **AI Supervisor (Claude Sonnet)**: Strategic planning, task decomposition, and quality assessment
- **Quality Gates**: Automatic test, lint, and build verification after each task
- **Blocker-Aware Scheduling**: Only works on tasks with satisfied dependencies
- **Conversational Interface**: REPL for natural language commands

#### Architecture: az + VC

```
┌─────────────────────────────────────────────────────────────────┐
│                          You (Human)                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌─────────────┐         ┌─────────────┐         ┌───────────┐  │
│  │ Azedarach   │◄───────►│  VC         │◄───────►│ Claude    │  │
│  │ (TUI)       │         │ (Orchestrator)        │ Code      │  │
│  │             │         │             │         │ (Executor)│  │
│  │ - Visualize │         │ - Schedule  │         │ - Code    │  │
│  │ - Navigate  │         │ - Gate      │         │ - Git     │  │
│  │ - Attach    │         │ - Supervise │         │ - Shell   │  │
│  └──────┬──────┘         └──────┬──────┘         └───────────┘  │
│         │                       │                                │
│         └───────────┬───────────┘                                │
│                     ▼                                            │
│            ┌─────────────────┐                                   │
│            │  .beads/beads.db │  ◄── Shared SQLite database      │
│            │  (Beads Issues)  │                                   │
│            └─────────────────┘                                   │
└─────────────────────────────────────────────────────────────────┘
```

**Key insight:** Both az and VC read/write the same `.beads/beads.db` database. This means:
- Changes you make in az (moving tasks, changing priority) are immediately visible to VC
- Tasks VC completes or updates appear in az in real-time
- No sync conflicts - SQLite handles concurrent access

#### When to Use Each

| Scenario | Use |
|----------|-----|
| Manual coding session | az: `Space+s` to start, `Space+a` to attach |
| Autonomous batch work | VC: Press `a` to start auto-pilot |
| Monitor both | az for visualization, VC for execution |
| Quick question to VC | az: Press `:` then type your question |

#### Basic Workflow

1. **Start VC auto-pilot**: Press `a` to launch VC in a background tmux session
2. **Monitor status**: StatusBar shows `VC: running` (green) or `VC: stopped` (yellow)
3. **Send commands**: Press `:` to send natural language commands to VC's REPL
4. **Watch progress**: VC claims issues, executes work, and updates status in real-time
5. **Stop when done**: Press `a` again to toggle off

#### Command Mode Examples

Press `:` to enter command mode, then type:

| Command | What it does |
|---------|--------------|
| `What's ready to work on?` | Query available tasks with no blockers |
| `Let's continue working` | Resume autonomous execution |
| `Add Docker support` | Create and work on a new feature |
| `Run tests` | Execute quality gates |
| `Pause` | Stop after current task completes |
| `Status` | Show what VC is currently doing |

#### Installation

VC must be installed separately:

```bash
# macOS (Homebrew)
brew tap steveyegge/vc
brew install vc

# Verify installation
vc --version
```

See [VC documentation](https://github.com/steveyegge/vc) for full setup instructions.

### Session Attachment (Requires Active Sessions)

**Important:** Session attachment only works when there are active tmux sessions running Claude Code.

To test attachment:
1. Start a Claude session in a tmux session named `claude-{bead-id}`:
   ```bash
   tmux new-session -d -s claude-az-05y "claude"
   ```
2. In Azedarach, navigate to the corresponding task
3. Press `Space` then `a` to attach in a new terminal window

---

## Testing Features

### Testing the TUI

```bash
# Start the application
bun run dev

# Navigate with hjkl
# Press ? for help
# Press Enter on a task for details
# Press Space+h or Space+l to move tasks
```

### Testing Session Attachment

Session attachment requires actual tmux sessions. Here's how to test:

```bash
# 1. Create a test tmux session
tmux new-session -d -s claude-test-session "bash"

# 2. In another terminal, start Azedarach
bun run dev

# 3. Create a test bead with matching ID (or use existing)
bd create --title="Test attachment" --type=task

# 4. Note the bead ID (e.g., az-xyz)

# 5. Rename your tmux session to match
tmux rename-session -t claude-test-session claude-az-xyz

# 6. In Azedarach, navigate to that task and press Space+a
```

### Testing Without Tmux

If you don't have tmux sessions running, you'll see errors in the console (not in the TUI). The attachment feature gracefully fails when:
- No tmux session exists with the expected name
- Terminal detection fails
- Terminal command execution fails

### Current Limitations

1. **Session Management Not Integrated**: The SessionManager service exists but isn't wired into the TUI's action commands yet. The `az-stv` task covers this integration.

2. **No Visual Error Feedback**: Attachment errors go to console.error, not to the TUI. Future work will add toast notifications.

3. **Inline Attachment Stubbed**: `Space+A` will show an error message - inline attachment is reserved for future implementation.

---

## Troubleshooting

### "Nothing happens when I press Space+a"

**Cause:** No tmux session exists with the name `claude-{task-id}`

**Solution:**
1. Check the console for error messages
2. Create a tmux session first (see Testing Session Attachment above)
3. Or wait for full SessionManager integration in `az-stv`

### "I can't see the help overlay"

**Solution:** Press `?` in Normal mode. Press any key to dismiss.

### "Tasks aren't moving between columns"

**Cause:** Beads sync issue or permission problem

**Solution:**
1. Check that `.beads/` directory exists
2. Verify `bd list` works from command line
3. Check console for error messages

### "The TUI looks corrupted"

**Cause:** OpenTUI rendering issues with certain terminal configurations

**Solution:**
1. Try resizing your terminal
2. Restart the application
3. Ensure your terminal supports true color

---

## Architecture Overview

```
src/
├── ui/                 # TUI components (OpenTUI + React)
│   ├── App.tsx         # Root component with modal keybindings
│   ├── Board.tsx       # Kanban board layout
│   ├── Column.tsx      # Status column with virtual scroll
│   ├── TaskCard.tsx    # Individual task card
│   ├── StatusBar.tsx   # Bottom status bar
│   ├── DetailPanel.tsx # Task detail overlay
│   └── HelpOverlay.tsx # Keyboard help
│
├── core/               # Effect services
│   ├── BeadsClient.ts      # bd CLI wrapper
│   ├── SessionManager.ts   # Claude session orchestration
│   ├── TmuxService.ts      # tmux operations
│   ├── TerminalService.ts  # Terminal detection
│   ├── AttachmentService.ts # Session attachment
│   ├── FileLockManager.ts  # Concurrent file locking
│   ├── WorktreeManager.ts  # Git worktree lifecycle
│   └── StateDetector.ts    # Claude output patterns
│
└── lib/                # Utilities
    └── effect-atom-react/  # React adapter for effect-atom
```

---

## Next Steps

See the [beads issues](../.beads/) for planned features:
- `az-stv`: Full task actions (start/attach/pause/resume)
- `az-l7a`: Agent Mail for multi-agent coordination
- `az-8ep`: Beads sync coordination for worktrees
