/**
 * NavigationService - Cursor navigation and focus management
 *
 * Manages keyboard navigation state using SubscriptionRef for reactive updates.
 * Provides methods for cursor movement, task jumping, and follow mode.
 *
 * Enhanced with BoardService integration for context-aware navigation
 * (half-page scroll, column boundaries, jump to end).
 */

import { Effect, SubscriptionRef } from "effect"
import { COLUMNS } from "../ui/types"
import { BoardService } from "./BoardService"

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

export class NavigationService extends Effect.Service<NavigationService>()("NavigationService", {
	dependencies: [BoardService.Default],

	effect: Effect.gen(function* () {
		// Inject BoardService for context-aware navigation
		const board = yield* BoardService

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
			 * Set the focused task ID (for syncing UI selection to service)
			 *
			 * Called by useNavigation when the selected task changes.
			 */
			setFocusedTask: (taskId: string | null): Effect.Effect<void> =>
				SubscriptionRef.set(focusedTaskId, taskId),

			/**
			 * Get the focused task ID
			 */
			getFocusedTaskId: (): Effect.Effect<string | null> => SubscriptionRef.get(focusedTaskId),

			/**
			 * Jump to the end of the board (last task in last column)
			 */
			jumpToEnd: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const lastColIdx = COLUMNS.length - 1
					const columnTasks = yield* board.getColumnTasks(lastColIdx)
					const lastTaskIdx = Math.max(0, columnTasks.length - 1)
					yield* SubscriptionRef.set(cursor, {
						columnIndex: lastColIdx,
						taskIndex: lastTaskIdx,
					})
				}),

			/**
			 * Go to first task (top-left: column 0, task 0)
			 */
			goToFirst: (): Effect.Effect<void> =>
				SubscriptionRef.set(cursor, { columnIndex: 0, taskIndex: 0 }),

			/**
			 * Go to last task in current column
			 */
			goToLast: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const { columnIndex } = yield* SubscriptionRef.get(cursor)
					const columnTasks = yield* board.getColumnTasks(columnIndex)
					const lastTaskIdx = Math.max(0, columnTasks.length - 1)
					yield* SubscriptionRef.set(cursor, {
						columnIndex,
						taskIndex: lastTaskIdx,
					})
				}),

			/**
			 * Jump to first column (column 0), keeping same task index
			 */
			goToFirstColumn: (): Effect.Effect<void> =>
				SubscriptionRef.update(cursor, (c) => ({
					...c,
					columnIndex: 0,
				})),

			/**
			 * Jump to last column, keeping same task index
			 */
			goToLastColumn: (): Effect.Effect<void> =>
				SubscriptionRef.update(cursor, (c) => ({
					...c,
					columnIndex: COLUMNS.length - 1,
				})),

			/**
			 * Move down by half the current column's height
			 */
			halfPageDown: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const { columnIndex, taskIndex } = yield* SubscriptionRef.get(cursor)
					const columnTasks = yield* board.getColumnTasks(columnIndex)
					const halfPage = Math.max(1, Math.floor(columnTasks.length / 2))
					const newIndex = Math.min(taskIndex + halfPage, Math.max(0, columnTasks.length - 1))
					yield* SubscriptionRef.set(cursor, {
						columnIndex,
						taskIndex: newIndex,
					})
				}),

			/**
			 * Move up by half the current column's height
			 */
			halfPageUp: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const { columnIndex, taskIndex } = yield* SubscriptionRef.get(cursor)
					const columnTasks = yield* board.getColumnTasks(columnIndex)
					const halfPage = Math.max(1, Math.floor(columnTasks.length / 2))
					const newIndex = Math.max(0, taskIndex - halfPage)
					yield* SubscriptionRef.set(cursor, {
						columnIndex,
						taskIndex: newIndex,
					})
				}),

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
}) {}
