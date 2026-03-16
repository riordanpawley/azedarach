import type { FileSystem, Path } from "@effect/platform"
import { Effect, Option } from "effect"
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

const sleep = (ms: number): Effect.Effect<void> => Effect.sleep(`${ms} millis`)

const withGlobalDaemonClient = <A, E>(
	socketUrl: string,
	effect: Effect.Effect<A, E, DaemonRpcClient>,
): Effect.Effect<A, E> => effect.pipe(Effect.provide(layerSocket(socketUrl)))

const readLiveGlobalDaemonDiscovery = (): Effect.Effect<
	Option.Option<GlobalDaemonDiscovery>,
	Error,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const discovery = yield* readGlobalDaemonDiscovery().pipe(
			Effect.mapError((error) => new Error(error.message)),
		)
		if (Option.isNone(discovery)) {
			return Option.none<GlobalDaemonDiscovery>()
		}

		const liveness = yield* probeGlobalDaemonOwnerLiveness(discovery.value)
		if (liveness === "dead") {
			yield* clearGlobalDaemonArtifacts().pipe(Effect.mapError((error) => new Error(error.message)))
			return Option.none<GlobalDaemonDiscovery>()
		}
		return Option.some(discovery.value)
	})

const waitForGlobalDaemonDiscovery = (params: {
	readonly timeoutMs: number
}): Effect.Effect<GlobalDaemonDiscovery, Error, FileSystem.FileSystem | Path.Path> =>
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
			new Error(
				"Timed out waiting for global daemon discovery metadata. Run `bun run src/daemon/GlobalDaemonMain.ts` and retry.",
			),
		)
	})

const spawnGlobalDaemonMain = (): Effect.Effect<void, Error> =>
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
			new Error(
				`Failed to spawn global daemon runtime: ${error instanceof Error ? error.message : String(error)}`,
			),
	})

export const buildGlobalDaemonSocketUrl = (socketPath: string): string =>
	`ws+unix://${socketPath}:/`

export const formatDaemonRpcClientFailure = (params: {
	readonly operation: string
	readonly socketUrl: string
	readonly error: DaemonRpcClientError
}): Error => {
	if (params.error instanceof DaemonRpcProtocolVersionMismatchError) {
		return new Error(
			`Daemon RPC protocol mismatch for '${params.operation}' (expected=${params.error.expectedProtocolVersion}, received=${params.error.receivedProtocolVersion}). Update CLI/daemon binaries so protocol versions match.`,
		)
	}
	if (params.error instanceof DaemonRpcRemoteActionError) {
		const actionHint = params.error.action === undefined ? "" : ` Action: ${params.error.action}.`
		return new Error(
			`Daemon RPC '${params.operation}' rejected by daemon (code=${params.error.code}): ${params.error.message}.${actionHint}`,
		)
	}
	if (params.error instanceof DaemonRpcTransportError) {
		return new Error(
			`Unable to connect to daemon RPC endpoint (${params.socketUrl}) for '${params.operation}': ${params.error.message}. ${params.error.suggestion}`,
		)
	}
	const exhaustive: never = params.error
	return new Error(`Daemon RPC '${params.operation}' failed: ${String(exhaustive)}`)
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
	Error,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const timeoutMs = params?.timeoutMs ?? GLOBAL_DAEMON_BOOTSTRAP_TIMEOUT_MS
		const retryBackoffMs = normalizeRetryBackoffMs(params?.attachRetryBackoffMs)
		const startedAtMs = Date.now()
		const deadlineMs = startedAtMs + timeoutMs

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
				discovery = liveDiscovery.value
			} else if (params?.autoStart && !startedDaemon) {
				const remainingTimeoutMs = Math.max(0, deadlineMs - Date.now())
				yield* spawnGlobalDaemonMain()
				discovery = yield* waitForGlobalDaemonDiscovery({ timeoutMs: remainingTimeoutMs })
				startedDaemon = true
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
			new Error(
				"Timed out waiting for a reachable global daemon endpoint. Restart the daemon and retry.",
			),
		)
	})
