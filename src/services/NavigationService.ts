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

import { Effect, Stream, SubscriptionRef } from "effect"
import { BeadsClient, type Issue } from "../core/BeadsClient.js"
import { computeDependencyPhases } from "../core/dependencyPhases.js"
import type { TaskWithSession } from "../ui/types.js"
import { BoardService } from "./BoardService.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { EditorService } from "./EditorService.js"

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
	dependencies: [
		BoardService.Default,
		DiagnosticsService.Default,
		EditorService.Default,
		BeadsClient.Default,
	],

	scoped: Effect.gen(function* () {
		// Inject services
		const board = yield* BoardService
		const diagnostics = yield* DiagnosticsService
		const editor = yield* EditorService
		const beadsClient = yield* BeadsClient

		// Register with diagnostics - tracks service health
		yield* diagnostics.trackService("NavigationService", "Cursor navigation and focus management")

		// Primary cursor state: the ID of the focused task
		const focusedTaskId = yield* SubscriptionRef.make<string | null>(null)

		// Follow mode: when set, cursor follows this task after operations
		const followTaskId = yield* SubscriptionRef.make<string | null>(null)

		// Drill-down mode: when set, board shows only children of this epic
		// Stores the epic ID, or null for normal view
		const drillDownEpic = yield* SubscriptionRef.make<string | null>(null)

		// Children IDs for drill-down filtering (populated when entering drill-down)
		const drillDownChildIds = yield* SubscriptionRef.make<ReadonlySet<string>>(new Set())

		// Full Issue objects for children (needed for dependency phase computation)
		// Map from child ID to full Issue with dependencies array
		const drillDownChildDetails = yield* SubscriptionRef.make<ReadonlyMap<string, Issue>>(new Map())

		// Remember cursor position before entering drill-down (for restoration)
		const savedFocusedTaskId = yield* SubscriptionRef.make<string | null>(null)

		/**
		 * Sort tasks by phase within a column (for drill-down mode).
		 *
		 * This matches the rendering order in Column.tsx which groups tasks by phase.
		 * Tasks within the same phase maintain their relative order.
		 */
		const sortByPhase = (
			tasks: TaskWithSession[],
			phases: ReadonlyMap<string, { phase: number; blockedBy: readonly string[] }>,
		): TaskWithSession[] => {
			return [...tasks].sort((a, b) => {
				const phaseA = phases.get(a.id)?.phase ?? 1
				const phaseB = phases.get(b.id)?.phase ?? 1
				return phaseA - phaseB
			})
		}

		/**
		 * Get the filtered/sorted tasks by column for navigation.
		 *
		 * IMPORTANT: Uses board.filteredTasksByColumn SubscriptionRef (same source as UI)
		 * to ensure navigation and rendering always see the exact same task list.
		 *
		 * When in drill-down mode:
		 * 1. Filters to only the epic's children
		 * 2. Sorts by dependency phase (matching Column.tsx rendering order)
		 */
		const getFilteredTasksByColumn = () =>
			Effect.gen(function* () {
				// Use the same pre-computed SubscriptionRef that the UI uses
				// This ensures navigation and rendering are always in sync
				const tasksByColumn = yield* SubscriptionRef.get(board.filteredTasksByColumn)

				// Apply drill-down filter if active
				const childIds = yield* SubscriptionRef.get(drillDownChildIds)
				if (childIds.size === 0) {
					// Main board: filter OUT epic children (same as board.ts drillDownFilteredTasksAtom)
					// Epic children are hidden on main board, only visible in drill-down
					return tasksByColumn.map((column) =>
						column.filter((task) => task.parentEpicId === undefined),
					)
				}

				// Drill-down mode: filter to only include the epic's children
				const filteredColumns = tasksByColumn.map((column) =>
					column.filter((task) => childIds.has(task.id)),
				)

				// Get child details for phase computation
				const childDetails = yield* SubscriptionRef.get(drillDownChildDetails)

				// If we have child details, compute phases and sort by phase
				// This matches Column.tsx rendering which groups tasks by phase
				if (childDetails.size > 0) {
					const phaseResult = computeDependencyPhases(childIds, childDetails)
					return filteredColumns.map((column) => sortByPhase(column, phaseResult.phases))
				}

				return filteredColumns
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

		// Watch for mode changes (especially search query changes) and ensure focus is valid
		// When the search filter changes, the currently focused task may no longer be visible
		// This keeps focusedTaskId in sync with the filtered view
		yield* Effect.forkScoped(
			Stream.runForEach(editor.mode.changes, () =>
				ensureValidFocus().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logDebug("NavigationService ensureValidFocus failed", { cause }).pipe(
							Effect.asVoid,
						),
					),
				),
			),
		)

		/**
		 * Core drill-down refresh logic.
		 * Re-fetches epic children and updates state if new children found.
		 */
		const refreshDrillDownCore = (epicId: string) =>
			Effect.gen(function* () {
				// Fetch current epic children
				const children = yield* beadsClient
					.getEpicChildren(epicId)
					.pipe(Effect.catchAll(() => Effect.succeed([])))

				const newChildIds = new Set(children.map((c: { id: string }) => c.id))

				// Get existing child IDs to check for new children
				const existingChildIds = yield* SubscriptionRef.get(drillDownChildIds)

				// Find new children (not in existing set)
				const addedChildren = children.filter((c) => !existingChildIds.has(c.id))

				if (addedChildren.length === 0) {
					// No new children - nothing to update
					return
				}

				yield* Effect.log(
					`Drill-down refresh: found ${addedChildren.length} new child(ren) for epic ${epicId}`,
				)

				// Update child IDs set
				yield* SubscriptionRef.set(drillDownChildIds, newChildIds)

				// Fetch details for new children only (incremental)
				const newDetailResults = yield* Effect.all(
					addedChildren.map((child: { id: string }) =>
						beadsClient
							.show(child.id)
							.pipe(Effect.map((issue) => [child.id, issue] as const))
							.pipe(Effect.catchAll(() => Effect.succeed(null))),
					),
					{ concurrency: "unbounded" },
				)

				// Merge new details into existing map
				const existingDetails = yield* SubscriptionRef.get(drillDownChildDetails)
				const updatedDetails = new Map(existingDetails)
				for (const result of newDetailResults) {
					if (result !== null) {
						updatedDetails.set(result[0], result[1])
					}
				}

				yield* SubscriptionRef.set(drillDownChildDetails, updatedDetails)
			})

		// Watch for board task changes and refresh drill-down state when in drill-down mode
		// This ensures newly added epic children appear without re-entering drill-down
		yield* Effect.forkScoped(
			Stream.runForEach(board.tasks.changes, () =>
				Effect.gen(function* () {
					// Check if in drill-down mode
					const epicId = yield* SubscriptionRef.get(drillDownEpic)
					if (epicId === null) return

					// Refresh drill-down with new children
					yield* refreshDrillDownCore(epicId)
				}).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logDebug("NavigationService drill-down refresh failed", { cause }).pipe(
							Effect.asVoid,
						),
					),
				),
			),
		)

		return {
			// Expose SubscriptionRefs for atom subscription
			focusedTaskId,
			followTaskId,
			drillDownEpic,
			drillDownChildIds,
			drillDownChildDetails,

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
						case "up": {
							const column = tasksByColumn[columnIndex]!
							if (taskIndex > 0) {
								// Move up within the same column
								newTaskIdx = taskIndex - 1
							} else {
								// At the top of column, wrap to bottom
								newTaskIdx = column.length - 1
							}
							break
						}
						case "down": {
							const column = tasksByColumn[columnIndex]!
							if (taskIndex < column.length - 1) {
								// Move down within the same column
								newTaskIdx = taskIndex + 1
							} else {
								// At the bottom of column, wrap to top
								newTaskIdx = 0
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
			 * Go to first task in current column
			 */
			goToFirst: (): Effect.Effect<void> =>
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
						const firstTask = column[0]!
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

			/**
			 * Enter drill-down mode for an epic
			 *
			 * Shows only the epic's children in the board view.
			 * Saves current cursor position for restoration when exiting.
			 *
			 * @param epicId - The epic ID to drill into
			 * @param childIds - Set of child task IDs for filtering
			 * @param childDetails - Optional map of child ID to full Issue (for phase computation)
			 */
			enterDrillDown: (
				epicId: string,
				childIds: ReadonlySet<string>,
				childDetails?: ReadonlyMap<string, Issue>,
			): Effect.Effect<void> =>
				Effect.gen(function* () {
					// Save current cursor position for restoration
					const currentId = yield* SubscriptionRef.get(focusedTaskId)
					yield* SubscriptionRef.set(savedFocusedTaskId, currentId)

					// Store children IDs for filtering
					yield* SubscriptionRef.set(drillDownChildIds, childIds)

					// Store child details for phase computation (if provided)
					yield* SubscriptionRef.set(drillDownChildDetails, childDetails ?? new Map())

					// Enter drill-down mode
					yield* SubscriptionRef.set(drillDownEpic, epicId)

					// Clear focus - will be set to first child when board renders
					yield* SubscriptionRef.set(focusedTaskId, null)
				}),

			/**
			 * Exit drill-down mode
			 *
			 * Returns to normal board view and restores cursor position.
			 */
			exitDrillDown: (): Effect.Effect<void> =>
				Effect.gen(function* () {
					// Restore saved cursor position
					const savedId = yield* SubscriptionRef.get(savedFocusedTaskId)
					if (savedId) {
						yield* SubscriptionRef.set(focusedTaskId, savedId)
					}
					yield* SubscriptionRef.set(savedFocusedTaskId, null)

					// Clear children IDs and details
					yield* SubscriptionRef.set(drillDownChildIds, new Set())
					yield* SubscriptionRef.set(drillDownChildDetails, new Map())

					// Exit drill-down mode
					yield* SubscriptionRef.set(drillDownEpic, null)
				}),

			/**
			 * Check if currently in drill-down mode
			 */
			isInDrillDown: (): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const epic = yield* SubscriptionRef.get(drillDownEpic)
					return epic !== null
				}),

			/**
			 * Get current drill-down epic ID (if any)
			 */
			getDrillDownEpic: (): Effect.Effect<string | null> => SubscriptionRef.get(drillDownEpic),

			/**
			 * Refresh drill-down state with current epic children
			 *
			 * Called when board tasks update to detect newly added epic children.
			 * Only updates if currently in drill-down mode.
			 *
			 * @param epicId - The epic ID to refresh (must match current drill-down epic)
			 */
			refreshDrillDown: (epicId: string) =>
				Effect.gen(function* () {
					// Only refresh if we're in drill-down for this epic
					const currentEpic = yield* SubscriptionRef.get(drillDownEpic)
					if (currentEpic !== epicId) return

					yield* refreshDrillDownCore(epicId)
				}),
		}
	}),
}) {}
