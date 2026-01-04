# Azedarach

> A TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

Named after the [bead tree](https://en.wikipedia.org/wiki/Melia_azedarach) (Melia azedarach), whose seeds have been used for prayer beads for millennia.

## Overview

Azedarach is a terminal-based Kanban board that:
- Displays tasks from any Beads-enabled project
- Spawns Claude Code sessions in isolated git worktrees
- Enables full parallelization of development work
- Monitors session state (busy/waiting/done/error)
- Auto-creates GitHub PRs when tasks complete
- Allows manual intervention via terminal attachment

The key insight: **Claude Code already handles all the hard parts** (permissions, tools, context, hooks). Azedarach is purely an orchestration layer that spawns Claude in the right place and monitors progress.

## Goals

1. **Parallel execution**: Work on multiple tasks simultaneously across isolated worktrees
2. **Minimal friction**: Start a task with a single keypress
3. **Full visibility**: See status of all running Claude sessions at a glance
4. **Easy intervention**: Attach to any session for manual fixes
5. **Automated workflow**: Sync beads, create PRs, notify on completion
6. **Zero Claude config**: 100% inherit project's Claude configuration

## Non-Goals

- Managing Claude permissions (project's `.claude/settings.json` handles this)
- Implementing custom Claude tools (project's MCP/skills handle this)
- Replacing beads CLI (we wrap it, not replace it)
- IDE integration (this is terminal-native)

---

## Implementations

This repository contains multiple implementations of Azedarach, each exploring different technology stacks and approaches:

### 🚀 ts-opentui/ (Primary, Active Development)

**Tech Stack:** TypeScript, Bun, OpenTUI, Effect, React

**Status:** Active development, most features implemented

**Key Features:**
- React-based UI with OpenTUI rendering
- Effect-based service architecture
- Modal keybindings (Helix-editor style)
- Full session management with tmux

**Documentation:** See [ts-opentui/CLAUDE.md](./ts-opentui/CLAUDE.md)

**Quick Start:**
```bash
cd ts-opentui
bun run dev              # Start development TUI
bun run type-check       # Full project check
bun run build            # Build the project
```

---

### 🧊 go-bubbletea/ (Alternative Implementation)

**Tech Stack:** Go, Bubbletea, Lip Gloss

**Status:** Implemented, alternative implementation

**Key Features:**
- Elm Architecture (Model-Update-View)
- Bubbletea for terminal UI
- Lip Gloss for styling
- Bubbles for UI components

**Documentation:** See [go-bubbletea/CLAUDE.md](./go-bubbletea/CLAUDE.md)

**Quick Start:**
```bash
cd go-bubbletea
make build              # Build Go binary
make run                # Build and run
make test               # Run tests
```

---

### 🧪 gleam/ (Experimental)

**Tech Stack:** Gleam (Beam/Erlang VM)

**Status:** Experimental, not actively developed

**Note:** For exploration purposes only

---

## Architecture (Overview)

The architecture is shared across implementations:

```
┌─────────────────────────────────────────────────────────────────────┐
│                      Azedarach TUI (Implementation-Specific)       │
│                                                                     │
│  ┌─────────┐ ┌─────────────┐ ┌─────────┐ ┌────────┐ ┌────────┐    │
│  │  open   │ │ in_progress │ │ blocked │ │ review │ │ closed │    │
│  ├─────────┤ ├─────────────┤ ├─────────┤ ├────────┤ ├────────┤    │
│  │ CHE-101 │ │ CHE-102 🔵  │ │ CHE-105 │ │CHE-103 │ │CHE-100 │    │
│  │ CHE-104 │ │ CHE-106 🟡  │ │         │ │   ✅   │ │        │    │
│  │         │ │ CHE-107 🔵  │ │         │ │        │ │        │    │
│  └─────────┘ └─────────────┘ └─────────┘ └────────┘ └────────┘    │
│                                                                     │
│  Status: 🔵 Busy  🟡 Waiting  ✅ Done  ❌ Error  ⏸️  Paused        │
│                                                                     │
│  [Enter] Start  [a] Attach  [p] Pause  [d] Diff  [P] PR  [q] Quit  │
└─────────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        Session Manager                              │
│                                                                     │
│  Responsibilities:                                                  │
│  - Create/destroy git worktrees                                     │
│  - Spawn/manage tmux sessions                                       │
│  - Monitor Claude output for state changes                          │
│  - Execute hooks on state transitions                               │
│  - Coordinate with beads via `bd` CLI                               │
└─────────────────────────────────────────────────────────────────────┘
                                  │
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│ tmux: che-102    │  │ tmux: che-106    │  │ tmux: che-107    │
│                  │  │                  │  │                  │
│ Worktree:        │  │ Worktree:        │  │ Worktree:        │
│ ../Proj-che-102  │  │ ../Proj-che-106  │  │ ../Proj-che-107  │
│                  │  │                  │  │                  │
│ State: Busy 🔵   │  │ State: Wait 🟡   │  │ State: Busy 🔵   │
│ Claude running   │  │ Needs input      │  │ Claude running   │
└──────────────────┘  └──────────────────┘  └──────────────────┘
```

---

## User Flows

### Flow 1: Start Working on a Task

```
User sees task CHE-102 in "open" column
  ↓
User presses Enter on CHE-102
  ↓
Azedarach:
  1. Creates worktree: ../Chefy-che-102
  2. Runs `bd sync` in worktree
  3. Updates status: `bd update che-102 --status=in_progress`
  4. Spawns tmux session: `tmux new-session -d -s che-102`
  5. Starts Claude: `claude "work on: che-102"`
  ↓
Task moves to "in_progress" with 🔵 indicator
  ↓
User can continue starting more tasks (parallel)
```

### Flow 2: Handle Waiting Task

```
Claude in CHE-106 asks a question (detected via output)
  ↓
Task shows 🟡 indicator
  ↓
User presses 'a' on CHE-106
  ↓
Azedarach attaches to tmux session:
  `tmux attach-session -t che-106`
  ↓
User responds to Claude's question
  ↓
User detaches (Ctrl+B, D) or closes tab
  ↓
Task continues, indicator returns to 🔵
```

### Flow 3: Task Completion

```
Claude finishes CHE-103 successfully
  ↓
Azedarach detects "done" state
  ↓
Azedarach:
  1. Runs `bd sync` (push progress)
  2. Commits changes: `git add -A && git commit -m "..."`
  3. Pushes: `git push -u origin che-103`
  4. Creates PR: `gh pr create --draft`
  5. Notifies user (terminal bell/notification)
  6. Moves task to "review" column with ✅
  ↓
User reviews PR, approves, merges
  ↓
User marks task verified (or auto-verify if configured)
  ↓
Azedarach:
  1. Runs `bd close che-103`
  2. Cleans up worktree
  3. Task moves to "closed"
```

---

## System Requirements

**Required:**
- **For ts-opentui:**
  - Bun >= 1.0 (required for OpenTUI's Zig FFI)
  - Git >= 2.20 (worktree support)
  - tmux >= 3.0
  - gh CLI (authenticated)
  - Beads (`bd` CLI installed and configured)
  - Claude Code (`claude` CLI installed and authenticated)

- **For go-bubbletea:**
  - Go >= 1.21
  - Git >= 2.20 (worktree support)
  - tmux >= 3.0
  - gh CLI (authenticated)
  - Beads (`bd` CLI installed and configured)
  - Claude Code (`claude` CLI installed and authenticated)

---

## Contributing

Please refer to the implementation-specific documentation:
- [ts-opentui/CLAUDE.md](./ts-opentui/CLAUDE.md) for TypeScript/Bun development
- [go-bubbletea/CLAUDE.md](./go-bubbletea/CLAUDE.md) for Go/Bubbletea development

---

## License

See [LICENSE](./LICENSE) for details.

---

## References

- [Beads](https://github.com/steveyegge/beads) - Task tracking backend
- [Beads Worktree Docs](https://github.com/steveyegge/beads/blob/main/docs/ADVANCED.md#git-worktrees)
- [CCManager](https://github.com/kbwo/ccmanager) - Session management inspiration
- [Claude Squad](https://github.com/smtg-ai/claude-squad) - Parallel Claude orchestration
- [OpenTUI](https://github.com/sst/opentui) - React for CLI (ts-opentui)
- [Bubbletea](https://github.com/charmbracelet/bubbletea) - TUI framework (go-bubbletea)
