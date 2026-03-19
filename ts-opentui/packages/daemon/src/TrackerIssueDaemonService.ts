import { AppConfig, type ResolvedConfig } from "@azedarach/config"
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
import { Command, CommandExecutor } from "@effect/platform"
import { Data, Effect, Schema } from "effect"

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
