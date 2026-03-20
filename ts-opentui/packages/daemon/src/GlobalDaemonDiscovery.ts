import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
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

export type GlobalDaemonDiscoveryMetadata = Schema.Schema.Type<typeof GlobalDaemonDiscoverySchema>

export type GlobalDaemonOwnerLiveness = "alive" | "dead" | "inaccessible"

export interface GlobalDaemonDiscoveryPaths {
	readonly daemonDir: string
	readonly socketPath: string
	readonly lockDir: string
	readonly discoveryPath: string
}

export interface GlobalDaemonLease {
	readonly lockId: string
	readonly pid: number
	readonly discovery: GlobalDaemonDiscoveryMetadata
	readonly paths: GlobalDaemonDiscoveryPaths
}

interface ErrnoLike {
	readonly code: unknown
}

const isErrnoLike = (value: unknown): value is ErrnoLike =>
	typeof value === "object" && value !== null && "code" in value

const decodeDiscovery = Schema.decode(Schema.parseJson(GlobalDaemonDiscoverySchema))
const encodeDiscovery = Schema.encode(Schema.parseJson(GlobalDaemonDiscoverySchema))

export class GlobalDaemonDiscoveryError extends Data.TaggedError("GlobalDaemonDiscoveryError")<{
	readonly message: string
	readonly cause: unknown
}> {}

export class GlobalDaemonAlreadyRunningError extends Data.TaggedError(
	"GlobalDaemonAlreadyRunningError",
)<{
	readonly paths: GlobalDaemonDiscoveryPaths
	readonly discovery: GlobalDaemonDiscoveryMetadata
	readonly liveness: GlobalDaemonOwnerLiveness
}> {}

export interface GlobalDaemonDiscoveryApi {
	readonly resolvePaths: (params?: {
		readonly homeDirectory?: string
	}) => Effect.Effect<GlobalDaemonDiscoveryPaths, GlobalDaemonDiscoveryError>
	readonly readDiscovery: (params?: {
		readonly homeDirectory?: string
	}) => Effect.Effect<Option.Option<GlobalDaemonDiscoveryMetadata>, GlobalDaemonDiscoveryError>
	readonly probeOwnerLiveness: (
		discovery: GlobalDaemonDiscoveryMetadata,
	) => Effect.Effect<GlobalDaemonOwnerLiveness, never>
	readonly clearArtifacts: (params?: {
		readonly homeDirectory?: string
	}) => Effect.Effect<void, GlobalDaemonDiscoveryError>
	readonly acquireLease: (params?: {
		readonly homeDirectory?: string
		readonly nowMs?: number
	}) => Effect.Effect<
		GlobalDaemonLease,
		GlobalDaemonDiscoveryError | GlobalDaemonAlreadyRunningError
	>
	readonly releaseLease: (
		lease: GlobalDaemonLease,
	) => Effect.Effect<void, GlobalDaemonDiscoveryError>
}

const resolveHomeDirectory = (
	homeDirectory: string | undefined,
): Effect.Effect<string, GlobalDaemonDiscoveryError> => {
	const resolvedHome = homeDirectory ?? process.env.HOME
	if (resolvedHome === undefined || resolvedHome.trim().length === 0) {
		return Effect.fail(
			new GlobalDaemonDiscoveryError({
				message: "HOME is not set; cannot resolve global daemon directory",
				cause: "missing_home",
			}),
		)
	}
	return Effect.succeed(resolvedHome)
}

const probeGlobalDaemonOwnerLiveness = (
	discovery: GlobalDaemonDiscoveryMetadata,
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

export class GlobalDaemonDiscovery extends Effect.Service<GlobalDaemonDiscoveryApi>()(
	"GlobalDaemonDiscovery",
	{
		dependencies: [BunContext.layer],
		effect: Effect.gen(function* () {
			const fileSystem = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path

			const resolvePaths: GlobalDaemonDiscoveryApi["resolvePaths"] = (params) =>
				Effect.gen(function* () {
					const home = yield* resolveHomeDirectory(params?.homeDirectory)
					const daemonDir = pathService.join(home, GLOBAL_DAEMON_STORAGE_DIR)
					const lockDir = pathService.join(daemonDir, GLOBAL_DAEMON_LOCK_DIR)
					return {
						daemonDir,
						socketPath: pathService.join(daemonDir, GLOBAL_DAEMON_SOCKET_FILE),
						lockDir,
						discoveryPath: pathService.join(lockDir, GLOBAL_DAEMON_DISCOVERY_FILE),
					}
				})

			const readDiscoveryAtPath = (
				discoveryPath: string,
			): Effect.Effect<Option.Option<GlobalDaemonDiscoveryMetadata>, GlobalDaemonDiscoveryError> =>
				Effect.gen(function* () {
					const exists = yield* fileSystem.exists(discoveryPath).pipe(
						Effect.catchAll((cause) =>
							Effect.fail(
								new GlobalDaemonDiscoveryError({
									message: `Failed to check discovery file '${discoveryPath}'`,
									cause,
								}),
							),
						),
					)
					if (!exists) {
						return Option.none()
					}
					const raw = yield* fileSystem.readFileString(discoveryPath).pipe(
						Effect.mapError(
							(cause) =>
								new GlobalDaemonDiscoveryError({
									message: `Failed to read discovery file '${discoveryPath}'`,
									cause,
								}),
						),
					)
					const decoded = yield* decodeDiscovery(raw).pipe(
						Effect.mapError(
							(cause) =>
								new GlobalDaemonDiscoveryError({
									message: `Failed to decode discovery file '${discoveryPath}'`,
									cause,
								}),
						),
					)
					return Option.some(decoded)
				})

			const clearLockDir = (lockDir: string): Effect.Effect<void, GlobalDaemonDiscoveryError> =>
				fileSystem.remove(lockDir, { recursive: true, force: true }).pipe(
					Effect.mapError(
						(cause) =>
							new GlobalDaemonDiscoveryError({
								message: `Failed to clear lock dir '${lockDir}'`,
								cause,
							}),
					),
				)

			const clearSocketPath = (
				socketPath: string,
			): Effect.Effect<void, GlobalDaemonDiscoveryError> =>
				fileSystem.remove(socketPath, { force: true }).pipe(
					Effect.mapError(
						(cause) =>
							new GlobalDaemonDiscoveryError({
								message: `Failed to clear socket path '${socketPath}'`,
								cause,
							}),
					),
				)

			const tryAcquireLockDir = (
				paths: GlobalDaemonDiscoveryPaths,
			): Effect.Effect<boolean, GlobalDaemonDiscoveryError> =>
				Effect.gen(function* () {
					yield* fileSystem.makeDirectory(paths.daemonDir, { recursive: true }).pipe(
						Effect.mapError(
							(cause) =>
								new GlobalDaemonDiscoveryError({
									message: `Failed to create daemon directory '${paths.daemonDir}'`,
									cause,
								}),
						),
					)
					return yield* fileSystem.makeDirectory(paths.lockDir).pipe(
						Effect.as(true),
						Effect.catchAll((cause) =>
							fileSystem.exists(paths.lockDir).pipe(
								Effect.flatMap((exists) =>
									exists
										? Effect.succeed(false)
										: Effect.fail(
												new GlobalDaemonDiscoveryError({
													message: `Failed to create lock dir '${paths.lockDir}'`,
													cause,
												}),
											),
								),
								Effect.catchAll((existsCause) =>
									Effect.fail(
										new GlobalDaemonDiscoveryError({
											message: `Failed to inspect lock dir '${paths.lockDir}'`,
											cause: existsCause,
										}),
									),
								),
							),
						),
					)
				})

			const writeDiscoveryAtomically = (
				paths: GlobalDaemonDiscoveryPaths,
				discovery: GlobalDaemonDiscoveryMetadata,
				lockId: string,
			): Effect.Effect<void, GlobalDaemonDiscoveryError> =>
				Effect.gen(function* () {
					const encoded = yield* encodeDiscovery(discovery).pipe(
						Effect.mapError(
							(cause) =>
								new GlobalDaemonDiscoveryError({
									message: `Failed to encode discovery metadata '${paths.discoveryPath}'`,
									cause,
								}),
						),
					)
					const tempPath = `${paths.discoveryPath}.${lockId}.tmp`
					yield* fileSystem.writeFileString(tempPath, encoded).pipe(
						Effect.mapError(
							(cause) =>
								new GlobalDaemonDiscoveryError({
									message: `Failed to write discovery temp file '${tempPath}'`,
									cause,
								}),
						),
					)
					yield* fileSystem.rename(tempPath, paths.discoveryPath).pipe(
						Effect.mapError(
							(cause) =>
								new GlobalDaemonDiscoveryError({
									message: `Failed to finalize discovery file '${paths.discoveryPath}'`,
									cause,
								}),
						),
					)
				})

			const readDiscovery: GlobalDaemonDiscoveryApi["readDiscovery"] = (params) =>
				Effect.gen(function* () {
					const paths = yield* resolvePaths(params)
					return yield* readDiscoveryAtPath(paths.discoveryPath)
				})

			const clearArtifacts: GlobalDaemonDiscoveryApi["clearArtifacts"] = (params) =>
				Effect.gen(function* () {
					const paths = yield* resolvePaths(params)
					yield* clearLockDir(paths.lockDir)
					yield* clearSocketPath(paths.socketPath)
				})

			const acquireLease: GlobalDaemonDiscoveryApi["acquireLease"] = (params) =>
				Effect.gen(function* () {
					const paths = yield* resolvePaths(params)
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
									new GlobalDaemonDiscoveryError({
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
									new GlobalDaemonDiscoveryError({
										message: `Failed to reacquire lock dir '${paths.lockDir}' after stale cleanup`,
										cause: "reacquire_failed",
									}),
								)
							}
						}
					}

					const discovery: GlobalDaemonDiscoveryMetadata = {
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

			const releaseLease: GlobalDaemonDiscoveryApi["releaseLease"] = (lease) =>
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

			return {
				resolvePaths,
				readDiscovery,
				probeOwnerLiveness: (discovery) => probeGlobalDaemonOwnerLiveness(discovery),
				clearArtifacts,
				acquireLease,
				releaseLease,
			} satisfies GlobalDaemonDiscoveryApi
		}),
	},
) {}
