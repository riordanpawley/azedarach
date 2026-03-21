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
type IssueBackendMode = "tracker" | "legacy" | "local" | "linear"

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
): IssueBackendMode => {
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

const buildListArgs = (
	filters: IssueListFilters | undefined,
	options: IssueListOptions | undefined,
): Array<string> => {
	const args = ["list"]
	const limit = options?.limit
	if (limit !== undefined) {
		args.push("--limit", String(limit))
	}
	if (options?.includeClosed ?? true) {
		args.push("--all")
	}
	if (filters?.status !== undefined) {
		args.push("--status", filters.status)
	}
	if (filters?.priority !== undefined) {
		args.push("--priority", String(filters.priority))
	}
	if (filters?.type !== undefined) {
		args.push("--type", filters.type)
	}
	if (filters?.parent !== undefined) {
		args.push("--parent", filters.parent)
	}
	return args
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
					return yield* Effect.fail(
						new TrackerIssueDaemonError({
							reason: "unsupported-backend",
							message: "Daemon issue RPC currently supports tracker/legacy backends only.",
						}),
					)
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

			const runDirect = (
				executable: "tracker" | "legacy",
				args: ReadonlyArray<string>,
				projectPath?: string,
			): Effect.Effect<string, TrackerIssueDaemonError> => {
				const baseCommand = Command.make(executable, ...args)
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

			const rejectUnsupportedImplementations = (
				implementations: ReadonlyArray<string> | undefined,
			): Effect.Effect<void, TrackerIssueDaemonError> =>
				implementations !== undefined && implementations.length > 0
					? Effect.fail(
							new TrackerIssueDaemonError({
								reason: "unsupported-field",
								message:
									"Tracker/legacy issue RPC does not support implementation-scoped issue fields yet.",
							}),
						)
					: Effect.void

			return {
				get: (issueId, projectPath) =>
					Effect.gen(function* () {
						const { issueTracker } = yield* getRuntimeConfig(projectPath)
						const backend = resolveConfiguredIssueBackend(issueTracker)
						if (backend === "local" || backend === "linear") {
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
						}
						const { executable } = yield* resolveExecutable(projectPath)
						const output = yield* runJson(executable, ["show", issueId], projectPath)
						const issues = yield* decodeJson(Schema.Array(TrackedIssueSchema), output)
						const issue = issues[0]
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
						yield* rejectUnsupportedImplementations(filters?.implementations)
						const { issueTracker } = yield* getRuntimeConfig(projectPath)
						const backend = resolveConfiguredIssueBackend(issueTracker)
						if (backend === "local" || backend === "linear") {
							return yield* listFromLocalSqlite(filters, projectPath, options, true)
						}
						const { executable } = yield* resolveExecutable(projectPath)
						const output = yield* runJson(executable, buildListArgs(filters, options), projectPath)
						const issues = yield* decodeJson(Schema.Array(TrackedIssueSchema), output)
						const nonTombstones = issues.filter((issue) => issue.status !== "tombstone")
						const sortBy = options?.sortBy ?? "updated_at"
						const sortDirection = options?.sortDirection ?? "desc"
						const sorted = sortIssuesInMemory(nonTombstones, sortBy, sortDirection)
						if (options?.limit === undefined) {
							return sorted
						}
						return sorted.slice(0, Math.max(0, options.limit))
					}),
				create: (input, projectPath) =>
					Effect.gen(function* () {
						yield* rejectUnsupportedImplementations(input.implementations)
						const { executable } = yield* resolveExecutable(projectPath)
						const args = ["create", input.title]
						if (input.type !== undefined) args.push("--type", input.type)
						if (input.priority !== undefined) args.push("--priority", String(input.priority))
						if (input.description !== undefined) args.push("--description", input.description)
						if (input.design !== undefined) args.push("--design", input.design)
						if (input.acceptance !== undefined) args.push("--acceptance", input.acceptance)
						if (input.assignee !== undefined) args.push("--assignee", input.assignee)
						if (input.estimate !== undefined) args.push("--estimate", String(input.estimate))
						if (input.labels !== undefined && input.labels.length > 0) {
							args.push("--labels", input.labels.join(","))
						}
						if (input.parent !== undefined) args.push("--parent", input.parent)
						const output = yield* runJson(executable, args, projectPath)
						const created = yield* decodeJson(TrackedIssueSchema, output)
						if (input.status === undefined || input.status === "open") {
							return created
						}
						yield* runJson(
							executable,
							["update", created.id, "--status", input.status],
							projectPath,
						)
						return { ...created, status: input.status }
					}),
				update: (issueId, patch, projectPath) =>
					Effect.gen(function* () {
						yield* rejectUnsupportedImplementations(patch.implementations)
						const { executable } = yield* resolveExecutable(projectPath)
						const args = ["update", issueId]
						if (patch.status !== undefined) args.push("--status", patch.status)
						if (patch.notes !== undefined) args.push("--notes", patch.notes)
						if (patch.priority !== undefined) args.push("--priority", String(patch.priority))
						if (patch.title !== undefined) args.push("--title", patch.title)
						if (patch.type !== undefined) args.push("--type", patch.type)
						if (patch.description !== undefined) args.push("--description", patch.description)
						if (patch.design !== undefined) args.push("--design", patch.design)
						if (patch.acceptance !== undefined) args.push("--acceptance", patch.acceptance)
						if (patch.assignee !== undefined) args.push("--assignee", patch.assignee)
						if (patch.estimate !== undefined) args.push("--estimate", String(patch.estimate))
						if (patch.labels !== undefined) {
							for (const label of patch.labels) {
								args.push("--set-labels", label)
							}
						}
						if (patch.parent !== undefined) args.push("--parent", patch.parent)
						yield* runJson(executable, args, projectPath)
					}),
				addDependency: (issueId, dependsOnId, dependencyType, projectPath) =>
					Effect.gen(function* () {
						const { executable } = yield* resolveExecutable(projectPath)
						yield* runJson(
							executable,
							["dep", "add", issueId, dependsOnId, "--type", dependencyType],
							projectPath,
						)
					}),
				removeDependency: (issueId, dependsOnId, dependencyType, projectPath) =>
					Effect.gen(function* () {
						const { executable } = yield* resolveExecutable(projectPath)
						const args = ["dep", "remove", issueId, dependsOnId]
						if (dependencyType !== undefined) {
							args.push("--type", dependencyType)
						}
						yield* runJson(executable, args, projectPath)
					}),
				close: (issueId, reason, projectPath) =>
					Effect.gen(function* () {
						const { executable } = yield* resolveExecutable(projectPath)
						const args = ["close", issueId]
						if (reason !== undefined) {
							args.push("--reason", reason)
						}
						yield* runJson(executable, args, projectPath)
					}),
				delete: (issueId, projectPath) =>
					Effect.gen(function* () {
						const { executable } = yield* resolveExecutable(projectPath)
						yield* runDirect(executable, ["delete", issueId, "--force"], projectPath)
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
