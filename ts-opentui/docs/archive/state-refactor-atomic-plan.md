# Refactoring App State to Atomic Effect Services

> **Status: COMPLETE** - Implemented in PR #10 (az-vpz branch)

## Overview

This is an alternative to the [Redux-like plan](./state-refactor-plan.md). Instead of a unified state tree with message passing, we use **atomic Effect services** where each domain owns its state via fine-grained `Ref`s.

### Philosophy Comparison

| Aspect | Redux-like Plan | Atomic Plan (this) |
|--------|-----------------|-------------------|
| **State location** | Single unified tree | Distributed across services |
| **Updates** | Messages → Reducer → New state | Direct service method calls |
| **Cross-cutting** | Orchestrator or message bus | Direct service injection |
| **React binding** | Subscribe to unified state | Subscribe to individual atoms |
| **Effect idiom** | Adapts Redux patterns to Effect | Native Effect service pattern |

---

## Problem Statement

Same problems as the Redux plan:
1. Fragmented useState/useRef synchronization
2. 600+ line keyboard handler
3. Inconsistent polling (setTimeout vs Effect)
4. Scattered async error handling

But we solve them differently.

---

## Architecture

### Core Principle: Services Own Their State

Each service:
1. Declares fine-grained `Ref`s for its state
2. Exposes methods that update those refs
3. Can depend on other services via Effect's DI
4. Publishes atoms for React subscription

```
┌─────────────────────────────────────────────────────────┐
│                    React Components                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │useAtom   │  │useAtom   │  │useAtom   │              │
│  │(toasts)  │  │(overlays)│  │(cursor)  │              │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘              │
└───────┼─────────────┼─────────────┼─────────────────────┘
        │             │             │
        ▼             ▼             ▼
┌─────────────────────────────────────────────────────────┐
│                   Effect Atoms Layer                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │toastsAtom│  │overlayAt.│  │cursorAtom│              │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘              │
└───────┼─────────────┼─────────────┼─────────────────────┘
        │             │             │
        ▼             ▼             ▼
┌─────────────────────────────────────────────────────────┐
│                   Effect Services                        │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐        │
│  │ToastService│  │OverlaySvc  │  │NavigationSvc│        │
│  │ - toasts   │  │ - stack    │  │ - column    │        │
│  │ - maxShow  │  │            │  │ - task      │        │
│  │ - duration │  │            │  │ - focused   │        │
│  └────────────┘  └────────────┘  └────────────┘        │
│         │                               │               │
│         └───────────────┬───────────────┘               │
│                         ▼                               │
│                  ┌────────────┐                         │
│                  │SessionSvc  │ (can call Toast, Nav)   │
│                  └────────────┘                         │
└─────────────────────────────────────────────────────────┘
```

---

## Service Definitions

### 1. ToastService

```typescript
// src/services/ToastService.ts

import { Effect, Ref } from "effect"

export interface Toast {
  readonly id: string
  readonly type: "success" | "error" | "info"
  readonly message: string
  readonly createdAt: number
}

export class ToastService extends Effect.Service<ToastService>()("ToastService", {
  scoped: Effect.gen(function* () {
    // Initialize fine-grained refs
    const toasts = yield* Ref.make<ReadonlyArray<Toast>>([])
    const duration = yield* Ref.make(5000)
    const maxVisible = yield* Ref.make(3)

    // Auto-expiration fiber
    yield* Effect.forkScoped(
      Effect.forever(
        Effect.gen(function* () {
          yield* Effect.sleep("100 millis")
          const now = Date.now()
          const durationMs = yield* Ref.get(duration)
          yield* Ref.update(toasts, (ts) =>
            ts.filter((t) => now - t.createdAt < durationMs)
          )
        })
      )
    )

    return {
      // State refs (fine-grained)
      toasts,
      duration,
      maxVisible,

      // Methods
      show: (type: Toast["type"], message: string) =>
        Effect.gen(function* () {
          const toast: Toast = {
            id: crypto.randomUUID(),
            type,
            message,
            createdAt: Date.now(),
          }
          const max = yield* Ref.get(maxVisible)
          yield* Ref.update(toasts, (ts) => [...ts.slice(-(max - 1)), toast])
          return toast
        }),

      dismiss: (id: string) =>
        Ref.update(toasts, (ts) => ts.filter((t) => t.id !== id)),

      clear: () => Ref.set(toasts, []),
    }
  }),
})

// Usage:
// ToastService.Default - provides the live implementation
// ToastService.make - raw effect for testing with custom deps
```

### 2. OverlayService

```typescript
// src/services/OverlayService.ts

import { Effect, Ref } from "effect"

export type Overlay =
  | { readonly _tag: "help" }
  | { readonly _tag: "detail"; readonly taskId: string }
  | { readonly _tag: "create" }
  | { readonly _tag: "settings" }
  | { readonly _tag: "confirm"; readonly message: string; readonly onConfirm: Effect.Effect<void> }

export class OverlayService extends Effect.Service<OverlayService>()("OverlayService", {
  effect: Effect.gen(function* () {
    const stack = yield* Ref.make<ReadonlyArray<Overlay>>([])

    return {
      stack,

      push: (overlay: Overlay) => Ref.update(stack, (s) => [...s, overlay]),

      pop: () =>
        Ref.modify(stack, (s) => {
          if (s.length === 0) return [undefined, s]
          return [s[s.length - 1], s.slice(0, -1)]
        }),

      clear: () => Ref.set(stack, []),

      current: () =>
        Ref.get(stack).pipe(
          Effect.map((s) => (s.length > 0 ? s[s.length - 1] : undefined))
        ),

      isOpen: () => Ref.get(stack).pipe(Effect.map((s) => s.length > 0)),
    }
  }),
})
```

### 3. NavigationService

```typescript
// src/services/NavigationService.ts

import { Effect, Ref } from "effect"

export interface Cursor {
  readonly columnIndex: number
  readonly taskIndex: number
}

export class NavigationService extends Effect.Service<NavigationService>()("NavigationService", {
  effect: Effect.gen(function* () {
    const columnIndex = yield* Ref.make(0)
    const taskIndex = yield* Ref.make(0)
    const focusedTaskId = yield* Ref.make<string | null>(null)
    const followTaskId = yield* Ref.make<string | null>(null)

    const getCursor = () =>
      Effect.all({
        columnIndex: Ref.get(columnIndex),
        taskIndex: Ref.get(taskIndex),
      })

    return {
      // State refs (fine-grained)
      columnIndex,
      taskIndex,
      focusedTaskId,
      followTaskId,

      // Methods
      move: (direction: "up" | "down" | "left" | "right") =>
        Effect.gen(function* () {
          switch (direction) {
            case "up":
              yield* Ref.update(taskIndex, (i) => Math.max(0, i - 1))
              break
            case "down":
              yield* Ref.update(taskIndex, (i) => i + 1) // Clamp in UI
              break
            case "left":
              yield* Ref.update(columnIndex, (i) => Math.max(0, i - 1))
              yield* Ref.set(taskIndex, 0)
              break
            case "right":
              yield* Ref.update(columnIndex, (i) => i + 1) // Clamp in UI
              yield* Ref.set(taskIndex, 0)
              break
          }
        }),

      jumpTo: (column: number, task: number) =>
        Effect.all([
          Ref.set(columnIndex, column),
          Ref.set(taskIndex, task),
        ]).pipe(Effect.asVoid),

      jumpToTask: (taskId: string) =>
        // Implementation depends on BoardService for task locations
        Ref.set(focusedTaskId, taskId),

      jumpToEnd: () =>
        // Would need board context to know actual end - placeholder
        Effect.void,

      setFollow: (taskId: string | null) => Ref.set(followTaskId, taskId),

      getCursor,
    }
  }),
})
```

### 4. KeyboardService

The keyboard handler lives in Effect-land, not React. The React hook is just a thin bridge.

```typescript
// src/services/KeyboardService.ts

import { Effect, Ref } from "effect"
import { ToastService } from "./ToastService"
import { OverlayService } from "./OverlayService"
import { NavigationService } from "./NavigationService"
import { EditorService } from "./EditorService"
import { BoardService } from "./BoardService"

export interface Keybinding {
  readonly key: string
  readonly mode: "normal" | "select" | "command" | "search" | "overlay" | "*"
  readonly description: string
  readonly action: Effect.Effect<void>
}

export class KeyboardService extends Effect.Service<KeyboardService>()("KeyboardService", {
  // Declare dependencies - Effect handles the wiring
  dependencies: [
    ToastService.Default,
    OverlayService.Default,
    NavigationService.Default,
    EditorService.Default,
    BoardService.Default,
  ],

  effect: Effect.gen(function* () {
    // Inject all services we need for actions
    const toast = yield* ToastService
    const overlay = yield* OverlayService
    const nav = yield* NavigationService
    const editor = yield* EditorService
    const board = yield* BoardService

    // Helpers for actions that need current context
    const openCurrentDetail = () =>
      Effect.gen(function* () {
        const cursor = yield* nav.getCursor()
        const task = yield* board.getTaskAt(cursor.columnIndex, cursor.taskIndex)
        if (task) {
          yield* overlay.push({ _tag: "detail", taskId: task.id })
        }
      })

    const toggleCurrentSelection = () =>
      Effect.gen(function* () {
        const cursor = yield* nav.getCursor()
        const task = yield* board.getTaskAt(cursor.columnIndex, cursor.taskIndex)
        if (task) {
          yield* editor.toggleSelection(task.id)
        }
      })

    const handleEscape = () =>
      Effect.gen(function* () {
        const hasOverlay = yield* overlay.isOpen()
        if (hasOverlay) {
          yield* overlay.pop()
          return
        }
        const mode = yield* editor.getMode()
        if (mode._tag !== "normal") {
          yield* editor.exitToNormal()
        }
      })

    // Default keybindings - defined as data, not switch statements
    const defaultBindings: ReadonlyArray<Keybinding> = [
      // Navigation (normal mode)
      { key: "j", mode: "normal", description: "Move down", action: nav.move("down") },
      { key: "k", mode: "normal", description: "Move up", action: nav.move("up") },
      { key: "h", mode: "normal", description: "Move left", action: nav.move("left") },
      { key: "l", mode: "normal", description: "Move right", action: nav.move("right") },
      { key: "g", mode: "normal", description: "Go to top", action: nav.jumpTo(0, 0) },
      { key: "G", mode: "normal", description: "Go to bottom", action: nav.jumpToEnd() },

      // Overlays
      { key: "?", mode: "normal", description: "Show help", action: overlay.push({ _tag: "help" }) },
      { key: "c", mode: "normal", description: "Create task", action: overlay.push({ _tag: "create" }) },
      { key: "Enter", mode: "normal", description: "View detail", action: openCurrentDetail() },
      { key: ",", mode: "normal", description: "Settings", action: overlay.push({ _tag: "settings" }) },

      // Mode transitions
      { key: "v", mode: "normal", description: "Select mode", action: editor.enterSelect() },
      { key: ":", mode: "normal", description: "Command mode", action: editor.enterCommand() },
      { key: "/", mode: "normal", description: "Search", action: editor.enterSearch() },

      // Universal escape
      { key: "Escape", mode: "*", description: "Exit/cancel", action: handleEscape() },

      // Select mode
      { key: "Space", mode: "select", description: "Toggle selection", action: toggleCurrentSelection() },
      { key: "j", mode: "select", description: "Move down", action: nav.move("down") },
      { key: "k", mode: "select", description: "Move up", action: nav.move("up") },

      // Overlay mode
      { key: "Escape", mode: "overlay", description: "Close overlay", action: overlay.pop() },
    ]

    const keybindings = yield* Ref.make<ReadonlyArray<Keybinding>>(defaultBindings)

    // Helper: get current context for matching
    const getContext = () =>
      Effect.gen(function* () {
        const mode = yield* editor.getMode()
        const hasOverlay = yield* overlay.isOpen()
        return { mode, hasOverlay }
      })

    // Helper: find matching binding
    const findBinding = (key: string, mode: string, hasOverlay: boolean) =>
      Effect.gen(function* () {
        const bindings = yield* Ref.get(keybindings)

        // Priority: overlay > specific mode > wildcard
        const effectiveMode = hasOverlay ? "overlay" : mode

        return (
          bindings.find((b) => b.key === key && b.mode === effectiveMode) ??
          bindings.find((b) => b.key === key && b.mode === "*")
        )
      })

    return {
      keybindings,

      handleKey: (key: string) =>
        Effect.gen(function* () {
          const { mode, hasOverlay } = yield* getContext()
          const binding = yield* findBinding(key, mode._tag, hasOverlay)

          if (binding) {
            yield* binding.action
          }
          // Unknown key - ignore (or could show toast in debug mode)
        }),

      register: (binding: Keybinding) =>
        Ref.update(keybindings, (bs) => [...bs, binding]),

      unregister: (key: string, mode: Keybinding["mode"]) =>
        Ref.update(keybindings, (bs) =>
          bs.filter((b) => !(b.key === key && b.mode === mode))
        ),

      getBindings: () => Ref.get(keybindings),
    }
  }),
})

// Usage:
// KeyboardService.Default - includes all dependencies automatically
// KeyboardService.DefaultWithoutDependencies - for testing with mocks
```

### React Bridge (Thin)

```tsx
// src/ui/hooks/useKeyboardBridge.ts

import { useEffect } from "react"
import { useAtomCallback } from "@effect-atom/react"
import { KeyboardService } from "../../services/KeyboardService"

export function useKeyboardBridge() {
  const handleKey = useAtomCallback(
    (key: string) => KeyboardService.pipe(Effect.flatMap((s) => s.handleKey(key)))
  )

  useEffect(() => {
    const listener = (e: KeyboardEvent) => {
      // Normalize key
      const key = e.key
      handleKey(key)
    }

    window.addEventListener("keydown", listener)
    return () => window.removeEventListener("keydown", listener)
  }, [handleKey])
}
```

This keeps React's role minimal - it's just bridging DOM events into Effect-land.

---

### 5. EditorService (Mode FSM)

```typescript
// src/services/EditorService.ts

import { Effect, Ref } from "effect"

export type EditorMode =
  | { readonly _tag: "normal" }
  | { readonly _tag: "select"; readonly selectedIds: ReadonlyArray<string> }
  | { readonly _tag: "command"; readonly input: string }
  | { readonly _tag: "search"; readonly query: string }

export class EditorService extends Effect.Service<EditorService>()("EditorService", {
  effect: Effect.gen(function* () {
    const mode = yield* Ref.make<EditorMode>({ _tag: "normal" })

    const getMode = () => Ref.get(mode)

    return {
      mode,

      enterSelect: () => Ref.set(mode, { _tag: "select", selectedIds: [] }),

      exitSelect: () => Ref.set(mode, { _tag: "normal" }),

      toggleSelection: (taskId: string) =>
        Ref.update(mode, (m) => {
          if (m._tag !== "select") return m
          const has = m.selectedIds.includes(taskId)
          return {
            _tag: "select",
            selectedIds: has
              ? m.selectedIds.filter((id) => id !== taskId)
              : [...m.selectedIds, taskId],
          }
        }),

      enterCommand: () => Ref.set(mode, { _tag: "command", input: "" }),

      updateCommand: (input: string) =>
        Ref.update(mode, (m) =>
          m._tag === "command" ? { ...m, input } : m
        ),

      executeCommand: () =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          if (m._tag !== "command") return
          // Command execution logic here
          yield* Ref.set(mode, { _tag: "normal" })
        }),

      enterSearch: () => Ref.set(mode, { _tag: "search", query: "" }),

      updateSearch: (query: string) =>
        Ref.update(mode, (m) =>
          m._tag === "search" ? { ...m, query } : m
        ),

      exitToNormal: () => Ref.set(mode, { _tag: "normal" }),

      getMode,
    }
  }),
})
```

---

## Cross-Service Coordination

### Pattern: Direct Service Injection

When a service needs another service, declare it in `dependencies`:

```typescript
// src/services/SessionService.ts

import { Effect } from "effect"
import { ToastService } from "./ToastService"
import { NavigationService } from "./NavigationService"

export class SessionService extends Effect.Service<SessionService>()("SessionService", {
  dependencies: [
    ToastService.Default,
    NavigationService.Default,
  ],

  effect: Effect.gen(function* () {
    const toast = yield* ToastService
    const navigation = yield* NavigationService

    return {
      spawn: (taskId: string) =>
        Effect.gen(function* () {
          // Spawn logic...
          yield* toast.show("info", `Spawning session for ${taskId}`)
          yield* navigation.setFollow(taskId)
        }),

      attach: (taskId: string) =>
        Effect.gen(function* () {
          // Attach logic...
          yield* toast.show("info", `Attaching to ${taskId}`)
        }),

      onComplete: (taskId: string) =>
        Effect.gen(function* () {
          // Called when session completes
          yield* toast.show("success", `Session ${taskId} completed!`)
          // Could trigger PR workflow, update board, etc.
        }),
    }
  }),
})
```

### Pattern: Ad-hoc Effect Composition

For one-off orchestrations that don't belong in any service, create effect atoms:

```typescript
// src/atoms/workflows.ts

import { Effect } from "effect"
import { appRuntime } from "./runtime"
import { ToastService } from "../services/ToastService"
import { NavigationService } from "../services/NavigationService"
import { BoardService } from "../services/BoardService"

// One-off workflow: Jump to task and show detail overlay
export const jumpToTaskDetailAtom = appRuntime.fn(
  (taskId: string) =>
    Effect.gen(function* () {
      const nav = yield* NavigationService
      const overlay = yield* OverlayService
      const toast = yield* ToastService

      yield* nav.jumpToTask(taskId)
      yield* overlay.push({ _tag: "detail", taskId })
      yield* toast.show("info", `Viewing ${taskId}`)
    })
)

// Workflow: Bulk move selected tasks
export const bulkMoveTasksAtom = appRuntime.fn(
  (targetStatus: string) =>
    Effect.gen(function* () {
      const editor = yield* EditorService
      const board = yield* BoardService
      const toast = yield* ToastService

      const mode = yield* editor.getMode()
      if (mode._tag !== "select") return

      const count = mode.selectedIds.length
      yield* Effect.forEach(mode.selectedIds, (id) =>
        board.moveTask(id, targetStatus)
      )
      yield* editor.exitToNormal()
      yield* toast.show("success", `Moved ${count} tasks to ${targetStatus}`)
    })
)
```

---

## Effect Atoms for React

### Creating Atoms from Service Refs

```typescript
// src/atoms/ui.ts

import { appRuntime } from "./runtime"
import { ToastService } from "../services/ToastService"
import { OverlayService } from "../services/OverlayService"
import { NavigationService } from "../services/NavigationService"
import { EditorService } from "../services/EditorService"

// Toast atoms
export const toastsAtom = appRuntime.atom(
  ToastService.pipe(
    Effect.flatMap((s) => Ref.get(s.toasts))
  ),
  { initialValue: [] }
)

export const showToastAtom = appRuntime.fn(
  (params: { type: "success" | "error" | "info"; message: string }) =>
    ToastService.pipe(
      Effect.flatMap((s) => s.show(params.type, params.message))
    )
)

// Overlay atoms
export const overlayStackAtom = appRuntime.atom(
  OverlayService.pipe(
    Effect.flatMap((s) => Ref.get(s.stack))
  ),
  { initialValue: [] }
)

export const currentOverlayAtom = appRuntime.atom(
  OverlayService.pipe(
    Effect.flatMap((s) => s.current())
  ),
  { initialValue: undefined }
)

export const pushOverlayAtom = appRuntime.fn(
  (overlay: Overlay) =>
    OverlayService.pipe(Effect.flatMap((s) => s.push(overlay)))
)

export const popOverlayAtom = appRuntime.fn(() =>
  OverlayService.pipe(Effect.flatMap((s) => s.pop()))
)

// Navigation atoms
export const cursorAtom = appRuntime.atom(
  NavigationService.pipe(
    Effect.flatMap((s) => s.getCursor())
  ),
  { initialValue: { columnIndex: 0, taskIndex: 0 } }
)

export const moveAtom = appRuntime.fn(
  (direction: "up" | "down" | "left" | "right") =>
    NavigationService.pipe(Effect.flatMap((s) => s.move(direction)))
)

// Editor atoms
export const editorModeAtom = appRuntime.atom(
  EditorService.pipe(
    Effect.flatMap((s) => s.getMode())
  ),
  { initialValue: { _tag: "normal" } as EditorMode }
)
```

### Using in React Components

```tsx
// src/ui/components/ToastContainer.tsx

import { useAtom } from "@effect-atom/react"
import { toastsAtom } from "../../atoms/ui"

export function ToastContainer() {
  const [toasts] = useAtom(toastsAtom)

  return (
    <Box>
      {toasts.map((toast) => (
        <Toast key={toast.id} toast={toast} />
      ))}
    </Box>
  )
}
```

```tsx
// src/ui/components/Board.tsx

import { useAtom, useAtomSet } from "@effect-atom/react"
import { cursorAtom, moveAtom, editorModeAtom } from "../../atoms/ui"

export function Board() {
  const [cursor] = useAtom(cursorAtom)
  const [mode] = useAtom(editorModeAtom)
  const move = useAtomSet(moveAtom)

  // Keyboard handling is simple - just call the atom functions
  useKeyboard({
    j: () => move("down"),
    k: () => move("up"),
    h: () => move("left"),
    l: () => move("right"),
  })

  return (
    <Box>
      {columns.map((col, i) => (
        <Column
          key={col.status}
          column={col}
          isFocused={cursor.columnIndex === i}
          focusedTaskIndex={cursor.columnIndex === i ? cursor.taskIndex : -1}
          selectionMode={mode._tag === "select"}
        />
      ))}
    </Box>
  )
}
```

---

## Keyboard Handler Refactor

With atomic services, the keyboard handler becomes much simpler:

```typescript
// src/ui/hooks/useAppKeyboard.ts

import { useAtom, useAtomSet } from "@effect-atom/react"
import {
  editorModeAtom,
  moveAtom,
  pushOverlayAtom,
  popOverlayAtom,
  currentOverlayAtom,
} from "../../atoms/ui"

export function useAppKeyboard() {
  const [mode] = useAtom(editorModeAtom)
  const [overlay] = useAtom(currentOverlayAtom)

  const move = useAtomSet(moveAtom)
  const pushOverlay = useAtomSet(pushOverlayAtom)
  const popOverlay = useAtomSet(popOverlayAtom)

  useKeyboard((key) => {
    // Overlay takes precedence
    if (overlay) {
      handleOverlayKey(key, overlay, popOverlay)
      return
    }

    // Delegate to mode-specific handler
    switch (mode._tag) {
      case "normal":
        handleNormalKey(key, { move, pushOverlay })
        break
      case "select":
        handleSelectKey(key, mode)
        break
      case "command":
        handleCommandKey(key, mode)
        break
      case "search":
        handleSearchKey(key, mode)
        break
    }
  })
}

// Pure functions - easy to test
function handleNormalKey(
  key: string,
  actions: { move: (d: Direction) => void; pushOverlay: (o: Overlay) => void }
) {
  const keymap: Record<string, () => void> = {
    j: () => actions.move("down"),
    k: () => actions.move("up"),
    h: () => actions.move("left"),
    l: () => actions.move("right"),
    "?": () => actions.pushOverlay({ _tag: "help" }),
    c: () => actions.pushOverlay({ _tag: "create" }),
    Enter: () => {/* open detail */},
  }
  keymap[key]?.()
}
```

---

## Layer Composition

No dedicated file needed. Just merge whatever your atoms depend on:

```typescript
// src/atoms/runtime.ts

import { Layer } from "effect"
import { KeyboardService } from "../services/KeyboardService"
import { SessionService } from "../services/SessionService"

// That's it. Dependencies are pulled in automatically.
const AppServices = Layer.mergeAll(
  KeyboardService.Default,
  SessionService.Default,
)

export const appRuntime = createAtomRuntime(AppServices)
```

Effect deduplicates automatically - shared deps like `ToastService` become one instance.

### Testing

```typescript
// Test services in isolation - no deps, works standalone
const testToast = ToastService.Default

// Or provide mocks for services with dependencies
const testKeyboard = KeyboardService.DefaultWithoutDependencies.pipe(
  Layer.provide(MockToastService),
  Layer.provide(MockOverlayService),
  // ...
)
```

---

## Implementation Plan

### Phase 1: Core Services (No Dependencies)

1. Create `src/services/ToastService.ts`
2. Create `src/services/OverlayService.ts`
3. Create `src/services/NavigationService.ts`
4. Create `src/services/EditorService.ts`
5. Add unit tests for each service

### Phase 2: Composite Services (With Dependencies)

1. Create `src/services/BoardService.ts` (depends on Navigation)
2. Create `src/services/KeyboardService.ts` (depends on all UI services)
3. Create `src/services/SessionService.ts` (depends on Toast, Navigation)
4. Add tests for composite services

### Phase 3: Effect Atoms

1. Create `src/atoms/runtime.ts` - appRuntime setup
2. Create `src/atoms/ui.ts` - atoms wrapping service refs
3. Create `src/atoms/workflows.ts` - cross-service compositions
4. Add tests for atoms

### Phase 3: React Integration

1. Update `ToastContainer` to use `toastsAtom`
2. Update overlay components to use `overlayStackAtom`
3. Update `Board` to use `cursorAtom`
4. Update mode-dependent rendering to use `editorModeAtom`

### Phase 5: Keyboard Bridge

1. Create `src/ui/hooks/useKeyboardBridge.ts` (thin React → Effect bridge)
2. Wire up DOM keydown events to `KeyboardService.handleKey`
3. Remove old 600+ line keyboard handler
4. Add integration tests for key → action flow

### Phase 6: Migration & Cleanup

1. Remove old useState/useRef patterns from App.tsx
2. Consolidate polling to Effect services
3. Remove duplicate state synchronization
4. Update documentation

---

## Comparison: When to Use Which Plan

| Scenario | Redux Plan | Atomic Plan |
|----------|-----------|-------------|
| **Large team** | ✓ Single source of truth | ✗ Harder to track all state |
| **Time-travel debug** | ✓ Message log | ✗ Would need custom solution |
| **Effect-native** | ✗ Adapts React patterns | ✓ Native service pattern |
| **Fine-grained updates** | ✗ Full state on each change | ✓ Only affected refs update |
| **Service isolation** | ✗ All in one reducer | ✓ Each service testable alone |
| **Learning curve** | Familiar to Redux devs | Familiar to Effect devs |

---

## Benefits of Atomic Approach

### Immediate Benefits

1. **Effect-native**: Uses standard Effect service patterns
2. **Fine-grained reactivity**: Only subscribe to what you need
3. **Service isolation**: Each service is independently testable
4. **Natural DI**: Cross-service calls via Effect's dependency injection
5. **No message boilerplate**: Direct method calls, not action creators

### Long-term Benefits

1. **Scalability**: Add new services without touching existing ones
2. **Composability**: Easy to create ad-hoc workflows
3. **Type safety**: Full inference through Effect's type system
4. **Testing**: Mock individual services, not entire state tree

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| State spread across services | Clear ownership boundaries, documentation |
| Hard to see full state | Create debug atom that combines all service states |
| Circular dependencies | Effect's Layer system prevents this at compile time |
| Over-polling with streams | Use SubscriptionRef or implement proper change detection |

---

## File Structure

```
src/
├── services/
│   ├── ToastService.ts       # Toast state + auto-expiration
│   ├── OverlayService.ts     # Modal stack management
│   ├── NavigationService.ts  # Cursor position
│   ├── EditorService.ts      # Mode FSM (normal/select/command/search)
│   ├── KeyboardService.ts    # Keybinding registry + handleKey
│   ├── SessionService.ts     # Claude session orchestration
│   ├── BoardService.ts       # Task data + mutations
│   └── __tests__/
│       ├── ToastService.test.ts
│       ├── KeyboardService.test.ts
│       └── ...
│
├── atoms/
│   ├── runtime.ts            # appRuntime + Layer.mergeAll(...)
│   ├── ui.ts                 # UI state atoms (cursor, toasts, etc.)
│   ├── workflows.ts          # Ad-hoc cross-service compositions
│   └── __tests__/
│
└── ui/
    └── hooks/
        ├── useKeyboardBridge.ts  # Thin DOM → Effect bridge
        └── __tests__/
```

---

## Recommendation

**Choose this atomic plan if:**
- You want Effect-native patterns
- Fine-grained reactivity matters
- Services should be independently testable
- Team is comfortable with Effect's service model

**Choose the Redux plan if:**
- You want time-travel debugging
- Single source of truth is paramount
- Team is more familiar with Redux patterns
- You need message replay/logging

Both plans solve the core problems (ref sync, scattered state, inconsistent polling). The choice is architectural preference.
