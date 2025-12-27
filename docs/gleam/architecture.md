# Azedarach Gleam Architecture

## Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              TERMINAL (Shore)                                │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                           Shore Application                            │  │
│  │                    TEA: Model → Update → View                          │  │
│  │                                                                        │  │
│  │  ┌─────────┐  ┌─────────┐  ┌─────────┐  ┌─────────┐                   │  │
│  │  │ backlog │  │in_prog  │  │ review  │  │  done   │  ← Kanban Board   │  │
│  │  │ ┌─────┐ │  │ ┌─────┐ │  │ ┌─────┐ │  │ ┌─────┐ │                   │  │
│  │  │ │Task │ │  │ │Task │ │  │ │Task │ │  │ │Task │ │                   │  │
│  │  │ └─────┘ │  │ └─────┘ │  │ └─────┘ │  │ └─────┘ │                   │  │
│  │  └─────────┘  └─────────┘  └─────────┘  └─────────┘                   │  │
│  │                                                                        │  │
│  │  [Status Bar: mode | project | session state | dev server port]       │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      │ Messages (Gleam process)
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            OTP Application                                   │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                         Coordinator Actor                              │  │
│  │                                                                        │  │
│  │  State:                        Responsibilities:                       │  │
│  │  - tasks: List(Task)           - Route commands to services           │  │
│  │  - sessions: Dict(id, state)   - Aggregate state for UI               │  │
│  │  - dev_servers: Dict(id, st)   - Manage optimistic updates            │  │
│  │  - projects: List(Project)     - Periodic beads refresh               │  │
│  │                                                                        │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│           │                    │                    │                        │
│           ▼                    ▼                    ▼                        │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐              │
│  │    Sessions     │  │   Dev Servers   │  │   (Stateless    │              │
│  │   Supervisor    │  │   Supervisor    │  │    Modules)     │              │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘              │
│           │                    │                    │                        │
│           ▼                    ▼                    ▼                        │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐              │
│  │ Session Monitor │  │ Server Monitor  │  │ • Beads         │              │
│  │ (per session)   │  │ (per server)    │  │ • Tmux          │              │
│  │                 │  │                 │  │ • Worktree      │              │
│  │ - polls tmux    │  │ - tracks port   │  │ • Git           │              │
│  │ - detects state │  │ - tracks status │  │ • PR            │              │
│  │ - sends updates │  │                 │  │ • Clipboard     │              │
│  └─────────────────┘  └─────────────────┘  │ • Image         │              │
│                                            └─────────────────┘              │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      │ Shell Commands
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           External Systems                                   │
│                                                                              │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌───────────┐ │
│  │   tmux    │  │    git    │  │  bd CLI   │  │  gh CLI   │  │ clipboard │ │
│  │           │  │           │  │  (beads)  │  │ (GitHub)  │  │  tools    │ │
│  └───────────┘  └───────────┘  └───────────┘  └───────────┘  └───────────┘ │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                         tmux Sessions                                  │  │
│  │  ┌─────────────────────────────────────────────────────────────────┐  │  │
│  │  │ Session: {bead-id}-az                                           │  │  │
│  │  │ ├── Window: main       → Claude Code running                    │  │  │
│  │  │ ├── Window: dev-web    → npm run dev (PORT=3000)                │  │  │
│  │  │ ├── Window: dev-api    → npm run api (PORT=8000)                │  │  │
│  │  │ ├── Window: task-1     → npm run watch (bg task)                │  │  │
│  │  │ └── Window: task-2     → npm run test:watch (bg task)           │  │  │
│  │  └─────────────────────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                         Git Worktrees                                  │  │
│  │  ../project-az-123/  ← isolated git environment per bead             │  │
│  │  ../project-az-456/                                                   │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │                         Beads Database                                 │  │
│  │  .beads/                                                              │  │
│  │  ├── issues.jsonl      ← task data                                   │  │
│  │  └── images/           ← attachments                                 │  │
│  │      └── {bead-id}/                                                  │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Message Flow

### User Keypress → State Update

```
User presses 'j' (move down)
         │
         ▼
┌─────────────────────────┐
│ Shore Key Event         │
│ Msg::KeyPressed("j")    │
└─────────────────────────┘
         │
         ▼
┌─────────────────────────┐
│ update(model, msg)      │
│ Pattern match on mode   │
│ → Normal mode           │
│ → key = 'j'             │
│ → move cursor down      │
└─────────────────────────┘
         │
         ▼
┌─────────────────────────┐
│ New Model               │
│ cursor.task_index += 1  │
└─────────────────────────┘
         │
         ▼
┌─────────────────────────┐
│ view(model)             │
│ Render updated board    │
└─────────────────────────┘
```

### Start Session Flow

```
User presses Space+s
         │
         ▼
┌─────────────────────────┐
│ update: Show ActionMenu │
└─────────────────────────┘
         │
User presses 's'
         │
         ▼
┌─────────────────────────┐
│ update: StartSession    │
│ Send to Coordinator     │
└─────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│ Coordinator Actor                                            │
│                                                              │
│ 1. Check if session exists                                   │
│    └─→ If yes: just attach                                  │
│                                                              │
│ 2. Worktree.create(bead_id, template)                       │
│    └─→ git worktree add ../project-{bead-id}                │
│                                                              │
│ 3. Tmux.new_session("{bead-id}-az", worktree_path)          │
│    └─→ tmux new-session -d -s {bead-id}-az -c {path}        │
│                                                              │
│ 4. Run init commands (ONCE, sequentially)                    │
│    └─→ direnv allow → wait prompt                           │
│    └─→ bun install → wait prompt                            │
│    └─→ bd sync → wait prompt                                │
│    └─→ Set @az_init_done marker                             │
│                                                              │
│ 5. Create main window with "claude"                          │
│                                                              │
│ 6. Create background task windows (parallel)                 │
│    └─→ Wait for init marker                                 │
│    └─→ Run task command                                     │
│    └─→ Window closes on success                             │
│                                                              │
│ 7. Start SessionMonitor actor                                │
│    └─→ Supervised under Sessions Supervisor                 │
│                                                              │
│ 8. Send SessionStarted to Shore app                          │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────┐
│ Model updates           │
│ sessions[id] = Busy     │
│ Toast: "Started..."     │
└─────────────────────────┘
```

### Session Monitoring

```
┌─────────────────────────────────────────────────────────────┐
│ Session Monitor (per session)                                │
│                                                              │
│ Every 500ms (configurable):                                  │
│                                                              │
│ 1. Tmux.capture_pane(session, 50 lines)                     │
│    └─→ tmux capture-pane -t {session} -p -S -50             │
│                                                              │
│ 2. StateDetector.detect(output)                              │
│    └─→ Pattern match for:                                   │
│        - Waiting: [y/n], Do you want, AskUserQuestion       │
│        - Done: Task completed, Successfully                  │
│        - Error: Error:, Exception:, Failed:                  │
│        - Busy: (default if output present)                  │
│                                                              │
│ 3. If state changed:                                         │
│    └─→ Send StateChanged(session_id, new_state) to Coord    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────┐
│ Coordinator updates     │
│ sessions[id] = Waiting  │
└─────────────────────────┘
         │
         ▼
┌─────────────────────────┐
│ Shore receives update   │
│ Model.sessions updated  │
│ Card shows new state    │
└─────────────────────────┘
```

## Data Flow

### State Sources

```
┌──────────────────────────────────────────────────────────────┐
│                    STATE HIERARCHY                            │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  Priority 1: TMUX (Source of Truth)                         │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ • Session exists?    → tmux has-session -t {id}        │ │
│  │ • Session output?    → tmux capture-pane               │ │
│  │ • Window exists?     → tmux list-windows               │ │
│  │                                                        │ │
│  │ On app restart: reconstruct state from tmux            │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
│  Priority 2: IN-MEMORY (Optimistic Updates)                 │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ • TEA Model          → all UI state                    │ │
│  │ • Coordinator state  → task cache, session registry    │ │
│  │ • Derived state      → filtered/sorted lists           │ │
│  │                                                        │ │
│  │ Fast updates, may be stale                             │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
│  Priority 3: FILES (Last Resort)                            │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ • Config             → .azedarach.json                 │ │
│  │ • Image attachments  → .beads/images/                  │ │
│  │ • Beads data         → via bd CLI (not direct)         │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

## Module Dependencies

```
azedarach.gleam (entry)
├── cli.gleam
├── config.gleam
│
└── ui/
    ├── app.gleam ─────────────────┐
    │   ├── model.gleam            │
    │   ├── update.gleam ──────────┼─→ actors/coordinator.gleam
    │   ├── view.gleam             │
    │   │   ├── board.gleam        │
    │   │   ├── card.gleam         │
    │   │   ├── status_bar.gleam   │
    │   │   └── overlays.gleam     │
    │   ├── keys.gleam             │
    │   └── theme.gleam            │
    │                              │
    └───────────────────────────────┘

actors/
├── coordinator.gleam ─────────────┬─→ services/*.gleam
├── sessions_sup.gleam             │
│   └── session_monitor.gleam ─────┤
├── servers_sup.gleam              │
│   └── server_monitor.gleam ──────┘
│
services/
├── beads.gleam      → bd CLI
├── tmux.gleam       → tmux commands
├── worktree.gleam   → git worktree
├── git.gleam        → git commands
├── pr.gleam         → gh CLI
├── clipboard.gleam  → pbpaste/wl-paste/xclip
├── state_detector.gleam
└── image.gleam

domain/
├── task.gleam
├── session.gleam
├── bead.gleam
├── project.gleam
└── attachment.gleam

util/
├── shell.gleam
├── time.gleam
└── platform.gleam
```

## OTP Supervision Tree

```
┌─────────────────────────────────────────────────────────────┐
│                    Application Supervisor                    │
│                    (one_for_one strategy)                    │
└─────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
        ▼                     ▼                     ▼
┌───────────────┐    ┌───────────────┐    ┌───────────────┐
│  Shore App    │    │  Coordinator  │    │   Sessions    │
│  (permanent)  │    │  (permanent)  │    │  Supervisor   │
└───────────────┘    └───────────────┘    │  (permanent)  │
                                          └───────────────┘
                                                  │
                           ┌──────────────────────┼──────────────────────┐
                           │                      │                      │
                           ▼                      ▼                      ▼
                    ┌─────────────┐        ┌─────────────┐        ┌─────────────┐
                    │  Session    │        │  Session    │        │   Dev       │
                    │  Monitor    │        │  Monitor    │        │  Servers    │
                    │  (az-123)   │        │  (az-456)   │        │  Supervisor │
                    │  transient  │        │  transient  │        │  permanent  │
                    └─────────────┘        └─────────────┘        └─────────────┘
                                                                         │
                                                          ┌──────────────┼──────────────┐
                                                          │              │              │
                                                          ▼              ▼              ▼
                                                   ┌───────────┐  ┌───────────┐  ┌───────────┐
                                                   │  Server   │  │  Server   │  │  Server   │
                                                   │  Monitor  │  │  Monitor  │  │  Monitor  │
                                                   │ (dev-web) │  │ (dev-api) │  │  (...)    │
                                                   │ transient │  │ transient │  │ transient │
                                                   └───────────┘  └───────────┘  └───────────┘

Restart Strategy:
- permanent: always restart
- transient: restart only on abnormal exit
- temporary: never restart

Monitor Crash Handling:
- Auto-restart with fresh state
- Poll tmux to reconstruct
- If 3 crashes in 60s → mark "unknown", toast warning
```
