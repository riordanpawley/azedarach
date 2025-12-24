# Azedarach Gleam Rewrite Scope Document

**Version:** 2.0.0
**Date:** 2025-12-24
**Status:** Draft (Refined)

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

### What We're Building

A TUI kanban board that:
- Shows tasks from beads
- Spawns Claude Code sessions in git worktrees
- Monitors session state (busy/waiting/done/error)
- Provides keyboard-driven navigation and actions

### Deferred to v2

- Epic orchestration / swarm pattern
- Auto PR creation (v1 has manual keybind)
- Image attachments

### Non-Goals

- Direct port of TypeScript code
- Hybrid architecture
- Feature bloat

---

## 2. Technical Architecture

### 2.1 The Five Actors

Replace 43 Effect services with **5 OTP actors**:

```
┌─────────────────────────────────────────────────────────────┐
│                    Shore Application                         │
│         (TEA: Model + Update + View = all UI state)         │
└─────────────────────────────────────────────────────────────┘
                            │
                    messages│(Gleam process messaging)
                            ▼
┌─────────────────────────────────────────────────────────────┐
│                    Coordinator Actor                         │
│  - Owns task list cache                                      │
│  - Routes commands to services                               │
│  - Aggregates state updates for UI                           │
│  - Single point of coordination                              │
└─────────────────────────────────────────────────────────────┘
          │              │              │              │
          ▼              ▼              ▼              ▼
    ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐
    │ Sessions │  │ Worktree │  │  Beads   │  │   Git    │
    │Supervisor│  │ (module) │  │ (module) │  │ (module) │
    └──────────┘  └──────────┘  └──────────┘  └──────────┘
          │
          ▼ (dynamic children)
    ┌──────────┐
    │ Monitor  │ ←── one per active Claude session
    │ (actor)  │     polls tmux, detects state
    └──────────┘
```

**Actor Responsibilities:**

| Actor | State | Purpose |
|-------|-------|---------|
| Shore App | TEA Model | All UI state, rendering |
| Coordinator | Task cache, session registry | Central orchestration |
| Sessions Supervisor | Child monitors | Supervises per-session monitors |
| Session Monitor | Output buffer, detected state | Polls tmux, pattern matching |

**Stateless Modules** (not actors, just functions):
- Worktree: `create/1`, `remove/1`, `status/1`
- Beads: `list/0`, `show/1`, `update/2`
- Git: `status/1`, `diff/1`, `pr_create/2`
- Tmux: `new_session/2`, `capture_pane/2`, `send_keys/2`

### 2.2 State Architecture

**Single TEA Model replaces three-layer architecture:**

```gleam
pub type Model {
  Model(
    // Core data
    tasks: List(Task),
    sessions: Dict(String, SessionState),

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

    // Meta
    loading: Bool,
    last_error: Option(String),
  )
}
```

**No more:**
- SubscriptionRef scattered across 43 services
- Atoms bridging Effect to React
- Three-layer reactive updates

### 2.3 Modes, Inputs, and Overlays

Current "8 modes" are actually:

| Current Name | Actually Is | Gleam Representation |
|--------------|-------------|---------------------|
| normal | Mode | `Mode::Normal` |
| select | Mode | `Mode::Select(Set(String))` |
| goto | Pending chord | `pending_key: Some("g")` |
| action | Overlay | `overlay: Some(ActionMenu)` |
| search | Input state | `input: Some(Search(query))` |
| command | Input state | `input: Some(Command(text))` |
| sort | Overlay | `overlay: Some(SortMenu)` |
| filter | Overlay | `overlay: Some(FilterMenu)` |

**Simplified model:**

```gleam
pub type Mode {
  Normal
  Select(selected: Set(String))
}

pub type InputState {
  Search(query: String)
  Command(text: String)
}

pub type Overlay {
  ActionMenu
  SortMenu
  FilterMenu
  HelpOverlay
  DetailPanel(task_id: String)
  ConfirmDialog(action: PendingAction)
}
```

**Rules:**
- One overlay at a time (no stacking)
- Escape always returns to Normal mode, clears input/overlay
- Overlays are full-screen or side panels (not floating)

### 2.4 OTP Supervision Strategy

**Session Monitor crashes:**

```
Crash detected
    │
    ▼
Supervisor auto-restarts monitor
    │
    ▼
Monitor re-initializes, polls tmux for current state
    │
    ▼
UI shows "refreshing..." for 2 seconds
    │
    ▼
Normal operation resumes

If 3 crashes in 60 seconds:
    │
    ▼
Mark session as "unknown" state
    │
    ▼
Surface warning to user via toast
```

Rationale: Session monitors are stateless observers. Tmux holds the real state. Crashing and restarting loses nothing.

### 2.5 Message Flow

```
User presses Space (action menu)
    │
    ▼
Shore captures key → Msg::KeyPressed(Space)
    │
    ▼
update(model, KeyPressed(Space))
    │
    ▼
Pattern match: mode is Normal, no pending_key
    │
    ▼
Return Model { overlay: Some(ActionMenu), ..model }
    │
    ▼
view(model) renders ActionMenu overlay
```

```
User selects "Start Session" from action menu
    │
    ▼
Msg::ActionSelected(StartSession)
    │
    ▼
update sends message to Coordinator: SpawnSession(task_id)
    │
    ▼
Coordinator:
  1. Calls Worktree.create(task)
  2. Calls Tmux.new_session(name, cwd)
  3. Starts new Monitor under Sessions Supervisor
  4. Sends SessionStarted(task_id) back to Shore app
    │
    ▼
Model updates, view re-renders with session indicator
```

---

## 3. Module Structure

### 3.1 Core Application

```
src/
├── azedarach.gleam              # Entry point, supervisor setup
├── cli.gleam                    # CLI argument parsing
└── config.gleam                 # Configuration loading
```

### 3.2 UI (Shore/TEA)

```
src/ui/
├── app.gleam                    # Shore application setup
├── model.gleam                  # Model type definitions
├── update.gleam                 # Update function (message handling)
├── view.gleam                   # Main view function
├── view/
│   ├── board.gleam              # Kanban board rendering
│   ├── card.gleam               # Task card rendering
│   ├── status_bar.gleam         # Bottom status bar
│   └── overlays.gleam           # All overlay views
└── keys.gleam                   # Keybinding definitions
```

### 3.3 Actors

```
src/actors/
├── coordinator.gleam            # Central orchestration actor
├── sessions_supervisor.gleam    # Dynamic supervisor for monitors
└── session_monitor.gleam        # Per-session PTY monitor
```

### 3.4 Services (Stateless Modules)

```
src/services/
├── beads.gleam                  # bd CLI wrapper
├── tmux.gleam                   # tmux command execution
├── worktree.gleam               # Git worktree operations
├── git.gleam                    # Git commands (status, diff, PR)
└── state_detector.gleam         # Output pattern matching
```

### 3.5 Domain Types

```
src/domain/
├── task.gleam                   # Task type, operations
├── session.gleam                # Session state types
└── bead.gleam                   # Bead schema (from bd)
```

### 3.6 Utilities

```
src/util/
├── shell.gleam                  # Shell command execution wrapper
└── time.gleam                   # Time formatting
```

**Estimated total: ~5,000-6,000 lines** (down from 33K TypeScript)

---

## 4. Dependencies

```toml
# gleam.toml
[dependencies]
gleam_stdlib = "~> 0.40"
gleam_erlang = "~> 0.27"
gleam_otp = "~> 0.12"
gleam_json = "~> 2.0"
shore = "~> 1.3"
simplifile = "~> 2.0"
shellout = "~> 1.6"
tom = "~> 1.0"
argv = "~> 1.0"

[dev-dependencies]
gleeunit = "~> 1.0"
```

**External CLI tools:** tmux, git, bd, gh

---

## 5. Distribution

**Goal:** Works both ways

1. **With Erlang runtime** - `gleam run` or escript
2. **Single binary** - Burrito or similar for self-contained executable

Package both:
- `az` - requires Erlang/OTP installed
- `az-standalone` - bundled runtime (~50MB)

---

## 6. Implementation Phases

### Phase 1: Foundation (Week 1-2)

**Goal:** Shore app renders empty board

- [ ] `gleam new azedarach`
- [ ] Shore integration, basic window
- [ ] TEA skeleton (Model/Update/View)
- [ ] Empty kanban board with 4 columns
- [ ] Status bar
- [ ] Basic keyboard navigation (hjkl)

**Milestone:** App starts, shows hardcoded empty columns, can move cursor

### Phase 2: Data Flow (Week 3-4)

**Goal:** Real tasks from beads displayed

- [ ] Beads module (`bd list --json` parsing)
- [ ] Coordinator actor (owns task cache)
- [ ] Task card rendering
- [ ] Periodic refresh (poll beads every 30s)
- [ ] Loading states

**Milestone:** Board shows real tasks from beads

### Phase 3: Session Management (Week 5-6)

**Goal:** Can spawn and monitor Claude sessions

- [ ] Tmux module (new_session, capture_pane, send_keys)
- [ ] Worktree module (create, status)
- [ ] Sessions supervisor
- [ ] Session monitor actor (polling, state detection)
- [ ] State detector (pattern matching on output)
- [ ] Session state display on cards

**Milestone:** Press keybind → worktree created → Claude spawns → state updates in UI

### Phase 4: Interaction (Week 7-8)

**Goal:** Full keyboard interaction

- [ ] Action menu overlay (Space)
- [ ] Search input (/)
- [ ] Filter overlay (f)
- [ ] Sort overlay (,)
- [ ] Select mode (v)
- [ ] Goto navigation (g)
- [ ] Help overlay (?)
- [ ] Detail panel (Enter)

**Milestone:** All keyboard interactions working

### Phase 5: Operations (Week 9-10)

**Goal:** Full workflow support

- [ ] Session attach (a)
- [ ] Session pause/resume
- [ ] Session stop
- [ ] PR creation (manual keybind)
- [ ] Worktree cleanup
- [ ] Toast notifications
- [ ] Confirm dialogs for destructive actions

**Milestone:** Complete workflow: spawn → monitor → create PR → cleanup

### Phase 6: Polish (Week 11-12)

**Goal:** Production ready

- [ ] Error handling and recovery
- [ ] Performance optimization
- [ ] Edge cases
- [ ] Documentation
- [ ] Distribution packaging (escript + Burrito)

**Milestone:** Ready for daily use

---

## 7. Keybinding Design

Organize keybindings into logical groups:

### Navigation
| Key | Action |
|-----|--------|
| h/j/k/l | Move cursor |
| g + column | Jump to column (gb, gi, ip, id) |
| / | Search |
| Enter | Open detail panel |
| Esc | Back to normal |

### Actions (Space menu)
| Key | Action |
|-----|--------|
| s | Start session |
| a | Attach to session |
| p | Pause session |
| r | Resume session |
| x | Stop session |
| c | Create PR |

### Selection
| Key | Action |
|-----|--------|
| v | Enter select mode |
| Space | Toggle selection |
| a | Select all in column |
| A | Select all |
| Esc | Clear selection |

### View
| Key | Action |
|-----|--------|
| f | Filter menu |
| , | Sort menu |
| ? | Help |
| R | Refresh |

---

## 8. Success Criteria

### Must Have (v1.0)
- [ ] Display tasks from beads in kanban columns
- [ ] Spawn Claude session in worktree
- [ ] Monitor session state
- [ ] Attach to running session
- [ ] Create PR via keybind
- [ ] Filter and sort tasks
- [ ] Keyboard-only navigation

### Performance
- [ ] Startup < 300ms
- [ ] Key response < 16ms (60fps feel)
- [ ] Memory < 50MB typical usage

### Quality
- [ ] No UI freezes
- [ ] Graceful error handling
- [ ] Auto-recovery from transient failures

---

## 9. Open Questions

1. **Config format** - Keep TOML? Switch to JSON for bd compatibility?

2. **Keybind customization** - Allow user keybind overrides in config?

3. **Theme support** - Hardcode colors or allow customization?

4. **Multi-project** - Support switching between projects, or one instance per project?

5. **Session hooks** - Do we need the complex hook system from TS version, or simpler approach?

---

## 10. Risks and Mitigations

### Shore Maturity
**Risk:** Shore is young, may have limitations
**Mitigation:** Fork early, contribute fixes upstream. Abstract rendering primitives.

### Tmux Flickering
**Risk:** Documented Shore + tmux issues
**Mitigation:** Test in Phase 1. Investigate synchronized output. Custom buffering if needed.

### Gleam Ecosystem Gaps
**Risk:** Missing libraries
**Mitigation:** Erlang FFI is straightforward. Can call any OTP library directly.

---

## 11. Next Steps

1. **Create new repo:** `azedarach-gleam` (clean slate)
2. **Spike:** Shore hello world + tmux rendering test
3. **Spike:** Coordinator + Monitor actor communication
4. **Begin Phase 1**

---

## Appendix: Why This Architecture

### 43 Services → 5 Actors

The TypeScript version evolved organically, adding services for each concern. This created:
- Complex dependency graphs
- State scattered across SubscriptionRefs
- Difficult-to-trace data flow
- Performance overhead from reactive updates

The Gleam version inverts this:
- **Coordinator** is the single source of truth for app state coordination
- **TEA Model** is the single source of truth for UI state
- Services are stateless functions, not actors
- Only session monitors need actor state (and they're supervised)

### TEA vs Effect Three-Layer

| Effect/React | TEA |
|--------------|-----|
| State in services → atoms → React | State in Model |
| Updates via SubscriptionRef.set | Updates via messages |
| Derived state in atoms | Derived state in view functions |
| Side effects via Effect.gen | Side effects via Cmd |
| Complex reactive graph | Linear message flow |

TEA is simpler because there's one update function, one model, one view. No bridging layers.

### OTP vs Effect Concurrency

| Effect | OTP |
|--------|-----|
| Fibers (cooperative) | Processes (preemptive) |
| Manual scoping | Supervision trees |
| Layer composition | Process registry |
| Schedule.spaced | send_after / timer |

OTP's preemptive scheduling means one slow operation can't block the UI. Supervision trees mean automatic recovery from failures.

---

*Document version: 2.0.0 - Refined after requirements discussion*
