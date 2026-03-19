import { AppConfig } from "@azedarach/config"
import { Data, Duration, Effect, Ref, Schedule } from "effect"
import type { TaskWithSession } from "../../../src/ui/types.js"
import {
	BackendDaemonService,
	type BackendDaemonServiceApi,
	type BackendDaemonSnapshot,
} from "./BackendDaemonService.js"
import {
	BackendSyncDaemonService,
	type BackendSyncDaemonServiceApi,
	type BackendSyncDaemonStatus,
} from "./BackendSyncDaemonService.js"
import {
	type DevServerDaemonListRequest,
	type DevServerDaemonListResult,
	type DevServerDaemonMutationResult,
	DevServerDaemonService,
	type DevServerDaemonServiceApi,
	type DevServerDaemonStatusRequest,
	type DevServerDaemonStatusResult,
} from "./DevServerDaemonService.js"
import { LocalIssueStore, SessionManager } from "./runtimeServices.js"

export interface BackendDaemonControlStatus {
	readonly checkedAtMs: number
	readonly runtime: BackendDaemonSnapshot
	readonly sync: BackendSyncDaemonStatus
}

export type BackendDaemonControlHealthState = "healthy" | "degraded" | "unhealthy"

export interface BackendDaemonControlHealth {
	readonly checkedAtMs: number
	readonly state: BackendDaemonControlHealthState
	readonly reason: string
	readonly status: BackendDaemonControlStatus
}

export interface BackendDaemonControlRestartOptions {
	readonly projectPath?: string
	readonly intervalMs?: number
}

export type BackendDaemonControlQueueDomain = "command" | "mutation"

export type BackendDaemonControlQueueItemState =
	| "queued"
	| "running"
	| "done"
	| "failed"
	| "cancelled"

export interface BackendDaemonControlQueueItem {
	readonly domain: BackendDaemonControlQueueDomain
	readonly operationId: string
	readonly operation: string
	readonly projectPath: string
	readonly issueId: string | null
	readonly dedupeKey: string | null
	readonly payloadJson: string | null
	readonly state: BackendDaemonControlQueueItemState
	readonly enqueuedAtMs: number
	readonly startedAtMs: number | null
	readonly finishedAtMs: number | null
	readonly error: string | null
}

export interface BackendDaemonControlQueueEnqueueRequest {
	readonly domain: BackendDaemonControlQueueDomain
	readonly operation: string
	readonly projectPath: string
	readonly issueId?: string
	readonly dedupeKey?: string
	readonly payloadJson?: string
}

export interface BackendDaemonControlQueueEnqueueResult {
	readonly acceptedAtMs: number
	readonly item: BackendDaemonControlQueueItem
}

export interface BackendDaemonControlQueueQueryRequest {
	readonly projectPath: string
	readonly domain?: BackendDaemonControlQueueDomain
	readonly operationId?: string
	readonly issueId?: string
	readonly limit?: number
}

export interface BackendDaemonControlQueueQueryResult {
	readonly queriedAtMs: number
	readonly items: ReadonlyArray<BackendDaemonControlQueueItem>
}

export interface BackendDaemonControlQueueCancelRequest {
	readonly projectPath: string
	readonly domain?: BackendDaemonControlQueueDomain
	readonly operationId?: string
	readonly issueId?: string
}

export interface BackendDaemonControlQueueCancelResult {
	readonly cancelledAtMs: number
	readonly cancelledOperationIds: ReadonlyArray<string>
}

export interface BackendDaemonControlBoardReadModelRequest {
	readonly projectPath: string
}

export interface BackendDaemonControlBoardReadModelResult {
	readonly capturedAtMs: number
	readonly projectPath: string
	readonly tasks: ReadonlyArray<TaskWithSession>
}

export interface BackendDaemonControlServiceApi {
	readonly status: () => Effect.Effect<BackendDaemonControlStatus>
	readonly health: () => Effect.Effect<BackendDaemonControlHealth>
	readonly stop: () => Effect.Effect<BackendDaemonControlStatus>
	readonly restart: (
		options: BackendDaemonControlRestartOptions,
	) => Effect.Effect<BackendDaemonControlStatus, BackendDaemonControlRestartConfigurationError>
	readonly queueEnqueue: (
		request: BackendDaemonControlQueueEnqueueRequest,
	) => Effect.Effect<BackendDaemonControlQueueEnqueueResult>
	readonly queueQuery: (
		request?: BackendDaemonControlQueueQueryRequest,
	) => Effect.Effect<BackendDaemonControlQueueQueryResult>
	readonly queueCancel: (
		request?: BackendDaemonControlQueueCancelRequest,
	) => Effect.Effect<BackendDaemonControlQueueCancelResult>
	readonly boardReadModel: (
		request: BackendDaemonControlBoardReadModelRequest,
	) => Effect.Effect<BackendDaemonControlBoardReadModelResult>
	readonly devServerStatus: (
		request: DevServerDaemonStatusRequest,
	) => Effect.Effect<DevServerDaemonStatusResult>
	readonly devServerList: (
		request?: DevServerDaemonListRequest,
	) => Effect.Effect<DevServerDaemonListResult>
	readonly devServerStart: (
		request: Parameters<DevServerDaemonServiceApi["start"]>[0],
	) => Effect.Effect<DevServerDaemonMutationResult>
	readonly devServerStop: (
		request: Parameters<DevServerDaemonServiceApi["stop"]>[0],
	) => Effect.Effect<DevServerDaemonMutationResult>
}

export class BackendDaemonControlRestartConfigurationError extends Data.TaggedError(
	"BackendDaemonControlRestartConfigurationError",
)<{
	readonly reason: "missing-project-path"
	readonly daemonSyncState: BackendSyncDaemonStatus["state"]
}> {}

const SESSION_RECOVERY_POLL_INTERVAL = "2 seconds"

const isTransientSessionRecoveryError = (error: unknown): boolean => {
	if (typeof error !== "object" || error === null) {
		return false
	}

	const tagged = Reflect.get(error, "_tag")
	if (typeof tagged !== "string") {
		return false
	}

	switch (tagged) {
		case "TmuxError":
		case "ShellNotReadyError":
		case "SessionLimitError":
			return true
		case "SessionNotFoundError":
			return typeof Reflect.get(error, "session") === "string"
		default:
			return false
	}
}

const isSessionWorktreeMissingError = (error: unknown): boolean =>
	typeof error === "object" &&
	error !== null &&
	Reflect.get(error, "_tag") === "SessionWorktreeMissingError"

const makeDaemonSessionRecoverySchedule = (params: {
	readonly retryBaseDelayMs: number
	readonly retryMaxDelayMs: number
}) =>
	Schedule.exponential(Duration.millis(Math.max(1, Math.trunc(params.retryBaseDelayMs)))).pipe(
		Schedule.jittered,
		Schedule.modifyDelay((_output, duration) =>
			Duration.min(duration, Duration.millis(Math.max(1, Math.trunc(params.retryMaxDelayMs)))),
		),
		Schedule.whileInput(isTransientSessionRecoveryError),
		Schedule.intersect(Schedule.recurs(5)),
	)

const readStatus = (
	runtime: BackendDaemonServiceApi,
	sync: BackendSyncDaemonServiceApi,
): Effect.Effect<BackendDaemonControlStatus> =>
	Effect.gen(function* () {
		const [runtimeSnapshot, syncStatus] = yield* Effect.all([runtime.snapshot(), sync.getStatus()])
		return {
			checkedAtMs: Date.now(),
			runtime: runtimeSnapshot,
			sync: syncStatus,
		} satisfies BackendDaemonControlStatus
	})

const deriveHealth = (status: BackendDaemonControlStatus): BackendDaemonControlHealth => {
	if (status.sync.state === "crashed" || status.runtime.runtimePhase === "crashed") {
		return {
			checkedAtMs: status.checkedAtMs,
			state: "unhealthy",
			reason: "daemon runtime is crashed",
			status,
		}
	}
	if (status.sync.state === "degraded" || status.runtime.runtimePhase === "degraded") {
		return {
			checkedAtMs: status.checkedAtMs,
			state: "degraded",
			reason: "daemon runtime is degraded",
			status,
		}
	}
	if (status.sync.state !== "running" || status.runtime.runtimePhase !== "ready") {
		return {
			checkedAtMs: status.checkedAtMs,
			state: "degraded",
			reason: "daemon runtime is not fully ready",
			status,
		}
	}
	return {
		checkedAtMs: status.checkedAtMs,
		state: "healthy",
		reason: "daemon runtime is healthy",
		status,
	}
}

export const makeBackendDaemonControlService = (params: {
	readonly runtime: BackendDaemonServiceApi
	readonly sync: BackendSyncDaemonServiceApi
	readonly devServer: DevServerDaemonServiceApi
	readonly readBoardTasks?: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<TaskWithSession>, unknown>
}): BackendDaemonControlServiceApi => {
	const queueItems = new Map<string, BackendDaemonControlQueueItem>()

	const sortBoardTasksForReadModel = (
		tasks: ReadonlyArray<TaskWithSession>,
	): ReadonlyArray<TaskWithSession> =>
		[...tasks].sort((left, right) => Date.parse(right.updated_at) - Date.parse(left.updated_at))

	const queueMatches = (
		item: BackendDaemonControlQueueItem,
		request:
			| BackendDaemonControlQueueQueryRequest
			| BackendDaemonControlQueueCancelRequest
			| undefined,
	): boolean => {
		if (request?.projectPath !== undefined && item.projectPath !== request.projectPath) {
			return false
		}
		if (request?.domain !== undefined && item.domain !== request.domain) {
			return false
		}
		if (request?.operationId !== undefined && item.operationId !== request.operationId) {
			return false
		}
		if (request?.issueId !== undefined && item.issueId !== request.issueId) {
			return false
		}
		return true
	}

	return {
		status: () => readStatus(params.runtime, params.sync),
		health: () => readStatus(params.runtime, params.sync).pipe(Effect.map(deriveHealth)),
		stop: () => params.sync.stop().pipe(Effect.zipRight(readStatus(params.runtime, params.sync))),
		restart: (options: BackendDaemonControlRestartOptions) =>
			Effect.gen(function* () {
				const previousSyncStatus = yield* params.sync.getStatus()
				const projectPath = options.projectPath ?? previousSyncStatus.projectPath
				if (projectPath === null) {
					return yield* Effect.fail(
						new BackendDaemonControlRestartConfigurationError({
							reason: "missing-project-path",
							daemonSyncState: previousSyncStatus.state,
						}),
					)
				}

				const intervalMs = options.intervalMs ?? previousSyncStatus.intervalMs ?? undefined
				yield* params.sync.stop()
				yield* params.runtime.markRuntimeRestart(Date.now())
				yield* params.sync.start({
					projectPath,
					...(intervalMs === undefined ? {} : { intervalMs }),
				})
				return yield* readStatus(params.runtime, params.sync)
			}),
		queueEnqueue: (request) =>
			Effect.sync(() => {
				const acceptedAtMs = Date.now()
				const item: BackendDaemonControlQueueItem = {
					domain: request.domain,
					operationId: crypto.randomUUID(),
					operation: request.operation,
					projectPath: request.projectPath,
					issueId: request.issueId ?? null,
					dedupeKey: request.dedupeKey ?? null,
					payloadJson: request.payloadJson ?? null,
					state: "queued",
					enqueuedAtMs: acceptedAtMs,
					startedAtMs: null,
					finishedAtMs: null,
					error: null,
				}
				queueItems.set(item.operationId, item)
				return {
					acceptedAtMs,
					item,
				} satisfies BackendDaemonControlQueueEnqueueResult
			}),
		queueQuery: (request) =>
			Effect.sync(() => {
				const items = [...queueItems.values()].filter((item) => queueMatches(item, request))
				const limit = request?.limit
				return {
					queriedAtMs: Date.now(),
					items: limit === undefined ? items : items.slice(0, Math.max(limit, 0)),
				} satisfies BackendDaemonControlQueueQueryResult
			}),
		queueCancel: (request) =>
			Effect.sync(() => {
				const cancelledOperationIds: Array<string> = []
				for (const item of queueItems.values()) {
					if (!queueMatches(item, request)) {
						continue
					}
					if (item.state !== "queued") {
						continue
					}
					queueItems.set(item.operationId, {
						...item,
						state: "cancelled",
						finishedAtMs: Date.now(),
					})
					cancelledOperationIds.push(item.operationId)
				}
				return {
					cancelledAtMs: Date.now(),
					cancelledOperationIds,
				} satisfies BackendDaemonControlQueueCancelResult
			}),
		boardReadModel: (request) =>
			(params.readBoardTasks === undefined
				? Effect.succeed<ReadonlyArray<TaskWithSession>>([])
				: params.readBoardTasks(request.projectPath)
			).pipe(
				Effect.map((tasks) => ({
					capturedAtMs: Date.now(),
					projectPath: request.projectPath,
					tasks: sortBoardTasksForReadModel(tasks),
				})),
				Effect.catchAll((error) =>
					Effect.logWarning(
						`Daemon board read model failed for projectPath=${request.projectPath}: ${String(error)}`,
					).pipe(
						Effect.as({
							capturedAtMs: Date.now(),
							projectPath: request.projectPath,
							tasks: [],
						} satisfies BackendDaemonControlBoardReadModelResult),
					),
				),
			),
		devServerStatus: (request) => params.devServer.status(request),
		devServerList: (request) => params.devServer.list(request),
		devServerStart: (request) => params.devServer.start(request),
		devServerStop: (request) => params.devServer.stop(request),
	}
}

export class BackendDaemonControlService extends Effect.Service<BackendDaemonControlService>()(
	"BackendDaemonControlService",
	{
		dependencies: [
			BackendDaemonService.Default,
			BackendSyncDaemonService.Default,
			DevServerDaemonService.Default,
			SessionManager.Default,
			AppConfig.Default,
			LocalIssueStore.Default,
		],
		effect: Effect.gen(function* () {
			const runtime = yield* BackendDaemonService
			const sync = yield* BackendSyncDaemonService
			const devServer = yield* DevServerDaemonService
			const localIssueStore = yield* LocalIssueStore
			const sessionManager = yield* SessionManager
			const appConfig = yield* AppConfig
			const recoveryInFlightRef = yield* Ref.make<ReadonlySet<string>>(new Set())

			const markRecoveryInFlight = (issueId: string): Effect.Effect<boolean> =>
				Ref.modify(recoveryInFlightRef, (current): [boolean, ReadonlySet<string>] => {
					if (current.has(issueId)) {
						return [false, current]
					}
					const next = new Set(current)
					next.add(issueId)
					return [true, next]
				})

			const clearRecoveryInFlight = (issueId: string): Effect.Effect<void> =>
				Ref.update(recoveryInFlightRef, (current) => {
					if (!current.has(issueId)) {
						return current
					}
					const next = new Set(current)
					next.delete(issueId)
					return next
				})

			const recoverIssueFromDaemonWorker = (issueId: string, projectPath: string) =>
				Effect.gen(function* () {
					const shouldRun = yield* markRecoveryInFlight(issueId)
					if (!shouldRun) {
						return
					}

					const recoveryConfig = yield* appConfig.getSessionRecoveryConfig()
					const schedule = makeDaemonSessionRecoverySchedule({
						retryBaseDelayMs: recoveryConfig.retryBaseDelayMs,
						retryMaxDelayMs: recoveryConfig.retryMaxDelayMs,
					})
					const autoRecoveryDelayMs = Math.max(0, Math.floor(recoveryConfig.autoRecoveryDelayMs))

					yield* Effect.sleep(`${autoRecoveryDelayMs} millis`).pipe(
						Effect.zipRight(sessionManager.recoverSession(issueId)),
						Effect.retry({ schedule }),
						Effect.tap(() =>
							Effect.log(
								`Daemon session recovery completed for ${issueId} (projectPath=${projectPath})`,
							),
						),
						Effect.catchAll((error) =>
							Effect.gen(function* () {
								if (isSessionWorktreeMissingError(error)) {
									yield* Effect.logWarning(
										`Daemon session recovery terminal failure for ${issueId} (projectPath=${projectPath}): worktree missing; resetting session state to idle`,
									)
									yield* sessionManager
										.updateState(issueId, "idle")
										.pipe(Effect.catchAll(() => Effect.void))
									return
								}

								yield* Effect.logWarning(
									`Daemon session recovery failed for ${issueId} (projectPath=${projectPath}): ${String(error)}`,
								)
							}),
						),
						Effect.ensuring(clearRecoveryInFlight(issueId)),
					)
				})

			const runDaemonRecoverySweep = Effect.gen(function* () {
				const status = yield* sync.getStatus()
				const projectPath = status.projectPath
				if (projectPath === null) {
					return
				}

				const recoveryConfig = yield* appConfig.getSessionRecoveryConfig()
				if (recoveryConfig.mode !== "auto") {
					return
				}

				const sessions = yield* sessionManager.listActive(projectPath)
				const crashedIssueIds = Array.from(
					new Set(
						sessions
							.filter((session) => session.state === "crashed")
							.map((session) => session.issueId),
					),
				)
				for (const issueId of crashedIssueIds) {
					yield* Effect.forkDaemon(recoverIssueFromDaemonWorker(issueId, projectPath))
				}
			}).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Daemon session recovery sweep failed: ${String(error)}`),
				),
				Effect.asVoid,
			)

			yield* Effect.forkDaemon(
				Effect.repeat(runDaemonRecoverySweep, Schedule.spaced(SESSION_RECOVERY_POLL_INTERVAL)),
			)

			return makeBackendDaemonControlService({
				runtime,
				sync,
				devServer,
				readBoardTasks: (projectPath) => localIssueStore.listBoardTasks(projectPath),
			})
		}),
	},
) {}
