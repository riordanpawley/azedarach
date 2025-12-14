/**
 * NavigationService - Cursor navigation and focus management
 *
 * Manages keyboard navigation state using fine-grained Effect Refs.
 * Provides methods for cursor movement, task jumping, and follow mode.
 */

import { Effect, Ref } from "effect"

// ============================================================================
// Types
// ============================================================================

/**
 * Cursor position on the kanban board
 */
export interface Cursor {
	readonly columnIndex: number
	readonly taskIndex: number
}

/**
 * Direction for cursor movement
 */
export type Direction = "up" | "down" | "left" | "right"

// ============================================================================
// Service Definition
// ============================================================================

export class NavigationService extends Effect.Service<NavigationService>()(
	"NavigationService",
	{
		effect: Effect.gen(function* () {
			// Fine-grained state refs
			const columnIndex = yield* Ref.make(0)
			const taskIndex = yield* Ref.make(0)
			const focusedTaskId = yield* Ref.make<string | null>(null)
			const followTaskId = yield* Ref.make<string | null>(null)

			/**
			 * Get current cursor position
			 */
			const getCursor = (): Effect.Effect<Cursor> =>
				Effect.all({
					columnIndex: Ref.get(columnIndex),
					taskIndex: Ref.get(taskIndex),
				})

			return {
				// State refs (fine-grained for external subscription)
				columnIndex,
				taskIndex,
				focusedTaskId,
				followTaskId,

				/**
				 * Move cursor in specified direction
				 *
				 * - up/down: Change task index (clamping done in UI)
				 * - left/right: Change column index and reset task index
				 */
				move: (direction: Direction): Effect.Effect<void> =>
					Effect.gen(function* () {
						switch (direction) {
							case "up":
								yield* Ref.update(taskIndex, (i) => Math.max(0, i - 1))
								break
							case "down":
								yield* Ref.update(taskIndex, (i) => i + 1) // Clamp in UI
								break
							case "left":
								yield* Ref.update(columnIndex, (i) => Math.max(0, i - 1))
								yield* Ref.set(taskIndex, 0)
								break
							case "right":
								yield* Ref.update(columnIndex, (i) => i + 1) // Clamp in UI
								yield* Ref.set(taskIndex, 0)
								break
						}
					}),

				/**
				 * Jump to specific column and task position
				 */
				jumpTo: (column: number, task: number): Effect.Effect<void> =>
					Effect.all([Ref.set(columnIndex, column), Ref.set(taskIndex, task)]).pipe(
						Effect.asVoid,
					),

				/**
				 * Jump to specific task by ID
				 *
				 * Note: Actual positioning requires BoardService for task locations.
				 * This method sets the focusedTaskId which can be used by the UI
				 * to scroll/highlight the task.
				 */
				jumpToTask: (taskId: string): Effect.Effect<void> =>
					Ref.set(focusedTaskId, taskId),

				/**
				 * Jump to the end of the board
				 *
				 * Note: Actual implementation requires board context to know
				 * the actual end position. This is a placeholder that can be
				 * enhanced when integrated with BoardService.
				 */
				jumpToEnd: (): Effect.Effect<void> => Effect.void,

				/**
				 * Set follow mode for a task
				 *
				 * When a task ID is set, the cursor will automatically follow
				 * that task as it moves between columns. Set to null to disable.
				 */
				setFollow: (taskId: string | null): Effect.Effect<void> =>
					Ref.set(followTaskId, taskId),

				/**
				 * Get current cursor position
				 */
				getCursor,
			}
		}),
	},
) {}
