# Effect Concurrency Skill

**Version:** 1.0
**Purpose:** Idiomatic patterns for fibers, forking, scheduling, and concurrent state
**Source:** Adapted from [Effect Patterns Hub](https://github.com/PaulJPhilp/EffectPatterns)

## Overview

Effect uses **fibers** for concurrency - lightweight virtual threads managed by the runtime. You can create millions of fibers on a single OS thread without the overhead of traditional threading.

## Fibers vs Threads

| Aspect | OS Threads | Effect Fibers |
|--------|-----------|---------------|
| Memory | ~8MB stack each | ~1KB each |
| Limit | Hundreds (system limit) | Millions |
| Scheduling | OS kernel | Effect runtime (cooperative) |
| Best for | CPU-bound parallelism | I/O-bound concurrency |

**Key insight**: Fibers excel at I/O-bound work. For CPU-bound parallelism in Node.js, use worker threads.

## Forking Fibers

### Effect.fork - Fire and Forget

```typescript
// Start background task, get fiber handle
const fiber = yield* Effect.fork(longRunningTask)

// Parent continues immediately
yield* doOtherWork()

// Later: wait for result or interrupt
const result = yield* Fiber.join(fiber)
// or: yield* Fiber.interrupt(fiber)
```

### Effect.forkScoped - Tied to Scope

```typescript
// ✅ CORRECT for long-running background tasks in services
export class PollingService extends Effect.Service<PollingService>()("PollingService", {
  scoped: Effect.gen(function* () {
    // Fiber lives for service lifetime
    yield* pollForUpdates.pipe(
      Effect.repeat(Schedule.spaced("5 seconds")),
      Effect.forkScoped,  // ✅ Tied to service scope
    )

    return { /* methods */ }
  }),
}) {}
```

### Effect.forkDaemon - Independent Fiber

```typescript
// Fiber survives parent interruption
yield* backgroundTask.pipe(Effect.forkDaemon)
// Use sparingly - harder to manage lifecycle
```

### When to Use Each

| Fork Type | Lifetime | Use Case |
|-----------|----------|----------|
| `Effect.fork` | Dies with parent | Short-lived concurrent work |
| `Effect.forkScoped` | Dies with scope | Background tasks in services |
| `Effect.forkDaemon` | Independent | Global background processes |
| `Effect.scheduleForked` | Scoped by default | Scheduled polling |

## CRITICAL: fork vs forkScoped

```typescript
// ❌ WRONG: Fiber dies immediately when start() returns
const start = () =>
  Effect.gen(function* () {
    yield* pollingEffect.pipe(
      Effect.repeat(Schedule.spaced("500 millis")),
      Effect.fork,  // ❌ Dies when start() completes!
    )
  })

// ✅ CORRECT: Use scoped service + forkScoped
export class MyService extends Effect.Service<MyService>()("MyService", {
  scoped: Effect.gen(function* () {  // Note: scoped, not effect
    yield* pollingEffect.pipe(
      Effect.repeat(Schedule.spaced("500 millis")),
      Effect.forkScoped,  // ✅ Lives for service lifetime
    )
    return { /* methods */ }
  }),
}) {}
```

## Scheduling

### Schedule.spaced - Fixed Intervals

```typescript
// Wait between executions
yield* task.pipe(
  Effect.repeat(Schedule.spaced("1 second")),
)
// Runs: task, wait 1s, task, wait 1s, ...
```

### Effect.scheduleForked - Background Scheduling

```typescript
// ✅ PREFERRED: Cleaner than manual fork + repeat
yield* Effect.scheduleForked(Schedule.spaced("5 seconds"))(
  Effect.gen(function* () {
    const data = yield* fetchData()
    yield* SubscriptionRef.set(state, data)
  }),
)
```

### Schedule.spaced Waits BEFORE First Run

```typescript
// ❌ WRONG: Board empty for 2 seconds on startup
yield* Effect.scheduleForked(Schedule.spaced("2 seconds"))(refresh())

// ✅ CORRECT: Initial load + polling
yield* refresh()  // Immediate first load
yield* Effect.scheduleForked(Schedule.spaced("2 seconds"))(refresh())
```

### Common Schedules

```typescript
// Fixed interval
Schedule.spaced("1 second")

// Fixed number of times
Schedule.recurs(5)

// Exponential backoff
Schedule.exponential("100 millis")

// With jitter (randomness)
Schedule.exponential("100 millis").pipe(Schedule.jittered)

// Combined: exponential, max 5 times, capped at 10s
Schedule.exponential("100 millis").pipe(
  Schedule.jittered,
  Schedule.intersect(Schedule.recurs(5)),
  Schedule.whileOutput((delay) => delay < Duration.seconds(10)),
)
```

## Shared State

### Ref - Atomic Mutable State

```typescript
// Create atomic reference
const counter = yield* Ref.make(0)

// Atomic operations
yield* Ref.get(counter)                    // Read
yield* Ref.set(counter, 10)                // Write
yield* Ref.update(counter, (n) => n + 1)   // Transform
yield* Ref.modify(counter, (n) => [n, n + 1])  // Read + transform

// Safe for concurrent access - all operations are atomic
```

### SubscriptionRef - Observable State

```typescript
// Create observable reference
const state = yield* SubscriptionRef.make({ count: 0, name: "" })

// Same operations as Ref
yield* SubscriptionRef.get(state)
yield* SubscriptionRef.set(state, newValue)
yield* SubscriptionRef.update(state, (s) => ({ ...s, count: s.count + 1 }))

// Plus: subscribe to changes
const changes = yield* SubscriptionRef.changes(state)
yield* Stream.runForEach(changes, (value) =>
  Effect.log(`State changed: ${JSON.stringify(value)}`)
)
```

### When to Use Each

| Type | Use Case |
|------|----------|
| `Ref` | Internal service state, counters, flags |
| `SubscriptionRef` | State that React/UI needs to observe |

## Parallel Execution

### Effect.all - Parallel Collection

```typescript
// Run all effects in parallel
const results = yield* Effect.all([
  fetchUser(1),
  fetchUser(2),
  fetchUser(3),
], { concurrency: "unbounded" })
// results: [User, User, User]

// With bounded concurrency
const results = yield* Effect.all(userIds.map(fetchUser), {
  concurrency: 10,  // Max 10 concurrent requests
})
```

### Effect.forEach - Parallel Map

```typescript
// Process items in parallel
const results = yield* Effect.forEach(
  items,
  (item) => processItem(item),
  { concurrency: 5 },
)
```

### Effect.race - First to Complete

```typescript
// Return first successful result
const result = yield* Effect.race([
  fetchFromPrimary(),
  fetchFromBackup(),
])
// Losers are automatically interrupted
```

## Coordination Primitives

### Deferred - One-Shot Promise

```typescript
// Create a promise-like coordination point
const deferred = yield* Deferred.make<string, Error>()

// In one fiber: wait for value
const value = yield* Deferred.await(deferred)

// In another fiber: complete the deferred
yield* Deferred.succeed(deferred, "done")
// or: yield* Deferred.fail(deferred, new Error("failed"))
```

### Queue - Work Distribution

```typescript
// Bounded queue for backpressure
const queue = yield* Queue.bounded<Task>(100)

// Producer
yield* Queue.offer(queue, task)

// Consumer
const task = yield* Queue.take(queue)

// Multiple consumers for parallelism
yield* Effect.forEach(
  Array.range(1, 5),
  () => worker(queue),
  { concurrency: "unbounded" },
)
```

### Semaphore - Rate Limiting

```typescript
// Limit concurrent access
const semaphore = yield* Effect.makeSemaphore(3)

// Only 3 can run concurrently
yield* semaphore.withPermits(1)(expensiveOperation)
```

## Graceful Shutdown

### Pattern: Signal Handler + Interrupt

```typescript
// Main entry point
const main = Effect.gen(function* () {
  // Start application
  const fiber = yield* Effect.fork(application)

  // Handle shutdown signals
  yield* Effect.async<never, never>((resume) => {
    const handler = () => {
      Effect.runPromise(Fiber.interrupt(fiber))
      resume(Effect.never)
    }
    process.on("SIGINT", handler)
    process.on("SIGTERM", handler)
  })
})

// Run with proper cleanup
Effect.runFork(main)
```

### Why This Matters

Without graceful shutdown:
- Database connections leak
- In-flight requests fail
- File handles stay open
- Data corruption possible

Effect's `forkScoped` and finalizers ensure cleanup runs on interruption.

## Anti-Patterns

### Don't Fork Then Immediately Join

```typescript
// ❌ WRONG: Pointless fork
const result = yield* Effect.fork(task).pipe(
  Effect.flatMap(Fiber.join),
)

// ✅ CORRECT: Just run it directly
const result = yield* task
```

### Don't Use Mutable Variables

```typescript
// ❌ WRONG: Race conditions
let count = 0
yield* Effect.forEach(items, () => Effect.sync(() => count++))

// ✅ CORRECT: Use Ref
const count = yield* Ref.make(0)
yield* Effect.forEach(items, () => Ref.update(count, (n) => n + 1))
```

### Lazy Evaluation in Scheduled Effects

```typescript
// ❌ WRONG: Date.now() captured once
yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
  SubscriptionRef.set(now, Date.now())  // ❌ Evaluated once!
)

// ✅ CORRECT: Fresh value each tick
yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
  Effect.flatMap(DateTime.now, (dt) => SubscriptionRef.set(now, dt))
)
```

## Summary

| Pattern | Description |
|---------|-------------|
| `Effect.fork` | Start fiber, dies with parent |
| `Effect.forkScoped` | Fiber tied to enclosing scope |
| `Effect.scheduleForked` | Scheduled background task (scoped) |
| `Schedule.spaced` | Fixed interval between runs |
| `Ref` | Atomic mutable state |
| `SubscriptionRef` | Observable mutable state |
| `Effect.all` | Run effects in parallel |
| `Effect.race` | First to complete wins |
| `Deferred` | One-shot coordination |
| `Queue` | Work distribution channel |
| `Semaphore` | Concurrency limiter |
