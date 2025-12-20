/**
 * BoardService - Task and board data management
 *
 * Manages board state (columns, tasks) using fine-grained Effect Refs.
 * Interfaces with BeadsClient for task data and provides methods for task access.
 */

import { Command } from "@effect/platform"
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
import { AppConfig } from "../config/AppConfig.js"
import { BeadsClient } from "../core/BeadsClient.js"
import { ClaudeSessionManager } from "../core/ClaudeSessionManager.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import { getWorktreePath } from "../core/paths.js"
import { emptyRecord } from "../lib/empty.js"
import type { GitStatus, TaskWithSession } from "../ui/types.js"
import { COLUMNS } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { EditorService, type FilterConfig, type SortConfig } from "./EditorService.js"
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
		case "warning":
			return 1 // Warning state: session started but with issues
		case "waiting":
			return 2
		case "paused":
			return 3
		case "done":
			return 4
		case "error":
			return 5
		case "idle":
			return 6
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
const filterTasksByQuery = (tasks: TaskWithSession[], query: string): TaskWithSession[] => {
	if (!query) return tasks
	const lowerQuery = query.toLowerCase()
	return tasks.filter((task) => {
		const titleMatch = task.title.toLowerCase().includes(lowerQuery)
		const idMatch = task.id.toLowerCase().includes(lowerQuery)
		return titleMatch || idMatch
	})
}

/**
 * Check if a task is a child of an epic (has parent-child dependency)
 */
const isEpicChild = (task: TaskWithSession): boolean => {
	if (!task.dependencies) return false
	return task.dependencies.some((dep) => dep.dependency_type === "parent-child")
}

/**
 * Apply FilterConfig to tasks
 *
 * Empty sets mean "show all" for that field.
 * Multiple values within a field use OR logic.
 * Different fields use AND logic.
 */
const applyFilterConfig = (tasks: TaskWithSession[], config: FilterConfig): TaskWithSession[] => {
	return tasks.filter((task) => {
		// Status filter (OR within set)
		if (config.status.size > 0) {
			const taskStatus = task.status
			// Map the task status to FilterConfig status types (excluding tombstone)
			if (
				taskStatus !== "open" &&
				taskStatus !== "in_progress" &&
				taskStatus !== "blocked" &&
				taskStatus !== "closed"
			) {
				return false
			}
			if (!config.status.has(taskStatus)) {
				return false
			}
		}

		// Priority filter (OR within set)
		if (config.priority.size > 0) {
			if (!config.priority.has(task.priority)) {
				return false
			}
		}

		// Type filter (OR within set)
		if (config.type.size > 0) {
			if (!config.type.has(task.issue_type)) {
				return false
			}
		}

		// Session filter (OR within set)
		if (config.session.size > 0) {
			// Map task sessionState to FilterSessionState (warning maps to idle for filtering)
			const sessionState = task.sessionState === "warning" ? "idle" : task.sessionState
			if (
				sessionState !== "idle" &&
				sessionState !== "busy" &&
				sessionState !== "waiting" &&
				sessionState !== "done" &&
				sessionState !== "error" &&
				sessionState !== "paused"
			) {
				return false
			}
			if (!config.session.has(sessionState)) {
				return false
			}
		}

		// Hide epic subtasks filter
		if (config.hideEpicSubtasks && isEpicChild(task)) {
			return false
		}

		return true
	})
}

/**
 * Filter tasks by search query and filter config
 */
const filterTasks = (
	tasks: TaskWithSession[],
	query: string,
	filterConfig?: FilterConfig,
): TaskWithSession[] => {
	let filtered = filterTasksByQuery(tasks, query)
	if (filterConfig) {
		filtered = applyFilterConfig(filtered, filterConfig)
	}
	return filtered
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
		ClaudeSessionManager.Default,
		BeadsClient.Default,
		EditorService.Default,
		PTYMonitor.Default,
		ProjectService.Default,
		DiagnosticsService.Default,
		AppConfig.Default,
	],
	scoped: Effect.gen(function* () {
		// Inject dependencies
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* ClaudeSessionManager
		const editorService = yield* EditorService
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig

		// Register with diagnostics
		yield* diagnostics.trackService("BoardService", "2s beads polling + session state merge")

		// Fine-grained state refs with SubscriptionRef for reactive updates
		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
		>(emptyRecord())

		// Loading state for non-blocking refresh feedback
		const isLoading = yield* SubscriptionRef.make<boolean>(false)

		// Filtered and sorted tasks by column - single source of truth for UI
		const filteredTasksByColumn = yield* SubscriptionRef.make<TaskWithSession[][]>(
			COLUMNS.map(() => []),
		)

		/**
		 * Check if a worktree has an active merge conflict
		 *
		 * Uses `git rev-parse MERGE_HEAD` which returns 0 if in merge state, 128 otherwise.
		 * Works correctly for worktrees (handles .git file pointing to real git dir).
		 */
		const checkMergeConflict = (worktreePath: string) =>
			Effect.gen(function* () {
				const command = Command.make("git", "-C", worktreePath, "rev-parse", "MERGE_HEAD").pipe(
					Command.exitCode,
				)
				// Exit code 0 = MERGE_HEAD exists (in merge state)
				// Exit code 128 = not in merge state (normal)
				const exitCode = yield* command.pipe(Effect.catchAll(() => Effect.succeed(128)))
				return exitCode === 0
			})

		/**
		 * Get git status for a worktree
		 *
		 * Returns information about:
		 * - How many commits the branch is behind the base branch
		 * - Whether there are uncommitted changes (dirty worktree)
		 * - Line additions/deletions (if showLineChanges is enabled)
		 */
		const checkGitStatus = (worktreePath: string, baseBranch: string, showLineChanges: boolean) =>
			Effect.gen(function* () {
				// Check commits behind: git rev-list --count HEAD..<baseBranch>
				// Uses LOCAL baseBranch (not origin/) since Azedarach merges are all local
				const behindCommand = Command.make(
					"git",
					"-C",
					worktreePath,
					"rev-list",
					"--count",
					`HEAD..${baseBranch}`,
				).pipe(Command.string)

				const behindCount = yield* behindCommand.pipe(
					Effect.map((output) => {
						const count = Number.parseInt(output.trim(), 10)
						return Number.isNaN(count) ? 0 : count
					}),
					Effect.catchAll(() => Effect.succeed(0)),
				)

				// Check for uncommitted changes: git status --porcelain
				// Non-empty output means there are changes
				const dirtyCommand = Command.make("git", "-C", worktreePath, "status", "--porcelain").pipe(
					Command.string,
				)

				const hasUncommittedChanges = yield* dirtyCommand.pipe(
					Effect.map((output) => output.trim().length > 0),
					Effect.catchAll(() => Effect.succeed(false)),
				)

				// Get line changes if enabled
				let gitAdditions: number | undefined
				let gitDeletions: number | undefined

				if (showLineChanges) {
					// Line stats comparing worktree branch to local baseBranch
					// Using numstat for easier parsing: <add>\t<del>\t<file>
					const diffCommand = Command.make(
						"git",
						"-C",
						worktreePath,
						"diff",
						"--numstat",
						`${baseBranch}...HEAD`,
					).pipe(Command.string)

					const diffStats = yield* diffCommand.pipe(
						Effect.map((output) => {
							let additions = 0
							let deletions = 0
							for (const line of output.trim().split("\n")) {
								if (!line) continue
								const parts = line.split("\t")
								const add = Number.parseInt(parts[0] ?? "0", 10)
								const del = Number.parseInt(parts[1] ?? "0", 10)
								if (!Number.isNaN(add)) additions += add
								if (!Number.isNaN(del)) deletions += del
							}
							return { additions, deletions }
						}),
						Effect.catchAll(() => Effect.succeed({ additions: 0, deletions: 0 })),
					)

					gitAdditions = diffStats.additions
					gitDeletions = diffStats.deletions
				}

				return {
					gitBehindCount: behindCount > 0 ? behindCount : undefined,
					hasUncommittedChanges: hasUncommittedChanges || undefined,
					gitAdditions: gitAdditions !== undefined && gitAdditions > 0 ? gitAdditions : undefined,
					gitDeletions: gitDeletions !== undefined && gitDeletions > 0 ? gitDeletions : undefined,
				}
			})

		/**
		 * Load tasks from BeadsClient and merge with session state + PTY metrics
		 */
		const loadTasks = () =>
			Effect.gen(function* () {
				// Get current project path (falls back to cwd if no project configured)
				const projectPath = yield* projectService.getCurrentPath()

				// Get git config for status checks
				const gitConfig = yield* appConfig.getGitConfig()
				const { baseBranch, showLineChanges } = gitConfig

				// Fetch all issues from beads (pass project path for multi-project support)
				const issues = yield* beadsClient.list(undefined, projectPath)

				// Get active sessions to merge their state and metrics (pass project path)
				const activeSessions = yield* sessionManager.listActive(projectPath ?? undefined)
				const sessionMap = new Map(activeSessions.map((session) => [session.beadId, session]))

				// Get PTY metrics for all sessions
				const allMetrics = yield* SubscriptionRef.get(ptyMonitor.metrics)

				// Map issues to TaskWithSession, merging session state and PTY metrics
				// Then check merge conflicts and git status in parallel for tasks with active sessions
				const tasksWithSession: TaskWithSession[] = yield* Effect.all(
					issues.map((issue) =>
						Effect.gen(function* () {
							const session = sessionMap.get(issue.id)
							const metricsOpt = HashMap.get(allMetrics, issue.id)
							const metrics = metricsOpt._tag === "Some" ? metricsOpt.value : {}
							const sessionState = session?.state ?? "idle"

							// Check for merge conflicts and git status only if task has an active session
							let hasMergeConflict = false
							let gitStatus: GitStatus = {}
							if (sessionState !== "idle" && projectPath) {
								const worktreePath = getWorktreePath(projectPath, issue.id)
								// Run merge conflict check and git status check in parallel
								const [mergeConflict, status] = yield* Effect.all([
									checkMergeConflict(worktreePath),
									checkGitStatus(worktreePath, baseBranch, showLineChanges),
								])
								hasMergeConflict = mergeConflict
								gitStatus = status
							}

							return {
								...issue,
								sessionState,
								hasMergeConflict,
								// Git status from checkGitStatus
								...gitStatus,
								// Session metrics from ClaudeSessionManager
								sessionStartedAt: session?.startedAt
									? DateTime.formatIso(session.startedAt)
									: undefined,
								// PTY-extracted metrics from PTYMonitor
								estimatedTokens: metrics.estimatedTokens,
								recentOutput: metrics.recentOutput,
								agentPhase: metrics.agentPhase,
							}
						}),
					),
					{ concurrency: "unbounded" },
				)

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
			filterConfig: FilterConfig,
		): TaskWithSession[][] => {
			return COLUMNS.map((col) => {
				const columnTasks = allTasks.filter((task) => task.status === col.status)
				const filtered = filterTasks(columnTasks, searchQuery, filterConfig)
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
				const filterConfig = yield* editorService.getFilterConfig()
				const searchQuery = mode._tag === "search" ? mode.query : ""

				const computed = computeFilteredTasksByColumn(
					allTasks,
					searchQuery,
					sortConfig,
					filterConfig,
				)
				yield* SubscriptionRef.set(filteredTasksByColumn, computed)
			})

		/**
		 * Refresh board data from BeadsClient
		 *
		 * Sets isLoading=true during refresh for UI feedback.
		 * Can be run in background via Effect.fork for non-blocking behavior.
		 */
		const refresh = () =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(isLoading, true)

				const loadedTasks = yield* loadTasks()
				yield* SubscriptionRef.set(tasks, loadedTasks)

				const grouped = groupTasksByColumn(loadedTasks)
				yield* SubscriptionRef.set(tasksByColumn, grouped)

				// Also update filtered/sorted view
				yield* updateFilteredTasks()
			}).pipe(Effect.ensuring(SubscriptionRef.set(isLoading, false)))

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

		// Watch for EditorService changes (mode, sortConfig, filterConfig) and update filteredTasksByColumn
		// Merge the change streams so any change triggers an update
		const modeChanges = editorService.mode.changes
		const sortConfigChanges = editorService.sortConfig.changes
		const filterConfigChanges = editorService.filterConfig.changes
		const editorChanges = Stream.merge(
			Stream.merge(modeChanges, sortConfigChanges),
			filterConfigChanges,
		)

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
			isLoading,

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
			 * as it applies the current search query, sort, and filter configuration.
			 */
			getFilteredTasksByColumn: (
				searchQuery: string,
				sortConfig: SortConfig,
				filterConfig: FilterConfig,
			): Effect.Effect<TaskWithSession[][]> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)

					return COLUMNS.map((col) => {
						// Filter by status
						const columnTasks = allTasks.filter((task) => task.status === col.status)
						// Apply search and filter config
						const filtered = filterTasks(columnTasks, searchQuery, filterConfig)
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
				filterConfig: FilterConfig,
			): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) {
						return undefined
					}

					const allTasks = yield* SubscriptionRef.get(tasks)
					const column = COLUMNS[columnIndex]!

					// Filter by status
					const columnTasks = allTasks.filter((task) => task.status === column.status)
					// Apply search and filter config
					const filtered = filterTasks(columnTasks, searchQuery, filterConfig)
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
