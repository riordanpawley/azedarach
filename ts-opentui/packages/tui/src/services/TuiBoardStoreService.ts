import { AppConfigProjectContext } from "@azedarach/config"
import { resolveBaseProjectPath, resolveEffectiveProjectPath } from "@azedarach/shared/project-path"
import {
	type DaemonBoardTask,
	DaemonRpcClient,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import {
	Array as Arr,
	Cause,
	DateTime,
	Deferred,
	Effect,
	Option,
	Order,
	Queue,
	Ref,
	Stream,
	SubscriptionRef,
} from "effect"
import type { Issue } from "../contracts.js"
import type { ColumnStatus, TaskWithSession } from "../types.js"
import { COLUMNS, parsePRInfo } from "../types.js"
import { BoardRefreshDaemonRpcClient } from "./BoardRefreshDaemonRpcClient.js"
import { EditorService, type FilterConfig, type SortConfig } from "./EditorService.js"

const LOCAL_CREATE_VISIBILITY_GRACE_MS = 15_000
const EMPTY_FILTERED_TASKS_BY_COLUMN: readonly (readonly TaskWithSession[])[] = [[], [], [], []]

interface TasksByColumn {
	readonly open: readonly TaskWithSession[]
	readonly in_progress: readonly TaskWithSession[]
	readonly blocked: readonly TaskWithSession[]
	readonly closed: readonly TaskWithSession[]
}

export interface MutationTaskUpsertOptions {
	readonly parentEpicId?: string | null
}

export interface TuiBoardStoreServiceApi {
	readonly tasks: SubscriptionRef.SubscriptionRef<readonly TaskWithSession[]>
	readonly tasksByColumn: SubscriptionRef.SubscriptionRef<TasksByColumn>
	readonly filteredTasksByColumn: SubscriptionRef.SubscriptionRef<
		readonly (readonly TaskWithSession[])[]
	>
	readonly isLoading: SubscriptionRef.SubscriptionRef<boolean>
	readonly isRefreshingGitStats: SubscriptionRef.SubscriptionRef<boolean>
	readonly currentProjectPath: SubscriptionRef.SubscriptionRef<string | null>
	readonly refresh: () => Effect.Effect<void, DaemonRpcClientError>
	readonly refreshGitStats: () => Effect.Effect<void, DaemonRpcClientError>
	readonly setVisibleTaskIds: (taskIds: ReadonlySet<string>) => Effect.Effect<void>
	readonly getTasks: () => Effect.Effect<readonly TaskWithSession[]>
	readonly getFilteredTasksByColumn: (
		searchQuery: string,
		sortConfig: SortConfig,
		filterConfig: FilterConfig,
	) => Effect.Effect<readonly (readonly TaskWithSession[])[]>
	readonly findTaskById: (taskId: string) => Effect.Effect<TaskWithSession | undefined>
	readonly applyOptimisticMove: (taskId: string, newStatus: ColumnStatus) => Effect.Effect<void>
	readonly removeTaskFromMutation: (taskId: string) => Effect.Effect<void>
	readonly patchTaskFromMutation: (
		taskId: string,
		patch: Partial<Omit<TaskWithSession, "id">>,
	) => Effect.Effect<void>
	readonly upsertIssueFromMutation: (
		issue: Issue,
		options?: MutationTaskUpsertOptions,
	) => Effect.Effect<void>
	readonly syncTaskFromBackend: (
		taskId: string,
		options?: MutationTaskUpsertOptions,
	) => Effect.Effect<void, DaemonRpcClientError>
	readonly saveToCache: (projectPath: string) => Effect.Effect<void>
	readonly switchToProject: <E>(
		newProjectPath: string,
		onRefreshComplete: Effect.Effect<void, E, never>,
	) => Effect.Effect<{ readonly cacheHit: boolean; readonly refreshFailed: boolean }>
}

const emptyTasksByColumn = (): TasksByColumn => ({
	open: [],
	in_progress: [],
	blocked: [],
	closed: [],
})

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
			return 5
		case "done":
			return 6
		case "error":
			return 7
		case "idle":
			return 8
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
	(task: TaskWithSession) => Date.parse(task.updated_at),
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

const sortTasks = (
	tasks: readonly TaskWithSession[],
	sortConfig: SortConfig,
): readonly TaskWithSession[] => Arr.sort([...tasks], buildSortOrder(sortConfig))

const filterTasksByQuery = (
	tasks: readonly TaskWithSession[],
	query: string,
): readonly TaskWithSession[] => {
	if (query.length === 0) {
		return tasks
	}

	const lowerQuery = query.toLowerCase()
	return tasks.filter((task) => {
		const titleMatch = task.title.toLowerCase().includes(lowerQuery)
		const idMatch = task.id.toLowerCase().includes(lowerQuery)
		return titleMatch || idMatch
	})
}

const applyFilterConfig = (
	tasks: readonly TaskWithSession[],
	config: FilterConfig,
): readonly TaskWithSession[] =>
	tasks.filter((task) => {
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
		if (config.priority.size > 0 && !config.priority.has(task.priority)) {
			return false
		}
		if (config.type.size > 0 && !config.type.has(task.issue_type)) {
			return false
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
		if (config.updatedDaysAgo !== null) {
			const now = DateTime.unsafeNow()
			const taskUpdated = DateTime.unsafeMake(task.updated_at)
			const daysSinceUpdate = DateTime.distance(taskUpdated, now) / (1000 * 60 * 60 * 24)
			if (daysSinceUpdate < config.updatedDaysAgo) {
				return false
			}
		}
		return true
	})

const filterTasks = (
	tasks: readonly TaskWithSession[],
	query: string,
	filterConfig: FilterConfig,
): readonly TaskWithSession[] => applyFilterConfig(filterTasksByQuery(tasks, query), filterConfig)

const groupTasksByColumn = (tasks: readonly TaskWithSession[]): TasksByColumn => ({
	open: tasks.filter((task) => task.status === "open"),
	in_progress: tasks.filter((task) => task.status === "in_progress"),
	blocked: tasks.filter((task) => task.status === "blocked"),
	closed: tasks.filter((task) => task.status === "closed"),
})

const computeFilteredTasksByColumn = (
	tasks: readonly TaskWithSession[],
	searchQuery: string,
	sortConfig: SortConfig,
	filterConfig: FilterConfig,
): readonly (readonly TaskWithSession[])[] =>
	COLUMNS.map((column) =>
		sortTasks(
			filterTasks(
				tasks.filter((task) => task.status === column.status),
				searchQuery,
				filterConfig,
			),
			sortConfig,
		),
	)

const hasParentChildDependents = (issue: Issue): boolean =>
	(issue.dependents ?? []).some((dependency) => dependency.dependency_type === "parent-child")

const toBoardIssueType = (issue: Issue): Issue["issue_type"] =>
	(issue.dependent_count ?? 0) > 0 || hasParentChildDependents(issue) ? "epic" : issue.issue_type

const mergeLoadedTasksWithLocalCreateGrace = (params: {
	readonly loadedTasks: readonly TaskWithSession[]
	readonly currentTasks: readonly TaskWithSession[]
	readonly localCreateGraceExpiries: ReadonlyMap<string, number>
	readonly nowMs: number
}): {
	readonly mergedTasks: readonly TaskWithSession[]
	readonly nextLocalCreateGraceExpiries: ReadonlyMap<string, number>
} => {
	const loadedTaskIds = new Set(params.loadedTasks.map((task) => task.id))
	const retainedTasks = params.currentTasks.filter((task) => {
		const expiry = params.localCreateGraceExpiries.get(task.id)
		return expiry !== undefined && expiry > params.nowMs && !loadedTaskIds.has(task.id)
	})

	const mergedTasks = [...params.loadedTasks, ...retainedTasks].sort(
		(left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at),
	)

	const nextLocalCreateGraceExpiries = new Map<string, number>()
	for (const [taskId, expiry] of params.localCreateGraceExpiries.entries()) {
		if (expiry <= params.nowMs || loadedTaskIds.has(taskId)) {
			continue
		}
		nextLocalCreateGraceExpiries.set(taskId, expiry)
	}

	return { mergedTasks, nextLocalCreateGraceExpiries }
}

const toTaskFromDaemonBoardTask = (task: DaemonBoardTask): TaskWithSession => ({
	...task,
	hasPR: task.hasPR === true ? true : undefined,
})

const toTaskFromIssueListItem = (issue: Issue): TaskWithSession => {
	const prInfo = parsePRInfo(issue.notes)
	return {
		...issue,
		issue_type: toBoardIssueType(issue),
		sessionState: "idle",
		hasPR: prInfo.hasPR === true ? true : undefined,
		prUrl: prInfo.prUrl,
		prNumber: prInfo.prNumber,
	}
}

const resolveBoardProjectPath = (projectPath: string | null | undefined): Effect.Effect<string> =>
	resolveBaseProjectPath(resolveEffectiveProjectPath(projectPath))

export class TuiBoardStoreService extends Effect.Service<TuiBoardStoreService>()(
	"TuiBoardStoreService",
	{
		dependencies: [EditorService.Default],
		scoped: Effect.gen(function* () {
			const daemonRpcClient = yield* DaemonRpcClient
			const boardRefreshRpcClient = yield* Effect.serviceOption(BoardRefreshDaemonRpcClient).pipe(
				Effect.map((client) => Option.getOrElse(client, () => daemonRpcClient)),
			)
			const projectContext = yield* AppConfigProjectContext
			const editorService = yield* EditorService
			const serviceScope = yield* Effect.scope

			const tasks = yield* SubscriptionRef.make<readonly TaskWithSession[]>([])
			const tasksByColumn = yield* SubscriptionRef.make<TasksByColumn>(emptyTasksByColumn())
			const filteredTasksByColumn = yield* SubscriptionRef.make<
				readonly (readonly TaskWithSession[])[]
			>(EMPTY_FILTERED_TASKS_BY_COLUMN)
			const isLoading = yield* SubscriptionRef.make(false)
			const isRefreshingGitStats = yield* SubscriptionRef.make(false)
			const currentProjectPath = yield* SubscriptionRef.make<string | null>(null)
			const visibleTaskIds = yield* Ref.make<ReadonlySet<string>>(new Set())
			const localCreateGraceExpiries = yield* Ref.make<ReadonlyMap<string, number>>(new Map())
			const boardCache = yield* Ref.make<ReadonlyMap<string, readonly TaskWithSession[]>>(new Map())
			const switchRefreshQueue = yield* Queue.unbounded<{
				readonly projectPath: string
				readonly done: Deferred.Deferred<{ readonly refreshFailed: boolean }>
			}>()

			const updateFilteredTasks = () =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)
					const mode = yield* editorService.getMode()
					const sortConfig = yield* editorService.getSortConfig()
					const filterConfig = yield* editorService.getFilterConfig()
					const searchQuery = mode._tag === "search" ? mode.query : ""
					yield* SubscriptionRef.set(
						filteredTasksByColumn,
						computeFilteredTasksByColumn(allTasks, searchQuery, sortConfig, filterConfig),
					)
				})

			const replaceTasks = (nextTasks: readonly TaskWithSession[]) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(tasks, nextTasks)
					yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(nextTasks))
					yield* updateFilteredTasks()
				})

			const refreshForProjectPath = (projectPath: string) =>
				Effect.gen(function* () {
					const resolvedProjectPath = yield* resolveBoardProjectPath(projectPath)
					yield* SubscriptionRef.set(isLoading, true)

					const currentTasks = yield* SubscriptionRef.get(tasks)
					const loadStreamedTasks = boardRefreshRpcClient
						.boardReadModel({
							projectPath: resolvedProjectPath,
						})
						.pipe(
							Stream.map(toTaskFromDaemonBoardTask),
							Stream.runCollect,
							Effect.map((chunks) => Array.from(chunks)),
							Effect.catchAllCause(() => Effect.succeed([])),
						)
					let streamedTasks = yield* loadStreamedTasks
					if (streamedTasks.length === 0) {
						yield* Effect.sleep("150 millis")
						streamedTasks = yield* loadStreamedTasks
					}
					const toIssueListTasks = (result: {
						readonly issues: ReadonlyArray<Issue>
					}): ReadonlyArray<TaskWithSession> => result.issues.map(toTaskFromIssueListItem)
					const listViaDaemon = (request: {
						readonly projectPath: string
						readonly options?: {
							readonly includeClosed?: boolean
							readonly sortBy?: "updated_at" | "created_at"
							readonly sortDirection?: "asc" | "desc"
							readonly limit?: number
						}
					}) =>
						boardRefreshRpcClient.issueListStream === undefined
							? boardRefreshRpcClient.issueList(request).pipe(Effect.map(toIssueListTasks))
							: boardRefreshRpcClient.issueListStream(request).pipe(
									Stream.map(toTaskFromIssueListItem),
									Stream.runCollect,
									Effect.map((chunks) => Array.from(chunks)),
								)
					const listWithOptions = listViaDaemon({
						projectPath: resolvedProjectPath,
						options: {
							includeClosed: true,
							sortBy: "updated_at",
							sortDirection: "desc",
							limit: 1000,
						},
					}).pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(
								`[board-refresh] issueList(options) project=${resolvedProjectPath} cause=${Cause.pretty(cause)}`,
							).pipe(Effect.zipRight(Effect.succeed([]))),
						),
					)
					const listWithoutOptions = listViaDaemon({ projectPath: resolvedProjectPath }).pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(
								`[board-refresh] issueList(default) project=${resolvedProjectPath} cause=${Cause.pretty(cause)}`,
							).pipe(Effect.zipRight(Effect.succeed([]))),
						),
					)
					const listedTasks =
						streamedTasks.length > 0
							? yield* listWithOptions.pipe(
									Effect.flatMap((tasksFromOptions) =>
										tasksFromOptions.length > 0
											? Effect.succeed(tasksFromOptions)
											: listWithoutOptions,
									),
								)
							: yield* listWithoutOptions.pipe(
									Effect.flatMap((tasksFromDefaultQuery) =>
										tasksFromDefaultQuery.length > 0
											? Effect.succeed(tasksFromDefaultQuery)
											: listWithOptions,
									),
								)
					const streamedById = new Map(streamedTasks.map((task) => [task.id, task] as const))
					const loadedTasks =
						listedTasks.length > 0
							? listedTasks.map((listedTask) => {
									const streamedTask = streamedById.get(listedTask.id)
									if (streamedTask === undefined) {
										return listedTask
									}
									return {
										...listedTask,
										sessionState: streamedTask.sessionState,
										hasTmuxSession: streamedTask.hasTmuxSession,
										hasWorktree: streamedTask.hasWorktree,
										hasMergeConflict: streamedTask.hasMergeConflict,
										hasDevServer: streamedTask.hasDevServer,
										parentEpicId: streamedTask.parentEpicId,
										gitBehindCount: streamedTask.gitBehindCount,
										hasUncommittedChanges: streamedTask.hasUncommittedChanges,
										gitAdditions: streamedTask.gitAdditions,
										gitDeletions: streamedTask.gitDeletions,
										sessionStartedAt: streamedTask.sessionStartedAt,
										estimatedTokens: streamedTask.estimatedTokens,
										recentOutput: streamedTask.recentOutput,
										agentPhase: streamedTask.agentPhase,
										hasPR: streamedTask.hasPR,
										prUrl: streamedTask.prUrl,
										prNumber: streamedTask.prNumber,
										prState: streamedTask.prState,
									} satisfies TaskWithSession
								})
							: streamedTasks
					yield* Effect.logInfo(
						`[board-refresh] project=${resolvedProjectPath} streamed=${streamedTasks.length} listed=${listedTasks.length} loaded=${loadedTasks.length}`,
					)
					const graceExpiries = yield* Ref.get(localCreateGraceExpiries)
					const { mergedTasks, nextLocalCreateGraceExpiries } =
						mergeLoadedTasksWithLocalCreateGrace({
							loadedTasks,
							currentTasks,
							localCreateGraceExpiries: graceExpiries,
							nowMs: Date.now(),
						})
					yield* Ref.set(localCreateGraceExpiries, nextLocalCreateGraceExpiries)
					yield* replaceTasks(mergedTasks)
					yield* Effect.logInfo(
						`[board-refresh] project=${resolvedProjectPath} merged=${mergedTasks.length}`,
					)
				}).pipe(Effect.ensuring(SubscriptionRef.set(isLoading, false)))

			const removeTaskFromMutation = (taskId: string) =>
				Effect.gen(function* () {
					const currentTasksValue = yield* SubscriptionRef.get(tasks)
					const nextTasks = currentTasksValue.filter((task) => task.id !== taskId)
					if (nextTasks.length === currentTasksValue.length) {
						return
					}
					yield* replaceTasks(nextTasks)
					yield* Ref.update(localCreateGraceExpiries, (current) => {
						if (!current.has(taskId)) {
							return current
						}
						const next = new Map(current)
						next.delete(taskId)
						return next
					})
				})

			const patchTaskFromMutation = (taskId: string, patch: Partial<Omit<TaskWithSession, "id">>) =>
				Effect.gen(function* () {
					const currentTasksValue = yield* SubscriptionRef.get(tasks)
					let changed = false
					const nextTasks = currentTasksValue.map((task) => {
						if (task.id !== taskId) {
							return task
						}
						changed = true
						return {
							...task,
							...patch,
						}
					})
					if (!changed) {
						return
					}
					yield* replaceTasks(nextTasks)
				})

			const toTaskFromMutationIssue = (
				issue: Issue,
				existingTask: TaskWithSession | undefined,
				options?: MutationTaskUpsertOptions,
			): TaskWithSession => {
				const prInfo = parsePRInfo(issue.notes)
				const parentEpicId =
					options?.parentEpicId === undefined
						? existingTask?.parentEpicId
						: (options.parentEpicId ?? undefined)

				return {
					...issue,
					issue_type: toBoardIssueType(issue),
					sessionState: existingTask?.sessionState ?? "idle",
					hasTmuxSession: existingTask?.hasTmuxSession,
					hasWorktree: existingTask?.hasWorktree,
					hasMergeConflict: existingTask?.hasMergeConflict,
					hasDevServer: existingTask?.hasDevServer,
					parentEpicId,
					gitBehindCount: existingTask?.gitBehindCount,
					hasUncommittedChanges: existingTask?.hasUncommittedChanges,
					gitAdditions: existingTask?.gitAdditions,
					gitDeletions: existingTask?.gitDeletions,
					sessionStartedAt: existingTask?.sessionStartedAt,
					estimatedTokens: existingTask?.estimatedTokens,
					recentOutput: existingTask?.recentOutput,
					agentPhase: existingTask?.agentPhase,
					hasPR: prInfo.hasPR === true ? true : undefined,
					prUrl: prInfo.prUrl,
					prNumber: prInfo.prNumber,
					prState: prInfo.hasPR === true ? existingTask?.prState : undefined,
				}
			}

			const upsertIssueFromMutation = (issue: Issue, options?: MutationTaskUpsertOptions) =>
				Effect.gen(function* () {
					const currentTasksValue = yield* SubscriptionRef.get(tasks)
					const existingTask = currentTasksValue.find((task) => task.id === issue.id)
					const nextTask = toTaskFromMutationIssue(issue, existingTask, options)
					const nextTasks = [
						...currentTasksValue.filter((task) => task.id !== nextTask.id),
						nextTask,
					].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at))
					yield* replaceTasks(nextTasks)
					if (existingTask === undefined) {
						const expiry = Date.now() + LOCAL_CREATE_VISIBILITY_GRACE_MS
						yield* Ref.update(localCreateGraceExpiries, (current) => {
							const next = new Map(current)
							next.set(issue.id, expiry)
							return next
						})
					}
				})

			const syncTaskFromBackend = (taskId: string, options?: MutationTaskUpsertOptions) =>
				Effect.gen(function* () {
					const activeProjectPath = yield* SubscriptionRef.get(currentProjectPath)
					const projectPath = yield* resolveBoardProjectPath(activeProjectPath)
					const result = yield* daemonRpcClient.issueGet({ issueId: taskId, projectPath })
					yield* upsertIssueFromMutation(result.issue, options)
				})

			const saveToCache = (projectPath: string) =>
				Effect.gen(function* () {
					const resolvedProjectPath = yield* resolveBoardProjectPath(projectPath)
					const currentTasksValue = yield* SubscriptionRef.get(tasks)
					if (currentTasksValue.length === 0) {
						return
					}
					yield* Ref.update(boardCache, (current) => {
						const next = new Map(current)
						next.set(resolvedProjectPath, currentTasksValue)
						return next
					})
				})

			const loadFromCache = (projectPath: string) =>
				Effect.gen(function* () {
					const resolvedProjectPath = yield* resolveBoardProjectPath(projectPath)
					const cached = (yield* Ref.get(boardCache)).get(resolvedProjectPath)
					if (cached === undefined || cached.length === 0) {
						return false
					}
					yield* replaceTasks(cached)
					return true
				})

			const clearBoard = () =>
				Effect.gen(function* () {
					yield* replaceTasks([])
				})

			const runProjectSwitchRefresh = (projectPath: string) =>
				Effect.gen(function* () {
					let refreshFailed = false
					const refreshOnce = refreshForProjectPath(projectPath).pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(Cause.pretty(cause)).pipe(
								Effect.tap(() =>
									Effect.sync(() => {
										if (!Cause.isInterruptedOnly(cause)) {
											refreshFailed = true
										}
									}),
								),
								Effect.asVoid,
							),
						),
					)
					const maxRefreshAttempts = 5
					for (let attempt = 0; attempt < maxRefreshAttempts; attempt += 1) {
						if (attempt > 0) {
							yield* Effect.sleep("500 millis")
						}
						yield* refreshOnce
						const refreshedTaskCount = (yield* SubscriptionRef.get(tasks)).length
						if (refreshedTaskCount > 0) {
							break
						}
					}
					return { refreshFailed } as const
				})

			yield* Effect.forkScoped(
				Stream.fromQueue(switchRefreshQueue).pipe(
					Stream.runForEach((request) =>
						runProjectSwitchRefresh(request.projectPath).pipe(
							Effect.flatMap((result) => Deferred.succeed(request.done, result)),
							Effect.catchAllCause((cause) => Deferred.failCause(request.done, cause)),
						),
					),
				),
			)

			const refresh = () =>
				Effect.gen(function* () {
					const activeProjectPath = yield* SubscriptionRef.get(currentProjectPath)
					const projectPath = yield* resolveBoardProjectPath(activeProjectPath)
					yield* SubscriptionRef.set(currentProjectPath, projectPath)
					yield* refreshForProjectPath(projectPath)
				})

			const refreshGitStats = () =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(isRefreshingGitStats, true)
					yield* refresh()
				}).pipe(Effect.ensuring(SubscriptionRef.set(isRefreshingGitStats, false)))

			const getTasks = (): Effect.Effect<readonly TaskWithSession[]> => SubscriptionRef.get(tasks)

			const getFilteredTasksByColumn = (
				searchQuery: string,
				sortConfig: SortConfig,
				filterConfig: FilterConfig,
			): Effect.Effect<readonly (readonly TaskWithSession[])[]> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)
					return computeFilteredTasksByColumn(allTasks, searchQuery, sortConfig, filterConfig)
				})

			const findTaskById = (taskId: string): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					const allTasks = yield* SubscriptionRef.get(tasks)
					return allTasks.find((task) => task.id === taskId)
				})

			const applyOptimisticMove = (taskId: string, newStatus: ColumnStatus): Effect.Effect<void> =>
				patchTaskFromMutation(taskId, {
					status: newStatus,
					updated_at: new Date().toISOString(),
				})

			const switchToProject = <E>(
				newProjectPath: string,
				onRefreshComplete: Effect.Effect<void, E, never>,
			) =>
				Effect.gen(function* () {
					const resolvedProjectPath = yield* resolveBoardProjectPath(newProjectPath)
					const previousProjectPath = yield* SubscriptionRef.get(currentProjectPath)
					if (previousProjectPath !== null) {
						yield* saveToCache(previousProjectPath)
					}

					yield* SubscriptionRef.set(currentProjectPath, resolvedProjectPath)
					const cacheHit = yield* loadFromCache(resolvedProjectPath)
					if (!cacheHit) {
						yield* clearBoard()
					}

					const done = yield* Deferred.make<{ readonly refreshFailed: boolean }>()
					yield* Queue.offer(switchRefreshQueue, {
						projectPath: resolvedProjectPath,
						done,
					})
					const { refreshFailed } = yield* Deferred.await(done).pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(Cause.pretty(cause)).pipe(
								Effect.as({
									refreshFailed: true,
								} as const),
							),
						),
					)

					yield* onRefreshComplete.pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(Cause.pretty(cause)).pipe(Effect.asVoid),
						),
					)

					return { cacheHit, refreshFailed }
				})

			const editorChanges = Stream.merge(
				Stream.merge(editorService.mode.changes, editorService.sortConfig.changes),
				editorService.filterConfig.changes,
			)

			yield* Effect.forkScoped(
				Stream.runForEach(editorChanges, () =>
					updateFilteredTasks().pipe(
						Effect.catchAllCause((cause) =>
							Effect.logError(Cause.pretty(cause)).pipe(Effect.asVoid),
						),
					),
				),
			)

			const initialProjectPath = yield* resolveBoardProjectPath(
				yield* projectContext.getCurrentPath(),
			)
			yield* SubscriptionRef.set(currentProjectPath, initialProjectPath)
			yield* refreshForProjectPath(initialProjectPath).pipe(
				Effect.catchAllCause((cause) => Effect.logWarning(Cause.pretty(cause)).pipe(Effect.asVoid)),
			)

			return {
				tasks,
				tasksByColumn,
				filteredTasksByColumn,
				isLoading,
				isRefreshingGitStats,
				currentProjectPath,
				refresh,
				refreshGitStats,
				setVisibleTaskIds: (taskIds: ReadonlySet<string>) => Ref.set(visibleTaskIds, taskIds),
				getTasks,
				getFilteredTasksByColumn,
				findTaskById,
				applyOptimisticMove,
				removeTaskFromMutation,
				patchTaskFromMutation,
				upsertIssueFromMutation,
				syncTaskFromBackend,
				saveToCache,
				switchToProject,
			} satisfies TuiBoardStoreServiceApi
		}),
	},
) {}
