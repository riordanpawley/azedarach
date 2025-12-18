/**
 * BoardService - Task and board data management
 *
 * Manages board state (columns, tasks) using fine-grained Effect Refs.
 * Interfaces with BeadsClient for task data and provides methods for task access.
 */

import {
	Array as Arr,
	Cause,
	DateTime,
	Effect,
	HashMap,
	Order,
	Record,
	Schedule,
	Stream,
	SubscriptionRef,
} from "effect"
import { BeadsClient } from "../core/BeadsClient.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import { SessionManager } from "../core/SessionManager.js"
import { emptyRecord } from "../lib/empty.js"
import type { TaskWithSession } from "../ui/types.js"
import { COLUMNS } from "../ui/types.js"
import { EditorService, type SortConfig } from "./EditorService.js"
import { ProjectService } from "./ProjectService.js"

// ============================================================================
// Sort Orders using Effect's composable Order module
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
 * Primary sort: tasks with active sessions (not idle) always come first.
 * This ensures that after actions like "space m" (move task), active sessions
 * remain visible at the top regardless of the selected sort mode.
 */
const byHasActiveSession: Order.Order<TaskWithSession> = Order.mapInput(
	Order.boolean,
	(task: TaskWithSession) => task.sessionState === "idle", // false (has session) < true (idle)
)

/**
 * Sort by detailed session state (busy > waiting > paused > done > error > idle)
 */
const bySessionState: Order.Order<TaskWithSession> = Order.mapInput(
	Order.number,
	(task: TaskWithSession) => getSessionSortValue(task.sessionState),
)

/**
 * Sort by priority (lower number = higher priority, P0 is most urgent)
 */
const byPriority: Order.Order<TaskWithSession> = Order.mapInput(
	Order.number,
	(task: TaskWithSession) => task.priority,
)

/**
 * Sort by updated_at timestamp (most recent first when reversed)
 */
const byUpdatedAt: Order.Order<TaskWithSession> = Order.mapInput(
	Order.number,
	(task: TaskWithSession) => new Date(task.updated_at).getTime(),
)

/**
 * Build a composite sort order based on user's configuration.
 *
 * All sort modes share the same structure:
 * 1. Active sessions first (tasks with sessionState !== "idle")
 * 2. User's primary sort field (with direction)
 * 3. Secondary sort: updatedAt (most recent first) - provides meaningful ordering within primary groups
 * 4. Remaining criteria as tie-breakers
 *
 * The key insight is that `updated` serves as a natural secondary sort for all modes:
 * - Session sort: within each session state, show most recently updated first
 * - Priority sort: within each priority level, show most recently updated first
 * - Updated sort: use session state and priority as tie-breakers
 */
const buildSortOrder = (sortConfig: SortConfig): Order.Order<TaskWithSession> => {
	const applyDirection = (order: Order.Order<TaskWithSession>): Order.Order<TaskWithSession> =>
		sortConfig.direction === "desc" ? Order.reverse(order) : order

	switch (sortConfig.field) {
		case "session":
			// Session mode: active first → session state (with direction) → updatedAt → priority
			// Updated is secondary so within each session state, recently touched tasks rise to top
			return Order.combine(
				byHasActiveSession,
				Order.combine(
					applyDirection(bySessionState),
					Order.combine(Order.reverse(byUpdatedAt), byPriority),
				),
			)

		case "priority":
			// Priority mode: active first → priority (with direction) → updatedAt → session state
			// Updated is secondary so within each priority level, recently touched tasks rise to top
			return Order.combine(
				byHasActiveSession,
				Order.combine(
					applyDirection(byPriority),
					Order.combine(Order.reverse(byUpdatedAt), bySessionState),
				),
			)

		case "updated":
			// Updated mode: active first → updatedAt (with direction) → priority → session state
			// Priority is secondary for updated mode (more useful than session state as tiebreaker)
			return Order.combine(
				byHasActiveSession,
				Order.combine(
					applyDirection(Order.reverse(byUpdatedAt)), // reverse twice for desc = chronological
					Order.combine(byPriority, bySessionState),
				),
			)
	}
}

/**
 * Sort tasks by the given configuration using Effect's composable Order module
 */
const sortTasks = (tasks: TaskWithSession[], sortConfig: SortConfig): TaskWithSession[] => {
	const order = buildSortOrder(sortConfig)
	return Arr.sort(tasks, order)
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
	dependencies: [
		SessionManager.Default,
		BeadsClient.Default,
		EditorService.Default,
		PTYMonitor.Default,
		ProjectService.Default,
	],
	scoped: Effect.gen(function* () {
		// Inject dependencies
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* SessionManager
		const editorService = yield* EditorService
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService

		// Fine-grained state refs with SubscriptionRef for reactive updates
		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
		>(emptyRecord())

		// Filtered and sorted tasks by column - single source of truth for UI
		const filteredTasksByColumn = yield* SubscriptionRef.make<TaskWithSession[][]>(
			COLUMNS.map(() => []),
		)

		/**
		 * Load tasks from BeadsClient and merge with session state + PTY metrics
		 */
		const loadTasks = () =>
			Effect.gen(function* () {
				// Get current project path (falls back to cwd if no project configured)
				const projectPath = yield* projectService.getCurrentPath()

				// Fetch all issues from beads (pass project path for multi-project support)
				const issues = yield* beadsClient.list(undefined, projectPath)

				// Get active sessions to merge their state and metrics
				const activeSessions = yield* sessionManager.listActive()
				const sessionMap = new Map(activeSessions.map((session) => [session.beadId, session]))

				// Get PTY metrics for all sessions
				const allMetrics = yield* SubscriptionRef.get(ptyMonitor.metrics)

				// Map issues to TaskWithSession, merging session state and PTY metrics
				const tasksWithSession: TaskWithSession[] = issues.map((issue) => {
					const session = sessionMap.get(issue.id)
					const metricsOpt = HashMap.get(allMetrics, issue.id)
					const metrics = metricsOpt._tag === "Some" ? metricsOpt.value : {}

					return {
						...issue,
						sessionState: session?.state ?? "idle",
						// Session metrics from SessionManager
						sessionStartedAt: session?.startedAt
							? DateTime.formatIso(session.startedAt)
							: undefined,
						// PTY-extracted metrics from PTYMonitor
						estimatedTokens: metrics.estimatedTokens,
						recentOutput: metrics.recentOutput,
						agentPhase: metrics.agentPhase,
					}
				})

				// Log task counts for debugging (helps diagnose az-vn0 disappearing beads bug)
				const withSessions = tasksWithSession.filter((t) => t.sessionState !== "idle").length
				if (issues.length === 0 || withSessions !== activeSessions.length) {
					yield* Effect.logWarning(
						`Task load: ${issues.length} beads, ${activeSessions.length} active sessions, ${withSessions} beads with sessions`,
					)
				}

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
		 * Compute filtered and sorted tasks by column
		 */
		const computeFilteredTasksByColumn = (
			allTasks: ReadonlyArray<TaskWithSession>,
			searchQuery: string,
			sortConfig: SortConfig,
		): TaskWithSession[][] => {
			return COLUMNS.map((col) => {
				const columnTasks = allTasks.filter((task) => task.status === col.status)
				const filtered = filterTasks(columnTasks, searchQuery)
				return sortTasks(filtered, sortConfig)
			})
		}

		/**
		 * Update filteredTasksByColumn based on current state
		 */
		const updateFilteredTasks = () =>
			Effect.gen(function* () {
				const allTasks = yield* SubscriptionRef.get(tasks)
				const mode = yield* editorService.getMode()
				const sortConfig = yield* editorService.getSortConfig()
				const searchQuery = mode._tag === "search" ? mode.query : ""

				const computed = computeFilteredTasksByColumn(allTasks, searchQuery, sortConfig)
				yield* SubscriptionRef.set(filteredTasksByColumn, computed)
			})

		/**
		 * Refresh board data from BeadsClient
		 */
		const refresh = () =>
			Effect.gen(function* () {
				const loadedTasks = yield* loadTasks()
				yield* SubscriptionRef.set(tasks, loadedTasks)

				const grouped = groupTasksByColumn(loadedTasks)
				yield* SubscriptionRef.set(tasksByColumn, grouped)

				// Also update filtered/sorted view
				yield* updateFilteredTasks()
			})

		// Initial data load - run immediately since Schedule.spaced waits before first execution
		yield* refresh().pipe(
			Effect.catchAllCause((cause) =>
				Effect.logError("BoardService initial refresh failed", Cause.pretty(cause)).pipe(
					Effect.asVoid,
				),
			),
		)

		// Background task refresh (every 2 seconds)
		yield* Effect.scheduleForked(Schedule.spaced("2 seconds"))(
			refresh().pipe(
				Effect.catchAllCause((cause) =>
					Effect.logError("BoardService refresh failed", Cause.pretty(cause)).pipe(Effect.asVoid),
				),
			),
		)

		// Watch for EditorService changes (mode, sortConfig) and update filteredTasksByColumn
		// Merge the change streams so any change triggers an update
		const modeChanges = editorService.mode.changes
		const sortConfigChanges = editorService.sortConfig.changes
		const editorChanges = Stream.merge(modeChanges, sortConfigChanges)

		yield* Effect.forkScoped(
			Stream.runForEach(editorChanges, () =>
				updateFilteredTasks().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logError("FilteredTasks update failed", Cause.pretty(cause)).pipe(Effect.asVoid),
					),
				),
			),
		)
		return {
			// State refs (fine-grained for external subscription)
			tasks,
			tasksByColumn,
			filteredTasksByColumn,

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
