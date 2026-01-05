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
	Schedule,
	Stream,
	SubscriptionRef,
} from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { BeadsClient, type SyncRequiredError } from "../core/BeadsClient.js"
import { ClaudeSessionManager } from "../core/ClaudeSessionManager.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import { getWorktreePath } from "../core/paths.js"
import { WorktreeManager } from "../core/WorktreeManager.js"
import { emptyRecord } from "../lib/empty.js"
import type { ColumnStatus, GitStatus, PRState, TaskWithSession } from "../ui/types.js"
import { COLUMNS, parsePRInfo } from "../ui/types.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { EditorService, type FilterConfig, type SortConfig } from "./EditorService.js"
import { MutationQueue } from "./MutationQueue.js"
import { PRStateService } from "./PRStateService.js"
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
		case "crashed":
			return 5 // Show crashed prominently - needs attention
		case "done":
			return 6
		case "error":
			return 7
		case "idle":
			return 8
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
		// NOTE: Epic children filtering happens in drillDownFilteredTasksAtom, not here.
		// This allows drill-down mode to see epic children while main board hides them.
		// Age filter: show tasks not updated in N days
		if (config.updatedDaysAgo !== null) {
			const now = DateTime.unsafeNow()
			const taskUpdated = DateTime.unsafeMake(task.updated_at)
			const daysSinceUpdate = DateTime.distance(taskUpdated, now) / (1000 * 60 * 60 * 24)
			// Show only tasks where daysSinceUpdate >= config.updatedDaysAgo
			if (daysSinceUpdate < config.updatedDaysAgo) {
				return false
			}
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
// Cache Types
// ============================================================================

/** TTL for git status cache in milliseconds (10 seconds)
 * Must be longer than the 5-second polling interval so cache survives between polls */
const GIT_STATUS_CACHE_TTL_MS = 10000

/**
 * Cached git status entry with timestamp
 */
interface CachedGitStatus {
	readonly status: GitStatus & { hasMergeConflict: boolean }
	readonly timestamp: number
}

/**
 * Git status cache keyed by worktree path
 */
type GitStatusCache = Map<string, CachedGitStatus>

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

/**
 * Per-project board state
 *
 * Stores all board state for a specific project, allowing instant switching
 * between projects without losing state.
 */
export interface PerProjectBoardState {
	readonly tasks: ReadonlyArray<TaskWithSession>
	readonly tasksByColumn: Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
	readonly filteredTasksByColumn: TaskWithSession[][]
	readonly isLoading: boolean
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
		WorktreeManager.Default,
		PRStateService.Default,
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
		const worktreeManager = yield* WorktreeManager
		const prStateService = yield* PRStateService

		// Capture the service's scope for use in methods that spawn background fibers
		const serviceScope = yield* Effect.scope
		// Register with diagnostics
		yield* diagnostics.trackService("BoardService", "5s beads polling + session state merge")

		yield* diagnostics.trackService("BoardService", "Event-driven refresh with per-project cache")

		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
		>(emptyRecord())
		const isLoading = yield* SubscriptionRef.make<boolean>(false)
		const isRefreshingGitStats = yield* SubscriptionRef.make<boolean>(false)
		const filteredTasksByColumn = yield* SubscriptionRef.make<TaskWithSession[][]>(
			COLUMNS.map(() => []),
		)
		const boardCache = yield* Ref.make<Map<string, ReadonlyArray<TaskWithSession>>>(new Map())
		const debounceFiberRef = yield* Ref.make<Fiber.Fiber<void, never> | null>(null)

		// ====================================================================
		// Per-Project State Management
		// ====================================================================

		/**
		 * Per-project board state storage
		 *
		 * Maps projectPath to full board state, enabling instant project switching
		 * without losing any state from the previous project.
		 */
		const perProjectState = yield* SubscriptionRef.make<Map<string, PerProjectBoardState>>(
			new Map(),
		)

		/**
		 * Currently active project path
		 *
		 * Used to route session state updates to the correct project's state.
		 */
		const currentProjectPath = yield* SubscriptionRef.make<string | null>(null)

		/**
		 * Get default empty board state for a new project
		 */
		const getDefaultBoardState = (): PerProjectBoardState => ({
			tasks: [],
			tasksByColumn: emptyRecord() as Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>,
			filteredTasksByColumn: COLUMNS.map(() => []),
			isLoading: false,
		})

		/**
		 * Get or create per-project state for a given project path
		 */
		const getOrCreateProjectState = (projectPath: string) =>
			Effect.gen(function* () {
				const stateMap = yield* SubscriptionRef.get(perProjectState)
				if (stateMap.has(projectPath)) {
					return stateMap.get(projectPath)!
				}
				const newState = getDefaultBoardState()
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					copy.set(projectPath, newState)
					return copy
				})
				return newState
			})

		/**
		 * Save current derived SubscriptionRef state to the per-project map
		 */
		const saveCurrentToMap = () =>
			Effect.gen(function* () {
				const path = yield* SubscriptionRef.get(currentProjectPath)
				if (!path) return
				const state: PerProjectBoardState = {
					tasks: yield* SubscriptionRef.get(tasks),
					tasksByColumn: yield* SubscriptionRef.get(tasksByColumn),
					filteredTasksByColumn: yield* SubscriptionRef.get(filteredTasksByColumn),
					isLoading: yield* SubscriptionRef.get(isLoading),
				}
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					copy.set(path, state)
					return copy
				})
			})

		/**
		 * Sync derived SubscriptionRefs from a project's stored state
		 */
		const _syncDerivedFromProject = (projectPath: string) =>
			Effect.gen(function* () {
				const state = yield* getOrCreateProjectState(projectPath)
				yield* SubscriptionRef.set(tasks, state.tasks)
				yield* SubscriptionRef.set(tasksByColumn, state.tasksByColumn)
				yield* SubscriptionRef.set(filteredTasksByColumn, state.filteredTasksByColumn)
				yield* SubscriptionRef.set(isLoading, state.isLoading)
			})

		/**
		 * Update a specific project's task session state in the per-project map
		 *
		 * This is called by session state updates (from TmuxSessionMonitor) to ensure
		 * session state changes are recorded in the correct project's state, even if
		 * that project is not currently active.
		 */
		const updateProjectTaskSessionState = (
			projectPath: string,
			beadId: string,
			sessionState: TaskWithSession["sessionState"],
		) =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					const existing = copy.get(projectPath)
					if (!existing) return copy // Project not loaded yet, skip

					const updatedTasks = existing.tasks.map((t) =>
						t.id === beadId ? { ...t, sessionState } : t,
					)
					const updatedTasksByColumn = groupTasksByColumn(updatedTasks)
					copy.set(projectPath, {
						...existing,
						tasks: updatedTasks,
						tasksByColumn: updatedTasksByColumn,
					})
					return copy
				})
			})

		// Git status cache to avoid redundant git commands
		const gitStatusCache = yield* Ref.make<GitStatusCache>(new Map())

		// Parent epic map cache - rarely changes, so cache for longer (30 seconds)
		// This avoids the expensive batch bd show call on every refresh
		// Now supports multiple projects for fast project switching
		const PARENT_EPIC_CACHE_TTL_MS = 30000
		interface ParentEpicCacheEntry {
			readonly map: Map<string, string | undefined>
			readonly timestamp: number
		}
		// Map from projectPath to cache entry (supports multiple projects)
		const parentEpicCacheRef = yield* Ref.make<Map<string, ParentEpicCacheEntry>>(new Map())

		/**
		 * Get git status with caching
		 *
		 * Returns cached result if within TTL, otherwise fetches fresh status
		 * and updates the cache.
		 */
		const getCachedGitStatus = (
			worktreePath: string,
			baseBranch: string,
			showLineChanges: boolean,
		) =>
			Effect.gen(function* () {
				const now = Date.now()
				const cache = yield* Ref.get(gitStatusCache)
				// Include baseBranch in cache key since git diff results depend on it
				const cacheKey = `${worktreePath}:${baseBranch}`
				const cached = cache.get(cacheKey)

				// Return cached value if still valid
				if (cached && now - cached.timestamp < GIT_STATUS_CACHE_TTL_MS) {
					return cached.status
				}

				// Fetch fresh status
				const [mergeConflict, status] = yield* Effect.all([
					checkMergeConflict(worktreePath),
					checkGitStatus(worktreePath, baseBranch, showLineChanges),
				])

				const result = { ...status, hasMergeConflict: mergeConflict }

				// Update cache
				yield* Ref.update(gitStatusCache, (c) => {
					const newCache = new Map(c)
					newCache.set(cacheKey, { status: result, timestamp: now })
					return newCache
				})

				return result
			})

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
					// Get merge-base first for consistent comparison with DiffService
					const mergeBaseCommand = Command.make(
						"git",
						"-C",
						worktreePath,
						"merge-base",
						baseBranch,
						"HEAD",
					).pipe(Command.string)

					const mergeBase = yield* mergeBaseCommand.pipe(
						Effect.map((output) => output.trim()),
						Effect.catchAll(() => Effect.succeed(baseBranch)), // Fallback to branch name
					)

					// Use merge-base for accurate diff stats (matches DiffService.getChangedFiles)
					// Excludes .beads/ directory - users care about code changes, not beads metadata
					const diffCommand = Command.make(
						"git",
						"-C",
						worktreePath,
						"diff",
						"--numstat",
						mergeBase,
						"HEAD",
						"--",
						":^.beads",
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
				const loadStartTime = Date.now()
				const projectPath = yield* projectService.getCurrentPath()
				const gitConfig = yield* appConfig.getGitConfig()
				const { baseBranch, showLineChanges } = gitConfig

				const issues = yield* beadsClient.list(undefined, projectPath)
				yield* Effect.log(
					`loadTasks: ${issues.length} issues fetched in ${Date.now() - loadStartTime}ms`,
				)
				const activeSessions = yield* sessionManager.listActive(projectPath ?? undefined)
				const sessionMap = new Map(activeSessions.map((session) => [session.beadId, session]))

				// Auto-recovery of crashed sessions (if enabled)
				const crashedSessions = activeSessions.filter((s) => s.state === "crashed")
				if (crashedSessions.length > 0) {
					const recoveryConfig = yield* appConfig.getSessionRecoveryConfig()

					if (recoveryConfig.mode === "auto") {
						yield* Effect.log(
							`Auto-recovering ${crashedSessions.length} crashed session(s) in ${recoveryConfig.autoRecoveryDelayMs}ms...`,
						)

						// Fork recovery to run in background after delay
						// This lets the UI render immediately while recovery happens
						yield* Effect.fork(
							Effect.gen(function* () {
								yield* Effect.sleep(recoveryConfig.autoRecoveryDelayMs)

								for (const session of crashedSessions) {
									yield* sessionManager.recoverSession(session.beadId).pipe(
										Effect.tap(() => Effect.log(`Auto-recovered session for ${session.beadId}`)),
										Effect.catchAll((error) =>
											Effect.log(`Failed to auto-recover ${session.beadId}: ${error._tag}`),
										),
									)
								}

								yield* Effect.log(
									`Auto-recovery complete: ${crashedSessions.length} session(s) processed`,
								)
							}),
						)
					} else {
						yield* Effect.log(
							`${crashedSessions.length} crashed session(s) detected. Manual recovery mode - use R to recover.`,
						)
					}
				}

				const allMetrics = yield* SubscriptionRef.get(ptyMonitor.metrics)

				// Get optimistic mutations
				const pendingMutations = yield* mutationQueue.getMutations()

				// Get parent epic map (cached for 30s to avoid expensive bd show calls)
				// This enables filtering epic children and using correct base branch for git diff
				// Cache supports multiple projects for fast project switching
				const batchStartTime = Date.now()
				let parentEpicMap: Map<string, string | undefined>
				let cacheStatus = "miss"

				// Check if we have a valid cached parent epic map for this project
				const allCachedParentEpics = yield* Ref.get(parentEpicCacheRef)
				const now = Date.now()
				const normalizedProjectPath = projectPath ?? ""
				const cachedEntry = allCachedParentEpics.get(normalizedProjectPath)

				if (cachedEntry && now - cachedEntry.timestamp < PARENT_EPIC_CACHE_TTL_MS) {
					// Cache hit - use cached map
					parentEpicMap = cachedEntry.map
					cacheStatus = "hit"
				} else {
					// Cache miss - fetch fresh data
					parentEpicMap = new Map<string, string | undefined>()
					const issuesWithDeps = issues.filter((issue) => (issue.dependency_count ?? 0) > 0)

					if (issuesWithDeps.length > 0) {
						// Single batched call to get all issues with their dependencies
						const issuesWithDepDetails = yield* beadsClient
							.showMultiple(
								issuesWithDeps.map((i) => i.id),
								projectPath,
							)
							.pipe(Effect.catchAll(() => Effect.succeed([])))

						// Extract parent epic IDs from dependencies
						for (const issue of issuesWithDepDetails) {
							const parentChildDep = issue.dependencies?.find(
								(dep) => dep.dependency_type === "parent-child" && dep.issue_type === "epic",
							)
							parentEpicMap.set(issue.id, parentChildDep?.id)
						}
					}

					// Cache the result (preserves cache for other projects)
					yield* Ref.update(parentEpicCacheRef, (cache) => {
						const newCache = new Map(cache)
						newCache.set(normalizedProjectPath, { map: parentEpicMap, timestamp: now })
						return newCache
					})
				}
				yield* Effect.log(
					`loadTasks: deps resolved in ${Date.now() - batchStartTime}ms (cache ${cacheStatus})`,
				)

				// Get all worktrees ONCE upfront instead of per-issue exists() calls
				// This eliminates 331 Effect operations → 1 operation
				const worktreeList = projectPath
					? yield* worktreeManager.list(projectPath).pipe(Effect.catchAll(() => Effect.succeed([])))
					: []
				const worktreeBeadIds = new Set(worktreeList.map((wt) => wt.beadId))

				const tasksWithNullable = yield* Effect.all(
					issues.map((issue) =>
						Effect.gen(function* () {
							const session = sessionMap.get(issue.id)
							const metricsOpt = HashMap.get(allMetrics, issue.id)
							const metrics = metricsOpt._tag === "Some" ? metricsOpt.value : {}
							const sessionState = session?.state ?? "idle"

							// Get parent epic ID (if this is an epic child)
							const parentEpicId = parentEpicMap.get(issue.id)

							// Check if worktree exists (using pre-fetched Set - instant lookup)
							const hasWorktree = worktreeBeadIds.has(issue.id)

							let hasMergeConflict = false
							let gitStatus: GitStatus = {}
							// Fetch git status if there's an active session OR if worktree exists
							if ((sessionState !== "idle" || hasWorktree) && projectPath) {
								const worktreePath = getWorktreePath(projectPath, issue.id)
								// Use parent epic branch as base for children, otherwise use config baseBranch
								// This ensures children show line changes relative to epic, not main
								const effectiveBaseBranch = parentEpicId ?? baseBranch
								// Use cached git status to avoid redundant git commands
								const cachedStatus = yield* getCachedGitStatus(
									worktreePath,
									effectiveBaseBranch,
									showLineChanges,
								)
								hasMergeConflict = cachedStatus.hasMergeConflict
								gitStatus = {
									gitBehindCount: cachedStatus.gitBehindCount,
									hasUncommittedChanges: cachedStatus.hasUncommittedChanges,
									gitAdditions: cachedStatus.gitAdditions,
									gitDeletions: cachedStatus.gitDeletions,
								}
							}

							// Parse PR info from notes field (fast, local-only)
							const prInfo = parsePRInfo(issue.notes)

							const baseTask: TaskWithSession = {
								...issue,
								sessionState,
								hasWorktree: hasWorktree || undefined,
								hasMergeConflict,
								parentEpicId,
								...gitStatus,
								...prInfo, // hasPR, prUrl, prNumber from notes field
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
					{ concurrency: 4 },
				)

				const tasksWithSession = tasksWithNullable.filter((t): t is TaskWithSession => t !== null)

				// Enrich tasks with PR state from gh CLI (batch fetch, cached)
				const tasksWithPRs = tasksWithSession.filter((t) => t.hasPR && t.prUrl)
				let prStateMap = new Map<string, PRState>()
				if (tasksWithPRs.length > 0 && projectPath) {
					const prInfos = tasksWithPRs.map((t) => ({ prUrl: t.prUrl!, beadId: t.id }))
					prStateMap = yield* prStateService
						.getPRStates(prInfos, projectPath)
						.pipe(Effect.catchAll(() => Effect.succeed(new Map<string, PRState>())))
					yield* Effect.log(
						`loadTasks: Fetched ${prStateMap.size}/${tasksWithPRs.length} PR states from gh CLI`,
					)
				}

				// Merge PR states into tasks
				const tasksWithPRState = tasksWithSession.map((task) => {
					const prState = prStateMap.get(task.id)
					return prState ? { ...task, prState } : task
				})

				// Debug: count tasks with parentEpicId set
				const tasksWithEpicParent = tasksWithPRState.filter((t) => t.parentEpicId !== undefined)
				if (tasksWithEpicParent.length > 0) {
					yield* Effect.logWarning(
						`loadTasks: ${tasksWithEpicParent.length} tasks have parentEpicId (will be hidden on main board). Sample: ${JSON.stringify(tasksWithEpicParent.slice(0, 3).map((t) => ({ id: t.id, parentEpicId: t.parentEpicId })))}`,
					)
				}

				// Debug: count by status
				const statusCounts = tasksWithPRState.reduce(
					(acc, t) => {
						acc[t.status] = (acc[t.status] || 0) + 1
						return acc
					},
					{} as Record<string, number>,
				)
				yield* Effect.log(
					`loadTasks: Complete in ${Date.now() - loadStartTime}ms. Total: ${tasksWithPRState.length}, by status: ${JSON.stringify(statusCounts)}`,
				)
				return tasksWithPRState
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
				const startProjectPath = (yield* projectService.getCurrentPath()) ?? null

				// Update currentProjectPath SubscriptionRef
				yield* SubscriptionRef.set(currentProjectPath, startProjectPath)

				const loadedTasks = yield* loadTasks()

				// Verify project hasn't changed during refresh (race condition guard)
				// If project changed, discard results to avoid showing wrong project's data
				const activeProjectPath = (yield* projectService.getCurrentPath()) ?? null
				if (startProjectPath !== activeProjectPath) {
					yield* Effect.log(
						`Refresh discarded: project changed from ${startProjectPath} to ${activeProjectPath}`,
					)
					return
				}

				yield* SubscriptionRef.set(tasks, loadedTasks)
				const grouped = groupTasksByColumn(loadedTasks)
				yield* SubscriptionRef.set(tasksByColumn, grouped)
				yield* updateFilteredTasks()

				// Save to per-project map for fast switching
				yield* saveCurrentToMap()

				yield* Effect.log(
					`refresh: State updated, ${loadedTasks.length} tasks now in SubscriptionRefs`,
				)
			}).pipe(Effect.ensuring(SubscriptionRef.set(isLoading, false)))

		/**
		 * Refresh with auto-recovery for database sync errors.
		 *
		 * If the beads database is out of sync with JSONL (common after git pull
		 * or when another worktree modifies issues), this will:
		 * 1. Detect the SyncRequiredError
		 * 2. Auto-run 'bd sync --import-only' to re-import JSONL
		 * 3. Retry the refresh
		 */
		const refreshWithRecovery = () =>
			refresh().pipe(
				Effect.catchIf(
					(error): error is SyncRequiredError => error._tag === "SyncRequiredError",
					() =>
						Effect.gen(function* () {
							yield* Effect.log(
								"Beads database out of sync, auto-recovering with 'bd sync --import-only'...",
							)
							const projectPath = yield* projectService.getCurrentPath()
							yield* beadsClient
								.syncImportOnly(projectPath ?? undefined)
								.pipe(
									Effect.catchAll((syncError) =>
										Effect.logError("Auto-sync recovery failed", String(syncError)),
									),
								)
							yield* Effect.log("Auto-sync complete, retrying refresh...")
							yield* refresh()
						}),
				),
			)

		const requestRefresh = () =>
			Effect.gen(function* () {
				const existingFiber = yield* Ref.get(debounceFiberRef)
				if (existingFiber) {
					yield* Fiber.interrupt(existingFiber)
				}
				// Fork into the service's scope (not daemon) so fiber is tied to service lifetime
				const fiber = yield* Effect.gen(function* () {
					yield* Effect.sleep("500 millis")
					yield* refreshWithRecovery().pipe(
						Effect.catchAllCause((cause) =>
							Effect.logError("BoardService debounced refresh failed", Cause.pretty(cause)).pipe(
								Effect.asVoid,
							),
						),
					)
				}).pipe(Effect.forkIn(serviceScope))
				yield* Ref.set(debounceFiberRef, fiber)
			})

		/**
		 * Refresh git stats (behind count, uncommitted changes, line additions/deletions)
		 * for all tasks with active sessions.
		 *
		 * This is a lightweight refresh that only updates git-related fields,
		 * avoiding a full board reload. Respects the `git.showLineChanges` config
		 * for line stat computation.
		 */
		const refreshGitStats = () =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(isRefreshingGitStats, true)

				const projectPath = yield* projectService.getCurrentPath()
				if (!projectPath) {
					return
				}

				const gitConfig = yield* appConfig.getGitConfig()
				const { baseBranch, showLineChanges } = gitConfig
				const currentTasks = yield* SubscriptionRef.get(tasks)

				// Update git stats for all tasks with active sessions
				const updatedTasks = yield* Effect.all(
					currentTasks.map((task) =>
						Effect.gen(function* () {
							// Only refresh for tasks with active sessions (they have worktrees)
							if (task.sessionState === "idle") {
								return task
							}
							const worktreePath = getWorktreePath(projectPath, task.id)
							// Use parent epic branch for children, otherwise config baseBranch
							const effectiveBaseBranch = task.parentEpicId ?? baseBranch
							const gitStatus = yield* checkGitStatus(
								worktreePath,
								effectiveBaseBranch,
								showLineChanges,
							)
							return { ...task, ...gitStatus }
						}),
					),
					{ concurrency: "unbounded" },
				)

				yield* SubscriptionRef.set(tasks, updatedTasks)
				yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(updatedTasks))
				yield* updateFilteredTasks()
			}).pipe(Effect.ensuring(SubscriptionRef.set(isRefreshingGitStats, false)))

		yield* refreshWithRecovery().pipe(
			Effect.catchAllCause((cause) =>
				Effect.logError("BoardService initial refresh failed", Cause.pretty(cause)).pipe(
					Effect.asVoid,
				),
			),
		)

		// Background polling fallback (every 5 seconds) to keep git stats fresh
		// This ensures data stays current even if event-driven refresh misses something
		const backgroundPollingFiber = yield* Effect.forkScoped(
			Effect.repeat(Schedule.spaced("5 seconds"))(
				refreshWithRecovery().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logError("BoardService background refresh failed", Cause.pretty(cause)).pipe(
							Effect.asVoid,
						),
					),
				),
			),
		)
		yield* diagnostics.registerFiber({
			id: "board-background-polling",
			name: "Board Background Polling",
			description: "Refreshes board every 5 seconds to keep git stats fresh",
			fiber: backgroundPollingFiber,
		})

		const ptyRefreshFiber = yield* Effect.forkScoped(
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
		yield* diagnostics.registerFiber({
			id: "board-pty-refresh",
			name: "Board PTY Refresh",
			description: "Triggers board refresh when PTY metrics change",
			fiber: ptyRefreshFiber,
		})

		const editorChanges = Stream.merge(
			Stream.merge(editorService.mode.changes, editorService.sortConfig.changes),
			editorService.filterConfig.changes,
		)

		const editorChangesFiber = yield* Effect.forkScoped(
			Stream.runForEach(editorChanges, () =>
				updateFilteredTasks().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logError("FilteredTasks update failed", Cause.pretty(cause)).pipe(Effect.asVoid),
					),
				),
			),
		)
		yield* diagnostics.registerFiber({
			id: "board-editor-changes",
			name: "Board Editor Changes",
			description: "Updates filtered tasks when mode/sort/filter changes",
			fiber: editorChangesFiber,
		})

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
		 * Switch to a new project with per-project state preservation.
		 *
		 * This method:
		 * 1. Saves the current project's state to the per-project map
		 * 2. Loads the new project's cached state (instant UI feedback)
		 * 3. Spawns a background refresh to get fresh data
		 * 4. Calls the onRefreshComplete callback after refresh (for state restoration)
		 *
		 * @param newProjectPath - Path to the new project
		 * @param onRefreshComplete - Callback effect to run after refresh completes (errors are caught and logged)
		 * @returns Whether cached data was loaded (for toast messaging)
		 */
		const switchToProject = <E>(
			newProjectPath: string,
			onRefreshComplete: Effect.Effect<void, E, never>,
		) =>
			Effect.gen(function* () {
				// Save current project state before switching
				yield* saveCurrentToMap()

				// Update the current project path
				yield* SubscriptionRef.set(currentProjectPath, newProjectPath)

				// Try to load from per-project state map first (fast path)
				const stateMap = yield* SubscriptionRef.get(perProjectState)
				const cachedState = stateMap.get(newProjectPath)

				let cacheHit = false
				if (cachedState && cachedState.tasks.length > 0) {
					// Clear git stats from cached tasks - they're stale and project-specific
					const tasksWithClearedGitStats = cachedState.tasks.map((task) => ({
						...task,
						gitBehindCount: undefined,
						hasUncommittedChanges: undefined,
						gitAdditions: undefined,
						gitDeletions: undefined,
					}))
					yield* SubscriptionRef.set(tasks, tasksWithClearedGitStats)
					yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(tasksWithClearedGitStats))
					yield* SubscriptionRef.set(filteredTasksByColumn, cachedState.filteredTasksByColumn)
					cacheHit = true
				} else {
					// Fall back to legacy boardCache
					const legacyCacheHit = yield* loadFromCache(newProjectPath)
					if (!legacyCacheHit) {
						yield* clearBoard()
					}
					cacheHit = legacyCacheHit
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
			isRefreshingGitStats,
			currentProjectPath,
			updateProjectTaskSessionState,
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
			refreshGitStats,
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
