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

export interface BackendDaemonControlServiceApi {
	readonly status: () => Effect.Effect<BackendDaemonControlStatus>
	readonly health: () => Effect.Effect<BackendDaemonControlHealth>
	readonly stop: () => Effect.Effect<BackendDaemonControlStatus>
	readonly restart: (
		options: BackendDaemonControlRestartOptions,
	) => Effect.Effect<BackendDaemonControlStatus, BackendDaemonControlRestartConfigurationError>
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
}): BackendDaemonControlServiceApi => ({
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
})

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
