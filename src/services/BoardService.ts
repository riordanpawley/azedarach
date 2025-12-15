/**
 * BoardService - Task and board data management
 *
 * Manages board state (columns, tasks) using fine-grained Effect Refs.
 * Interfaces with BeadsClient for task data and provides methods for task access.
 */

import { Effect, SubscriptionRef } from "effect"
import { BeadsClient } from "../core/BeadsClient"
import { SessionManager } from "../core/SessionManager"
import type { TaskWithSession } from "../ui/types"
import { COLUMNS } from "../ui/types"

// ============================================================================
// Types
// ============================================================================

/**
 * Board state containing tasks organized by column
 */
export interface BoardState {
	readonly tasks: ReadonlyArray<TaskWithSession>
	readonly tasksByColumn: ReadonlyMap<string, ReadonlyArray<TaskWithSession>>
}

/**
 * Column metadata matching the UI COLUMNS constant
 */
export interface ColumnInfo {
	readonly id: string
	readonly title: string
	readonly status: string
}

// ============================================================================
// Service Definition
// ============================================================================

export class BoardService extends Effect.Service<BoardService>()("BoardService", {
	dependencies: [SessionManager.Default, BeadsClient.Default],
	effect: Effect.gen(function* () {
		// Inject dependencies
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* SessionManager

		// Fine-grained state refs with SubscriptionRef for reactive updates
		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			ReadonlyMap<string, ReadonlyArray<TaskWithSession>>
		>(new Map())

		/**
		 * Load tasks from BeadsClient and merge with session state
		 */
		const loadTasks = () =>
			Effect.gen(function* () {
				// Fetch all issues from beads
				const issues = yield* beadsClient.list()

				// Get active sessions to merge their state
				const activeSessions = yield* sessionManager.listActive()
				const sessionStateMap = new Map(
					activeSessions.map((session) => [session.beadId, session.state]),
				)

				// Map issues to TaskWithSession, using real session state if available
				const tasksWithSession: TaskWithSession[] = issues.map((issue) => ({
					...issue,
					sessionState: sessionStateMap.get(issue.id) ?? ("idle" as const),
				}))

				return tasksWithSession
			})

		/**
		 * Group tasks by column status
		 */
		const groupTasksByColumn = (
			taskList: ReadonlyArray<TaskWithSession>,
		): ReadonlyMap<string, ReadonlyArray<TaskWithSession>> => {
			const grouped = new Map<string, TaskWithSession[]>()

			// Initialize all columns with empty arrays
			COLUMNS.forEach((col) => {
				grouped.set(col.status, [])
			})

			// Group tasks by status
			taskList.forEach((task) => {
				const columnTasks = grouped.get(task.status) ?? []
				columnTasks.push(task)
				grouped.set(task.status, columnTasks)
			})

			return grouped
		}

		/**
		 * Refresh board data from BeadsClient
		 */
		const refresh = () =>
			Effect.gen(function* () {
				const loadedTasks = yield* loadTasks()
				yield* SubscriptionRef.set(tasks, loadedTasks)

				const grouped = groupTasksByColumn(loadedTasks)
				yield* SubscriptionRef.set(tasksByColumn, grouped)
			})

		return {
			// State refs (fine-grained for external subscription)
			tasks,
			tasksByColumn,

			/**
			 * Get all tasks
			 */
			getTasks: (): Effect.Effect<ReadonlyArray<TaskWithSession>> => SubscriptionRef.get(tasks),

			/**
			 * Get tasks grouped by column
			 */
			getTasksByColumn: (): Effect.Effect<ReadonlyMap<string, ReadonlyArray<TaskWithSession>>> =>
				SubscriptionRef.get(tasksByColumn),

			/**
			 * Get tasks for a specific column by index
			 */
			getColumnTasks: (columnIndex: number): Effect.Effect<ReadonlyArray<TaskWithSession>> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) {
						return []
					}

					const column = COLUMNS[columnIndex]!
					const grouped = yield* SubscriptionRef.get(tasksByColumn)
					return grouped.get(column.status) ?? []
				}),

			/**
			 * Get task at specific column and task position
			 *
			 * Returns undefined if position is out of bounds.
			 */
			getTaskAt: (
				columnIndex: number,
				taskIndex: number,
			): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) {
						return undefined
					}

					const column = COLUMNS[columnIndex]!
					const grouped = yield* SubscriptionRef.get(tasksByColumn)
					const columnTasks = grouped.get(column.status) ?? []

					if (taskIndex < 0 || taskIndex >= columnTasks.length) {
						return undefined
					}

					return columnTasks[taskIndex]
				}),

			/**
			 * Find task by ID
			 */
			findTaskById: (taskId: string): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)
					return allTasks.find((task) => task.id === taskId)
				}),

			/**
			 * Find task position (column and task index) by ID
			 *
			 * Returns undefined if task is not found.
			 */
			findTaskPosition: (
				taskId: string,
			): Effect.Effect<{ columnIndex: number; taskIndex: number } | undefined> =>
				Effect.gen(function* () {
					const grouped = yield* SubscriptionRef.get(tasksByColumn)

					for (let colIndex = 0; colIndex < COLUMNS.length; colIndex++) {
						const column = COLUMNS[colIndex]!
						const columnTasks = grouped.get(column.status) ?? []

						const taskIndex = columnTasks.findIndex((task) => task.id === taskId)
						if (taskIndex !== -1) {
							return { columnIndex: colIndex, taskIndex }
						}
					}

					return undefined
				}),

			/**
			 * Get column info by index
			 */
			getColumnInfo: (columnIndex: number): Effect.Effect<ColumnInfo | undefined> =>
				// biome-ignore lint/correctness/useYield: <eh>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) {
						return undefined
					}

					return COLUMNS[columnIndex]!
				}),

			/**
			 * Get total number of columns
			 */
			getColumnCount: (): Effect.Effect<number> => Effect.succeed(COLUMNS.length),

			/**
			 * Refresh board data from BeadsClient
			 */
			refresh,

			/**
			 * Load initial board data
			 */
			initialize: refresh,
		}
	}),
}) {}
