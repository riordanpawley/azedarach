import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Schema } from "effect"

const GLOBAL_DAEMON_STORAGE_DIR = ".azedarach/daemon"
const GLOBAL_DAEMON_SOCKET_FILE = "global.sock"
const GLOBAL_DAEMON_LOCK_DIR = "global.lock"
const GLOBAL_DAEMON_DISCOVERY_FILE = "owner.json"
const GLOBAL_DAEMON_DISCOVERY_SCHEMA_VERSION = 1 as const

const GlobalDaemonDiscoverySchema = Schema.Struct({
	schemaVersion: Schema.Literal(GLOBAL_DAEMON_DISCOVERY_SCHEMA_VERSION),
	pid: Schema.Number.pipe(Schema.int()),
	lockId: Schema.String,
	socketPath: Schema.String,
	startedAtMs: Schema.Number.pipe(Schema.int()),
})

export type GlobalDaemonDiscovery = Schema.Schema.Type<typeof GlobalDaemonDiscoverySchema>

export type GlobalDaemonOwnerLiveness = "alive" | "dead" | "inaccessible"

export interface GlobalDaemonRegistryPaths {
	readonly daemonDir: string
	readonly socketPath: string
	readonly lockDir: string
	readonly discoveryPath: string
}

export interface GlobalDaemonLease {
	readonly lockId: string
	readonly pid: number
	readonly discovery: GlobalDaemonDiscovery
	readonly paths: GlobalDaemonRegistryPaths
}

interface ErrnoLike {
	readonly code: unknown
}

const isErrnoLike = (value: unknown): value is ErrnoLike =>
	typeof value === "object" && value !== null && "code" in value

const decodeDiscovery = Schema.decode(Schema.parseJson(GlobalDaemonDiscoverySchema))
const encodeDiscovery = Schema.encode(Schema.parseJson(GlobalDaemonDiscoverySchema))

export class GlobalDaemonRegistryError extends Data.TaggedError("GlobalDaemonRegistryError")<{
	readonly message: string
	readonly cause: unknown
}> {}

export class GlobalDaemonAlreadyRunningError extends Data.TaggedError(
	"GlobalDaemonAlreadyRunningError",
)<{
	readonly paths: GlobalDaemonRegistryPaths
	readonly discovery: GlobalDaemonDiscovery
	readonly liveness: GlobalDaemonOwnerLiveness
}> {}

export interface GlobalDaemonDiscoveryApi {
	readonly readDiscovery: (params?: {
		readonly homeDirectory?: string
	}) => Effect.Effect<
		Option.Option<GlobalDaemonDiscovery>,
		GlobalDaemonRegistryError,
		FileSystem.FileSystem | Path.Path
	>
	readonly probeOwnerLiveness: (
		discovery: GlobalDaemonDiscovery,
	) => Effect.Effect<GlobalDaemonOwnerLiveness, never>
	readonly clearArtifacts: (params?: {
		readonly homeDirectory?: string
	}) => Effect.Effect<void, GlobalDaemonRegistryError, FileSystem.FileSystem | Path.Path>
}

const resolveHomeDirectory = (
	homeDirectory: string | undefined,
): Effect.Effect<string, GlobalDaemonRegistryError> => {
	const resolvedHome = homeDirectory ?? process.env.HOME
	if (resolvedHome === undefined || resolvedHome.trim().length === 0) {
		return Effect.fail(
			new GlobalDaemonRegistryError({
				message: "HOME is not set; cannot resolve global daemon directory",
				cause: "missing_home",
			}),
		)
	}
	return Effect.succeed(resolvedHome)
}

export const resolveGlobalDaemonRegistryPaths = (params: {
	readonly homeDirectory?: string
	readonly pathOps: Pick<Path.Path, "join">
}): Effect.Effect<GlobalDaemonRegistryPaths, GlobalDaemonRegistryError> =>
	Effect.gen(function* () {
		const home = yield* resolveHomeDirectory(params.homeDirectory)
		const daemonDir = params.pathOps.join(home, GLOBAL_DAEMON_STORAGE_DIR)
		const lockDir = params.pathOps.join(daemonDir, GLOBAL_DAEMON_LOCK_DIR)
		return {
			daemonDir,
			socketPath: params.pathOps.join(daemonDir, GLOBAL_DAEMON_SOCKET_FILE),
			lockDir,
			discoveryPath: params.pathOps.join(lockDir, GLOBAL_DAEMON_DISCOVERY_FILE),
		}
	})

const readDiscoveryAtPath = (
	discoveryPath: string,
): Effect.Effect<
	Option.Option<GlobalDaemonDiscovery>,
	GlobalDaemonRegistryError,
	FileSystem.FileSystem
> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const exists = yield* fs.exists(discoveryPath).pipe(
			Effect.catchAll((cause) =>
				Effect.fail(
					new GlobalDaemonRegistryError({
						message: `Failed to check discovery file '${discoveryPath}'`,
						cause,
					}),
				),
			),
		)
		if (!exists) {
			return Option.none()
		}
		const raw = yield* fs.readFileString(discoveryPath).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to read discovery file '${discoveryPath}'`,
						cause,
					}),
			),
		)
		const decoded = yield* decodeDiscovery(raw).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to decode discovery file '${discoveryPath}'`,
						cause,
					}),
			),
		)
		return Option.some(decoded)
	})

const writeDiscoveryAtomically = (
	paths: GlobalDaemonRegistryPaths,
	discovery: GlobalDaemonDiscovery,
	lockId: string,
): Effect.Effect<void, GlobalDaemonRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const encoded = yield* encodeDiscovery(discovery).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to encode discovery metadata '${paths.discoveryPath}'`,
						cause,
					}),
			),
		)
		const tempPath = `${paths.discoveryPath}.${lockId}.tmp`
		yield* fs.writeFileString(tempPath, encoded).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to write discovery temp file '${tempPath}'`,
						cause,
					}),
			),
		)
		yield* fs.rename(tempPath, paths.discoveryPath).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to finalize discovery file '${paths.discoveryPath}'`,
						cause,
					}),
			),
		)
	})

const tryAcquireLockDir = (
	paths: GlobalDaemonRegistryPaths,
): Effect.Effect<boolean, GlobalDaemonRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		yield* fs.makeDirectory(paths.daemonDir, { recursive: true }).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to create daemon directory '${paths.daemonDir}'`,
						cause,
					}),
			),
		)
		return yield* fs.makeDirectory(paths.lockDir).pipe(
			Effect.as(true),
			Effect.catchAll((cause) =>
				fs.exists(paths.lockDir).pipe(
					Effect.flatMap((exists) =>
						exists
							? Effect.succeed(false)
							: Effect.fail(
									new GlobalDaemonRegistryError({
										message: `Failed to create lock dir '${paths.lockDir}'`,
										cause,
									}),
								),
					),
					Effect.catchAll((existsCause) =>
						Effect.fail(
							new GlobalDaemonRegistryError({
								message: `Failed to inspect lock dir '${paths.lockDir}'`,
								cause: existsCause,
							}),
						),
					),
				),
			),
		)
	})

const clearLockDir = (
	lockDir: string,
): Effect.Effect<void, GlobalDaemonRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		yield* fs.remove(lockDir, { recursive: true, force: true }).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to clear lock dir '${lockDir}'`,
						cause,
					}),
			),
		)
	})

const clearSocketPath = (
	socketPath: string,
): Effect.Effect<void, GlobalDaemonRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		yield* fs.remove(socketPath, { force: true }).pipe(
			Effect.mapError(
				(cause) =>
					new GlobalDaemonRegistryError({
						message: `Failed to clear socket path '${socketPath}'`,
						cause,
					}),
			),
		)
	})

export const probeGlobalDaemonOwnerLiveness = (
	discovery: GlobalDaemonDiscovery,
): Effect.Effect<GlobalDaemonOwnerLiveness> =>
	Effect.sync(() => {
		try {
			process.kill(discovery.pid, 0)
			return "alive" as const
		} catch (cause) {
			if (isErrnoLike(cause) && cause.code === "ESRCH") {
				return "dead" as const
			}
			if (isErrnoLike(cause) && cause.code === "EPERM") {
				return "alive" as const
			}
			return "inaccessible" as const
		}
	})

export const readGlobalDaemonDiscovery = (params?: {
	readonly homeDirectory?: string
}): Effect.Effect<
	Option.Option<GlobalDaemonDiscovery>,
	GlobalDaemonRegistryError,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const pathService = yield* Path.Path
		const paths = yield* resolveGlobalDaemonRegistryPaths({
			homeDirectory: params?.homeDirectory,
			pathOps: pathService,
		})
		return yield* readDiscoveryAtPath(paths.discoveryPath)
	})

export const clearGlobalDaemonArtifacts = (params?: {
	readonly homeDirectory?: string
}): Effect.Effect<void, GlobalDaemonRegistryError, FileSystem.FileSystem | Path.Path> =>
	Effect.gen(function* () {
		const pathService = yield* Path.Path
		const paths = yield* resolveGlobalDaemonRegistryPaths({
			homeDirectory: params?.homeDirectory,
			pathOps: pathService,
		})
		yield* clearLockDir(paths.lockDir)
		yield* clearSocketPath(paths.socketPath)
	})

export const acquireGlobalDaemonLease = (params?: {
	readonly homeDirectory?: string
	readonly nowMs?: number
}): Effect.Effect<
	GlobalDaemonLease,
	GlobalDaemonRegistryError | GlobalDaemonAlreadyRunningError,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const pathService = yield* Path.Path
		const paths = yield* resolveGlobalDaemonRegistryPaths({
			homeDirectory: params?.homeDirectory,
			pathOps: pathService,
		})

		const pid = process.pid
		const lockId = crypto.randomUUID()
		const nowMs = params?.nowMs ?? Date.now()

		const acquired = yield* tryAcquireLockDir(paths)
		if (!acquired) {
			const existing = yield* readDiscoveryAtPath(paths.discoveryPath).pipe(
				Effect.flatMap((discovery) =>
					Option.isSome(discovery)
						? Effect.succeed(discovery)
						: Effect.sleep("50 millis").pipe(
								Effect.zipRight(readDiscoveryAtPath(paths.discoveryPath)),
							),
				),
			)
			if (Option.isNone(existing)) {
				yield* clearLockDir(paths.lockDir)
				yield* clearSocketPath(paths.socketPath)
				const reacquired = yield* tryAcquireLockDir(paths)
				if (!reacquired) {
					return yield* Effect.fail(
						new GlobalDaemonRegistryError({
							message: `Failed to reacquire lock dir '${paths.lockDir}' after stale missing-discovery cleanup`,
							cause: "reacquire_failed",
						}),
					)
				}
			} else {
				const liveness = yield* probeGlobalDaemonOwnerLiveness(existing.value)
				if (liveness !== "dead") {
					return yield* Effect.fail(
						new GlobalDaemonAlreadyRunningError({
							paths,
							discovery: existing.value,
							liveness,
						}),
					)
				}

				yield* clearLockDir(paths.lockDir)
				yield* clearSocketPath(paths.socketPath)
				const reacquired = yield* tryAcquireLockDir(paths)
				if (!reacquired) {
					return yield* Effect.fail(
						new GlobalDaemonRegistryError({
							message: `Failed to reacquire lock dir '${paths.lockDir}' after stale cleanup`,
							cause: "reacquire_failed",
						}),
					)
				}
			}
		}

		const discovery: GlobalDaemonDiscovery = {
			schemaVersion: GLOBAL_DAEMON_DISCOVERY_SCHEMA_VERSION,
			pid,
			lockId,
			socketPath: paths.socketPath,
			startedAtMs: nowMs,
		}

		yield* writeDiscoveryAtomically(paths, discovery, lockId).pipe(
			Effect.catchAll((cause) =>
				clearLockDir(paths.lockDir).pipe(
					Effect.catchAll(() => Effect.void),
					Effect.zipRight(Effect.fail(cause)),
				),
			),
		)

		return {
			lockId,
			pid,
			discovery,
			paths,
		}
	})

export const releaseGlobalDaemonLease = (
	lease: GlobalDaemonLease,
): Effect.Effect<void, GlobalDaemonRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const existing = yield* readDiscoveryAtPath(lease.paths.discoveryPath)
		if (Option.isNone(existing)) {
			return
		}
		if (existing.value.lockId !== lease.lockId || existing.value.pid !== lease.pid) {
			return
		}
		yield* clearLockDir(lease.paths.lockDir)
	})

export class GlobalDaemonDiscoveryService extends Effect.Service<GlobalDaemonDiscoveryApi>()(
	"GlobalDaemonDiscoveryService",
	{
		effect: Effect.succeed({
			readDiscovery: (params) => readGlobalDaemonDiscovery(params),
			probeOwnerLiveness: (discovery) => probeGlobalDaemonOwnerLiveness(discovery),
			clearArtifacts: (params) => clearGlobalDaemonArtifacts(params),
		} satisfies GlobalDaemonDiscoveryApi),
	},
) {}
