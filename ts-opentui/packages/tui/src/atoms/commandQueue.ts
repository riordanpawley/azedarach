/**
 * Command Queue Atoms
 *
 * Handles action busy state tracking for tasks.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect, HashMap } from "effect"
import {
	buildTaskQueueKey,
	CommandQueueService,
} from "../../../../src/services/CommandQueueService.js"
import { ProjectService } from "../../../../src/services/ProjectService.js"
import { focusedTaskIdAtom } from "./navigation.js"
import { currentProjectAtom } from "./project.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Command Queue Atoms
// ============================================================================

/**
 * Command queue state atom - subscribes to CommandQueueService state changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 * Returns a HashMap of taskId -> TaskQueueState.
 *
 * Usage: const queueState = useAtomValue(commandQueueStateAtom)
 */
export const commandQueueStateAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const queue = yield* CommandQueueService
		return queue.state
	}),
)

/**
 * Running operation for focused task - derives from NavigationService and CommandQueueService
 *
 * Returns the running operation label (e.g., "merge", "cleanup") for the currently
 * focused task, or null if no operation is running.
 *
 * Usage: const runningOp = useAtomValue(focusedTaskRunningOperationAtom)
 */
export const focusedTaskRunningOperationAtom = Atom.readable((get) => {
	// Get the focused task ID from NavigationService
	const focusedIdResult = get(focusedTaskIdAtom)
	if (!Result.isSuccess(focusedIdResult)) return null
	const taskId = focusedIdResult.value
	if (!taskId) return null

	const currentProjectResult = get(currentProjectAtom)
	const projectPath = Result.isSuccess(currentProjectResult)
		? (currentProjectResult.value?.path ?? undefined)
		: undefined
	const queueKey = buildTaskQueueKey(taskId, projectPath)

	// Get the queue state
	const stateResult = get(commandQueueStateAtom)
	if (!Result.isSuccess(stateResult)) return null

	// Look up the running operation for this task
	const taskState = HashMap.get(stateResult.value, queueKey)
	if (taskState._tag === "None") return null

	return taskState.value.running?.label ?? null
})

/**
 * Running operation for a specific task - parameterized atom factory
 *
 * Returns the running operation label (e.g., "merge", "cleanup") for the given task,
 * or null if no operation is running.
 *
 * Used by TaskCard to show operation indicators on individual cards.
 *
 * Usage: const runningOp = useAtomValue(taskRunningOperationAtom(taskId))
 */
export const taskRunningOperationAtom = (taskId: string) =>
	Atom.readable((get) => {
		const currentProjectResult = get(currentProjectAtom)
		const projectPath = Result.isSuccess(currentProjectResult)
			? (currentProjectResult.value?.path ?? undefined)
			: undefined
		const queueKey = buildTaskQueueKey(taskId, projectPath)

		// Get the queue state
		const stateResult = get(commandQueueStateAtom)
		if (!Result.isSuccess(stateResult)) return null

		// Look up the running operation for this task
		const taskState = HashMap.get(stateResult.value, queueKey)
		if (taskState._tag === "None") return null

		return taskState.value.running?.label ?? null
	})

/**
 * Get queue info for a specific task
 *
 * Returns the running operation label (if any) and queued operation count.
 * Used by ActionPalette to show busy state and disable actions.
 *
 * Usage: const queueInfo = await getQueueInfo(taskId)
 */
export const getQueueInfoAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const queue = yield* CommandQueueService
		const projectService = yield* ProjectService
		const projectPath = yield* projectService.getCurrentPath()
		return yield* queue.getTaskQueueInfo(taskId, projectPath)
	}),
)

/**
 * Check if a task has any operations running or queued
 *
 * Usage: const isBusy = await checkTaskBusy(taskId)
 */
export const checkTaskBusyAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const queue = yield* CommandQueueService
		const projectService = yield* ProjectService
		const projectPath = yield* projectService.getCurrentPath()
		return yield* queue.isTaskBusy(taskId, projectPath)
	}),
)
