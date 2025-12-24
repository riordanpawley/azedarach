# Azedarach Gleam Rewrite Scope Document

**Version:** 1.0.0
**Date:** 2025-12-24
**Status:** Draft

---

## Executive Summary

Full rewrite of Azedarach from TypeScript (Effect + React + OpenTUI) to Gleam using the Shore TUI framework. The goal is to leverage Gleam's type safety, functional purity, and the Erlang VM's concurrency model for a more robust orchestration system.

---

## 1. Project Goals

### Primary Goals

1. **Full Gleam implementation** - No TypeScript/JavaScript in the final product
2. **Shore TUI framework** - Use TEA (The Elm Architecture) for UI
3. **Erlang VM benefits** - Leverage OTP supervision, fault tolerance, lightweight processes
4. **Feature parity** - All existing Azedarach functionality preserved
5. **Improved reliability** - Erlang's "let it crash" philosophy for session management

### Non-Goals

- Hybrid architecture (Gleam backend + React frontend)
- Incremental migration (will be a clean rewrite)
- Supporting non-Erlang compile targets (JavaScript target not used)

---

## 2. Technical Architecture

### 2.1 Runtime Target

**Erlang VM (BEAM)** - Not JavaScript target

Rationale:
- OTP supervision trees for session management
- Lightweight processes for concurrent tmux monitoring
- Built-in fault tolerance
- Hot code reloading potential

### 2.2 Core Architecture Pattern

Replace Effect services with **OTP-style architecture**:

```
Current (TypeScript/Effect)          Gleam/OTP Equivalent
─────────────────────────────        ────────────────────
Effect.Service                   →   Gleam OTP Actor (gleam_otp)
SubscriptionRef                  →   Actor state + message passing
Layer (dependency injection)     →   Supervisor children / process registry
PubSub                          →   gleam_erlang process messaging
Effect.fork / forkScoped        →   OTP supervised processes
Schedule.spaced                 →   erlang:send_after / timer
```

### 2.3 UI Framework

**Shore** (https://github.com/bgwdotdev/shore)

- TEA architecture (Model, Update, View)
- Terminal rendering with synchronized output
- Keybinding system for modal editing

### 2.4 State Management

Replace three-layer architecture with TEA:

```
Current                              Gleam/Shore
───────                              ──────────
React Components (render)        →   Shore view function
Atoms (derived state)            →   Model derivations in view
Effect Services (state + logic)  →   OTP Actors + Shore Model
```

### 2.5 Process Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     Application Supervisor                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │ Shore App    │  │ Session      │  │ Beads        │          │
│  │ (UI Process) │  │ Supervisor   │  │ Client       │          │
│  └──────────────┘  └──────────────┘  └──────────────┘          │
│         │                 │                 │                    │
│         │          ┌──────┴──────┐          │                    │
│         │          │             │          │                    │
│         │    ┌─────┴────┐ ┌─────┴────┐     │                    │
│         │    │ Session  │ │ Session  │     │                    │
│         │    │ Monitor  │ │ Monitor  │     │                    │
│         │    │ (task-1) │ │ (task-2) │     │                    │
│         │    └──────────┘ └──────────┘     │                    │
│         │                                   │                    │
│  ┌──────┴───────────────────────────────────┴──────┐            │
│  │              Message Bus (process registry)      │            │
│  └──────────────────────────────────────────────────┘            │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │ Tmux         │  │ Git          │  │ PR           │          │
│  │ Service      │  │ Service      │  │ Workflow     │          │
│  └──────────────┘  └──────────────┘  └──────────────┘          │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 3. Module Breakdown

### 3.1 Core Modules

| Module | Purpose | Lines Est. | Priority |
|--------|---------|------------|----------|
| `azedarach` | Application entry, supervisor setup | 200 | P0 |
| `azedarach/config` | Configuration loading, schema | 400 | P0 |
| `azedarach/cli` | CLI argument parsing | 200 | P0 |

### 3.2 Service Modules (OTP Actors)

| Module | Purpose | Lines Est. | Priority |
|--------|---------|------------|----------|
| `azedarach/services/session_manager` | Claude session lifecycle | 600 | P0 |
| `azedarach/services/session_monitor` | PTY output polling per session | 500 | P0 |
| `azedarach/services/beads_client` | bd CLI wrapper | 400 | P0 |
| `azedarach/services/tmux` | tmux command execution | 500 | P0 |
| `azedarach/services/worktree` | Git worktree management | 600 | P1 |
| `azedarach/services/pr_workflow` | GitHub PR automation | 400 | P1 |
| `azedarach/services/state_detector` | Output pattern matching | 300 | P0 |

### 3.3 UI Modules (Shore)

| Module | Purpose | Lines Est. | Priority |
|--------|---------|------------|----------|
| `azedarach/ui/app` | Root Shore application | 400 | P0 |
| `azedarach/ui/model` | Application state (Model) | 500 | P0 |
| `azedarach/ui/update` | Message handlers (Update) | 800 | P0 |
| `azedarach/ui/view` | Render functions (View) | 600 | P0 |
| `azedarach/ui/board` | Kanban board view | 400 | P0 |
| `azedarach/ui/task_card` | Task card component | 200 | P0 |
| `azedarach/ui/status_bar` | Bottom status bar | 150 | P1 |
| `azedarach/ui/overlays/*` | Modal overlays | 800 | P1 |
| `azedarach/ui/keyboard` | Keybinding definitions | 300 | P0 |

### 3.4 Domain Modules

| Module | Purpose | Lines Est. | Priority |
|--------|---------|------------|----------|
| `azedarach/domain/task` | Task types and operations | 300 | P0 |
| `azedarach/domain/session` | Session types and states | 250 | P0 |
| `azedarach/domain/bead` | Bead schema types | 200 | P0 |
| `azedarach/domain/filter` | Filter/sort logic | 300 | P1 |

### 3.5 Utility Modules

| Module | Purpose | Lines Est. | Priority |
|--------|---------|------------|----------|
| `azedarach/util/shell` | Shell command execution | 200 | P0 |
| `azedarach/util/json` | JSON encoding/decoding | 150 | P0 |
| `azedarach/util/regex` | Pattern matching utilities | 200 | P0 |
| `azedarach/util/time` | Time formatting | 100 | P1 |

**Total Estimated Lines:** ~8,500 (vs 33K TypeScript - reduction due to Gleam's conciseness and no React boilerplate)

---

## 4. Dependencies

### 4.1 Required Gleam Packages

```toml
# gleam.toml
[dependencies]
gleam_stdlib = "~> 0.40"
gleam_erlang = "~> 0.27"
gleam_otp = "~> 0.12"
gleam_json = "~> 2.0"
gleam_http = "~> 3.6"
shore = "~> 1.3"
simplifile = "~> 2.0"       # File system operations
shellout = "~> 1.6"         # Shell command execution
tom = "~> 1.0"              # TOML parsing (for config)
argv = "~> 1.0"             # CLI argument parsing
glint = "~> 1.0"            # CLI framework (alternative)

[dev-dependencies]
gleeunit = "~> 1.0"
startest = "~> 0.3"         # Property-based testing
```

### 4.2 External CLI Dependencies (unchanged)

- `tmux` - Session persistence
- `git` - Worktree management
- `bd` - Beads CLI
- `gh` - GitHub CLI

---

## 5. Key Technical Decisions

### 5.1 Actor vs Module Pattern

**Decision:** Use OTP Actors (via `gleam_otp`) for stateful services

```gleam
// Session monitor as an OTP actor
pub type Message {
  Poll
  GetState(reply_to: process.Subject(SessionState))
  UpdateState(SessionState)
  Stop
}

pub fn start(session_id: String) -> Result(Subject(Message), StartError) {
  actor.start_spec(actor.Spec(
    init: fn() { init(session_id) },
    loop: handle_message,
    init_timeout: 5000,
  ))
}
```

### 5.2 State Pattern Matching

**Decision:** Leverage Gleam's exhaustive pattern matching for session states

```gleam
pub type SessionState {
  Idle
  Initializing
  Busy(output: String, phase: AgentPhase)
  Waiting(prompt: String)
  Done(summary: String)
  Error(message: String)
  Paused
}

pub type AgentPhase {
  Planning
  Action
  Verification
  PlanMode
  PhaseIdle
}
```

### 5.3 Modal Keyboard Handling

**Decision:** TEA messages for mode transitions

```gleam
pub type Mode {
  Normal
  Select(selected: Set(String))
  Goto(pending: Option(String))
  Action
  Search(query: String)
  Command(input: String)
  Sort
  Filter
  Orchestrate(epic_id: String)
}

pub type Msg {
  KeyPressed(key: Key)
  ModeChanged(Mode)
  // ... other messages
}
```

### 5.4 Tmux Integration

**Decision:** Direct shell execution via `shellout`

```gleam
pub fn capture_pane(session: String, lines: Int) -> Result(String, Error) {
  shellout.command(
    run: "tmux",
    with: ["capture-pane", "-t", session, "-p", "-S", int.to_string(-lines)],
    in: ".",
    opt: [],
  )
}
```

### 5.5 Configuration

**Decision:** TOML config with Gleam types

```gleam
pub type Config {
  Config(
    session: SessionConfig,
    pr: PRConfig,
    hooks: HooksConfig,
  )
}

pub fn load(path: String) -> Result(Config, ConfigError) {
  use content <- result.try(simplifile.read(path))
  use toml <- result.try(tom.parse(content))
  decode_config(toml)
}
```

---

## 6. Migration Strategy

### Phase 1: Foundation (Week 1-2)

**Goal:** Minimal running application

- [ ] Project scaffolding (`gleam new azedarach_gleam`)
- [ ] Shore integration and basic window
- [ ] Configuration loading
- [ ] CLI argument parsing
- [ ] Basic Model/Update/View structure
- [ ] Empty kanban board rendering

**Milestone:** App starts, shows empty board

### Phase 2: Core Services (Week 3-4)

**Goal:** Session management working

- [ ] Tmux service (commands, capture)
- [ ] Beads client (bd CLI wrapper)
- [ ] State detector (pattern matching)
- [ ] Session monitor actor
- [ ] Session manager supervisor
- [ ] Task loading and display

**Milestone:** Board shows tasks from beads

### Phase 3: Session Lifecycle (Week 5-6)

**Goal:** Can spawn and monitor Claude sessions

- [ ] Worktree manager
- [ ] Session spawning (Space+s)
- [ ] PTY output monitoring
- [ ] State detection (busy/waiting/done/error)
- [ ] Status updates in UI

**Milestone:** Can spawn Claude session, see state changes

### Phase 4: Keyboard & Navigation (Week 7-8)

**Goal:** Full keyboard navigation

- [ ] Modal editing state machine
- [ ] Normal mode (hjkl navigation)
- [ ] Action mode (space menu)
- [ ] Goto mode (jump labels)
- [ ] Search mode (filtering)
- [ ] Select mode (multi-select)

**Milestone:** Full keyboard navigation working

### Phase 5: Advanced Features (Week 9-10)

**Goal:** PR workflow, overlays

- [ ] PR workflow service
- [ ] Help overlay
- [ ] Settings overlay
- [ ] Filter/sort menus
- [ ] Toast notifications
- [ ] Detail panel

**Milestone:** Feature parity with TypeScript version

### Phase 6: Polish & Testing (Week 11-12)

**Goal:** Production ready

- [ ] Error handling and recovery
- [ ] Comprehensive tests
- [ ] Performance tuning
- [ ] Documentation
- [ ] Migration guide

**Milestone:** Ready for production use

---

## 7. Risk Mitigation

### Risk 1: Shore Immaturity

**Concern:** Shore is relatively new, may have bugs/limitations

**Mitigation:**
- Fork Shore early, prepare to contribute fixes upstream
- Build abstraction layer for rendering primitives
- Have fallback plan: raw termbox bindings via FFI

### Risk 2: Tmux Flickering

**Concern:** Documented Shore + tmux flickering

**Mitigation:**
- Investigate synchronized output settings
- Implement custom double-buffering if needed
- Test early in Phase 1

### Risk 3: Missing Gleam Libraries

**Concern:** May need functionality not available in Gleam ecosystem

**Mitigation:**
- Erlang FFI is straightforward in Gleam
- Can call any Erlang/OTP library directly
- Maintain list of required FFI bindings

### Risk 4: Regex Performance

**Concern:** 40+ patterns for state detection

**Mitigation:**
- Pre-compile patterns at startup
- Use Erlang `:re` module directly if needed
- Consider alternative parsing approaches

### Risk 5: Learning Curve

**Concern:** Team familiarity with Gleam/OTP

**Mitigation:**
- Document patterns as we go
- Pair programming on complex sections
- Weekly architecture reviews

---

## 8. Success Criteria

### Functional Requirements

- [ ] Display kanban board with tasks from beads
- [ ] Spawn Claude sessions in worktrees
- [ ] Monitor session state (busy/waiting/done/error)
- [ ] Full keyboard navigation (all 8 modes)
- [ ] Create GitHub PRs on completion
- [ ] Filter and sort tasks
- [ ] Epic drill-down view
- [ ] Multi-select operations

### Non-Functional Requirements

- [ ] Startup time < 500ms
- [ ] UI refresh rate 60fps equivalent
- [ ] Memory usage < 100MB
- [ ] Graceful degradation on errors
- [ ] No UI freezes during operations

### Quality Requirements

- [ ] > 80% test coverage on services
- [ ] Zero runtime crashes in normal operation
- [ ] All keyboard shortcuts documented

---

## 9. Open Questions

1. **Shore customization** - How much can we customize Shore's rendering? May need fork.

2. **Image support** - Current version has terminal image support. Shore capability?

3. **Hot reload** - Can we leverage Erlang hot code reloading for development?

4. **Distribution** - Single binary via escriptize? Or require Erlang runtime?

5. **Windows support** - Shore appears Linux/macOS focused. Acceptable?

---

## 10. Next Steps

1. **Spike: Shore hello world** - Verify Shore works in our environment
2. **Spike: tmux flickering** - Test Shore + tmux rendering
3. **Spike: OTP actor pattern** - Prototype session monitor actor
4. **Decision: Config format** - TOML vs JSON vs custom
5. **Begin Phase 1** - Project scaffolding

---

## Appendix A: Module Dependency Graph

```
azedarach (entry)
├── cli
├── config
└── app
    ├── ui/app (Shore)
    │   ├── ui/model
    │   ├── ui/update
    │   ├── ui/view
    │   │   ├── ui/board
    │   │   ├── ui/task_card
    │   │   └── ui/status_bar
    │   └── ui/keyboard
    │
    └── services (OTP Supervisor)
        ├── session_manager
        │   └── session_monitor (per session)
        ├── beads_client
        ├── tmux
        ├── worktree
        ├── pr_workflow
        └── state_detector
```

## Appendix B: Message Flow Example

```
User presses 'j' (move down)
    │
    ▼
Shore key event → Msg::KeyPressed(Key::Char('j'))
    │
    ▼
update(model, msg) pattern match
    │
    ▼
case Mode::Normal → handle_normal_key('j')
    │
    ▼
Model { cursor: Cursor { task_index: model.cursor.task_index + 1, ..} }
    │
    ▼
Shore re-renders view(model)
    │
    ▼
Board displays new cursor position
```

## Appendix C: TypeScript → Gleam Patterns

| TypeScript/Effect | Gleam Equivalent |
|-------------------|------------------|
| `Effect.gen(function* () { ... })` | `use x <- result.try(...)` |
| `yield* SomeService` | Actor message or direct call |
| `SubscriptionRef.make(x)` | Actor state |
| `SubscriptionRef.update(ref, f)` | `actor.send(self, Update(f))` |
| `Effect.fork` | `process.start` |
| `Effect.forkScoped` | Supervised child process |
| `Layer.mergeAll(...)` | Supervisor children |
| `Schema.decode` | Custom decoder function |
| `pipe(x, f, g, h)` | `x \|> f \|> g \|> h` |
| `Option.some(x)` | `option.Some(x)` |
| `Result.ok(x)` | `Ok(x)` |

---

*Document maintained in: `docs/gleam-rewrite-scope.md`*
