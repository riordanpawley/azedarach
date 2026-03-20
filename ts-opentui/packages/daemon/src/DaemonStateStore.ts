import type { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Schema } from "effect"
import type {
	BackendSyncDaemonRunStatus,
	BackendSyncDaemonStatus,
} from "./BackendSyncDaemonService.js"
import { AZEDARACH_STORAGE_DIRECTORY } from "./storagePaths.js"

const DAEMON_DIRECTORY_NAME = "daemon"
const DAEMON_STATE_FILENAME = "backend-sync-state.json"
const DAEMON_STATE_TEMP_SUFFIX = ".tmp"
const DAEMON_STATE_SCHEMA_VERSION = 2 as const

const DaemonRunStatusSchema: Schema.Schema<BackendSyncDaemonRunStatus> = Schema.Struct({
	runAtMs: Schema.Number.pipe(Schema.int()),
	result: Schema.Literal("flushed", "skipped", "failed"),
	pushed: Schema.Number.pipe(Schema.int()),
	pulled: Schema.Number.pipe(Schema.int()),
	message: Schema.NullOr(Schema.String),
})

const PersistedDaemonRuntimeSchema = Schema.Struct({
	state: Schema.Literal("stopped", "running", "degraded", "crashed"),
	generation: Schema.Number.pipe(Schema.int()),
	intervalMs: Schema.NullOr(Schema.Number.pipe(Schema.int())),
	startedAtMs: Schema.NullOr(Schema.Number.pipe(Schema.int())),
})

const PersistedDaemonSyncSchema = Schema.Struct({
	runCount: Schema.Number.pipe(Schema.int()),
	successCount: Schema.Number.pipe(Schema.int()),
	failureCount: Schema.Number.pipe(Schema.int()),
	failureStreak: Schema.Number.pipe(Schema.int()),
	restartStreak: Schema.Number.pipe(Schema.int()),
	lastBackoffMs: Schema.NullOr(Schema.Number.pipe(Schema.int())),
	lastSuccessfulRunAtMs: Schema.NullOr(Schema.Number.pipe(Schema.int())),
	lastRun: Schema.NullOr(DaemonRunStatusSchema),
	lastError: Schema.NullOr(Schema.String),
})

const PersistedDaemonSessionSchema = Schema.Struct({
	persistedAtMs: Schema.Number.pipe(Schema.int()),
	persistedByPid: Schema.Number.pipe(Schema.int()),
})

const PersistedDaemonStateSchema = Schema.Struct({
	schemaVersion: Schema.Literal(DAEMON_STATE_SCHEMA_VERSION),
	runtime: PersistedDaemonRuntimeSchema,
	sync: PersistedDaemonSyncSchema,
	session: PersistedDaemonSessionSchema,
})

const PersistedDaemonStateJsonSchema = Schema.parseJson(PersistedDaemonStateSchema)

export type PersistedDaemonState = Schema.Schema.Type<typeof PersistedDaemonStateSchema>

export interface DaemonStateStorePaths {
	readonly daemonDirectory: string
	readonly statePath: string
	readonly tempPath: string
}

export interface DaemonStateStoreApi {
	readonly load: () => Effect.Effect<Option.Option<PersistedDaemonState>, DaemonStateStoreError>
	readonly persist: (status: BackendSyncDaemonStatus) => Effect.Effect<void, DaemonStateStoreError>
}

export class DaemonStateStoreError extends Data.TaggedError("DaemonStateStoreError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

const decodePersistedDaemonState = Schema.decode(PersistedDaemonStateJsonSchema)
const encodePersistedDaemonState = Schema.encode(PersistedDaemonStateJsonSchema)

const toPersistedState = (status: BackendSyncDaemonStatus): PersistedDaemonState => ({
	schemaVersion: DAEMON_STATE_SCHEMA_VERSION,
	runtime: {
		state: status.state,
		generation: status.generation,
		intervalMs: status.intervalMs,
		startedAtMs: status.startedAtMs,
	},
	sync: {
		runCount: status.runCount,
		successCount: status.successCount,
		failureCount: status.failureCount,
		failureStreak: status.failureStreak,
		restartStreak: status.restartStreak,
		lastBackoffMs: status.lastBackoffMs,
		lastSuccessfulRunAtMs: status.lastSuccessfulRunAtMs,
		lastRun: status.lastRun,
		lastError: status.lastError,
	},
	session: {
		persistedAtMs: Date.now(),
		persistedByPid: process.pid,
	},
})

export const toDaemonStatus = (persisted: PersistedDaemonState): BackendSyncDaemonStatus => ({
	state: persisted.runtime.state,
	generation: persisted.runtime.generation,
	intervalMs: persisted.runtime.intervalMs,
	startedAtMs: persisted.runtime.startedAtMs,
	runCount: persisted.sync.runCount,
	successCount: persisted.sync.successCount,
	failureCount: persisted.sync.failureCount,
	failureStreak: persisted.sync.failureStreak,
	restartStreak: persisted.sync.restartStreak,
	lastBackoffMs: persisted.sync.lastBackoffMs,
	lastSuccessfulRunAtMs: persisted.sync.lastSuccessfulRunAtMs,
	lastRun: persisted.sync.lastRun,
	lastError: persisted.sync.lastError,
})

export const resolveDaemonStateStorePaths = (
	pathOps: Pick<Path.Path, "join">,
): DaemonStateStorePaths => {
	const daemonRootPath = process.env.HOME ?? process.cwd()
	const daemonDirectory = pathOps.join(
		daemonRootPath,
		AZEDARACH_STORAGE_DIRECTORY,
		DAEMON_DIRECTORY_NAME,
	)
	const statePath = pathOps.join(daemonDirectory, DAEMON_STATE_FILENAME)
	return {
		daemonDirectory,
		statePath,
		tempPath: `${statePath}${DAEMON_STATE_TEMP_SUFFIX}`,
	}
}

export const makeDaemonStateStore = (dependencies: {
	readonly fs: Pick<
		FileSystem.FileSystem,
		"exists" | "makeDirectory" | "readFileString" | "remove" | "rename" | "writeFileString"
	>
	readonly path: Pick<Path.Path, "join">
}): DaemonStateStoreApi => ({
	load: () =>
		Effect.gen(function* () {
			const paths = resolveDaemonStateStorePaths(dependencies.path)
			const exists = yield* dependencies.fs.exists(paths.statePath).pipe(
				Effect.catchAll((cause) =>
					Effect.fail(
						new DaemonStateStoreError({
							message: `Failed to stat daemon state '${paths.statePath}'`,
							cause,
						}),
					),
				),
			)
			if (!exists) {
				return Option.none()
			}

			const raw = yield* dependencies.fs.readFileString(paths.statePath).pipe(
				Effect.mapError(
					(cause) =>
						new DaemonStateStoreError({
							message: `Failed to read daemon state '${paths.statePath}'`,
							cause,
						}),
				),
			)
			const decoded = yield* decodePersistedDaemonState(raw).pipe(
				Effect.option,
				Effect.catchAll(() => Effect.succeed(Option.none())),
			)
			return decoded
		}),
	persist: (status: BackendSyncDaemonStatus) =>
		Effect.gen(function* () {
			const paths = resolveDaemonStateStorePaths(dependencies.path)
			yield* dependencies.fs.makeDirectory(paths.daemonDirectory, { recursive: true }).pipe(
				Effect.mapError(
					(cause) =>
						new DaemonStateStoreError({
							message: `Failed to create daemon state directory '${paths.daemonDirectory}'`,
							cause,
						}),
				),
			)
			const encoded = yield* encodePersistedDaemonState(toPersistedState(status)).pipe(
				Effect.mapError(
					(cause) =>
						new DaemonStateStoreError({
							message: `Failed to encode daemon state for '${paths.statePath}'`,
							cause,
						}),
				),
			)
			yield* dependencies.fs
				.remove(paths.tempPath, { force: true, recursive: false })
				.pipe(Effect.orElseSucceed(() => undefined))
			yield* dependencies.fs.writeFileString(paths.tempPath, encoded).pipe(
				Effect.mapError(
					(cause) =>
						new DaemonStateStoreError({
							message: `Failed to write daemon state temp file '${paths.tempPath}'`,
							cause,
						}),
				),
			)
			yield* dependencies.fs.rename(paths.tempPath, paths.statePath).pipe(
				Effect.mapError(
					(cause) =>
						new DaemonStateStoreError({
							message: `Failed to finalize daemon state '${paths.statePath}'`,
							cause,
						}),
				),
				Effect.catchAll((error) =>
					dependencies.fs.remove(paths.tempPath, { force: true, recursive: false }).pipe(
						Effect.orElseSucceed(() => undefined),
						Effect.zipRight(Effect.fail(error)),
					),
				),
			)
		}),
})
