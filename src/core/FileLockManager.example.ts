/**
 * FileLockManager usage examples
 *
 * Demonstrates various locking patterns and scenarios.
 */

import { Effect, Duration, Console, Fiber } from "effect"
import {
  FileLockManager,
  FileLockManagerLive,
  withLock,
  acquireLock,
  releaseLock,
  getLockState,
  type Lock,
} from "./FileLockManager.js"

// ============================================================================
// Example 1: Basic exclusive lock with scoped cleanup
// ============================================================================

const example1_ScopedExclusiveLock = Effect.gen(function* () {
  yield* Console.log("Example 1: Scoped exclusive lock")

  // withLock automatically handles acquire/release
  const result = yield* withLock(
    {
      path: "/tmp/example.txt",
      type: "exclusive",
      timeout: Duration.seconds(5),
      sessionId: "session-1",
    },
    Effect.gen(function* () {
      yield* Console.log("  → Lock acquired, doing work...")
      yield* Effect.sleep(Duration.millis(100))
      yield* Console.log("  → Work complete")
      return "done"
    })
  )

  yield* Console.log(`  → Result: ${result}`)
  yield* Console.log("  → Lock automatically released\n")
})

// ============================================================================
// Example 2: Manual lock management
// ============================================================================

const example2_ManualLockManagement = Effect.gen(function* () {
  yield* Console.log("Example 2: Manual lock management")

  const manager = yield* FileLockManager

  // Manually acquire lock
  const lock = yield* manager.acquireLock({
    path: "/tmp/example2.txt",
    type: "exclusive",
    sessionId: "session-2",
  })

  yield* Console.log(`  → Lock acquired: ${lock.id}`)

  try {
    yield* Console.log("  → Doing work...")
    yield* Effect.sleep(Duration.millis(100))
  } finally {
    // Always release in finally block
    yield* manager.releaseLock(lock)
    yield* Console.log("  → Lock released\n")
  }
})

// ============================================================================
// Example 3: Shared (read) locks
// ============================================================================

const example3_SharedLocks = Effect.gen(function* () {
  yield* Console.log("Example 3: Multiple shared locks")

  // Multiple readers can hold shared locks simultaneously
  const readers = [1, 2, 3].map((i) =>
    withLock(
      {
        path: "/tmp/shared.txt",
        type: "shared",
        sessionId: `reader-${i}`,
      },
      Effect.gen(function* () {
        yield* Console.log(`  → Reader ${i}: Lock acquired`)
        yield* Effect.sleep(Duration.millis(200))
        yield* Console.log(`  → Reader ${i}: Done reading`)
        return i
      })
    )
  )

  // All readers run concurrently
  const results = yield* Effect.all(readers, { concurrency: "unbounded" })
  yield* Console.log(`  → All readers complete: ${results}\n`)
})

// ============================================================================
// Example 4: Lock conflict (exclusive blocks shared)
// ============================================================================

const example4_LockConflict = Effect.gen(function* () {
  yield* Console.log("Example 4: Lock conflict handling")

  const manager = yield* FileLockManager

  // Acquire exclusive lock
  const exclusiveLock = yield* manager.acquireLock({
    path: "/tmp/conflict.txt",
    type: "exclusive",
    sessionId: "writer",
  })

  yield* Console.log("  → Exclusive lock acquired")

  // Try to acquire shared lock with short timeout (will fail)
  const sharedLockAttempt = manager
    .acquireLock({
      path: "/tmp/conflict.txt",
      type: "shared",
      timeout: Duration.millis(100),
      sessionId: "reader",
    })
    .pipe(
      Effect.tap(() => Console.log("  → Shared lock acquired (unexpected!)")),
      Effect.catchTag("LockTimeoutError", (error) =>
        Console.log(`  → Shared lock timed out as expected (${error.timeout})`).pipe(
          Effect.as(null as Lock | null)
        )
      )
    )

  yield* sharedLockAttempt

  // Release exclusive lock
  yield* manager.releaseLock(exclusiveLock)
  yield* Console.log("  → Exclusive lock released\n")
})

// ============================================================================
// Example 5: Wait queue processing
// ============================================================================

const example5_WaitQueue = Effect.gen(function* () {
  yield* Console.log("Example 5: Wait queue processing")

  const manager = yield* FileLockManager

  // Acquire exclusive lock
  const firstLock = yield* manager.acquireLock({
    path: "/tmp/queue.txt",
    type: "exclusive",
    sessionId: "first",
  })

  yield* Console.log("  → First lock acquired")

  // Start second lock acquisition (will wait)
  const secondLockFiber = yield* Effect.fork(
    Effect.gen(function* () {
      yield* Console.log("  → Second lock waiting...")
      const lock = yield* manager.acquireLock({
        path: "/tmp/queue.txt",
        type: "exclusive",
        sessionId: "second",
      })
      yield* Console.log("  → Second lock acquired!")
      return lock
    })
  )

  // Give the second lock time to enter wait queue
  yield* Effect.sleep(Duration.millis(100))

  // Check lock state
  const state = yield* manager.getLockState("/tmp/queue.txt")
  if (state) {
    yield* Console.log(`  → Lock state: exclusive=${state.exclusiveHolder}, waiting=${state.waitingCount}`)
  }

  // Release first lock (second lock should be granted)
  yield* Console.log("  → Releasing first lock...")
  yield* manager.releaseLock(firstLock)

  // Wait for second lock to be acquired
  const secondLock = yield* Fiber.join(secondLockFiber)

  // Clean up
  yield* manager.releaseLock(secondLock)
  yield* Console.log("  → Second lock released\n")
})

// ============================================================================
// Example 6: Lock state inspection
// ============================================================================

const example6_LockStateInspection = Effect.gen(function* () {
  yield* Console.log("Example 6: Lock state inspection")

  const manager = yield* FileLockManager

  // Initially no locks
  const initialState = yield* manager.getLockState("/tmp/inspect.txt")
  yield* Console.log(`  → Initial state: ${initialState === null ? "no locks" : "has locks"}`)

  // Acquire shared locks
  const lock1 = yield* manager.acquireLock({
    path: "/tmp/inspect.txt",
    type: "shared",
    sessionId: "reader-1",
  })
  const lock2 = yield* manager.acquireLock({
    path: "/tmp/inspect.txt",
    type: "shared",
    sessionId: "reader-2",
  })

  const sharedState = yield* manager.getLockState("/tmp/inspect.txt")
  if (sharedState) {
    yield* Console.log(`  → Shared state: ${sharedState.sharedHolders.length} readers`)
  }

  // Release locks
  yield* manager.releaseLock(lock1)
  yield* manager.releaseLock(lock2)

  const finalState = yield* manager.getLockState("/tmp/inspect.txt")
  yield* Console.log(`  → Final state: ${finalState === null ? "no locks (cleaned up)" : "has locks"}\n`)
})

// ============================================================================
// Example 7: Automatic cleanup on interruption
// ============================================================================

const example7_InterruptionCleanup = Effect.gen(function* () {
  yield* Console.log("Example 7: Automatic cleanup on interruption")

  const manager = yield* FileLockManager

  // Start a long-running locked operation
  const fiber = yield* Effect.fork(
    withLock(
      {
        path: "/tmp/interrupt.txt",
        type: "exclusive",
        sessionId: "long-task",
      },
      Effect.gen(function* () {
        yield* Console.log("  → Lock acquired, starting long task...")
        yield* Effect.sleep(Duration.seconds(10))
        yield* Console.log("  → Task complete (should not reach here)")
      })
    )
  )

  // Give it time to acquire lock
  yield* Effect.sleep(Duration.millis(100))

  // Check that lock is held
  const beforeState = yield* manager.getLockState("/tmp/interrupt.txt")
  if (beforeState) {
    yield* Console.log(`  → Lock is held: exclusive=${beforeState.exclusiveHolder !== null}`)
  }

  // Interrupt the fiber
  yield* Console.log("  → Interrupting fiber...")
  yield* Fiber.interrupt(fiber)

  // Lock should be released automatically
  const afterState = yield* manager.getLockState("/tmp/interrupt.txt")
  yield* Console.log(`  → Lock state after interrupt: ${afterState === null ? "released" : "still held"}\n`)
})

// ============================================================================
// Example 8: Timeout handling
// ============================================================================

const example8_TimeoutHandling = Effect.gen(function* () {
  yield* Console.log("Example 8: Timeout handling")

  const manager = yield* FileLockManager

  // Acquire a lock
  const lock = yield* manager.acquireLock({
    path: "/tmp/timeout.txt",
    type: "exclusive",
    sessionId: "holder",
  })

  yield* Console.log("  → Lock acquired")

  // Try to acquire with very short timeout
  const result = yield* manager
    .acquireLock({
      path: "/tmp/timeout.txt",
      type: "exclusive",
      timeout: Duration.millis(50),
      sessionId: "waiter",
    })
    .pipe(
      Effect.map(() => "acquired" as const),
      Effect.catchTag("LockTimeoutError", (error) =>
        Console.log(`  → Timed out after ${Duration.toMillis(error.timeout)}ms`).pipe(
          Effect.as("timeout" as const)
        )
      )
    )

  yield* Console.log(`  → Result: ${result}`)

  // Clean up
  yield* manager.releaseLock(lock)
  yield* Console.log("  → Lock released\n")
})

// ============================================================================
// Run all examples
// ============================================================================

const runAllExamples = Effect.gen(function* () {
  yield* Console.log("=".repeat(60))
  yield* Console.log("FileLockManager Examples")
  yield* Console.log("=".repeat(60) + "\n")

  yield* example1_ScopedExclusiveLock
  yield* example2_ManualLockManagement
  yield* example3_SharedLocks
  yield* example4_LockConflict
  yield* example5_WaitQueue
  yield* example6_LockStateInspection
  yield* example7_InterruptionCleanup
  yield* example8_TimeoutHandling

  yield* Console.log("=".repeat(60))
  yield* Console.log("All examples complete!")
  yield* Console.log("=".repeat(60))
})

// Main program
const program = runAllExamples.pipe(
  Effect.provide(FileLockManagerLive)
)

// Run if executed directly
// if (import.meta.main) {
//   Effect.runPromise(program).catch(console.error)
// }

export { program }
