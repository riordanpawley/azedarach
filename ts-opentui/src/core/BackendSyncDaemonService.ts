import { Effect, Fiber, Ref, type Scope } from "effect"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { BackendSyncRouter } from "./BackendSyncRouter.js"

const DEFAULT_INTERVAL_MS = 5_000
const MIN_INTERVAL_MS = 50

export type BackendSyncDaemonRunResult = "flushed" | "skipped" | "failed"

export interface BackendSyncDaemonRunStatus {
	readonly runAtMs: number
	readonly result: BackendSyncDaemonRunResult
	readonly pushed: number
	readonly pulled: number
	readonly message: string | null
}

export interface BackendSyncDaemonStatus {
	readonly state: "stopped" | "running"
	readonly projectPath: string | null
	readonly intervalMs: number | null
	readonly startedAtMs: number | null
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

const normalizeIntervalMs = (value: number | undefined): number => {
	if (value === undefined || !Number.isFinite(value)) return DEFAULT_INTERVAL_MS
	const rounded = Math.floor(value)
	return rounded >= MIN_INTERVAL_MS ? rounded : MIN_INTERVAL_MS
}

const emptyStatus = (): BackendSyncDaemonStatus => ({
	state: "stopped",
	projectPath: null,
	intervalMs: null,
	startedAtMs: null,
	lastRun: null,
	lastError: null,
})

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

const pollingLoop = (
	runtimeRef: Ref.Ref<RuntimeState>,
	router: { readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined> },
	projectPath: string,
	intervalMs: number,
): Effect.Effect<void, never> =>
	Effect.forever(
		runOnce(router, projectPath).pipe(
			Effect.flatMap((run) =>
				Ref.update(runtimeRef, (runtime) => {
					const nextError = run.result === "failed" ? run.message : null
					return {
						...runtime,
						status: {
							...runtime.status,
							lastRun: run,
							lastError: nextError,
						},
					}
				}),
			),
			Effect.zipRight(Effect.sleep(`${intervalMs} millis`)),
		),
	).pipe(Effect.catchAllCause(() => Effect.void))

export const makeBackendSyncDaemonService = (router: {
	readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined>
}): Effect.Effect<BackendSyncDaemonServiceApi, never, Scope.Scope> =>
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
					yield* stopFiber(previous.fiber)

					yield* Ref.update(runtimeRef, (runtime) => {
						const runningStatus: BackendSyncDaemonStatus = {
							state: "running",
							projectPath: options.projectPath,
							intervalMs,
							startedAtMs,
							lastRun: runtime.status.lastRun,
							lastError: runtime.status.lastError,
						}
						return {
							...runtime,
							fiber: null,
							status: runningStatus,
						}
					})

					const fiber = yield* Effect.forkIn(
						pollingLoop(runtimeRef, router, options.projectPath, intervalMs),
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
							projectPath: null,
							intervalMs: null,
							startedAtMs: null,
							lastRun: runtime.status.lastRun,
							lastError: runtime.status.lastError,
						}
						return {
							fiber: null,
							status: stoppedStatus,
						}
					})
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
			return yield* makeBackendSyncDaemonService(backendSyncRouter)
		}),
	},
) {}
