# Services Architecture

Azedarach uses the [Effect](https://effect.website/) library for all backend services. Each service follows a consistent pattern using `Effect.Tag` for dependency injection.

## Service Pattern

All services follow this structure:

```typescript
// 1. Service interface
interface MyServiceI {
  readonly method: (arg: string) => Effect.Effect<Result, Error>
}

// 2. Service tag
class MyService extends Context.Tag("MyService")<MyService, MyServiceI>() {}

// 3. Implementation
const MyServiceImpl = Effect.gen(function* () {
  // ... dependencies ...
  return MyService.of({
    method: (arg) => Effect.gen(function* () {
      // ... implementation ...
    })
  })
})

// 4. Layer
export const MyServiceLive = Layer.effect(MyService, MyServiceImpl)

// 5. Convenience functions
export const method = (arg: string) =>
  Effect.flatMap(MyService, (s) => s.method(arg))
```

## Available Services

### BeadsClient

**Location:** `src/core/BeadsClient.ts`

Wrapper around the `bd` CLI for issue tracking operations.

```typescript
interface BeadsClientI {
  list(options?): Effect<Issue[], BeadsError>
  show(id): Effect<Issue, NotFoundError | ParseError>
  create(options): Effect<Issue, BeadsError>
  update(id, fields): Effect<void, NotFoundError>
  close(id, reason?): Effect<void, NotFoundError>
  ready(): Effect<Issue[], BeadsError>
}
```

**Usage:**
```typescript
const program = Effect.gen(function* () {
  const client = yield* BeadsClient
  const issues = yield* client.list({ status: "open" })
  yield* client.update("az-05y", { status: "in_progress" })
})
```

### SessionManager

**Location:** `src/core/SessionManager.ts`

Orchestrates Claude Code sessions in tmux with git worktrees.

```typescript
interface SessionManagerService {
  start(options): Effect<Session, SessionError | GitError>
  stop(beadId): Effect<void, SessionError>
  pause(beadId): Effect<void, SessionError>  // Ctrl+C + WIP commit
  resume(beadId): Effect<void, InvalidStateError>
  getState(beadId): Effect<SessionState, SessionNotFoundError>
  listActive(): Effect<Session[], never>
  subscribeToStateChanges(): Effect<PubSub<SessionStateChange>, never>
}
```

**Session lifecycle:**
1. `start()` → Creates worktree → Spawns tmux session → Runs `claude`
2. `pause()` → Sends Ctrl+C → Creates WIP commit → Updates state
3. `resume()` → Reattaches to tmux → Updates state to busy
4. `stop()` → Kills tmux session → Cleanup

### TmuxService

**Location:** `src/core/TmuxService.ts`

Low-level tmux session management.

```typescript
interface TmuxServiceI {
  newSession(name, opts?): Effect<void, TmuxError>
  killSession(name): Effect<void, TmuxError>
  listSessions(): Effect<TmuxSession[], TmuxError>
  hasSession(name): Effect<boolean, TmuxError>
  sendKeys(session, keys): Effect<void, TmuxError>
  attachCommand(session): string  // Returns command string
}
```

### TerminalService

**Location:** `src/core/TerminalService.ts`

Terminal emulator detection and command execution.

```typescript
interface TerminalServiceI {
  detect(): Effect<TerminalType, never>  // "ghostty" | "iterm" | "terminal-app" | "unknown"
  openWithCommand(cmd): Effect<void, TerminalError>
}
```

**Detection:** Uses `TERM_PROGRAM` environment variable.

### AttachmentService

**Location:** `src/core/AttachmentService.ts`

Session attachment for manual intervention.

```typescript
interface AttachmentServiceI {
  attachExternal(sessionId): Effect<void, AttachmentError | SessionNotFoundError>
  attachInline(sessionId): Effect<void, AttachmentError>  // Not yet implemented
  getAttachmentHistory(): Effect<AttachmentEvent[], never>
  hasAttached(sessionId): Effect<boolean, never>
}
```

**External attachment:**
1. Detects terminal type (Ghostty, iTerm, Terminal.app)
2. Generates `tmux attach-session -t {sessionId}` command
3. Opens new terminal window with that command

### WorktreeManager

**Location:** `src/core/WorktreeManager.ts`

Git worktree lifecycle for isolated task execution.

```typescript
interface WorktreeManagerService {
  create(options): Effect<Worktree, GitError | NotAGitRepoError>
  remove(beadId): Effect<void, GitError | WorktreeNotFoundError>
  list(): Effect<Worktree[], GitError>
  exists(beadId): Effect<boolean, GitError>
  get(beadId): Effect<Worktree, WorktreeNotFoundError>
}
```

**Worktree naming:** `../ProjectName-{beadId}/`

### StateDetector

**Location:** `src/core/StateDetector.ts`

Claude output pattern matching for session state detection.

```typescript
interface StateDetectorI {
  detectFromChunk(chunk): Effect<SessionState | null, never>
  createDetector(): Effect<StatefulDetector, never>
}
```

**Session states:** `idle` | `busy` | `waiting` | `done` | `error`

**Pattern matching:**
- `waiting`: `[y/n]`, `Do you want to`, `Press Enter`
- `error`: `Error:`, `Exception:`, `Failed:`, `ENOENT`
- `done`: `Task completed`, `Successfully`, `Done.`
- `busy`: Any output not matching above

### FileLockManager

**Location:** `src/core/FileLockManager.ts`

Prevents concurrent file access across sessions.

```typescript
interface FileLockManagerService {
  acquireLock(options): Effect<Lock, LockError | LockTimeoutError>
  releaseLock(lock): Effect<void, never>
  getLockState(path): Effect<LockState | null, never>
}

// Convenience function with automatic cleanup
withLock(options, effect): Effect<A, E | LockError, R>
```

**Lock types:**
- `exclusive`: Only one holder, blocks all other locks
- `shared`: Multiple holders allowed, blocks exclusive locks

## Layer Composition

Services are composed using Effect's Layer system:

```typescript
// Base services with no dependencies
const baseLayer = Layer.mergeAll(
  BeadsClientLiveWithPlatform,
  TmuxServiceLive,
  TerminalServiceLive
)

// Services with dependencies
const appLayer = baseLayer.pipe(
  Layer.merge(AttachmentServiceLive.pipe(Layer.provide(baseLayer)))
)

// Full application layer
const fullLayer = Layer.mergeAll(
  SessionManagerLive,
  WorktreeManagerLive,
  StateDetectorLive,
  FileLockManagerLive
).pipe(Layer.provide(BunContext.layer))
```

## Error Handling

All services use typed errors with `Data.TaggedError`:

```typescript
class BeadsError extends Data.TaggedError("BeadsError")<{
  message: string
  command?: string
}> {}

// Pattern matching on errors
yield* someEffect.pipe(
  Effect.catchTag("BeadsError", (e) => ...),
  Effect.catchTag("NotFoundError", (e) => ...)
)
```

## Testing Services

Each service has a `*Test` layer for mocking:

```typescript
const TestBeadsClient = Layer.succeed(BeadsClient, {
  list: () => Effect.succeed([mockIssue]),
  show: (id) => Effect.succeed(mockIssue),
  // ...
})

const testProgram = myProgram.pipe(
  Effect.provide(TestBeadsClient)
)
```
