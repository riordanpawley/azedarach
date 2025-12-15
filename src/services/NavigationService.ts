/**
 * NavigationService - ID-based cursor navigation
 *
 * Manages keyboard navigation state using focusedTaskId as the primary cursor.
 * This approach avoids sync issues between index-based positions and filtered/sorted views.
 *
 * The cursor is always a task ID, not an index. When navigating:
 * 1. Find current task's position in the filtered view
 * 2. Calculate new position based on direction
 * 3. Store the new task's ID
 *
 * This ensures the selected task is always correct regardless of filtering/sorting.
 */

import { Effect, SubscriptionRef } from "effect"
import { BoardService } from "./BoardService"
import { EditorService } from "./EditorService"

// ============================================================================
// Types
// ============================================================================

/**
 * Direction for cursor movement
 */
export type Direction = "up" | "down" | "left" | "right"

/**
 * Position in the filtered view (computed from focusedTaskId)
 */
export interface Position {
	readonly columnIndex: number
	readonly taskIndex: number
}

// ============================================================================
// Service Definition
// ============================================================================

export class NavigationService extends Effect.Service<NavigationService>()("NavigationService", {
	dependencies: [BoardService.Default, EditorService.Default],

	effect: Effect.gen(function* () {
		// Inject services
		const board = yield* BoardService
		const editor = yield* EditorService

		// Primary cursor state: the ID of the focused task
		const focusedTaskId = yield* SubscriptionRef.make<string | null>(null)

		// Follow mode: when set, cursor follows this task after operations
		const followTaskId = yield* SubscriptionRef.make<string | null>(null)

		/**
		 * Get the filtered/sorted tasks by column using current search/sort config
		 */
		const getFilteredTasksByColumn = () =>
			Effect.gen(function* () {
				const mode = yield* editor.getMode()
				const sortConfig = yield* editor.getSortConfig()
				const searchQuery = mode._tag === "search" ? mode.query : ""
				return yield* board.getFilteredTasksByColumn(searchQuery, sortConfig)
			})

		/**
		 * Find position of a task by ID in the filtered view
		 * Returns undefined if not found
		 */
		const findTaskPosition = (taskId: string | null) =>
			Effect.gen(function* () {
				if (!taskId) return undefined

				const tasksByColumn = yield* getFilteredTasksByColumn()

				for (let colIdx = 0; colIdx < tasksByColumn.length; colIdx++) {
					const column = tasksByColumn[colIdx]!
					const taskIdx = column.findIndex((t) => t.id === taskId)
					if (taskIdx >= 0) {
						return { columnIndex: colIdx, taskIndex: taskIdx }
					}
				}
				return undefined
			})

		/**
		 * Get task at position in filtered view
		 */
		const getTaskAtPosition = (columnIndex: number, taskIndex: number) =>
			Effect.gen(function* () {
				const tasksByColumn = yield* getFilteredTasksByColumn()

				if (columnIndex < 0 || columnIndex >= tasksByColumn.length) {
					return undefined
				}

				const column = tasksByColumn[columnIndex]!
				if (taskIndex < 0 || taskIndex >= column.length) {
					return undefined
				}

				return column[taskIndex]
			})

		/**
		 * Get the first available task (for initialization or when current task is deleted)
		 */
		const getFirstTask = () =>
			Effect.gen(function* () {
				const tasksByColumn = yield* getFilteredTasksByColumn()

				for (const column of tasksByColumn) {
					if (column.length > 0) {
						return column[0]
					}
				}
				return undefined
			})

		/**
		 * Ensure we have a valid focused task
		 * If current focusedTaskId is invalid, select the first available task
		 */
		const ensureValidFocus = () =>
			Effect.gen(function* () {
				const currentId = yield* SubscriptionRef.get(focusedTaskId)
				const position = yield* findTaskPosition(currentId)

				if (!position) {
					// Current task not found, select first available
					const firstTask = yield* getFirstTask()
					if (firstTask) {
						yield* SubscriptionRef.set(focusedTaskId, firstTask.id)
					}
				}
			})

		return {
			// Expose SubscriptionRefs for atom subscription
			focusedTaskId,
			followTaskId,

			/**
			 * Get current position of focused task
			 * Returns position in filtered view, or { 0, 0 } if not found
			 */
			getPosition: (): Effect.Effect<Position> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const position = yield* findTaskPosition(currentId)
					return position ?? { columnIndex: 0, taskIndex: 0 }
				}),

			/**
			 * Get the focused task ID
			 */
			getFocusedTaskId: (): Effect.Effect<string | null> => SubscriptionRef.get(focusedTaskId),

			/**
			 * Get the currently focused task
			 */
			getFocusedTask: () =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					if (!currentId) return undefined

					const allTasks = yield* board.getTasks()
					return allTasks.find((t) => t.id === currentId)
				}),

			/**
			 * Move cursor in specified direction
			 *
			 * Finds current position in filtered view, calculates new position,
			 * and updates focusedTaskId to the task at the new position.
			 */
			move: (direction: Direction): Effect.Effect<void> =>
				Effect.gen(function* () {
					const tasksByColumn = yield* getFilteredTasksByColumn()
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)

					// If no current position, initialize to first task
					if (!currentPos) {
						yield* ensureValidFocus()
						return
					}

					const { columnIndex, taskIndex } = currentPos
					let newColIdx = columnIndex
					let newTaskIdx = taskIndex

					switch (direction) {
						case "up":
							if (taskIndex > 0) {
								// Move up within the same column
								newTaskIdx = taskIndex - 1
							} else {
								// At the top of column, move to previous column's last task
								for (let col = columnIndex - 1; col >= 0; col--) {
									if (tasksByColumn[col]!.length > 0) {
										newColIdx = col
										newTaskIdx = tasksByColumn[col]!.length - 1
										break
									}
								}
							}
							break
						case "down": {
							const column = tasksByColumn[columnIndex]!
							if (taskIndex < column.length - 1) {
								// Move down within the same column
								newTaskIdx = taskIndex + 1
							} else {
								// At the bottom of column, move to next column's first task
								for (let col = columnIndex + 1; col < tasksByColumn.length; col++) {
									if (tasksByColumn[col]!.length > 0) {
										newColIdx = col
										newTaskIdx = 0
										break
									}
								}
							}
							break
						}
						case "left":
							// Skip empty columns when moving left
							for (let col = columnIndex - 1; col >= 0; col--) {
								if (tasksByColumn[col]!.length > 0) {
									newColIdx = col
									newTaskIdx = 0
									break
								}
							}
							break
						case "right":
							// Skip empty columns when moving right
							for (let col = columnIndex + 1; col < tasksByColumn.length; col++) {
								if (tasksByColumn[col]!.length > 0) {
									newColIdx = col
									newTaskIdx = 0
									break
								}
							}
							break
					}

					// Get task at new position
					const newTask = yield* getTaskAtPosition(newColIdx, newTaskIdx)
					if (newTask) {
						yield* SubscriptionRef.set(focusedTaskId, newTask.id)
					}
				}),

			/**
			 * Jump to specific column and task position
			 */
			jumpTo: (column: number, task: number): Effect.Effect<void> =>
				Effect.gen(function* () {
					const newTask = yield* getTaskAtPosition(column, task)
					if (newTask) {
						yield* SubscriptionRef.set(focusedTaskId, newTask.id)
					}
				}),

			/**
			 * Jump to specific task by ID
			 */
			jumpToTask: (taskId: string): Effect.Effect<void> =>
				SubscriptionRef.set(focusedTaskId, taskId),

			/**
			 * Set the focused task ID directly
			 */
			setFocusedTask: (taskId: string | null): Effect.Effect<void> =>
				SubscriptionRef.set(focusedTaskId, taskId),

			/**
			 * Jump to the end of the board (last task in last column)
			 */
			jumpToEnd: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const tasksByColumn = yield* getFilteredTasksByColumn()

					// Find last non-empty column
					for (let colIdx = tasksByColumn.length - 1; colIdx >= 0; colIdx--) {
						const column = tasksByColumn[colIdx]!
						if (column.length > 0) {
							const lastTask = column[column.length - 1]!
							yield* SubscriptionRef.set(focusedTaskId, lastTask.id)
							return
						}
					}
				}),

			/**
			 * Go to first task (top-left: first task in first column)
			 */
			goToFirst: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const firstTask = yield* getFirstTask()
					if (firstTask) {
						yield* SubscriptionRef.set(focusedTaskId, firstTask.id)
					}
				}),

			/**
			 * Go to last task in current column
			 */
			goToLast: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)

					if (!currentPos) {
						yield* ensureValidFocus()
						return
					}

					const tasksByColumn = yield* getFilteredTasksByColumn()
					const column = tasksByColumn[currentPos.columnIndex]!

					if (column.length > 0) {
						const lastTask = column[column.length - 1]!
						yield* SubscriptionRef.set(focusedTaskId, lastTask.id)
					}
				}),

			/**
			 * Jump to first column, keeping approximate task position
			 */
			goToFirstColumn: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					const tasksByColumn = yield* getFilteredTasksByColumn()

					const firstColumn = tasksByColumn[0]!
					if (firstColumn.length === 0) return

					// Try to keep same row, otherwise go to last row in first column
					const targetRow = currentPos ? Math.min(currentPos.taskIndex, firstColumn.length - 1) : 0
					const task = firstColumn[targetRow]!
					yield* SubscriptionRef.set(focusedTaskId, task.id)
				}),

			/**
			 * Jump to last column, keeping approximate task position
			 */
			goToLastColumn: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)
					const tasksByColumn = yield* getFilteredTasksByColumn()

					const lastColIdx = tasksByColumn.length - 1
					const lastColumn = tasksByColumn[lastColIdx]!
					if (lastColumn.length === 0) return

					// Try to keep same row, otherwise go to last row
					const targetRow = currentPos ? Math.min(currentPos.taskIndex, lastColumn.length - 1) : 0
					const task = lastColumn[targetRow]!
					yield* SubscriptionRef.set(focusedTaskId, task.id)
				}),

			/**
			 * Move down by half the current column's height
			 */
			halfPageDown: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)

					if (!currentPos) {
						yield* ensureValidFocus()
						return
					}

					const tasksByColumn = yield* getFilteredTasksByColumn()
					const column = tasksByColumn[currentPos.columnIndex]!
					const halfPage = Math.max(1, Math.floor(column.length / 2))
					const newIdx = Math.min(currentPos.taskIndex + halfPage, column.length - 1)

					const task = column[newIdx]
					if (task) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
				}),

			/**
			 * Move up by half the current column's height
			 */
			halfPageUp: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const currentPos = yield* findTaskPosition(currentId)

					if (!currentPos) {
						yield* ensureValidFocus()
						return
					}

					const tasksByColumn = yield* getFilteredTasksByColumn()
					const column = tasksByColumn[currentPos.columnIndex]!
					const halfPage = Math.max(1, Math.floor(column.length / 2))
					const newIdx = Math.max(0, currentPos.taskIndex - halfPage)

					const task = column[newIdx]
					if (task) {
						yield* SubscriptionRef.set(focusedTaskId, task.id)
					}
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
			 * Initialize cursor to first task if not set
			 */
			initialize: (): Effect.Effect<void> => ensureValidFocus(),

			// Legacy compatibility - cursor position computed from focusedTaskId
			// TODO: Remove once all consumers use ID-based navigation
			getCursor: (): Effect.Effect<Position> =>
				Effect.gen(function* () {
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					const position = yield* findTaskPosition(currentId)
					return position ?? { columnIndex: 0, taskIndex: 0 }
				}),
		}
	}),
}) {}
