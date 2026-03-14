import type { FileSystem, Path } from "@effect/platform"
import { Effect, Option } from "effect"
import {
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

export const bootstrapDaemonRpcClient = (params?: {
	readonly autoStart: boolean
	readonly timeoutMs?: number
}): Effect.Effect<
	{
		readonly client: DaemonRpcClientApi
		readonly discovery: GlobalDaemonDiscovery
		readonly socketUrl: string
		readonly startedDaemon: boolean
	},
	Error,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const timeoutMs = params?.timeoutMs ?? GLOBAL_DAEMON_BOOTSTRAP_TIMEOUT_MS
		const existingDiscovery = yield* readLiveGlobalDaemonDiscovery()

		let discovery: GlobalDaemonDiscovery
		let startedDaemon = false

		if (Option.isSome(existingDiscovery)) {
			discovery = existingDiscovery.value
		} else {
			if (!params?.autoStart) {
				return yield* Effect.fail(
					new Error(
						"No global daemon discovery metadata found. Start the daemon first with `bun run src/daemon/GlobalDaemonMain.ts`.",
					),
				)
			}
			yield* spawnGlobalDaemonMain()
			discovery = yield* waitForGlobalDaemonDiscovery({ timeoutMs })
			startedDaemon = true
		}

		const socketUrl = buildGlobalDaemonSocketUrl(discovery.socketPath)
		const client = yield* withGlobalDaemonClient(
			socketUrl,
			Effect.gen(function* () {
				return yield* DaemonRpcClient
			}),
		)

		return {
			client,
			discovery,
			socketUrl,
			startedDaemon,
		}
	})
