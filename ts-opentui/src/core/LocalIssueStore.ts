import { Reactivity } from "@effect/experimental"
import { FileSystem, Path } from "@effect/platform"
import type * as SqlClient from "@effect/sql/SqlClient"
import type { SqlError } from "@effect/sql/SqlError"
import { SqliteClient } from "@effect/sql-sqlite-bun"
import { Data, Effect, Schema, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import type { ResolvedConfig } from "../config/defaults.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { ProjectService } from "../services/ProjectService.js"
import type {
	DependencyRef,
	DependencyType,
	Issue,
	IssueListFilters,
	IssueListOptions,
	IssueListSortField,
	IssueStatus,
	IssueType,
} from "./BeadsClient.js"

const LabelsJsonSchema = Schema.parseJson(Schema.Array(Schema.String))

export type SyncTarget = "linear"
export type SyncOperation = "upsert" | "close" | "delete"

interface IssueRow {
	readonly id: string
	readonly title: string
	readonly description: string | null
	readonly status: string
	readonly priority: number
	readonly issue_type: string
	readonly created_at: string
	readonly updated_at: string
	readonly closed_at: string | null
	readonly assignee: string | null
	readonly labels_json: string | null
	readonly design: string | null
	readonly notes: string | null
	readonly acceptance: string | null
	readonly estimate: number | null
	readonly deleted_at: string | null
}

interface DependencyLinkRow {
	readonly issue_id: string
	readonly depends_on_id: string
	readonly dependency_type: string
	readonly tombstoned_at: string | null
}

interface SyncQueueRow {
	readonly id: number
	readonly issue_id: string
	readonly operation: string
	readonly target: string
	readonly attempts: number
	readonly payload_json: string | null
	readonly attempt_token: string | null
}

interface ExternalRefRow {
	readonly issue_id: string
	readonly target: string
	readonly external_id: string
	readonly external_key: string | null
	readonly last_synced_at: string | null
}

interface MetaRow {
	readonly value: string
}

interface IssueAttachmentRow {
	readonly issue_id: string
	readonly attachment_id: string
	readonly filename: string
	readonly original_path: string
	readonly mime_type: string
	readonly size_bytes: number
	readonly created_at: string
	readonly content_blob: Uint8Array | ArrayBuffer
}

interface TableInfoRow {
	readonly name: string
}

export interface PendingSyncItem {
	readonly id: number
	readonly issueId: string
	readonly operation: SyncOperation
	readonly target: SyncTarget
	readonly attempts: number
	readonly payloadJson: string | null
	readonly attemptToken: string
}

export interface SyncQueueClaim {
	readonly id: number
	readonly attemptToken: string
}

export interface MarkSyncSucceededParams {
	readonly issueId: string
	readonly target: SyncTarget
	readonly maxQueueId: number
	readonly claims: readonly SyncQueueClaim[]
}

export interface MarkSyncRetriableParams {
	readonly claims: readonly SyncQueueClaim[]
	readonly errorMessage: string
	readonly delaySeconds: number
	readonly nextAttempts: number
}

export interface MarkSyncTerminalFailureParams {
	readonly claims: readonly SyncQueueClaim[]
	readonly errorMessage: string
	readonly nextAttempts: number
}

export interface ExternalIssueSnapshot {
	readonly localId: string
	readonly externalId: string
	readonly externalKey?: string
	readonly title: string
	readonly description?: string
	readonly status: IssueStatus
	readonly priority: number
	readonly issueType: IssueType
	readonly createdAt: string
	readonly updatedAt: string
	readonly closedAt?: string | null
	readonly assignee?: string | null
	readonly labels: readonly string[]
	readonly notes?: string
	readonly design?: string
	readonly acceptance?: string
	readonly estimate?: number
	readonly parentLocalId?: string
}

export interface IssueAttachmentMetadata {
	readonly id: string
	readonly filename: string
	readonly originalPath: string
	readonly mimeType: string
	readonly size: number
	readonly createdAt: string
}

export interface IssueAttachmentRecord extends IssueAttachmentMetadata {
	readonly content: Uint8Array
}

export class LocalIssueStoreError extends Data.TaggedError("LocalIssueStoreError")<{
	readonly message: string
	readonly cause?: unknown
}> {}

const DEFAULT_PAGE_SIZE = 200
const SYNC_QUEUE_LEASE_SECONDS = 120
const LOCAL_ISSUE_DB_DIRECTORY = ".azedarach"
const LOCAL_ISSUE_DB_FILENAME = "issues.db"
const LOCAL_ISSUE_BACKUP_META_LAST_SUCCESS_AT = "backup:last_success_at"
const LOCAL_ISSUE_ID_NEXT_ALPHA_INDEX_META = "issue:id_next_alpha_index"
const LOCAL_ISSUE_BACKUP_FILE_PATTERN = /^issues-(\d{8}T\d{6}Z)\.db$/

const DEFAULT_LOCAL_ISSUE_BACKUP_CONFIG: LocalIssueBackupConfig = {
	enabled: true,
	intervalMinutes: 60,
	writeCooldownSeconds: 300,
	maxBackups: 30,
	directory: ".azedarach/backups",
}

interface LocalIssueBackupConfig {
	readonly enabled: boolean
	readonly intervalMinutes: number
	readonly writeCooldownSeconds: number
	readonly maxBackups: number
	readonly directory: string
}

interface LocalIssueBackupPaths {
	readonly backupDir: string
	readonly backupPath: string
	readonly tempPath: string
}

interface NamedBackupFile {
	readonly name: string
	readonly timestampMs: number
}

interface ResolveLocalIssueStorageRootInput {
	readonly explicitProjectPath?: string
	readonly currentProjectPath?: string
	readonly fallbackCwd: string
}

export const resolveLocalIssueStorageRoot = ({
	explicitProjectPath,
	currentProjectPath,
	fallbackCwd,
}: ResolveLocalIssueStorageRootInput): string =>
	explicitProjectPath ?? currentProjectPath ?? fallbackCwd

interface LocalIssueStorageResolution {
	readonly storageRoot: string
	readonly explicitProjectPath?: string
	readonly currentProjectPath?: string
	readonly fallbackCwd: string
}

const schemaStatements: readonly string[] = [
	`CREATE TABLE IF NOT EXISTS issues (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		description TEXT,
		status TEXT NOT NULL,
		priority INTEGER NOT NULL,
		issue_type TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		closed_at TEXT,
		assignee TEXT,
		labels_json TEXT,
		design TEXT,
		notes TEXT,
		acceptance TEXT,
		estimate INTEGER,
		deleted_at TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS issue_dependencies (
		issue_id TEXT NOT NULL,
		depends_on_id TEXT NOT NULL,
		dependency_type TEXT NOT NULL,
		tombstoned_at TEXT,
		PRIMARY KEY (issue_id, depends_on_id, dependency_type)
	)`,
	`CREATE TABLE IF NOT EXISTS issue_attachments (
		issue_id TEXT NOT NULL,
		attachment_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		original_path TEXT NOT NULL,
		mime_type TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		content_blob BLOB NOT NULL,
		PRIMARY KEY (issue_id, attachment_id)
	)`,
	`CREATE TABLE IF NOT EXISTS issue_external_refs (
		issue_id TEXT NOT NULL,
		target TEXT NOT NULL,
		external_id TEXT NOT NULL,
		external_key TEXT,
		last_synced_at TEXT,
		PRIMARY KEY (issue_id, target)
	)`,
	`CREATE TABLE IF NOT EXISTS sync_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		issue_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		target TEXT NOT NULL,
		payload_json TEXT,
		status TEXT NOT NULL,
		attempts INTEGER NOT NULL,
		attempt_token TEXT,
		lease_expires_at TEXT,
		next_attempt_at TEXT,
		error TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_queue_pending ON sync_queue(target, status, next_attempt_at, id)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_queue_claimable ON sync_queue(target, status, next_attempt_at, lease_expires_at, id)`,
	`CREATE INDEX IF NOT EXISTS idx_dependencies_depends_on ON issue_dependencies(depends_on_id, dependency_type, tombstoned_at)`,
	`CREATE INDEX IF NOT EXISTS idx_issue_attachments_issue ON issue_attachments(issue_id, created_at, attachment_id)`,
]

const nowIso = (): string => new Date().toISOString()

const normalizePositiveInteger = (value: number | undefined, fallback: number): number => {
	if (value === undefined || !Number.isFinite(value)) {
		return fallback
	}
	const rounded = Math.floor(value)
	return rounded > 0 ? rounded : fallback
}

const parseBackupTimestampToken = (token: string): number | undefined => {
	if (token.length !== 16) {
		return undefined
	}
	const year = Number(token.slice(0, 4))
	const month = Number(token.slice(4, 6))
	const day = Number(token.slice(6, 8))
	const hour = Number(token.slice(9, 11))
	const minute = Number(token.slice(11, 13))
	const second = Number(token.slice(13, 15))
	if (
		!Number.isFinite(year) ||
		!Number.isFinite(month) ||
		!Number.isFinite(day) ||
		!Number.isFinite(hour) ||
		!Number.isFinite(minute) ||
		!Number.isFinite(second)
	) {
		return undefined
	}
	const timestampMs = Date.UTC(year, month - 1, day, hour, minute, second)
	return Number.isNaN(timestampMs) ? undefined : timestampMs
}

const formatBackupTimestampToken = (date: Date): string => {
	const year = String(date.getUTCFullYear()).padStart(4, "0")
	const month = String(date.getUTCMonth() + 1).padStart(2, "0")
	const day = String(date.getUTCDate()).padStart(2, "0")
	const hour = String(date.getUTCHours()).padStart(2, "0")
	const minute = String(date.getUTCMinutes()).padStart(2, "0")
	const second = String(date.getUTCSeconds()).padStart(2, "0")
	return `${year}${month}${day}T${hour}${minute}${second}Z`
}

export const parseBackupTimestampFromFilename = (filename: string): number | undefined => {
	const match = LOCAL_ISSUE_BACKUP_FILE_PATTERN.exec(filename)
	if (match === null) {
		return undefined
	}
	return parseBackupTimestampToken(match[1] ?? "")
}

const toNamedBackupFiles = (filenames: readonly string[]): readonly NamedBackupFile[] =>
	filenames.flatMap((name) => {
		const timestampMs = parseBackupTimestampFromFilename(name)
		if (timestampMs === undefined) {
			return []
		}
		return [
			{
				name,
				timestampMs,
			},
		]
	})

export const selectBackupFilesToPrune = (
	filenames: readonly string[],
	maxBackups: number,
): readonly string[] => {
	const keepCount = normalizePositiveInteger(
		maxBackups,
		DEFAULT_LOCAL_ISSUE_BACKUP_CONFIG.maxBackups,
	)
	const sorted = [...toNamedBackupFiles(filenames)].sort((left, right) => {
		if (left.timestampMs !== right.timestampMs) {
			return right.timestampMs - left.timestampMs
		}
		return right.name.localeCompare(left.name)
	})
	if (sorted.length <= keepCount) {
		return []
	}
	return sorted.slice(keepCount).map((entry) => entry.name)
}

export const encodeAlphaIssueIndex = (index: number): string => {
	if (!Number.isInteger(index) || index < 0) {
		throw new Error(`Issue index must be a non-negative integer: ${index}`)
	}

	let remaining = index
	let encoded = ""

	while (remaining >= 0) {
		const digit = remaining % 26
		encoded = String.fromCharCode(97 + digit) + encoded
		remaining = Math.floor(remaining / 26) - 1
	}

	return encoded
}

const parseNextAlphaIssueIndex = (rawValue: string | undefined): number => {
	if (rawValue === undefined) {
		return 0
	}

	const parsed = Number.parseInt(rawValue, 10)
	return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0
}

export const allocateNextAlphaIssueId = (
	startIndex: number,
	existingIds: ReadonlySet<string>,
): { readonly issueId: string; readonly nextIndex: number } => {
	let candidateIndex = startIndex

	while (true) {
		const candidateId = encodeAlphaIssueIndex(candidateIndex)
		if (!existingIds.has(candidateId)) {
			return {
				issueId: candidateId,
				nextIndex: candidateIndex + 1,
			}
		}
		candidateIndex += 1
	}
}

const resolveLocalIssueBackupConfig = (config: ResolvedConfig): LocalIssueBackupConfig => {
	const fallback = DEFAULT_LOCAL_ISSUE_BACKUP_CONFIG
	if (!("local" in config.issueTracker)) {
		return {
			enabled: fallback.enabled,
			intervalMinutes: fallback.intervalMinutes,
			writeCooldownSeconds: fallback.writeCooldownSeconds,
			maxBackups: fallback.maxBackups,
			directory: fallback.directory,
		}
	}
	const configured = config.issueTracker.local.backups
	return {
		enabled: configured?.enabled ?? fallback.enabled,
		intervalMinutes: normalizePositiveInteger(
			configured?.intervalMinutes,
			fallback.intervalMinutes,
		),
		writeCooldownSeconds: normalizePositiveInteger(
			configured?.writeCooldownSeconds,
			fallback.writeCooldownSeconds,
		),
		maxBackups: normalizePositiveInteger(configured?.maxBackups, fallback.maxBackups),
		directory:
			configured?.directory && configured.directory.trim().length > 0
				? configured.directory
				: fallback.directory,
	}
}

const normalizeIssueStatus = (status: string | undefined): IssueStatus => {
	switch (status) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return status
		default:
			return "open"
	}
}

const normalizeIssueType = (issueType: string | undefined): IssueType => {
	switch (issueType) {
		case "bug":
		case "feature":
		case "task":
		case "epic":
		case "chore":
			return issueType
		default:
			return "task"
	}
}

const normalizeDependencyType = (dependencyType: string | undefined): DependencyType => {
	switch (dependencyType) {
		case "blocks":
		case "related":
		case "parent-child":
		case "discovered-from":
			return dependencyType
		default:
			return "related"
	}
}

const toTimestampMs = (value: string): number => {
	const parsed = Date.parse(value)
	return Number.isNaN(parsed) ? 0 : parsed
}

const sortIssuesInMemory = (
	issues: readonly Issue[],
	sortBy: IssueListSortField,
	sortDirection: "asc" | "desc",
): Issue[] => {
	const sorted = [...issues]
	sorted.sort((left, right) => {
		switch (sortBy) {
			case "updated_at":
				return toTimestampMs(left.updated_at) - toTimestampMs(right.updated_at)
			case "created_at":
				return toTimestampMs(left.created_at) - toTimestampMs(right.created_at)
			case "priority":
				return left.priority - right.priority
			case "title":
				return left.title.localeCompare(right.title)
			default:
				return 0
		}
	})
	return sortDirection === "desc" ? sorted.reverse() : sorted
}

const decodeLabels = (value: string | null): readonly string[] => {
	if (value === null) {
		return []
	}
	return Schema.decodeSync(LabelsJsonSchema)(value)
}

const encodeLabels = (labels: readonly string[] | undefined): string =>
	Schema.encodeSync(LabelsJsonSchema)(labels === undefined ? [] : [...labels])

const toUint8Array = (value: Uint8Array | ArrayBuffer): Uint8Array =>
	value instanceof Uint8Array ? value : new Uint8Array(value)

const rowToIssueAttachmentMetadata = (row: IssueAttachmentRow): IssueAttachmentMetadata => ({
	id: row.attachment_id,
	filename: row.filename,
	originalPath: row.original_path,
	mimeType: row.mime_type,
	size: row.size_bytes,
	createdAt: row.created_at,
})

const rowToIssueAttachmentRecord = (row: IssueAttachmentRow): IssueAttachmentRecord => ({
	...rowToIssueAttachmentMetadata(row),
	content: toUint8Array(row.content_blob),
})

const buildDependencyView = (
	issueId: string,
	links: readonly DependencyLinkRow[],
	issueById: ReadonlyMap<string, IssueRow>,
): {
	readonly dependencies: readonly DependencyRef[]
	readonly dependents: readonly DependencyRef[]
} => {
	const dependencies: DependencyRef[] = []
	const dependents: DependencyRef[] = []

	for (const link of links) {
		if (link.tombstoned_at !== null) continue

		if (link.issue_id === issueId) {
			const related = issueById.get(link.depends_on_id)
			dependencies.push({
				id: link.depends_on_id,
				title: related?.title,
				status: normalizeIssueStatus(related?.status),
				dependency_type: normalizeDependencyType(link.dependency_type),
				issue_type: normalizeIssueType(related?.issue_type),
			})
		}

		if (link.depends_on_id === issueId) {
			const related = issueById.get(link.issue_id)
			dependents.push({
				id: link.issue_id,
				title: related?.title,
				status: normalizeIssueStatus(related?.status),
				dependency_type: normalizeDependencyType(link.dependency_type),
				issue_type: normalizeIssueType(related?.issue_type),
			})
		}
	}

	return { dependencies, dependents }
}

const rowToIssue = (
	row: IssueRow,
	links: readonly DependencyLinkRow[],
	issueById: ReadonlyMap<string, IssueRow>,
): Issue => {
	const deps = buildDependencyView(row.id, links, issueById)
	const labels = decodeLabels(row.labels_json)

	return {
		id: row.id,
		title: row.title,
		description: row.description ?? undefined,
		status: normalizeIssueStatus(row.status),
		priority: row.priority,
		issue_type: normalizeIssueType(row.issue_type),
		created_at: row.created_at,
		updated_at: row.updated_at,
		closed_at: row.closed_at,
		assignee: row.assignee,
		labels,
		design: row.design ?? undefined,
		notes: row.notes ?? undefined,
		acceptance: row.acceptance ?? undefined,
		estimate: row.estimate ?? undefined,
		dependency_count: deps.dependencies.length,
		dependent_count: deps.dependents.length,
		dependencies: deps.dependencies,
		dependents: deps.dependents,
	}
}

const isValidSyncOperation = (operation: string): operation is SyncOperation =>
	operation === "upsert" || operation === "close" || operation === "delete"

const isValidSyncTarget = (target: string): target is SyncTarget => target === "linear"

export class LocalIssueStore extends Effect.Service<LocalIssueStore>()("LocalIssueStore", {
	dependencies: [ProjectService.Default, AppConfig.Default, DiagnosticsService.Default],
	effect: Effect.gen(function* () {
		const pathService = yield* Path.Path
		const fs = yield* FileSystem.FileSystem
		const projectService = yield* ProjectService
		const appConfig = yield* AppConfig
		const diagnostics = yield* DiagnosticsService
		const backupWarningAtByDbPath = new Map<string, number>()

		const resolveStorageRoot = (cwd?: string): Effect.Effect<LocalIssueStorageResolution> =>
			projectService.getCurrentPath().pipe(
				Effect.map((projectPath) => {
					const fallbackCwd = process.cwd()
					const currentProjectPath = projectPath ?? undefined
					const storageRoot = resolveLocalIssueStorageRoot({
						explicitProjectPath: cwd,
						currentProjectPath,
						fallbackCwd,
					})
					return {
						storageRoot,
						explicitProjectPath: cwd,
						currentProjectPath,
						fallbackCwd,
					}
				}),
			)

		const ensureSyncQueueColumns = (sql: SqlClient.SqlClient): Effect.Effect<void, SqlError> =>
			Effect.gen(function* () {
				const columns = yield* sql<TableInfoRow>`PRAGMA table_info(sync_queue)`
				const columnNames = new Set(columns.map((column) => column.name))

				if (!columnNames.has("attempt_token")) {
					yield* sql`ALTER TABLE sync_queue ADD COLUMN attempt_token TEXT`
				}
				if (!columnNames.has("lease_expires_at")) {
					yield* sql`ALTER TABLE sync_queue ADD COLUMN lease_expires_at TEXT`
				}
			})

		const getBackupConfig = (): Effect.Effect<LocalIssueBackupConfig> =>
			SubscriptionRef.get(appConfig.config).pipe(Effect.map(resolveLocalIssueBackupConfig))

		const getMetaValue = (
			sql: SqlClient.SqlClient,
			key: string,
		): Effect.Effect<string | undefined, SqlError> =>
			sql<MetaRow>`
				SELECT value
				FROM meta
				WHERE key = ${key}
				LIMIT 1
			`.pipe(Effect.map((rows) => rows[0]?.value))

		const setMetaValue = (
			sql: SqlClient.SqlClient,
			key: string,
			value: string,
		): Effect.Effect<void, SqlError> =>
			sql`
				INSERT INTO meta (key, value)
				VALUES (${key}, ${value})
				ON CONFLICT(key)
				DO UPDATE SET value = ${value}
			`.pipe(Effect.asVoid)

		const getLastBackupTimestampMs = (
			sql: SqlClient.SqlClient,
		): Effect.Effect<number | undefined, SqlError> =>
			getMetaValue(sql, LOCAL_ISSUE_BACKUP_META_LAST_SUCCESS_AT).pipe(
				Effect.map((value) => {
					if (value === undefined) {
						return undefined
					}
					const parsed = Date.parse(value)
					return Number.isNaN(parsed) ? undefined : parsed
				}),
			)

		const resolveBackupDirectory = (
			storageRoot: string,
			backupConfig: LocalIssueBackupConfig,
		): string =>
			pathService.isAbsolute(backupConfig.directory)
				? backupConfig.directory
				: pathService.join(storageRoot, backupConfig.directory)

		const reserveBackupPaths = (
			backupDir: string,
		): Effect.Effect<LocalIssueBackupPaths, LocalIssueStoreError> =>
			Effect.gen(function* () {
				for (let offsetSeconds = 0; offsetSeconds < 5; offsetSeconds += 1) {
					const candidateDate = new Date(Date.now() + offsetSeconds * 1000)
					const token = formatBackupTimestampToken(candidateDate)
					const backupPath = pathService.join(backupDir, `issues-${token}.db`)
					const exists = yield* fs
						.exists(backupPath)
						.pipe(Effect.catchAll(() => Effect.succeed(false)))
					if (!exists) {
						return {
							backupDir,
							backupPath,
							tempPath: `${backupPath}.tmp-${crypto.randomUUID()}`,
						}
					}
				}
				return yield* Effect.fail(
					new LocalIssueStoreError({
						message: "Failed to reserve backup path after multiple attempts",
					}),
				)
			})

		const pruneBackups = (
			backupDir: string,
			maxBackups: number,
		): Effect.Effect<void, LocalIssueStoreError> =>
			Effect.gen(function* () {
				const entries = yield* fs.readDirectory(backupDir).pipe(
					Effect.catchAll((cause) =>
						Effect.fail(
							new LocalIssueStoreError({
								message: `Failed to read backup directory: ${String(cause)}`,
								cause,
							}),
						),
					),
				)
				const toRemove = selectBackupFilesToPrune(entries, maxBackups)
				for (const filename of toRemove) {
					const targetPath = pathService.join(backupDir, filename)
					yield* fs.remove(targetPath, { force: true }).pipe(
						Effect.catchAll((cause) =>
							Effect.fail(
								new LocalIssueStoreError({
									message: `Failed to prune backup '${filename}': ${String(cause)}`,
									cause,
								}),
							),
						),
					)
				}
			})

		const reportBackupFailure = (
			dbPath: string,
			backupConfig: LocalIssueBackupConfig,
			message: string,
			cause: unknown,
		): Effect.Effect<void> =>
			Effect.gen(function* () {
				const nowMs = Date.now()
				const cooldownMs = backupConfig.writeCooldownSeconds * 1000
				const lastWarningAtMs = backupWarningAtByDbPath.get(dbPath)
				if (lastWarningAtMs !== undefined && nowMs - lastWarningAtMs < cooldownMs) {
					return
				}
				backupWarningAtByDbPath.set(dbPath, nowMs)
				yield* Effect.logWarning(`${message}: ${String(cause)}`)
				yield* diagnostics.logEvent({
					severity: "warning",
					source: "LocalIssueStore",
					message,
					details: String(cause),
				})
			})

		const runBackup = (
			sql: SqlClient.SqlClient,
			storageRoot: string,
			backupConfig: LocalIssueBackupConfig,
		): Effect.Effect<void, LocalIssueStoreError | SqlError> =>
			Effect.gen(function* () {
				const backupDir = resolveBackupDirectory(storageRoot, backupConfig)
				yield* fs.makeDirectory(backupDir, { recursive: true }).pipe(
					Effect.mapError(
						(cause) =>
							new LocalIssueStoreError({
								message: `Failed to create backup directory: ${String(cause)}`,
								cause,
							}),
					),
				)

				const paths = yield* reserveBackupPaths(backupDir)
				const cleanupTemp = fs.remove(paths.tempPath, { force: true }).pipe(Effect.ignore)

				yield* sql`VACUUM INTO ${paths.tempPath}`.pipe(
					Effect.catchAll((cause) => cleanupTemp.pipe(Effect.zipRight(Effect.fail(cause)))),
				)

				yield* fs.rename(paths.tempPath, paths.backupPath).pipe(
					Effect.catchAll((cause) =>
						cleanupTemp.pipe(
							Effect.zipRight(
								Effect.fail(
									new LocalIssueStoreError({
										message: `Failed to finalize sqlite backup: ${String(cause)}`,
										cause,
									}),
								),
							),
						),
					),
				)

				yield* setMetaValue(sql, LOCAL_ISSUE_BACKUP_META_LAST_SUCCESS_AT, nowIso())
				yield* pruneBackups(paths.backupDir, backupConfig.maxBackups)
			})

		const maybeRunStaleOpenBackup = (
			sql: SqlClient.SqlClient,
			dbPath: string,
			storageRoot: string,
			backupConfig: LocalIssueBackupConfig,
		): Effect.Effect<void> =>
			Effect.gen(function* () {
				if (!backupConfig.enabled) {
					return
				}
				const lastBackupAtMs = yield* getLastBackupTimestampMs(sql).pipe(
					Effect.catchAll((cause) => {
						return reportBackupFailure(
							dbPath,
							backupConfig,
							"Failed to read sqlite backup metadata",
							cause,
						).pipe(Effect.as(undefined))
					}),
				)
				const staleIntervalMs = backupConfig.intervalMinutes * 60 * 1000
				const shouldBackup =
					lastBackupAtMs === undefined || Date.now() - lastBackupAtMs >= staleIntervalMs
				if (!shouldBackup) {
					return
				}
				yield* runBackup(sql, storageRoot, backupConfig).pipe(
					Effect.catchAll((cause) =>
						reportBackupFailure(dbPath, backupConfig, "Automatic sqlite backup failed", cause),
					),
				)
			})

		const maybeRunWriteCooldownBackup = (
			sql: SqlClient.SqlClient,
			dbPath: string,
			storageRoot: string,
			backupConfig: LocalIssueBackupConfig,
		): Effect.Effect<void> =>
			Effect.gen(function* () {
				if (!backupConfig.enabled) {
					return
				}
				const lastBackupAtMs = yield* getLastBackupTimestampMs(sql).pipe(
					Effect.catchAll((cause) => {
						return reportBackupFailure(
							dbPath,
							backupConfig,
							"Failed to read sqlite backup metadata",
							cause,
						).pipe(Effect.as(undefined))
					}),
				)
				const cooldownMs = backupConfig.writeCooldownSeconds * 1000
				const shouldBackup =
					lastBackupAtMs === undefined || Date.now() - lastBackupAtMs >= cooldownMs
				if (!shouldBackup) {
					return
				}
				yield* runBackup(sql, storageRoot, backupConfig).pipe(
					Effect.catchAll((cause) =>
						reportBackupFailure(
							dbPath,
							backupConfig,
							"Write-triggered sqlite backup failed",
							cause,
						),
					),
				)
			})

		const withSql = <A>(
			cwd: string | undefined,
			effect: (sql: SqlClient.SqlClient) => Effect.Effect<A, SqlError | LocalIssueStoreError>,
			options?: {
				readonly triggerWriteBackup?: boolean
			},
		): Effect.Effect<A, LocalIssueStoreError> =>
			Effect.gen(function* () {
				const storageResolution = yield* resolveStorageRoot(cwd)
				const storageRoot = storageResolution.storageRoot
				const dbDir = pathService.join(storageRoot, LOCAL_ISSUE_DB_DIRECTORY)
				const dbPath = pathService.join(dbDir, LOCAL_ISSUE_DB_FILENAME)
				const backupConfig = yield* getBackupConfig()
				yield* Effect.log(
					`LocalIssueStore.withSql: dbPath=${dbPath} explicitCwd=${storageResolution.explicitProjectPath ?? "<none>"} currentProjectPath=${storageResolution.currentProjectPath ?? "<none>"} fallbackCwd=${storageResolution.fallbackCwd}`,
				)

				yield* fs.makeDirectory(dbDir, { recursive: true }).pipe(
					Effect.catchAll((cause) =>
						Effect.fail(
							new LocalIssueStoreError({
								message: `Failed to create sqlite directory: ${String(cause)}`,
								cause,
							}),
						),
					),
				)

				return yield* Effect.scoped(
					Effect.gen(function* () {
						const sql = yield* SqliteClient.make({ filename: dbPath }).pipe(
							Effect.provide(Reactivity.layer),
						)

						for (const statement of schemaStatements) {
							yield* sql.unsafe(statement)
						}
						yield* ensureSyncQueueColumns(sql)
						yield* maybeRunStaleOpenBackup(sql, dbPath, storageRoot, backupConfig)

						const result = yield* effect(sql)
						if (options?.triggerWriteBackup === true) {
							yield* maybeRunWriteCooldownBackup(sql, dbPath, storageRoot, backupConfig)
						}
						return result
					}),
				).pipe(
					Effect.catchAll((cause) =>
						Effect.fail(
							new LocalIssueStoreError({
								message: `SQLite operation failed: ${String(cause)}`,
								cause,
							}),
						),
					),
				)
			})

		const withSqlMutation = <A>(
			cwd: string | undefined,
			effect: (sql: SqlClient.SqlClient) => Effect.Effect<A, SqlError | LocalIssueStoreError>,
		): Effect.Effect<A, LocalIssueStoreError> => withSql(cwd, effect, { triggerWriteBackup: true })

		const listIssueRows = (
			sql: SqlClient.SqlClient,
		): Effect.Effect<readonly IssueRow[], SqlError> =>
			sql<IssueRow>`
				SELECT
					id,
					title,
					description,
					status,
					priority,
					issue_type,
					created_at,
					updated_at,
					closed_at,
					assignee,
					labels_json,
					design,
					notes,
					acceptance,
					estimate,
					deleted_at
				FROM issues
				WHERE deleted_at IS NULL
			`

		const listDependencyRows = (
			sql: SqlClient.SqlClient,
		): Effect.Effect<readonly DependencyLinkRow[], SqlError> =>
			sql<DependencyLinkRow>`
				SELECT issue_id, depends_on_id, dependency_type, tombstoned_at
				FROM issue_dependencies
				WHERE tombstoned_at IS NULL
			`

		const buildIssues = (
			issueRows: readonly IssueRow[],
			dependencyRows: readonly DependencyLinkRow[],
		): readonly Issue[] => {
			const issueById = new Map(issueRows.map((row) => [row.id, row]))
			return issueRows.map((row) => rowToIssue(row, dependencyRows, issueById))
		}

		const loadAllIssues = (sql: SqlClient.SqlClient): Effect.Effect<readonly Issue[], SqlError> =>
			Effect.all([listIssueRows(sql), listDependencyRows(sql)]).pipe(
				Effect.map(([rows, links]) => buildIssues(rows, links)),
			)

		const buildDefaultSyncPayloadJson = (
			issueId: string,
			operation: SyncOperation,
			target: SyncTarget,
		): string | null => {
			if (operation !== "upsert") {
				return null
			}
			return JSON.stringify({
				idempotencyKey: `${target}:upsert:${issueId}`,
			})
		}

		const enqueueSync = (
			sql: SqlClient.SqlClient,
			issueId: string,
			operation: SyncOperation,
			target: SyncTarget,
			payloadJson?: string,
		): Effect.Effect<void, SqlError> =>
			Effect.gen(function* () {
				const now = nowIso()
				const normalizedPayload =
					payloadJson ?? buildDefaultSyncPayloadJson(issueId, operation, target)
				yield* sql`
					INSERT INTO sync_queue (
						issue_id,
						operation,
						target,
						payload_json,
						status,
						attempts,
						attempt_token,
						lease_expires_at,
						next_attempt_at,
						error,
						created_at,
						updated_at
					)
					VALUES (
						${issueId},
						${operation},
						${target},
						${normalizedPayload},
						${"pending"},
						${0},
						${null},
						${null},
						${null},
						${null},
						${now},
						${now}
					)
				`
			})

		const loadIssueById = (
			sql: SqlClient.SqlClient,
			id: string,
		): Effect.Effect<Issue | undefined, SqlError> =>
			Effect.gen(function* () {
				const issues = yield* loadAllIssues(sql)
				return issues.find((issue) => issue.id === id)
			})

		return {
			list: (
				filters?: IssueListFilters,
				cwd?: string,
				options?: IssueListOptions,
			): Effect.Effect<readonly Issue[], LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					loadAllIssues(sql).pipe(
						Effect.map((issues) => {
							const includeClosed = options?.includeClosed ?? true
							const pageSize = options?.pageSize ?? DEFAULT_PAGE_SIZE
							const sortBy = options?.sortBy ?? "updated_at"
							const sortDirection = options?.sortDirection ?? "desc"

							const filtered = issues.filter((issue) => {
								if (!includeClosed && issue.status === "closed") return false
								if (filters?.status && issue.status !== filters.status) return false
								if (filters?.priority !== undefined && issue.priority !== filters.priority)
									return false
								if (filters?.type && issue.issue_type !== filters.type) return false
								return true
							})

							const sorted = sortIssuesInMemory(filtered, sortBy, sortDirection)
							const limitedByPage = sorted.slice(0, Math.max(0, pageSize))
							return options?.limit === undefined
								? limitedByPage
								: limitedByPage.slice(0, Math.max(0, options.limit))
						}),
					),
				),

			show: (id: string, cwd?: string): Effect.Effect<Issue | undefined, LocalIssueStoreError> =>
				withSql(cwd, (sql) => loadIssueById(sql, id)),

			showMultiple: (
				ids: readonly string[],
				cwd?: string,
			): Effect.Effect<readonly Issue[], LocalIssueStoreError> =>
				ids.length === 0
					? Effect.succeed([])
					: withSql(cwd, (sql) =>
							loadAllIssues(sql).pipe(
								Effect.map((issues) => {
									const idSet = new Set(ids)
									return issues.filter((issue) => idSet.has(issue.id))
								}),
							),
						),

			create: (
				params: {
					title: string
					type?: string
					priority?: number
					description?: string
					design?: string
					acceptance?: string
					assignee?: string
					estimate?: number
					labels?: readonly string[]
					parent?: string
				},
				syncTarget?: SyncTarget,
				cwd?: string,
			): Effect.Effect<Issue, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					Effect.gen(function* () {
						const now = nowIso()
						const nextAlphaIndex = yield* getMetaValue(
							sql,
							LOCAL_ISSUE_ID_NEXT_ALPHA_INDEX_META,
						).pipe(Effect.map(parseNextAlphaIssueIndex))
						let nextIssueAlphaIndex = nextAlphaIndex
						let issueId = encodeAlphaIssueIndex(nextIssueAlphaIndex)

						while (true) {
							const existing = yield* sql<{ readonly id: string }>`
								SELECT id FROM issues WHERE id = ${issueId}
							`
							if (existing.length === 0) {
								break
							}
							nextIssueAlphaIndex += 1
							issueId = encodeAlphaIssueIndex(nextIssueAlphaIndex)
						}

						const nextReservedAlphaIndex = nextIssueAlphaIndex + 1

						yield* sql.withTransaction(
							Effect.gen(function* () {
								yield* sql`
									INSERT INTO issues (
										id,
										title,
										description,
										status,
										priority,
										issue_type,
										created_at,
										updated_at,
										closed_at,
										assignee,
										labels_json,
										design,
										notes,
										acceptance,
										estimate,
										deleted_at
									)
									VALUES (
										${issueId},
										${params.title},
										${params.description ?? null},
										${"open"},
										${params.priority ?? 2},
										${normalizeIssueType(params.type)},
										${now},
										${now},
										${null},
										${params.assignee ?? null},
										${encodeLabels(params.labels)},
										${params.design ?? null},
										${null},
										${params.acceptance ?? null},
										${params.estimate ?? null},
										${null}
									)
								`
								if (params.parent !== undefined && params.parent.trim().length > 0) {
									yield* sql`
										INSERT INTO issue_dependencies (
											issue_id,
											depends_on_id,
											dependency_type,
											tombstoned_at
										)
										VALUES (${issueId}, ${params.parent}, ${"parent-child"}, ${null})
									`
								}
								if (syncTarget !== undefined) {
									yield* enqueueSync(sql, issueId, "upsert", syncTarget)
								}
								yield* setMetaValue(
									sql,
									LOCAL_ISSUE_ID_NEXT_ALPHA_INDEX_META,
									String(nextReservedAlphaIndex),
								)
							}),
						)

						const created = yield* loadIssueById(sql, issueId)
						if (created === undefined) {
							return yield* Effect.fail(
								new LocalIssueStoreError({
									message: `Failed to load created issue ${issueId}`,
								}),
							)
						}
						return created
					}),
				),

			update: (
				id: string,
				fields: {
					status?: string
					notes?: string
					priority?: number
					title?: string
					type?: string
					description?: string
					design?: string
					acceptance?: string
					assignee?: string
					estimate?: number
					labels?: readonly string[]
					parent?: string
				},
				syncTarget?: SyncTarget,
				cwd?: string,
			): Effect.Effect<boolean, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					Effect.gen(function* () {
						const existingRows = yield* sql<IssueRow>`
							SELECT
								id,
								title,
								description,
								status,
								priority,
								issue_type,
								created_at,
								updated_at,
								closed_at,
								assignee,
								labels_json,
								design,
								notes,
								acceptance,
								estimate,
								deleted_at
							FROM issues
							WHERE id = ${id} AND deleted_at IS NULL
						`
						const existing = existingRows[0]
						if (existing === undefined) {
							return false
						}

						const now = nowIso()
						const nextStatus = fields.status === undefined ? existing.status : fields.status
						const nextClosedAt =
							normalizeIssueStatus(nextStatus) === "closed"
								? (existing.closed_at ?? now)
								: fields.status === undefined
									? existing.closed_at
									: null

						yield* sql.withTransaction(
							Effect.gen(function* () {
								yield* sql`
									UPDATE issues
									SET
										title = ${fields.title ?? existing.title},
										description = ${fields.description ?? existing.description},
										status = ${normalizeIssueStatus(nextStatus)},
										priority = ${fields.priority ?? existing.priority},
										issue_type = ${normalizeIssueType(fields.type ?? existing.issue_type)},
										updated_at = ${now},
										closed_at = ${nextClosedAt},
										assignee = ${fields.assignee ?? existing.assignee},
										labels_json = ${
											fields.labels === undefined
												? (existing.labels_json ?? encodeLabels([]))
												: encodeLabels(fields.labels)
										},
										design = ${fields.design ?? existing.design},
										notes = ${fields.notes ?? existing.notes},
										acceptance = ${fields.acceptance ?? existing.acceptance},
										estimate = ${fields.estimate ?? existing.estimate}
									WHERE id = ${id}
								`

								if (fields.parent !== undefined) {
									yield* sql`
										DELETE FROM issue_dependencies
										WHERE issue_id = ${id} AND dependency_type = ${"parent-child"}
									`
									if (fields.parent.trim().length > 0) {
										yield* sql`
											INSERT INTO issue_dependencies (
												issue_id,
												depends_on_id,
												dependency_type,
												tombstoned_at
											)
											VALUES (${id}, ${fields.parent}, ${"parent-child"}, ${null})
										`
									}
								}

								if (syncTarget !== undefined) {
									yield* enqueueSync(sql, id, "upsert", syncTarget)
								}
							}),
						)
						return true
					}),
				),

			close: (
				id: string,
				syncTarget?: SyncTarget,
				cwd?: string,
			): Effect.Effect<boolean, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					Effect.gen(function* () {
						const now = nowIso()
						const existing = yield* sql<{ readonly id: string }>`
							SELECT id FROM issues WHERE id = ${id} AND deleted_at IS NULL
						`
						if (existing.length === 0) {
							return false
						}
						yield* sql.withTransaction(
							Effect.gen(function* () {
								yield* sql`
									UPDATE issues
									SET
										status = ${"closed"},
										closed_at = ${now},
										updated_at = ${now}
									WHERE id = ${id}
								`
								if (syncTarget !== undefined) {
									yield* enqueueSync(sql, id, "close", syncTarget)
								}
							}),
						)
						return true
					}),
				),

			delete: (
				id: string,
				syncTarget?: SyncTarget,
				cwd?: string,
			): Effect.Effect<boolean, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					Effect.gen(function* () {
						const existing = yield* sql<{ readonly id: string }>`
							SELECT id FROM issues WHERE id = ${id} AND deleted_at IS NULL
						`
						if (existing.length === 0) {
							return false
						}

						yield* sql.withTransaction(
							Effect.gen(function* () {
								yield* sql`DELETE FROM issue_dependencies WHERE issue_id = ${id} OR depends_on_id = ${id}`
								yield* sql`DELETE FROM issue_attachments WHERE issue_id = ${id}`
								yield* sql`DELETE FROM issues WHERE id = ${id}`
								if (syncTarget !== undefined) {
									yield* enqueueSync(sql, id, "delete", syncTarget)
								}
							}),
						)
						return true
					}),
				),

			ready: (cwd?: string): Effect.Effect<readonly Issue[], LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					loadAllIssues(sql).pipe(
						Effect.map((issues) =>
							issues.filter((issue) => issue.status === "open" || issue.status === "in_progress"),
						),
					),
				),

			search: (
				query: string,
				cwd?: string,
			): Effect.Effect<readonly Issue[], LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					loadAllIssues(sql).pipe(
						Effect.map((issues) => {
							const needle = query.trim().toLowerCase()
							if (needle.length === 0) return issues
							return issues.filter((issue) => {
								const haystack =
									`${issue.id} ${issue.title} ${issue.description ?? ""}`.toLowerCase()
								return haystack.includes(needle)
							})
						}),
					),
				),

			addDependency: (
				issueId: string,
				dependsOnId: string,
				type: DependencyType,
				syncTarget?: SyncTarget,
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					sql.withTransaction(
						Effect.gen(function* () {
							if (type === "parent-child") {
								yield* sql`
									DELETE FROM issue_dependencies
									WHERE issue_id = ${issueId} AND dependency_type = ${"parent-child"}
								`
							}

							yield* sql`
								INSERT INTO issue_dependencies (
									issue_id,
									depends_on_id,
									dependency_type,
									tombstoned_at
								)
								VALUES (${issueId}, ${dependsOnId}, ${type}, ${null})
								ON CONFLICT(issue_id, depends_on_id, dependency_type)
								DO UPDATE SET tombstoned_at = ${null}
							`
							if (syncTarget !== undefined) {
								yield* enqueueSync(sql, issueId, "upsert", syncTarget)
							}
						}),
					),
				),

			getEpicChildren: (
				epicId: string,
				cwd?: string,
			): Effect.Effect<readonly DependencyRef[], LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					Effect.gen(function* () {
						const rows = yield* sql<DependencyLinkRow>`
							SELECT issue_id, depends_on_id, dependency_type, tombstoned_at
							FROM issue_dependencies
							WHERE
								depends_on_id = ${epicId}
								AND dependency_type = ${"parent-child"}
								AND tombstoned_at IS NULL
						`
						if (rows.length === 0) {
							return []
						}

						const ids = rows.map((row) => row.issue_id)
						const issues = yield* loadAllIssues(sql)
						const byId = new Map(issues.map((issue) => [issue.id, issue]))
						return ids.flatMap((id) => {
							const issue = byId.get(id)
							if (issue === undefined) return []
							const dependency: DependencyRef = {
								id: issue.id,
								title: issue.title,
								status: issue.status,
								dependency_type: "parent-child",
								issue_type: issue.issue_type,
							}
							return [dependency]
						})
					}),
				),

			getParentEpic: (
				issueId: string,
				cwd?: string,
			): Effect.Effect<Issue | undefined, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					Effect.gen(function* () {
						const rows = yield* sql<{ readonly depends_on_id: string }>`
							SELECT depends_on_id
							FROM issue_dependencies
							WHERE
								issue_id = ${issueId}
								AND dependency_type = ${"parent-child"}
								AND tombstoned_at IS NULL
							LIMIT 1
						`
						if (rows.length === 0) {
							return undefined
						}

						const parentId = rows[0]!.depends_on_id
						const parentIssue = yield* loadIssueById(sql, parentId)
						if (parentIssue === undefined || parentIssue.issue_type !== "epic") {
							return undefined
						}
						return parentIssue
					}),
				),

			countIssues: (cwd?: string): Effect.Effect<number, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql<{ readonly count: number }>`
						SELECT COUNT(*) as count
						FROM issues
						WHERE deleted_at IS NULL
					`.pipe(Effect.map((rows) => rows[0]?.count ?? 0)),
				),

			listIssueAttachments: (
				issueId: string,
				cwd?: string,
			): Effect.Effect<readonly IssueAttachmentMetadata[], LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql<IssueAttachmentRow>`
						SELECT
							issue_id,
							attachment_id,
							filename,
							original_path,
							mime_type,
							size_bytes,
							created_at,
							content_blob
						FROM issue_attachments
						WHERE issue_id = ${issueId}
						ORDER BY created_at ASC, attachment_id ASC
					`.pipe(Effect.map((rows) => rows.map(rowToIssueAttachmentMetadata))),
				),

			getIssueAttachment: (
				issueId: string,
				attachmentId: string,
				cwd?: string,
			): Effect.Effect<IssueAttachmentRecord | undefined, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql<IssueAttachmentRow>`
						SELECT
							issue_id,
							attachment_id,
							filename,
							original_path,
							mime_type,
							size_bytes,
							created_at,
							content_blob
						FROM issue_attachments
						WHERE issue_id = ${issueId} AND attachment_id = ${attachmentId}
						LIMIT 1
					`.pipe(
						Effect.map((rows) => {
							const row = rows[0]
							return row === undefined ? undefined : rowToIssueAttachmentRecord(row)
						}),
					),
				),

			upsertIssueAttachment: (
				params: {
					issueId: string
					attachmentId: string
					filename: string
					originalPath: string
					mimeType: string
					size: number
					createdAt: string
					content: Uint8Array
				},
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql`
						INSERT INTO issue_attachments (
							issue_id,
							attachment_id,
							filename,
							original_path,
							mime_type,
							size_bytes,
							created_at,
							content_blob
						)
						VALUES (
							${params.issueId},
							${params.attachmentId},
							${params.filename},
							${params.originalPath},
							${params.mimeType},
							${params.size},
							${params.createdAt},
							${params.content}
						)
						ON CONFLICT(issue_id, attachment_id)
						DO UPDATE SET
							filename = ${params.filename},
							original_path = ${params.originalPath},
							mime_type = ${params.mimeType},
							size_bytes = ${params.size},
							created_at = ${params.createdAt},
							content_blob = ${params.content}
					`.pipe(Effect.asVoid),
				),

			removeIssueAttachment: (
				issueId: string,
				attachmentId: string,
				cwd?: string,
			): Effect.Effect<boolean, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql.withTransaction(
						Effect.gen(function* () {
							const existing = yield* sql<{ readonly count: number }>`
								SELECT COUNT(*) as count
								FROM issue_attachments
								WHERE issue_id = ${issueId} AND attachment_id = ${attachmentId}
							`
							if ((existing[0]?.count ?? 0) === 0) {
								return false
							}
							yield* sql`
								DELETE FROM issue_attachments
								WHERE issue_id = ${issueId} AND attachment_id = ${attachmentId}
							`
							return true
						}),
					),
				),

			clearIssueAttachments: (
				issueId: string,
				cwd?: string,
			): Effect.Effect<number, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql.withTransaction(
						Effect.gen(function* () {
							const existing = yield* sql<{ readonly count: number }>`
								SELECT COUNT(*) as count
								FROM issue_attachments
								WHERE issue_id = ${issueId}
							`
							const count = existing[0]?.count ?? 0
							if (count > 0) {
								yield* sql`
									DELETE FROM issue_attachments
									WHERE issue_id = ${issueId}
								`
							}
							return count
						}),
					),
				),

			listPendingSync: (
				target: SyncTarget,
				limit: number,
				cwd?: string,
			): Effect.Effect<readonly PendingSyncItem[], LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					sql.withTransaction(
						Effect.gen(function* () {
							const now = nowIso()
							const leaseExpiresAt = new Date(
								Date.now() + SYNC_QUEUE_LEASE_SECONDS * 1000,
							).toISOString()
							const claimToken = crypto.randomUUID()
							const candidateRows = yield* sql<{ readonly id: number }>`
								SELECT id
								FROM sync_queue
								WHERE
									target = ${target}
									AND (
										status = ${"pending"}
										OR (
											status = ${"processing"}
											AND (
												attempt_token IS NULL
												OR lease_expires_at IS NULL
												OR lease_expires_at <= ${now}
											)
										)
									)
									AND (next_attempt_at IS NULL OR next_attempt_at <= ${now})
								ORDER BY id ASC
								LIMIT ${Math.max(1, limit)}
							`
							const candidateIds = candidateRows.map((row) => row.id)
							if (candidateIds.length === 0) {
								return []
							}

							yield* sql`
								UPDATE sync_queue
								SET
									status = ${"processing"},
									attempt_token = ${claimToken},
									lease_expires_at = ${leaseExpiresAt},
									error = ${null},
									updated_at = ${now}
								WHERE
									${sql.in("id", candidateIds)}
									AND (
										status = ${"pending"}
										OR (
											status = ${"processing"}
											AND (
												attempt_token IS NULL
												OR lease_expires_at IS NULL
												OR lease_expires_at <= ${now}
											)
										)
									)
									AND (next_attempt_at IS NULL OR next_attempt_at <= ${now})
							`

							const claimedRows = yield* sql<SyncQueueRow>`
								SELECT id, issue_id, operation, target, attempts, payload_json, attempt_token
								FROM sync_queue
								WHERE attempt_token = ${claimToken}
								ORDER BY id ASC
							`

							return yield* Effect.forEach(claimedRows, (row) => {
								if (!isValidSyncOperation(row.operation)) {
									return Effect.fail(
										new LocalIssueStoreError({
											message: `Invalid sync operation in queue: ${row.operation}`,
										}),
									)
								}
								if (!isValidSyncTarget(row.target)) {
									return Effect.fail(
										new LocalIssueStoreError({
											message: `Invalid sync target in queue: ${row.target}`,
										}),
									)
								}
								if (row.attempt_token === null) {
									return Effect.fail(
										new LocalIssueStoreError({
											message: `Missing sync claim token for queue id ${row.id}`,
										}),
									)
								}
								return Effect.succeed({
									id: row.id,
									issueId: row.issue_id,
									operation: row.operation,
									target: row.target,
									attempts: row.attempts,
									payloadJson: row.payload_json,
									attemptToken: row.attempt_token,
								} satisfies PendingSyncItem)
							})
						}),
					),
				),

			markSyncSucceeded: (
				params: MarkSyncSucceededParams,
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				params.claims.length === 0
					? Effect.void
					: withSqlMutation(cwd, (sql) =>
							sql
								.withTransaction(
									Effect.gen(function* () {
										for (const claim of params.claims) {
											yield* sql`
											DELETE FROM sync_queue
											WHERE
												id = ${claim.id}
												AND status = ${"processing"}
												AND attempt_token = ${claim.attemptToken}
										`
										}

										yield* sql`
										DELETE FROM sync_queue
										WHERE
											issue_id = ${params.issueId}
											AND target = ${params.target}
											AND id <= ${params.maxQueueId}
									`
									}),
								)
								.pipe(Effect.asVoid),
						),

			markSyncRetriable: (
				params: MarkSyncRetriableParams,
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				params.claims.length === 0
					? Effect.void
					: withSqlMutation(cwd, (sql) =>
							sql
								.withTransaction(
									Effect.gen(function* () {
										const nextAttemptAt = new Date(
											Date.now() + params.delaySeconds * 1000,
										).toISOString()
										const now = nowIso()
										const nextAttempts = Math.max(0, params.nextAttempts)

										for (const claim of params.claims) {
											yield* sql`
											UPDATE sync_queue
											SET
												attempts = CASE
													WHEN attempts < ${nextAttempts}
														THEN ${nextAttempts}
														ELSE attempts
												END,
												error = ${params.errorMessage},
												status = ${"pending"},
												attempt_token = ${null},
												lease_expires_at = ${null},
												next_attempt_at = ${nextAttemptAt},
												updated_at = ${now}
											WHERE
												id = ${claim.id}
												AND status = ${"processing"}
												AND attempt_token = ${claim.attemptToken}
										`
										}
									}),
								)
								.pipe(Effect.asVoid),
						),

			markSyncTerminalFailure: (
				params: MarkSyncTerminalFailureParams,
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				params.claims.length === 0
					? Effect.void
					: withSqlMutation(cwd, (sql) =>
							sql
								.withTransaction(
									Effect.gen(function* () {
										const nextAttempts = Math.max(0, params.nextAttempts)
										const now = nowIso()
										for (const claim of params.claims) {
											yield* sql`
											UPDATE sync_queue
											SET
												attempts = CASE
													WHEN attempts < ${nextAttempts}
														THEN ${nextAttempts}
														ELSE attempts
												END,
												error = ${params.errorMessage},
												status = ${"failed"},
												attempt_token = ${null},
												lease_expires_at = ${null},
												next_attempt_at = ${null},
												updated_at = ${now}
											WHERE
												id = ${claim.id}
												AND status = ${"processing"}
												AND attempt_token = ${claim.attemptToken}
										`
										}
									}),
								)
								.pipe(Effect.asVoid),
						),

			getIssueForSync: (
				issueId: string,
				cwd?: string,
			): Effect.Effect<Issue | undefined, LocalIssueStoreError> =>
				withSql(cwd, (sql) => loadIssueById(sql, issueId)),

			getIssuesForSync: (
				issueIds: readonly string[],
				cwd?: string,
			): Effect.Effect<ReadonlyMap<string, Issue>, LocalIssueStoreError> =>
				issueIds.length === 0
					? Effect.succeed(new Map())
					: withSql(cwd, (sql) =>
							loadAllIssues(sql).pipe(
								Effect.map((issues) => {
									const lookup = new Set(issueIds)
									return new Map(
										issues
											.filter((issue) => lookup.has(issue.id))
											.map((issue) => [issue.id, issue] as const),
									)
								}),
							),
						),

			getExternalRef: (
				issueId: string,
				target: SyncTarget,
				cwd?: string,
			): Effect.Effect<
				{ readonly externalId: string; readonly externalKey?: string } | undefined,
				LocalIssueStoreError
			> =>
				withSql(cwd, (sql) =>
					sql<ExternalRefRow>`
						SELECT issue_id, target, external_id, external_key, last_synced_at
						FROM issue_external_refs
						WHERE issue_id = ${issueId} AND target = ${target}
						LIMIT 1
					`.pipe(
						Effect.map((rows) => {
							const row = rows[0]
							if (row === undefined) {
								return undefined
							}
							return {
								externalId: row.external_id,
								externalKey: row.external_key ?? undefined,
							}
						}),
					),
				),

			upsertExternalRef: (
				params: {
					issueId: string
					target: SyncTarget
					externalId: string
					externalKey?: string
				},
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					sql`
						INSERT INTO issue_external_refs (
							issue_id,
							target,
							external_id,
							external_key,
							last_synced_at
						)
						VALUES (
							${params.issueId},
							${params.target},
							${params.externalId},
							${params.externalKey ?? null},
							${nowIso()}
						)
						ON CONFLICT(issue_id, target)
						DO UPDATE SET
							external_id = ${params.externalId},
							external_key = ${params.externalKey ?? null},
							last_synced_at = ${nowIso()}
					`.pipe(Effect.asVoid),
				),

			importExternalSnapshot: (
				target: SyncTarget,
				snapshots: readonly ExternalIssueSnapshot[],
				cwd?: string,
			): Effect.Effect<number, LocalIssueStoreError> =>
				snapshots.length === 0
					? Effect.succeed(0)
					: withSqlMutation(cwd, (sql) =>
							sql.withTransaction(
								Effect.gen(function* () {
									for (const snapshot of snapshots) {
										yield* sql`
											INSERT INTO issues (
												id,
												title,
												description,
												status,
												priority,
												issue_type,
												created_at,
												updated_at,
												closed_at,
												assignee,
												labels_json,
												design,
												notes,
												acceptance,
												estimate,
												deleted_at
											)
											VALUES (
												${snapshot.localId},
												${snapshot.title},
												${snapshot.description ?? null},
												${snapshot.status},
												${snapshot.priority},
												${snapshot.issueType},
												${snapshot.createdAt},
												${snapshot.updatedAt},
												${snapshot.closedAt ?? null},
												${snapshot.assignee ?? null},
												${encodeLabels(snapshot.labels)},
												${snapshot.design ?? null},
												${snapshot.notes ?? null},
												${snapshot.acceptance ?? null},
												${snapshot.estimate ?? null},
												${null}
											)
											ON CONFLICT(id)
											DO UPDATE SET
												title = ${snapshot.title},
												description = ${snapshot.description ?? null},
												status = ${snapshot.status},
												priority = ${snapshot.priority},
												issue_type = ${snapshot.issueType},
												updated_at = ${snapshot.updatedAt},
												closed_at = ${snapshot.closedAt ?? null},
												assignee = ${snapshot.assignee ?? null},
												labels_json = ${encodeLabels(snapshot.labels)},
												design = ${snapshot.design ?? null},
												notes = ${snapshot.notes ?? null},
												acceptance = ${snapshot.acceptance ?? null},
												estimate = ${snapshot.estimate ?? null},
												deleted_at = ${null}
										`

										yield* sql`
											INSERT INTO issue_external_refs (
												issue_id,
												target,
												external_id,
												external_key,
												last_synced_at
											)
											VALUES (
												${snapshot.localId},
												${target},
												${snapshot.externalId},
												${snapshot.externalKey ?? null},
												${nowIso()}
											)
											ON CONFLICT(issue_id, target)
											DO UPDATE SET
												external_id = ${snapshot.externalId},
												external_key = ${snapshot.externalKey ?? null},
												last_synced_at = ${nowIso()}
										`

										yield* sql`
											DELETE FROM issue_dependencies
											WHERE issue_id = ${snapshot.localId} AND dependency_type = ${"parent-child"}
										`
										if (snapshot.parentLocalId !== undefined) {
											yield* sql`
												INSERT INTO issue_dependencies (
													issue_id,
													depends_on_id,
													dependency_type,
													tombstoned_at
												)
												VALUES (
													${snapshot.localId},
													${snapshot.parentLocalId},
													${"parent-child"},
													${null}
												)
											`
										}
									}

									return snapshots.length
								}),
							),
						),

			isBootstrapComplete: (
				target: SyncTarget,
				cwd?: string,
			): Effect.Effect<boolean, LocalIssueStoreError> =>
				withSql(cwd, (sql) =>
					sql<MetaRow>`
						SELECT value
						FROM meta
						WHERE key = ${`bootstrap:${target}`}
						LIMIT 1
					`.pipe(Effect.map((rows) => rows[0]?.value === "1")),
				),

			markBootstrapComplete: (
				target: SyncTarget,
				cwd?: string,
			): Effect.Effect<void, LocalIssueStoreError> =>
				withSqlMutation(cwd, (sql) =>
					sql`
						INSERT INTO meta (key, value)
						VALUES (${`bootstrap:${target}`}, ${"1"})
						ON CONFLICT(key)
						DO UPDATE SET value = ${"1"}
					`.pipe(Effect.asVoid),
				),
		}
	}),
}) {}
