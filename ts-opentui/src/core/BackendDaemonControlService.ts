import { Data, Effect } from "effect"
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
	readonly projectPath: string | null
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
	readonly projectPath?: string
	readonly issueId?: string
	readonly dedupeKey?: string
	readonly payloadJson?: string
}

export interface BackendDaemonControlQueueEnqueueResult {
	readonly acceptedAtMs: number
	readonly item: BackendDaemonControlQueueItem
}

export interface BackendDaemonControlQueueQueryRequest {
	readonly domain?: BackendDaemonControlQueueDomain
	readonly operationId?: string
	readonly projectPath?: string
	readonly issueId?: string
	readonly limit?: number
}

export interface BackendDaemonControlQueueQueryResult {
	readonly queriedAtMs: number
	readonly items: ReadonlyArray<BackendDaemonControlQueueItem>
}

export interface BackendDaemonControlQueueCancelRequest {
	readonly domain?: BackendDaemonControlQueueDomain
	readonly operationId?: string
	readonly projectPath?: string
	readonly issueId?: string
}

export interface BackendDaemonControlQueueCancelResult {
	readonly cancelledAtMs: number
	readonly cancelledOperationIds: ReadonlyArray<string>
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
}

export class BackendDaemonControlRestartConfigurationError extends Data.TaggedError(
	"BackendDaemonControlRestartConfigurationError",
)<{
	readonly reason: "missing-project-path"
	readonly daemonSyncState: BackendSyncDaemonStatus["state"]
}> {}

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
}): BackendDaemonControlServiceApi => {
	const queueItems = new Map<string, BackendDaemonControlQueueItem>()

	const queueMatches = (
		item: BackendDaemonControlQueueItem,
		request:
			| BackendDaemonControlQueueQueryRequest
			| BackendDaemonControlQueueCancelRequest
			| undefined,
	): boolean => {
		if (request?.domain !== undefined && item.domain !== request.domain) {
			return false
		}
		if (request?.operationId !== undefined && item.operationId !== request.operationId) {
			return false
		}
		if (request?.projectPath !== undefined && item.projectPath !== request.projectPath) {
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
					projectPath: request.projectPath ?? null,
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
	}
}

export class BackendDaemonControlService extends Effect.Service<BackendDaemonControlService>()(
	"BackendDaemonControlService",
	{
		dependencies: [BackendDaemonService.Default, BackendSyncDaemonService.Default],
		effect: Effect.gen(function* () {
			const runtime = yield* BackendDaemonService
			const sync = yield* BackendSyncDaemonService
			return makeBackendDaemonControlService({
				runtime,
				sync,
			})
		}),
	},
) {}
