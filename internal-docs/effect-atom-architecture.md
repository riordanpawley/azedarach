# effect-atom Architecture: When to Use Services vs Atoms

> **Created**: 2025-12-18
> **Context**: Bead az-eh8i - understanding effect-atom's purpose
> **Status**: Architectural decision documented

## TL;DR

**effect-atom is NOT a state management library** - it's a **React integration layer**.

- **Effect Services** own state and business logic (via `SubscriptionRef`)
- **effect-atom Atoms** bridge that state to React and enable parameterized derivations
- Your instinct to "put state in services" is correct

## Decision Matrix

| What | Where | Why |
|------|-------|-----|
| Core domain state | Effect Services | Testable, lifecycle-managed, business logic home |
| Polling/background tasks | Effect Services | Scoped fibers, automatic cleanup |
| **Parameterized derivations** | Atoms (`Atom.readable`) | Per-instance computation (services can't do this) |
| **Cross-service composition** | Atoms | Combine without coupling services |
| Bridge to React | `appRuntime.subscriptionRef()` | Converts SubscriptionRef → useAtomValue |
| Actions/mutations | `appRuntime.fn()` | Provides services + error handling |

## The Three Things effect-atom Actually Does

### 1. Bridge SubscriptionRef → React Reactivity

Without effect-atom, React wouldn't re-render when Effect's `SubscriptionRef` changes:

```typescript
// Service owns state
export class ClockService extends Effect.Service<ClockService>()(...) {
  scoped: Effect.gen(function* () {
    const now = yield* SubscriptionRef.make<DateTime.Utc>(initial)
    yield* Effect.scheduleForked(Schedule.spaced("1 second"))(...)
    return { now }  // <- SubscriptionRef
  })
}

// Atom bridges it to React
export const clockTickAtom = appRuntime.subscriptionRef(
  Effect.gen(function* () {
    const clock = yield* ClockService
    return clock.now  // Returns the SubscriptionRef, atom subscribes to it
  })
)
```

### 2. Parameterized Derivations (The Killer Feature)

**This is something services CANNOT do:**

```typescript
// Parameterized by startedAt - different value per component instance!
export const elapsedFormattedAtom = (startedAt: string) =>
  Atom.readable((get) => {
    const nowResult = get(clockTickAtom)
    if (!Result.isSuccess(nowResult)) return "00:00"
    return computeElapsedFormatted(startedAt, nowResult.value)
  })

// In React - each instance gets its own derived value:
<ElapsedTimer startedAt="2025-12-18T10:00:00Z" />  // "00:45"
<ElapsedTimer startedAt="2025-12-18T11:30:00Z" />  // "01:15"
```

A service method like `getElapsed(startedAt)` would compute the value once, but:
- It wouldn't be reactive (wouldn't re-compute when `now` changes)
- You can't create a "per-instance" service

The atom establishes a reactive dependency graph per consumer.

### 3. Cross-Service Composition Without Coupling

```typescript
export const focusedTaskRunningOperationAtom = Atom.readable((get) => {
  const focusedIdResult = get(focusedTaskIdAtom)     // From NavigationService
  const stateResult = get(commandQueueStateAtom)     // From CommandQueueService
  // Compose data from two services without those services knowing about each other
})
```

If this lived in a service, you'd need to inject both NavigationService and CommandQueueService, coupling them. The atom is a presentation-layer composition point.

## What effect-atom Does NOT Do

- **Doesn't manage fiber lifecycles** - that's Effect + scoped services
- **Doesn't replace SubscriptionRef for state storage** - services own state
- **Doesn't provide business logic home** - that's services
- **Doesn't handle background tasks** - that's `Effect.scheduleForked` in services

## When to Keep Derivation in Services

For derivations that:
1. Are **NOT** parameterized (same for all consumers)
2. Don't compose across services
3. Are core business logic (not presentation)

**Example:** `BoardService.filteredTasksByColumn` lives in the service because:
- It's not parameterized (all consumers see the same filtered list)
- It's central to the board model
- Multiple atoms use it

```typescript
// In BoardService - not parameterized, core logic
const computeFilteredTasksByColumn = (
  allTasks: ReadonlyArray<TaskWithSession>,
  searchQuery: string,
  sortConfig: SortConfig,
): TaskWithSession[][] => {
  return COLUMNS.map((col) => {
    const columnTasks = allTasks.filter((task) => task.status === col.status)
    const filtered = filterTasks(columnTasks, searchQuery)
    return sortTasks(filtered, sortConfig)
  })
}
```

## Rule of Thumb

> **Should this derived value live in a service?**
> Answer **YES** unless:
> - It needs to be parameterized per-React-component
> - It composes data from multiple unrelated services for UI purposes

## Atom Type Reference

| Pattern | Use Case | Example |
|---------|----------|---------|
| `appRuntime.subscriptionRef()` | Subscribe to service state | `modeAtom`, `clockTickAtom` |
| `appRuntime.fn()` | Actions/mutations | `startSessionAtom`, `moveTaskAtom` |
| `appRuntime.atom()` | One-time Effect execution | `ghCLIAvailableAtom` |
| `Atom.readable()` | Derived/computed state | `selectedIdsAtom`, `searchQueryAtom` |
| `Atom.readable((get) => ...)` with param | Per-instance computation | `elapsedFormattedAtom(startedAt)` |
| `Atom.make()` | Simple local state (rare) | UI-only state |

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                    React Components (PURE RENDER)                │
│   Only: useAtomValue() + JSX. NO business logic.                │
└─────────────────────────────────────────────────────────────────┘
                                    ▲
                                    │ useAtomValue()
                                    │
┌─────────────────────────────────────────────────────────────────┐
│                     effect-atom Atoms                            │
│                                                                  │
│   appRuntime.subscriptionRef() ── Bridge service state to React │
│   appRuntime.fn()              ── Execute service methods       │
│   Atom.readable()              ── Parameterized derivations     │
│                                ── Cross-service composition     │
└─────────────────────────────────────────────────────────────────┘
                                    ▲
                                    │ yield* Service, return ref
                                    │
┌─────────────────────────────────────────────────────────────────┐
│                  Effect Services (STATE + LOGIC)                 │
│                                                                  │
│   SubscriptionRef.make()       ── Reactive state storage        │
│   Effect.scheduleForked()      ── Background tasks              │
│   Pure utility functions       ── Business logic                │
│   Service methods              ── Mutations                     │
└─────────────────────────────────────────────────────────────────┘
```

## Related Files

- `src/ui/atoms.ts` - All atom definitions
- `src/atoms/runtime.ts` - Layer composition for services
- `src/services/*.ts` - Effect services with SubscriptionRef state
- `CLAUDE.md` - State Management Architecture section
