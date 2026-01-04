# Session Retrospective: React Migration & FSM State Management

**Date:** 2025-12-12
**Focus:** Fixing StatusBar reactivity bug, SolidJS to React migration

---

## Summary

Fixed a critical bug where pressing Space to enter action mode didn't update the StatusBar UI, even though the keyboard handler was actually processing action mode commands (tasks moved in backend but UI showed "normal" mode).

Root cause: React stale closures in event handler callbacks passed to external systems.

---

## Wins

### 1. Successful SolidJS to React Migration
- Migrated all 11 UI components from SolidJS to React
- Removed custom `effect-atom-solid` library
- Now using official `@effect-atom/atom-react` integration

### 2. FSM Pattern for Editor State
- Created clean `editorFSM.ts` with explicit state machine
- `useReducer` provides synchronous state updates
- Ref pattern (`stateRef.current`) ensures callbacks always read fresh state

### 3. Proper Atom Refresh Mechanism
- Discovered `useAtomRefresh` hook from effect-atom
- Replaced broken dummy counter pattern that nothing depended on
- UI now updates immediately after mutations

---

## Issues Found

### 1. React Stale Closure Pattern (Critical)
**Problem:** `useKeyboard` callback captured `mode` from `useState` at creation time. When `setMode("action")` was called, React scheduled an async re-render, but the callback still had old `mode = "normal"`.

**Solution:**
```typescript
const stateRef = useRef(editorState)
stateRef.current = editorState  // sync on every render

const dispatch = useCallback((action) => {
  const newState = editorReducer(stateRef.current, action)
  stateRef.current = newState  // Update ref NOW (sync)
  baseDispatch(action)         // Trigger React re-render (async)
}, [])
```

**Learning:** Any callback passed to external event systems (keyboard, timers, websockets) MUST read from refs, not React state.

### 2. Ineffective Refresh Pattern
**Problem:**
```typescript
const [, setRefreshCounter] = useState(0)
const refreshTasks = () => setRefreshCounter((c) => c + 1)  // Nothing depends on this!
```

**Solution:**
```typescript
const refreshTasks = useAtomRefresh(tasksAtom)  // Actually invalidates the atom
```

**Learning:** Know your reactive library's cache invalidation mechanism.

### 3. OpenTUI React Rendering Constraints
**Problem:** OpenTUI's `<text>` component doesn't handle React fragments or nested `<text>` elements.

**Solution:** Use `<box flexDirection="column">` layouts with separate `<text>` elements for each line.

---

## Patterns to Remember

### The Ref Pattern for External Event Systems
```typescript
// For ANY callback passed to external systems:
const stateRef = useRef(state)
stateRef.current = state  // Sync on every render

// In callback: ALWAYS read from ref
useExternalEventSystem((event) => {
  const currentState = stateRef.current  // Fresh!
  // NOT: state (stale!)
})
```

### useReducer + Ref for Synchronous Updates
When you need state changes to be visible immediately (not after next render):
1. Use `useReducer` (dispatch is synchronous within reducer)
2. Keep a ref that mirrors the state
3. Update ref immediately when dispatching

---

## Files Modified

| File | Change |
|------|--------|
| `src/ui/editorFSM.ts` | NEW - FSM for editor modes |
| `src/ui/App.tsx` | Major refactor - useReducer + ref pattern |
| `src/ui/Board.tsx` | SolidJS to React |
| `src/ui/Column.tsx` | SolidJS to React |
| `src/ui/TaskCard.tsx` | SolidJS to React |
| `src/ui/StatusBar.tsx` | SolidJS to React |
| `src/ui/ActionPalette.tsx` | SolidJS to React + redesign (bottom-right) |
| `src/ui/DetailPanel.tsx` | SolidJS to React |
| `src/ui/HelpOverlay.tsx` | SolidJS to React |
| `src/ui/CreateTaskPrompt.tsx` | SolidJS to React |
| `src/ui/Toast.tsx` | SolidJS to React |
| `src/ui/launch.tsx` | SolidJS to React |
| `src/lib/effect-atom-solid/*` | DELETED |
| `package.json` | Removed solid-js, added react |
| `tsconfig.json` | Changed jsxImportSource |
| `bunfig.toml` | Removed solid preload |

---

## Suggested Follow-ups

1. **Add tests for FSM state transitions** - The state machine is now isolated in `editorFSM.ts` and easily testable
2. **Document the ref pattern** - Add to CLAUDE.md or a patterns doc for future debugging
3. **Review other useCallback usages** - Check if any other callbacks might have stale closure issues

---

## Commit

`313fe30` - Migrate UI from SolidJS to React with FSM state management
