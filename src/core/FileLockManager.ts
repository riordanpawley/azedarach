/**
 * FileLockManager - Effect service for preventing concurrent file access
 *
 * Implements socket-based file locking to prevent multiple Claude sessions from
 * editing the same file simultaneously. Supports both exclusive (write) and
 * shared (read) locks with automatic cleanup on fiber interruption.
 *
 * Key features:
 * - In-memory lock tracking via Ref<HashMap>
 * - Shared vs exclusive lock semantics
 * - Automatic timeout handling
 * - Fiber interruption cleanup
 * - Scoped lock acquisition for guaranteed release
 * - Path normalization for consistent lock granularity
 *
 * Socket path: /tmp/azedarach-<project-hash>.sock
 */

import { Effect, Context, Layer, Data, Ref, HashMap, Deferred, Duration } from "effect"
import { normalizePath } from "./paths.js"
import * as crypto from "node:crypto"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Lock type: exclusive (write) or shared (read)
 */
export type LockType = "exclusive" | "shared"

/**
 * Lock information
 */
export interface Lock {
  readonly id: string
  readonly path: string
  readonly type: LockType
  readonly acquiredAt: Date
  readonly sessionId: string
}

/**
 * Internal lock state tracking
 */
interface LockState {
  readonly exclusiveHolder: string | null
  readonly sharedHolders: Set<string>
  readonly waitQueue: Array<{
    lockId: string
    type: LockType
    deferred: Deferred.Deferred<Lock, LockError>
  }>
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Generic lock operation error
 */
export class LockError extends Data.TaggedError("LockError")<{
  readonly message: string
  readonly path: string
}> {}

/**
 * Lock acquisition timeout error
 */
export class LockTimeoutError extends Data.TaggedError("LockTimeoutError")<{
  readonly path: string
  readonly timeout: Duration.Duration
}> {}

/**
 * Lock conflict error (trying to acquire incompatible lock)
 */
export class LockConflictError extends Data.TaggedError("LockConflictError")<{
  readonly path: string
  readonly requestedType: LockType
  readonly existingType: LockType
  readonly holders: readonly string[]
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * FileLockManager service interface
 *
 * Provides file locking capabilities to prevent concurrent access across
 * multiple Claude sessions. Locks are tracked per normalized file path.
 */
export interface FileLockManagerService {
  /**
   * Acquire a lock on a file path
   *
   * Returns a Lock that must be released via releaseLock.
   * Fails with LockTimeoutError if the lock cannot be acquired within the timeout.
   * Fails with LockConflictError if an incompatible lock is held.
   *
   * Lock semantics:
   * - Exclusive locks: only one holder, blocks all other locks
   * - Shared locks: multiple holders allowed, blocks exclusive locks
   *
   * @example
   * ```ts
   * const manager = yield* FileLockManager
   * const lock = yield* manager.acquireLock({
   *   path: "/path/to/file.ts",
   *   type: "exclusive",
   *   timeout: Duration.seconds(30)
   * })
   * // ... do work ...
   * yield* manager.releaseLock(lock)
   * ```
   */
  readonly acquireLock: (options: {
    path: string
    type: LockType
    timeout?: Duration.Duration
    sessionId?: string
  }) => Effect.Effect<Lock, LockError | LockTimeoutError | LockConflictError>

  /**
   * Release a previously acquired lock
   *
   * Safe to call multiple times (idempotent).
   * Processes the wait queue to grant locks to waiting requests.
   *
   * @example
   * ```ts
   * const manager = yield* FileLockManager
   * yield* manager.releaseLock(lock)
   * ```
   */
  readonly releaseLock: (lock: Lock) => Effect.Effect<void, never>


  /**
   * Get current lock state for a path (for debugging/monitoring)
   *
   * Returns null if no locks exist for the path.
   *
   * @example
   * ```ts
   * const manager = yield* FileLockManager
   * const state = yield* manager.getLockState("/path/to/file.ts")
   * ```
   */
  readonly getLockState: (path: string) => Effect.Effect<{
    readonly exclusiveHolder: string | null
    readonly sharedHolders: readonly string[]
    readonly waitingCount: number
  } | null, never>
}

/**
 * FileLockManager service tag
 */
export class FileLockManager extends Context.Tag("FileLockManager")<
  FileLockManager,
  FileLockManagerService
>() {}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Generate a deterministic hash for a project path
 * Used for creating unique socket paths per project
 */
export function getProjectHash(projectPath: string): string {
  const normalized = normalizePath(projectPath)
  return crypto.createHash("sha256").update(normalized).digest("hex").slice(0, 16)
}

/**
 * Get socket path for file locking
 */
export function getSocketPath(projectPath: string): string {
  const hash = getProjectHash(projectPath)
  return `/tmp/azedarach-${hash}.sock`
}

/**
 * Generate a unique lock ID
 */
const generateLockId = (): string => {
  return crypto.randomUUID()
}

/**
 * Create initial lock state for a path
 */
const createInitialLockState = (): LockState => ({
  exclusiveHolder: null,
  sharedHolders: new Set(),
  waitQueue: [],
})

/**
 * Check if a lock can be granted given current state
 */
const canGrantLock = (state: LockState, type: LockType): boolean => {
  if (type === "exclusive") {
    // Exclusive lock requires no other locks
    return state.exclusiveHolder === null && state.sharedHolders.size === 0
  } else {
    // Shared lock requires no exclusive lock
    return state.exclusiveHolder === null
  }
}

/**
 * Grant a lock by updating state
 */
const grantLock = (state: LockState, lockId: string, type: LockType): LockState => {
  if (type === "exclusive") {
    return {
      ...state,
      exclusiveHolder: lockId,
    }
  } else {
    const newSharedHolders = new Set(state.sharedHolders)
    newSharedHolders.add(lockId)
    return {
      ...state,
      sharedHolders: newSharedHolders,
    }
  }
}

/**
 * Release a lock by updating state
 */
const releaseLockFromState = (state: LockState, lockId: string): LockState => {
  if (state.exclusiveHolder === lockId) {
    return {
      ...state,
      exclusiveHolder: null,
    }
  }

  const newSharedHolders = new Set(state.sharedHolders)
  newSharedHolders.delete(lockId)
  return {
    ...state,
    sharedHolders: newSharedHolders,
  }
}

/**
 * Process wait queue to grant pending locks
 */
const processWaitQueue = (
  locksRef: Ref.Ref<HashMap.HashMap<string, LockState>>,
  path: string
): Effect.Effect<void, never> =>
  Effect.gen(function* () {
    const locks = yield* Ref.get(locksRef)
    const state = HashMap.get(locks, path)

    if (state._tag === "None") {
      return
    }

    const currentState = state.value
    const remainingQueue: typeof currentState.waitQueue = []

    // Try to grant locks from the queue
    for (const waiter of currentState.waitQueue) {
      if (canGrantLock(currentState, waiter.type)) {
        // Grant the lock
        const newState = grantLock(currentState, waiter.lockId, waiter.type)
        yield* Ref.update(locksRef, HashMap.set(path, newState))

        // Resolve the deferred with the lock
        const lock: Lock = {
          id: waiter.lockId,
          path,
          type: waiter.type,
          acquiredAt: new Date(),
          sessionId: waiter.lockId.split("-")[0] || "unknown",
        }
        yield* Deferred.succeed(waiter.deferred, lock)
      } else {
        // Can't grant yet, keep in queue
        remainingQueue.push(waiter)
      }
    }

    // Update the wait queue
    if (remainingQueue.length !== currentState.waitQueue.length) {
      yield* Ref.update(
        locksRef,
        HashMap.modify(path, (s) => ({
          ...s,
          waitQueue: remainingQueue,
        }))
      )
    }
  })

// ============================================================================
// Live Implementation
// ============================================================================

/**
 * Default lock acquisition timeout
 */
const DEFAULT_TIMEOUT = Duration.seconds(30)

/**
 * Live FileLockManager implementation
 *
 * Maintains lock state in-memory using Ref<HashMap>.
 * Automatically cleans up locks on fiber interruption.
 */
const FileLockManagerServiceImpl = Effect.gen(function* () {
  // Track locks per normalized path
  const locksRef = yield* Ref.make<HashMap.HashMap<string, LockState>>(HashMap.empty())

  return FileLockManager.of({
    acquireLock: (options) =>
      Effect.gen(function* () {
        const { path: rawPath, type, timeout = DEFAULT_TIMEOUT, sessionId } = options
        const path = normalizePath(rawPath)
        const lockId = sessionId ? `${sessionId}-${generateLockId()}` : generateLockId()

        // Get or create lock state for this path
        const locks = yield* Ref.get(locksRef)
        const existingState = HashMap.get(locks, path)

        const state =
          existingState._tag === "Some"
            ? existingState.value
            : createInitialLockState()

        // Check if we can grant the lock immediately
        if (canGrantLock(state, type)) {
          const newState = grantLock(state, lockId, type)
          yield* Ref.update(locksRef, HashMap.set(path, newState))

          const lock: Lock = {
            id: lockId,
            path,
            type,
            acquiredAt: new Date(),
            sessionId: sessionId || lockId.split("-")[0] || "unknown",
          }

          return lock
        }

        // Can't grant immediately - add to wait queue with deferred
        const deferred = yield* Deferred.make<Lock, LockError>()

        const waitEntry = {
          lockId,
          type,
          deferred,
        }

        const stateWithWait = {
          ...state,
          waitQueue: [...state.waitQueue, waitEntry],
        }

        yield* Ref.update(locksRef, HashMap.set(path, stateWithWait))

        // Wait for the lock to be granted or timeout
        const lock = yield* Deferred.await(deferred).pipe(
          Effect.timeoutFail({
            duration: timeout,
            onTimeout: () =>
              new LockTimeoutError({
                path,
                timeout,
              }),
          }),
          Effect.ensuring(
            // If we timeout or are interrupted, remove ourselves from wait queue
            Effect.gen(function* () {
              yield* Ref.update(
                locksRef,
                HashMap.modify(path, (s) => ({
                  ...s,
                  waitQueue: s.waitQueue.filter((w) => w.lockId !== lockId),
                }))
              )
            })
          )
        )

        return lock
      }),

    releaseLock: (lock) =>
      Effect.gen(function* () {
        const path = normalizePath(lock.path)
        const locks = yield* Ref.get(locksRef)
        const state = HashMap.get(locks, path)

        if (state._tag === "None") {
          // Lock already released, idempotent
          return
        }

        // Release the lock
        const newState = releaseLockFromState(state.value, lock.id)
        yield* Ref.update(locksRef, HashMap.set(path, newState))

        // Process wait queue to grant pending locks
        yield* processWaitQueue(locksRef, path)

        // Clean up empty lock states
        const updatedLocks = yield* Ref.get(locksRef)
        const updatedState = HashMap.get(updatedLocks, path)

        if (
          updatedState._tag === "Some" &&
          updatedState.value.exclusiveHolder === null &&
          updatedState.value.sharedHolders.size === 0 &&
          updatedState.value.waitQueue.length === 0
        ) {
          yield* Ref.update(locksRef, HashMap.remove(path))
        }
      }),

    getLockState: (rawPath) =>
      Effect.gen(function* () {
        const path = normalizePath(rawPath)
        const locks = yield* Ref.get(locksRef)
        const state = HashMap.get(locks, path)

        if (state._tag === "None") {
          return null
        }

        return {
          exclusiveHolder: state.value.exclusiveHolder,
          sharedHolders: Array.from(state.value.sharedHolders),
          waitingCount: state.value.waitQueue.length,
        }
      }),
  })
})

/**
 * Live FileLockManager layer
 *
 * This layer provides the FileLockManager service with no dependencies.
 * It can be used directly in any Effect program.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const manager = yield* FileLockManager
 *   yield* manager.withLock(
 *     { path: "/path/to/file.ts", type: "exclusive" },
 *     Effect.gen(function* () {
 *       // ... work with file ...
 *     })
 *   )
 * }).pipe(Effect.provide(FileLockManagerLive))
 * ```
 */
export const FileLockManagerLive = Layer.effect(FileLockManager, FileLockManagerServiceImpl)

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Acquire a lock on a file path (convenience function)
 *
 * @example
 * ```ts
 * const lock = yield* acquireLock({
 *   path: "/path/to/file.ts",
 *   type: "exclusive",
 *   timeout: Duration.seconds(10)
 * })
 * ```
 */
export const acquireLock = (options: {
  path: string
  type: LockType
  timeout?: Duration.Duration
  sessionId?: string
}): Effect.Effect<
  Lock,
  LockError | LockTimeoutError | LockConflictError,
  FileLockManager
> => Effect.flatMap(FileLockManager, (manager) => manager.acquireLock(options))

/**
 * Release a lock (convenience function)
 *
 * @example
 * ```ts
 * yield* releaseLock(lock)
 * ```
 */
export const releaseLock = (lock: Lock): Effect.Effect<void, never, FileLockManager> =>
  Effect.flatMap(FileLockManager, (manager) => manager.releaseLock(lock))

/**
 * Execute an effect with a scoped lock (convenience function)
 *
 * Automatically acquires the lock before running the effect and releases
 * it when the effect completes (success or failure).
 *
 * This is the preferred way to use locks as it guarantees cleanup.
 *
 * @example
 * ```ts
 * const result = yield* withLock(
 *   { path: "/path/to/file.ts", type: "exclusive" },
 *   Effect.sync(() => {
 *     // ... do work ...
 *     return value
 *   })
 * )
 * ```
 */
export const withLock = <A, E, R>(
  options: {
    path: string
    type: LockType
    timeout?: Duration.Duration
    sessionId?: string
  },
  effect: Effect.Effect<A, E, R>
): Effect.Effect<A, E | LockError | LockTimeoutError | LockConflictError, R | FileLockManager> =>
  Effect.gen(function* () {
    const manager = yield* FileLockManager
    const lock = yield* manager.acquireLock(options)

    return yield* Effect.ensuring(effect, manager.releaseLock(lock))
  })

/**
 * Get lock state for a path (convenience function)
 *
 * @example
 * ```ts
 * const state = yield* getLockState("/path/to/file.ts")
 * if (state) {
 *   console.log(`Exclusive holder: ${state.exclusiveHolder}`)
 *   console.log(`Shared holders: ${state.sharedHolders.length}`)
 * }
 * ```
 */
export const getLockState = (
  path: string
): Effect.Effect<
  {
    readonly exclusiveHolder: string | null
    readonly sharedHolders: readonly string[]
    readonly waitingCount: number
  } | null,
  never,
  FileLockManager
> => Effect.flatMap(FileLockManager, (manager) => manager.getLockState(path))
