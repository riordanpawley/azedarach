/**
 * BoardService - Task and board data management
 *
 * Manages board state (columns, tasks) using fine-grained Effect Refs.
 * Interfaces with BeadsClient for task data and provides methods for task access.
 */

import { Effect, Record, Schedule, SubscriptionRef } from "effect"
import { BeadsClient } from "../core/BeadsClient"
import { SessionManager } from "../core/SessionManager"
import { emptyRecord } from "../lib/empty"
import type { TaskWithSession } from "../ui/types"
import { COLUMNS } from "../ui/types"
import type { SortConfig } from "./EditorService"

// ============================================================================
// Helpers
// ============================================================================

/**
 * Get sort value for session state (lower = higher priority)
 */
const getSessionSortValue = (state: TaskWithSession["sessionState"]): number => {
	switch (state) {
		case "busy":
			return 0
		case "waiting":
			return 1
		case "paused":
			return 2
		case "done":
			return 3
		case "error":
			return 4
		case "idle":
			return 5
	}
}

/**
 * Sort tasks by the given configuration
 */
const sortTasks = (tasks: TaskWithSession[], sortConfig: SortConfig): TaskWithSession[] => {
	return [...tasks].sort((a, b) => {
		const direction = sortConfig.direction === "desc" ? -1 : 1

		switch (sortConfig.field) {
			case "session": {
				const sessionDiff =
					getSessionSortValue(a.sessionState) - getSessionSortValue(b.sessionState)
				if (sessionDiff !== 0) return sessionDiff * direction
				const priorityDiff = a.priority - b.priority
				if (priorityDiff !== 0) return priorityDiff
				return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
			}
			case "priority": {
				const priorityDiff = a.priority - b.priority
				if (priorityDiff !== 0) return priorityDiff * direction
				const sessionDiff =
					getSessionSortValue(a.sessionState) - getSessionSortValue(b.sessionState)
				if (sessionDiff !== 0) return sessionDiff
				return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
			}
			case "updated": {
				const dateDiff = new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
				if (dateDiff !== 0) return dateDiff * direction
				const sessionDiff =
					getSessionSortValue(a.sessionState) - getSessionSortValue(b.sessionState)
				if (sessionDiff !== 0) return sessionDiff
				return a.priority - b.priority
			}
			default:
				return 0
		}
	})
}

/**
 * Filter tasks by search query
 */
const filterTasks = (tasks: TaskWithSession[], query: string): TaskWithSession[] => {
	if (!query) return tasks
	const lowerQuery = query.toLowerCase()
	return tasks.filter((task) => {
		const titleMatch = task.title.toLowerCase().includes(lowerQuery)
		const idMatch = task.id.toLowerCase().includes(lowerQuery)
		return titleMatch || idMatch
	})
}

// ============================================================================
// Types
// ============================================================================

/**
 * Board state containing tasks organized by column
 */
export interface BoardState {
	readonly tasks: ReadonlyArray<TaskWithSession>
	readonly tasksByColumn: Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
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
	scoped: Effect.gen(function* () {
		// Inject dependencies
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* SessionManager

		// Fine-grained state refs with SubscriptionRef for reactive updates
		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
		>(emptyRecord())

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
		): Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>> => {
			// Initialize all columns with empty arrays, then populate
			const initial: Record.ReadonlyRecord<
				string,
				ReadonlyArray<TaskWithSession>
			> = Record.fromEntries(COLUMNS.map((col) => [col.status, [] as TaskWithSession[]]))

			// Group tasks by status using reduce for immutability
			return taskList.reduce(
				(acc, task) => Record.set(acc, task.status, [...(acc[task.status] ?? []), task]),
				initial,
			)
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

		yield* Effect.scheduleForked(Schedule.spaced("2 seconds"))(
			Effect.gen(function* () {
				yield* refresh()
			}),
		)
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
			getTasksByColumn: (): Effect.Effect<
				Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
			> => SubscriptionRef.get(tasksByColumn),

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
					return grouped[column.status] ?? []
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
					const columnTasks = grouped[column.status] ?? []

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
						const columnTasks = grouped[column.status] ?? []

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

			/**
			 * Get filtered and sorted tasks grouped by column
			 *
			 * This is the method that should be used for display and navigation,
			 * as it applies the current search query and sort configuration.
			 */
			getFilteredTasksByColumn: (
				searchQuery: string,
				sortConfig: SortConfig,
			): Effect.Effect<TaskWithSession[][]> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)

					return COLUMNS.map((col) => {
						// Filter by status
						const columnTasks = allTasks.filter((task) => task.status === col.status)
						// Apply search filter
						const filtered = filterTasks(columnTasks, searchQuery)
						// Apply sorting
						return sortTasks(filtered, sortConfig)
					})
				}),

			/**
			 * Get task at specific position in filtered/sorted view
			 *
			 * This is the method KeyboardService should use to get the currently selected task.
			 */
			getFilteredTaskAt: (
				columnIndex: number,
				taskIndex: number,
				searchQuery: string,
				sortConfig: SortConfig,
			): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) {
						return undefined
					}

					const allTasks = yield* SubscriptionRef.get(tasks)
					const column = COLUMNS[columnIndex]!

					// Filter by status
					const columnTasks = allTasks.filter((task) => task.status === column.status)
					// Apply search filter
					const filtered = filterTasks(columnTasks, searchQuery)
					// Apply sorting
					const sorted = sortTasks(filtered, sortConfig)

					if (taskIndex < 0 || taskIndex >= sorted.length) {
						return undefined
					}

					return sorted[taskIndex]
				}),
		}
	}),
}) {}
