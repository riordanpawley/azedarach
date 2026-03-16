import type { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option } from "effect"
import {
	clearGlobalDaemonArtifacts,
	type GlobalDaemonDiscovery,
	probeGlobalDaemonOwnerLiveness,
	readGlobalDaemonDiscovery,
} from "../core/GlobalDaemonRegistry.js"
import {
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
	DaemonRpcProtocolVersionMismatchError,
	DaemonRpcRemoteActionError,
	DaemonRpcTransportError,
	layerSocket,
} from "../rpc/DaemonRpcClient.js"

const GLOBAL_DAEMON_BOOTSTRAP_TIMEOUT_MS = 5_000
const GLOBAL_DAEMON_POLL_INTERVAL_MS = 50
const GLOBAL_DAEMON_ATTACH_RETRY_BACKOFF_MS: ReadonlyArray<number> = [25, 50, 100]
const GLOBAL_DAEMON_MAIN_ENTRY_PATH = decodeURIComponent(
	new URL("../daemon/GlobalDaemonMain.ts", import.meta.url).pathname,
)

export class GlobalDaemonBootstrapError extends Data.TaggedError("GlobalDaemonBootstrapError")<{
	readonly message: string
	readonly reason:
		| "registry-read"
		| "registry-clear"
		| "discovery-timeout"
		| "spawn-failed"
		| "rpc-protocol-mismatch"
		| "rpc-remote-action"
		| "rpc-transport"
		| "rpc-unknown"
		| "endpoint-timeout"
}> {}

const sleep = (ms: number): Effect.Effect<void> => Effect.sleep(`${ms} millis`)

const withGlobalDaemonClient = <A, E>(
	socketUrl: string,
	effect: Effect.Effect<A, E, DaemonRpcClient>,
): Effect.Effect<A, E> => effect.pipe(Effect.provide(layerSocket(socketUrl)))

const readLiveGlobalDaemonDiscovery = (): Effect.Effect<
	Option.Option<GlobalDaemonDiscovery>,
	GlobalDaemonBootstrapError,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const discovery = yield* readGlobalDaemonDiscovery().pipe(
			Effect.mapError(
				(error) =>
					new GlobalDaemonBootstrapError({
						message: error.message,
						reason: "registry-read",
					}),
			),
		)
		if (Option.isNone(discovery)) {
			return Option.none<GlobalDaemonDiscovery>()
		}

		const liveness = yield* probeGlobalDaemonOwnerLiveness(discovery.value)
		if (liveness === "dead") {
			yield* clearGlobalDaemonArtifacts().pipe(
				Effect.mapError(
					(error) =>
						new GlobalDaemonBootstrapError({
							message: error.message,
							reason: "registry-clear",
						}),
				),
			)
			return Option.none<GlobalDaemonDiscovery>()
		}
		return Option.some(discovery.value)
	})

const waitForGlobalDaemonDiscovery = (params: {
	readonly timeoutMs: number
}): Effect.Effect<
	GlobalDaemonDiscovery,
	GlobalDaemonBootstrapError,
	FileSystem.FileSystem | Path.Path
> =>
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
					"Timed out waiting for global daemon discovery metadata. Run `bun run src/daemon/GlobalDaemonMain.ts` and retry.",
				reason: "discovery-timeout",
			}),
		)
	})

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

export const buildGlobalDaemonSocketUrl = (socketPath: string): string =>
	`ws+unix://${socketPath}:/`

export const formatDaemonRpcClientFailure = (params: {
	readonly operation: string
	readonly socketUrl: string
	readonly error: DaemonRpcClientError
}): GlobalDaemonBootstrapError => {
	if (params.error instanceof DaemonRpcProtocolVersionMismatchError) {
		return new GlobalDaemonBootstrapError({
			message: `Daemon RPC protocol mismatch for '${params.operation}' (expected=${params.error.expectedProtocolVersion}, received=${params.error.receivedProtocolVersion}). Update CLI/daemon binaries so protocol versions match.`,
			reason: "rpc-protocol-mismatch",
		})
	}
	if (params.error instanceof DaemonRpcRemoteActionError) {
		const actionHint = params.error.action === undefined ? "" : ` Action: ${params.error.action}.`
		return new GlobalDaemonBootstrapError({
			message: `Daemon RPC '${params.operation}' rejected by daemon (code=${params.error.code}): ${params.error.message}.${actionHint}`,
			reason: "rpc-remote-action",
		})
	}
	if (params.error instanceof DaemonRpcTransportError) {
		return new GlobalDaemonBootstrapError({
			message: `Unable to connect to daemon RPC endpoint (${params.socketUrl}) for '${params.operation}': ${params.error.message}. ${params.error.suggestion}`,
			reason: "rpc-transport",
		})
	}
	const exhaustive: never = params.error
	return new GlobalDaemonBootstrapError({
		message: `Daemon RPC '${params.operation}' failed: ${String(exhaustive)}`,
		reason: "rpc-unknown",
	})
}

export interface GlobalDaemonAttachAttemptObservation {
	readonly attempt: number
	readonly delayMs: number
	readonly timeoutRemainingMs: number
	readonly socketPath: string | null
	readonly socketUrl: string | null
}

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

export const bootstrapDaemonRpcClient = (params?: {
	readonly autoStart: boolean
	readonly timeoutMs?: number
	readonly attachRetryBackoffMs?: ReadonlyArray<number>
	readonly onAttachAttempt?: (observation: GlobalDaemonAttachAttemptObservation) => void
	readonly verifyReachable?: boolean
}): Effect.Effect<
	{
		readonly client: DaemonRpcClientApi
		readonly discovery: GlobalDaemonDiscovery
		readonly socketUrl: string
		readonly startedDaemon: boolean
		readonly attachAttemptCount: number
	},
	GlobalDaemonBootstrapError,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const timeoutMs = params?.timeoutMs ?? GLOBAL_DAEMON_BOOTSTRAP_TIMEOUT_MS
		const retryBackoffMs = normalizeRetryBackoffMs(params?.attachRetryBackoffMs)
		const startedAtMs = Date.now()
		const deadlineMs = startedAtMs + timeoutMs
		yield* Effect.log(
			`daemon_bootstrap: start autoStart=${String(params?.autoStart ?? false)} timeoutMs=${timeoutMs} verifyReachable=${String(params?.verifyReachable ?? false)}`,
		)

		let discovery: GlobalDaemonDiscovery
		let startedDaemon = false
		let attachAttemptCount = 0
		let lastSocketUrl: string | null = null
		let lastTransportError: DaemonRpcTransportError | null = null

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
				discovery = yield* waitForGlobalDaemonDiscovery({ timeoutMs: remainingTimeoutMs })
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

			const client = yield* withGlobalDaemonClient(
				socketUrl,
				Effect.gen(function* () {
					return yield* DaemonRpcClient
				}),
			)

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
			if (error instanceof DaemonRpcTransportError) {
				lastTransportError = error
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

		if (lastTransportError !== null) {
			return yield* Effect.fail(
				formatDaemonRpcClientFailure({
					operation: "status",
					socketUrl: lastSocketUrl ?? "<unknown>",
					error: lastTransportError,
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
