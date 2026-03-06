# Effect Resource Management Skill

**Version:** 1.0
**Purpose:** Idiomatic patterns for scopes, acquireRelease, and resource lifecycle
**Source:** Adapted from [Effect Patterns Hub](https://github.com/PaulJPhilp/EffectPatterns)

## Overview

Effect's resource management ensures cleanup **always** runs - on success, failure, or interruption. This is critical for:
- Database connections
- File handles
- Network sockets
- External service clients
- Background fibers

## Core Concept: Scope

A `Scope` collects finalizers (cleanup effects) and executes them when closed. This provides:
- **Guaranteed cleanup**: Even on interruption
- **Reverse order**: Last acquired = first released
- **Composability**: Scopes can be nested

## Basic Pattern: acquireRelease

### Simple Resource

```typescript
import { Effect } from "effect"

// Define a scoped resource
const scopedFile = Effect.acquireRelease(
  // Acquire: open the file
  Effect.sync(() => {
    console.log("Opening file")
    return { handle: "file-handle" }
  }),
  // Release: close the file (always runs)
  (file) =>
    Effect.sync(() => {
      console.log("Closing file:", file.handle)
    }),
)

// Use with Effect.scoped
const program = Effect.scoped(
  Effect.gen(function* () {
    const file = yield* scopedFile
    yield* Effect.log(`Using file: ${file.handle}`)
    // Cleanup runs automatically when scope exits
  }),
)
```

### Why Not Try/Finally?

```typescript
// ❌ WRONG: Try/finally doesn't handle interruption
const unsafe = async () => {
  const conn = await openConnection()
  try {
    await useConnection(conn)
  } finally {
    await closeConnection(conn)  // May not run if fiber interrupted!
  }
}

// ✅ CORRECT: acquireRelease handles all cases
const safe = Effect.acquireRelease(
  openConnection,
  (conn) => closeConnection(conn),
)
```

## Using Resources in Services

### Scoped Service Pattern

```typescript
export class DatabaseService extends Effect.Service<DatabaseService>()("DatabaseService", {
  scoped: Effect.gen(function* () {
    // Acquire database pool
    const pool = yield* Effect.acquireRelease(
      Effect.tryPromise(() => createPool(config)),
      (pool) => Effect.promise(() => pool.end()),
    )

    return {
      query: (sql: string) =>
        Effect.tryPromise(() => pool.query(sql)),
    }
  }),
}) {}

// The pool is:
// - Created when service is first accessed
// - Shared across all uses
// - Cleaned up when application shuts down
```

### Adding Finalizers Manually

```typescript
export class FileWatcherService extends Effect.Service<FileWatcherService>()("FileWatcherService", {
  scoped: Effect.gen(function* () {
    const watcher = createWatcher()

    // Register cleanup
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        watcher.close()
        console.log("Watcher closed")
      }),
    )

    return {
      watch: (path: string) => Effect.sync(() => watcher.add(path)),
    }
  }),
}) {}
```

## Multiple Resources

### Sequential Acquisition

```typescript
const program = Effect.scoped(
  Effect.gen(function* () {
    const db = yield* acquireDatabase
    const cache = yield* acquireCache
    const logger = yield* acquireLogger

    // Use all resources
    yield* doWork(db, cache, logger)

    // Cleanup order: logger, cache, db (reverse of acquisition)
  }),
)
```

### Parallel Acquisition

```typescript
const program = Effect.scoped(
  Effect.gen(function* () {
    // Acquire resources in parallel
    const [db, cache] = yield* Effect.all([
      acquireDatabase,
      acquireCache,
    ])

    yield* doWork(db, cache)
  }),
)
```

## Resource Hierarchies

### Nested Scopes

```typescript
const program = Effect.scoped(
  Effect.gen(function* () {
    const outerResource = yield* acquireOuter

    // Inner scope for temporary resources
    yield* Effect.scoped(
      Effect.gen(function* () {
        const tempResource = yield* acquireTemp
        yield* useTemp(tempResource)
        // tempResource cleaned up here
      }),
    )

    // outerResource still available
    yield* useOuter(outerResource)
    // outerResource cleaned up at end
  }),
)
```

### Layer Composition

Layers automatically manage resource lifecycles:

```typescript
// Each layer acquires/releases its resources
const DatabaseLayer = Layer.scoped(
  DatabaseService,
  Effect.acquireRelease(createPool, closePool),
)

const CacheLayer = Layer.scoped(
  CacheService,
  Effect.acquireRelease(createCache, closeCache),
)

// Compose layers - cleanup is automatic
const AppLayer = Layer.mergeAll(DatabaseLayer, CacheLayer)
```

## Common Patterns

### Database Connection Pool

```typescript
const acquirePool = Effect.acquireRelease(
  Effect.tryPromise({
    try: () => Pool.connect({
      host: "localhost",
      max: 10,
      idleTimeoutMillis: 30000,
    }),
    catch: (e) => new DatabaseConnectionError({ cause: e }),
  }),
  (pool) => Effect.promise(() => pool.end()),
)
```

### HTTP Client

```typescript
const acquireClient = Effect.acquireRelease(
  Effect.sync(() => new HttpClient({ timeout: 5000 })),
  (client) => Effect.sync(() => client.close()),
)
```

### File Handle

```typescript
const acquireFile = (path: string) =>
  Effect.acquireRelease(
    Effect.tryPromise({
      try: () => fs.open(path, "r"),
      catch: (e) => new FileOpenError({ path, cause: e }),
    }),
    (handle) => Effect.promise(() => handle.close()),
  )
```

### Temporary Directory

```typescript
const acquireTempDir = Effect.acquireRelease(
  Effect.tryPromise(() => fs.mkdtemp("/tmp/app-")),
  (dir) => Effect.tryPromise(() => fs.rm(dir, { recursive: true })),
)
```

## Graceful Shutdown Integration

### Application Entry Point

```typescript
const main = Effect.gen(function* () {
  // Build application with all resources
  const runtime = yield* Layer.toRuntime(AppLayer)

  // Run the application
  yield* application.pipe(Effect.provide(runtime))
})

// Run with scope - cleanup on SIGINT/SIGTERM
const fiber = Effect.runFork(
  main.pipe(Effect.scoped),
)

process.on("SIGINT", () => {
  Effect.runPromise(Fiber.interrupt(fiber))
})
```

### Finalizer Order

Finalizers run in **reverse acquisition order**:

```typescript
Effect.scoped(
  Effect.gen(function* () {
    yield* Effect.addFinalizer(() => Effect.log("3: Last cleanup"))
    yield* Effect.addFinalizer(() => Effect.log("2: Middle cleanup"))
    yield* Effect.addFinalizer(() => Effect.log("1: First cleanup"))
  }),
)
// Output:
// 1: First cleanup
// 2: Middle cleanup
// 3: Last cleanup
```

## Advanced: Manual Scope Management

For rare cases where you need explicit scope control:

```typescript
const program = Effect.gen(function* () {
  // Create a scope manually
  const scope = yield* Scope.make()

  // Acquire resource into scope
  const resource = yield* acquireResource.pipe(
    Scope.extend(scope),
  )

  // Use resource...
  yield* useResource(resource)

  // Manually close scope when done
  yield* Scope.close(scope, Exit.succeed(undefined))
})
```

## Anti-Patterns

### Don't Acquire Without Release

```typescript
// ❌ WRONG: No cleanup guarantee
const bad = Effect.tryPromise(() => openConnection())

// ✅ CORRECT: Pair with release
const good = Effect.acquireRelease(
  Effect.tryPromise(() => openConnection()),
  (conn) => Effect.promise(() => conn.close()),
)
```

### Don't Use Outside Scope

```typescript
// ❌ WRONG: Resource used outside scope
const bad = Effect.gen(function* () {
  const resource = yield* Effect.scoped(acquireResource)
  yield* useResource(resource)  // ❌ Resource already released!
})

// ✅ CORRECT: Use within scope
const good = Effect.scoped(
  Effect.gen(function* () {
    const resource = yield* acquireResource
    yield* useResource(resource)  // ✅ Resource still valid
  }),
)
```

### Don't Mix Promise and Effect Cleanup

```typescript
// ❌ WRONG: Promise cleanup not guaranteed
const bad = Effect.gen(function* () {
  const conn = yield* Effect.promise(() => openConnection())
  yield* doWork(conn)
  yield* Effect.promise(() => conn.close())  // May not run!
})

// ✅ CORRECT: Use acquireRelease
const good = Effect.scoped(
  Effect.gen(function* () {
    const conn = yield* Effect.acquireRelease(
      Effect.promise(() => openConnection()),
      (c) => Effect.promise(() => c.close()),
    )
    yield* doWork(conn)
  }),
)
```

## Summary

| Pattern | Description |
|---------|-------------|
| `Effect.acquireRelease` | Pair acquire with guaranteed release |
| `Effect.scoped` | Execute scoped effect, run finalizers |
| `Effect.addFinalizer` | Register cleanup in current scope |
| `Layer.scoped` | Create layer with scoped resource |
| `Scope.make` | Manual scope for advanced cases |
| Service `scoped:` | Service with resource lifecycle |

### Key Rules

1. **Always pair acquisition with release** using `acquireRelease`
2. **Use resources within their scope** - don't return them
3. **Prefer Layer for application-wide resources** - automatic lifecycle
4. **Finalizers run in reverse order** - last in, first out
5. **Interruption triggers cleanup** - no special handling needed
