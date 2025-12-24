# Azedarach Gleam Rewrite Scope Document

**Version:** 4.0.0
**Date:** 2025-12-24
**Status:** Draft (Revised)

---

## Executive Summary

Full rewrite of Azedarach from TypeScript (Effect + React + OpenTUI) to Gleam using the Shore TUI framework. This is a **reimagining**, not a port—same features, better architecture.

**Core Mission:** Task tracking and worktree management with consideration for CLI AI agents.

**Why Rewrite:**
- Current state management is painful and performance is poor
- 43 services is excessive for what the app actually does
- Gleam + OTP provides simpler concurrency and fault tolerance
- TEA (The Elm Architecture) naturally enforces simpler state

**Location:** `azedarach/gleam/` subdirectory, will replace TypeScript version eventually.

---

## 1. Project Goals

### Primary Goals

1. **Simpler architecture** - ~6 actors instead of 43 services
2. **Better performance** - OTP lightweight processes, no React reconciliation overhead
3. **Less jank** - Focus on polish and quality of life
4. **Single source of truth** - TEA model replaces three-layer state management
5. **Fault tolerance** - OTP supervision with sensible auto-recovery

### Core Features (v1.0)

| Feature | Description |
|---------|-------------|
| **Kanban board** | Overview of beads tasks by status (4 columns) |
| **Multi-project** | Project switcher within single instance |
| **Bead CRUD** | Create/edit beads with image attachment support |
| **Git integration** | Worktrees, status, diffs, PR creation, merge to main |
| **Session management** | 1 worktree + 1 tmux session per bead |
| **Dev servers** | Managed dev server processes with port allocation |
| **Init commands** | Setup commands run once at tmux session creation |
| **Background tasks** | Long-running processes in separate tmux windows |
| **Cleanup** | Delete worktree, branch, session |

### Out of Scope (v1.0)

| Feature | Reason |
|---------|--------|
| Epic orchestration / swarm | Deferred to v2 |
| VC integration | Not yet used |
| Command mode (`:`) | Tied to VC |
| Compact view | Nice-to-have |
| Keybind customization | Stretch goal |
| Attach inline | Deprecated |
| Chat about task | Not needed |

---

## 2. Technical Decisions

### 2.1 Resolved Questions

| Question | Decision |
|----------|----------|
| Config format | **JSON** (bd compatibility) |
| State persistence | **Tmux as source of truth**, in-memory for optimistic updates, files only as last resort |
| Theme | **Catppuccin Macchiato** default, custom themes supported |
| Tmux session naming | **`<bead-id>-az`** (suffix) |
| Worktree location | **Template string**: `"{project}-{bead-id}"` (configurable) |
| Polling intervals | **Configurable** with current values as defaults |
| Beads dependency | **Required** - beads is the task persistence layer |
| Dev server ports | **Trust the port we set** (no output polling for port) |
| Repo location | **Subdirectory** `azedarach/gleam/`, replace eventually |
| Shore customization | **Fork first**, contribute upstream later |
| Multi-project | **Switcher within single instance** |

### 2.2 Configuration Schema

```json
{
  "worktree": {
    "pathTemplate": "../{project}-{bead-id}",
    "initCommands": ["direnv allow", "bun install", "bd sync"],
    "continueOnFailure": true
  },
  "session": {
    "shell": "zsh",
    "tmuxPrefix": "C-a",
    "backgroundTasks": ["npm run watch", "npm run test:watch"]
  },
  "devServer": {
    "servers": {
      "default": {
        "command": "npm run dev",
        "ports": { "PORT": 3000 }
      }
    }
  },
  "git": {
    "workflowMode": "origin",
    "pushBranchOnCreate": true,
    "pushEnabled": true,
    "fetchEnabled": true,
    "baseBranch": "main",
    "remote": "origin",
    "branchPrefix": "az-"
  },
  "pr": {
    "enabled": true,
    "autoDraft": true,
    "autoMerge": false
  },
  "beads": {
    "syncEnabled": true
  },
  "polling": {
    "beadsRefresh": 30000,
    "sessionMonitor": 500
  },
  "theme": "catppuccin-macchiato"
}
```

### 2.3 Git Workflow Modes

**Local Mode (`workflowMode: "local"`):**
- Direct merge to main branch
- No remote push required
- Ideal for solo development

**Origin Mode (`workflowMode: "origin"`):**
- PR-based workflow
- Auto-push branch on worktree creation
- PR creation via `gh pr create`

**Kill Switches:**
- `pushEnabled: false` - Disables all git push operations
- `fetchEnabled: false` - Disables all git fetch operations
- Useful for offline work or restricted environments

**Settings Overlay:**
- Live toggle of push/fetch enabled
- Switch between workflow modes
- Changes persist to config file

---

## 3. Architecture

### 3.1 Actor Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    Shore Application                         │
│         (TEA: Model + Update + View = all UI state)         │
└─────────────────────────────────────────────────────────────┘
                            │
                    messages│
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                    Coordinator Actor                         │
│  - Task cache (from beads)                                   │
│  - Session registry (optimistic state)                       │
│  - Dev server registry                                       │
│  - Routes commands to services                               │
└─────────────────────────────────────────────────────────────┘
          │                    │
          ▼                    ▼
    ┌──────────┐         ┌──────────┐
    │ Sessions │         │  Dev     │
    │Supervisor│         │ Servers  │
    └──────────┘         │Supervisor│
          │              └──────────┘
          ▼ (dynamic)          │
    ┌──────────┐               ▼ (dynamic)
    │ Session  │         ┌──────────┐
    │ Monitor  │         │  Server  │
    └──────────┘         │ Monitor  │
                         └──────────┘

Stateless Modules: Worktree, Beads, Git, Tmux, Clipboard
```

### 3.2 State Hierarchy

```
Source of Truth Priority:
1. Tmux (session exists? what's the output?)
2. In-memory (optimistic updates, derived state)
3. Files (only for persistent config, image attachments)
```

**On restart:** Reconstruct state by querying tmux for existing sessions.

### 3.3 TEA Model

```gleam
pub type Model {
  Model(
    // Core data
    tasks: List(Task),
    sessions: Dict(String, SessionState),
    dev_servers: Dict(String, DevServerState),

    // Multi-project
    projects: List(Project),
    current_project: Option(String),

    // Navigation
    cursor: Cursor,
    mode: Mode,

    // UI state
    input: Option(InputState),
    overlay: Option(Overlay),
    pending_key: Option(String),

    // Filters
    status_filter: Set(Status),
    sort_by: SortField,
    search_query: String,

    // Config
    config: Config,

    // Meta
    loading: Bool,
    toasts: List(Toast),
  )
}
```

### 3.4 Modes and Overlays

**2 actual modes:**
```gleam
pub type Mode {
  Normal
  Select(selected: Set(String))
}
```

**Input states:**
```gleam
pub type InputState {
  Search(query: String)
  BeadTitle(text: String)
  BeadNotes(text: String)
}
```

**Overlays (one at a time):**
```gleam
pub type Overlay {
  ActionMenu
  SortMenu
  FilterMenu
  HelpOverlay
  SettingsOverlay
  DiagnosticsOverlay
  LogsViewer
  ProjectSelector
  DetailPanel(bead_id: String)
  ImageAttach(bead_id: String)
  ImagePreview(path: String)
  DevServerMenu(bead_id: String)
  DiffViewer(bead_id: String)
  MergeChoice(bead_id: String)
  ConfirmDialog(action: PendingAction)
}
```

### 3.5 OTP Supervision Strategy

```
Monitor crashes:
  → Supervisor auto-restarts
  → Monitor polls tmux for current state
  → UI shows "refreshing..." briefly
  → Normal operation resumes

If 3 crashes in 60 seconds:
  → Mark session/server "unknown"
  → Surface toast warning
```

---

## 4. Tmux Session Model

### 4.1 Session Creation Triggers

A tmux session (`{bead-id}-az`) can be created by any of:

1. **Starting Claude session** (Space+s, Space+S, Space+!)
2. **Starting dev server** (Space+r)
3. **Opening editor in worktree** (if implemented)
4. **Pre-spinning up for later use** (if implemented)

### 4.2 Init Commands (Once Per Session)

Init commands run **only once** when the tmux session is first created:

```
Session Creation:
    │
    ├─→ Create tmux session
    │
    ├─→ Run init commands ONCE (sequentially):
    │     1. direnv allow  → wait for prompt
    │     2. bun install   → wait for prompt
    │     3. bd sync       → wait for prompt
    │     4. Set marker @az_init_done = 1
    │
    └─→ Ready for windows (main, dev, background tasks)
```

If session already exists, skip init and just create the requested window.

### 4.3 Window Structure

```
Session: {bead-id}-az
├── Window: main      → Claude or empty shell
├── Window: dev-web   → Dev server "web" (named for lookup)
├── Window: dev-api   → Dev server "api" (if multiple)
├── Window: task-1    → Background task (closes on success)
└── Window: task-2    → Background task (closes on success)
```

**Dev servers:** One window per server, named `dev-{server-name}` for lookup.

**Background tasks:** Windows close on success, stay open only on failure for debugging.

### 4.4 Session Lifecycle

```
Start Session (Space+s):
  IF session exists:
    → Attach to existing session
  ELSE:
    → Create worktree (if needed)
    → Create tmux session
    → Run init commands (once)
    → Create main window with "claude"
    → Create background task windows (parallel)
    → Start session monitor

Start Dev Server (Space+r):
  IF session exists:
    → Create window "dev-{name}" with command
  ELSE:
    → Create worktree (if needed)
    → Create tmux session
    → Run init commands (once)
    → Create window "dev-{name}" with command
    → Start server monitor (tracks port)

Stop Session (Space+x):
  → Kill tmux session
  → Cleanup monitors

Delete/Cleanup (Space+d):
  → Stop session (if running)
  → Delete worktree
  → Delete remote branch
  → Delete local branch
  → Optionally close bead
```

---

## 5. Complete Feature List

### 5.1 Navigation

| Key | Action |
|-----|--------|
| h/j/k/l or arrows | Move cursor |
| Ctrl+Shift+d | Half-page down |
| Ctrl+Shift+u | Half-page up |
| Enter | View detail / enter epic |
| q | Quit (or exit drill-down) |
| Esc | Exit mode / close overlay |
| Tab | Toggle view mode (future: kanban ↔ compact) |
| Ctrl+l | Force redraw |

### 5.2 Goto Mode (g + key)

| Key | Action |
|-----|--------|
| g | First task in column |
| e | Last task in column |
| h | First column |
| l | Last column |
| w | Jump labels mode |
| p | Project selector |

### 5.3 Select Mode (v)

| Key | Action |
|-----|--------|
| Space | Toggle selection |
| h/j/k/l | Navigate with selections |
| Esc | Exit and clear |

### 5.4 Search Mode (/)

| Key | Action |
|-----|--------|
| typing | Filter by title/ID |
| Enter | Confirm filter |
| Esc | Clear and exit |

### 5.5 Sort Mode (,)

| Key | Action |
|-----|--------|
| s | Sort by session status |
| p | Sort by priority |
| u | Sort by updated_at |
| Esc | Cancel |

### 5.6 Filter Mode (f)

| Key | Action |
|-----|--------|
| s | Status sub-menu |
| p | Priority sub-menu |
| t | Type sub-menu |
| S | Session state sub-menu |
| e | Toggle hide epic children |
| c | Clear all filters |
| Esc | Cancel |

### 5.7 Action Menu (Space)

**Session Actions:**
| Key | Action |
|-----|--------|
| s | Start session |
| S | Start+work (with bead context prompt) |
| ! | Start yolo (skip permissions) |
| a | Attach to session |
| p | Pause session |
| S+r | Resume session |
| x | Stop session |

**Dev Server Actions:**
| Key | Action |
|-----|--------|
| r | Toggle dev server |
| v | View dev server (attach to window) |
| C+r | Restart dev server |

**Git/PR Actions:**
| Key | Action |
|-----|--------|
| u | Update from main (merge main into branch) |
| f | Show diff |
| S+p | Create PR |
| m | Merge to main |
| d | Delete worktree / cleanup |

**Task Actions:**
| Key | Action |
|-----|--------|
| h | Move task left |
| l | Move task right |

### 5.8 Detail Panel (Enter)

| Key | Action |
|-----|--------|
| Ctrl+u/d | Scroll |
| j/k | Select attachment |
| v | Preview image |
| o | Open in viewer |
| x | Delete attachment |
| i | Add image |
| e | Edit bead |

### 5.9 Top-Level Keys

| Key | Action |
|-----|--------|
| ? | Help overlay |
| s | Settings overlay |
| d | Diagnostics overlay |
| c | Create bead ($EDITOR) |
| S+c | Create bead (Claude prompt) |
| S+l | View logs |
| R | Refresh |

### 5.10 Image Attachment

| Key | Action |
|-----|--------|
| p/v | Paste from clipboard |
| f | Enter file path mode |
| Esc | Close |

---

## 6. Module Structure

```
gleam/
├── src/
│   ├── azedarach.gleam           # Entry point
│   ├── cli.gleam                 # CLI parsing
│   ├── config.gleam              # JSON config
│   │
│   ├── ui/
│   │   ├── app.gleam             # Shore application
│   │   ├── model.gleam           # Model types
│   │   ├── update.gleam          # Message handling
│   │   ├── view.gleam            # Main view
│   │   ├── view/
│   │   │   ├── board.gleam       # Kanban columns
│   │   │   ├── card.gleam        # Task cards
│   │   │   ├── status_bar.gleam  # Bottom bar
│   │   │   └── overlays.gleam    # All overlays
│   │   ├── keys.gleam            # Keybindings
│   │   └── theme.gleam           # Catppuccin + custom
│   │
│   ├── actors/
│   │   ├── coordinator.gleam     # Central orchestration
│   │   ├── sessions_sup.gleam    # Session supervisor
│   │   ├── session_monitor.gleam # Claude state detection
│   │   ├── servers_sup.gleam     # Dev server supervisor
│   │   └── server_monitor.gleam  # Server state tracking
│   │
│   ├── services/
│   │   ├── beads.gleam           # bd CLI wrapper
│   │   ├── tmux.gleam            # tmux commands
│   │   ├── worktree.gleam        # git worktree ops
│   │   ├── git.gleam             # git commands
│   │   ├── pr.gleam              # GitHub PR via gh
│   │   ├── clipboard.gleam       # Platform clipboard
│   │   ├── state_detector.gleam  # Output pattern matching
│   │   └── image.gleam           # Image attachments
│   │
│   ├── domain/
│   │   ├── task.gleam            # Task types
│   │   ├── session.gleam         # Session state
│   │   ├── bead.gleam            # Bead schema
│   │   ├── project.gleam         # Project types
│   │   └── attachment.gleam      # Image types
│   │
│   └── util/
│       ├── shell.gleam           # Command execution
│       ├── time.gleam            # Formatting
│       └── platform.gleam        # OS detection
│
├── test/
│   └── ...
│
└── gleam.toml
```

---

## 7. Implementation Phases

### Phase 1: Foundation (Week 1-2)

- [ ] Project scaffolding in `gleam/`
- [ ] Shore integration, TEA skeleton
- [ ] Catppuccin Macchiato theme
- [ ] Empty kanban board (4 columns)
- [ ] Navigation (hjkl, arrows)
- [ ] Status bar
- [ ] JSON config loading

**Milestone:** App starts, themed board, cursor moves

### Phase 2: Beads & Projects (Week 3-4)

- [ ] Beads module (`bd list/show/create/update`)
- [ ] Coordinator actor
- [ ] Task card rendering
- [ ] Periodic refresh
- [ ] Bead creation/editing
- [ ] Project switcher (multi-project)

**Milestone:** Board shows beads, can switch projects

### Phase 3: Sessions & Worktrees (Week 5-6)

- [ ] Worktree module
- [ ] Tmux module (sessions, windows)
- [ ] Init commands (once per session)
- [ ] Session spawning (s, S, !)
- [ ] Session monitor + state detection
- [ ] Attach, pause, resume, stop
- [ ] Session state on cards

**Milestone:** Full session lifecycle works

### Phase 4: Dev Servers & Git (Week 7-8)

- [ ] Dev server windows (one per server)
- [ ] Port allocation (trust the port)
- [ ] Server monitor
- [ ] Background task windows (close on success)
- [ ] Update from main
- [ ] Merge to main
- [ ] Show diff
- [ ] Create PR
- [ ] Delete/cleanup

**Milestone:** Full git workflow, dev servers work

### Phase 5: Full Interaction (Week 9-10)

- [ ] All overlays (action, filter, sort, help, settings, diagnostics)
- [ ] Search
- [ ] Select mode
- [ ] Goto mode + jump labels
- [ ] Detail panel
- [ ] Image attachment (clipboard + file)
- [ ] Logs viewer
- [ ] Confirm dialogs
- [ ] Toast notifications

**Milestone:** Feature complete

### Phase 6: Polish (Week 11-12)

- [ ] Custom theme support
- [ ] Error handling
- [ ] Performance optimization
- [ ] Edge cases
- [ ] Documentation
- [ ] Distribution (escript + standalone)

**Milestone:** Production ready

---

## 8. Success Criteria

### Must Have
- [ ] Kanban board with beads
- [ ] Multi-project switcher
- [ ] Bead CRUD with image attachments
- [ ] Session spawn/attach/pause/resume/stop
- [ ] Start+work and yolo variants
- [ ] Dev servers (one window per server)
- [ ] Init commands (once per session)
- [ ] Background tasks (close on success)
- [ ] Update from main, merge to main
- [ ] Create PR, show diff
- [ ] Delete/cleanup worktree+branch
- [ ] Filter, sort, search
- [ ] Catppuccin theme

### Performance
- [ ] Startup < 300ms
- [ ] Key response < 16ms
- [ ] Memory < 50MB typical

### Quality
- [ ] No UI freezes
- [ ] Graceful error handling
- [ ] State reconstruction from tmux on restart

---

## 9. Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Shore immaturity | Fork early, contribute later |
| Tmux flickering | Test in Phase 1, custom buffering if needed |
| Clipboard cross-platform | Platform detection, graceful fallback |
| Image rendering | Use external viewer if terminal support lacking |

---

## 10. Resolved Questions

All open questions have been addressed in companion documents:

| Question | Resolution | Document |
|----------|------------|----------|
| Testing strategy | Unit (<5s), integration with real tmux/bd (<30s total), snapshot tests | `docs/gleam/testing-strategy.md` |
| Start+work prompt format | Bead ID, type, title, `bd show` instruction, ask-first directive, image paths | `docs/gleam/start-work-prompt.md` |
| Merge conflict UX | MergeChoice overlay, git merge-tree detection, Claude spawn for resolution | `docs/gleam/merge-conflict-ux.md` |

---

## 11. Companion Documents

| Document | Purpose |
|----------|---------|
| `docs/gleam/architecture.md` | Actor diagram, message flow, OTP supervision tree |
| `docs/gleam/session-lifecycle.md` | State machine, creation triggers, init commands |
| `docs/gleam/feature-matrix.md` | Complete in/out of scope table |
| `docs/gleam/user-flows.md` | 10 detailed user interaction flows |
| `docs/gleam/testing-strategy.md` | Test pyramid, examples, CI setup |
| `docs/gleam/start-work-prompt.md` | Exact prompt format for Space+S |
| `docs/gleam/merge-conflict-ux.md` | Conflict detection and resolution flow |

---

## 12. Next Steps

1. ~~Create architecture diagram~~ ✓
2. ~~Create session lifecycle diagram~~ ✓
3. ~~Create feature matrix~~ ✓
4. ~~Create user flows~~ ✓
5. ~~Resolve open questions~~ ✓
6. Shore spike - verify flickering, test basic TEA
7. Begin Phase 1 implementation

---

*Document version: 4.1.0 - Added git workflow configuration, resolved open questions*
