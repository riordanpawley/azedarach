import { Reactivity } from "@effect/experimental"
import { FileSystem, Path } from "@effect/platform"
import type * as SqlClient from "@effect/sql/SqlClient"
import type { SqlError } from "@effect/sql/SqlError"
import { SqliteClient } from "@effect/sql-sqlite-bun"
import { Cause, Data, DateTime, Duration, Effect, HashMap, Schema } from "effect"
import type { SessionState } from "../ui/types.js"
import { getProjectStoragePaths } from "./storagePaths.js"

export interface PersistedSession {
	readonly issueId: string
	readonly worktreePath: string
	readonly tmuxSessionName: string
	readonly state: SessionState
	readonly startedAt: DateTime.Utc
	readonly projectPath: string
}

export interface SessionStateStoreService {
	readonly load: (
		projectPath: string,
	) => Effect.Effect<HashMap.HashMap<string, PersistedSession>, SessionStateStoreError>
	readonly save: (
		projectPath: string,
		sessions: HashMap.HashMap<string, PersistedSession>,
	) => Effect.Effect<void, SessionStateStoreError>
}

export class SessionStateStoreError extends Data.TaggedError("SessionStateStoreError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

const LEGACY_SESSION_FILENAME = "sessions.json"
const LEGACY_MIGRATION_NAME = "legacy_sessions_json_to_sqlite_v1"

const SessionStateSchema = Schema.Literal(
	"idle",
	"initializing",
	"busy",
	"waiting",
	"done",
	"error",
	"paused",
	"warning",
	"crashed",
)

const LegacySessionSchema = Schema.Struct({
	issueId: Schema.String,
	worktreePath: Schema.String,
	tmuxSessionName: Schema.String,
	state: SessionStateSchema,
	startedAt: Schema.DateTimeUtc,
	projectPath: Schema.optional(Schema.String),
})

const LegacySessionsSchema = Schema.parseJson(
	Schema.HashMap({
		key: Schema.String,
		value: LegacySessionSchema,
	}),
)

const decodeLegacySessions = Schema.decodeUnknown(LegacySessionsSchema)

interface SessionRow {
	readonly project_path: string
	readonly issue_id: string
	readonly worktree_path: string
	readonly tmux_session_name: string
	readonly state: string
	readonly started_at: string
}

interface MigrationRow {
	readonly applied: number
}

interface LegacyLoadResult {
	readonly sessions: HashMap.HashMap<string, PersistedSession>
	readonly filePath: string | null
}

const sessionSchemaStatements: readonly string[] = [
	`CREATE TABLE IF NOT EXISTS session_state (
		project_path TEXT NOT NULL,
		issue_id TEXT NOT NULL,
		worktree_path TEXT NOT NULL,
		tmux_session_name TEXT NOT NULL,
		state TEXT NOT NULL,
		started_at TEXT NOT NULL,
		PRIMARY KEY (project_path, issue_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_session_state_tmux ON session_state(project_path, tmux_session_name)`,
	`CREATE TABLE IF NOT EXISTS session_state_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`,
]

const normalizeProjectPath = (projectPath: string): string => {
	const trimmed = projectPath.trim()
	if (trimmed.length === 0) {
		return process.cwd().replace(/\/+$/, "")
	}
	const withoutTrailingSlashes = trimmed.replace(/\/+$/, "")
	return withoutTrailingSlashes.length === 0 ? "/" : withoutTrailingSlashes
}

const parseSessionState = (value: string): SessionState | undefined => {
	switch (value) {
		case "idle":
		case "initializing":
		case "busy":
		case "waiting":
		case "done":
		case "error":
		case "paused":
		case "warning":
		case "crashed":
			return value
		default:
			return undefined
	}
}

const parseStartedAt = (value: string): DateTime.Utc | undefined => {
	const parsedMs = Date.parse(value)
	if (Number.isNaN(parsedMs)) {
		return undefined
	}
	return DateTime.unsafeFromDate(new Date(parsedMs))
}

const mapSqlError = (message: string, cause: unknown): SessionStateStoreError =>
	new SessionStateStoreError({ message: `${message}: ${String(cause)}`, cause })

const SQLITE_TRANSIENT_ERROR_CODES = new Set(["SQLITE_BUSY", "SQLITE_LOCKED"])
const SQLITE_TRANSIENT_ERROR_NUMBERS = new Set([5, 6])
const SQLITE_OPERATION_RETRY_MAX_ATTEMPTS = 4
const SQLITE_OPERATION_RETRY_BASE_DELAY_MS = 80
const SQLITE_OPERATION_RETRY_MAX_DELAY_MS = 600
const SQLITE_BUSY_TIMEOUT_MS = 3000

const isObjectRecord = (value: unknown): value is Record<string, unknown> =>
	typeof value === "object" && value !== null

const extractStructuredSqliteErrorSignal = (
	value: unknown,
): { readonly code?: string; readonly errno?: number } => {
	let current: unknown = value
	let depth = 0

	while (isObjectRecord(current) && depth < 10) {
		const code = Reflect.get(current, "code")
		if (typeof code === "string" && code.trim().length > 0) {
			return { code }
		}

		const errno = Reflect.get(current, "errno")
		if (typeof errno === "number" && Number.isFinite(errno)) {
			return { errno: Math.trunc(errno) }
		}

		current = Reflect.get(current, "cause")
		depth += 1
	}

	return {}
}

const isTransientSqliteFailure = (value: unknown): boolean => {
	const signal = extractStructuredSqliteErrorSignal(value)
	if (signal.code !== undefined && SQLITE_TRANSIENT_ERROR_CODES.has(signal.code)) {
		return true
	}
	return signal.errno !== undefined && SQLITE_TRANSIENT_ERROR_NUMBERS.has(signal.errno)
}

const backoffDelayMs = (attempt: number): number =>
	Math.min(
		SQLITE_OPERATION_RETRY_BASE_DELAY_MS * Math.max(1, 2 ** (attempt - 1)),
		SQLITE_OPERATION_RETRY_MAX_DELAY_MS,
	)

const withTransientSqliteRetry = <A>(
	effect: Effect.Effect<A, SessionStateStoreError>,
): Effect.Effect<A, SessionStateStoreError> => {
	const attemptWithRetry = (attempt: number): Effect.Effect<A, SessionStateStoreError> =>
		effect.pipe(
			Effect.catchAll((error) => {
				const transient = error.cause !== undefined && isTransientSqliteFailure(error.cause)
				if (!transient || attempt >= SQLITE_OPERATION_RETRY_MAX_ATTEMPTS) {
					return Effect.fail(error)
				}

				const nextAttempt = attempt + 1
				const delayMs = backoffDelayMs(attempt)
				return Effect.logWarning(
					`SessionStateStore transient sqlite lock detected; retry ${nextAttempt}/${SQLITE_OPERATION_RETRY_MAX_ATTEMPTS} in ${delayMs}ms`,
				).pipe(
					Effect.zipRight(Effect.sleep(Duration.millis(delayMs))),
					Effect.zipRight(attemptWithRetry(nextAttempt)),
				)
			}),
		)

	return attemptWithRetry(1)
}

export class SessionStateStore extends Effect.Service<SessionStateStore>()("SessionStateStore", {
	effect: Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const withSql = <A>(
			projectPath: string,
			operation: (
				sql: SqlClient.SqlClient,
				normalizedProjectPath: string,
			) => Effect.Effect<A, SqlError | SessionStateStoreError>,
		): Effect.Effect<A, SessionStateStoreError> =>
			withTransientSqliteRetry(
				Effect.gen(function* () {
					const normalizedProjectPath = normalizeProjectPath(projectPath)
					const storagePaths = getProjectStoragePaths(normalizedProjectPath, pathService)
					const canonicalDbExists = yield* fs
						.exists(storagePaths.canonicalDbPath)
						.pipe(Effect.orElseSucceed(() => false))
					const legacyDbExists = canonicalDbExists
						? false
						: yield* fs.exists(storagePaths.legacyDbPath).pipe(Effect.orElseSucceed(() => false))
					const dbPath = canonicalDbExists
						? storagePaths.canonicalDbPath
						: legacyDbExists
							? storagePaths.legacyDbPath
							: storagePaths.canonicalDbPath

					yield* fs
						.makeDirectory(storagePaths.storageDirectory, { recursive: true })
						.pipe(
							Effect.mapError((cause) => mapSqlError("Failed to create sqlite directory", cause)),
						)

					return yield* Effect.scoped(
						Effect.gen(function* () {
							const sql = yield* SqliteClient.make({ filename: dbPath }).pipe(
								Effect.provide(Reactivity.layer),
							)
							yield* sql.unsafe("PRAGMA journal_mode = WAL")
							yield* sql.unsafe("PRAGMA synchronous = NORMAL")
							yield* sql.unsafe(`PRAGMA busy_timeout = ${SQLITE_BUSY_TIMEOUT_MS}`)
							for (const statement of sessionSchemaStatements) {
								yield* sql.unsafe(statement)
							}
							return yield* operation(sql, normalizedProjectPath)
						}),
					).pipe(
						Effect.catchAllCause((cause) =>
							Effect.fail(mapSqlError("SQLite operation failed", Cause.squash(cause))),
						),
					)
				}),
			)

		const loadFromSqlite = (
			projectPath: string,
		): Effect.Effect<HashMap.HashMap<string, PersistedSession>, SessionStateStoreError> =>
			withSql(projectPath, (sql, normalizedProjectPath) =>
				Effect.gen(function* () {
					const rows = yield* sql<SessionRow>`
						SELECT
							project_path,
							issue_id,
							worktree_path,
							tmux_session_name,
							state,
							started_at
						FROM session_state
						WHERE project_path = ${normalizedProjectPath}
					`

					let sessions = HashMap.empty<string, PersistedSession>()
					for (const row of rows) {
						const state = parseSessionState(row.state)
						if (state === undefined) {
							yield* Effect.logWarning(
								`Skipping persisted session '${row.issue_id}': invalid state '${row.state}'`,
							)
							continue
						}

						const startedAt = parseStartedAt(row.started_at)
						if (startedAt === undefined) {
							yield* Effect.logWarning(
								`Skipping persisted session '${row.issue_id}': invalid started_at '${row.started_at}'`,
							)
							continue
						}

						const session: PersistedSession = {
							issueId: row.issue_id,
							worktreePath: row.worktree_path,
							tmuxSessionName: row.tmux_session_name,
							state,
							startedAt,
							projectPath: row.project_path,
						}
						sessions = HashMap.set(sessions, session.issueId, session)
					}

					return sessions
				}),
			)

		const isLegacyMigrationApplied = (
			projectPath: string,
		): Effect.Effect<boolean, SessionStateStoreError> =>
			withSql(projectPath, (sql) =>
				sql<MigrationRow>`
					SELECT CASE WHEN EXISTS(
						SELECT 1
						FROM session_state_migrations
						WHERE name = ${LEGACY_MIGRATION_NAME}
					) THEN 1 ELSE 0 END as applied
				`.pipe(Effect.map((rows) => rows[0]?.applied === 1)),
			)

		const markLegacyMigrationApplied = (
			projectPath: string,
		): Effect.Effect<void, SessionStateStoreError> =>
			withSql(projectPath, (sql) =>
				sql`
					INSERT INTO session_state_migrations (name, applied_at)
					VALUES (${LEGACY_MIGRATION_NAME}, ${new Date().toISOString()})
					ON CONFLICT(name)
					DO UPDATE SET applied_at = ${new Date().toISOString()}
				`.pipe(Effect.asVoid),
			)

		const replaceSessionsForProject = (
			projectPath: string,
			sessions: HashMap.HashMap<string, PersistedSession>,
		): Effect.Effect<void, SessionStateStoreError> =>
			withSql(projectPath, (sql, normalizedProjectPath) =>
				sql.withTransaction(
					Effect.gen(function* () {
						yield* sql`
							DELETE FROM session_state
							WHERE project_path = ${normalizedProjectPath}
						`

						for (const [issueId, session] of HashMap.entries(sessions)) {
							const sessionProjectPath = normalizeProjectPath(session.projectPath)
							if (sessionProjectPath !== normalizedProjectPath) {
								continue
							}
							yield* sql`
								INSERT INTO session_state (
									project_path,
									issue_id,
									worktree_path,
									tmux_session_name,
									state,
									started_at
								)
								VALUES (
									${normalizedProjectPath},
									${issueId},
									${session.worktreePath},
									${session.tmuxSessionName},
									${session.state},
									${DateTime.formatIso(session.startedAt)}
								)
							`
						}
					}),
				),
			).pipe(Effect.asVoid)

		const loadLegacySessions = (
			projectPath: string,
		): Effect.Effect<LegacyLoadResult, SessionStateStoreError> =>
			Effect.gen(function* () {
				const normalizedProjectPath = normalizeProjectPath(projectPath)
				const storagePaths = getProjectStoragePaths(normalizedProjectPath, pathService)
				const filePath = pathService.join(storagePaths.storageDirectory, LEGACY_SESSION_FILENAME)

				const exists = yield* fs
					.exists(filePath)
					.pipe(
						Effect.mapError((cause) => mapSqlError("Failed to check legacy session file", cause)),
					)
				if (!exists) {
					return {
						sessions: HashMap.empty<string, PersistedSession>(),
						filePath: null,
					}
				}

				const content = yield* fs
					.readFileString(filePath)
					.pipe(
						Effect.mapError((cause) => mapSqlError("Failed to read legacy session file", cause)),
					)

				const decoded = yield* decodeLegacySessions(content).pipe(
					Effect.mapError(
						(cause) =>
							new SessionStateStoreError({
								message: "Failed to decode legacy sessions.json",
								cause,
							}),
					),
				)

				let sessions = HashMap.empty<string, PersistedSession>()
				for (const [issueId, session] of HashMap.entries(decoded)) {
					const sessionProjectPath = normalizeProjectPath(
						session.projectPath ?? normalizedProjectPath,
					)
					if (sessionProjectPath !== normalizedProjectPath) {
						continue
					}
					sessions = HashMap.set(sessions, issueId, {
						issueId: session.issueId,
						worktreePath: session.worktreePath,
						tmuxSessionName: session.tmuxSessionName,
						state: session.state,
						startedAt: session.startedAt,
						projectPath: sessionProjectPath,
					})
				}

				return { sessions, filePath }
			})

		const removeLegacySessionFile = (
			filePath: string,
		): Effect.Effect<void, SessionStateStoreError> =>
			Effect.gen(function* () {
				yield* fs
					.remove(filePath, { force: true })
					.pipe(
						Effect.mapError((cause) =>
							mapSqlError("Failed to remove migrated legacy session file", cause),
						),
					)
			})

		return {
			load: (projectPath: string) =>
				Effect.gen(function* () {
					const migrationApplied = yield* isLegacyMigrationApplied(projectPath)
					if (!migrationApplied) {
						const legacyLoadResult = yield* loadLegacySessions(projectPath)
						if (HashMap.size(legacyLoadResult.sessions) > 0) {
							yield* replaceSessionsForProject(projectPath, legacyLoadResult.sessions)
						}
						yield* markLegacyMigrationApplied(projectPath)
						if (legacyLoadResult.filePath !== null) {
							yield* removeLegacySessionFile(legacyLoadResult.filePath).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(error).pipe(Effect.zipRight(Effect.void)),
								),
							)
						}
					}
					return yield* loadFromSqlite(projectPath)
				}),

			save: (
				projectPath: string,
				sessions: HashMap.HashMap<string, PersistedSession>,
			): Effect.Effect<void, SessionStateStoreError> =>
				replaceSessionsForProject(projectPath, sessions),
		} satisfies SessionStateStoreService
	}),
}) {}
