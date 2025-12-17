/**
 * Keyboard Handler Helpers
 *
 * Shared utility functions used by multiple handler modules.
 * These are pure Effect-returning functions that operate on the HandlerContext.
 */

import type { CommandExecutor } from "@effect/platform"
import { Effect } from "effect"
import type { TaskWithSession } from "../../ui/types"
import type { BoardService } from "../BoardService"
import type { CommandQueueService } from "../CommandQueueService"
import type { EditorService } from "../EditorService"
import { formatForToast } from "../ErrorFormatter"
import type { NavigationService } from "../NavigationService"
import type { OverlayService } from "../OverlayService"
import type { ToastService } from "../ToastService"

// ============================================================================
// Helper Factory Functions
// ============================================================================

/**
 * Create a function that gets the currently selected task by ID
 *
 * NavigationService stores the focused task ID directly,
 * so we just look it up from the task list.
 */
export const createGetSelectedTask =
	(nav: NavigationService, board: BoardService) => (): Effect.Effect<TaskWithSession | undefined> =>
		Effect.gen(function* () {
			const taskId = yield* nav.getFocusedTaskId()
			if (!taskId) return undefined

			const allTasks = yield* board.getTasks()
			return allTasks.find((t) => t.id === taskId)
		})

/**
 * Create a function that gets the current cursor column index
 */
export const createGetColumnIndex = (nav: NavigationService) => (): Effect.Effect<number> =>
	Effect.gen(function* () {
		const position = yield* nav.getPosition()
		return position.columnIndex
	})

/**
 * Create a function that shows an error toast with consistent formatting
 * Uses ErrorFormatter for user-friendly messages and suggestions
 * Also logs the full error via Effect.logError for debugging
 *
 * @param toast - ToastService instance
 */
export const createShowErrorToast =
	(toast: ToastService) =>
	(prefix: string) =>
	(error: unknown): Effect.Effect<void> =>
		Effect.gen(function* () {
			const formatted = formatForToast(error)
			yield* Effect.logError(`${prefix}: ${formatted}`, { error })
			yield* toast.show("error", `${prefix}: ${formatted}`)
		})

/**
 * Create a function that opens the detail overlay for the current cursor position
 */
export const createOpenCurrentDetail =
	(overlay: OverlayService, getSelectedTask: () => Effect.Effect<TaskWithSession | undefined>) =>
	(): Effect.Effect<void> =>
		Effect.gen(function* () {
			const task = yield* getSelectedTask()
			if (task) {
				yield* overlay.push({ _tag: "detail", taskId: task.id })
			}
		})

/**
 * Create a function that toggles selection for the task at current cursor position
 */
export const createToggleCurrentSelection =
	(editor: EditorService, getSelectedTask: () => Effect.Effect<TaskWithSession | undefined>) =>
	(): Effect.Effect<void> =>
		Effect.gen(function* () {
			const task = yield* getSelectedTask()
			if (task) {
				yield* editor.toggleSelection(task.id)
			}
		})

/**
 * Create a function that executes a queued operation for a task
 *
 * Operations like merge, cleanup, start session, etc. can conflict if run
 * simultaneously on the same task. This helper ensures they run one at a time.
 *
 * CommandExecutor is allowed to propagate - it will be satisfied by the runtime.
 *
 * @param commandQueue - CommandQueueService instance
 * @param toast - ToastService instance for error display
 */
export const createWithQueue =
	(commandQueue: CommandQueueService, toast: ToastService) =>
	<A, E>(
		taskId: string,
		label: string,
		operation: Effect.Effect<A, E, CommandExecutor.CommandExecutor>,
	) =>
		commandQueue
			.enqueue({
				taskId,
				label,
				// CommandExecutor propagates to runtime - no provide needed
				effect: Effect.asVoid(operation),
			})
			.pipe(
				// Queue errors (timeout, cancelled) are handled separately
				Effect.catchAll((error) =>
					toast
						.show("error", `${label} timed out or was cancelled: ${error._tag}`)
						.pipe(Effect.asVoid),
				),
			)

/**
 * Create a function that checks if a task has an operation in progress.
 *
 * Returns true if busy (caller should abort), false if idle (caller can proceed).
 * When busy, shows a toast with the running operation label.
 *
 * @param commandQueue - CommandQueueService instance
 * @param toast - ToastService instance for notifications
 */
export const createCheckBusy =
	(commandQueue: CommandQueueService, toast: ToastService) =>
	(taskId: string): Effect.Effect<boolean> =>
		Effect.gen(function* () {
			const queueInfo = yield* commandQueue.getQueueInfo(taskId)

			if (queueInfo.runningLabel !== null) {
				yield* toast.show("error", `${taskId} is busy (${queueInfo.runningLabel} in progress)`)
				return true
			}

			return false
		})
