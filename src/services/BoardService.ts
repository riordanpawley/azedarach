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
	Fiber,
	HashMap,
	Order,
	Record,
	Ref,
	Stream,
	SubscriptionRef,
} from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { BeadsClient } from "../core/BeadsClient.js"
import { ClaudeSessionManager } from "../core/ClaudeSessionManager.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import { getWorktreePath } from "../core/paths.js"
import { emptyRecord } from "../lib/empty.js"
import type { ColumnStatus, GitStatus, TaskWithSession } from "../ui/types.js"
import { COLUMNS } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { EditorService, type FilterConfig, type SortConfig } from "./EditorService.js"
import { MutationQueue } from "./MutationQueue.js"
import { ProjectService } from "./ProjectService.js"

// ============================================================================
// Sort Orders using Effect's composable Order module
// ============================================================================

const getSessionSortValue = (state: TaskWithSession["sessionState"]): number => {
	switch (state) {
		case "initializing":
			return 0
		case "busy":
			return 1
		case "warning":
			return 2
		case "waiting":
			return 3
		case "paused":
			return 4
		case "done":
			return 5
		case "error":
			return 6
		case "idle":
			return 7
		default:
			return 99
	}
}

const byHasActiveSession: Order.Order<TaskWithSession> = Order.mapInput(
	Order.boolean,
	(task: TaskWithSession) => task.sessionState === "idle",
)

const bySessionState: Order.Order<TaskWithSession> = Order.mapInput(
	Order.number,
	(task: TaskWithSession) => getSessionSortValue(task.sessionState),
)

const byPriority: Order.Order<TaskWithSession> = Order.mapInput(
	Order.number,
	(task: TaskWithSession) => task.priority,
)

const byUpdatedAt: Order.Order<TaskWithSession> = Order.mapInput(
	Order.number,
	(task: TaskWithSession) => new Date(task.updated_at).getTime(),
)

const buildSortOrder = (sortConfig: SortConfig): Order.Order<TaskWithSession> => {
	const applyDirection = (order: Order.Order<TaskWithSession>): Order.Order<TaskWithSession> =>
		sortConfig.direction === "desc" ? Order.reverse(order) : order

	switch (sortConfig.field) {
		case "session":
			return Order.combine(
				byHasActiveSession,
				Order.combine(
					applyDirection(bySessionState),
					Order.combine(Order.reverse(byUpdatedAt), byPriority),
				),
			)
		case "priority":
			return Order.combine(
				byHasActiveSession,
				Order.combine(
					applyDirection(byPriority),
					Order.combine(Order.reverse(byUpdatedAt), bySessionState),
				),
			)
		case "updated":
			return Order.combine(
				byHasActiveSession,
				Order.combine(
					applyDirection(Order.reverse(byUpdatedAt)),
					Order.combine(byPriority, bySessionState),
				),
			)
	}
}

const sortTasks = (tasks: TaskWithSession[], sortConfig: SortConfig): TaskWithSession[] => {
	const order = buildSortOrder(sortConfig)
	return Arr.sort(tasks, order)
}

const filterTasksByQuery = (tasks: TaskWithSession[], query: string): TaskWithSession[] => {
	if (!query) return tasks
	const lowerQuery = query.toLowerCase()
	return tasks.filter((task) => {
		const titleMatch = task.title.toLowerCase().includes(lowerQuery)
		const idMatch = task.id.toLowerCase().includes(lowerQuery)
		return titleMatch || idMatch
	})
}

const isEpicChild = (task: TaskWithSession): boolean => {
	if (!task.dependencies) return false
	return task.dependencies.some((dep) => dep.dependency_type === "parent-child")
}

const applyFilterConfig = (tasks: TaskWithSession[], config: FilterConfig): TaskWithSession[] => {
	return tasks.filter((task) => {
		if (config.status.size > 0) {
			const taskStatus = task.status
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
		if (config.priority.size > 0) {
			if (!config.priority.has(task.priority)) {
				return false
			}
		}
		if (config.type.size > 0) {
			if (!config.type.has(task.issue_type)) {
				return false
			}
		}
		if (config.session.size > 0) {
			const sessionState = task.sessionState === "warning" ? "idle" : task.sessionState
			if (
				sessionState !== "idle" &&
				sessionState !== "initializing" &&
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
		if (config.hideEpicSubtasks && isEpicChild(task)) {
			return false
		}
		return true
	})
}

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

export interface BoardState {
	readonly tasks: ReadonlyArray<TaskWithSession>
	readonly tasksByColumn: Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
}

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
		MutationQueue.Default,
	],
	scoped: Effect.gen(function* () {
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* ClaudeSessionManager
		const editorService = yield* EditorService
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig
		const mutationQueue = yield* MutationQueue

		// Capture the service's scope for use in methods that spawn background fibers
		const serviceScope = yield* Effect.scope

		yield* diagnostics.trackService("BoardService", "Event-driven refresh with per-project cache")

		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
		>(emptyRecord())
		const isLoading = yield* SubscriptionRef.make<boolean>(false)
		const filteredTasksByColumn = yield* SubscriptionRef.make<TaskWithSession[][]>(
			COLUMNS.map(() => []),
		)
		const boardCache = yield* Ref.make<Map<string, ReadonlyArray<TaskWithSession>>>(new Map())
		const debounceFiberRef = yield* Ref.make<Fiber.Fiber<void, never> | null>(null)

		const checkMergeConflict = (worktreePath: string) =>
			Effect.gen(function* () {
				const command = Command.make("git", "-C", worktreePath, "rev-parse", "MERGE_HEAD").pipe(
					Command.exitCode,
				)
				const exitCode = yield* command.pipe(Effect.catchAll(() => Effect.succeed(128)))
				return exitCode === 0
			})

		const checkGitStatus = (worktreePath: string, baseBranch: string, showLineChanges: boolean) =>
			Effect.gen(function* () {
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

				const dirtyCommand = Command.make("git", "-C", worktreePath, "status", "--porcelain").pipe(
					Command.string,
				)

				const hasUncommittedChanges = yield* dirtyCommand.pipe(
					Effect.map((output) => output.trim().length > 0),
					Effect.catchAll(() => Effect.succeed(false)),
				)

				let gitAdditions: number | undefined
				let gitDeletions: number | undefined

				if (showLineChanges) {
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

		const loadTasks = () =>
			Effect.gen(function* () {
				const projectPath = yield* projectService.getCurrentPath()
				const gitConfig = yield* appConfig.getGitConfig()
				const { baseBranch, showLineChanges } = gitConfig

				const issues = yield* beadsClient.list(undefined, projectPath)
				const activeSessions = yield* sessionManager.listActive(projectPath ?? undefined)
				const sessionMap = new Map(activeSessions.map((session) => [session.beadId, session]))
				const allMetrics = yield* SubscriptionRef.get(ptyMonitor.metrics)

				// Get optimistic mutations
				const pendingMutations = yield* mutationQueue.getMutations()

				const tasksWithNullable = yield* Effect.all(
					issues.map((issue) =>
						Effect.gen(function* () {
							const session = sessionMap.get(issue.id)
							const metricsOpt = HashMap.get(allMetrics, issue.id)
							const metrics = metricsOpt._tag === "Some" ? metricsOpt.value : {}
							const sessionState = session?.state ?? "idle"

							let hasMergeConflict = false
							let gitStatus: GitStatus = {}
							if (sessionState !== "idle" && projectPath) {
								const worktreePath = getWorktreePath(projectPath, issue.id)
								const [mergeConflict, status] = yield* Effect.all([
									checkMergeConflict(worktreePath),
									checkGitStatus(worktreePath, baseBranch, showLineChanges),
								])
								hasMergeConflict = mergeConflict
								gitStatus = status
							}

							const baseTask = {
								...issue,
								sessionState,
								hasMergeConflict,
								...gitStatus,
								sessionStartedAt: session?.startedAt
									? DateTime.formatIso(session.startedAt)
									: undefined,
								estimatedTokens: metrics.estimatedTokens,
								recentOutput: metrics.recentOutput,
								agentPhase: metrics.agentPhase,
							}

							// Apply optimistic updates
							const queuedMutation = pendingMutations.get(issue.id)
							if (queuedMutation) {
								const mutation = queuedMutation.mutation
								switch (mutation._tag) {
									case "Move":
										return { ...baseTask, status: mutation.status }
									case "Update":
										return { ...baseTask, ...mutation.fields }
									case "Delete":
										return null
								}
							}

							return baseTask
						}),
					),
					{ concurrency: "unbounded" },
				).pipe(
					Effect.map(
						(list) =>
							list.filter((t): t is NonNullable<typeof t> => t !== null) as TaskWithSession[],
					),
				)

				const tasksWithSession = tasksWithNullable.filter((t): t is TaskWithSession => t !== null)

				return tasksWithSession
			})

		const groupTasksByColumn = (
			taskList: ReadonlyArray<TaskWithSession>,
		): Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>> => {
			const initial: Record.ReadonlyRecord<
				string,
				ReadonlyArray<TaskWithSession>
			> = Record.fromEntries(COLUMNS.map((col) => [col.status, [] as TaskWithSession[]]))

			return taskList.reduce(
				(acc, task) => Record.set(acc, task.status, [...(acc[task.status] ?? []), task]),
				initial,
			)
		}

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

		const refresh = () =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(isLoading, true)

				// Capture project path at refresh START
				const startProjectPath = yield* projectService.getCurrentPath()

				const loadedTasks = yield* loadTasks()

				// Verify project hasn't changed during refresh (race condition guard)
				// If project changed, discard results to avoid showing wrong project's data
				const currentProjectPath = yield* projectService.getCurrentPath()
				if (startProjectPath !== currentProjectPath) {
					yield* Effect.log(
						`Refresh discarded: project changed from ${startProjectPath} to ${currentProjectPath}`,
					)
					return
				}

				yield* SubscriptionRef.set(tasks, loadedTasks)
				const grouped = groupTasksByColumn(loadedTasks)
				yield* SubscriptionRef.set(tasksByColumn, grouped)
				yield* updateFilteredTasks()
			}).pipe(Effect.ensuring(SubscriptionRef.set(isLoading, false)))

		const requestRefresh = () =>
			Effect.gen(function* () {
				const existingFiber = yield* Ref.get(debounceFiberRef)
				if (existingFiber) {
					yield* Fiber.interrupt(existingFiber)
				}
				// Fork into the service's scope (not daemon) so fiber is tied to service lifetime
				const fiber = yield* Effect.gen(function* () {
					yield* Effect.sleep("500 millis")
					yield* refresh().pipe(
						Effect.catchAllCause((cause) =>
							Effect.logError("BoardService debounced refresh failed", Cause.pretty(cause)).pipe(
								Effect.asVoid,
							),
						),
					)
				}).pipe(Effect.forkIn(serviceScope))
				yield* Ref.set(debounceFiberRef, fiber)
			})

		yield* refresh().pipe(
			Effect.catchAllCause((cause) =>
				Effect.logError("BoardService initial refresh failed", Cause.pretty(cause)).pipe(
					Effect.asVoid,
				),
			),
		)

		yield* Effect.forkScoped(
			Stream.runForEach(ptyMonitor.metrics.changes, () =>
				requestRefresh().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logError("PTY-triggered refresh failed", Cause.pretty(cause)).pipe(
							Effect.asVoid,
						),
					),
				),
			),
		)

		const editorChanges = Stream.merge(
			Stream.merge(editorService.mode.changes, editorService.sortConfig.changes),
			editorService.filterConfig.changes,
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

		const saveToCache = (projectPath: string) =>
			Effect.gen(function* () {
				const currentTasks = yield* SubscriptionRef.get(tasks)
				if (currentTasks.length > 0) {
					yield* Ref.update(boardCache, (cache) => {
						const newCache = new Map(cache)
						newCache.set(projectPath, currentTasks)
						return newCache
					})
				}
			})

		const loadFromCache = (projectPath: string) =>
			Effect.gen(function* () {
				const cache = yield* Ref.get(boardCache)
				const cached = cache.get(projectPath)
				if (cached && cached.length > 0) {
					// Clear git stats from cached tasks - they're stale and project-specific
					const tasksWithClearedGitStats = cached.map((task) => ({
						...task,
						gitBehindCount: undefined,
						hasUncommittedChanges: undefined,
						gitAdditions: undefined,
						gitDeletions: undefined,
					}))
					yield* SubscriptionRef.set(tasks, tasksWithClearedGitStats)
					yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(tasksWithClearedGitStats))
					yield* updateFilteredTasks()
					return true
				}
				return false
			})

		const clearBoard = () =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(tasks, [])
				yield* SubscriptionRef.set(tasksByColumn, emptyRecord())
				yield* SubscriptionRef.set(
					filteredTasksByColumn,
					COLUMNS.map(() => []),
				)
			})

		/**
		 * Apply an optimistic move directly to in-memory state.
		 * This provides instant UI feedback without waiting for refresh.
		 */
		const applyOptimisticMove = (taskId: string, newStatus: ColumnStatus) =>
			Effect.gen(function* () {
				// Update tasks
				yield* SubscriptionRef.update(tasks, (currentTasks) =>
					currentTasks.map((task) => (task.id === taskId ? { ...task, status: newStatus } : task)),
				)
				// Update tasksByColumn
				yield* SubscriptionRef.update(tasksByColumn, (current) => {
					const allTasks: TaskWithSession[] = Object.values(current).flat()
					const updatedTasks = allTasks.map((task) =>
						task.id === taskId ? { ...task, status: newStatus } : task,
					)
					return groupTasksByColumn(updatedTasks)
				})
				// Update filtered view
				yield* updateFilteredTasks()
			})

		/**
		 * Switch to a new project with proper fiber management.
		 *
		 * This method:
		 * 1. Immediately loads cached data or clears the board (fast UI feedback)
		 * 2. Spawns a scoped refresh fiber that lives for BoardService's lifetime
		 * 3. Calls the onRefreshComplete callback after refresh (for state restoration)
		 *
		 * @param projectPath - Path to the new project
		 * @param onRefreshComplete - Callback effect to run after refresh completes (errors are caught and logged)
		 * @returns Whether cached data was loaded (for toast messaging)
		 */
		const switchToProject = <E>(
			projectPath: string,
			onRefreshComplete: Effect.Effect<void, E, never>,
		) =>
			Effect.gen(function* () {
				const cacheHit = yield* loadFromCache(projectPath)
				if (!cacheHit) {
					yield* clearBoard()
				}

				// Fork the refresh into the service's scope - not a daemon fiber
				yield* Effect.gen(function* () {
					yield* refresh()
					yield* onRefreshComplete
				}).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logError("Project switch refresh failed", Cause.pretty(cause)).pipe(
							Effect.asVoid,
						),
					),
					Effect.forkIn(serviceScope),
				)

				return { cacheHit }
			})

		return {
			tasks,
			tasksByColumn,
			filteredTasksByColumn,
			isLoading,
			getTasks: (): Effect.Effect<ReadonlyArray<TaskWithSession>> => SubscriptionRef.get(tasks),
			getTasksByColumn: (): Effect.Effect<
				Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
			> => SubscriptionRef.get(tasksByColumn),
			getColumnTasks: (columnIndex: number): Effect.Effect<ReadonlyArray<TaskWithSession>> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) return []
					const column = COLUMNS[columnIndex]!
					const grouped = yield* SubscriptionRef.get(tasksByColumn)
					return grouped[column.status] ?? []
				}),
			getTaskAt: (
				columnIndex: number,
				taskIndex: number,
			): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) return undefined
					const column = COLUMNS[columnIndex]!
					const grouped = yield* SubscriptionRef.get(tasksByColumn)
					const columnTasks = grouped[column.status] ?? []
					return columnTasks[taskIndex]
				}),
			findTaskById: (taskId: string): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)
					return allTasks.find((task) => task.id === taskId)
				}),
			findTaskPosition: (
				taskId: string,
			): Effect.Effect<{ columnIndex: number; taskIndex: number } | undefined> =>
				Effect.gen(function* () {
					const grouped = yield* SubscriptionRef.get(tasksByColumn)
					for (let colIndex = 0; colIndex < COLUMNS.length; colIndex++) {
						const column = COLUMNS[colIndex]!
						const columnTasks = grouped[column.status] ?? []
						const taskIndex = columnTasks.findIndex((task) => task.id === taskId)
						if (taskIndex !== -1) return { columnIndex: colIndex, taskIndex }
					}
					return undefined
				}),
			getColumnInfo: (columnIndex: number): Effect.Effect<ColumnInfo | undefined> =>
				Effect.succeed(
					columnIndex < 0 || columnIndex >= COLUMNS.length ? undefined : COLUMNS[columnIndex]!,
				),
			getColumnCount: (): Effect.Effect<number> => Effect.succeed(COLUMNS.length),
			refresh,
			requestRefresh,
			clearBoard,
			saveToCache,
			loadFromCache,
			switchToProject,
			applyOptimisticMove,
			initialize: refresh,
			getFilteredTasksByColumn: (
				searchQuery: string,
				sortConfig: SortConfig,
				filterConfig: FilterConfig,
			): Effect.Effect<TaskWithSession[][]> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)
					return COLUMNS.map((col) => {
						const columnTasks = allTasks.filter((task) => task.status === col.status)
						const filtered = filterTasks(columnTasks, searchQuery, filterConfig)
						return sortTasks(filtered, sortConfig)
					})
				}),
			getFilteredTaskAt: (
				columnIndex: number,
				taskIndex: number,
				searchQuery: string,
				sortConfig: SortConfig,
				filterConfig: FilterConfig,
			): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					if (columnIndex < 0 || columnIndex >= COLUMNS.length) return undefined
					const allTasks = yield* SubscriptionRef.get(tasks)
					const column = COLUMNS[columnIndex]!
					const columnTasks = allTasks.filter((task) => task.status === column.status)
					const filtered = filterTasks(columnTasks, searchQuery, filterConfig)
					const sorted = sortTasks(filtered, sortConfig)
					return sorted[taskIndex]
				}),
		}
	}),
}) {}
