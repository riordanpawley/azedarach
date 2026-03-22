import { AppConfig, getProjectStoragePaths, type ResolvedConfig } from "@azedarach/config"
import {
	type DaemonIssueCreateInput,
	type DaemonIssueSyncResult,
	type DaemonIssueUpdatePatch,
	type DependencyType,
	type IssueListFilters,
	type IssueListOptions,
	type TrackedIssue,
	TrackedIssueSchema,
} from "@azedarach/shared/rpc"
import { Command, CommandExecutor, Path } from "@effect/platform"
import { Data, Effect, Schema } from "effect"

/**
 * Configured issueSyncBackend option from issueTracker config.
 * Note: this is not the runtime read source-of-truth selector for board/list/get.
 * Daemon/TUI/CLI reads remain sqlite-local; issueSyncBackend controls sync flows.
 */
type IssueSyncBackendOption = "tracker" | "legacy" | "local" | "linear"

const LegacySyncResultSchema = Schema.Struct({
	pushed: Schema.Number,
	pulled: Schema.Number,
})

const BrSyncResultSchema = Schema.Struct({
	created: Schema.optional(Schema.Number),
	updated: Schema.optional(Schema.Number),
})

type LegacySyncResult = Schema.Schema.Type<typeof LegacySyncResultSchema>
type BrSyncResult = Schema.Schema.Type<typeof BrSyncResultSchema>

export class TrackerIssueDaemonError extends Data.TaggedError("TrackerIssueDaemonError")<{
	readonly reason:
		| "unsupported-backend"
		| "unsupported-field"
		| "command-failed"
		| "json-parse"
		| "not-found"
	readonly message: string
}> {}

export interface TrackerIssueDaemonServiceApi {
	readonly get: (
		issueId: string,
		projectPath?: string,
	) => Effect.Effect<TrackedIssue, TrackerIssueDaemonError>
	readonly list: (
		filters?: IssueListFilters,
		projectPath?: string,
		options?: IssueListOptions,
	) => Effect.Effect<ReadonlyArray<TrackedIssue>, TrackerIssueDaemonError>
	readonly create: (
		input: DaemonIssueCreateInput,
		projectPath?: string,
	) => Effect.Effect<TrackedIssue, TrackerIssueDaemonError>
	readonly update: (
		issueId: string,
		patch: DaemonIssueUpdatePatch,
		projectPath?: string,
	) => Effect.Effect<void, TrackerIssueDaemonError>
	readonly addDependency: (
		issueId: string,
		dependsOnId: string,
		dependencyType: DependencyType,
		projectPath?: string,
	) => Effect.Effect<void, TrackerIssueDaemonError>
	readonly removeDependency: (
		issueId: string,
		dependsOnId: string,
		dependencyType?: DependencyType,
		projectPath?: string,
	) => Effect.Effect<void, TrackerIssueDaemonError>
	readonly close: (
		issueId: string,
		reason?: string,
		projectPath?: string,
	) => Effect.Effect<void, TrackerIssueDaemonError>
	readonly delete: (
		issueId: string,
		projectPath?: string,
	) => Effect.Effect<void, TrackerIssueDaemonError>
	readonly sync: (
		projectPath?: string,
	) => Effect.Effect<DaemonIssueSyncResult, TrackerIssueDaemonError>
}

const resolveConfiguredIssueBackend = (
	issueTracker: ResolvedConfig["issueTracker"],
): IssueSyncBackendOption => {
	// Keep legacy shape compatibility while separating read behavior from issueSyncBackend.
	if ("tracker" in issueTracker) return "tracker"
	if ("legacy" in issueTracker) return "legacy"
	if ("linear" in issueTracker) return "linear"
	return "local"
}

const normalizeLegacySyncResult = (result: LegacySyncResult): DaemonIssueSyncResult => result

const normalizeBrSyncResult = (result: BrSyncResult): DaemonIssueSyncResult => ({
	pushed: 0,
	pulled: (result.created ?? 0) + (result.updated ?? 0),
})

const extractJsonPayload = (output: string): string => {
	const trimmed = output.trim()
	if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
		return trimmed
	}

	for (let index = 0; index < trimmed.length; index += 1) {
		const character = trimmed[index]
		if (character !== "{" && character !== "[") {
			continue
		}
		return trimmed.slice(index)
	}

	return trimmed
}

const decodeJson = <A, I>(
	schema: Schema.Schema<A, I>,
	output: string,
): Effect.Effect<A, TrackerIssueDaemonError> =>
	Schema.decode(Schema.parseJson(schema))(extractJsonPayload(output)).pipe(
		Effect.mapError(
			() =>
				new TrackerIssueDaemonError({
					reason: "json-parse",
					message: "tracker command returned invalid JSON",
				}),
		),
	)

const LocalIssueRowSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	description: Schema.NullOr(Schema.String),
	status: Schema.String,
	priority: Schema.Number,
	issue_type: Schema.String,
	created_at: Schema.String,
	updated_at: Schema.String,
	closed_at: Schema.NullOr(Schema.String),
	assignee: Schema.NullOr(Schema.String),
	labels_json: Schema.NullOr(Schema.String),
	implementations_json: Schema.NullOr(Schema.String),
	design: Schema.NullOr(Schema.String),
	notes: Schema.NullOr(Schema.String),
	acceptance: Schema.NullOr(Schema.String),
	estimate: Schema.NullOr(Schema.Number),
})

const LocalDependencyRowSchema = Schema.Struct({
	issue_id: Schema.String,
	depends_on_id: Schema.String,
	dependency_type: Schema.String,
	depends_on_title: Schema.NullOr(Schema.String),
	depends_on_status: Schema.NullOr(Schema.String),
	depends_on_issue_type: Schema.NullOr(Schema.String),
	issue_title: Schema.NullOr(Schema.String),
	issue_status: Schema.NullOr(Schema.String),
	issue_issue_type: Schema.NullOr(Schema.String),
})

const toIssueStatus = (value: string): TrackedIssue["status"] => {
	switch (value) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return value
		default:
			return "open"
	}
}

const toIssueType = (value: string): TrackedIssue["issue_type"] => {
	switch (value) {
		case "bug":
		case "feature":
		case "task":
		case "epic":
		case "chore":
			return value
		default:
			return "task"
	}
}

const toDependencyType = (value: string): DependencyType => {
	switch (value) {
		case "blocks":
		case "related":
		case "parent-child":
		case "discovered-from":
			return value
		default:
			return "related"
	}
}

const parseJsonStringArray = (value: string | null): ReadonlyArray<string> => {
	if (value === null) {
		return []
	}
	try {
		const parsed: unknown = JSON.parse(value)
		if (!Array.isArray(parsed)) {
			return []
		}
		const strings: Array<string> = []
		for (const entry of parsed) {
			if (typeof entry === "string") {
				strings.push(entry)
			}
		}
		return strings
	} catch {
		return []
	}
}

const escapeSqlString = (value: string): string => value.replaceAll("'", "''")

const sqlStringLiteral = (value: string): string => `'${escapeSqlString(value)}'`

const sortIssuesInMemory = (
	issues: ReadonlyArray<TrackedIssue>,
	sortBy: "updated_at" | "created_at",
	sortDirection: "asc" | "desc",
): ReadonlyArray<TrackedIssue> => {
	const sorted = [...issues]
	sorted.sort((left, right) => {
		const leftValue = sortBy === "created_at" ? left.created_at : left.updated_at
		const rightValue = sortBy === "created_at" ? right.created_at : right.updated_at
		return Date.parse(leftValue) - Date.parse(rightValue)
	})
	return sortDirection === "desc" ? sorted.reverse() : sorted
}

export class TrackerIssueDaemonService extends Effect.Service<TrackerIssueDaemonService>()(
	"TrackerIssueDaemonService",
	{
		dependencies: [AppConfig.Default],
		effect: Effect.gen(function* () {
			const appConfig = yield* AppConfig
			const commandExecutor = yield* CommandExecutor.CommandExecutor
			const pathService = yield* Path.Path

			const getRuntimeConfig = (
				projectPath?: string,
			): Effect.Effect<
				{
					readonly issueTracker: ResolvedConfig["issueTracker"]
					readonly syncEnabled: boolean
				},
				TrackerIssueDaemonError
			> =>
				(projectPath === undefined
					? appConfig.getIssueTrackerSyncConfig()
					: appConfig.getIssueTrackerSyncConfigForProjectPath(projectPath)
				).pipe(
					Effect.mapError(
						(error) =>
							new TrackerIssueDaemonError({
								reason: "unsupported-backend",
								message: error.message,
							}),
					),
				)

			const resolveExecutable = (
				projectPath?: string,
			): Effect.Effect<
				{ readonly executable: "tracker" | "legacy"; readonly syncEnabled: boolean },
				TrackerIssueDaemonError
			> =>
				Effect.gen(function* () {
					const { issueTracker, syncEnabled } = yield* getRuntimeConfig(projectPath)
					const backend = resolveConfiguredIssueBackend(issueTracker)
					if (backend === "tracker") {
						const trackerRuntime: {
							readonly executable: "tracker" | "legacy"
							readonly syncEnabled: boolean
						} = {
							executable: "tracker",
							syncEnabled,
						}
						return trackerRuntime
					}
					if (backend === "legacy") {
						const legacyRuntime: {
							readonly executable: "tracker" | "legacy"
							readonly syncEnabled: boolean
						} = {
							executable: "legacy",
							syncEnabled,
						}
						return legacyRuntime
					}
					const localRuntime: {
						readonly executable: "tracker" | "legacy"
						readonly syncEnabled: boolean
					} = {
						executable: "tracker",
						syncEnabled,
					}
					return localRuntime
				})

			const runJson = (
				executable: "tracker" | "legacy",
				args: ReadonlyArray<string>,
				projectPath?: string,
			): Effect.Effect<string, TrackerIssueDaemonError> => {
				const baseCommand = Command.make(executable, ...args, "--json")
				const command =
					projectPath === undefined
						? baseCommand
						: baseCommand.pipe(Command.workingDirectory(projectPath))
				return commandExecutor.string(command).pipe(
					Effect.mapError(
						(error) =>
							new TrackerIssueDaemonError({
								reason: "command-failed",
								message: "stderr" in error ? String(error.stderr) : String(error),
							}),
					),
				)
			}

			const runSqliteJson = <A, I>(
				dbPath: string,
				query: string,
				schema: Schema.Schema<A, I>,
			): Effect.Effect<A, TrackerIssueDaemonError> =>
				commandExecutor.string(Command.make("sqlite3", "-json", dbPath, query)).pipe(
					Effect.mapError(
						(error) =>
							new TrackerIssueDaemonError({
								reason: "command-failed",
								message: "stderr" in error ? String(error.stderr) : String(error),
							}),
					),
					Effect.flatMap((output) =>
						decodeJson(schema, output.trim().length === 0 ? "[]" : output),
					),
				)

			const runSqliteExec = (
				dbPath: string,
				query: string,
			): Effect.Effect<void, TrackerIssueDaemonError> =>
				commandExecutor.string(Command.make("sqlite3", dbPath, query)).pipe(
					Effect.mapError(
						(error) =>
							new TrackerIssueDaemonError({
								reason: "command-failed",
								message: "stderr" in error ? String(error.stderr) : String(error),
							}),
					),
					Effect.asVoid,
				)

			const resolveLocalDbPath = (projectPath: string | undefined): string => {
				const targetProjectPath = projectPath ?? process.cwd()
				const storagePaths = getProjectStoragePaths(targetProjectPath, pathService)
				return storagePaths.canonicalDbPath
			}

			const parseAlphaIssueId = (issueId: string): number | undefined => {
				if (!/^[a-z]+$/.test(issueId)) {
					return undefined
				}
				let value = 0
				for (const char of issueId) {
					value = value * 26 + (char.charCodeAt(0) - 96)
				}
				return value
			}

			const formatAlphaIssueId = (value: number): string => {
				let remaining = value
				let output = ""
				while (remaining > 0) {
					const index = (remaining - 1) % 26
					output = String.fromCharCode(97 + index) + output
					remaining = Math.floor((remaining - 1) / 26)
				}
				return output.length === 0 ? "a" : output
			}

			const generateNextIssueId = (
				projectPath: string | undefined,
			): Effect.Effect<string, TrackerIssueDaemonError> =>
				Effect.gen(function* () {
					const dbPath = resolveLocalDbPath(projectPath)
					const rows = yield* runSqliteJson(
						dbPath,
						"SELECT id FROM issues WHERE deleted_at IS NULL;",
						Schema.Array(Schema.Struct({ id: Schema.String })),
					)
					let maxValue = 0
					for (const row of rows) {
						const parsed = parseAlphaIssueId(row.id)
						if (parsed !== undefined && parsed > maxValue) {
							maxValue = parsed
						}
					}
					return formatAlphaIssueId(maxValue + 1)
				})

			const listFromLocalSqlite = (
				filters: IssueListFilters | undefined,
				projectPath: string | undefined,
				options: IssueListOptions | undefined,
				compactPayload: boolean,
			): Effect.Effect<ReadonlyArray<TrackedIssue>, TrackerIssueDaemonError> =>
				Effect.gen(function* () {
					const targetProjectPath = projectPath ?? process.cwd()
					const storagePaths = getProjectStoragePaths(targetProjectPath, pathService)
					const dbPath = storagePaths.canonicalDbPath
					const issueRows = yield* runSqliteJson(
						dbPath,
						"SELECT id, title, description, status, priority, issue_type, created_at, updated_at, closed_at, assignee, labels_json, implementations_json, design, notes, acceptance, estimate FROM issues WHERE deleted_at IS NULL;",
						Schema.Array(LocalIssueRowSchema),
					)
					const dependencyRows = yield* runSqliteJson(
						dbPath,
						"SELECT d.issue_id, d.depends_on_id, d.dependency_type, parent.title AS depends_on_title, parent.status AS depends_on_status, parent.issue_type AS depends_on_issue_type, child.title AS issue_title, child.status AS issue_status, child.issue_type AS issue_issue_type FROM issue_dependencies d LEFT JOIN issues parent ON parent.id = d.depends_on_id AND parent.deleted_at IS NULL LEFT JOIN issues child ON child.id = d.issue_id AND child.deleted_at IS NULL WHERE d.tombstoned_at IS NULL;",
						Schema.Array(LocalDependencyRowSchema),
					)

					const dependenciesByIssueId = new Map<
						string,
						Array<NonNullable<TrackedIssue["dependencies"]>[number]>
					>()
					const dependentsByIssueId = new Map<
						string,
						Array<NonNullable<TrackedIssue["dependents"]>[number]>
					>()

					for (const row of dependencyRows) {
						const dependencyType = toDependencyType(row.dependency_type)
						const dependencies = dependenciesByIssueId.get(row.issue_id) ?? []
						dependencies.push({
							id: row.depends_on_id,
							dependency_type: dependencyType,
							title: row.depends_on_title ?? undefined,
							status:
								row.depends_on_status === null ? undefined : toIssueStatus(row.depends_on_status),
							issue_type:
								row.depends_on_issue_type === null
									? undefined
									: toIssueType(row.depends_on_issue_type),
						})
						dependenciesByIssueId.set(row.issue_id, dependencies)

						const dependents = dependentsByIssueId.get(row.depends_on_id) ?? []
						dependents.push({
							id: row.issue_id,
							dependency_type: dependencyType,
							title: row.issue_title ?? undefined,
							status: row.issue_status === null ? undefined : toIssueStatus(row.issue_status),
							issue_type:
								row.issue_issue_type === null ? undefined : toIssueType(row.issue_issue_type),
						})
						dependentsByIssueId.set(row.depends_on_id, dependents)
					}

					const issues = issueRows.map((row): TrackedIssue => {
						const dependencies = dependenciesByIssueId.get(row.id) ?? []
						const dependents = dependentsByIssueId.get(row.id) ?? []
						const compactDependencies = compactPayload ? undefined : dependencies
						const compactDependents = compactPayload ? undefined : dependents
						return {
							id: row.id,
							title: row.title,
							description: compactPayload ? undefined : (row.description ?? undefined),
							status: toIssueStatus(row.status),
							priority: row.priority,
							issue_type: toIssueType(row.issue_type),
							created_at: row.created_at,
							updated_at: row.updated_at,
							closed_at: row.closed_at,
							assignee: row.assignee,
							labels: [...parseJsonStringArray(row.labels_json)],
							design: compactPayload ? undefined : (row.design ?? undefined),
							notes: compactPayload ? undefined : (row.notes ?? undefined),
							acceptance: compactPayload ? undefined : (row.acceptance ?? undefined),
							estimate: row.estimate ?? undefined,
							implementations: [...parseJsonStringArray(row.implementations_json)],
							dependencies: compactDependencies,
							dependents: compactDependents,
							dependency_count: dependencies.length,
							dependent_count: dependents.length,
						}
					})

					const filtered = issues.filter((issue) => {
						if (filters?.status !== undefined && issue.status !== filters.status) return false
						if (filters?.priority !== undefined && issue.priority !== filters.priority) return false
						if (filters?.type !== undefined && issue.issue_type !== filters.type) return false
						if (
							filters?.parent !== undefined &&
							!(issue.dependencies ?? []).some(
								(dependency) =>
									dependency.id === filters.parent && dependency.dependency_type === "parent-child",
							)
						) {
							return false
						}
						if (
							filters?.implementations !== undefined &&
							filters.implementations.length > 0 &&
							!issue.implementations.some((implementation) =>
								filters.implementations?.includes(implementation),
							)
						) {
							return false
						}
						return true
					})

					const includeClosed = options?.includeClosed ?? true
					const visible = includeClosed
						? filtered
						: filtered.filter((issue) => issue.status !== "closed" && issue.status !== "tombstone")
					const sortBy = options?.sortBy ?? "updated_at"
					const sortDirection = options?.sortDirection ?? "desc"
					const sorted = sortIssuesInMemory(visible, sortBy, sortDirection)
					const limit = options?.limit
					const bounded = limit === undefined ? sorted : sorted.slice(0, Math.max(0, limit))
					return yield* Schema.decodeUnknown(Schema.Array(TrackedIssueSchema))(bounded).pipe(
						Effect.mapError(
							() =>
								new TrackerIssueDaemonError({
									reason: "json-parse",
									message: "local issue backend produced invalid issue payload",
								}),
						),
					)
				})

			return {
				get: (issueId, projectPath) =>
					Effect.gen(function* () {
						const issues = yield* listFromLocalSqlite(undefined, projectPath, undefined, false)
						const issue = issues.find((candidate) => candidate.id === issueId)
						if (issue === undefined || issue.status === "tombstone") {
							return yield* Effect.fail(
								new TrackerIssueDaemonError({
									reason: "not-found",
									message: `Issue not found: ${issueId}`,
								}),
							)
						}
						return issue
					}),
				list: (filters, projectPath, options) =>
					Effect.gen(function* () {
						return yield* listFromLocalSqlite(filters, projectPath, options, true)
					}),
				create: (input, projectPath) =>
					Effect.gen(function* () {
						const dbPath = resolveLocalDbPath(projectPath)
						const nowIso = new Date().toISOString()
						const issueId = yield* generateNextIssueId(projectPath)
						const status = input.status ?? "open"
						const type = input.type ?? "task"
						const priority = input.priority ?? 3
						const labelsJson = JSON.stringify(input.labels ?? [])
						const implementationsJson = JSON.stringify(input.implementations ?? [])
						const closedAtLiteral = status === "closed" ? sqlStringLiteral(nowIso) : "NULL"
						yield* runSqliteExec(
							dbPath,
							`INSERT INTO issues (
                                id, title, description, status, priority, issue_type,
                                created_at, updated_at, closed_at, assignee,
                                labels_json, implementations_json, design, notes, acceptance, estimate, deleted_at
                             ) VALUES (
                                ${sqlStringLiteral(issueId)},
                                ${sqlStringLiteral(input.title)},
                                ${sqlStringLiteral(input.description ?? "")},
                                ${sqlStringLiteral(status)},
                                ${String(priority)},
                                ${sqlStringLiteral(type)},
                                ${sqlStringLiteral(nowIso)},
                                ${sqlStringLiteral(nowIso)},
                                ${closedAtLiteral},
                                ${
																	input.assignee === undefined
																		? "NULL"
																		: sqlStringLiteral(input.assignee)
																},
                                ${sqlStringLiteral(labelsJson)},
                                ${sqlStringLiteral(implementationsJson)},
                                ${
																	input.design === undefined
																		? "NULL"
																		: sqlStringLiteral(input.design)
																},
                                NULL,
                                ${
																	input.acceptance === undefined
																		? "NULL"
																		: sqlStringLiteral(input.acceptance)
																},
                                ${input.estimate === undefined ? "NULL" : String(input.estimate)},
                                NULL
                             );`,
						)

						if (input.parent !== undefined) {
							yield* runSqliteExec(
								dbPath,
								`INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, tombstoned_at)
                                 VALUES (
                                    ${sqlStringLiteral(issueId)},
                                    ${sqlStringLiteral(input.parent)},
                                    'parent-child',
                                    NULL
                                 );`,
							)
						}

						const refreshedIssues = yield* listFromLocalSqlite(
							undefined,
							projectPath,
							undefined,
							false,
						)
						const refreshed = refreshedIssues.find((issue) => issue.id === issueId)
						if (refreshed === undefined) {
							return yield* Effect.fail(
								new TrackerIssueDaemonError({
									reason: "not-found",
									message: `Issue not found after create: ${issueId}`,
								}),
							)
						}
						return refreshed
					}),
				update: (issueId, patch, projectPath) =>
					Effect.gen(function* () {
						if (patch.parent !== undefined) {
							return yield* Effect.fail(
								new TrackerIssueDaemonError({
									reason: "unsupported-field",
									message: "Issue parent updates are not supported via daemon local backend yet.",
								}),
							)
						}

						const issues = yield* listFromLocalSqlite(undefined, projectPath, undefined, false)
						const existing = issues.find((candidate) => candidate.id === issueId)
						if (existing === undefined || existing.status === "tombstone") {
							return yield* Effect.fail(
								new TrackerIssueDaemonError({
									reason: "not-found",
									message: `Issue not found: ${issueId}`,
								}),
							)
						}

						const dbPath = resolveLocalDbPath(projectPath)
						const nowIso = new Date().toISOString()
						const updates: Array<string> = [`updated_at = ${sqlStringLiteral(nowIso)}`]

						if (patch.status !== undefined) {
							updates.push(`status = ${sqlStringLiteral(patch.status)}`)
							updates.push(
								`closed_at = ${patch.status === "closed" ? sqlStringLiteral(nowIso) : "NULL"}`,
							)
						}
						if (patch.notes !== undefined) {
							updates.push(`notes = ${sqlStringLiteral(patch.notes)}`)
						}
						if (patch.priority !== undefined) {
							updates.push(`priority = ${String(patch.priority)}`)
						}
						if (patch.title !== undefined) {
							updates.push(`title = ${sqlStringLiteral(patch.title)}`)
						}
						if (patch.type !== undefined) {
							updates.push(`issue_type = ${sqlStringLiteral(patch.type)}`)
						}
						if (patch.description !== undefined) {
							updates.push(`description = ${sqlStringLiteral(patch.description)}`)
						}
						if (patch.design !== undefined) {
							updates.push(`design = ${sqlStringLiteral(patch.design)}`)
						}
						if (patch.acceptance !== undefined) {
							updates.push(`acceptance = ${sqlStringLiteral(patch.acceptance)}`)
						}
						if (patch.assignee !== undefined) {
							updates.push(`assignee = ${sqlStringLiteral(patch.assignee)}`)
						}
						if (patch.estimate !== undefined) {
							updates.push(`estimate = ${String(patch.estimate)}`)
						}
						if (patch.labels !== undefined) {
							updates.push(`labels_json = ${sqlStringLiteral(JSON.stringify(patch.labels))}`)
						}
						if (patch.implementations !== undefined) {
							updates.push(
								`implementations_json = ${sqlStringLiteral(JSON.stringify(patch.implementations))}`,
							)
						}

						yield* runSqliteExec(
							dbPath,
							`UPDATE issues SET ${updates.join(", ")} WHERE id = ${sqlStringLiteral(issueId)} AND deleted_at IS NULL;`,
						)
					}),
				addDependency: (issueId, dependsOnId, dependencyType, projectPath) =>
					Effect.gen(function* () {
						const dbPath = resolveLocalDbPath(projectPath)
						const nowIso = new Date().toISOString()
						yield* runSqliteExec(
							dbPath,
							`UPDATE issue_dependencies
                             SET tombstoned_at = NULL
                             WHERE issue_id = ${sqlStringLiteral(issueId)}
                               AND depends_on_id = ${sqlStringLiteral(dependsOnId)}
                               AND dependency_type = ${sqlStringLiteral(dependencyType)};`,
						)
						yield* runSqliteExec(
							dbPath,
							`INSERT INTO issue_dependencies (issue_id, depends_on_id, dependency_type, created_at, tombstoned_at)
                             SELECT
                                ${sqlStringLiteral(issueId)},
                                ${sqlStringLiteral(dependsOnId)},
                                ${sqlStringLiteral(dependencyType)},
                                ${sqlStringLiteral(nowIso)},
                                NULL
                             WHERE NOT EXISTS (
                                SELECT 1 FROM issue_dependencies
                                WHERE issue_id = ${sqlStringLiteral(issueId)}
                                  AND depends_on_id = ${sqlStringLiteral(dependsOnId)}
                                  AND dependency_type = ${sqlStringLiteral(dependencyType)}
                             );`,
						)
					}),
				removeDependency: (issueId, dependsOnId, dependencyType, projectPath) =>
					Effect.gen(function* () {
						const dbPath = resolveLocalDbPath(projectPath)
						const nowIso = new Date().toISOString()
						const filters = [
							`issue_id = ${sqlStringLiteral(issueId)}`,
							`depends_on_id = ${sqlStringLiteral(dependsOnId)}`,
							"tombstoned_at IS NULL",
						]
						if (dependencyType !== undefined) {
							filters.push(`dependency_type = ${sqlStringLiteral(dependencyType)}`)
						}
						yield* runSqliteExec(
							dbPath,
							`UPDATE issue_dependencies
                             SET tombstoned_at = ${sqlStringLiteral(nowIso)}
                             WHERE ${filters.join(" AND ")};`,
						)
					}),
				close: (issueId, reason, projectPath) =>
					Effect.gen(function* () {
						const dbPath = resolveLocalDbPath(projectPath)
						const nowIso = new Date().toISOString()
						const escapedReason =
							reason === undefined ? undefined : reason.trim().length === 0 ? undefined : reason
						const notesUpdate =
							escapedReason === undefined ? "" : `, notes = ${sqlStringLiteral(escapedReason)}`
						yield* runSqliteExec(
							dbPath,
							`UPDATE issues
                             SET status = 'closed',
                                 closed_at = ${sqlStringLiteral(nowIso)},
                                 updated_at = ${sqlStringLiteral(nowIso)}
                                 ${notesUpdate}
                             WHERE id = ${sqlStringLiteral(issueId)} AND deleted_at IS NULL;`,
						)
					}),
				delete: (issueId, projectPath) =>
					Effect.gen(function* () {
						const dbPath = resolveLocalDbPath(projectPath)
						const nowIso = new Date().toISOString()
						yield* runSqliteExec(
							dbPath,
							`UPDATE issues
                             SET deleted_at = ${sqlStringLiteral(nowIso)},
                                 updated_at = ${sqlStringLiteral(nowIso)}
                             WHERE id = ${sqlStringLiteral(issueId)} AND deleted_at IS NULL;`,
						)
						yield* runSqliteExec(
							dbPath,
							`UPDATE issue_dependencies
                             SET tombstoned_at = ${sqlStringLiteral(nowIso)}
                             WHERE tombstoned_at IS NULL
                               AND (
                                   issue_id = ${sqlStringLiteral(issueId)}
                                   OR depends_on_id = ${sqlStringLiteral(issueId)}
                               );`,
						)
					}),
				sync: (projectPath) =>
					Effect.gen(function* () {
						const { executable, syncEnabled } = yield* resolveExecutable(projectPath)
						if (!syncEnabled) {
							return { pushed: 0, pulled: 0 }
						}
						const output = yield* runJson(executable, ["sync"], projectPath)
						switch (executable) {
							case "tracker": {
								const parsed = yield* decodeJson(LegacySyncResultSchema, output)
								return normalizeLegacySyncResult(parsed)
							}
							case "legacy": {
								const parsed = yield* decodeJson(BrSyncResultSchema, output)
								return normalizeBrSyncResult(parsed)
							}
						}
					}),
			} satisfies TrackerIssueDaemonServiceApi
		}),
	},
) {}
