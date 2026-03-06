# Testing Guide

This guide explains how to test each feature of Azedarach.

## Prerequisites

```bash
# Ensure dependencies are installed
bun install

# Verify linear is working
az issue list --output json --compact --all --status=open
```

## Testing the TUI

### Basic Startup

```bash
# Start the TUI
bun run dev
```

**Expected:** Kanban board with your linear issues organized by status.

### Navigation Testing

| Test | Steps | Expected Result |
|------|-------|-----------------|
| Basic nav | Press `h`, `j`, `k`, `l` | Selection moves between tasks/columns |
| Arrow keys | Press arrow keys | Same as hjkl |
| Half-page | Press `Ctrl-Shift-d`, `Ctrl-Shift-u` | Fast scrolling in tall columns |
| Column jump | Press `g` then `h` or `l` | Jump to first/last task in column |
| Global jump | Press `g` then `g` or `e` | Jump to first/last task on board |

### Mode Testing

| Test | Steps | Expected Result |
|------|-------|-----------------|
| Help | Press `?` | Help overlay appears |
| Detail | Press `Enter` on task | Detail panel shows |
| Goto | Press `g` | Status bar shows `GTO` |
| Select | Press `v` | Status bar shows `SEL` |
| Action | Press `Space` | Status bar shows `ACT` |
| Escape | Press `Esc` from any mode | Returns to `NOR` |
| Redraw | Press `Ctrl-l` | Screen clears and redraws fully |

### Jump Labels Testing

```bash
# 1. Start TUI with several tasks visible
bun run dev

# 2. Press 'g' then 'w'
# Expected: Each task gets a 2-char label (aa, as, ad, etc.)

# 3. Type a label (e.g., 'as')
# Expected: Selection jumps to that task
```

### Selection Testing

```bash
# 1. Press 'v' to enter Select mode
# 2. Navigate to a task, press Space to select
# 3. Navigate to another task, press Space
# Expected: Both tasks show selection highlight

# 4. Press Esc
# Expected: Selections persist, mode returns to Normal
```

### Task Movement Testing

```bash
# 1. Navigate to a task in the "Open" column
# 2. Press Space, then 'l'
# Expected: Task moves to "In Progress" column

# 3. Press Space, then 'h'
# Expected: Task moves back to "Open"
```

**Verification:**
```bash
# Check the task status changed
az issue get <task-id>
```

### Batch Movement Testing

```bash
# 1. Press 'v' to enter Select mode
# 2. Select multiple tasks with Space
# 3. Press Esc (selections persist)
# 4. Press Space, then 'l'
# Expected: ALL selected tasks move right
```

## Testing Session Attachment

Session attachment requires tmux sessions to exist. Here's how to test:

### Create Test Session

```bash
# Create a tmux session with a matching name
# Format: claude-{bead-id}

# Example for bead az-05y:
tmux new-session -d -s claude-az-05y "bash -c 'echo Claude session && sleep 3600'"

# Verify it exists
tmux list-sessions
```

### Test External Attachment

```bash
# 1. Start Azedarach
bun run dev

# 2. Navigate to the task matching your tmux session (az-05y)
# 3. Press Space, then 'a'
# Expected: New terminal window opens attached to the tmux session
```

### Test Attachment Failure

```bash
# 1. Navigate to a task with NO matching tmux session
# 2. Press Space, then 'a'
# Expected: Nothing visible happens (error goes to console)

# 3. Check console output
# Expected: "Failed to attach to session: SessionNotFoundError"
```

### Manual Attachment Verification

```bash
# If automatic attachment doesn't work, try manually:
tmux attach-session -t claude-az-05y

# This verifies tmux is working correctly
```

## Testing Services Directly

### BeadsClient

```bash
# Run the example file
bun run src/core/BeadsClient.example.ts

# Or test specific operations:
bun -e "
import { Effect } from 'effect'
import { BeadsClient, BeadsClientLiveWithPlatform } from './src/core/BeadsClient'

const program = Effect.gen(function* () {
  const client = yield* BeadsClient
  const issues = yield* client.list()
  console.log('Issues:', issues.length)
})

Effect.runPromise(program.pipe(Effect.provide(BeadsClientLiveWithPlatform)))
"
```

### TmuxService

```bash
# Test tmux operations
bun -e "
import { Effect } from 'effect'
import { TmuxService, TmuxServiceLive } from './src/core/TmuxService'
import { BunContext } from '@effect/platform-bun'

const program = Effect.gen(function* () {
  const tmux = yield* TmuxService
  const sessions = yield* tmux.listSessions()
  console.log('Tmux sessions:', sessions)
})

Effect.runPromise(program.pipe(
  Effect.provide(TmuxServiceLive),
  Effect.provide(BunContext.layer)
))
"
```

### FileLockManager

```bash
# Run the example file with 8 test scenarios
bun run src/core/FileLockManager.example.ts

# Or run the integration test
bun run src/core/FileLockManager.test.ts
```

### TerminalService

```bash
# Test terminal detection
bun -e "
import { Effect } from 'effect'
import { TerminalService, TerminalServiceLive } from './src/core/TerminalService'
import { BunContext } from '@effect/platform-bun'

const program = Effect.gen(function* () {
  const terminal = yield* TerminalService
  const type = yield* terminal.detect()
  console.log('Detected terminal:', type)
})

Effect.runPromise(program.pipe(
  Effect.provide(TerminalServiceLive),
  Effect.provide(BunContext.layer)
))
"
```

### Linear Backend Sync (Rare Integration)

This test performs real Linear API operations (create + close via sync queue), so run it rarely.

```bash
# Opt-in only: skips by default unless flag is set to 1
AZEDARACH_RUN_LINEAR_SYNC_BACKEND_INTEGRATION=1 \
LINEAR_API_KEY=lin_api_xxx \
bun test src/core/BackendSyncLinear.integration.test.ts

# Optional: override the Linear team key used by the integration config
AZEDARACH_LINEAR_TEST_TEAM=AZE
```

## Type Checking

```bash
# Verify all types are correct
bun run type-check

# Expected: No errors
```

## Effect Schedule Testing (TestClock)

Auto-recovery retry schedules are validated with non-live Effect tests using `@effect/vitest` and `TestClock`.

```bash
# Run Effect Vitest schedule tests
bunx vitest run
```

Coverage includes:
- Exponential + jitter retry progression under virtual time
- Max retry-wait cap behavior (`retryMaxDelayMs`)

## Common Issues

### "Cannot find module" errors

```bash
# Ensure dependencies are installed
bun install
```

### "TmuxNotFoundError"

```bash
# Verify tmux is installed
which tmux
tmux -V

# Install if needed (macOS)
brew install tmux
```

### "BEADS_DIR not found"

```bash
# Ensure you're in a project with issue tracker data
ls -la .azedarach/

# Prime issue guidance for the session
az prime
```

### Rendering issues

1. Resize terminal window
2. Restart the application
3. Try a different terminal with true color support
4. Check terminal supports true color:
   ```bash
   echo $COLORTERM  # Should be "truecolor"
   ```
