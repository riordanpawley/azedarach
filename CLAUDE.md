<!--
File: CLAUDE.md
Version: 2.0.0
Updated: 2025-12-15
Purpose: Claude Code entry point for Azedarach development
-->

<ai_context version="1.0" tool="claude">

# Azedarach Project Context

> TUI Kanban board for orchestrating parallel Claude Code sessions with Beads task tracking

## Critical Rules (Always Apply)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any'.

2. **Modern CLI Tools**: ALWAYS use `rg` (NOT grep), `fd` (NOT find), `sd` (NOT sed), `bat` (NOT cat). 10x faster, gitignore-aware.

3. **Beads Tracker**: ALWAYS use `bd` CLI commands for beads operations. Use `bd search` for discovery, `bd ready` for unblocked work. NEVER use `bd list` (causes context bloat). **In worktrees: run `bd sync` manually.** See beads-tracking.skill.md for details.

4. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

5. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

6. **Beads Tracking**: ALWAYS track ALL work in beads. Update notes during work. Close with summary when done.

## Quick Commands

```bash
# Development
bun run dev                       # Start development

# Type Checking
bun run type-check                # Full project check

# Search (modern tools)
rg "pattern" --type ts            # Search content (NOT grep)
fd "filename" -t f                # Find files (NOT find)

# Beads (Task Management)
bd search "keywords"              # Search issues (PRIMARY - not list!)
bd ready                          # Find unblocked work
bd create --title="..." --type=task  # Create issue
bd update <id> --status=in_progress  # Update status/notes
bd close <id>                     # Mark complete
bd sync                           # REQUIRED in worktrees (manual sync)
```

## Project Overview

**Azedarach:** TUI Kanban board for parallel Claude Code orchestration

**Stack:**
- TypeScript (strict mode)
- OpenTUI + React (TUI framework)
- Effect (services and state)
- tmux (session persistence)
- Beads (task tracking backend)

**Core Features:**
- Kanban board displaying beads issues
- Spawn Claude sessions in isolated git worktrees
- Monitor session state (busy/waiting/done/error)
- Auto-create GitHub PRs on completion
- Attach to sessions for manual intervention

## Architecture

```
src/
├── index.tsx              # Entry point
├── cli.ts                 # CLI argument parsing
│
├── ui/                    # OpenTUI components
│   ├── App.tsx            # Root component
│   ├── Board.tsx          # Kanban board
│   ├── Column.tsx         # Status column
│   ├── TaskCard.tsx       # Task card
│   └── StatusBar.tsx      # Bottom status bar
│
├── core/                  # Business logic
│   ├── SessionManager.ts  # Claude session orchestration
│   ├── WorktreeManager.ts # Git worktree lifecycle
│   ├── StateDetector.ts   # Output pattern matching
│   ├── BeadsClient.ts     # bd CLI wrapper
│   └── PRWorkflow.ts      # GitHub PR automation
│
├── hooks/                 # State transition hooks
│   ├── onWaiting.ts       # Notify when Claude waits
│   ├── onDone.ts          # PR creation
│   └── onError.ts         # Error handling
│
└── config/                # Configuration
    ├── schema.ts          # Config validation (zod)
    └── defaults.ts        # Default values
```

## Beads Task Management

**Track ALL work** - preserves context across sessions, enables resumability.

**Quick workflow (CLI):**
1. User requests work → Search: `bd search "keywords"` or check `bd ready`
2. Start work → Update: `bd update <id> --status=in_progress`
3. During work → Add notes: `bd update <id> --notes="..."`
4. Complete → Close: `bd close <id> --reason="..."`

**Essential CLI commands:**
- `bd search "pattern"` - Search issues (PRIMARY discovery tool)
- `bd ready` - Find unblocked work
- `bd create --title="..." --type=task` - Create new issue
- `bd update <id> --status=in_progress` - Update status, notes
- `bd close <id>` - Mark work complete
- `bd show <id>` - Get issue details
- `bd list` - NEVER USE (causes context bloat)
- `bd dep add <issue> <depends-on>` - Add dependencies

**Worktree sync:**
- **Main worktree**: Auto-sync works normally
- **Other worktrees**: Run `bd sync` manually at session end

**Full reference:** `.claude/skills/workflow/beads-tracking.skill.md`

## State Management Architecture

This project uses a **three-layer reactive architecture** with strict separation of concerns:

```
┌─────────────────────────────────────────────────────────────────┐
│                    React Components (PURE RENDER)                │
│   Only: useAtomValue() + JSX. NO business logic.                │
└─────────────────────────────────────────────────────────────────┘
                                    ▲
                                    │ effect-atom bridge
                                    ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Atoms (DERIVED STATE)                        │
│   Transform/format data. All computation before React.          │
└─────────────────────────────────────────────────────────────────┘
                                    ▲
                                    │ Effect services
                                    ▼
┌─────────────────────────────────────────────────────────────────┐
│                  Effect Services (STATE + LOGIC)                 │
│   SubscriptionRef state, methods, pure utility functions        │
└─────────────────────────────────────────────────────────────────┘
```

### Critical Rule: React = Pure Render Only

**React components should ONLY contain:**
- `useAtomValue()` calls to get ready-to-render data
- JSX rendering
- Style/layout decisions

**React components should NEVER contain:**
- Data transformation or formatting
- Business logic or calculations
- Direct calls to utility functions with data
- Conditional logic beyond simple render branching

### Layer 1: Effect Services (State + Logic + Utilities)

Services hold state in `SubscriptionRef`, expose methods, AND provide pure utility functions:

```typescript
// src/services/ClockService.ts

// Pure utility functions - exported for atoms to use
export const computeElapsedFormatted = (startedAt: string, now: DateTime.Utc): string => {
  const start = DateTime.unsafeMake(startedAt)
  return formatElapsedMs(DateTime.distance(start, now))
}

export class ClockService extends Effect.Service<ClockService>()("ClockService", {
  scoped: Effect.gen(function* () {
    const now = yield* SubscriptionRef.make<DateTime.Utc>(yield* DateTime.now)

    // Schedule updates - NOTE: Schedule.spaced waits before first execution!
    yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
      Effect.flatMap(DateTime.now, (dt) => SubscriptionRef.set(now, dt)),
    )

    return { now }
  }),
}) {}
```

**Key patterns:**
- State in `SubscriptionRef` → enables reactive subscriptions
- Methods return `Effect.Effect<T>` → composable, testable
- Pure utility functions exported for atoms to use
- Use `DateTime` from Effect, not native `Date`
- `Schedule.spaced` waits before first run - add initial call if needed

### Layer 2: Atoms (Computation Layer)

Atoms compute/transform data so React receives ready-to-render values:

```typescript
// src/ui/atoms.ts

// Subscribe to service state
export const clockTickAtom = appRuntime.subscriptionRef(
  Effect.gen(function* () {
    const clock = yield* ClockService
    return clock.now
  }),
)

// Parameterized atom - returns formatted string ready for render
// ALL computation happens here, not in React
export const elapsedFormattedAtom = (startedAt: string) =>
  Atom.readable((get) => {
    const nowResult = get(clockTickAtom)
    if (!Result.isSuccess(nowResult)) return "00:00"
    return computeElapsedFormatted(startedAt, nowResult.value)  // Service utility
  })
```

**Atom types:**
| Pattern | Use Case | Example |
|---------|----------|---------|
| `appRuntime.subscriptionRef()` | Subscribe to service state | `modeAtom`, `clockTickAtom` |
| `appRuntime.fn()` | Actions/mutations | `startSessionAtom`, `moveTaskAtom` |
| `Atom.readable()` | Derived/computed state | `selectedIdsAtom`, `searchQueryAtom` |
| `Atom.readable((get) => ...)` with param | Per-instance computation | `elapsedFormattedAtom(startedAt)` |
| `Atom.make()` | Simple local state | `viewModeAtom` |

### Layer 3: React Components (Pure Render)

Components are pure render - single `useAtomValue`, no function calls:

```tsx
// CORRECT: Pure render component
export const ElapsedTimer = ({ startedAt, color }: Props) => {
  const elapsed = useAtomValue(elapsedFormattedAtom(startedAt))
  return <text fg={color}>{elapsed}</text>
}

// WRONG: Logic in React
export const ElapsedTimer = ({ startedAt, color }: Props) => {
  const now = useAtomValue(clockTickAtom)
  const start = DateTime.unsafeMake(startedAt)  // ❌ Logic in React!
  const elapsed = formatElapsed(DateTime.distance(start, now))  // ❌ Computation in React!
  return <text fg={color}>{elapsed}</text>
}
```

### Effect Scheduling Gotchas

**`Schedule.spaced` waits before first execution:**
```typescript
// BAD: Board is empty for 2 seconds on startup
yield* Effect.scheduleForked(Schedule.spaced("2 seconds"))(refresh())

// GOOD: Initial load + polling
yield* refresh()  // Immediate first load
yield* Effect.scheduleForked(Schedule.spaced("2 seconds"))(refresh())
```

**Lazy evaluation in scheduled effects:**
```typescript
// BAD: Date.now() captured once at service creation
yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
  SubscriptionRef.set(now, Date.now())  // ❌ Evaluated once!
)

// GOOD: Fresh value each tick using Effect.flatMap
yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
  Effect.flatMap(DateTime.now, (dt) => SubscriptionRef.set(now, dt))
)
```

### Mutation Flow

```
User presses Space
       │
       ▼
useKeyboard callback → enterAction()
       │
       ▼
enterActionAtom (appRuntime.fn)
       │
       ▼
EditorService.enterAction() → SubscriptionRef.set(mode, { _tag: "action" })
       │
       ▼
modeAtom (subscriptionRef) detects change
       │
       ▼
React re-renders with new mode
```

## TUI & Keyboard Architecture

### Modal Editing (Helix-style)

The UI uses modal editing like Helix/Vim:

```
┌──────────────────────────────────────────────────────────────┐
│                        Mode States                            │
├──────────────────────────────────────────────────────────────┤
│  normal  ─────v────→  select    (multi-select tasks)         │
│     │                    │                                    │
│     g ──────────────→  goto     (jump navigation)            │
│     │                    │                                    │
│  space ─────────────→  action   (task actions menu)          │
│     │                    │                                    │
│     / ──────────────→  search   (filter tasks)               │
│     │                    │                                    │
│     : ──────────────→  command  (VC REPL commands)           │
│     │                    │                                    │
│     , ──────────────→  sort     (sort menu)                  │
│                                                               │
│  All modes: Escape returns to normal                         │
└──────────────────────────────────────────────────────────────┘
```

### Keyboard Handling

App.tsx handles keyboard events with mode-specific handlers:

```typescript
// App.tsx
useKeyboard((event) => {
  // Route to mode-specific handler
  if (isAction) return handleActionMode(event)
  if (isSearch) return handleSearchMode(event)
  if (isNormal) return handleNormalMode(event)
  // ...
})

const handleNormalMode = useCallback((event: KeyEvent) => {
  switch (event.name) {
    case "j": moveDown(); break
    case "k": moveUp(); break
    case "space": enterAction(); break
    case "g": enterGoto(); break
    // ...
  }
}, [moveDown, moveUp, enterAction, enterGoto])
```

### Navigation State

NavigationService manages cursor position:

```typescript
// Cursor position
interface Cursor {
  columnIndex: number  // Which kanban column (0-3)
  taskIndex: number    // Which task in column
}

// Movement updates cursor
const move = (direction: Direction) =>
  SubscriptionRef.update(cursor, (c) => {
    switch (direction) {
      case "up": return { ...c, taskIndex: c.taskIndex - 1 }
      case "down": return { ...c, taskIndex: c.taskIndex + 1 }
      case "left": return { columnIndex: c.columnIndex - 1, taskIndex: 0 }
      case "right": return { columnIndex: c.columnIndex + 1, taskIndex: 0 }
    }
  })
```

## Adding New Features

### Adding a New Service

1. Create service with `Effect.Service`:
```typescript
// src/services/MyService.ts
export class MyService extends Effect.Service<MyService>()("MyService", {
  dependencies: [OtherService.Default],  // Optional
  effect: Effect.gen(function* () {
    const state = yield* SubscriptionRef.make(initialState)
    return {
      state,
      doThing: () => SubscriptionRef.update(state, ...),
    }
  }),
}) {}
```

2. Add to layer in `atoms.ts`:
```typescript
const appLayer = Layer.mergeAll(
  // ...existing services
  MyService.Default,
)
```

3. Create atoms:
```typescript
export const myStateAtom = appRuntime.subscriptionRef(
  Effect.gen(function* () {
    const svc = yield* MyService
    return svc.state
  }),
)
```

### Adding a New Keybinding

1. Add handler in appropriate mode function in `App.tsx`
2. Update `docs/keybindings.md`
3. If new mode needed, add to EditorService

### Adding a New Overlay

1. Add variant to OverlayService stack type
2. Create overlay component in `src/ui/`
3. Add rendering logic in App.tsx

## Key Design Decisions

### Session State Detection

Detect Claude session state via output pattern matching:

```typescript
const PATTERNS = {
  waiting: [/\[y\/n\]/i, /Do you want to/i],
  done: [/Task completed/i, /Successfully/i],
  error: [/Error:|Exception:|Failed:/i],
};
```

### Worktree Naming

Worktrees created as siblings to the project:
```
../ProjectName-<bead-id>/
```

### Epic/Task Handling

- Epic → dedicated worktree
- Task with epic parent → use epic's worktree
- Standalone task → dedicated worktree

### PR Workflow

Default: Auto-create draft PR, notify user
Configurable: Ready PR, auto-merge after CI, immediate merge

## Skills

Skills auto-load when you edit files or mention keywords:

**Workflow Skills:**
- `.claude/skills/workflow/beads-tracking.skill.md` - Issue tracking workflow

## Development Tips

- **Type errors:** Always run `bun run type-check` for validation
- **Files:** Check references before deleting (`rg "filename"`)
- **Testing:** Run `bun run dev` for interactive testing, `bun run type-check` for types

## Documentation

**IMPORTANT:** Keep the user guide updated when implementing features.

**Documentation location:** `docs/`

| File | Purpose |
|------|---------|
| `docs/README.md` | Main user guide index - UPDATE when adding features |
| `docs/keybindings.md` | Keybinding reference |
| `docs/services.md` | Effect services architecture |
| `docs/testing.md` | Testing guide |
| `docs/tmux-guide.md` | tmux primer for new users |

**When to update docs:**
- Adding new keybindings → Update `keybindings.md` AND `README.md`
- Adding new services → Update `services.md`
- Changing test procedures → Update `testing.md`
- Any user-facing feature → Update `README.md`

## Quick Help

- Workflow help: Use beads-tracking skill
- Architecture: See README.md for full spec
- User guide: See `docs/README.md`

</ai_context>
