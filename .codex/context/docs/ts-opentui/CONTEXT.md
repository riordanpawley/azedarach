<!--
File: CONTEXT.md
Version: 2.3.0
Updated: 2026-03-07
Purpose: Canonical ts-opentui AI context source synced to nested entrypoints
-->

<ai_context version="1.0" tool="shared">

# Azedarach Project Context

> TUI Kanban board for orchestrating parallel Claude Code sessions with issue tracking

## Critical Rules (Always Apply)

1. **Type Safety**: ALWAYS use TypeScript strict mode. NEVER use 'as' casting or 'any'.

2. **Modern CLI Tools**: ALWAYS use `rg` (NOT grep), `fd` (NOT find), `sd` (NOT sed), `bat` (NOT cat). 10x faster, gitignore-aware.

3. **Issue Tracking**: ALWAYS start with `az prime`, then use `az issue` for issue operations.

4. **Branch Workflow**: Use local-only git flow by default. Do not run remote sync/cleanup commands (for example pull/rebase, push, remote prune) unless explicitly requested. When already in the target worktree, use plain `git` commands; use `git -C <path>` only when intentionally targeting a different path.

5. **File Deletion**: NEVER delete untracked files without permission. Check references first (`rg "filename"`).

6. **Git Restore**: NEVER use `git restore` without EXPLICIT user permission.

7. **🚨 CRITICAL: Commit Before Done 🚨**: Before saying "done", "complete", "finished", or stopping work, you MUST commit all changes. Uncommitted work is LOST work.

   **MANDATORY CHECKLIST** (run these commands):
   ```bash
   git status                    # Check for uncommitted changes
   git add -A                    # Stage all changes
   git commit -m "descriptive message"   # Commit with clear message
   ```

   **If work is complete:** Use a proper descriptive commit message
   **If work is partial/WIP:** Use `git commit -m "wip: brief description of state"`

   **This applies when you:**
   - Say "done", "complete", "finished", "all set", etc.
   - Are about to stop responding
   - Have completed a task or subtask
   - Are switching to a different task

   ⚠️ DO NOT say "done" until `git status` shows "nothing to commit"

8. **Effect Service Patterns**:
   - NEVER create global scope Effect-returning functions with service requirements (antipattern)
   - NEVER use `Effect.provide` or `Effect.provideService` inside service methods - that's wrong
   - Services grab dependencies at layer construction (`yield* SomeService`), then use them directly
   - If you need `Path.Path` operations, grab `pathService` at layer construction, then call `pathService.resolve()`, `pathService.join()`, etc. directly - don't create wrappers
   - Don't wrap one-liners in helper functions - just use the method directly
   - **Pattern Reference**: Use the `effect-docs` MCP server tools to look up idiomatic Effect patterns:
     - `mcp__effect-docs__search_patterns` - Search patterns by keyword (e.g., "retry", "concurrency pool")
     - `mcp__effect-docs__get_pattern` - Get full pattern details with code examples
     - `mcp__effect-docs__generate_snippet` - Generate customized Effect code snippets

9. **No Node.js Imports**: NEVER import from `node:*`. Use `@effect/platform` instead:
   - `node:path` → Use `Path.Path` service methods (`pathService.resolve()`, `.join()`, etc.)
   - `node:crypto` → Use `crypto.randomUUID()` (Web Crypto API, works in Bun/Node)
   - `node:child_process` → Use `@effect/platform` `Command`
   - `node:os` → Use `process.env.HOME` for homedir

10. **effect-atom is a React Bridge, NOT State Management**:
   - State belongs in Effect Services (via `SubscriptionRef`)
   - Atoms bridge that state to React via `appRuntime.subscriptionRef()`
   - Use atoms for: parameterized derivations, cross-service composition
   - Use services for: core state, business logic, background tasks
   - See `internal-docs/effect-atom-architecture.md` for full decision matrix

11. **OpenTUI Text Nesting**: NEVER nest `<text>` inside `<text>`. Use `<span>` for inline styled text:
    - `<text>` → `TextRenderable` (container, accepts: string | TextNodeRenderable | StyledText)
    - `<span>` → `SpanRenderable` → `TextNodeRenderable` (can be nested inside `<text>`)
    - **Wrong:** `<text fg="gray"><text fg="blue">→</text> Back</text>` - inner `<text>` is a `TextRenderable`, not accepted
    - **Right:** `<text fg="gray"><span fg="blue">→</span> Back</text>` - `<span>` is a `TextNodeRenderable`, works!
    - **Also Right:** Use sibling `<text>` in `<box flexDirection="row">` for different colors
    - Error message: "TextNodeRenderable only accepts strings, TextNodeRenderable instances, or StyledText instances"

12. **Schema Encode/Decode**: ALWAYS use `Schema.encode()` and `Schema.decode()` for serialization:
    - NEVER manually convert types (e.g., `{ ...state, port: state.port ?? null }`)
    - NEVER use `JSON.stringify()` or `JSON.parse()` - use `Schema.parseJson()` wrapper instead
    - Define the schema to handle transformations automatically
    - Use `Schema.UndefinedOr(Schema.Number)` for optional fields
    - `Schema.decode(schema)` - use when input type matches Encoded (e.g., `string` from `readFileString`)
    - `Schema.decodeUnknown(schema)` - use when input is truly `unknown` (e.g., external API response)
    - Let Schema handle the type conversion between runtime and serialized forms
    ```typescript
    // ❌ BAD: Manual JSON and conversion
    const parsed = JSON.parse(content)
    const toEncodable = (state) => ({ ...state, port: state.port ?? null })
    const json = JSON.stringify(toEncodable(state))

    // ✅ GOOD: Use Schema.parseJson wrapper
    const MySchema = Schema.parseJson(Schema.Struct({ ... }))
    const decoded = yield* Schema.decode(MySchema)(jsonString)  // Input is string
    const json = yield* Schema.encode(MySchema)(data)  // Returns string
    ```

13. **tmux Session Creation**: ALWAYS use interactive shell with direnv loading for tmux sessions:
    - Use `${shell} -i -c '${command}; exec ${shell}'` pattern
    - **`-i` flag**: Loads `.zshrc`/`.bashrc`, which triggers direnv hooks
    - **`exec ${shell}`**: Keeps session alive after command exits for debugging
    - Without `-i`, direnv won't load and environment variables (DATABASE_URL, VITE_PORT, etc.) will be missing
    ```typescript
    // ❌ BAD: Raw command without shell wrapper
    yield* tmux.newSession(sessionName, {
      cwd,
      command: "PORT=3000 pnpm run dev",  // direnv won't load!
    })

    // ✅ GOOD: Interactive shell with exec fallback
    const shell = config.session.shell  // e.g., "zsh"
    yield* tmux.newSession(sessionName, {
      cwd,
      command: `${shell} -i -c 'PORT=3000 pnpm run dev; exec ${shell}'`,
    })
    ```

14. **Spec Sync Required for ts-opentui Behavior Changes**:
    - If a task changes `ts-opentui` product behavior (user-visible flow, workflow semantics, validation, state/status transitions, failure/retry handling, or release-gate expectations), update `az spec` requirements/links in the same task.
    - If no spec update is needed, add an explicit issue note with `Spec impact: none` and a concrete rationale tied to changed files.
    - Use the workflow spec-maintenance skill to drive analysis and validation: `.rulesync/skills/workflow-spec-maintenance/SKILL.md`.
    - Do not close work as complete until spec updates (or a justified no-impact note) are recorded.

## Quick Commands

```bash
# Development
bun run dev                       # Start development

# Type Checking
bun run type-check                # Full project check

# Search (modern tools)
rg "pattern" --type ts            # Search content (NOT grep)
fd "filename" -t f                # Find files (NOT find)

# Issue Tracking
az prime                                                       # Session primer + workflow guidance
az issue --help                                                # Command reference
az issue get <issue-id>                                        # Show issue details
az issue create "Issue title" --description "Detailed context" # Create issue
az issue update <issue-id> --notes "progress update"           # Update progress
az issue close <issue-id>                                      # Mark complete
```

## Project Overview

**Azedarach:** TUI Kanban board for parallel Claude Code orchestration

**Stack:**
- TypeScript (strict mode)
- OpenTUI + React (TUI framework)
- Effect (services and state)
- tmux (session persistence)
- az issue tracker

**Core Features:**
- Kanban board displaying Linear issues
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
│   ├── BeadsClient.ts     # issue tracker CLI wrapper
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

## Issue Tracking

**Track ALL work** - preserves context across sessions, enables resumability.

Run `az prime` first in each session, then use `az issue` as the single tracking interface.

**Core workflow:**
1. Track any task that takes more than one command.
2. Keep one active parent issue per session whenever possible.
3. Subagents create, maintain, and close child issues under the active parent issue.
4. Keep issue notes/status current while implementing.
5. Include issue ID in commit messages; commit before closing issues.

**Core commands:**
- `az issue --help`
- `az issue get <issue-id>`
- `az issue create "Issue title" --description "Detailed context"`
- `az issue update <issue-id> --notes "progress update"`
- `az issue close <issue-id>`

**Worktree sync:** Keep sync local by default; only run remote git sync commands when explicitly requested.

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

### Services vs Atoms Decision Matrix

| What | Where | Why |
|------|-------|-----|
| Core domain state | Effect Services | Testable, lifecycle-managed |
| Polling/background tasks | Effect Services | Scoped fibers, cleanup |
| Parameterized derivations | Atoms (`Atom.readable`) | Per-instance computation |
| Cross-service composition | Atoms | Combine without coupling |
| Bridge to React | `appRuntime.subscriptionRef()` | SubscriptionRef → useAtomValue |
| Actions/mutations | `appRuntime.fn()` | Service access + error handling |

**Full documentation:** `internal-docs/effect-atom-architecture.md`

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

**Effect.fork vs Effect.forkScoped - CRITICAL:**

`Effect.fork` creates a fiber that is interrupted when the parent effect completes. This is almost always the WRONG choice for background tasks that need to outlive the spawning effect.

```typescript
// ❌ BAD: Fiber is immediately interrupted after start() returns
const start = (handler: Handler) =>
  Effect.gen(function* () {
    const fiber = yield* pollingEffect.pipe(
      Effect.repeat(Schedule.spaced("500 millis")),
      Effect.fork,  // ❌ Fiber dies when start() returns!
    )
    return fiber
  })

// ✅ GOOD: Use scoped service + forkScoped for long-running tasks
export class MyService extends Effect.Service<MyService>()("MyService", {
  scoped: Effect.gen(function* () {  // Note: scoped, not effect
    // Fiber lives for service lifetime (tied to scope)
    yield* pollingEffect.pipe(
      Effect.repeat(Schedule.spaced("500 millis")),
      Effect.forkScoped,  // ✅ Lives for scope duration
    )
    return { /* service methods */ }
  }),
}) {}

// ✅ ALSO GOOD: Use Effect.scheduleForked which is scoped by default
yield* Effect.scheduleForked(Schedule.spaced("500 millis"))(pollingEffect)
```

**When to use each:**
- `Effect.fork` - Fire-and-forget within a long-running parent (rare)
- `Effect.forkScoped` - Background task that should live for scope duration
- `Effect.scheduleForked` - Scheduled polling with proper scoping (preferred)
- `Effect.forkDaemon` - Background task that survives parent interruption

**Service definition: `scoped:` vs `effect:`**

Use `scoped:` when the service spawns fibers that need to outlive the constructor:

```typescript
// ✅ GOOD: Service spawns long-running fibers
export class PollingService extends Effect.Service<PollingService>()("PollingService", {
  scoped: Effect.gen(function* () {  // scoped: provides scope for forkScoped
    yield* pollEffect.pipe(Effect.forkScoped)  // Lives for service lifetime
    return { /* methods */ }
  }),
}) {}

// ✅ GOOD: Service only has state and methods, no background fibers
export class StateService extends Effect.Service<StateService>()("StateService", {
  effect: Effect.gen(function* () {  // effect: no scope needed
    const state = yield* SubscriptionRef.make(initial)
    return { state, update: (x) => SubscriptionRef.set(state, x) }
  }),
}) {}

// ❌ BAD: Uses effect: but spawns fibers with forkScoped - no scope available!
export class BrokenService extends Effect.Service<BrokenService>()("BrokenService", {
  effect: Effect.gen(function* () {
    yield* cleanup.pipe(Effect.forkScoped)  // ❌ No scope! Will fail or be immediately interrupted
    return { /* methods */ }
  }),
}) {}
```

**Decision checklist:**
- Service spawns `Effect.forkScoped` or `Effect.scheduleForked` → use `scoped:`
- Service methods spawn fibers that must outlive the method call → use `scoped:`
- Service only has state (SubscriptionRef/Ref) and synchronous methods → use `effect:`

**Service dependencies - use `dependencies:` to avoid leaking requirements:**

When a service depends on another service, declare it in `dependencies:` so `.Default` provides its own deps:

```typescript
// ✅ GOOD: TmuxSessionMonitor declares its dependency on DiagnosticsService
export class TmuxSessionMonitor extends Effect.Service<TmuxSessionMonitor>()("TmuxSessionMonitor", {
  dependencies: [DiagnosticsService.Default],  // ← Provides own dependency
  scoped: Effect.gen(function* () {
    const diagnostics = yield* DiagnosticsService  // Available because of dependencies
    // ...
  }),
}) {}

// Now TmuxSessionMonitor.Default has NO unsatisfied requirements
// Can be used in Layer.mergeAll without leaking DiagnosticsService requirement

// ❌ BAD: Missing dependencies declaration - leaks requirement to app layer
export class BrokenReceiver extends Effect.Service<BrokenReceiver>()("BrokenReceiver", {
  scoped: Effect.gen(function* () {
    const diagnostics = yield* DiagnosticsService  // ❌ Requirement leaks!
    // ...
  }),
}) {}

// BrokenReceiver.Default now REQUIRES DiagnosticsService from the layer composition
// This forces awkward Layer.provideMerge ordering in the app layer
```

**Rule**: If your service uses `yield* SomeOtherService`, add `SomeOtherService.Default` to `dependencies:[]`.

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

Skills available in this repository:

**Workflow Skills:**
- Issue tracking workflow skill (use local workflow skill that documents current `az issue` process)
- Spec maintenance workflow skill (use local workflow skill that keeps `az spec` requirements/links aligned with behavior changes)

**Effect Skills:**
- `.rulesync/skills/effect-services/SKILL.md` - Services, layers, dependency injection
- `.rulesync/skills/effect-errors/SKILL.md` - Tagged errors, retry, timeout patterns
- `.rulesync/skills/effect-concurrency/SKILL.md` - Fibers, forking, scheduling, Ref/SubscriptionRef
- `.rulesync/skills/effect-resources/SKILL.md` - Scopes, acquireRelease, resource lifecycle

## Development Tips

- **Type errors:** Always run `bun run type-check` for validation
- **Files:** Check references before deleting (`rg "filename"`)
- **Testing:** Run `bun run build` to verify changes compile and bundle correctly (not `bun run dev` which requires interactive testing)

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

- Workflow help: Use the issue-tracking workflow skill
- Architecture: See README.md for full spec
- User guide: See `docs/README.md`

</ai_context>
