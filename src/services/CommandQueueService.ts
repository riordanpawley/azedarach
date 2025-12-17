/**
 * CommandQueueService - Serializes conflicting operations per task
 *
 * Prevents race conditions when multiple operations target the same task.
 * For example, merge and cleanup both try to delete worktrees - if triggered
 * in rapid succession, they would race. This service queues them FIFO.
 *
 * Key design decisions:
 * - Per-task queuing: Operations on different tasks run in parallel
 * - Queue (not reject): If busy, queue the command instead of error
 * - Timeout protection: Commands don't wait forever
 * - Observable state: UI can show "queued" indicators
 */

import * as crypto from "node:crypto"
import type { CommandExecutor } from "@effect/platform"
import { Data, Deferred, Duration, Effect, HashMap, SubscriptionRef } from "effect"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * A command waiting in the queue
 */
export interface QueuedCommand {
	readonly id: string
	readonly taskId: string
	readonly label: string // e.g. "merge", "cleanup" for display
	readonly queuedAt: Date
}

/**
 * State for a single task's command queue
 */
export interface TaskQueueState {
	readonly running: QueuedCommand | null
	readonly queue: readonly QueuedCommand[]
}

/**
 * Public view of queue state for a task (for UI)
 */
export interface TaskQueueInfo {
	readonly runningLabel: string | null
	readonly queuedCount: number
	readonly queuedLabels: readonly string[]
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Command timed out waiting in queue
 */
export class CommandTimeoutError extends Data.TaggedError("CommandTimeoutError")<{
	readonly taskId: string
	readonly label: string
	readonly timeout: Duration.Duration
}> {}

/**
 * Command was cancelled (e.g., task deleted)
 */
export class CommandCancelledError extends Data.TaggedError("CommandCancelledError")<{
	readonly taskId: string
	readonly label: string
	readonly reason: string
}> {}

// ============================================================================
// Internal Types
// ============================================================================

/**
 * Internal representation with the deferred and effect
 * CommandExecutor is allowed to propagate - it will be satisfied by the runtime
 */
interface InternalQueuedCommand extends QueuedCommand {
	readonly effect: Effect.Effect<void, unknown, CommandExecutor.CommandExecutor>
	readonly deferred: Deferred.Deferred<void, CommandTimeoutError | CommandCancelledError>
}

interface InternalTaskQueueState {
	readonly running: InternalQueuedCommand | null
	readonly queue: InternalQueuedCommand[]
}

// ============================================================================
// Service Implementation
// ============================================================================

const DEFAULT_TIMEOUT = Duration.minutes(5)

const generateCommandId = (): string => crypto.randomUUID()

const createEmptyState = (): InternalTaskQueueState => ({
	running: null,
	queue: [],
})

/**
 * CommandQueueService - Serializes conflicting operations per task
 *
 * Usage:
 * ```ts
 * const queue = yield* CommandQueueService
 * yield* queue.enqueue({
 *   taskId: "az-123",
 *   label: "merge",
 *   effect: doMergeEffect,
 * })
 * ```
 */
export class CommandQueueService extends Effect.Service<CommandQueueService>()(
	"CommandQueueService",
	{
		// Use scoped to get a layer-level Scope for forkScoped fibers
		scoped: Effect.gen(function* () {
			// Capture the service's scope for use in forkScoped
			const serviceScope = yield* Effect.scope

			// Main state: HashMap of taskId -> queue state
			const stateRef = yield* SubscriptionRef.make<HashMap.HashMap<string, InternalTaskQueueState>>(
				HashMap.empty(),
			)

			/**
			 * Process the next command in a task's queue
			 * Called after a command completes or when first command enqueued
			 * CommandExecutor propagates from the queued effects
			 */
			const processNext = (
				taskId: string,
			): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const state = yield* SubscriptionRef.get(stateRef)
					const taskState = HashMap.get(state, taskId)

					if (taskState._tag === "None") return

					const { running, queue } = taskState.value

					// If something is running or queue is empty, nothing to do
					if (running !== null || queue.length === 0) return

					// Pop next command from queue
					const [next, ...rest] = queue
					if (!next) return

					// Mark as running
					yield* SubscriptionRef.update(stateRef, (s) =>
						HashMap.set(s, taskId, {
							running: next,
							queue: rest,
						}),
					)

					// Execute the command in background (don't block processNext)
					// Use forkIn with the service's captured scope so the fiber:
					// 1. Isn't interrupted when processNext returns (outlives parent)
					// 2. Gets cleaned up when the app shuts down (not a daemon)
					yield* Effect.forkIn(
						Effect.gen(function* () {
							// Run the actual effect
							const result = yield* Effect.either(next.effect)

							// Complete the deferred
							if (result._tag === "Right") {
								yield* Deferred.succeed(next.deferred, undefined)
							} else {
								// Command failed - still resolve deferred (caller can handle via the effect)
								yield* Deferred.succeed(next.deferred, undefined)
							}

							// Clear running and process next
							yield* SubscriptionRef.update(stateRef, (s) => {
								const current = HashMap.get(s, taskId)
								if (current._tag === "None") return s

								return HashMap.set(s, taskId, {
									...current.value,
									running: null,
								})
							})

							// Recursively process next
							yield* processNext(taskId)
						}),
						serviceScope,
					)
				})

			return {
				/**
				 * Observable state for UI subscription
				 */
				state: stateRef,

				/**
				 * Enqueue a command for a task
				 *
				 * Returns when the command completes (after waiting in queue if needed).
				 * The effect is run with all errors caught - caller should handle errors
				 * within the effect itself (e.g., show toast).
				 *
				 * CommandExecutor is allowed to propagate - it will be satisfied by the runtime.
				 */
				enqueue: (options: {
					taskId: string
					label: string
					effect: Effect.Effect<void, unknown, CommandExecutor.CommandExecutor>
					timeout?: Duration.Duration
				}): Effect.Effect<
					void,
					CommandTimeoutError | CommandCancelledError,
					CommandExecutor.CommandExecutor
				> =>
					Effect.gen(function* () {
						const { taskId, label, effect, timeout = DEFAULT_TIMEOUT } = options
						const id = generateCommandId()
						const deferred = yield* Deferred.make<
							void,
							CommandTimeoutError | CommandCancelledError
						>()

						const command: InternalQueuedCommand = {
							id,
							taskId,
							label,
							queuedAt: new Date(),
							effect,
							deferred,
						}

						// Add to queue
						yield* SubscriptionRef.update(stateRef, (state) => {
							const existing = HashMap.get(state, taskId)
							const taskState = existing._tag === "Some" ? existing.value : createEmptyState()

							return HashMap.set(state, taskId, {
								...taskState,
								queue: [...taskState.queue, command],
							})
						})

						// Trigger processing (will start immediately if nothing running)
						yield* processNext(taskId)

						// Wait for completion with timeout
						yield* Deferred.await(deferred).pipe(
							Effect.timeoutFail({
								duration: timeout,
								onTimeout: () =>
									new CommandTimeoutError({
										taskId,
										label,
										timeout,
									}),
							}),
							// On timeout, remove from queue
							Effect.onError(() =>
								SubscriptionRef.update(stateRef, (state) => {
									const existing = HashMap.get(state, taskId)
									if (existing._tag === "None") return state

									return HashMap.set(state, taskId, {
										...existing.value,
										queue: existing.value.queue.filter((c) => c.id !== id),
									})
								}),
							),
						)
					}),

				/**
				 * Get queue info for a specific task
				 */
				getQueueInfo: (taskId: string): Effect.Effect<TaskQueueInfo, never, never> =>
					Effect.gen(function* () {
						const state = yield* SubscriptionRef.get(stateRef)
						const taskState = HashMap.get(state, taskId)

						if (taskState._tag === "None") {
							return {
								runningLabel: null,
								queuedCount: 0,
								queuedLabels: [],
							}
						}

						const { running, queue } = taskState.value
						return {
							runningLabel: running?.label ?? null,
							queuedCount: queue.length,
							queuedLabels: queue.map((c) => c.label),
						}
					}),

				/**
				 * Cancel all queued commands for a task
				 * (e.g., when task is deleted)
				 */
				cancelAll: (taskId: string, reason: string): Effect.Effect<void, never, never> =>
					Effect.gen(function* () {
						const state = yield* SubscriptionRef.get(stateRef)
						const taskState = HashMap.get(state, taskId)

						if (taskState._tag === "None") return

						// Fail all queued command deferreds
						for (const cmd of taskState.value.queue) {
							yield* Deferred.fail(
								cmd.deferred,
								new CommandCancelledError({
									taskId,
									label: cmd.label,
									reason,
								}),
							)
						}

						// Clear the queue (running command will complete naturally)
						yield* SubscriptionRef.update(stateRef, (s) =>
							HashMap.set(s, taskId, {
								running: taskState.value.running,
								queue: [],
							}),
						)
					}),

				/**
				 * Check if a task has any commands running or queued
				 */
				isBusy: (taskId: string): Effect.Effect<boolean, never, never> =>
					Effect.gen(function* () {
						const state = yield* SubscriptionRef.get(stateRef)
						const taskState = HashMap.get(state, taskId)

						if (taskState._tag === "None") return false

						return taskState.value.running !== null || taskState.value.queue.length > 0
					}),
			}
		}),
	},
) {}

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Enqueue a command (convenience function)
 * CommandExecutor is allowed to propagate - it will be satisfied by the runtime.
 */
export const enqueueCommand = (options: {
	taskId: string
	label: string
	effect: Effect.Effect<void, unknown, CommandExecutor.CommandExecutor>
	timeout?: Duration.Duration
}): Effect.Effect<
	void,
	CommandTimeoutError | CommandCancelledError,
	CommandQueueService | CommandExecutor.CommandExecutor
> => Effect.flatMap(CommandQueueService, (queue) => queue.enqueue(options))

/**
 * Get queue info for a task (convenience function)
 */
export const getQueueInfo = (
	taskId: string,
): Effect.Effect<TaskQueueInfo, never, CommandQueueService> =>
	Effect.flatMap(CommandQueueService, (queue) => queue.getQueueInfo(taskId))
