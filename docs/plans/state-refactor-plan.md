# Refactoring App State to Effect Atom Runtime Services with FSM/Actor Model

## Problem Statement

The current state management has several pain points:

### 1. Fragmented State (App.tsx)
```typescript
// FSM state for editor modes
const [editorState, baseDispatch] = useReducer(editorReducer, initialEditorState)

// Navigation state (separate from FSM)
const [nav, setNav] = useState<NavigationState>({ columnIndex: 0, taskIndex: 0 })

// Overlay states (4 separate booleans)
const [showHelp, setShowHelp] = useState(false)
const [showDetail, setShowDetail] = useState(false)
const [showCreatePrompt, setShowCreatePrompt] = useState(false)
const [showSettings, setShowSettings] = useState(false)

// Follow-up navigation
const [followTaskId, setFollowTaskId] = useState<string | null>(null)

// Toast notifications
const [toasts, setToasts] = useState<ToastMessage[]>([])
```

### 2. Manual Ref Synchronization
Every piece of state needs a corresponding ref for the keyboard handler:
```typescript
const stateRef = useRef<EditorState>(editorState)
stateRef.current = editorState  // Sync on every render

const navRef = useRef(nav)
navRef.current = nav  // Sync on every render

// ... and so on for every state piece
```

### 3. 600+ Line Keyboard Handler
- Reads from multiple refs
- Triggers multiple async actions
- Complex conditional logic
- Hard to test in isolation

### 4. Inconsistent Polling
- Some uses Effect.scheduleForked (vcStatusPollerAtom) ✓
- Some uses setInterval (duplicate VC polling in App.tsx) ✗
- Toast timers use manual setTimeout management ✗

### 5. Scattered Async Error Handling
```typescript
createTask(params)
  .then((issue) => {
    setShowCreatePrompt(false)
    refreshTasks()
    showSuccess(`Created task: ${issue.id}`)
  })
  .catch((error) => {
    setShowCreatePrompt(false)
    showError(`Failed to create task: ${error}`)
  })
```
This pattern repeats 20+ times.

---

## Solution: Unified AppState Machine with Effect Atoms

### Core Concepts

1. **Single Source of Truth**: All UI state in one Effect-managed state machine
2. **Message Passing**: Actions as typed messages, not direct mutations
3. **Effect Scheduling**: All timers/polling through Effect, not setTimeout/setInterval
4. **Optimistic Updates**: Immediate state updates with rollback on failure
5. **Derived State**: Computed values via Effect atoms

---

## Architecture

### 1. Unified AppState Type

```typescript
// src/ui/state/types.ts

export interface AppState {
  // Navigation
  navigation: NavigationState

  // Editor mode FSM (existing)
  editor: EditorState

  // Overlay stack (modal management)
  overlays: OverlayState

  // Notifications
  toasts: ToastState

  // Async operation tracking
  operations: OperationState

  // Follow-up navigation
  followTaskId: string | null
}

export interface NavigationState {
  columnIndex: number
  taskIndex: number
  focusedTaskId: string | null
}

export interface OverlayState {
  stack: Overlay[]  // Stack-based for proper nesting
}

export type Overlay =
  | { type: 'help' }
  | { type: 'detail'; taskId: string }
  | { type: 'create' }
  | { type: 'settings' }
  | { type: 'confirm'; message: string; onConfirm: () => void }

export interface ToastState {
  messages: Toast[]
}

export interface Toast {
  id: string
  type: 'success' | 'error' | 'info'
  message: string
  expiresAt: number  // Timestamp for Effect scheduling
}

export interface OperationState {
  pending: Map<string, PendingOperation>
}

export interface PendingOperation {
  id: string
  type: string
  startedAt: number
  optimisticUpdate?: () => void
  rollback?: () => void
}
```

### 2. Message Types (Actor Model)

```typescript
// src/ui/state/messages.ts

export type AppMessage =
  // Navigation
  | { _tag: 'Navigate'; direction: 'up' | 'down' | 'left' | 'right' }
  | { _tag: 'NavigateTo'; columnIndex: number; taskIndex: number }
  | { _tag: 'JumpToTask'; taskId: string }

  // Editor mode transitions (delegates to existing FSM)
  | { _tag: 'EditorAction'; action: EditorAction }

  // Overlay management
  | { _tag: 'PushOverlay'; overlay: Overlay }
  | { _tag: 'PopOverlay' }
  | { _tag: 'ClearOverlays' }

  // Toast notifications
  | { _tag: 'ShowToast'; toast: Omit<Toast, 'id' | 'expiresAt'> }
  | { _tag: 'DismissToast'; id: string }
  | { _tag: 'ExpireToasts' }

  // Async operation tracking
  | { _tag: 'StartOperation'; operation: PendingOperation }
  | { _tag: 'CompleteOperation'; id: string; success: boolean }

  // Follow-up
  | { _tag: 'SetFollowTask'; taskId: string | null }

  // Batch updates (for optimistic updates)
  | { _tag: 'Batch'; messages: AppMessage[] }
```

### 3. AppState Atom with SubscriptionRef

```typescript
// src/ui/state/appStateAtom.ts

import { Effect, SubscriptionRef, PubSub, Fiber, Schedule } from "effect"
import { Atom } from "@effect-atom/atom"

// The core state atom - holds the SubscriptionRef
export const appStateRefAtom = appRuntime.atom(
  SubscriptionRef.make<AppState>(initialAppState),
  { initialValue: undefined }
)

// Message hub for actor-style message passing
export const messageHubAtom = appRuntime.atom(
  PubSub.unbounded<AppMessage>(),
  { initialValue: undefined }
)

// The processor that handles messages
export const appStateProcessorAtom = appRuntime.atom(
  (get) => Effect.gen(function* () {
    const stateRef = yield* get.result(appStateRefAtom)
    const hub = yield* get.result(messageHubAtom)

    // Subscribe to messages and process them
    yield* PubSub.subscribe(hub).pipe(
      Stream.runForEach((message) =>
        SubscriptionRef.update(stateRef, (state) =>
          appStateReducer(state, message)
        )
      ),
      Effect.forkScoped  // Auto-cleanup on unmount
    )

    // Toast expiration scheduler
    yield* Effect.scheduleForked(Schedule.spaced("100 milliseconds"))(
      Effect.gen(function* () {
        const state = yield* SubscriptionRef.get(stateRef)
        const now = Date.now()
        const expired = state.toasts.messages.filter(t => t.expiresAt <= now)
        if (expired.length > 0) {
          yield* SubscriptionRef.update(stateRef, (s) => ({
            ...s,
            toasts: {
              messages: s.toasts.messages.filter(t => t.expiresAt > now)
            }
          }))
        }
      })
    )
  }),
  { initialValue: undefined }
)

// Read-only stream of state changes
export const appStateAtom = appRuntime.atom(
  (get) => pipe(
    get.result(appStateRefAtom),
    Effect.map((ref) => ref.changes),
    Stream.unwrap
  ),
  { initialValue: initialAppState }
)
```

### 4. Pure Reducer (Predictable State Transitions)

```typescript
// src/ui/state/reducer.ts

export function appStateReducer(state: AppState, message: AppMessage): AppState {
  switch (message._tag) {
    case 'Navigate':
      return handleNavigation(state, message.direction)

    case 'NavigateTo':
      return {
        ...state,
        navigation: {
          ...state.navigation,
          columnIndex: message.columnIndex,
          taskIndex: message.taskIndex,
        }
      }

    case 'EditorAction':
      return {
        ...state,
        editor: editorReducer(state.editor, message.action)
      }

    case 'PushOverlay':
      return {
        ...state,
        overlays: {
          stack: [...state.overlays.stack, message.overlay]
        }
      }

    case 'PopOverlay':
      return {
        ...state,
        overlays: {
          stack: state.overlays.stack.slice(0, -1)
        }
      }

    case 'ShowToast':
      return {
        ...state,
        toasts: {
          messages: [
            ...state.toasts.messages,
            {
              id: crypto.randomUUID(),
              ...message.toast,
              expiresAt: Date.now() + TOAST_DURATION_MS
            }
          ]
        }
      }

    case 'Batch':
      return message.messages.reduce(appStateReducer, state)

    // ... other cases
  }
}
```

### 5. Dispatch Function (Message Sending)

```typescript
// src/ui/state/dispatch.ts

export const dispatchAtom = appRuntime.fn(
  (message: AppMessage, get) => Effect.gen(function* () {
    const hub = yield* get.result(messageHubAtom)
    yield* PubSub.publish(hub, message)
  })
)

// Optimistic action helper
export const optimisticActionAtom = appRuntime.fn(
  <A, E>(params: {
    optimisticMessage: AppMessage
    effect: Effect.Effect<A, E>
    rollbackMessage?: AppMessage
    successMessage?: (result: A) => AppMessage
    errorMessage?: (error: E) => AppMessage
  }, get) => Effect.gen(function* () {
    const hub = yield* get.result(messageHubAtom)

    // Apply optimistic update immediately
    yield* PubSub.publish(hub, params.optimisticMessage)

    // Run the effect
    const result = yield* params.effect.pipe(
      Effect.tapError(() => {
        // Rollback on error
        if (params.rollbackMessage) {
          return PubSub.publish(hub, params.rollbackMessage)
        }
        return Effect.void
      }),
      Effect.tap((result) => {
        // Success message
        if (params.successMessage) {
          return PubSub.publish(hub, params.successMessage(result))
        }
        return Effect.void
      })
    )

    return result
  })
)
```

### 6. Keyboard Handler Refactor

```typescript
// src/ui/hooks/useAppKeyboard.ts

export function useAppKeyboard() {
  const dispatch = useAtomSet(dispatchAtom, { mode: 'sync' })
  const [state] = useAtom(appStateAtom)

  // Actions are now simple message dispatches
  const handleKey = useCallback((key: string) => {
    // No refs needed - state is synchronously available
    const currentState = state

    // Handle overlays first (they capture input)
    if (currentState.overlays.stack.length > 0) {
      if (key === 'Escape') {
        dispatch({ _tag: 'PopOverlay' })
        return
      }
      // Overlay-specific handling...
      return
    }

    // Mode-specific handling
    switch (currentState.editor.mode) {
      case 'normal':
        handleNormalMode(key, dispatch)
        break
      case 'select':
        handleSelectMode(key, dispatch, currentState)
        break
      // ...
    }
  }, [state, dispatch])

  useKeyboard(handleKey)
}

// Separate pure functions for each mode
function handleNormalMode(key: string, dispatch: Dispatch) {
  switch (key) {
    case 'j':
      dispatch({ _tag: 'Navigate', direction: 'down' })
      break
    case 'k':
      dispatch({ _tag: 'Navigate', direction: 'up' })
      break
    case 'h':
      dispatch({ _tag: 'Navigate', direction: 'left' })
      break
    case 'l':
      dispatch({ _tag: 'Navigate', direction: 'right' })
      break
    case '?':
      dispatch({ _tag: 'PushOverlay', overlay: { type: 'help' } })
      break
    // ...
  }
}
```

---

## Implementation Plan

### Phase 1: Foundation (State Types and Core Atoms)

1. Create `src/ui/state/types.ts` - unified state types
2. Create `src/ui/state/messages.ts` - message type definitions
3. Create `src/ui/state/reducer.ts` - pure reducer function
4. Create `src/ui/state/atoms.ts` - appStateRefAtom, messageHubAtom, dispatchAtom
5. Add tests for reducer (pure function, easy to test)

### Phase 2: Toast Migration

1. Move toast state from useState to unified state
2. Replace setTimeout-based expiration with Effect.scheduleForked
3. Update ToastContainer to read from appStateAtom
4. Remove manual timer cleanup

### Phase 3: Overlay Migration

1. Replace individual showX booleans with overlay stack
2. Update overlay components to use stack-based rendering
3. Add proper overlay nesting support (modals on modals)

### Phase 4: Navigation Migration

1. Move navigation state to unified state
2. Update Column/TaskCard focus logic
3. Remove navRef synchronization

### Phase 5: Editor FSM Integration

1. Integrate existing editorFSM into unified state
2. Route EditorAction messages through main reducer
3. Remove stateRef synchronization

### Phase 6: Keyboard Handler Refactor

1. Create `useAppKeyboard` hook with message-based dispatch
2. Split into mode-specific handler functions
3. Remove all useRef synchronization
4. Add comprehensive tests for key handling

### Phase 7: Optimistic Updates

1. Implement optimisticActionAtom pattern
2. Migrate task operations (move, create, etc.)
3. Add loading/error states to operations tracking
4. Show pending operations in UI

### Phase 8: Cleanup

1. Remove duplicate VC polling (App.tsx setInterval)
2. Consolidate all polling to Effect.scheduleForked
3. Remove unused refs and state
4. Update documentation

---

## Migration Strategy

### Incremental Adoption

We can migrate incrementally by:

1. **Parallel State**: Run both systems simultaneously during migration
2. **Feature Flags**: Enable new state system per-feature
3. **Bridge Pattern**: Create adapters that sync old state to new atoms

```typescript
// Bridge during migration
export function useBridgedState() {
  const [oldState, setOldState] = useState(...)
  const dispatch = useAtomSet(dispatchAtom)

  // Sync old state changes to new system
  useEffect(() => {
    dispatch({ _tag: 'SyncLegacyState', state: oldState })
  }, [oldState])

  return { oldState, setOldState }
}
```

### Testing Strategy

1. **Unit Tests**: Pure reducer is trivial to test
2. **Integration Tests**: Message dispatch and state updates
3. **Snapshot Tests**: State transitions for complex scenarios

```typescript
describe('appStateReducer', () => {
  it('navigates down', () => {
    const state = initialAppState
    const result = appStateReducer(state, { _tag: 'Navigate', direction: 'down' })
    expect(result.navigation.taskIndex).toBe(1)
  })

  it('handles batch messages', () => {
    const state = initialAppState
    const result = appStateReducer(state, {
      _tag: 'Batch',
      messages: [
        { _tag: 'Navigate', direction: 'down' },
        { _tag: 'ShowToast', toast: { type: 'success', message: 'Done' } }
      ]
    })
    expect(result.navigation.taskIndex).toBe(1)
    expect(result.toasts.messages).toHaveLength(1)
  })
})
```

---

## Benefits

### Immediate Benefits

1. **No More Ref Sync**: State is synchronously available everywhere
2. **Predictable Updates**: All state changes through pure reducer
3. **Easy Testing**: Pure functions are trivial to test
4. **Optimistic Updates**: Built-in pattern for immediate feedback

### Long-term Benefits

1. **Time Travel Debugging**: Message log enables replay
2. **Undo/Redo**: Natural with message-based architecture
3. **Persistence**: Serialize state for session recovery
4. **Plugin System**: External actors can observe/inject messages

---

## File Structure

```
src/ui/state/
├── types.ts          # AppState, NavigationState, OverlayState, etc.
├── messages.ts       # AppMessage union type
├── reducer.ts        # Pure appStateReducer function
├── atoms.ts          # appStateRefAtom, messageHubAtom, dispatchAtom
├── selectors.ts      # Derived state computations
├── optimistic.ts     # Optimistic update helpers
└── __tests__/
    ├── reducer.test.ts
    └── selectors.test.ts

src/ui/hooks/
├── useAppKeyboard.ts # Refactored keyboard handler
├── useAppState.ts    # Convenience hook for state access
└── useDispatch.ts    # Typed dispatch hook
```

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Large refactor scope | Incremental migration with bridge pattern |
| Breaking existing functionality | Comprehensive test suite before migration |
| Performance regression | Effect's built-in batching and scheduling |
| Learning curve for team | Documentation and examples |

---

## Success Criteria

1. Zero `useRef` for state synchronization in App.tsx
2. All timers/polling through Effect scheduling
3. Pure reducer with 100% test coverage
4. Keyboard handler under 100 lines (split by mode)
5. Optimistic updates for all mutation operations
