/**
 * BoardService - Task and board data management
 *
 * Manages board state (columns, tasks) using fine-grained Effect Refs.
 * Interfaces with IssueTrackerClient for task data and provides methods for task access.
 */

import { Command } from "@effect/platform"
import {
	Array as Arr,
	Cause,
	Config,
	DateTime,
	Duration,
	Effect,
	Fiber,
	HashMap,
	Option,
	Order,
	Record,
	Ref,
	Schedule,
	Stream,
	SubscriptionRef,
} from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { BackendSyncRouter } from "../core/BackendSyncRouter.js"
import {
	type Issue,
	IssueTrackerClient,
	inferLinearIssueType,
	resolveConfiguredIssueBackend,
	type SyncRequiredError,
} from "../core/IssueTrackerClient.js"
import { LocalIssueStore, type PersistedBoardTaskState } from "../core/LocalIssueStore.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import {
	getWorktreePath,
	normalizeIssueIdForLookup,
	resolveIssueIdFromSessionName,
} from "../core/paths.js"
import type {
	InvalidStateError,
	SessionError,
	SessionLimitError,
	SessionNotFoundError,
	SessionWorktreeMissingError,
} from "../core/SessionManager.js"
import type {
	TmuxError,
	SessionNotFoundError as TmuxSessionNotFoundError,
} from "../core/TmuxService.js"
import { WorktreeManager } from "../core/WorktreeManager.js"
import type { ShellNotReadyError } from "../core/WorktreeSessionService.js"
import { emptyRecord } from "../lib/empty.js"
import { DaemonRpcClient, type DaemonRpcClientApi } from "../rpc/DaemonRpcClient.js"
import type { DaemonEventStreamResult } from "../rpc/DaemonRpcSchemas.js"
import type {
	ColumnStatus,
	GitStatus,
	PRState,
	SessionState,
	TaskWithSession,
} from "../ui/types.js"
import { COLUMNS, parsePRInfo } from "../ui/types.js"
import { DiagnosticsService, type LinearWebhookHealth } from "./DiagnosticsService.js"
import { EditorService, type FilterConfig, type SortConfig } from "./EditorService.js"
import {
	type LinearIssueWebhookMessage,
	type LinearWebhookMode,
	LinearWebhookService,
	normalizePublicBaseUrl,
	parseTailscaleDnsName,
} from "./LinearWebhookService.js"
import { MutationQueue } from "./MutationQueue.js"
import { PRStateService } from "./PRStateService.js"
import { ProjectService } from "./ProjectService.js"
import { ToastService } from "./ToastService.js"
import {
	isTransientOperationalError,
	isTransientOperationalErrorMessage,
} from "./transientError.js"
import { makeTransientRetrySchedule } from "./transientRetrySchedule.js"

const BOARD_ISSUE_LIST_PAGE_SIZE = 200
const BOARD_BACKGROUND_POLL_INTERVAL = "5 seconds"
const REFRESH_FAILURE_TOAST_DEBOUNCE_MS = 15000
const WEBHOOK_FALLBACK_TOAST_DEBOUNCE_MS = 30000
const LINEAR_WEBHOOK_RESTART_DELAY = "5 seconds"
const LINEAR_WEBHOOK_DEFAULT_PORT = 9000
const LINEAR_WEBHOOK_DEFAULT_EVENTS: readonly string[] = ["Issue"]
const LINEAR_WEBHOOK_PUBLIC_URL_ENV = "LINEAR_WEBHOOK_PUBLIC_URL"
const LINEAR_WEBHOOK_TAILSCALE_STATUS_TIMEOUT_MS = 2000
const LINEAR_WEBHOOK_TAILSCALE_FUNNEL_TIMEOUT_MS = 2000
const LINEAR_SDK_DEFENSIVE_RECONCILIATION_INTERVAL = "2 minutes"
const LOCAL_CREATE_VISIBILITY_GRACE_MS = 15000
const GIT_STATUS_COMMAND_TIMEOUT_MS = 3000
const TRANSIENT_RETRY_ATTEMPTS = 4
const TRANSIENT_RETRY_BASE_DELAY_MS = 120
const TRANSIENT_RETRY_MAX_DELAY_MS = 1000
const AZEDARACH_REQUIRE_DAEMON_AUTHORITY_ENV = "AZEDARACH_REQUIRE_DAEMON_AUTHORITY"

export const isBoardDaemonAuthorityRequired = (
	env: Readonly<Record<string, string | undefined>>,
): boolean => env[AZEDARACH_REQUIRE_DAEMON_AUTHORITY_ENV] !== "0"

const withTransientRetry = <A, E, R>(
	context: string,
	effect: Effect.Effect<A, E, R>,
): Effect.Effect<A, E, R> =>
	effect.pipe(
		Effect.tapError((error) =>
			isTransientOperationalError(error)
				? Effect.logWarning(
						`Transient error detected during ${context}; retrying (max ${TRANSIENT_RETRY_ATTEMPTS} attempts)`,
					)
				: Effect.void,
		),
		Effect.retry({
			schedule: makeTransientRetrySchedule({
				retryBaseDelayMs: TRANSIENT_RETRY_BASE_DELAY_MS,
				retryMaxDelayMs: TRANSIENT_RETRY_MAX_DELAY_MS,
				retryMaxAttempts: TRANSIENT_RETRY_ATTEMPTS,
				while: isTransientOperationalError,
			}),
		}),
	)

type SessionRecoveryError =
	| SessionNotFoundError
	| TmuxSessionNotFoundError
	| InvalidStateError
	| TmuxError
	| ShellNotReadyError
	| SessionError
	| SessionWorktreeMissingError
	| SessionLimitError

export type SessionRecoveryRetryability = "transient" | "terminal"

export const classifySessionRecoveryError = (
	error: SessionRecoveryError,
): SessionRecoveryRetryability => {
	switch (error._tag) {
		case "TmuxError":
			return "transient"
		case "ShellNotReadyError":
			return "transient"
		case "SessionNotFoundError":
			return "session" in error ? "transient" : "terminal"
		case "SessionLimitError":
			return "transient"
		case "SessionError":
			return isTransientOperationalErrorMessage(error.message) ? "transient" : "terminal"
		case "SessionWorktreeMissingError":
			return "terminal"
		case "InvalidStateError":
			return "terminal"
	}
}

export const makeBoardDaemonIpcSignals = (params: {
	readonly daemonRpcClient: Option.Option<DaemonRpcClientApi>
	readonly daemonFrontendClientId: string
	readonly nowMs: () => number
	readonly getProjectPath?: () => Effect.Effect<string | undefined, never, never>
	readonly onDaemonStreamBatch?: (batch: DaemonEventStreamResult) => Effect.Effect<void>
}) => {
	const resolveProjectPath = (projectPath: string | undefined): string =>
		projectPath ?? process.cwd()

	const observeSessionSnapshot = (): Effect.Effect<void> => {
		if (Option.isNone(params.daemonRpcClient)) {
			return Effect.void
		}
		const daemonRpcClient = params.daemonRpcClient.value
		if (daemonRpcClient.sessionSnapshot === undefined) {
			return Effect.void
		}
		const sessionSnapshot = daemonRpcClient.sessionSnapshot
		return Effect.gen(function* () {
			const projectPath = resolveProjectPath(
				params.getProjectPath === undefined ? undefined : yield* params.getProjectPath(),
			)
			return yield* sessionSnapshot({ projectPath })
		}).pipe(
			Effect.flatMap((snapshot) =>
				Effect.logDebug(
					`BoardService daemon snapshot observed: total=${snapshot.sessions.length} capturedAtMs=${snapshot.capturedAtMs}`,
				),
			),
			Effect.asVoid,
			Effect.catchAll(() => Effect.void),
		)
	}

	const signalAttach = (): Effect.Effect<void> => {
		if (Option.isNone(params.daemonRpcClient)) {
			return Effect.void
		}
		return params.daemonRpcClient.value
			.attach({
				clientId: params.daemonFrontendClientId,
				requestedAtMs: params.nowMs(),
			})
			.pipe(
				Effect.zipRight(observeSessionSnapshot()),
				Effect.asVoid,
				Effect.catchAll(() => Effect.void),
			)
	}

	const signalReconnect = (): Effect.Effect<void> => {
		if (Option.isNone(params.daemonRpcClient)) {
			return Effect.void
		}
		return params.daemonRpcClient.value
			.reconnect({
				clientId: params.daemonFrontendClientId,
				requestedAtMs: params.nowMs(),
			})
			.pipe(
				Effect.asVoid,
				Effect.catchAll(() => Effect.void),
			)
	}

	const signalHeartbeat = (): Effect.Effect<void> => {
		if (Option.isNone(params.daemonRpcClient)) {
			return Effect.void
		}
		const daemonRpcClient = params.daemonRpcClient.value
		return daemonRpcClient
			.heartbeat({
				clientId: params.daemonFrontendClientId,
				observedAtMs: params.nowMs(),
			})
			.pipe(
				Effect.catchAll((heartbeatError) =>
					signalReconnect().pipe(
						Effect.zipRight(
							daemonRpcClient.heartbeat({
								clientId: params.daemonFrontendClientId,
								observedAtMs: params.nowMs(),
							}),
						),
						Effect.mapError(() => heartbeatError),
					),
				),
				Effect.zipRight(observeSessionSnapshot()),
				Effect.asVoid,
				Effect.catchAll(() => Effect.void),
			)
	}

	const processDaemonStreamBatch = (batch: DaemonEventStreamResult): Effect.Effect<void> =>
		Effect.forEach(
			batch.events,
			(entry) => {
				switch (entry.event._tag) {
					case "DaemonEventStreamSessionSnapshotEvent":
						return Effect.logDebug(
							`BoardService daemon stream session snapshot: cursor=${entry.cursor} sessions=${entry.event.sessions.length} capturedAtMs=${entry.event.capturedAtMs}`,
						)
					case "DaemonEventStreamRuntimeSnapshotEvent":
						return Effect.logDebug(
							`BoardService daemon stream runtime snapshot: cursor=${entry.cursor} revision=${entry.event.runtime.revision} phase=${entry.event.runtime.runtimePhase}`,
						)
				}
			},
			{ discard: true },
		).pipe(
			Effect.zipRight(
				params.onDaemonStreamBatch === undefined ? Effect.void : params.onDaemonStreamBatch(batch),
			),
		)

	const consumeStreamBatch = (cursor: number | undefined): Effect.Effect<number | undefined> => {
		if (Option.isNone(params.daemonRpcClient)) {
			return Effect.succeed(cursor)
		}
		const daemonEventStream = params.daemonRpcClient.value.eventStream
		if (daemonEventStream === undefined) {
			return Effect.succeed(cursor)
		}
		return Effect.gen(function* () {
			const projectPath = resolveProjectPath(
				params.getProjectPath === undefined ? undefined : yield* params.getProjectPath(),
			)
			return yield* daemonEventStream({
				clientId: params.daemonFrontendClientId,
				projectPath,
				cursor,
				batchSize: 32,
				waitMs: 2500,
			})
		}).pipe(
			Effect.tap(processDaemonStreamBatch),
			Effect.map((batch) => batch.nextCursor),
			Effect.catchAll(() => Effect.succeed(cursor)),
		)
	}

	return {
		signalAttach,
		signalReconnect,
		signalHeartbeat,
		consumeStreamBatch,
	}
}

export const resolveDaemonAuthoritativeProjectPath = (
	projectPath: string | null | undefined,
): string => projectPath ?? process.cwd()

const normalizeLinearWebhookStatus = (stateName: string | undefined): ColumnStatus => {
	if (!stateName) return "open"
	const normalized = stateName.trim().toLowerCase()

	if (
		normalized.includes("done") ||
		normalized.includes("complete") ||
		normalized.includes("cancel") ||
		normalized.includes("duplicate")
	) {
		return "closed"
	}

	if (normalized.includes("block")) {
		return "blocked"
	}

	if (
		normalized.includes("progress") ||
		normalized.includes("review") ||
		normalized.includes("started")
	) {
		return "in_progress"
	}

	return "open"
}

const normalizeLinearWebhookPriority = (priority: number | undefined): number => {
	if (priority === undefined || priority <= 0) return 2
	if (priority === 1) return 0
	if (priority === 2) return 1
	if (priority === 3) return 2
	return 3
}

interface LinearSdkEventsTickerBehavior {
	readonly localRefreshOnly: boolean
	readonly defensiveReconciliationInterval: Duration.DurationInput | undefined
}

export const resolveLinearSdkEventsTickerBehavior = (
	mode: LinearWebhookMode,
	healthy: boolean,
): LinearSdkEventsTickerBehavior =>
	mode === "sdk" && healthy
		? {
				localRefreshOnly: true,
				defensiveReconciliationInterval: LINEAR_SDK_DEFENSIVE_RECONCILIATION_INTERVAL,
			}
		: {
				localRefreshOnly: false,
				defensiveReconciliationInterval: undefined,
			}

const normalizeLinearWebhookReason = (reason: string | undefined): string | undefined => {
	if (reason === undefined) return undefined
	const trimmed = reason.trim()
	return trimmed.length > 0 ? trimmed : undefined
}

const isMissingPublicWebhookUrlReason = (reason: string): boolean => {
	const normalizedReason = reason.toLowerCase()
	return (
		normalizedReason.includes("public webhook url") ||
		normalizedReason.includes(LINEAR_WEBHOOK_PUBLIC_URL_ENV.toLowerCase())
	)
}

export const resolveLinearSdkPollingFallbackHealthMessage = (params: {
	readonly mode: LinearWebhookMode
	readonly healthy: boolean
	readonly reason: string | undefined
}): string => {
	const normalizedReason = normalizeLinearWebhookReason(params.reason)
	return normalizedReason === undefined
		? `SDK mode=${params.mode} healthy=${String(params.healthy)} with no CLI fallback; using background polling.`
		: `SDK mode=${params.mode} healthy=${String(params.healthy)} with no CLI fallback; reason=${normalizedReason}; using background polling.`
}

export const resolveLinearSdkPollingFallbackToastMessage = (params: {
	readonly mode: LinearWebhookMode
	readonly reason: string | undefined
}): string => {
	const normalizedReason = normalizeLinearWebhookReason(params.reason)
	if (normalizedReason === undefined) {
		return `Linear webhooks unavailable (mode=${params.mode}). Falling back to background polling.`
	}

	if (params.mode === "misconfigured" && isMissingPublicWebhookUrlReason(normalizedReason)) {
		return `Linear webhooks unavailable: ${normalizedReason}. Falling back to background polling.`
	}

	return `Linear webhooks unavailable (mode=${params.mode}): ${normalizedReason}. Falling back to background polling.`
}

export const shouldApplyLinearWebhookIssueEvent = (params: {
	readonly eventConfigKey: string
	readonly activeConfigKey: string | null
}): boolean => params.activeConfigKey !== null && params.eventConfigKey === params.activeConfigKey

export const shouldRunProjectSwitchLinearSync = (params: {
	readonly backend: ReturnType<typeof resolveConfiguredIssueBackend>
	readonly syncEnabled: boolean
}): boolean => params.backend === "linear" && params.syncEnabled

const linearWebhookReasonKey = (reason: string | undefined): string => {
	const normalizedReason = normalizeLinearWebhookReason(reason)
	return normalizedReason === undefined ? "none" : encodeURIComponent(normalizedReason)
}

export type BoardRefreshReason = "default" | "mutation" | "initial-load" | "project-switch" | "pty"

interface BoardRefreshOptions {
	readonly reason?: BoardRefreshReason
	readonly forceRemote?: boolean
}

type BoardRefreshExecutionMode = "remote" | "local-session-only" | "local-session-and-git"

export const resolveBoardRefreshExecutionMode = (params: {
	readonly localRefreshOnly: boolean
	readonly options: BoardRefreshOptions | undefined
}): BoardRefreshExecutionMode => {
	if (params.options?.forceRemote === true) return "remote"
	if (params.options?.reason === "pty") return "local-session-only"
	if (!params.localRefreshOnly) return "remote"
	switch (params.options?.reason ?? "default") {
		case "mutation":
		case "initial-load":
		case "project-switch":
			return "remote"
		default:
			return "local-session-and-git"
	}
}

export const resolveDaemonBoardReadModelRpc = (params: {
	readonly daemonRpcClient: Option.Option<DaemonRpcClientApi>
}): Option.Option<NonNullable<DaemonRpcClientApi["boardReadModel"]>> => {
	if (Option.isNone(params.daemonRpcClient)) {
		return Option.none()
	}
	const boardReadModel = params.daemonRpcClient.value.boardReadModel
	return boardReadModel === undefined ? Option.none() : Option.some(boardReadModel)
}

export const mergeDaemonTasksWithTmuxSessionPresence = (params: {
	readonly daemonTasks: ReadonlyArray<TaskWithSession>
	readonly tmuxSessionIssueIds: ReadonlySet<string>
}): ReadonlyArray<TaskWithSession> =>
	params.daemonTasks.map((task) => {
		if (task.hasTmuxSession === true) {
			return task
		}
		const hasDiscoveredTmuxSession = params.tmuxSessionIssueIds.has(
			normalizeIssueIdForLookup(task.id),
		)
		return hasDiscoveredTmuxSession ? { ...task, hasTmuxSession: true } : task
	})

export const applySessionRefreshPatch = (params: {
	readonly task: TaskWithSession
	readonly sessionState: TaskWithSession["sessionState"]
	readonly hasTmuxSession: boolean | undefined
	readonly sessionStartedAt: string | undefined
	readonly estimatedTokens: number | undefined
	readonly recentOutput: string | undefined
	readonly agentPhase: TaskWithSession["agentPhase"]
	readonly gitStatusPatch: GitStatus | undefined
}): TaskWithSession => ({
	...params.task,
	...(params.gitStatusPatch ?? {}),
	sessionState: params.sessionState,
	hasTmuxSession: params.hasTmuxSession,
	sessionStartedAt: params.sessionStartedAt,
	estimatedTokens: params.estimatedTokens,
	recentOutput: params.recentOutput,
	agentPhase: params.agentPhase,
})

export const reconcileLoadedTasksWithLocalCreateGrace = (params: {
	readonly loadedTasks: ReadonlyArray<TaskWithSession>
	readonly currentTasks: ReadonlyArray<TaskWithSession>
	readonly localCreateGraceExpiries: ReadonlyMap<string, number>
	readonly nowMs: number
}): {
	readonly mergedTasks: ReadonlyArray<TaskWithSession>
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
		if (expiry <= params.nowMs) {
			continue
		}
		if (loadedTaskIds.has(taskId)) {
			continue
		}
		nextLocalCreateGraceExpiries.set(taskId, expiry)
	}

	return {
		mergedTasks,
		nextLocalCreateGraceExpiries,
	}
}

export const resolveHasWorktreeFlag = (params: {
	readonly issueId: string
	readonly persistedHasWorktree: boolean | undefined
	readonly worktreeIssueIds: ReadonlySet<string>
	readonly worktreeInventoryLoaded: boolean
}): boolean | undefined => {
	if (params.worktreeInventoryLoaded) {
		return params.worktreeIssueIds.has(params.issueId) ? true : undefined
	}

	return params.persistedHasWorktree === true ? true : undefined
}

interface RetainedTaskGitStateSource extends GitStatus {
	readonly hasMergeConflict?: boolean
}

const emptyGitStatus = (): GitStatus => ({
	gitBehindCount: undefined,
	hasUncommittedChanges: undefined,
	gitAdditions: undefined,
	gitDeletions: undefined,
})

export const shouldTaskExposeGitStatus = (params: {
	readonly hasWorktree: boolean | undefined
	readonly sessionState: TaskWithSession["sessionState"]
}): boolean => params.hasWorktree === true || params.sessionState !== "idle"

export const resolveRetainedTaskGitState = (params: {
	readonly hasWorktree: boolean | undefined
	readonly sessionState: TaskWithSession["sessionState"]
	readonly source: RetainedTaskGitStateSource | undefined
}): {
	readonly hasMergeConflict: boolean
	readonly gitStatus: GitStatus
} => {
	if (!shouldTaskExposeGitStatus(params)) {
		return {
			hasMergeConflict: false,
			gitStatus: emptyGitStatus(),
		}
	}

	return {
		hasMergeConflict: params.source?.hasMergeConflict ?? false,
		gitStatus: {
			gitBehindCount: params.source?.gitBehindCount,
			hasUncommittedChanges: params.source?.hasUncommittedChanges,
			gitAdditions: params.source?.gitAdditions,
			gitDeletions: params.source?.gitDeletions,
		},
	}
}

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

const hasParentChildDependents = (issue: Issue): boolean =>
	(issue.dependents ?? []).some((dependency) => dependency.dependency_type === "parent-child")

const toBoardIssueType = (issue: Issue): Issue["issue_type"] =>
	(issue.dependent_count ?? 0) > 0 || hasParentChildDependents(issue) ? "epic" : issue.issue_type

// ============================================================================
// Cache Types
// ============================================================================

/** TTL for git status cache in milliseconds.
 * Keep this short so git badges feel responsive during active work. */
const GIT_STATUS_CACHE_TTL_MS = 3000

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

interface LinearWebhookListenerConfig {
	readonly command: string
	readonly team: string
	readonly url: string
	readonly port: number
	readonly events: readonly string[]
	readonly secret: string | undefined
}

interface ActiveLinearRefreshStrategy {
	readonly key: string
	readonly fiber: Fiber.RuntimeFiber<unknown, never>
}

interface LinearRefreshStrategyPlan {
	readonly key: string
	readonly start: Effect.Effect<Fiber.RuntimeFiber<unknown, never>, never, unknown>
}

interface MutationTaskUpsertOptions {
	readonly parentEpicId?: string | null
}

interface AuthoritativeSessionView {
	readonly issueId: string
	readonly state: SessionState
	readonly tmuxSessionName: string
	readonly startedAt: DateTime.Utc
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
		IssueTrackerClient.Default,
		BackendSyncRouter.Default,
		EditorService.Default,
		PTYMonitor.Default,
		ProjectService.Default,
		DiagnosticsService.Default,
		AppConfig.Default,
		LinearWebhookService.Default,
		MutationQueue.Default,
		WorktreeManager.Default,
		PRStateService.Default,
		ToastService.Default,
		LocalIssueStore.Default,
	],
	scoped: Effect.gen(function* () {
		const issueTrackerClient = yield* IssueTrackerClient
		const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
		const backendSyncRouter = yield* BackendSyncRouter
		const editorService = yield* EditorService
		const ptyMonitor = yield* PTYMonitor
		const projectService = yield* ProjectService
		const diagnostics = yield* DiagnosticsService
		const appConfig = yield* AppConfig
		const linearWebhookService = yield* LinearWebhookService
		const mutationQueue = yield* MutationQueue
		const worktreeManager = yield* WorktreeManager
		const prStateService = yield* PRStateService
		const toast = yield* ToastService
		const localIssueStore = yield* LocalIssueStore

		// Capture the service's scope for use in methods that spawn background fibers
		const serviceScope = yield* Effect.scope
		// Register with diagnostics
		yield* diagnostics.trackService("BoardService", "Board refresh + session state merge")

		yield* diagnostics.trackService("BoardService", "Event-driven refresh with per-project cache")

		const tasks = yield* SubscriptionRef.make<ReadonlyArray<TaskWithSession>>([])
		const tasksByColumn = yield* SubscriptionRef.make<
			Record.ReadonlyRecord<string, ReadonlyArray<TaskWithSession>>
		>(emptyRecord())
		const isLoading = yield* SubscriptionRef.make<boolean>(false)
		const isRefreshingGitStats = yield* SubscriptionRef.make<boolean>(false)
		const visibleTaskIds = yield* SubscriptionRef.make<ReadonlySet<string>>(new Set())
		const localCreateGraceExpiriesRef = yield* Ref.make<Map<string, number>>(new Map())
		const filteredTasksByColumn = yield* SubscriptionRef.make<TaskWithSession[][]>(
			COLUMNS.map(() => []),
		)
		const debounceFiberRef = yield* Ref.make<Fiber.Fiber<void, never> | null>(null)
		const localRefreshOnlyRef = yield* Ref.make(false)
		const linearRefreshStrategyRef = yield* Ref.make<ActiveLinearRefreshStrategy | null>(null)
		const refreshSemaphore = yield* Effect.makeSemaphore(1)
		const refreshFailureToastRef = yield* Ref.make<{
			readonly message: string
			readonly timestamp: number
		} | null>(null)
		const webhookFallbackToastRef = yield* Ref.make<{
			readonly message: string
			readonly timestamp: number
		} | null>(null)
		const linearIdentifierByEntityIdRef = yield* Ref.make<Map<string, string>>(new Map())
		const daemonFrontendClientId = `board-ui:${process.pid}`
		const {
			signalAttach: signalDaemonAttach,
			signalHeartbeat: signalDaemonHeartbeat,
			signalReconnect: signalDaemonReconnect,
			consumeStreamBatch: consumeDaemonStreamBatch,
		} = makeBoardDaemonIpcSignals({
			daemonRpcClient,
			daemonFrontendClientId,
			nowMs: Date.now,
			getProjectPath: projectService.getCurrentPath,
		})
		const daemonStreamCursorRef = yield* Ref.make<number | undefined>(undefined)

		const parseDaemonSessionState = (state: string): SessionState | undefined => {
			switch (state) {
				case "idle":
				case "initializing":
				case "busy":
				case "waiting":
				case "done":
				case "error":
				case "paused":
				case "warning":
				case "crashed":
					return state
				default:
					return undefined
			}
		}

		const parseDaemonStartedAt = (value: string): DateTime.Utc | undefined => {
			const timestampMs = Date.parse(value)
			if (Number.isNaN(timestampMs)) {
				return undefined
			}
			return DateTime.unsafeFromDate(new Date(timestampMs))
		}

		const toAuthoritativeSessionView = (entry: {
			readonly issueId: string
			readonly state: string
			readonly tmuxSessionName: string
			readonly startedAt: string
		}): AuthoritativeSessionView | undefined => {
			const state = parseDaemonSessionState(entry.state)
			if (state === undefined) {
				return undefined
			}
			const startedAt = parseDaemonStartedAt(entry.startedAt)
			if (startedAt === undefined) {
				return undefined
			}
			return {
				issueId: entry.issueId,
				state,
				tmuxSessionName: entry.tmuxSessionName,
				startedAt,
			}
		}

		const loadTmuxSessionIssueIds = (projectPath: string | null) =>
			Effect.sync(() => {
				const output = Bun.spawnSync(["tmux", "list-sessions", "-F", "#{session_name}"], {
					stdout: "pipe",
					stderr: "pipe",
				})
				if (output.exitCode !== 0) {
					return new Set<string>()
				}
				const sessions = new TextDecoder().decode(output.stdout).trim().split("\n").filter(Boolean)
				const scopedProjectPath = resolveDaemonAuthoritativeProjectPath(projectPath)
				const issueIds = new Set<string>()
				for (const sessionName of sessions) {
					const issueId = resolveIssueIdFromSessionName(sessionName, {
						projectPath: scopedProjectPath,
					})
					if (issueId) {
						issueIds.add(normalizeIssueIdForLookup(issueId))
					}
				}
				return issueIds
			}).pipe(
				Effect.tapError((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`),
				),
				Effect.catchAll(() => Effect.succeed(new Set<string>())),
			)

		const loadAuthoritativeSessions = (projectPath: string | null) => {
			if (Option.isSome(daemonRpcClient) && daemonRpcClient.value.sessionSnapshot !== undefined) {
				const daemonProjectPath = resolveDaemonAuthoritativeProjectPath(projectPath)
				return daemonRpcClient.value.sessionSnapshot({ projectPath: daemonProjectPath }).pipe(
					Effect.map((result) =>
						result.sessions
							.map((entry) => toAuthoritativeSessionView(entry))
							.filter((entry): entry is AuthoritativeSessionView => entry !== undefined),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed([])),
						),
					),
				)
			}

			return Effect.logWarning(
				"BoardService authoritative daemon session snapshot RPC unavailable; returning empty session snapshot",
			).pipe(Effect.zipRight(Effect.succeed([])))
		}

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

		const toPersistedBoardTaskState = (task: TaskWithSession): PersistedBoardTaskState => ({
			issueId: task.id,
			hasWorktree: task.hasWorktree,
			hasMergeConflict: task.hasMergeConflict,
			parentEpicId: task.parentEpicId,
			estimatedTokens: task.estimatedTokens,
			recentOutput: task.recentOutput,
			agentPhase: task.agentPhase,
			hasPR: task.hasPR,
			prUrl: task.prUrl,
			prNumber: task.prNumber,
			prState: task.prState,
			gitBehindCount: task.gitBehindCount,
			hasUncommittedChanges: task.hasUncommittedChanges,
			gitAdditions: task.gitAdditions,
			gitDeletions: task.gitDeletions,
			hasDevServer: task.hasDevServer,
		})

		const persistBoardProjection = (
			projectPath: string,
			taskList: ReadonlyArray<TaskWithSession>,
		): Effect.Effect<void> =>
			localIssueStore
				.replaceBoardTaskStates(taskList.map(toPersistedBoardTaskState), projectPath)
				.pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.void),
						),
					),
				)

		const loadBoardProjection = (
			projectPath: string,
		): Effect.Effect<ReadonlyArray<TaskWithSession>> =>
			localIssueStore
				.listBoardTasks(projectPath)
				.pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed([])),
						),
					),
				)

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
				yield* persistBoardProjection(path, state.tasks)
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
			issueId: string,
			sessionState: TaskWithSession["sessionState"],
		) =>
			Effect.gen(function* () {
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					const existing = copy.get(projectPath)
					if (!existing) return copy // Project not loaded yet, skip

					const updatedTasks = existing.tasks.map((t) =>
						t.id === issueId ? { ...t, sessionState } : t,
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

		// Parent relationship cache - rarely changes, so cache for longer (30 seconds)
		// This avoids the expensive batch tracker show call on every refresh
		// Now supports multiple projects for fast project switching
		const PARENT_EPIC_CACHE_TTL_MS = 30000
		interface ParentEpicCacheEntry {
			readonly parentByIssueId: Map<string, string | undefined>
			readonly parentEpicByIssueId: Map<string, string | undefined>
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

		const runGitStatusCommand = <A, E, R>(
			label: string,
			command: Effect.Effect<A, E, R>,
		): Effect.Effect<A | undefined, never, R> =>
			command.pipe(
				Effect.timeout(`${GIT_STATUS_COMMAND_TIMEOUT_MS} millis`),
				Effect.catchAll((error) =>
					Effect.logWarning(`Git status command failed (${label}): ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(undefined)),
					),
				),
				Effect.tap((result) =>
					result === undefined
						? Effect.logWarning(
								`Git status command timed out or failed (${label}) after ${GIT_STATUS_COMMAND_TIMEOUT_MS}ms`,
							)
						: Effect.void,
				),
			)

		const checkMergeConflict = (worktreePath: string) =>
			Effect.gen(function* () {
				const exitCode = yield* runGitStatusCommand(
					"rev-parse MERGE_HEAD",
					Command.make("git", "-C", worktreePath, "rev-parse", "MERGE_HEAD").pipe(Command.exitCode),
				)
				return exitCode === 0
			})

		const checkGitStatus = (worktreePath: string, baseBranch: string, showLineChanges: boolean) =>
			diagnostics.measure(
				{
					source: "BoardService",
					name: "git.status",
					thresholdMs: 300,
					details: worktreePath,
				},
				Effect.gen(function* () {
					const behindCommand = Command.make(
						"git",
						"-C",
						worktreePath,
						"rev-list",
						"--count",
						`HEAD..${baseBranch}`,
					).pipe(Command.string)

					const behindOutput = yield* runGitStatusCommand(
						`rev-list HEAD..${baseBranch}`,
						behindCommand,
					)
					const behindCount =
						behindOutput === undefined
							? 0
							: (() => {
									const count = Number.parseInt(behindOutput.trim(), 10)
									return Number.isNaN(count) ? 0 : count
								})()

					const dirtyCommand = Command.make(
						"git",
						"-C",
						worktreePath,
						"status",
						"--porcelain",
					).pipe(Command.string)

					const dirtyOutput = yield* runGitStatusCommand("status --porcelain", dirtyCommand)
					const hasUncommittedChanges = dirtyOutput !== undefined && dirtyOutput.trim().length > 0

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

						const mergeBaseOutput = yield* runGitStatusCommand(
							`merge-base ${baseBranch} HEAD`,
							mergeBaseCommand,
						)
						const mergeBase = mergeBaseOutput?.trim() || baseBranch

						// Use merge-base for accurate diff stats (matches DiffService.getChangedFiles)
						// Excludes .azedarach/ directory - users care about code changes, not tracker metadata
						const diffCommand = Command.make(
							"git",
							"-C",
							worktreePath,
							"diff",
							"--numstat",
							mergeBase,
							"HEAD",
							"--",
							":^.azedarach",
						).pipe(Command.string)

						const diffOutput = yield* runGitStatusCommand(
							`diff --numstat ${mergeBase} HEAD`,
							diffCommand,
						)
						if (diffOutput !== undefined) {
							let additions = 0
							let deletions = 0
							for (const line of diffOutput.trim().split("\n")) {
								if (!line) continue
								const parts = line.split("\t")
								const add = Number.parseInt(parts[0] ?? "0", 10)
								const del = Number.parseInt(parts[1] ?? "0", 10)
								if (!Number.isNaN(add)) additions += add
								if (!Number.isNaN(del)) deletions += del
							}
							gitAdditions = additions
							gitDeletions = deletions
						} else {
							gitAdditions = 0
							gitDeletions = 0
						}
					}

					return {
						gitBehindCount: behindCount > 0 ? behindCount : undefined,
						hasUncommittedChanges: hasUncommittedChanges || undefined,
						gitAdditions: gitAdditions !== undefined && gitAdditions > 0 ? gitAdditions : undefined,
						gitDeletions: gitDeletions !== undefined && gitDeletions > 0 ? gitDeletions : undefined,
					}
				}).pipe(Effect.withSpan("board.gitStatus")),
			)

		const getCurrentBoardProjectPath = (): Effect.Effect<string | null> =>
			SubscriptionRef.get(currentProjectPath).pipe(
				Effect.flatMap((storedPath) =>
					storedPath !== null
						? Effect.succeed(storedPath)
						: projectService.getCurrentPath().pipe(Effect.map((path) => path ?? null)),
				),
			)

		const resolveProjectPath = (
			preferredProjectPath?: string | null,
		): Effect.Effect<string | null> =>
			preferredProjectPath !== undefined
				? Effect.succeed(preferredProjectPath)
				: getCurrentBoardProjectPath()

		const getGitConfigForResolvedProject = (
			projectPath: string | null,
		): Effect.Effect<{
			readonly baseBranch: string
			readonly showLineChanges: boolean
			readonly workflowMode: "local" | "origin"
		}> =>
			projectPath === null
				? appConfig.getGitConfig()
				: appConfig
						.getGitConfigForProjectPath(projectPath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(
									`BoardService: failed to load git config for projectPath=${projectPath}; using reactive fallback config (${error.message})`,
								).pipe(Effect.zipRight(appConfig.getGitConfig())),
							),
						)

		const loadTasks = (preferredProjectPath?: string | null) =>
			Effect.gen(function* () {
				const loadStartTime = Date.now()
				const daemonAuthorityRequired = isBoardDaemonAuthorityRequired(process.env)
				const projectPath = yield* resolveProjectPath(preferredProjectPath)
				const boardProjectPath = yield* SubscriptionRef.get(currentProjectPath)
				const serviceProjectPath = (yield* projectService.getCurrentPath()) ?? null
				yield* Effect.log(
					`loadTasks: resolved projectPath=${projectPath ?? "null"} preferredProjectPath=${preferredProjectPath ?? "null"} boardCurrentProjectPath=${boardProjectPath ?? "null"} projectServiceCurrentPath=${serviceProjectPath ?? "null"}`,
				)
				const daemonBoardReadModelRpc = resolveDaemonBoardReadModelRpc({
					daemonRpcClient,
				})
				if (Option.isNone(daemonBoardReadModelRpc)) {
					if (!daemonAuthorityRequired) {
						yield* Effect.logWarning(
							"loadTasks: daemon boardReadModel RPC unavailable; using legacy local fallback because daemon authority is explicitly disabled",
						)
					}
				}
				if (Option.isSome(daemonBoardReadModelRpc)) {
					const daemonPendingMutations = yield* mutationQueue.getOptimisticMutations()
					const daemonProjectPath = resolveDaemonAuthoritativeProjectPath(projectPath)
					const daemonTasks = yield* diagnostics.measure(
						{
							source: "BoardService",
							name: "daemon.boardReadModel",
							thresholdMs: 150,
							details: daemonProjectPath,
						},
						daemonBoardReadModelRpc.value({ projectPath: daemonProjectPath }).pipe(
							Effect.map((result) => result.tasks),
							Effect.catchAll((error) =>
								Effect.logWarning(
									`loadTasks: daemon boardReadModel failed for projectPath=${daemonProjectPath}: ${error.message}`,
								).pipe(Effect.zipRight(Effect.succeed([]))),
							),
						),
					)
					const tmuxSessionIssueIds = yield* diagnostics.measure(
						{
							source: "BoardService",
							name: "tmux.listSessions",
							thresholdMs: 150,
							details: daemonProjectPath,
						},
						loadTmuxSessionIssueIds(projectPath).pipe(Effect.withSpan("tmux.listSessions")),
					)
					const daemonTasksWithTmuxState = mergeDaemonTasksWithTmuxSessionPresence({
						daemonTasks,
						tmuxSessionIssueIds,
					})
					const daemonTasksWithMutations = daemonTasksWithTmuxState
						.map((task) => {
							const queuedMutation = daemonPendingMutations.get(task.id)
							if (queuedMutation === undefined) {
								return task
							}
							const mutation = queuedMutation.mutation
							switch (mutation._tag) {
								case "Move":
									return { ...task, status: mutation.status }
								case "Update":
									return { ...task, ...mutation.fields }
								case "Delete":
									return null
								default:
									return task
							}
						})
						.filter((task): task is TaskWithSession => task !== null)
					yield* Effect.log(
						`loadTasks: daemon read-model ${daemonTasksWithMutations.length} tasks fetched in ${Date.now() - loadStartTime}ms`,
					)
					return daemonTasksWithMutations
				}
				if (daemonAuthorityRequired) {
					yield* Effect.logWarning(
						"loadTasks: daemon boardReadModel RPC unavailable; using legacy load path to avoid empty board while daemon capabilities converge",
					)
				}
				const startupBatch = yield* Effect.all(
					{
						gitConfig: getGitConfigForResolvedProject(projectPath),
						startupConfig: SubscriptionRef.get(appConfig.config),
						currentVisibleTaskIds: SubscriptionRef.get(visibleTaskIds),
						persistedBoardTasks: loadBoardProjection(projectPath ?? process.cwd()),
						issues: diagnostics.measure(
							{
								source: "BoardService",
								name: "tracker.list",
								thresholdMs: 200,
								details: projectPath ?? "default",
							},
							withTransientRetry(
								"tracker.list",
								issueTrackerClient
									.list(undefined, projectPath ?? undefined, {
										pageSize: BOARD_ISSUE_LIST_PAGE_SIZE,
										sortBy: "updated_at",
										sortDirection: "desc",
										includeClosed: true,
									})
									.pipe(Effect.withSpan("tracker.list")),
							),
						),
						activeSessions: diagnostics.measure(
							{
								source: "BoardService",
								name: "sessions.listActive",
								thresholdMs: 150,
								details: projectPath ?? "default",
							},
							loadAuthoritativeSessions(projectPath).pipe(Effect.withSpan("sessions.listActive")),
						),
						tmuxSessionIssueIds: diagnostics.measure(
							{
								source: "BoardService",
								name: "tmux.listSessions",
								thresholdMs: 150,
								details: projectPath ?? "default",
							},
							loadTmuxSessionIssueIds(projectPath).pipe(Effect.withSpan("tmux.listSessions")),
						),
					},
					{ concurrency: "unbounded" },
				)
				const {
					gitConfig,
					startupConfig,
					currentVisibleTaskIds,
					persistedBoardTasks,
					issues,
					activeSessions,
					tmuxSessionIssueIds,
				} = startupBatch
				const { baseBranch, showLineChanges } = gitConfig
				const isLinearBackend = "linear" in startupConfig.issueTracker
				yield* Effect.log(
					`loadTasks: ${issues.length} issues fetched in ${Date.now() - loadStartTime}ms`,
				)
				const persistedTaskMap = new Map(persistedBoardTasks.map((task) => [task.id, task]))
				const sessionMap = new Map(activeSessions.map((session) => [session.issueId, session]))
				const ptySessionTargets = activeSessions
					.filter((session) => session.state !== "idle" && session.state !== "crashed")
					.map((session) => ({
						issueId: session.issueId,
						tmuxSessionName: session.tmuxSessionName,
					}))
				yield* ptyMonitor.syncSessions(ptySessionTargets).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(
							`Failed to sync PTY sessions during board load: ${String(error)}`,
						).pipe(Effect.asVoid),
					),
					Effect.forkIn(serviceScope),
				)
				const allMetrics = yield* SubscriptionRef.get(ptyMonitor.metrics)

				// Get optimistic mutations through queue adapter (daemon or local fallback)
				const pendingMutations = yield* mutationQueue.getOptimisticMutations()

				// Get parent relationship maps (cached for 30s to avoid expensive tracker show calls)
				// parentByIssueId is used for main-board filtering; parentEpicByIssueId is used
				// for epic-branch-specific git behavior.
				// Cache supports multiple projects for fast project switching
				const batchStartTime = Date.now()
				let parentByIssueId: Map<string, string | undefined>
				let parentEpicMap: Map<string, string | undefined>
				let cacheStatus = "miss"

				// Check if we have a valid cached parent epic map for this project
				const allCachedParentEpics = yield* Ref.get(parentEpicCacheRef)
				const now = Date.now()
				const normalizedProjectPath = projectPath ?? ""
				const cachedEntry = allCachedParentEpics.get(normalizedProjectPath)

				if (cachedEntry === undefined) {
					// Cache miss - fetch fresh data
					parentByIssueId = new Map<string, string | undefined>()
					parentEpicMap = new Map<string, string | undefined>()
					const issuesById = new Map(issues.map((issue) => [issue.id, issue]))
					const issuesWithDeps = issues.filter((issue) => (issue.dependency_count ?? 0) > 0)

					if (issuesWithDeps.length > 0) {
						// Linear list already includes dependency details; avoid an additional fetch.
						const issuesWithDepDetails = isLinearBackend
							? issuesWithDeps
							: yield* diagnostics.measure(
									{
										source: "BoardService",
										name: "tracker.showMultiple",
										thresholdMs: 200,
										details: `count=${issuesWithDeps.length}`,
									},
									issueTrackerClient
										.showMultiple(
											issuesWithDeps.map((i) => i.id),
											projectPath ?? undefined,
										)
										.pipe(
											Effect.withSpan("tracker.showMultiple"),
											Effect.catchAll((error) =>
												Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
													Effect.zipRight(Effect.succeed([])),
												),
											),
										),
								)

						// Extract parent IDs from dependencies, plus epic parent IDs for git base behavior
						for (const issue of issuesWithDepDetails) {
							const parentChildDep = issue.dependencies?.find(
								(dep) => dep.dependency_type === "parent-child",
							)
							let parentId: string | undefined
							let parentEpicId: string | undefined
							if (parentChildDep !== undefined) {
								const parentDependency = parentChildDep!
								parentId = parentDependency.id
								if (parentDependency.issue_type === "epic") {
									parentEpicId = parentDependency.id
								} else if (parentDependency.issue_type === undefined) {
									const parentIssue = issuesById.get(parentDependency.id)
									if (parentIssue?.issue_type === "epic") {
										parentEpicId = parentDependency.id
									}
								}
							}
							parentByIssueId.set(issue.id, parentId)
							parentEpicMap.set(issue.id, parentEpicId)
						}
					}

					// Cache the result (preserves cache for other projects)
					yield* Ref.update(parentEpicCacheRef, (cache) => {
						const newCache = new Map(cache)
						newCache.set(normalizedProjectPath, {
							parentByIssueId,
							parentEpicByIssueId: parentEpicMap,
							timestamp: now,
						})
						return newCache
					})
				} else if (now - cachedEntry!.timestamp < PARENT_EPIC_CACHE_TTL_MS) {
					// Cache hit - use cached map
					parentByIssueId = cachedEntry!.parentByIssueId
					parentEpicMap = cachedEntry!.parentEpicByIssueId
					cacheStatus = "hit"
				} else {
					// Cache miss - fetch fresh data
					parentByIssueId = new Map<string, string | undefined>()
					parentEpicMap = new Map<string, string | undefined>()
					const issuesById = new Map(issues.map((issue) => [issue.id, issue]))
					const issuesWithDeps = issues.filter((issue) => (issue.dependency_count ?? 0) > 0)

					if (issuesWithDeps.length > 0) {
						// Linear list already includes dependency details; avoid an additional fetch.
						const issuesWithDepDetails = isLinearBackend
							? issuesWithDeps
							: yield* diagnostics.measure(
									{
										source: "BoardService",
										name: "tracker.showMultiple",
										thresholdMs: 200,
										details: `count=${issuesWithDeps.length}`,
									},
									issueTrackerClient
										.showMultiple(
											issuesWithDeps.map((i) => i.id),
											projectPath ?? undefined,
										)
										.pipe(
											Effect.withSpan("tracker.showMultiple"),
											Effect.catchAll((error) =>
												Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
													Effect.zipRight(Effect.succeed([])),
												),
											),
										),
								)

						// Extract parent IDs from dependencies, plus epic parent IDs for git base behavior
						for (const issue of issuesWithDepDetails) {
							const parentChildDep = issue.dependencies?.find(
								(dep) => dep.dependency_type === "parent-child",
							)
							let parentId: string | undefined
							let parentEpicId: string | undefined
							if (parentChildDep !== undefined) {
								const parentDependency = parentChildDep!
								parentId = parentDependency.id
								if (parentDependency.issue_type === "epic") {
									parentEpicId = parentDependency.id
								} else if (parentDependency.issue_type === undefined) {
									const parentIssue = issuesById.get(parentDependency.id)
									if (parentIssue?.issue_type === "epic") {
										parentEpicId = parentDependency.id
									}
								}
							}
							parentByIssueId.set(issue.id, parentId)
							parentEpicMap.set(issue.id, parentEpicId)
						}
					}

					// Cache the result (preserves cache for other projects)
					yield* Ref.update(parentEpicCacheRef, (cache) => {
						const newCache = new Map(cache)
						newCache.set(normalizedProjectPath, {
							parentByIssueId,
							parentEpicByIssueId: parentEpicMap,
							timestamp: now,
						})
						return newCache
					})
				}
				yield* Effect.log(
					`loadTasks: deps resolved in ${Date.now() - batchStartTime}ms (cache ${cacheStatus})`,
				)

				// Get all worktrees ONCE upfront instead of per-issue exists() calls
				// This eliminates 331 Effect operations → 1 operation
				const worktreeInventoryProjectPath = projectPath ?? process.cwd()
				const worktreeInventory = projectPath
					? yield* diagnostics.measure(
							{
								source: "BoardService",
								name: "worktrees.list",
								thresholdMs: 200,
								details: worktreeInventoryProjectPath,
							},
							worktreeManager.list(worktreeInventoryProjectPath).pipe(
								Effect.withSpan("worktrees.list"),
								Effect.map((worktreeList) => ({
									loaded: true as const,
									issueIds: new Set(worktreeList.map((wt) => wt.issueId)),
								})),
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(
											Effect.succeed({
												loaded: false as const,
												issueIds: new Set<string>(),
											}),
										),
									),
								),
							),
						)
					: {
							loaded: false as const,
							issueIds: new Set<string>(),
						}

				const tasksWithNullable = yield* Effect.all(
					issues.map((issue) =>
						Effect.gen(function* () {
							const session = sessionMap.get(issue.id)
							const persistedTask = persistedTaskMap.get(issue.id)
							const metricsOpt = HashMap.get(allMetrics, issue.id)
							const metrics = metricsOpt._tag === "Some" ? metricsOpt.value : undefined
							const sessionState = session?.state ?? "idle"
							const hasTmuxSession =
								session !== undefined ||
								tmuxSessionIssueIds.has(normalizeIssueIdForLookup(issue.id))
									? true
									: undefined

							// Get parent IDs for filtering and epic-branch behavior
							const parentId = parentByIssueId.get(issue.id)
							const parentEpicId = parentEpicMap.get(issue.id)

							// Trust fresh worktree inventory when available; only fall back to persisted
							// state if the inventory could not be loaded.
							const hasWorktree = resolveHasWorktreeFlag({
								issueId: issue.id,
								persistedHasWorktree: persistedTask?.hasWorktree,
								worktreeIssueIds: worktreeInventory.issueIds,
								worktreeInventoryLoaded: worktreeInventory.loaded,
							})
							const retainedGitState = resolveRetainedTaskGitState({
								hasWorktree,
								sessionState,
								source: persistedTask,
							})

							let hasMergeConflict = retainedGitState.hasMergeConflict
							let gitStatus: GitStatus = retainedGitState.gitStatus
							const isVisible = currentVisibleTaskIds.has(issue.id)
							// Fetch git status only for visible tasks with active sessions or worktrees
							if (isVisible && (sessionState !== "idle" || hasWorktree) && projectPath) {
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

							const persistedHasPR = persistedTask?.hasPR === true
							const hasPRFromNotes = parsePRInfo(issue.notes)
							const prInfo = persistedHasPR
								? {
										hasPR: true,
										prUrl: persistedTask?.prUrl,
										prNumber: persistedTask?.prNumber,
									}
								: hasPRFromNotes

							const baseTask: TaskWithSession = {
								...issue,
								issue_type: toBoardIssueType(issue),
								sessionState,
								hasTmuxSession,
								hasWorktree,
								hasMergeConflict,
								hasDevServer: persistedTask?.hasDevServer === true ? true : undefined,
								parentEpicId: parentId ?? persistedTask?.parentEpicId,
								...gitStatus,
								hasPR: prInfo.hasPR === true ? true : undefined,
								prUrl: prInfo.prUrl ?? persistedTask?.prUrl,
								prNumber: prInfo.prNumber ?? persistedTask?.prNumber,
								prState: persistedTask?.prState,
								sessionStartedAt: session?.startedAt
									? DateTime.formatIso(session.startedAt)
									: undefined,
								estimatedTokens: metrics?.estimatedTokens ?? persistedTask?.estimatedTokens,
								recentOutput: metrics?.recentOutput ?? persistedTask?.recentOutput,
								agentPhase: metrics?.agentPhase ?? persistedTask?.agentPhase,
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
				const tasksWithPRs = tasksWithSession.filter(
					(t) => t.hasPR && t.prUrl && currentVisibleTaskIds.has(t.id),
				)
				let prStateMap = new Map<string, PRState>()
				if (tasksWithPRs.length > 0 && projectPath) {
					const prInfos = tasksWithPRs.map((t) => ({ prUrl: t.prUrl!, issueId: t.id }))
					prStateMap = yield* diagnostics.measure(
						{
							source: "BoardService",
							name: "prStates.get",
							thresholdMs: 300,
							details: `count=${tasksWithPRs.length}`,
						},
						prStateService.getPRStates(prInfos, projectPath ?? process.cwd()).pipe(
							Effect.withSpan("prStates.get"),
							Effect.catchAll((error) =>
								Effect.logWarning(error).pipe(
									Effect.zipRight(Effect.succeed(new Map<string, PRState>())),
								),
							),
						),
					)
					yield* Effect.log(
						`loadTasks: Fetched ${prStateMap.size}/${tasksWithPRs.length} PR states from gh CLI`,
					)
				}

				// Merge PR states into tasks
				const tasksWithPRState = tasksWithSession.map((task) => {
					const prState = prStateMap.get(task.id)
					return prState ? { ...task, prState } : task
				})

				const nowMs = DateTime.toEpochMillis(yield* DateTime.now)
				const currentTasks = yield* SubscriptionRef.get(tasks)
				const localCreateGraceExpiries = yield* Ref.get(localCreateGraceExpiriesRef)
				const { mergedTasks, nextLocalCreateGraceExpiries } =
					reconcileLoadedTasksWithLocalCreateGrace({
						loadedTasks: tasksWithPRState,
						currentTasks,
						localCreateGraceExpiries,
						nowMs,
					})
				yield* Ref.set(localCreateGraceExpiriesRef, new Map(nextLocalCreateGraceExpiries))

				// Debug: count tasks with parentEpicId set
				const tasksWithEpicParent = mergedTasks.filter((t) => t.parentEpicId !== undefined)
				if (tasksWithEpicParent.length > 0) {
					yield* Effect.logWarning(
						`loadTasks: ${tasksWithEpicParent.length} tasks have parentEpicId (will be hidden on main board). Sample: ${JSON.stringify(tasksWithEpicParent.slice(0, 3).map((t) => ({ id: t.id, parentEpicId: t.parentEpicId })))}`,
					)
				}

				// Debug: count by status
				const statusCounts = mergedTasks.reduce(
					(acc, t) => {
						acc[t.status] = (acc[t.status] || 0) + 1
						return acc
					},
					{} as Record<string, number>,
				)
				yield* Effect.log(
					`loadTasks: Complete in ${Date.now() - loadStartTime}ms. Total: ${mergedTasks.length}, by status: ${JSON.stringify(statusCounts)}`,
				)
				return mergedTasks
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

		const replaceTasks = (nextTasks: ReadonlyArray<TaskWithSession>) =>
			Effect.gen(function* () {
				yield* SubscriptionRef.set(tasks, nextTasks)
				yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(nextTasks))
				yield* updateFilteredTasks()
				yield* saveCurrentToMap()
			})

		const upsertTaskInMemory = (task: TaskWithSession) =>
			Effect.gen(function* () {
				const currentTasks = yield* SubscriptionRef.get(tasks)
				const nextTasks = [
					...currentTasks.filter((existing) => existing.id !== task.id),
					task,
				].sort(
					(left, right) =>
						new Date(right.updated_at).getTime() - new Date(left.updated_at).getTime(),
				)
				yield* replaceTasks(nextTasks)
			})

		const removeTaskFromMutation = (taskId: string) =>
			Effect.gen(function* () {
				const currentTasks = yield* SubscriptionRef.get(tasks)
				const nextTasks = currentTasks.filter((task) => task.id !== taskId)
				if (nextTasks.length === currentTasks.length) return
				yield* replaceTasks(nextTasks)
				yield* Ref.update(localCreateGraceExpiriesRef, (current) => {
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
				const currentTasks = yield* SubscriptionRef.get(tasks)
				let changed = false
				const nextTasks = currentTasks.map((task) => {
					if (task.id !== taskId) return task
					changed = true
					return { ...task, ...patch }
				})
				if (!changed) return
				yield* replaceTasks(nextTasks)
			})

		const toTaskFromMutationIssue = (
			issue: Issue,
			existingTask: TaskWithSession | undefined,
			options?: MutationTaskUpsertOptions,
		): TaskWithSession => {
			const prInfo = parsePRInfo(issue.notes)
			const hasPR = prInfo.hasPR === true
			const parentEpicId =
				options?.parentEpicId === undefined
					? existingTask?.parentEpicId
					: (options.parentEpicId ?? undefined)
			const retainedGitState = resolveRetainedTaskGitState({
				hasWorktree: existingTask?.hasWorktree,
				sessionState: existingTask?.sessionState ?? "idle",
				source: existingTask,
			})

			return {
				...issue,
				issue_type: toBoardIssueType(issue),
				sessionState: existingTask?.sessionState ?? "idle",
				hasTmuxSession: existingTask?.hasTmuxSession,
				hasWorktree: existingTask?.hasWorktree,
				hasMergeConflict: retainedGitState.hasMergeConflict,
				hasDevServer: existingTask?.hasDevServer,
				parentEpicId,
				...retainedGitState.gitStatus,
				sessionStartedAt: existingTask?.sessionStartedAt,
				estimatedTokens: existingTask?.estimatedTokens,
				recentOutput: existingTask?.recentOutput,
				agentPhase: existingTask?.agentPhase,
				hasPR: hasPR ? true : undefined,
				prUrl: hasPR ? prInfo.prUrl : undefined,
				prNumber: hasPR ? prInfo.prNumber : undefined,
				prState: hasPR ? existingTask?.prState : undefined,
			}
		}

		const upsertIssueFromMutation = (issue: Issue, options?: MutationTaskUpsertOptions) =>
			Effect.gen(function* () {
				const currentTasks = yield* SubscriptionRef.get(tasks)
				const existingTask = currentTasks.find((task) => task.id === issue.id)
				const nextTask = toTaskFromMutationIssue(issue, existingTask, options)
				yield* upsertTaskInMemory(nextTask)
				if (existingTask === undefined) {
					const nowMs = DateTime.toEpochMillis(yield* DateTime.now)
					yield* Ref.update(localCreateGraceExpiriesRef, (current) => {
						const next = new Map(current)
						next.set(issue.id, nowMs + LOCAL_CREATE_VISIBILITY_GRACE_MS)
						return next
					})
				}
			})

		const syncTaskFromBackend = (taskId: string, options?: MutationTaskUpsertOptions) =>
			issueTrackerClient
				.show(taskId)
				.pipe(Effect.flatMap((issue) => upsertIssueFromMutation(issue, options)))

		const refresh = (preferredProjectPath?: string | null) =>
			diagnostics
				.measure(
					{
						source: "BoardService",
						name: "refresh",
						thresholdMs: 500,
					},
					Effect.gen(function* () {
						yield* SubscriptionRef.set(isLoading, true)

						// Capture project path at refresh START
						const startProjectPath = yield* resolveProjectPath(preferredProjectPath)
						yield* Effect.log(
							`refresh: startProjectPath=${startProjectPath ?? "null"} preferredProjectPath=${preferredProjectPath ?? "null"}`,
						)

						// Update currentProjectPath SubscriptionRef
						yield* SubscriptionRef.set(currentProjectPath, startProjectPath)

						const loadedTasks = yield* diagnostics.measure(
							{
								source: "BoardService",
								name: "loadTasks",
								thresholdMs: 400,
							},
							loadTasks(startProjectPath).pipe(Effect.withSpan("board.loadTasks")),
						)

						// Verify project hasn't changed during refresh (race condition guard)
						// If project changed, discard results to avoid showing wrong project's data
						const activeProjectPath = yield* getCurrentBoardProjectPath()
						yield* Effect.log(
							`refresh: race-guard startProjectPath=${startProjectPath ?? "null"} activeProjectPath=${activeProjectPath ?? "null"}`,
						)
						if (startProjectPath !== activeProjectPath) {
							yield* Effect.log(
								`Refresh discarded: project changed from ${startProjectPath} to ${activeProjectPath}`,
							)
							return
						}

						yield* replaceTasks(loadedTasks)

						yield* Effect.log(
							`refresh: State updated, ${loadedTasks.length} tasks now in SubscriptionRefs`,
						)
					}).pipe(Effect.withSpan("board.refresh")),
				)
				.pipe(Effect.ensuring(SubscriptionRef.set(isLoading, false)))

		const formatRefreshFailureMessage = (cause: Cause.Cause<unknown>): string => {
			const pretty = Cause.pretty(cause)
			const firstLine = pretty
				.split("\n")
				.find((line) => line.trim().length > 0)
				?.trim()
			const base = firstLine && firstLine.length > 0 ? firstLine : "Unknown board refresh failure"
			return base.length > 180 ? `${base.slice(0, 177)}...` : base
		}

		const logAndToastRefreshFailure = (context: string, cause: Cause.Cause<unknown>) =>
			Effect.gen(function* () {
				yield* signalDaemonReconnect()
				yield* Effect.logError(`BoardService ${context} failed`, Cause.pretty(cause))
				const message = `Board refresh (${context}) failed: ${formatRefreshFailureMessage(cause)}`
				const now = Date.now()
				const previousToast = yield* Ref.get(refreshFailureToastRef)
				const shouldToast =
					previousToast === null ||
					previousToast.message !== message ||
					now - previousToast.timestamp >= REFRESH_FAILURE_TOAST_DEBOUNCE_MS

				if (shouldToast) {
					yield* toast.show("error", message)
					yield* Ref.set(refreshFailureToastRef, { message, timestamp: now })
				}
			}).pipe(Effect.asVoid)

		const toastWebhookFallback = (message: string) =>
			Effect.gen(function* () {
				const now = Date.now()
				const previousToast = yield* Ref.get(webhookFallbackToastRef)
				const shouldToast =
					previousToast === null ||
					previousToast.message !== message ||
					now - previousToast.timestamp >= WEBHOOK_FALLBACK_TOAST_DEBOUNCE_MS

				if (shouldToast) {
					yield* toast.show("warning", message)
					yield* Ref.set(webhookFallbackToastRef, { message, timestamp: now })
				}
			}).pipe(Effect.asVoid)

		/**
		 * Refresh with auto-recovery for database sync errors.
		 *
		 * If the tracker database is out of sync with JSONL (common after git pull
		 * or when another worktree modifies issues), this will:
		 * 1. Detect the SyncRequiredError
		 * 2. Auto-run import-only sync to re-import JSONL
		 * 3. Retry the refresh
		 */
		const refreshWithRecovery = (preferredProjectPath?: string | null) =>
			refresh(preferredProjectPath).pipe(
				Effect.catchIf(
					(error): error is SyncRequiredError => error._tag === "SyncRequiredError",
					() =>
						Effect.gen(function* () {
							yield* Effect.log(
								"IssueTracker database out of sync, auto-recovering with import-only sync...",
							)
							const projectPath = yield* resolveProjectPath(preferredProjectPath)
							yield* issueTrackerClient
								.syncImportOnly(projectPath ?? undefined)
								.pipe(
									Effect.catchAll((syncError) =>
										Effect.logError("Auto-sync recovery failed", String(syncError)),
									),
								)
							yield* Effect.log("Auto-sync complete, retrying refresh...")
							yield* refresh(preferredProjectPath)
						}),
				),
			)

		const refreshLocalSessionState = (params: {
			readonly preferredProjectPath?: string | null
			readonly includeGitStatus: boolean
		}) =>
			Effect.gen(function* () {
				const projectPath = yield* resolveProjectPath(params.preferredProjectPath)
				const activeSessions = yield* loadAuthoritativeSessions(projectPath)
				const sessionMap = new Map(activeSessions.map((session) => [session.issueId, session]))
				const tmuxSessionIssueIds = yield* loadTmuxSessionIssueIds(projectPath)
				const allMetrics = yield* SubscriptionRef.get(ptyMonitor.metrics)
				const currentTasks = yield* SubscriptionRef.get(tasks)
				const currentVisibleTaskIds = params.includeGitStatus
					? yield* SubscriptionRef.get(visibleTaskIds)
					: undefined
				const gitConfig = params.includeGitStatus
					? yield* getGitConfigForResolvedProject(projectPath)
					: undefined

				const nextTasks = yield* Effect.all(
					currentTasks.map((task) =>
						Effect.gen(function* () {
							const session = sessionMap.get(task.id)
							const metricsOpt = HashMap.get(allMetrics, task.id)
							const metrics = metricsOpt._tag === "Some" ? metricsOpt.value : undefined
							const sessionState = session?.state ?? "idle"
							const hasTmuxSession =
								session !== undefined || tmuxSessionIssueIds.has(normalizeIssueIdForLookup(task.id))
									? true
									: undefined
							const sessionStartedAt = session?.startedAt
								? DateTime.formatIso(session.startedAt)
								: undefined

							let gitStatusPatch: GitStatus | undefined = params.includeGitStatus
								? {
										gitBehindCount: undefined,
										hasUncommittedChanges: undefined,
										gitAdditions: undefined,
										gitDeletions: undefined,
									}
								: undefined
							if (
								params.includeGitStatus &&
								gitConfig !== undefined &&
								projectPath &&
								currentVisibleTaskIds?.has(task.id) &&
								(sessionState !== "idle" || task.hasWorktree === true)
							) {
								const worktreePath = getWorktreePath(projectPath, task.id)
								const effectiveBaseBranch = task.parentEpicId ?? gitConfig.baseBranch
								const cachedStatus = yield* getCachedGitStatus(
									worktreePath,
									effectiveBaseBranch,
									gitConfig.showLineChanges,
								)
								gitStatusPatch = {
									gitBehindCount: cachedStatus.gitBehindCount,
									hasUncommittedChanges: cachedStatus.hasUncommittedChanges,
									gitAdditions: cachedStatus.gitAdditions,
									gitDeletions: cachedStatus.gitDeletions,
								}
							}

							return applySessionRefreshPatch({
								task,
								sessionState,
								hasTmuxSession,
								sessionStartedAt,
								estimatedTokens: metrics?.estimatedTokens,
								recentOutput: metrics?.recentOutput,
								agentPhase: metrics?.agentPhase,
								gitStatusPatch,
							})
						}),
					),
					{ concurrency: 4 },
				)

				yield* SubscriptionRef.set(tasks, nextTasks)
				yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(nextTasks))
				yield* updateFilteredTasks()
				yield* saveCurrentToMap()
			})

		const refreshLocalSessionAndGitState = (preferredProjectPath?: string | null) =>
			refreshLocalSessionState({
				preferredProjectPath,
				includeGitStatus: true,
			})

		const refreshLocalSessionOnly = (preferredProjectPath?: string | null) =>
			refreshLocalSessionState({
				preferredProjectPath,
				includeGitStatus: false,
			})

		const refreshWithPolicy = (
			options?: BoardRefreshOptions,
			preferredProjectPath?: string | null,
		) =>
			Effect.gen(function* () {
				const localRefreshOnly = yield* Ref.get(localRefreshOnlyRef)
				const refreshMode = resolveBoardRefreshExecutionMode({
					localRefreshOnly,
					options,
				})
				const refreshEffect =
					refreshMode === "remote"
						? refreshWithRecovery(preferredProjectPath)
						: refreshMode === "local-session-only"
							? refreshLocalSessionOnly(preferredProjectPath)
							: refreshLocalSessionAndGitState(preferredProjectPath)
				yield* refreshSemaphore.withPermits(1)(refreshEffect)
				yield* signalDaemonHeartbeat()
			})

		const syncLinearProjectBeforeRefresh = (projectPath: string) =>
			Effect.gen(function* () {
				const syncConfigResult = yield* appConfig
					.getIssueTrackerSyncConfigForProjectPath(projectPath)
					.pipe(Effect.either)
				if (syncConfigResult._tag === "Left") {
					yield* Effect.logWarning(
						`Project switch Linear sync config load failed for ${projectPath}: ${syncConfigResult.left.message}`,
					)
					return
				}

				if (
					!shouldRunProjectSwitchLinearSync({
						backend: resolveConfiguredIssueBackend(syncConfigResult.right.issueTracker),
						syncEnabled: syncConfigResult.right.syncEnabled,
					})
				) {
					return
				}

				const backendSync = yield* backendSyncRouter.resolve()
				if (backendSync === undefined) {
					yield* Effect.logWarning(
						`Project switch Linear sync skipped for ${projectPath}: no backend sync runtime available`,
					)
					return
				}

				yield* backendSync.flushQueue(projectPath).pipe(
					Effect.tap((syncResult) =>
						Effect.log(
							`Project switch Linear sync: projectPath=${projectPath} pushed=${syncResult.pushed} pulled=${syncResult.pulled}`,
						),
					),
					Effect.catchAll((error) =>
						Effect.logWarning(
							`Project switch Linear sync failed for projectPath=${projectPath}: ${String(error)}`,
						).pipe(Effect.asVoid),
					),
				)
			})

		const requestRefresh = (options?: BoardRefreshOptions) =>
			Effect.gen(function* () {
				const existingFiber = yield* Ref.get(debounceFiberRef)
				if (existingFiber) {
					yield* Fiber.interrupt(existingFiber)
				}
				const localRefreshOnly = yield* Ref.get(localRefreshOnlyRef)
				const refreshMode = resolveBoardRefreshExecutionMode({
					localRefreshOnly,
					options,
				})
				// Fork into the service's scope (not daemon) so fiber is tied to service lifetime
				const fiber = yield* Effect.gen(function* () {
					yield* Effect.sleep("500 millis")
					yield* refreshWithPolicy(options).pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(cause).pipe(
								Effect.zipRight(
									logAndToastRefreshFailure(
										refreshMode === "remote"
											? "debounced"
											: refreshMode === "local-session-only"
												? "debounced-local-session"
												: "debounced-local",
										cause,
									),
								),
							),
						),
					)
				}).pipe(Effect.forkIn(serviceScope))
				yield* Ref.set(debounceFiberRef, fiber)
			})

		const isRemoveAction = (action: string): boolean => {
			const normalized = action.trim().toLowerCase()
			return normalized === "remove" || normalized === "delete" || normalized === "archive"
		}

		const applyLinearWebhookIssueEvent = (message: LinearIssueWebhookMessage) =>
			Effect.gen(function* () {
				const activeWebhookStatus = yield* SubscriptionRef.get(linearWebhookService.status)
				if (
					!shouldApplyLinearWebhookIssueEvent({
						eventConfigKey: message.configKey,
						activeConfigKey: activeWebhookStatus.configKey,
					})
				) {
					yield* Effect.logDebug(
						`Ignoring stale Linear webhook issue event for configKey=${message.configKey}; activeConfigKey=${activeWebhookStatus.configKey ?? "<none>"}`,
					)
					return
				}

				const event = message.payload
				const payload = event.data
				const issueId = payload.identifier
				const status = normalizeLinearWebhookStatus(payload.state.name)
				const labels = payload.labels.map((label) => label.name)
				const priority = normalizeLinearWebhookPriority(payload.priority)

				yield* Ref.update(linearIdentifierByEntityIdRef, (current) => {
					const next = new Map(current)
					next.set(payload.id, issueId)
					if (payload.parent && payload.parent.id.trim().length > 0) {
						next.set(payload.parent.id, payload.parent.identifier)
					}
					return next
				})
				const identifierByEntityId = yield* Ref.get(linearIdentifierByEntityIdRef)

				const currentTasks = yield* SubscriptionRef.get(tasks)
				const existingTask = currentTasks.find((task) => task.id === issueId)
				const withRemoved = currentTasks.filter((task) => task.id !== issueId)

				if (isRemoveAction(event.action)) {
					yield* SubscriptionRef.set(tasks, withRemoved)
					yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(withRemoved))
					yield* updateFilteredTasks()
					yield* saveCurrentToMap()
					return
				}

				let nextParentEpicId = existingTask?.parentEpicId
				if (payload.parentId === null || payload.parent === null) {
					nextParentEpicId = undefined
				} else if (payload.parent && payload.parent.identifier.trim().length > 0) {
					nextParentEpicId = payload.parent.identifier.trim()
				} else if (typeof payload.parentId === "string" && payload.parentId.trim().length > 0) {
					const parentEntityId = payload.parentId.trim()
					const mappedParentIdentifier = identifierByEntityId.get(parentEntityId)
					if (mappedParentIdentifier !== undefined) {
						nextParentEpicId = mappedParentIdentifier
					} else if (currentTasks.some((task) => task.id === parentEntityId)) {
						nextParentEpicId = parentEntityId
					} else if (existingTask?.parentEpicId !== undefined) {
						nextParentEpicId = existingTask.parentEpicId
					} else {
						yield* Effect.logDebug(
							`Linear webhook parent mapping unavailable for ${issueId}: parentId=${parentEntityId}`,
						)
					}
				}

				const hasChildrenInBoard = withRemoved.some((task) => task.parentEpicId === issueId)
				const inferredType = inferLinearIssueType(
					labels,
					hasChildrenInBoard || existingTask?.issue_type === "epic",
					undefined,
				)

				const updatedTask: TaskWithSession = existingTask
					? {
							...existingTask,
							title: payload.title,
							description: payload.description ?? undefined,
							status,
							priority,
							issue_type: inferredType,
							created_at: payload.createdAt,
							updated_at: payload.updatedAt,
							closed_at:
								status === "closed"
									? (payload.completedAt ?? payload.canceledAt ?? existingTask.closed_at)
									: undefined,
							labels,
							parentEpicId: nextParentEpicId,
						}
					: {
							id: issueId,
							title: payload.title,
							description: payload.description ?? undefined,
							status,
							priority,
							issue_type: inferredType,
							created_at: payload.createdAt,
							updated_at: payload.updatedAt,
							closed_at:
								status === "closed"
									? (payload.completedAt ?? payload.canceledAt ?? null)
									: undefined,
							implementations: ["default"],
							labels,
							parentEpicId: nextParentEpicId,
							sessionState: "idle",
						}

				const nextTasks = [...withRemoved, updatedTask].sort(
					(left, right) =>
						new Date(right.updated_at).getTime() - new Date(left.updated_at).getTime(),
				)

				yield* SubscriptionRef.set(tasks, nextTasks)
				yield* SubscriptionRef.set(tasksByColumn, groupTasksByColumn(nextTasks))
				yield* updateFilteredTasks()
				yield* saveCurrentToMap()
			})

		const getLinearWebhookListenerConfig = (options?: { readonly requireCliTransport?: boolean }) =>
			Effect.gen(function* () {
				const config = yield* SubscriptionRef.get(appConfig.config)
				if (!("linear" in config.issueTracker)) {
					return undefined
				}

				const linearConfig = config.issueTracker.linear
				const webhookConfig = linearConfig.webhooks
				const webhooksEnabled = webhookConfig.enabled
				if (webhooksEnabled === false) {
					return undefined
				}
				if ((options?.requireCliTransport ?? true) && webhookConfig.transport !== "cli") {
					return undefined
				}
				const port =
					Number.isInteger(webhookConfig.port) && webhookConfig.port > 0
						? webhookConfig.port
						: LINEAR_WEBHOOK_DEFAULT_PORT
				const team = linearConfig.team?.trim()

				const configuredUrl = normalizePublicBaseUrl(webhookConfig.url)
				const envUrl = yield* Config.option(Config.string(LINEAR_WEBHOOK_PUBLIC_URL_ENV)).pipe(
					Effect.map((value) =>
						Option.isSome(value) ? normalizePublicBaseUrl(value.value) : undefined,
					),
					Effect.catchAll(() => Effect.succeed(undefined)),
				)

				const url =
					configuredUrl ??
					envUrl ??
					(yield* Effect.gen(function* () {
						const tailscaleStatusResult = yield* Command.string(
							Command.make("tailscale", "status", "--json"),
						).pipe(
							Effect.timeout(`${LINEAR_WEBHOOK_TAILSCALE_STATUS_TIMEOUT_MS} millis`),
							Effect.catchAll(() => Effect.succeed(undefined)),
						)
						if (tailscaleStatusResult === undefined) {
							yield* Effect.logDebug(
								`Linear webhook listener config: tailscale status unavailable or timed out after ${LINEAR_WEBHOOK_TAILSCALE_STATUS_TIMEOUT_MS}ms`,
							)
							return undefined
						}
						const tailscaleStatus = tailscaleStatusResult.trim()

						const dnsName = parseTailscaleDnsName(tailscaleStatus)
						if (Option.isNone(dnsName)) {
							return undefined
						}

						const funnelExitCodeResult = yield* Command.exitCode(
							Command.make("tailscale", "funnel", "--bg", "--yes", String(port)),
						).pipe(
							Effect.timeout(`${LINEAR_WEBHOOK_TAILSCALE_FUNNEL_TIMEOUT_MS} millis`),
							Effect.catchAll(() => Effect.succeed(undefined)),
						)
						if (funnelExitCodeResult === undefined) {
							yield* Effect.logDebug(
								`Linear webhook listener config: tailscale funnel unavailable or timed out after ${LINEAR_WEBHOOK_TAILSCALE_FUNNEL_TIMEOUT_MS}ms`,
							)
							return undefined
						}
						const funnelExitCode = funnelExitCodeResult
						if (funnelExitCode !== 0) {
							return undefined
						}

						return `https://${dnsName.value}`
					}))

				if (!team || !url) {
					return undefined
				}

				const configuredEvents = webhookConfig.events
					?.map((eventType) => eventType.trim())
					.filter((eventType) => eventType.length > 0)
				const events =
					configuredEvents !== undefined && configuredEvents.length > 0
						? configuredEvents
						: LINEAR_WEBHOOK_DEFAULT_EVENTS

				const secret = webhookConfig.secret?.trim()

				return {
					command: linearConfig.command,
					team,
					url,
					port,
					events,
					secret: secret && secret.length > 0 ? secret : undefined,
				}
			})

		const runLinearWebhookListener = (listenerConfig: LinearWebhookListenerConfig) =>
			Effect.gen(function* () {
				const args = ["webhooks", "listen", "--json", "--events", listenerConfig.events.join(",")]
				args.push("--team", listenerConfig.team, "--url", listenerConfig.url)
				args.push("--port", String(listenerConfig.port), "--quiet", "--no-color")
				if (listenerConfig.secret !== undefined) {
					args.push("--secret", listenerConfig.secret)
				}

				const projectPath = yield* projectService.getCurrentPath()
				const command = projectPath
					? Command.make(listenerConfig.command, ...args).pipe(
							Command.workingDirectory(projectPath),
						)
					: Command.make(listenerConfig.command, ...args)

				yield* Stream.runForEach(Command.streamLines(command), (line) => {
					const trimmed = line.trim()
					if (trimmed.length === 0) {
						return Effect.void
					}
					if (!trimmed.startsWith("{")) {
						return Effect.logDebug(`Linear webhook listener: ${trimmed}`).pipe(Effect.asVoid)
					}
					return requestRefresh().pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(cause).pipe(
								Effect.zipRight(logAndToastRefreshFailure("linear-webhook", cause)),
							),
						),
					)
				})
			})

		const reportLinearWebhookHealth = (
			health: Omit<LinearWebhookHealth, "updatedAt">,
		): Effect.Effect<void, never> =>
			diagnostics
				.setLinearWebhookHealth({
					...health,
					updatedAt: new Date(),
				})
				.pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => void 0),
				)

		const startBackgroundPollingFiber = () =>
			Effect.gen(function* () {
				const backgroundPollingFiber = yield* Effect.forkScoped(
					Effect.repeat(Schedule.spaced(BOARD_BACKGROUND_POLL_INTERVAL))(
						Effect.gen(function* () {
							const projectPath = yield* projectService.getCurrentPath()
							if (projectPath !== null) {
								yield* issueTrackerClient.sync(projectPath).pipe(
									Effect.tap((syncResult) =>
										syncResult.pushed > 0 || syncResult.pulled > 0
											? Effect.log(
													`Background issue sync: projectPath=${projectPath} pushed=${syncResult.pushed} pulled=${syncResult.pulled}`,
												)
											: Effect.void,
									),
									Effect.catchAll((error) =>
										Effect.logWarning(
											`Background issue sync failed for projectPath=${projectPath}: ${String(error)}`,
										).pipe(Effect.asVoid),
									),
								)
							}

							yield* refreshWithPolicy({ forceRemote: true })
						}).pipe(
							Effect.catchAllCause((cause) =>
								Effect.logWarning(cause).pipe(
									Effect.zipRight(logAndToastRefreshFailure("background", cause)),
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
				return backgroundPollingFiber
			})

		const startLinearCliListenerFiber = (listenerConfig: LinearWebhookListenerConfig) =>
			Effect.gen(function* () {
				const linearWebhookFiber = yield* Effect.forkScoped(
					Effect.repeat(Schedule.spaced(LINEAR_WEBHOOK_RESTART_DELAY))(
						runLinearWebhookListener(listenerConfig).pipe(
							Effect.catchAllCause((cause) =>
								Cause.isInterruptedOnly(cause)
									? Effect.void
									: Effect.logWarning(
											`Linear CLI webhook listener exited: ${formatRefreshFailureMessage(cause)}. Restarting in ${LINEAR_WEBHOOK_RESTART_DELAY}.`,
										).pipe(Effect.asVoid),
							),
						),
					),
				)
				yield* diagnostics.registerFiber({
					id: "board-linear-webhook-cli-listener",
					name: "Board Linear CLI Webhook Listener",
					description: "Refreshes board from linear-cli webhook events",
					fiber: linearWebhookFiber,
				})
				return linearWebhookFiber
			})

		const startLinearSdkCliFallbackListenerFiber = (listenerConfig: LinearWebhookListenerConfig) =>
			Effect.gen(function* () {
				const linearWebhookFallbackFiber = yield* Effect.forkScoped(
					Effect.repeat(Schedule.spaced(LINEAR_WEBHOOK_RESTART_DELAY))(
						runLinearWebhookListener(listenerConfig).pipe(
							Effect.catchAllCause((cause) =>
								Cause.isInterruptedOnly(cause)
									? Effect.void
									: Effect.logWarning(
											`Linear SDK->CLI webhook fallback listener exited: ${formatRefreshFailureMessage(cause)}. Restarting in ${LINEAR_WEBHOOK_RESTART_DELAY}.`,
										).pipe(Effect.asVoid),
							),
						),
					),
				)
				yield* diagnostics.registerFiber({
					id: "board-linear-webhook-sdk-cli-fallback-listener",
					name: "Board Linear SDK CLI Fallback Listener",
					description: "Refreshes board from CLI webhook events when SDK mode is unhealthy",
					fiber: linearWebhookFallbackFiber,
				})
				return linearWebhookFallbackFiber
			})

		const runLinearSdkDefensiveReconciliationLoop = (interval: Duration.DurationInput) =>
			Effect.repeat(Schedule.spaced(interval))(
				Effect.gen(function* () {
					const projectPath = yield* projectService.getCurrentPath()
					if (projectPath !== null) {
						yield* issueTrackerClient.sync(projectPath).pipe(
							Effect.tap((syncResult) =>
								syncResult.pushed > 0 || syncResult.pulled > 0
									? Effect.log(
											`Linear SDK defensive reconciliation sync: projectPath=${projectPath} pushed=${syncResult.pushed} pulled=${syncResult.pulled}`,
										)
									: Effect.void,
							),
							Effect.catchAll((error) =>
								Effect.logWarning(
									`Linear SDK defensive reconciliation sync failed for projectPath=${projectPath}: ${String(error)}`,
								).pipe(Effect.asVoid),
							),
						)
					}

					yield* refreshWithPolicy({ forceRemote: true }).pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(
								`Linear SDK defensive reconciliation refresh failed: ${formatRefreshFailureMessage(cause)}`,
							).pipe(Effect.asVoid),
						),
					)
				}).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logWarning(
							`Linear SDK defensive reconciliation iteration failed: ${formatRefreshFailureMessage(cause)}`,
						).pipe(Effect.asVoid),
					),
				),
			)

		const startLinearSdkEventsFiber = (tickerBehavior: LinearSdkEventsTickerBehavior) =>
			Effect.gen(function* () {
				const linearWebhookFiber = yield* Effect.forkScoped(
					Effect.all(
						[
							Stream.runForEach(linearWebhookService.issueEvents, (event) =>
								applyLinearWebhookIssueEvent(event).pipe(
									Effect.catchAllCause((cause) =>
										Effect.logWarning(cause).pipe(
											Effect.zipRight(logAndToastRefreshFailure("linear-webhook-sdk", cause)),
										),
									),
								),
							),
							tickerBehavior.defensiveReconciliationInterval
								? runLinearSdkDefensiveReconciliationLoop(
										tickerBehavior.defensiveReconciliationInterval,
									)
								: Effect.void,
						],
						{
							concurrency: "unbounded",
							discard: true,
						},
					),
				)
				yield* diagnostics.registerFiber({
					id: "board-linear-webhook-sdk-events",
					name: "Board Linear SDK Webhook Events",
					description:
						"Applies Linear SDK webhook issue events and runs slow defensive reconciliation",
					fiber: linearWebhookFiber,
				})
				return linearWebhookFiber
			})

		const linearWebhookListenerConfigKey = (listenerConfig: LinearWebhookListenerConfig): string =>
			[
				listenerConfig.command,
				listenerConfig.team,
				listenerConfig.url,
				String(listenerConfig.port),
				listenerConfig.events.join(","),
				listenerConfig.secret ?? "",
			].join("|")

		const buildLinearRefreshStrategyPlan = () =>
			Effect.gen(function* () {
				const startupConfig = yield* SubscriptionRef.get(appConfig.config)
				const currentProjectPath = yield* projectService.getCurrentPath()
				const strategyScopeKey = encodeURIComponent((currentProjectPath ?? "").trim())
				yield* linearWebhookService.reconfigure()
				if (!("linear" in startupConfig.issueTracker)) {
					return {
						key: `non-linear:background-polling:${strategyScopeKey}`,
						start: Effect.gen(function* () {
							yield* Ref.set(localRefreshOnlyRef, false)
							yield* reportLinearWebhookHealth({
								mode: "disabled",
								strategy: "disabled",
								healthy: true,
								message: "Linear backend not active; using background polling.",
							})
							return yield* startBackgroundPollingFiber()
						}),
					} satisfies LinearRefreshStrategyPlan
				}

				const webhookConfig = startupConfig.issueTracker.linear.webhooks
				const transport = webhookConfig.transport

				if (webhookConfig.enabled === false) {
					return {
						key: `linear:disabled:${strategyScopeKey}`,
						start: Effect.gen(function* () {
							yield* Ref.set(localRefreshOnlyRef, false)
							yield* Effect.log(
								"Linear webhooks disabled in config, using background polling fallback",
							)
							yield* reportLinearWebhookHealth({
								mode: "disabled",
								strategy: "disabled",
								healthy: true,
								message: "Webhooks disabled in config; using background polling.",
							})
							return yield* startBackgroundPollingFiber()
						}),
					} satisfies LinearRefreshStrategyPlan
				}

				if (transport === "cli") {
					const linearWebhookListenerConfig = yield* getLinearWebhookListenerConfig()
					if (linearWebhookListenerConfig !== undefined) {
						return {
							key: `linear:cli-listener:${strategyScopeKey}:${linearWebhookListenerConfigKey(linearWebhookListenerConfig)}`,
							start: Effect.gen(function* () {
								yield* Ref.set(localRefreshOnlyRef, false)
								yield* reportLinearWebhookHealth({
									mode: "cli",
									strategy: "cli-listener",
									healthy: true,
									message: `Using linear-cli webhook listener (team=${linearWebhookListenerConfig.team}, url=${linearWebhookListenerConfig.url}).`,
								})
								return yield* startLinearCliListenerFiber(linearWebhookListenerConfig)
							}),
						} satisfies LinearRefreshStrategyPlan
					}

					return {
						key: `linear:cli-polling-fallback:${strategyScopeKey}`,
						start: Effect.gen(function* () {
							yield* Ref.set(localRefreshOnlyRef, false)
							yield* Effect.logWarning(
								"Linear CLI webhook listener configuration unavailable, using background polling fallback",
							)
							yield* reportLinearWebhookHealth({
								mode: "cli",
								strategy: "polling-fallback",
								healthy: false,
								message:
									"CLI webhook listener configuration unavailable; using background polling.",
							})
							yield* toastWebhookFallback(
								"Linear webhook listener configuration unavailable. Falling back to background polling.",
							)
							return yield* startBackgroundPollingFiber()
						}),
					} satisfies LinearRefreshStrategyPlan
				}

				const sdkStatus = yield* SubscriptionRef.get(linearWebhookService.status)
				const sdkMode = sdkStatus.mode
				const sdkHealthy = sdkStatus.healthy
				const sdkReason = normalizeLinearWebhookReason(sdkStatus.reason)
				const sdkTickerBehavior = resolveLinearSdkEventsTickerBehavior(sdkMode, sdkHealthy)
				if (sdkTickerBehavior.localRefreshOnly) {
					return {
						key: `linear:sdk-events:${strategyScopeKey}:${sdkStatus.configKey ?? "none"}`,
						start: Effect.gen(function* () {
							yield* Ref.set(localRefreshOnlyRef, sdkTickerBehavior.localRefreshOnly)
							yield* reportLinearWebhookHealth({
								mode: sdkMode,
								strategy: "sdk-events",
								healthy: true,
								message: "Using SDK webhook events with local refresh-only updates.",
							})
							return yield* startLinearSdkEventsFiber(sdkTickerBehavior)
						}),
					} satisfies LinearRefreshStrategyPlan
				}

				const listenerConfig = yield* getLinearWebhookListenerConfig({
					requireCliTransport: false,
				})
				if (listenerConfig !== undefined) {
					return {
						key: `linear:sdk-cli-fallback:${strategyScopeKey}:${sdkMode}:${String(sdkHealthy)}:${sdkStatus.configKey ?? "none"}:${linearWebhookReasonKey(sdkReason)}:${linearWebhookListenerConfigKey(listenerConfig)}`,
						start: Effect.gen(function* () {
							yield* Ref.set(localRefreshOnlyRef, false)
							yield* Effect.logWarning(
								`Linear SDK webhook mode is ${sdkMode} (healthy=${String(sdkHealthy)}${sdkReason ? ` reason=${sdkReason}` : ""}); attempting CLI webhook listener fallback`,
							)
							yield* reportLinearWebhookHealth({
								mode: sdkMode,
								strategy: "cli-fallback-listener",
								healthy: true,
								message:
									sdkReason === undefined
										? `SDK mode=${sdkMode} healthy=${String(sdkHealthy)}; using CLI webhook listener fallback.`
										: `SDK mode=${sdkMode} healthy=${String(sdkHealthy)} reason=${sdkReason}; using CLI webhook listener fallback.`,
							})
							return yield* startLinearSdkCliFallbackListenerFiber(listenerConfig)
						}),
					} satisfies LinearRefreshStrategyPlan
				}

				return {
					key: `linear:sdk-polling-fallback:${strategyScopeKey}:${sdkMode}:${String(sdkHealthy)}:${sdkStatus.configKey ?? "none"}:${linearWebhookReasonKey(sdkReason)}`,
					start: Effect.gen(function* () {
						yield* Ref.set(localRefreshOnlyRef, false)
						yield* Effect.logWarning(
							`Linear SDK webhook mode is ${sdkMode} (healthy=${String(sdkHealthy)}${sdkReason ? ` reason=${sdkReason}` : ""}), using background polling fallback`,
						)
						yield* reportLinearWebhookHealth({
							mode: sdkMode,
							strategy: "polling-fallback",
							healthy: false,
							message: resolveLinearSdkPollingFallbackHealthMessage({
								mode: sdkMode,
								healthy: sdkHealthy,
								reason: sdkReason,
							}),
						})
						yield* toastWebhookFallback(
							resolveLinearSdkPollingFallbackToastMessage({
								mode: sdkMode,
								reason: sdkReason,
							}),
						)
						return yield* startBackgroundPollingFiber()
					}),
				} satisfies LinearRefreshStrategyPlan
			})

		const applyLinearRefreshStrategy = () =>
			Effect.gen(function* () {
				const nextPlan = yield* buildLinearRefreshStrategyPlan()
				const activeStrategy = yield* Ref.get(linearRefreshStrategyRef)
				if (activeStrategy?.key === nextPlan.key) {
					return
				}

				if (activeStrategy !== null) {
					yield* Fiber.interrupt(activeStrategy.fiber).pipe(
						Effect.catchAllCause((cause) =>
							Cause.isInterruptedOnly(cause)
								? Effect.void
								: Effect.logWarning(
										`Failed to stop previous linear refresh strategy: ${formatRefreshFailureMessage(cause)}`,
									).pipe(Effect.asVoid),
						),
					)
				}

				const nextFiber = yield* nextPlan.start
				yield* Ref.set(linearRefreshStrategyRef, {
					key: nextPlan.key,
					fiber: nextFiber,
				})
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

				const projectPath = yield* getCurrentBoardProjectPath()
				if (!projectPath) {
					return
				}

				const gitConfig = yield* getGitConfigForResolvedProject(projectPath)
				const { baseBranch, showLineChanges } = gitConfig
				const currentTasks = yield* SubscriptionRef.get(tasks)
				const currentVisibleTaskIds = yield* SubscriptionRef.get(visibleTaskIds)

				// Update git stats for all tasks with active sessions
				const updatedTasks = yield* Effect.all(
					currentTasks.map((task) =>
						Effect.gen(function* () {
							if (!currentVisibleTaskIds.has(task.id)) {
								return task
							}
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

				yield* replaceTasks(updatedTasks)
			}).pipe(Effect.ensuring(SubscriptionRef.set(isRefreshingGitStats, false)))

		yield* signalDaemonAttach()
		yield* Effect.forkScoped(
			Effect.gen(function* () {
				const cursor = yield* Ref.get(daemonStreamCursorRef)
				const nextCursor = yield* consumeDaemonStreamBatch(cursor)
				yield* Ref.set(daemonStreamCursorRef, nextCursor)
			}).pipe(Effect.repeat(Schedule.spaced(Duration.seconds(2)))),
		)

		const initialProjectPath = yield* projectService.getCurrentPath()
		if (initialProjectPath) {
			yield* SubscriptionRef.set(currentProjectPath, initialProjectPath)
			const cached = yield* loadBoardProjection(initialProjectPath).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed([])),
					),
				),
			)
			if (cached.length > 0) {
				yield* replaceTasks(cached)
			}
		}

		const startupBootstrapFiber = yield* Effect.forkScoped(
			Effect.gen(function* () {
				yield* refreshWithPolicy({ reason: "initial-load", forceRemote: true }).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logWarning(`Recovering after caught error: ${String(cause)}`).pipe(
							Effect.zipRight(logAndToastRefreshFailure("initial", cause)),
						),
					),
				)

				yield* applyLinearRefreshStrategy().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logWarning(
							`Failed to initialize linear refresh strategy: ${formatRefreshFailureMessage(cause)}`,
						).pipe(Effect.asVoid),
					),
				)
			}),
		)
		yield* diagnostics.registerFiber({
			id: "board-startup-bootstrap",
			name: "Board Startup Bootstrap",
			description: "Performs initial board refresh and webhook strategy bootstrap asynchronously",
			fiber: startupBootstrapFiber,
		})

		const webhookConfigChangesFiber = yield* Effect.forkScoped(
			appConfig.config.changes.pipe(
				Stream.drop(1),
				Stream.runForEach(() =>
					applyLinearRefreshStrategy().pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(
								`Failed to reconfigure webhook strategy: ${formatRefreshFailureMessage(cause)}`,
							).pipe(Effect.asVoid),
						),
					),
				),
			),
		)
		yield* diagnostics.registerFiber({
			id: "board-webhook-strategy-config-changes",
			name: "Board Webhook Strategy Config Changes",
			description: "Reconciles webhook/polling strategy when app config changes",
			fiber: webhookConfigChangesFiber,
		})

		const ptyRefreshFiber = yield* Effect.forkScoped(
			Stream.runForEach(ptyMonitor.metrics.changes, () =>
				requestRefresh({ reason: "pty" }).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logWarning(cause).pipe(
							Effect.zipRight(logAndToastRefreshFailure("pty-triggered", cause)),
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
					yield* persistBoardProjection(projectPath, currentTasks)
				}
			})

		const loadFromCache = (projectPath: string) =>
			Effect.gen(function* () {
				const cached = yield* loadBoardProjection(projectPath)
				if (cached.length > 0) {
					yield* replaceTasks(cached)
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

		const setVisibleTaskIds = (taskIds: ReadonlySet<string>) =>
			SubscriptionRef.set(visibleTaskIds, taskIds)

		/**
		 * Apply an optimistic move directly to in-memory state.
		 * This provides instant UI feedback without waiting for refresh.
		 */
		const applyOptimisticMove = (taskId: string, newStatus: ColumnStatus) =>
			Effect.gen(function* () {
				yield* patchTaskFromMutation(taskId, {
					status: newStatus,
					updated_at: new Date().toISOString(),
				})
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

				yield* projectService
					.switchProjectPath(newProjectPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(
								`Failed to sync ProjectService before project switch: ${String(error)}`,
							).pipe(Effect.asVoid),
						),
					)

				yield* appConfig
					.reload()
					.pipe(
						Effect.catchAllCause((cause) =>
							Effect.logWarning(
								`Project switch config reload failed before cache/refresh handoff: ${formatRefreshFailureMessage(cause)}`,
							).pipe(Effect.asVoid),
						),
					)

				// Update the current project path
				yield* SubscriptionRef.set(currentProjectPath, newProjectPath)
				yield* signalDaemonHeartbeat()

				const cacheHit = yield* loadFromCache(newProjectPath)
				if (!cacheHit) {
					yield* clearBoard()
				}

				yield* applyLinearRefreshStrategy().pipe(
					Effect.catchAllCause((cause) =>
						Effect.logWarning(
							`Failed to reconfigure webhook strategy after project switch: ${formatRefreshFailureMessage(cause)}`,
						).pipe(Effect.asVoid),
					),
				)

				// Fork the refresh into the service's scope - not a daemon fiber
				yield* Effect.gen(function* () {
					yield* syncLinearProjectBeforeRefresh(newProjectPath)
					yield* refreshWithPolicy({ reason: "project-switch", forceRemote: true }, newProjectPath)
					yield* onRefreshComplete
				}).pipe(
					Effect.catchAllCause((cause) =>
						Effect.logWarning(cause).pipe(
							Effect.zipRight(logAndToastRefreshFailure("project-switch", cause)),
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
			setVisibleTaskIds,
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
			refresh: (options?: BoardRefreshOptions) => refreshWithPolicy(options),
			requestRefresh,
			refreshGitStats,
			removeTaskFromMutation,
			patchTaskFromMutation,
			upsertIssueFromMutation,
			syncTaskFromBackend,
			clearBoard,
			saveToCache,
			loadFromCache,
			switchToProject,
			applyOptimisticMove,
			initialize: () => refreshWithPolicy({ reason: "initial-load", forceRemote: true }),
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
