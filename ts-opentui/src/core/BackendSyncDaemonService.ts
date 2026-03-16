import { CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Effect, Fiber, Option, Ref, type Scope } from "effect"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { BackendSyncRouter } from "./BackendSyncRouter.js"
import {
	type DaemonStateStoreApi,
	makeDaemonStateStore,
	toDaemonStatus,
} from "./DaemonStateStore.js"
import { SessionManager } from "./SessionManager.js"

const DEFAULT_INTERVAL_MS = 5_000
const MIN_INTERVAL_MS = 50
const DEGRADED_FAILURE_STREAK_THRESHOLD = 2
const CRASHED_FAILURE_STREAK_THRESHOLD = 4
const MAX_FAILURE_BACKOFF_MS = 200

export type BackendSyncDaemonRunResult = "flushed" | "skipped" | "failed"

export interface BackendSyncDaemonRunStatus {
	readonly runAtMs: number
	readonly result: BackendSyncDaemonRunResult
	readonly pushed: number
	readonly pulled: number
	readonly message: string | null
}

export interface BackendSyncDaemonStatus {
	readonly state: "stopped" | "running" | "degraded" | "crashed"
	readonly generation: number
	readonly projectPath: string | null
	readonly intervalMs: number | null
	readonly startedAtMs: number | null
	readonly runCount: number
	readonly successCount: number
	readonly failureCount: number
	readonly failureStreak: number
	readonly restartStreak: number
	readonly lastBackoffMs: number | null
	readonly lastSuccessfulRunAtMs: number | null
	readonly lastRun: BackendSyncDaemonRunStatus | null
	readonly lastError: string | null
}

export interface BackendSyncDaemonStartOptions {
	readonly projectPath: string
	readonly intervalMs?: number
}

export interface BackendSyncDaemonServiceApi {
	readonly getStatus: () => Effect.Effect<BackendSyncDaemonStatus>
	readonly start: (options: BackendSyncDaemonStartOptions) => Effect.Effect<BackendSyncDaemonStatus>
	readonly stop: () => Effect.Effect<BackendSyncDaemonStatus>
}

interface RuntimeState {
	readonly status: BackendSyncDaemonStatus
	readonly fiber: Fiber.RuntimeFiber<void, never> | null
}

interface SessionRecoveryRuntime {
	readonly listActive: (
		projectPath: string,
	) => Effect.Effect<ReadonlyArray<{ issueId: string; state: string }>, never, never>
	readonly recover: (issueId: string) => Effect.Effect<void, never, never>
}

const normalizeIntervalMs = (value: number | undefined): number => {
	if (value === undefined || !Number.isFinite(value)) return DEFAULT_INTERVAL_MS
	const rounded = Math.floor(value)
	return rounded >= MIN_INTERVAL_MS ? rounded : MIN_INTERVAL_MS
}

const emptyStatus = (): BackendSyncDaemonStatus => ({
	state: "stopped",
	generation: 0,
	projectPath: null,
	intervalMs: null,
	startedAtMs: null,
	runCount: 0,
	successCount: 0,
	failureCount: 0,
	failureStreak: 0,
	restartStreak: 0,
	lastBackoffMs: null,
	lastSuccessfulRunAtMs: null,
	lastRun: null,
	lastError: null,
})

const calculateBackoffMs = (intervalMs: number, restartStreak: number): number => {
	if (restartStreak <= 0) return intervalMs
	const scaled = intervalMs * 2 ** Math.max(0, restartStreak - 1)
	return Math.min(MAX_FAILURE_BACKOFF_MS, Math.max(intervalMs, Math.floor(scaled)))
}

const runOnce = (
	router: { readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined> },
	projectPath: string,
): Effect.Effect<BackendSyncDaemonRunStatus> =>
	Effect.gen(function* () {
		const backendSync = yield* router.resolve()
		const runAtMs = Date.now()
		if (backendSync === undefined) {
			return {
				runAtMs,
				result: "skipped",
				pushed: 0,
				pulled: 0,
				message: "no backend sync runtime available",
			} satisfies BackendSyncDaemonRunStatus
		}

		const result = yield* backendSync
			.flushQueue(projectPath)
			.pipe(
				Effect.mapError((error) =>
					error instanceof Error ? error.message : `backend sync flush failed: ${String(error)}`,
				),
			)
		return {
			runAtMs,
			result: "flushed",
			pushed: result.pushed,
			pulled: result.pulled,
			message: null,
		} satisfies BackendSyncDaemonRunStatus
	}).pipe(
		Effect.catchAll((errorMessage) =>
			Effect.succeed({
				runAtMs: Date.now(),
				result: "failed",
				pushed: 0,
				pulled: 0,
				message: errorMessage,
			} satisfies BackendSyncDaemonRunStatus),
		),
	)

const recoverCrashedSessions = (
	sessionRecoveryRuntime: SessionRecoveryRuntime | null,
	projectPath: string,
): Effect.Effect<void, never> => {
	if (sessionRecoveryRuntime === null) {
		return Effect.void
	}

	return sessionRecoveryRuntime.listActive(projectPath).pipe(
		Effect.catchAll(() => Effect.succeed([])),
		Effect.flatMap((sessions) =>
			Effect.forEach(
				sessions,
				(session) =>
					session.state === "crashed"
						? sessionRecoveryRuntime
								.recover(session.issueId)
								.pipe(Effect.catchAll(() => Effect.void))
						: Effect.void,
				{
					discard: true,
				},
			),
		),
	)
}

const pollingLoop = (
	runtimeRef: Ref.Ref<RuntimeState>,
	router: { readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined> },
	stateStore: DaemonStateStoreApi,
	sessionRecoveryRuntime: SessionRecoveryRuntime | null,
	projectPath: string,
	intervalMs: number,
): Effect.Effect<void, never> =>
	Effect.gen(function* () {
		const loop: Effect.Effect<void, never> = runOnce(router, projectPath).pipe(
			Effect.flatMap((run) =>
				Ref.modify(runtimeRef, (runtime) => {
					const isSuccess = run.result === "flushed"
					const isFailure = run.result === "failed"
					const nextRunCount = runtime.status.runCount + 1
					const nextSuccessCount = isSuccess
						? runtime.status.successCount + 1
						: runtime.status.successCount
					const nextFailureCount = isFailure
						? runtime.status.failureCount + 1
						: runtime.status.failureCount
					const nextFailureStreak = isFailure ? runtime.status.failureStreak + 1 : 0
					const nextRestartStreak = isFailure ? runtime.status.restartStreak + 1 : 0
					const nextLastSuccessfulRunAtMs = isSuccess
						? run.runAtMs
						: runtime.status.lastSuccessfulRunAtMs
					const nextError = isFailure ? run.message : null
					const nextState: BackendSyncDaemonStatus["state"] = isFailure
						? nextFailureStreak >= CRASHED_FAILURE_STREAK_THRESHOLD
							? "crashed"
							: nextFailureStreak >= DEGRADED_FAILURE_STREAK_THRESHOLD
								? "degraded"
								: "running"
						: "running"
					const nextBackoffMs = isFailure ? calculateBackoffMs(intervalMs, nextRestartStreak) : null
					const nextDelayMs: number = isFailure ? (nextBackoffMs ?? intervalMs) : intervalMs
					const nextRuntime: RuntimeState = {
						...runtime,
						status: {
							...runtime.status,
							state: nextState,
							runCount: nextRunCount,
							successCount: nextSuccessCount,
							failureCount: nextFailureCount,
							failureStreak: nextFailureStreak,
							restartStreak: nextRestartStreak,
							lastBackoffMs: nextBackoffMs,
							lastSuccessfulRunAtMs: nextLastSuccessfulRunAtMs,
							lastRun: run,
							lastError: nextError,
						},
					}

					return [
						{
							nextDelayMs,
							status: nextRuntime.status,
						},
						nextRuntime,
					]
				}),
			),
			Effect.tap(({ status }) =>
				stateStore.persist(projectPath, status).pipe(Effect.catchAll(() => Effect.void)),
			),
			Effect.tap(() => recoverCrashedSessions(sessionRecoveryRuntime, projectPath)),
			Effect.map(({ nextDelayMs }) => nextDelayMs),
			Effect.flatMap((delayMs) => Effect.sleep(`${delayMs} millis`)),
			Effect.zipRight(Effect.suspend(() => loop)),
		)
		yield* loop
	}).pipe(Effect.catchAllCause(() => Effect.void))

export const makeBackendSyncDaemonService = (
	router: {
		readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined>
	},
	stateStore: DaemonStateStoreApi = {
		load: () => Effect.succeed(Option.none()),
		persist: () => Effect.void,
	},
	sessionRecoveryRuntime: SessionRecoveryRuntime | null = null,
): Effect.Effect<BackendSyncDaemonServiceApi, never, Scope.Scope> =>
	Effect.gen(function* () {
		const serviceScope = yield* Effect.scope
		const runtimeRef = yield* Ref.make<RuntimeState>({
			status: emptyStatus(),
			fiber: null,
		})

		const stopFiber = (fiber: Fiber.RuntimeFiber<void, never> | null): Effect.Effect<void> =>
			fiber === null
				? Effect.void
				: Fiber.interrupt(fiber).pipe(
						Effect.catchAllCause(() => Effect.void),
						Effect.asVoid,
					)

		return {
			getStatus: () => Ref.get(runtimeRef).pipe(Effect.map((runtime) => runtime.status)),
			start: (options: BackendSyncDaemonStartOptions) =>
				Effect.gen(function* () {
					const intervalMs = normalizeIntervalMs(options.intervalMs)
					const startedAtMs = Date.now()
					const previous = yield* Ref.get(runtimeRef)
					const shouldReuseExistingRuntime =
						previous.status.state === "running" &&
						previous.status.projectPath === options.projectPath &&
						previous.status.intervalMs === intervalMs
					if (shouldReuseExistingRuntime) {
						return previous.status
					}
					yield* stopFiber(previous.fiber)
					const recovered = yield* stateStore
						.load(options.projectPath)
						.pipe(Effect.catchAll(() => Effect.succeed(Option.none())))
					const baseline = Option.isSome(recovered)
						? toDaemonStatus(recovered.value)
						: previous.status

					yield* Ref.update(runtimeRef, (runtime) => {
						const runningStatus: BackendSyncDaemonStatus = {
							state: "running",
							generation: baseline.generation + 1,
							projectPath: options.projectPath,
							intervalMs,
							startedAtMs,
							runCount: baseline.runCount,
							successCount: baseline.successCount,
							failureCount: baseline.failureCount,
							failureStreak: baseline.failureStreak,
							restartStreak: baseline.restartStreak,
							lastBackoffMs: baseline.lastBackoffMs,
							lastSuccessfulRunAtMs: baseline.lastSuccessfulRunAtMs,
							lastRun: baseline.lastRun,
							lastError: baseline.lastError,
						}
						return {
							...runtime,
							fiber: null,
							status: runningStatus,
						}
					})

					yield* Ref.get(runtimeRef).pipe(
						Effect.flatMap((runtime) =>
							stateStore
								.persist(options.projectPath, runtime.status)
								.pipe(Effect.catchAll(() => Effect.void)),
						),
					)

					const fiber = yield* Effect.forkIn(
						pollingLoop(
							runtimeRef,
							router,
							stateStore,
							sessionRecoveryRuntime,
							options.projectPath,
							intervalMs,
						),
						serviceScope,
					)

					yield* Ref.update(runtimeRef, (runtime) => ({
						...runtime,
						fiber,
					}))

					return yield* Ref.get(runtimeRef).pipe(Effect.map((runtime) => runtime.status))
				}),
			stop: () =>
				Effect.gen(function* () {
					const current = yield* Ref.get(runtimeRef)
					yield* stopFiber(current.fiber)
					yield* Ref.update(runtimeRef, (runtime) => {
						const stoppedStatus: BackendSyncDaemonStatus = {
							state: "stopped",
							generation: runtime.status.generation,
							projectPath: null,
							intervalMs: null,
							startedAtMs: null,
							runCount: runtime.status.runCount,
							successCount: runtime.status.successCount,
							failureCount: runtime.status.failureCount,
							failureStreak: runtime.status.failureStreak,
							restartStreak: runtime.status.restartStreak,
							lastBackoffMs: runtime.status.lastBackoffMs,
							lastSuccessfulRunAtMs: runtime.status.lastSuccessfulRunAtMs,
							lastRun: runtime.status.lastRun,
							lastError: runtime.status.lastError,
						}
						return {
							fiber: null,
							status: stoppedStatus,
						}
					})
					yield* Ref.get(runtimeRef).pipe(
						Effect.flatMap((runtime) =>
							current.status.projectPath === null
								? Effect.void
								: stateStore
										.persist(current.status.projectPath, runtime.status)
										.pipe(Effect.catchAll(() => Effect.void)),
						),
					)
					return yield* Ref.get(runtimeRef).pipe(Effect.map((runtime) => runtime.status))
				}),
		} satisfies BackendSyncDaemonServiceApi
	})

export class BackendSyncDaemonService extends Effect.Service<BackendSyncDaemonService>()(
	"BackendSyncDaemonService",
	{
		dependencies: [BackendSyncRouter.Default],
		scoped: Effect.gen(function* () {
			const backendSyncRouter = yield* BackendSyncRouter
			const maybeSessionManager = yield* Effect.serviceOption(SessionManager)
			const commandExecutor = yield* CommandExecutor.CommandExecutor
			const maybeFileSystem = yield* Effect.serviceOption(FileSystem.FileSystem)
			const maybePath = yield* Effect.serviceOption(Path.Path)
			const daemonStateStore =
				Option.isSome(maybeFileSystem) && Option.isSome(maybePath)
					? makeDaemonStateStore({
							fs: maybeFileSystem.value,
							path: maybePath.value,
						})
					: {
							load: () => Effect.succeed(Option.none()),
							persist: () => Effect.void,
						}
			const sessionRecoveryRuntime: SessionRecoveryRuntime | null = Option.isSome(
				maybeSessionManager,
			)
				? {
						listActive: (projectPath) =>
							maybeSessionManager.value.listActive(projectPath).pipe(
								Effect.map((sessions) =>
									sessions.map((session) => ({
										issueId: session.issueId,
										state: session.state,
									})),
								),
								Effect.catchAll(() => Effect.succeed([])),
								Effect.provideService(CommandExecutor.CommandExecutor, commandExecutor),
							),
						recover: (issueId) =>
							maybeSessionManager.value.recoverSession(issueId).pipe(
								Effect.asVoid,
								Effect.catchAll(() => Effect.void),
								Effect.provideService(CommandExecutor.CommandExecutor, commandExecutor),
							),
					}
				: null
			return yield* makeBackendSyncDaemonService(
				backendSyncRouter,
				daemonStateStore,
				sessionRecoveryRuntime,
			)
		}),
	},
) {}
