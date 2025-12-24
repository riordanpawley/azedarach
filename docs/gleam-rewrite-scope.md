# Azedarach Gleam Rewrite Scope Document

**Version:** 3.0.0
**Date:** 2025-12-24
**Status:** Draft (Complete)

---

## Executive Summary

Full rewrite of Azedarach from TypeScript (Effect + React + OpenTUI) to Gleam using the Shore TUI framework. This is a **reimagining**, not a port—same features, better architecture.

**Core Mission:** Task tracking and worktree management with consideration for CLI AI agents.

**Why Rewrite:**
- Current state management is painful and performance is poor
- 43 services is excessive for what the app actually does
- Gleam + OTP provides simpler concurrency and fault tolerance
- TEA (The Elm Architecture) naturally enforces simpler state

---

## 1. Project Goals

### Primary Goals

1. **Simpler architecture** - 5 actors instead of 43 services
2. **Better performance** - OTP lightweight processes, no React reconciliation overhead
3. **Less jank** - Focus on polish and quality of life
4. **Single source of truth** - TEA model replaces three-layer state management
5. **Fault tolerance** - OTP supervision with sensible auto-recovery

### Core Features (v1.0)

| Feature | Description |
|---------|-------------|
| **Kanban board** | Overview of beads tasks by status |
| **Bead CRUD** | Create/edit beads with image attachment support |
| **Git integration** | Worktrees, status, diffs, PR creation |
| **Session management** | 1 worktree + 1 tmux session per bead |
| **Dev servers** | Managed dev server processes with port allocation |
| **Init commands** | Setup commands run on worktree creation |
| **Background tasks** | Long-running processes in separate tmux windows |

### Deferred to v2

- Epic orchestration / swarm pattern
- Auto PR creation (v1 has manual keybind)

### Non-Goals

- Direct port of TypeScript code
- Hybrid architecture
- Feature bloat

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
    "portPattern": "localhost:(\\d+)",
    "servers": {
      "default": {
        "command": "npm run dev",
        "ports": { "PORT": 3000 }
      }
    }
  },
  "polling": {
    "beadsRefresh": 30000,
    "sessionMonitor": 500
  },
  "theme": "catppuccin-macchiato"
}
```

---

## 3. Architecture

### 3.1 The Five Actors

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
│  - Routes commands to services                               │
│  - Aggregates state updates for UI                           │
└─────────────────────────────────────────────────────────────┘
        │           │           │           │           │
        ▼           ▼           ▼           ▼           ▼
  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
  │ Sessions │ │ Worktree │ │  Beads   │ │   Git    │ │   Dev    │
  │Supervisor│ │ (module) │ │ (module) │ │ (module) │ │ Servers  │
  └──────────┘ └──────────┘ └──────────┘ └──────────┘ └──────────┘
        │                                                   │
        ▼ (dynamic)                                         ▼ (dynamic)
  ┌──────────┐                                        ┌──────────┐
  │ Session  │ ←── one per active Claude session      │  Server  │
  │ Monitor  │     polls tmux, detects state          │ Monitor  │
  └──────────┘                                        └──────────┘
```

**Actor Responsibilities:**

| Actor | State | Purpose |
|-------|-------|---------|
| Shore App | TEA Model | All UI state, rendering |
| Coordinator | Task cache, session registry | Central orchestration |
| Sessions Supervisor | Child monitors | Supervises session monitors |
| Session Monitor | Output buffer, detected state | Polls tmux for Claude state |
| Server Monitor | Port, status | Polls dev server output for port detection |

**Stateless Modules:**
- Worktree: `create/2`, `remove/1`, `status/1`
- Beads: `list/0`, `show/1`, `create/1`, `update/2`
- Git: `status/1`, `diff/1`, `pr_create/2`
- Tmux: `new_session/2`, `capture_pane/2`, `send_keys/3`
- Clipboard: `read_image/0`, `has_image/0`

### 3.2 State Hierarchy

```
Source of Truth Priority:
1. Tmux (session exists? what's the output?)
2. In-memory (optimistic updates, derived state)
3. Files (only for persistent config, image attachments)
```

**Why Tmux first:** If the app crashes and restarts, tmux sessions are still there. We reconstruct state by querying tmux.

### 3.3 TEA Model

```gleam
pub type Model {
  Model(
    // Core data (from beads + tmux)
    tasks: List(Task),
    sessions: Dict(String, SessionState),
    dev_servers: Dict(String, DevServerState),

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

    // Config (loaded once)
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
  Command(text: String)
  BeadTitle(text: String)      // Creating/editing bead
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
  DetailPanel(bead_id: String)
  ImageAttach(bead_id: String)
  ImagePreview(path: String)
  DevServerMenu(bead_id: String)
  ConfirmDialog(action: PendingAction)
}
```

### 3.5 OTP Supervision Strategy

```
Session Monitor crashes:
  → Supervisor auto-restarts
  → Monitor polls tmux for current state
  → UI shows "refreshing..." briefly
  → Normal operation resumes

If 3 crashes in 60 seconds:
  → Mark session "unknown"
  → Surface toast warning

Dev Server Monitor crashes:
  → Same pattern
  → Re-detect port from output
```

---

## 4. Core Features Detail

### 4.1 Session Lifecycle

```
User triggers "Start Session" (Space+s)
    │
    ▼
Coordinator receives SpawnSession(bead_id)
    │
    ├─→ Worktree.create(bead_id, config.pathTemplate)
    │     └─→ git worktree add ../project-bead-id
    │
    ├─→ Tmux.new_session("{bead_id}-az", worktree_path)
    │     └─→ tmux new-session -d -s {bead_id}-az -c {path}
    │
    ├─→ Run init commands sequentially:
    │     for cmd in config.initCommands:
    │       Tmux.send_keys(session, cmd)
    │       wait_for_prompt()
    │
    ├─→ Tmux.send_keys(session, "claude")
    │
    ├─→ Spawn background tasks (parallel):
    │     for task in config.backgroundTasks:
    │       Tmux.new_window(session, "task-N")
    │       wait_for_init_marker()
    │       run init commands
    │       Tmux.send_keys(window, task)
    │
    └─→ Start SessionMonitor under supervisor
          └─→ Polls every 500ms, detects state
```

### 4.2 Dev Servers

```
User triggers "Start Dev Server" (Space+d)
    │
    ▼
Coordinator receives StartDevServer(bead_id, server_name)
    │
    ├─→ Get/create worktree for bead
    │
    ├─→ Allocate port (base + offset for running servers)
    │
    ├─→ Create tmux window "dev" or split pane
    │
    ├─→ Set PORT env var, run command
    │
    └─→ Start ServerMonitor
          └─→ Polls output for port pattern
          └─→ Updates state when port detected
```

**Port allocation:** Each server config has a base port. If that port is in use, increment until free.

### 4.3 Image Attachments

```
User in detail panel, triggers "Attach Image" (i)
    │
    ▼
ImageAttach overlay opens
    │
    ├─→ "p" = Paste from clipboard
    │     └─→ Platform-specific: pbpaste (mac), wl-paste (wayland), xclip (x11)
    │     └─→ Save to .beads/images/{bead-id}/{id}.png
    │     └─→ Append markdown link to bead notes
    │
    └─→ Path input = Attach from filesystem
          └─→ Copy file to .beads/images/{bead-id}/
          └─→ Append markdown link to bead notes

Storage: .beads/images/
├── index.json              # Metadata
└── {bead-id}/
    └── {attachment-id}.png
```

### 4.4 Init Commands

Run sequentially via tmux send-keys (not `&&` chained):
- Allows direnv to load between commands
- Each command waits for shell prompt
- Proper error detection per command

```
1. direnv allow     → wait for prompt (direnv now active)
2. bun install      → wait for prompt (deps installed with direnv env)
3. bd sync          → wait for prompt (beads synced)
4. [set marker]     → @az_init_done = 1
5. [main command]   → claude or dev server
```

### 4.5 Background Tasks

Separate tmux windows in same session:
- Each window runs init commands first
- Then runs the background task
- Window stays open after task exits (`; exec $SHELL`)

```
Session: task-123-az
├── Window 0 (main): claude running
├── Window task-1: npm run watch
└── Window task-2: npm run test:watch
```

---

## 5. Module Structure

```
src/
├── azedarach.gleam              # Entry point, supervisor setup
├── cli.gleam                    # CLI argument parsing
├── config.gleam                 # JSON config loading
│
├── ui/
│   ├── app.gleam                # Shore application
│   ├── model.gleam              # Model type definitions
│   ├── update.gleam             # Message handling
│   ├── view.gleam               # Main view
│   ├── view/
│   │   ├── board.gleam          # Kanban columns
│   │   ├── card.gleam           # Task cards
│   │   ├── status_bar.gleam     # Bottom bar
│   │   └── overlays.gleam       # All overlays
│   ├── keys.gleam               # Keybinding definitions
│   └── theme.gleam              # Catppuccin + custom themes
│
├── actors/
│   ├── coordinator.gleam        # Central orchestration
│   ├── sessions_sup.gleam       # Session monitor supervisor
│   ├── session_monitor.gleam    # Claude state detection
│   ├── servers_sup.gleam        # Dev server supervisor
│   └── server_monitor.gleam     # Port detection
│
├── services/
│   ├── beads.gleam              # bd CLI wrapper
│   ├── tmux.gleam               # tmux commands
│   ├── worktree.gleam           # git worktree ops
│   ├── git.gleam                # git status/diff/PR
│   ├── clipboard.gleam          # Platform clipboard
│   ├── state_detector.gleam     # Output pattern matching
│   └── port_allocator.gleam     # Dev server port management
│
├── domain/
│   ├── task.gleam               # Task types
│   ├── session.gleam            # Session state
│   ├── bead.gleam               # Bead schema
│   └── attachment.gleam         # Image attachment types
│
└── util/
    ├── shell.gleam              # Command execution
    ├── time.gleam               # Formatting
    └── platform.gleam           # OS detection
```

**Estimated: ~7,000-8,000 lines** (accounting for new features)

---

## 6. Dependencies

```toml
[dependencies]
gleam_stdlib = "~> 0.40"
gleam_erlang = "~> 0.27"
gleam_otp = "~> 0.12"
gleam_json = "~> 2.0"
shore = "~> 1.3"
simplifile = "~> 2.0"
shellout = "~> 1.6"
argv = "~> 1.0"

[dev-dependencies]
gleeunit = "~> 1.0"
```

**External CLI tools:** tmux, git, bd, gh, platform clipboard tools

---

## 7. Implementation Phases

### Phase 1: Foundation (Week 1-2)

- [ ] Project scaffolding
- [ ] Shore integration, TEA skeleton
- [ ] Catppuccin theme implementation
- [ ] Empty kanban board (4 columns)
- [ ] Basic navigation (hjkl)
- [ ] Status bar
- [ ] Config loading (JSON)

**Milestone:** App starts, shows empty themed board, cursor moves

### Phase 2: Beads Integration (Week 3-4)

- [ ] Beads module (`bd list/show/create/update`)
- [ ] Coordinator actor with task cache
- [ ] Task card rendering
- [ ] Periodic refresh (configurable interval)
- [ ] Bead creation (n key)
- [ ] Bead editing in detail panel

**Milestone:** Board shows real beads, can create/edit

### Phase 3: Worktrees & Sessions (Week 5-6)

- [ ] Worktree module with template paths
- [ ] Tmux module (sessions, windows, panes)
- [ ] Init commands (sequential execution)
- [ ] Session spawning (Space+s)
- [ ] Sessions supervisor + monitors
- [ ] State detector (pattern matching)
- [ ] Session state on cards

**Milestone:** Can spawn Claude session, see state updates

### Phase 4: Dev Servers & Background (Week 7-8)

- [ ] Dev server config and port allocation
- [ ] Server supervisor + monitors
- [ ] Port detection from output
- [ ] Dev server menu overlay
- [ ] Background tasks in separate windows
- [ ] Toggle dev server (Space+d)

**Milestone:** Can start dev servers, see ports, run background tasks

### Phase 5: Full Interaction (Week 9-10)

- [ ] All overlays (action, filter, sort, help)
- [ ] Search input
- [ ] Select mode
- [ ] Goto navigation
- [ ] Detail panel with full info
- [ ] Image attachment (clipboard + file)
- [ ] Session attach/pause/resume/stop
- [ ] PR creation keybind
- [ ] Toast notifications

**Milestone:** Full keyboard interaction, image attachments work

### Phase 6: Polish (Week 11-12)

- [ ] Custom theme support
- [ ] Error handling and recovery
- [ ] Performance optimization
- [ ] Edge cases and resilience
- [ ] Documentation
- [ ] Distribution (escript + standalone)

**Milestone:** Production ready

---

## 8. Keybindings

### Navigation
| Key | Action |
|-----|--------|
| h/j/k/l | Move cursor |
| g + key | Jump (gb=backlog, gi=in_progress, ir=review, id=done) |
| / | Search |
| Enter | Detail panel |
| Esc | Back to normal / close overlay |

### Actions (Space menu)
| Key | Action |
|-----|--------|
| s | Start Claude session |
| a | Attach to session |
| p | Pause session |
| r | Resume session |
| x | Stop session |
| d | Toggle dev server |
| c | Create PR |
| i | Attach image |

### Bead Operations
| Key | Action |
|-----|--------|
| n | New bead |
| e | Edit bead (in detail) |
| D | Delete bead (confirm) |

### Selection
| Key | Action |
|-----|--------|
| v | Enter select mode |
| Space | Toggle selection |
| Esc | Exit select mode |

### View
| Key | Action |
|-----|--------|
| f | Filter menu |
| , | Sort menu |
| ? | Help |
| R | Refresh |

---

## 9. Success Criteria

### Must Have
- [ ] Kanban board with beads
- [ ] Bead CRUD with image attachments
- [ ] Worktree creation with configurable path
- [ ] Claude session spawn/attach/stop
- [ ] Dev servers with port detection
- [ ] Init commands and background tasks
- [ ] PR creation via keybind
- [ ] Filter and sort
- [ ] Catppuccin theme

### Performance
- [ ] Startup < 300ms
- [ ] Key response < 16ms
- [ ] Memory < 50MB typical

### Quality
- [ ] No UI freezes
- [ ] Graceful error handling
- [ ] Auto-recovery from crashes
- [ ] State reconstruction from tmux on restart

---

## 10. Risks and Mitigations

### Shore Maturity
**Risk:** Young library, may have limitations
**Mitigation:** Fork early, contribute upstream, abstract rendering

### Tmux Flickering
**Risk:** Documented Shore + tmux issues
**Mitigation:** Test in Phase 1, investigate sync output, custom buffering if needed

### Clipboard Cross-Platform
**Risk:** Different tools per platform (pbpaste, wl-paste, xclip)
**Mitigation:** Platform detection module, graceful fallback with clear errors

### Image Rendering
**Risk:** Terminal image support varies (iTerm2, sixel, unicode blocks)
**Mitigation:** Defer image preview to v1.1 if complex, or use external viewer

---

## 11. Next Steps

1. **Create repo:** `azedarach-gleam` (or within existing as `gleam/`)
2. **Spike:** Shore + tmux rendering
3. **Spike:** OTP actor communication pattern
4. **Spike:** Clipboard access on target platforms
5. **Begin Phase 1**

---

*Document version: 3.0.0 - Complete scope with all features*
