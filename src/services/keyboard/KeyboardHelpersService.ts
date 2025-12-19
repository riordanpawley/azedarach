/**
 * KeyboardHelpersService
 *
 * Provides shared utility functions used by keyboard handler services.
 * Converted from factory pattern to Effect.Service layer.
 *
 * Dependencies are resolved at layer construction time via yield*,
 * following the Effect.Service pattern.
 */

import type { CommandExecutor } from "@effect/platform"
import { Effect } from "effect"
import type { TaskWithSession } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { CommandQueueService } from "../CommandQueueService.js"
import { EditorService } from "../EditorService.js"
import { formatForToast } from "../ErrorFormatter.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ProjectService } from "../ProjectService.js"
import { ToastService } from "../ToastService.js"

// ============================================================================
// Service Definition
// ============================================================================

export class KeyboardHelpersService extends Effect.Service<KeyboardHelpersService>()(
	"KeyboardHelpersService",
	{
		dependencies: [
			ToastService.Default,
			NavigationService.Default,
			BoardService.Default,
			EditorService.Default,
			OverlayService.Default,
			CommandQueueService.Default,
			ProjectService.Default,
		],

		effect: Effect.gen(function* () {
			// Inject services at construction time
			const toast = yield* ToastService
			const nav = yield* NavigationService
			const board = yield* BoardService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const commandQueue = yield* CommandQueueService
			const projectService = yield* ProjectService

			// ================================================================
			// Helper Methods (bound to injected services)
			// ================================================================

			/**
			 * Get the task currently at cursor position
			 * Resolves: cursor position → task ID → task object
			 */
			const getSelectedTask = (): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					const taskId = yield* nav.getFocusedTaskId()
					if (!taskId) return undefined

					const allTasks = yield* board.getTasks()
					return allTasks.find((t) => t.id === taskId)
				})

			/**
			 * Get current cursor column index (0-3)
			 */
			const getColumnIndex = (): Effect.Effect<number> =>
				Effect.gen(function* () {
					const position = yield* nav.getPosition()
					return position.columnIndex
				})

			/**
			 * Get current project path (from ProjectService or cwd fallback)
			 */
			const getProjectPath = (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const path = yield* projectService.getCurrentPath()
					return path ?? process.cwd()
				})

			/**
			 * Show error toast with consistent formatting
			 * Uses ErrorFormatter for user-friendly messages and suggestions
			 * Also logs the full error via Effect.logError for debugging
			 *
			 * @param prefix - Error message prefix (e.g., "Failed to start")
			 */
			const showErrorToast =
				(prefix: string) =>
				(error: unknown): Effect.Effect<void> =>
					Effect.gen(function* () {
						const formatted = formatForToast(error)
						yield* Effect.logError(`${prefix}: ${formatted}`, { error })
						yield* toast.show("error", `${prefix}: ${formatted}`)
					})

			/**
			 * Open detail overlay for current cursor position
			 */
			const openCurrentDetail = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getSelectedTask()
					if (task) {
						yield* overlay.push({ _tag: "detail", taskId: task.id })
					}
				})

			/**
			 * Toggle selection for task at current cursor position
			 */
			const toggleCurrentSelection = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getSelectedTask()
					if (task) {
						yield* editor.toggleSelection(task.id)
					}
				})

			/**
			 * Execute a queued operation for a task
			 *
			 * Operations like merge, cleanup, start session, etc. can conflict if run
			 * simultaneously on the same task. This helper ensures they run one at a time.
			 *
			 * CommandExecutor is allowed to propagate - it will be satisfied by the runtime.
			 *
			 * @param taskId - The task to operate on
			 * @param label - Human-readable label for the operation (e.g., "merge", "cleanup")
			 * @param operation - The Effect to execute (return value is ignored)
			 */
			const withQueue = <A, E>(
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
			 * Check if a task has an operation in progress and show a toast if so.
			 *
			 * Returns true if busy (caller should abort), false if idle (caller can proceed).
			 * When busy, shows a toast like "az-123 is busy (merge in progress)"
			 *
			 * @param taskId - The task to check
			 * @returns Effect that resolves to true if busy, false if idle
			 */
			const checkBusy = (taskId: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const queueInfo = yield* commandQueue.getQueueInfo(taskId)

					if (queueInfo.runningLabel !== null) {
						yield* toast.show("error", `${taskId} is busy (${queueInfo.runningLabel} in progress)`)
						return true
					}

					return false
				})

			/**
			 * Check if ANY task has operations running or queued.
			 * Used to prevent app quit while operations are in progress.
			 *
			 * @returns Effect that resolves to true if any task is busy
			 */
			const isAnyBusy = (): Effect.Effect<boolean, never, never> => commandQueue.isAnyBusy()

			/**
			 * Get labels of all currently running operations.
			 * Used to show what's blocking app quit.
			 *
			 * @returns Effect that resolves to array of operation labels
			 */
			const getRunningOperationLabels = (): Effect.Effect<readonly string[], never, never> =>
				commandQueue.getRunningOperationLabels()

			// ================================================================
			// Public API
			// ================================================================

			return {
				getSelectedTask,
				getColumnIndex,
				getProjectPath,
				showErrorToast,
				openCurrentDetail,
				toggleCurrentSelection,
				withQueue,
				checkBusy,
				isAnyBusy,
				getRunningOperationLabels,
			}
		}),
	},
) {}
