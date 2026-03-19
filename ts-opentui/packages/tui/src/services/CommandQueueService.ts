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

import { DaemonRpcClient, type DaemonRpcClientApi } from "@azedarach/shared/rpc"
import type { CommandExecutor } from "@effect/platform"
import {
	Cause,
	Data,
	DateTime,
	Deferred,
	Duration,
	Effect,
	Exit,
	HashMap,
	Option,
	SubscriptionRef,
} from "effect"
import { buildTaskQueueKey } from "../utils/queueKey.js"

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
	readonly queuedAt: DateTime.Utc
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
 * Command timed out waiting in queue or running
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
	readonly timeout: Duration.Duration
	readonly startedAt: DateTime.Utc | null
}

interface InternalTaskQueueState {
	readonly running: InternalQueuedCommand | null
	readonly queue: InternalQueuedCommand[]
}

interface QueueScope {
	readonly taskId: string
	readonly queueKey: string
	readonly projectPath?: string
}

interface DaemonQueueRpc {
	readonly queueEnqueue: NonNullable<DaemonRpcClientApi["queueEnqueue"]>
	readonly queueQuery: NonNullable<DaemonRpcClientApi["queueQuery"]>
	readonly queueCancel: NonNullable<DaemonRpcClientApi["queueCancel"]>
}

const isCommandTimeoutError = (error: unknown): error is CommandTimeoutError =>
	typeof error === "object" &&
	error !== null &&
	"_tag" in error &&
	error._tag === "CommandTimeoutError"

// ============================================================================
// Service Implementation
// ============================================================================

const DEFAULT_TIMEOUT = Duration.minutes(5)
const STALE_COMMAND_GRACE = Duration.seconds(10)

const generateCommandId = Effect.sync(() => crypto.randomUUID())

const createEmptyState = (): InternalTaskQueueState => ({
	running: null,
	queue: [],
})

const QUEUE_KEY_SEPARATOR = "::"

const resolveQueueScope = (taskId: string, queueKey?: string): QueueScope => {
	const effectiveQueueKey = queueKey ?? taskId
	const delimiterIndex = effectiveQueueKey.lastIndexOf(QUEUE_KEY_SEPARATOR)
	if (delimiterIndex <= 0) {
		return {
			taskId,
			queueKey: effectiveQueueKey,
		}
	}

	const scopedTaskId = effectiveQueueKey.slice(delimiterIndex + QUEUE_KEY_SEPARATOR.length)
	if (scopedTaskId !== taskId) {
		return {
			taskId,
			queueKey: effectiveQueueKey,
		}
	}

	const scopedProjectPath = effectiveQueueKey.slice(0, delimiterIndex)
	if (scopedProjectPath.trim().length === 0) {
		return {
			taskId,
			queueKey: effectiveQueueKey,
		}
	}

	return {
		taskId,
		queueKey: effectiveQueueKey,
		projectPath: scopedProjectPath,
	}
}

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
			const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)

			// Main state: HashMap of taskId -> queue state
			const stateRef = yield* SubscriptionRef.make<HashMap.HashMap<string, InternalTaskQueueState>>(
				HashMap.empty(),
			)

			const getDaemonQueueRpc = (): Option.Option<DaemonQueueRpc> => {
				if (Option.isNone(daemonRpcClient)) {
					return Option.none()
				}

				const queueEnqueue = daemonRpcClient.value.queueEnqueue
				const queueQuery = daemonRpcClient.value.queueQuery
				const queueCancel = daemonRpcClient.value.queueCancel
				if (queueEnqueue === undefined || queueQuery === undefined || queueCancel === undefined) {
					return Option.none()
				}

				return Option.some({
					queueEnqueue,
					queueQuery,
					queueCancel,
				})
			}

			const buildDaemonQueryRequest = (
				scope: QueueScope,
				projectPath: string,
			): {
				readonly domain: "command"
				readonly issueId: string
				readonly projectPath: string
			} => ({
				domain: "command",
				issueId: scope.taskId,
				projectPath,
			})

			const buildDaemonEnqueueRequest = (
				scope: QueueScope,
				label: string,
				projectPath: string,
			): {
				readonly domain: "command"
				readonly operation: string
				readonly issueId: string
				readonly dedupeKey: string
				readonly projectPath: string
			} => ({
				domain: "command",
				operation: label,
				issueId: scope.taskId,
				dedupeKey: scope.queueKey,
				projectPath,
			})

			const resolveDaemonProjectPath = (
				projectPath: string | undefined,
			): Effect.Effect<string, never, never> =>
				projectPath === undefined
					? Effect.succeed(globalThis.process.cwd())
					: Effect.succeed(projectPath)

			const toTaskQueueInfoFromDaemonItems = (
				items: ReadonlyArray<{
					readonly operation: string
					readonly state: "queued" | "running" | "done" | "failed" | "cancelled"
				}>,
			): TaskQueueInfo => {
				const runningItems = items.filter((item) => item.state === "running")
				const queuedItems = items.filter((item) => item.state === "queued")

				return {
					runningLabel: runningItems[0]?.operation ?? null,
					queuedCount: queuedItems.length,
					queuedLabels: queuedItems.map((item) => item.operation),
				}
			}

			const getQueueInfoFromLocal = (
				taskId: string,
				queueKey?: string,
			): Effect.Effect<TaskQueueInfo, never, never> =>
				Effect.gen(function* () {
					const effectiveQueueKey = queueKey ?? taskId
					const state = yield* SubscriptionRef.get(stateRef)
					const taskState = HashMap.get(state, effectiveQueueKey)

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
				})

			const getQueueInfoAdapter = (
				taskId: string,
				queueKey?: string,
			): Effect.Effect<TaskQueueInfo, never, never> =>
				Effect.gen(function* () {
					const scope = resolveQueueScope(taskId, queueKey)
					const daemonProjectPath = yield* resolveDaemonProjectPath(scope.projectPath)
					const daemonQueueRpc = getDaemonQueueRpc()
					if (Option.isSome(daemonQueueRpc)) {
						return yield* daemonQueueRpc.value
							.queueQuery(buildDaemonQueryRequest(scope, daemonProjectPath))
							.pipe(
								Effect.map((result) => toTaskQueueInfoFromDaemonItems(result.items)),
								Effect.catchAll((error) =>
									Effect.logWarning(
										"Daemon queue query failed; falling back to local queue state",
										{
											taskId,
											error,
										},
									).pipe(Effect.zipRight(getQueueInfoFromLocal(taskId, scope.queueKey))),
								),
							)
					}

					return yield* getQueueInfoFromLocal(taskId, scope.queueKey)
				})

			/**
			 * Process the next command in a task's queue
			 * Called after a command completes or when first command enqueued
			 * CommandExecutor propagates from the queued effects
			 */
			const processNext = (
				queueKey: string,
			): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const state = yield* SubscriptionRef.get(stateRef)
					const taskState = HashMap.get(state, queueKey)

					if (taskState._tag === "None") return

					const { running, queue } = taskState.value

					// If something is running or queue is empty, nothing to do
					if (running !== null || queue.length === 0) return

					// Pop next command from queue
					const [next, ...rest] = queue
					if (!next) return

					const startedAt = yield* DateTime.now
					const runningCommand: InternalQueuedCommand = {
						...next,
						startedAt,
					}

					// Mark as running
					yield* SubscriptionRef.update(stateRef, (s) =>
						HashMap.set(s, queueKey, {
							running: runningCommand,
							queue: rest,
						}),
					)

					// Execute the command in background (don't block processNext)
					// Use forkIn with the service's captured scope so the fiber:
					// 1. Isn't interrupted when processNext returns (outlives parent)
					// 2. Gets cleaned up when the app shuts down (not a daemon)
					yield* Effect.forkIn(
						Effect.gen(function* () {
							// Run the actual effect with Effect.exit to capture ALL outcomes
							// Effect.either only catches expected failures, NOT defects (thrown exceptions)
							// Effect.exit captures: success, failure, AND defects
							const result = yield* Effect.exit(
								runningCommand.effect.pipe(
									Effect.timeoutFail({
										duration: runningCommand.timeout,
										onTimeout: () =>
											new CommandTimeoutError({
												taskId: next.taskId,
												label: runningCommand.label,
												timeout: runningCommand.timeout,
											}),
									}),
								),
							)

							const timeoutError = Exit.isFailure(result)
								? Cause.failureOption(result.cause).pipe(Option.filter(isCommandTimeoutError))
								: Option.none<CommandTimeoutError>()

							if (Option.isSome(timeoutError)) {
								yield* Effect.logWarning("Command timed out", {
									taskId: next.taskId,
									label: runningCommand.label,
									timeout: runningCommand.timeout,
								})
							}

							// Log defects for debugging - these would otherwise silently crash the fiber
							if (Exit.isFailure(result) && Cause.isDie(result.cause)) {
								yield* Effect.logError("Command failed with defect (unexpected error)", {
									taskId: next.taskId,
									label: runningCommand.label,
									cause: Cause.pretty(result.cause),
								})
							}

							// Complete the deferred.
							// Timeout errors propagate to caller so handlers can show a clear message.
							if (Option.isSome(timeoutError)) {
								yield* Deferred.fail(runningCommand.deferred, timeoutError.value)
							} else {
								// Caller handles operation errors within their effect via catchAll
								yield* Deferred.succeed(runningCommand.deferred, undefined)
							}

							// Clear running and process next
							yield* SubscriptionRef.update(stateRef, (s) => {
								const current = HashMap.get(s, queueKey)
								if (current._tag === "None") return s

								return HashMap.set(s, queueKey, {
									...current.value,
									running: null,
								})
							})

							// Recursively process next
							yield* processNext(queueKey)
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
					queueKey?: string
				}): Effect.Effect<
					void,
					CommandTimeoutError | CommandCancelledError,
					CommandExecutor.CommandExecutor
				> =>
					Effect.gen(function* () {
						const { taskId, label, effect, timeout = DEFAULT_TIMEOUT } = options
						const scope = resolveQueueScope(taskId, options.queueKey)
						const queueKey = scope.queueKey
						const daemonProjectPath = yield* resolveDaemonProjectPath(scope.projectPath)

						const daemonQueueRpc = getDaemonQueueRpc()
						const delegatedToDaemon = yield* Option.match(daemonQueueRpc, {
							onNone: () => Effect.succeed(false),
							onSome: (rpc) =>
								rpc.queueEnqueue(buildDaemonEnqueueRequest(scope, label, daemonProjectPath)).pipe(
									Effect.as(true),
									Effect.catchAll((error) =>
										Effect.logWarning("Daemon queue enqueue failed; falling back to local queue", {
											taskId,
											label,
											error,
										}).pipe(Effect.zipRight(Effect.succeed(false))),
									),
								),
						})
						if (delegatedToDaemon) {
							return
						}

						const id = yield* generateCommandId
						const queuedAt = yield* DateTime.now
						const deferred = yield* Deferred.make<
							void,
							CommandTimeoutError | CommandCancelledError
						>()

						const command: InternalQueuedCommand = {
							id,
							taskId,
							label,
							queuedAt,
							effect,
							deferred,
							timeout,
							startedAt: null,
						}

						// Add to queue
						yield* SubscriptionRef.update(stateRef, (state) => {
							const existing = HashMap.get(state, queueKey)
							const taskState = existing._tag === "Some" ? existing.value : createEmptyState()

							return HashMap.set(state, queueKey, {
								...taskState,
								queue: [...taskState.queue, command],
							})
						})

						// Trigger processing (will start immediately if nothing running)
						yield* processNext(queueKey)

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
									const existing = HashMap.get(state, queueKey)
									if (existing._tag === "None") return state

									return HashMap.set(state, queueKey, {
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
				getQueueInfo: (
					taskId: string,
					queueKey?: string,
				): Effect.Effect<TaskQueueInfo, never, never> => getQueueInfoAdapter(taskId, queueKey),

				/**
				 * Recover a stale running command that has exceeded timeout + grace period.
				 *
				 * This is a safety valve for commands that finish side effects but never clear
				 * queue state due an interruption edge case.
				 *
				 * Returns true when recovery was performed, false otherwise.
				 */
				recoverStaleRunning: (
					taskId: string,
					options?: { readonly grace?: Duration.DurationInput },
				): Effect.Effect<boolean, never, never> =>
					Effect.gen(function* () {
						const state = yield* SubscriptionRef.get(stateRef)
						const taskState = HashMap.get(state, taskId)
						if (taskState._tag === "None") return false

						const running = taskState.value.running
						if (running === null) return false

						const now = yield* DateTime.now
						const startedAt = running.startedAt ?? running.queuedAt
						const elapsedMs = DateTime.distance(startedAt, now)
						const timeoutMs = Duration.toMillis(running.timeout)
						const staleGrace = options?.grace ?? STALE_COMMAND_GRACE
						const staleThresholdMs = timeoutMs + Duration.toMillis(staleGrace)
						if (elapsedMs <= staleThresholdMs) return false

						const timeoutError = new CommandTimeoutError({
							taskId,
							label: running.label,
							timeout: running.timeout,
						})

						yield* Deferred.fail(running.deferred, timeoutError)

						yield* SubscriptionRef.update(stateRef, (s) => {
							const current = HashMap.get(s, taskId)
							if (current._tag === "None") return s
							return HashMap.set(s, taskId, {
								...current.value,
								running: null,
							})
						})

						yield* Effect.logWarning("Recovered stale running command", {
							taskId,
							label: running.label,
							elapsedMs,
							timeoutMs,
						})

						return true
					}),

				/**
				 * Cancel all queued commands for a task
				 * (e.g., when task is deleted)
				 */
				cancelAll: (
					taskId: string,
					reason: string,
					queueKey?: string,
				): Effect.Effect<void, never, never> =>
					Effect.gen(function* () {
						const scope = resolveQueueScope(taskId, queueKey)
						const daemonProjectPath = yield* resolveDaemonProjectPath(scope.projectPath)
						const daemonQueueRpc = getDaemonQueueRpc()
						const cancelledViaDaemon = yield* Option.match(daemonQueueRpc, {
							onNone: () => Effect.succeed(false),
							onSome: (rpc) =>
								rpc.queueCancel(buildDaemonQueryRequest(scope, daemonProjectPath)).pipe(
									Effect.as(true),
									Effect.catchAll((error) =>
										Effect.logWarning("Daemon queue cancel failed; falling back to local queue", {
											taskId,
											reason,
											error,
										}).pipe(Effect.zipRight(Effect.succeed(false))),
									),
								),
						})
						if (cancelledViaDaemon) {
							return
						}

						const state = yield* SubscriptionRef.get(stateRef)
						const taskState = HashMap.get(state, scope.queueKey)

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
							HashMap.set(s, scope.queueKey, {
								running: taskState.value.running,
								queue: [],
							}),
						)
					}),

				/**
				 * Check if a task has any commands running or queued
				 */
				isBusy: (taskId: string, queueKey?: string): Effect.Effect<boolean, never, never> =>
					Effect.gen(function* () {
						const queueInfo = yield* getQueueInfoAdapter(taskId, queueKey)
						return queueInfo.runningLabel !== null || queueInfo.queuedCount > 0
					}),

				/**
				 * Check if ANY task has commands running or queued
				 * Used to prevent app quit while operations are in progress
				 */
				isAnyBusy: (): Effect.Effect<boolean, never, never> =>
					Effect.gen(function* () {
						const getLocalBusyState = (): Effect.Effect<boolean, never, never> =>
							Effect.gen(function* () {
								const state = yield* SubscriptionRef.get(stateRef)
								return HashMap.reduce(
									state,
									false,
									(acc, taskState) =>
										acc || taskState.running !== null || taskState.queue.length > 0,
								)
							})

						const daemonQueueRpc = getDaemonQueueRpc()
						if (Option.isSome(daemonQueueRpc)) {
							const daemonProjectPath = yield* resolveDaemonProjectPath(undefined)
							return yield* daemonQueueRpc.value
								.queueQuery({
									domain: "command",
									limit: 1,
									projectPath: daemonProjectPath,
								})
								.pipe(
									Effect.map((result) =>
										result.items.some(
											(item) => item.state === "queued" || item.state === "running",
										),
									),
									Effect.catchAll((error) =>
										Effect.logWarning("Daemon queue query failed; using local queue busy state", {
											error,
										}).pipe(Effect.zipRight(getLocalBusyState())),
									),
								)
						}

						return yield* getLocalBusyState()
					}),

				/**
				 * Get labels of all currently running operations
				 * Used to show what's blocking app quit
				 */
				getRunningOperationLabels: (): Effect.Effect<readonly string[], never, never> =>
					Effect.gen(function* () {
						const getLocalRunningLabels = (): Effect.Effect<readonly string[], never, never> =>
							Effect.gen(function* () {
								const state = yield* SubscriptionRef.get(stateRef)
								return HashMap.reduce(state, [] as readonly string[], (acc, taskState) =>
									taskState.running !== null ? [...acc, taskState.running.label] : acc,
								)
							})

						const daemonQueueRpc = getDaemonQueueRpc()
						if (Option.isSome(daemonQueueRpc)) {
							const daemonProjectPath = yield* resolveDaemonProjectPath(undefined)
							return yield* daemonQueueRpc.value
								.queueQuery({
									domain: "command",
									projectPath: daemonProjectPath,
								})
								.pipe(
									Effect.map((result) => {
										const runningLabels = result.items
											.filter((item) => item.state === "running")
											.map((item) => item.operation)
										return [...new Set(runningLabels)]
									}),
									Effect.catchAll((error) =>
										Effect.logWarning("Daemon queue query failed; using local running labels", {
											error,
										}).pipe(Effect.zipRight(getLocalRunningLabels())),
									),
								)
						}

						return yield* getLocalRunningLabels()
					}),

				getTaskQueueInfo: (
					taskId: string,
					projectPath?: string,
				): Effect.Effect<TaskQueueInfo, never, never> =>
					getQueueInfoAdapter(taskId, buildTaskQueueKey(taskId, projectPath)),

				isTaskBusy: (taskId: string, projectPath?: string): Effect.Effect<boolean, never, never> =>
					Effect.gen(function* () {
						const queueInfo = yield* getQueueInfoAdapter(
							taskId,
							buildTaskQueueKey(taskId, projectPath),
						)
						return queueInfo.runningLabel !== null || queueInfo.queuedCount > 0
					}),
			}
		}),
	},
) {}
