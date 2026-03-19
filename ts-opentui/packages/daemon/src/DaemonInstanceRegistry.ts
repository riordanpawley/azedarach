import { AZEDARACH_STORAGE_DIRECTORY } from "@azedarach/shared"
import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Schema } from "effect"

const DAEMON_DIRECTORY_NAME = "daemon"
const SYNC_LOCK_DIRECTORY_NAME = "sync.lock"
const OWNER_FILENAME = "owner.json"
const OWNER_SCHEMA_VERSION = 1 as const

const DaemonOwnerEndpointSchema = Schema.Struct({
	protocol: Schema.String,
	address: Schema.String,
})

const DaemonOwnerMetadataSchema = Schema.Struct({
	schemaVersion: Schema.Literal(OWNER_SCHEMA_VERSION),
	daemonKind: Schema.Literal("backend-sync"),
	projectPath: Schema.String,
	pid: Schema.Number.pipe(Schema.int()),
	lockId: Schema.String,
	acquiredAtMs: Schema.Number.pipe(Schema.int()),
	endpoint: DaemonOwnerEndpointSchema,
})

export type DaemonOwnerMetadata = Schema.Schema.Type<typeof DaemonOwnerMetadataSchema>

export type DaemonOwnerLiveness = "alive" | "dead" | "inaccessible"

export interface DaemonSyncLockPaths {
	readonly daemonDirectory: string
	readonly lockDirectory: string
	readonly ownerMetadataPath: string
}

export interface DaemonSyncInstanceLease {
	readonly lockId: string
	readonly pid: number
	readonly owner: DaemonOwnerMetadata
	readonly paths: DaemonSyncLockPaths
}

interface ErrnoLike {
	readonly code: unknown
}

const isErrnoLike = (value: unknown): value is ErrnoLike =>
	typeof value === "object" && value !== null && "code" in value

const isProcessPid = (value: number): boolean => Number.isInteger(value) && value > 0

const decodeOwnerMetadata = Schema.decode(Schema.parseJson(DaemonOwnerMetadataSchema))
const encodeOwnerMetadata = Schema.encode(Schema.parseJson(DaemonOwnerMetadataSchema))

export const resolveDaemonSyncLockPaths = (
	projectPath: string,
	pathOps: Pick<Path.Path, "join">,
): DaemonSyncLockPaths => {
	const daemonDirectory = pathOps.join(
		projectPath,
		AZEDARACH_STORAGE_DIRECTORY,
		DAEMON_DIRECTORY_NAME,
	)
	const lockDirectory = pathOps.join(daemonDirectory, SYNC_LOCK_DIRECTORY_NAME)
	return {
		daemonDirectory,
		lockDirectory,
		ownerMetadataPath: pathOps.join(lockDirectory, OWNER_FILENAME),
	}
}

const readOwnerMetadataFromPath = (
	ownerMetadataPath: string,
): Effect.Effect<
	Option.Option<DaemonOwnerMetadata>,
	DaemonInstanceRegistryError,
	FileSystem.FileSystem
> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const exists = yield* fs.exists(ownerMetadataPath).pipe(
			Effect.catchAll((cause) =>
				Effect.fail(
					new DaemonInstanceRegistryError({
						message: `Failed to check daemon metadata path '${ownerMetadataPath}'`,
						cause,
					}),
				),
			),
		)
		if (!exists) {
			return Option.none()
		}

		const raw = yield* fs.readFileString(ownerMetadataPath).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to read daemon metadata '${ownerMetadataPath}'`,
						cause,
					}),
			),
		)
		const owner = yield* decodeOwnerMetadata(raw).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to decode daemon metadata '${ownerMetadataPath}'`,
						cause,
					}),
			),
		)
		return Option.some(owner)
	})

const writeOwnerMetadataAtomically = (
	paths: DaemonSyncLockPaths,
	owner: DaemonOwnerMetadata,
	lockId: string,
): Effect.Effect<void, DaemonInstanceRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const encoded = yield* encodeOwnerMetadata(owner).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to encode daemon metadata '${paths.ownerMetadataPath}'`,
						cause,
					}),
			),
		)
		const tempPath = `${paths.ownerMetadataPath}.${lockId}.tmp`
		yield* fs.writeFileString(tempPath, encoded).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to write daemon metadata temp file '${tempPath}'`,
						cause,
					}),
			),
		)
		yield* fs.rename(tempPath, paths.ownerMetadataPath).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to finalize daemon metadata '${paths.ownerMetadataPath}'`,
						cause,
					}),
			),
		)
	})

const tryAcquireLockDirectory = (
	paths: DaemonSyncLockPaths,
): Effect.Effect<boolean, DaemonInstanceRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		yield* fs.makeDirectory(paths.daemonDirectory, { recursive: true }).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to create daemon directory '${paths.daemonDirectory}'`,
						cause,
					}),
			),
		)

		return yield* fs.makeDirectory(paths.lockDirectory).pipe(
			Effect.as(true),
			Effect.catchAll((cause) =>
				fs.exists(paths.lockDirectory).pipe(
					Effect.map((exists) => (exists ? false : true)),
					Effect.catchAll((existsCause) =>
						Effect.fail(
							new DaemonInstanceRegistryError({
								message: `Failed to resolve lock directory status '${paths.lockDirectory}'`,
								cause: existsCause,
							}),
						),
					),
					Effect.flatMap((acquired) =>
						acquired
							? Effect.fail(
									new DaemonInstanceRegistryError({
										message: `Failed to create lock directory '${paths.lockDirectory}'`,
										cause,
									}),
								)
							: Effect.succeed(false),
					),
				),
			),
		)
	})

export class DaemonInstanceRegistryError extends Data.TaggedError("DaemonInstanceRegistryError")<{
	readonly message: string
	readonly cause: unknown
}> {}

export class DaemonInstanceAlreadyRunningError extends Data.TaggedError(
	"DaemonInstanceAlreadyRunningError",
)<{
	readonly projectPath: string
	readonly lockDirectory: string
	readonly owner: DaemonOwnerMetadata
	readonly liveness: DaemonOwnerLiveness
}> {}

export const checkDaemonOwnerLiveness = (
	owner: DaemonOwnerMetadata,
): Effect.Effect<DaemonOwnerLiveness, never> =>
	Effect.sync(() => {
		if (!isProcessPid(owner.pid)) {
			return "inaccessible"
		}
		try {
			process.kill(owner.pid, 0)
			return "alive"
		} catch (cause) {
			if (isErrnoLike(cause) && cause.code === "ESRCH") {
				return "dead"
			}
			if (isErrnoLike(cause) && cause.code === "EPERM") {
				return "alive"
			}
			return "inaccessible"
		}
	})

export const readDaemonSyncDiscovery = (
	projectPath: string,
): Effect.Effect<
	Option.Option<DaemonOwnerMetadata>,
	DaemonInstanceRegistryError,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const pathService = yield* Path.Path
		const paths = resolveDaemonSyncLockPaths(projectPath, pathService)
		return yield* readOwnerMetadataFromPath(paths.ownerMetadataPath)
	})

const clearStaleLockDirectory = (
	paths: DaemonSyncLockPaths,
): Effect.Effect<void, DaemonInstanceRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		yield* fs.remove(paths.lockDirectory, { recursive: true, force: true }).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to clear stale daemon lock '${paths.lockDirectory}'`,
						cause,
					}),
			),
		)
	})

export const acquireDaemonSyncInstanceLease = (params: {
	readonly projectPath: string
	readonly endpoint: {
		readonly protocol: string
		readonly address: string
	}
}): Effect.Effect<
	DaemonSyncInstanceLease,
	DaemonInstanceRegistryError | DaemonInstanceAlreadyRunningError,
	FileSystem.FileSystem | Path.Path
> =>
	Effect.gen(function* () {
		const pathService = yield* Path.Path
		const fs = yield* FileSystem.FileSystem
		const paths = resolveDaemonSyncLockPaths(params.projectPath, pathService)
		const pid = process.pid
		const lockId = crypto.randomUUID()

		const acquired = yield* tryAcquireLockDirectory(paths)
		if (!acquired) {
			const existing = yield* readOwnerMetadataFromPath(paths.ownerMetadataPath)
			if (Option.isNone(existing)) {
				return yield* Effect.fail(
					new DaemonInstanceRegistryError({
						message: `Daemon lock exists but metadata is missing at '${paths.ownerMetadataPath}'. Remove '${paths.lockDirectory}' if no daemon is running.`,
						cause: "missing_metadata",
					}),
				)
			}

			const liveness = yield* checkDaemonOwnerLiveness(existing.value)
			if (liveness !== "dead") {
				return yield* Effect.fail(
					new DaemonInstanceAlreadyRunningError({
						projectPath: params.projectPath,
						lockDirectory: paths.lockDirectory,
						owner: existing.value,
						liveness,
					}),
				)
			}

			yield* clearStaleLockDirectory(paths)
			const reacquired = yield* tryAcquireLockDirectory(paths)
			if (!reacquired) {
				return yield* Effect.fail(
					new DaemonInstanceRegistryError({
						message: `Failed to acquire daemon lock directory '${paths.lockDirectory}' after stale lock cleanup`,
						cause: "reacquire_failed",
					}),
				)
			}
		}

		const owner: DaemonOwnerMetadata = {
			schemaVersion: OWNER_SCHEMA_VERSION,
			daemonKind: "backend-sync",
			projectPath: params.projectPath,
			pid,
			lockId,
			acquiredAtMs: Date.now(),
			endpoint: {
				protocol: params.endpoint.protocol,
				address: params.endpoint.address,
			},
		}

		yield* writeOwnerMetadataAtomically(paths, owner, lockId).pipe(
			Effect.catchAll((cause) =>
				clearStaleLockDirectory(paths).pipe(
					Effect.catchAll(() => Effect.void),
					Effect.zipRight(Effect.fail(cause)),
				),
			),
		)

		return {
			lockId,
			pid,
			owner,
			paths,
		}
	})

export const releaseDaemonSyncInstanceLease = (
	lease: DaemonSyncInstanceLease,
): Effect.Effect<void, DaemonInstanceRegistryError, FileSystem.FileSystem> =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const existing = yield* readOwnerMetadataFromPath(lease.paths.ownerMetadataPath)
		if (Option.isNone(existing)) {
			return
		}
		if (existing.value.lockId !== lease.lockId || existing.value.pid !== lease.pid) {
			return
		}
		yield* fs.remove(lease.paths.lockDirectory, { recursive: true, force: true }).pipe(
			Effect.mapError(
				(cause) =>
					new DaemonInstanceRegistryError({
						message: `Failed to release daemon lock '${lease.paths.lockDirectory}'`,
						cause,
					}),
			),
		)
	})

export const formatDaemonInstanceAlreadyRunningMessage = (
	error: DaemonInstanceAlreadyRunningError,
): string =>
	[
		`A headless sync daemon is already running for ${error.projectPath}.`,
		`Owner pid=${error.owner.pid} (liveness=${error.liveness}, acquiredAtMs=${error.owner.acquiredAtMs}).`,
		`Endpoint: ${error.owner.endpoint.protocol}://${error.owner.endpoint.address}`,
		`If this owner is stale, remove ${error.lockDirectory} and retry.`,
	].join(" ")
