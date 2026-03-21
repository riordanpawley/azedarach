import {
	buildGlobalDaemonSocketUrl,
	formatDaemonRpcClientFailure,
	GlobalDaemonBootstrap,
	type GlobalDaemonBootstrapApi,
	GlobalDaemonBootstrapError,
	type GlobalDaemonDiscoveryMetadata,
	isRetryableRpcClientError,
} from "@azedarach/daemon-control"
import { DaemonRpcClient, type DaemonRpcClientApi, layerSocket } from "@azedarach/shared/rpc"
import { BunContext } from "@effect/platform-bun"
import type { RpcClientError } from "@effect/rpc/RpcClientError"
import { Context, Effect, Exit, Layer, Option, Scope } from "effect"
import { GlobalDaemonDiscovery } from "./GlobalDaemonDiscovery.js"

const GLOBAL_DAEMON_BOOTSTRAP_TIMEOUT_MS = 5_000
const GLOBAL_DAEMON_POLL_INTERVAL_MS = 50
const GLOBAL_DAEMON_ATTACH_RETRY_BACKOFF_MS: ReadonlyArray<number> = [25, 50, 100]
const GLOBAL_DAEMON_MAIN_ENTRY_PATH = decodeURIComponent(
	new URL("./GlobalDaemonMain.ts", import.meta.url).pathname,
)

const sleep = (ms: number): Effect.Effect<void> => Effect.sleep(`${ms} millis`)

const spawnGlobalDaemonMain = (): Effect.Effect<void, GlobalDaemonBootstrapError> =>
	Effect.try({
		try: () => {
			const child = Bun.spawn([process.execPath, "run", GLOBAL_DAEMON_MAIN_ENTRY_PATH], {
				cwd: process.cwd(),
				env: process.env,
				stdio: ["ignore", "ignore", "ignore"],
			})
			child.unref()
		},
		catch: (error) =>
			new GlobalDaemonBootstrapError({
				message: `Failed to spawn global daemon runtime: ${error instanceof Error ? error.message : String(error)}`,
				reason: "spawn-failed",
			}),
	})

const retryDelayForAttempt = (attempt: number, retryBackoffMs: ReadonlyArray<number>): number => {
	if (attempt <= 1 || retryBackoffMs.length === 0) {
		return 0
	}
	const index = Math.min(attempt - 2, retryBackoffMs.length - 1)
	return retryBackoffMs[index] ?? 0
}

const normalizeRetryBackoffMs = (
	retryBackoffMs: ReadonlyArray<number> | undefined,
): ReadonlyArray<number> => {
	const source = retryBackoffMs ?? GLOBAL_DAEMON_ATTACH_RETRY_BACKOFF_MS
	const normalized = source
		.map((value) => (Number.isFinite(value) && value >= 0 ? Math.floor(value) : 0))
		.filter((value) => value > 0)
	return normalized.length > 0 ? normalized : [0]
}

export const GlobalDaemonBootstrapLive = Layer.scoped(
	GlobalDaemonBootstrap,
	Effect.gen(function* () {
		const discoveryService = yield* GlobalDaemonDiscovery
		const bootstrapScope = yield* Scope.make()
		yield* Effect.addFinalizer(() => Scope.close(bootstrapScope, Exit.succeed(undefined)))

		const readLiveGlobalDaemonDiscovery = (): Effect.Effect<
			Option.Option<GlobalDaemonDiscoveryMetadata>,
			GlobalDaemonBootstrapError
		> =>
			Effect.gen(function* () {
				const discovery = yield* discoveryService.readDiscovery().pipe(
					Effect.mapError(
						(error) =>
							new GlobalDaemonBootstrapError({
								message: error.message,
								reason: "discovery-read",
							}),
					),
				)
				if (Option.isNone(discovery)) {
					return Option.none<GlobalDaemonDiscoveryMetadata>()
				}

				const liveness = yield* discoveryService.probeOwnerLiveness(discovery.value)
				if (liveness === "dead") {
					yield* discoveryService.clearArtifacts().pipe(
						Effect.mapError(
							(error) =>
								new GlobalDaemonBootstrapError({
									message: error.message,
									reason: "discovery-clear",
								}),
						),
					)
					return Option.none<GlobalDaemonDiscoveryMetadata>()
				}
				return Option.some(discovery.value)
			})

		const waitForGlobalDaemonDiscovery = (params: {
			readonly timeoutMs: number
		}): Effect.Effect<GlobalDaemonDiscoveryMetadata, GlobalDaemonBootstrapError> =>
			Effect.gen(function* () {
				const deadline = Date.now() + params.timeoutMs
				while (Date.now() <= deadline) {
					const discovery = yield* readLiveGlobalDaemonDiscovery()
					if (Option.isSome(discovery)) {
						return discovery.value
					}
					yield* sleep(GLOBAL_DAEMON_POLL_INTERVAL_MS)
				}
				return yield* Effect.fail(
					new GlobalDaemonBootstrapError({
						message:
							"Timed out waiting for global daemon discovery metadata. Run `bun run packages/daemon/src/GlobalDaemonMain.ts` and retry.",
						reason: "discovery-timeout",
					}),
				)
			})

		const buildDaemonRpcClient = (socketUrl: string) =>
			Layer.buildWithScope(bootstrapScope)(layerSocket(socketUrl)).pipe(
				Effect.map((context) => Context.get(context, DaemonRpcClient)),
				Effect.mapError(
					(error) =>
						new GlobalDaemonBootstrapError({
							message: `Failed to establish daemon RPC client at ${socketUrl}: ${String(error)}`,
							reason: "rpc-transport",
						}),
				),
			)

		const bootstrapDaemonRpcClient: GlobalDaemonBootstrapApi["bootstrapDaemonRpcClient"] = (
			params,
		) =>
			Effect.gen(function* () {
				const timeoutMs = params?.timeoutMs ?? GLOBAL_DAEMON_BOOTSTRAP_TIMEOUT_MS
				const retryBackoffMs = normalizeRetryBackoffMs(params?.attachRetryBackoffMs)
				const startedAtMs = Date.now()
				const deadlineMs = startedAtMs + timeoutMs
				yield* Effect.log(
					`daemon_bootstrap: start autoStart=${String(params?.autoStart ?? false)} timeoutMs=${timeoutMs} verifyReachable=${String(params?.verifyReachable ?? false)}`,
				)

				let discovery: GlobalDaemonDiscoveryMetadata
				let startedDaemon = false
				let attachAttemptCount = 0
				let lastSocketUrl: string | null = null
				let lastRetryableTransportError: RpcClientError | null = null

				while (Date.now() <= deadlineMs) {
					const attempt = attachAttemptCount + 1
					const delayMs = retryDelayForAttempt(attempt, retryBackoffMs)
					if (delayMs > 0) {
						const remainingBeforeDelay = deadlineMs - Date.now()
						if (remainingBeforeDelay <= 0 || delayMs > remainingBeforeDelay) {
							break
						}
						yield* sleep(delayMs)
					}

					const liveDiscovery = yield* readLiveGlobalDaemonDiscovery()
					if (Option.isSome(liveDiscovery)) {
						yield* Effect.log(
							`daemon_bootstrap: discovered live daemon socket=${liveDiscovery.value.socketPath} pid=${liveDiscovery.value.pid} attempt=${attempt}`,
						)
						discovery = liveDiscovery.value
					} else if (params?.autoStart && !startedDaemon) {
						const remainingTimeoutMs = Math.max(0, deadlineMs - Date.now())
						yield* Effect.log(
							`daemon_bootstrap: no discovery found; spawning daemon (remainingTimeoutMs=${remainingTimeoutMs})`,
						)
						yield* spawnGlobalDaemonMain()
						discovery = yield* waitForGlobalDaemonDiscovery({
							timeoutMs: remainingTimeoutMs,
						})
						startedDaemon = true
						yield* Effect.log(
							`daemon_bootstrap: spawned daemon discovered socket=${discovery.socketPath} pid=${discovery.pid}`,
						)
					} else {
						attachAttemptCount = attempt
						params?.onAttachAttempt?.({
							attempt,
							delayMs,
							timeoutRemainingMs: Math.max(0, deadlineMs - Date.now()),
							socketPath: null,
							socketUrl: null,
						})
						continue
					}

					const socketUrl = buildGlobalDaemonSocketUrl(discovery.socketPath)
					lastSocketUrl = socketUrl
					attachAttemptCount = attempt
					yield* Effect.log(`daemon_bootstrap: attach attempt=${attempt} socketUrl=${socketUrl}`)
					params?.onAttachAttempt?.({
						attempt,
						delayMs,
						timeoutRemainingMs: Math.max(0, deadlineMs - Date.now()),
						socketPath: discovery.socketPath,
						socketUrl,
					})

					const client = yield* buildDaemonRpcClient(socketUrl)

					if (!params?.verifyReachable) {
						yield* Effect.log(
							`daemon_bootstrap: attach success socketUrl=${socketUrl} startedDaemon=${String(startedDaemon)} attempts=${attachAttemptCount}`,
						)
						return {
							client,
							discovery,
							socketUrl,
							startedDaemon,
							attachAttemptCount,
						}
					}

					const connectivity = yield* client.status().pipe(Effect.either)
					if (connectivity._tag === "Right") {
						yield* Effect.log(
							`daemon_bootstrap: verifyReachable status succeeded socketUrl=${socketUrl} attempts=${attachAttemptCount}`,
						)
						return {
							client,
							discovery,
							socketUrl,
							startedDaemon,
							attachAttemptCount,
						}
					}

					const error = connectivity.left
					if (isRetryableRpcClientError(error)) {
						lastRetryableTransportError = error
						yield* Effect.logWarning(
							`daemon_bootstrap: verifyReachable transport failure socketUrl=${socketUrl} attempt=${attempt} error=${error.message}`,
						)
						continue
					}
					return yield* Effect.fail(
						formatDaemonRpcClientFailure({
							operation: "status",
							socketUrl,
							error,
						}),
					)
				}

				if (lastRetryableTransportError !== null) {
					return yield* Effect.fail(
						formatDaemonRpcClientFailure({
							operation: "status",
							socketUrl: lastSocketUrl ?? "<unknown>",
							error: lastRetryableTransportError,
						}),
					)
				}

				return yield* Effect.fail(
					new GlobalDaemonBootstrapError({
						message:
							"Timed out waiting for a reachable global daemon endpoint. Restart the daemon and retry.",
						reason: "endpoint-timeout",
					}),
				)
			})

		return {
			bootstrapDaemonRpcClient,
			formatDaemonRpcClientFailure,
			isRetryableRpcClientError,
			buildGlobalDaemonSocketUrl,
		} satisfies GlobalDaemonBootstrapApi
	}),
)

const DaemonControlDependencies = Layer.mergeAll(BunContext.layer, GlobalDaemonDiscovery.Default)

export const DaemonControlLive = Layer.provide(GlobalDaemonBootstrapLive, DaemonControlDependencies)
