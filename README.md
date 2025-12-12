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

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                      Azedarach TUI (OpenTUI)                         │
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

## Core Components

### 1. TUI Layer (OpenTUI + React)

The user-facing terminal interface.

**Technology**: OpenTUI (React for CLI) with:
- `@opentui/core` - TUI rendering engine (Zig backend)
- `@opentui/react` - React bindings for OpenTUI
- `effect-atom` - Effect-to-React state bridge

**Views**:
- **Board View**: Kanban columns matching beads statuses
- **Detail View**: Full task info, logs, actions
- **Session View**: Live output from attached Claude session

**Keybindings** (Helix-editor style):

Azedarach uses **modal keybindings** inspired by the [Helix editor](https://helix-editor.com/). The current mode is shown in the status bar (NOR/SEL/GTO/ACT).

**Normal Mode** (default):
| Key | Action |
|-----|--------|
| `h/j/k/l` or `←↓↑→` | Navigate (column/down/up/column) |
| `Ctrl-d` / `Ctrl-u` | Half-page down/up |
| `Space` | Open action menu |
| `v` | Enter select mode (multi-select) |
| `g` | Enter goto mode (see below) |
| `q` | Quit (sessions persist) |

**Goto Mode** (after pressing `g`):
| Key | Action |
|-----|--------|
| `w` | Word/item jump - shows 2-char labels on each task |
| `g` | Go to first task (first column, top) |
| `e` | Go to last task (last column, bottom) |
| `h` | Go to first column (keep task index) |
| `l` | Go to last column (keep task index) |
| `Esc` | Cancel |

**Jump Mode** (after `gw`):
Each task displays a 2-character label (e.g., `aa`, `as`, `ad`). Type the label to instantly jump to that task. Labels use home-row keys for ergonomics.

**Select Mode** (after pressing `v`):
| Key | Action |
|-----|--------|
| `h/j/k/l` | Navigate (same as normal mode) |
| `Space` | Toggle selection on current task |
| `v` | Exit select mode (keep selections) |
| `Esc` | Exit and clear all selections |

**Action Mode** (after pressing `Space` in normal):
| Key | Action |
|-----|--------|
| `h/l` | **Move task(s)** to previous/next column |
| `s` | Start task (spawn Claude session) |
| `a` | Attach to running session (opens terminal) |
| `p` | Pause session (commit WIP, detach) |
| `r` | Resume paused session |
| `d` | Show diff for task |
| `P` | Create/view PR |
| `Esc` | Cancel |

In action mode, `h`/`l` (or left/right arrows) **move the selected task(s)** to the adjacent column. This updates the task's status via the beads CLI. If you have multi-selected tasks (via `v` → `Space`), all selected tasks are moved together.

### 2. Session Manager

Orchestrates Claude sessions and worktrees.

**Responsibilities**:
- Create git worktrees with proper naming
- Spawn tmux sessions with Claude
- Monitor output streams for state detection
- Handle state transitions and hooks
- Manage session lifecycle (pause/resume/cleanup)

**Session States**:
```typescript
type SessionState =
  | "idle"      // Worktree exists, no Claude running
  | "starting"  // Claude spawning
  | "busy"      // Claude actively working
  | "waiting"   // Claude needs user input
  | "done"      // Claude finished successfully
  | "error"     // Claude exited with error
  | "paused"    // Manually paused (WIP committed)
```

**State Detection** (inspired by CCManager):
```typescript
interface StatePattern {
  pattern: RegExp;
  state: SessionState;
  priority: number;
}

const CLAUDE_PATTERNS: StatePattern[] = [
  // Waiting for input
  { pattern: /\[y\/n\]/i, state: "waiting", priority: 10 },
  { pattern: /\[Y\/n\]/i, state: "waiting", priority: 10 },
  { pattern: /Do you want to/i, state: "waiting", priority: 8 },
  { pattern: /Please (provide|specify|confirm)/i, state: "waiting", priority: 7 },

  // Done indicators
  { pattern: /Task completed/i, state: "done", priority: 10 },
  { pattern: /Successfully (created|updated|fixed)/i, state: "done", priority: 6 },

  // Error indicators
  { pattern: /Error:|Exception:|Failed:/i, state: "error", priority: 5 },

  // Busy (default when Claude is outputting)
  { pattern: /.+/, state: "busy", priority: 1 },
];
```

### 3. Worktree Manager

Handles git worktree lifecycle.

**Naming Convention**:
```
../ProjectName-<bead-id>/
../Chefy-che-102/
../Chefy-che-106/
```

**Epic Handling**:
- If bead is an **epic**: one worktree for the epic
- If bead has **epic parent**: use the epic's worktree
- If bead is **standalone**: dedicated worktree

**Worktree Creation**:
```bash
# Create worktree from main branch
git worktree add ../ProjectName-che-102 -b che-102 main

# Initialize beads in worktree (creates local db)
cd ../ProjectName-che-102
bd init

# Copy Claude session context (optional, for continuity)
cp -r .claude/projects/$(pwd | sed 's/\//-/g')/* ../ProjectName-che-102/.claude/projects/
```

**Worktree Cleanup** (after PR merge):
```bash
git worktree remove ../ProjectName-che-102
git branch -d che-102
```

### 4. Beads Integration

All beads operations via `bd` CLI (not MCP, for efficiency).

**Key Commands Used**:
```bash
# Discovery
bd search "keywords"        # Find tasks
bd ready                    # Unblocked tasks
bd show <id>                # Task details

# Lifecycle
bd update <id> --status=in_progress  # Claim task
bd update <id> --notes="..."         # Progress notes
bd close <id> --reason="..."         # Complete task

# Sync (critical for worktrees)
bd sync                     # Manual sync in worktrees
```

**Worktree Sync Strategy**:
Per beads docs, daemon mode is disabled in worktrees. Azedarach handles sync:
1. Before starting Claude: `bd sync` (pull latest)
2. Periodically during work: `bd sync` (push progress)
3. On task completion: `bd sync` (final state)
4. On PR merge: beads changes merged via git

### 5. PR Workflow

Automated GitHub PR creation via `gh` CLI.

**Default Flow** (on task completion):
1. Claude signals "done" (or user marks complete)
2. Azedarach runs `bd sync` in worktree
3. Commits any uncommitted changes
4. Pushes branch to origin
5. Creates draft PR: `gh pr create --draft --title "..." --body "..."`
6. Notifies user

**Configurable Behaviors**:
```typescript
interface TaskCompletionConfig {
  // PR creation
  createPR: "draft" | "ready" | "none";

  // Auto-merge settings
  autoMerge: "disabled" | "after-ci" | "immediate";

  // Notifications
  notify: "always" | "on-waiting" | "never";

  // Auto-verify (skip user review)
  autoVerify: boolean;
}

// Defaults
const DEFAULT_CONFIG: TaskCompletionConfig = {
  createPR: "draft",
  autoMerge: "disabled",
  notify: "always",
  autoVerify: false,
};
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
Azedarach opens new terminal tab:
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

### Flow 4: Manual Fix Required

```
Claude in CHE-107 hits an error (or user spots issue)
  ↓
User presses 'a' to attach
  ↓
User fixes issue manually in attached session
  ↓
User tells Claude to continue (or restarts Claude)
  ↓
User detaches
  ↓
Task continues normally
```

### Flow 5: Pause and Resume

```
User needs to context-switch, presses 'p' on CHE-102
  ↓
Azedarach:
  1. Sends interrupt to Claude (Ctrl+C)
  2. Commits WIP: `git add -A && git commit -m "WIP: che-102"`
  3. Updates notes: `bd update che-102 --notes="Paused: reason"`
  4. Detaches tmux session
  ↓
Task shows ⏸️ indicator
  ↓
Later, user presses 'r' on CHE-102
  ↓
Azedarach:
  1. Reattaches to tmux session
  2. Restarts Claude with context
  ↓
Task resumes with 🔵 indicator
```

---

## Technical Stack

### Runtime & Build
- **Bun** >= 1.0 (required for OpenTUI's Zig FFI)
- **TypeScript** (strict mode)
- **tsup** (bundling)

### TUI Framework
- **@opentui/core** - TUI rendering engine
- **@opentui/react** - React bindings
- **effect-atom** - Effect-to-React state bridge
- **react** ^19.0 - React framework

### Terminal/Process Management
- **node-pty** - PTY for Claude process
- **tmux** - Session persistence (system dependency)
- **execa** - Subprocess execution

### Git Operations
- **simple-git** - Git commands from Node
- Native `git` CLI - Worktree operations

### System Integration
- **gh** CLI - GitHub PR creation (system dependency)

### Configuration
- **cosmiconfig** - Config file loading
- **zod** - Config validation

---

## File Structure

```
azedarach/
├── package.json
├── tsconfig.json
├── README.md
│
├── src/
│   ├── index.tsx              # Entry point
│   ├── cli.ts                 # CLI argument parsing
│   │
│   ├── ui/                    # OpenTUI components
│   │   ├── App.tsx            # Root component
│   │   ├── Board.tsx          # Kanban board
│   │   ├── Column.tsx         # Status column
│   │   ├── TaskCard.tsx       # Task card
│   │   ├── DetailView.tsx     # Task detail panel
│   │   ├── SessionView.tsx    # Live session output
│   │   ├── StatusBar.tsx      # Bottom status bar
│   │   └── Help.tsx           # Help overlay
│   │
│   ├── core/                  # Business logic
│   │   ├── SessionManager.ts  # Claude session orchestration
│   │   ├── WorktreeManager.ts # Git worktree lifecycle
│   │   ├── StateDetector.ts   # Output pattern matching
│   │   ├── BeadsClient.ts     # bd CLI wrapper
│   │   └── PRWorkflow.ts      # GitHub PR automation
│   │
│   ├── hooks/                 # State transition hooks
│   │   ├── onWaiting.ts       # Notify when Claude waits
│   │   ├── onDone.ts          # PR creation, etc.
│   │   ├── onError.ts         # Error handling
│   │   └── index.ts           # Hook registry
│   │
│   ├── config/                # Configuration
│   │   ├── schema.ts          # Config validation (zod)
│   │   ├── defaults.ts        # Default values
│   │   └── loader.ts          # cosmiconfig setup
│   │
│   └── utils/                 # Utilities
│       ├── tmux.ts            # tmux commands
│       ├── terminal.ts        # Terminal detection/launch
│       ├── logger.ts          # Logging
│       └── paths.ts           # Path helpers
│
├── bin/
│   └── az.js                  # CLI executable
│
└── test/
    ├── SessionManager.test.ts
    ├── StateDetector.test.ts
    └── ...
```

---

## Configuration

Config file: `.azedarach.json`, `.azedarachrc`, or `azedarach` key in `package.json`

```json
{
  "terminal": "iterm",
  "terminalArgs": ["-e"],

  "worktree": {
    "location": "../{project}-{bead-id}",
    "baseBranch": "main"
  },

  "session": {
    "initialPrompt": "work on: {bead-id}",
    "stuckTimeout": 300,
    "autoReconnect": true
  },

  "completion": {
    "createPR": "draft",
    "autoMerge": "disabled",
    "autoVerify": false
  },

  "notifications": {
    "onWaiting": true,
    "onDone": true,
    "onError": true,
    "sound": true
  },

  "patterns": {
    "waiting": ["\\[y/n\\]", "Do you want to"],
    "done": ["Task completed", "Successfully"],
    "error": ["Error:", "Failed:"]
  }
}
```

---

## CLI Interface

```bash
# Start TUI in current directory (must have .beads/)
az

# Start TUI for specific project
az /path/to/project

# Add project to multi-project mode
az add /path/to/another/project

# List managed projects
az list

# Quick actions without TUI
az start che-102        # Start task
az attach che-102       # Attach to session
az pause che-102        # Pause task
az status               # Show all task statuses
az sync                 # Sync all worktrees
```

---

## System Requirements

**Required**:
- Bun >= 1.0 (required for OpenTUI's Zig FFI)
- Git >= 2.20 (worktree support)
- tmux >= 3.0
- gh CLI (authenticated)
- Beads (`bd` CLI installed and configured)
- Claude Code (`claude` CLI installed and authenticated)

**Optional**:
- Terminal configured for external attachment (iTerm, Terminal.app)

---

## Future Considerations

### Phase 2 Features
- **Multi-project dashboard**: Monitor tasks across multiple beads projects
- **Team visibility**: See who's working on what (via beads assignee)
- **Session sharing**: Allow multiple users to attach to same session
- **AI status summaries**: Claude summarizes its own progress periodically

### Phase 3 Features
- **Dependency visualization**: Graph view of blocked/blocking tasks
- **Cost tracking**: Token usage per task
- **Time tracking**: Automatic time logging
- **VS Code extension**: Same functionality in IDE sidebar

### Integration Possibilities
- **Linear/Jira sync**: Two-way sync with external trackers
- **Slack notifications**: Team alerts via webhooks
- **CI integration**: Trigger tests before PR creation

---

## Open Questions

1. **Session persistence across machine restarts**: tmux sessions don't survive reboot. Should we persist session state to disk for recovery?

2. **Conflict resolution**: If two Claude sessions modify the same file (in different worktrees on different branches), how to handle merge conflicts at PR time?

3. **Resource limits**: Should there be configurable limits on parallel sessions? Memory/CPU monitoring?

4. **Claude version management**: Different projects might need different Claude behaviors. How to handle?

---

## Getting Started (Future README)

```bash
# Install
npm install -g azedarach

# Ensure dependencies
brew install tmux gh
gh auth login

# Start in your beads-enabled project
cd your-project
az

# Or specify project path
az /path/to/project
```

---

## References

- [Beads](https://github.com/steveyegge/beads) - Task tracking backend
- [Beads Worktree Docs](https://github.com/steveyegge/beads/blob/main/docs/ADVANCED.md#git-worktrees)
- [CCManager](https://github.com/kbwo/ccmanager) - Session management inspiration
- [Claude Squad](https://github.com/smtg-ai/claude-squad) - Parallel Claude orchestration
- [OpenTUI](https://github.com/sst/opentui) - React for CLI (successor to Ink)
- [node-pty](https://github.com/microsoft/node-pty) - PTY handling
