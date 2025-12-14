/**
 * NavigationService - Cursor navigation and focus management
 *
 * Manages keyboard navigation state using SubscriptionRef for reactive updates.
 * Provides methods for cursor movement, task jumping, and follow mode.
 */

import { Effect, SubscriptionRef } from "effect"

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
			// Single SubscriptionRef for cursor - enables reactive subscriptions
			const cursor = yield* SubscriptionRef.make<Cursor>({ columnIndex: 0, taskIndex: 0 })
			const focusedTaskId = yield* SubscriptionRef.make<string | null>(null)
			const followTaskId = yield* SubscriptionRef.make<string | null>(null)

			return {
				// Expose SubscriptionRefs for atom subscription
				cursor,
				focusedTaskId,
				followTaskId,

				/**
				 * Move cursor in specified direction
				 *
				 * - up/down: Change task index (clamping done in UI)
				 * - left/right: Change column index and reset task index
				 */
				move: (direction: Direction): Effect.Effect<void> =>
					SubscriptionRef.update(cursor, (c) => {
						switch (direction) {
							case "up":
								return { ...c, taskIndex: Math.max(0, c.taskIndex - 1) }
							case "down":
								return { ...c, taskIndex: c.taskIndex + 1 } // Clamp in UI
							case "left":
								return { columnIndex: Math.max(0, c.columnIndex - 1), taskIndex: 0 }
							case "right":
								return { columnIndex: c.columnIndex + 1, taskIndex: 0 } // Clamp in UI
						}
					}),

				/**
				 * Jump to specific column and task position
				 */
				jumpTo: (column: number, task: number): Effect.Effect<void> =>
					SubscriptionRef.set(cursor, { columnIndex: column, taskIndex: task }),

				/**
				 * Jump to specific task by ID
				 *
				 * Note: Actual positioning requires BoardService for task locations.
				 * This method sets the focusedTaskId which can be used by the UI
				 * to scroll/highlight the task.
				 */
				jumpToTask: (taskId: string): Effect.Effect<void> =>
					SubscriptionRef.set(focusedTaskId, taskId),

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
					SubscriptionRef.set(followTaskId, taskId),

				/**
				 * Get current cursor position (for non-reactive reads)
				 */
				getCursor: (): Effect.Effect<Cursor> => SubscriptionRef.get(cursor),
			}
		}),
	},
) {}
