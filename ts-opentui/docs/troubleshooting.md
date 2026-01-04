# Troubleshooting

## Tmux Popup Persistence on Ctrl-C

### Problem

When running azedarach inside tmux and pressing Ctrl-C to quit, tmux popups (like the editor opened via `c`) could persist or reappear, requiring manual dismissal before the application fully exits.

### Root Cause

**Terminal raw mode changes how Ctrl-C works.**

In a normal terminal, pressing Ctrl-C sends the `SIGINT` signal to the process, which can be caught with `process.on("SIGINT", handler)`. However, TUI frameworks like OpenTUI put the terminal in **raw mode** to capture every keystroke for custom handling.

In raw mode:
- Ctrl-C is **not** converted to SIGINT
- It arrives as a regular key event: `{ ctrl: true, name: "c" }`
- The SIGINT signal handler never fires
- Any cleanup code relying on SIGINT won't run

Additionally, when azedarach spawns a tmux popup for the editor:
- The main process blocks waiting for the editor to close
- During this block, the JavaScript event loop can't process events
- Ctrl-C during the block goes to tmux/the popup, not to our code

### Solution

1. **Explicit keyboard handler for Ctrl-C** - Added a handler in the keyboard callback that catches `event.ctrl && event.name === "c"` and explicitly calls cleanup + exit.

2. **Process tracking for popup cleanup** - Track the temp file path used by the editor so we can find and kill associated processes with `pkill -f`.

3. **Conditional state clearing** - Only clear popup tracking state when the editor exits cleanly (exit code 0), so interrupted operations leave the state intact for cleanup.

### Key Takeaway

When building TUI applications that need to handle Ctrl-C:

```typescript
// Don't rely solely on SIGINT in raw mode terminals
process.on("SIGINT", handler)  // May not fire!

// Also add explicit keyboard handling
useKeyboard((event) => {
  if (event.ctrl && event.name === "c") {
    cleanup()
    process.exit(0)
  }
})
```

---

## Shift+Key Not Working in Keyboard Handlers

### Problem

Pressing Shift+D (to delete a bead) was triggering the lowercase `d` action (cleanup worktree) instead.

### Root Cause

Terminal keyboard libraries typically report key events with:
- `event.name` - always the **base key** (lowercase for letters)
- `event.shift` - boolean indicating if Shift was held

So pressing Shift+D gives `{ name: "d", shift: true }`, not `{ name: "D" }`.

A `switch` statement on `event.name` with `case "D":` will **never match**.

### Solution

Check `event.shift` within the lowercase case:

```typescript
// Wrong - "D" never matches
case "d": { /* cleanup */ }
case "D": { /* delete */ }  // Never reached!

// Correct - check shift flag
case "d": {
  if (event.shift) {
    // Shift+D: delete
  } else {
    // d: cleanup
  }
}
```

### Key Takeaway

When handling shifted keys in terminal applications, always check `event.shift` rather than expecting uppercase `event.name` values.
