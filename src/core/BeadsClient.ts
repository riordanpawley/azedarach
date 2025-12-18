/**
 * BeadsClient - Effect service for interacting with the bd CLI
 *
 * Wraps bd commands with Effect for type-safe, composable issue tracking operations.
 * All bd commands are executed with --json flag for structured output.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"
import * as Schema from "effect/Schema"
import { ProjectService } from "../services/ProjectService.js"

// ============================================================================
// Schema Definitions
// ============================================================================

/**
 * Dependency reference schema for issue dependencies/dependents
 */
const DependencyRefSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	status: Schema.Literal("open", "in_progress", "blocked", "closed", "tombstone"),
	dependency_type: Schema.Literal("blocks", "related", "parent-child", "discovered-from"),
	issue_type: Schema.Literal("bug", "feature", "task", "epic", "chore").pipe(Schema.optional),
})

export type DependencyRef = Schema.Schema.Type<typeof DependencyRefSchema>

/**
 * Issue schema matching bd --json output
 */
const IssueSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	description: Schema.String.pipe(Schema.optional),
	status: Schema.Literal("open", "in_progress", "blocked", "closed", "tombstone"),
	priority: Schema.Number,
	issue_type: Schema.Literal("bug", "feature", "task", "epic", "chore"),
	created_at: Schema.String,
	updated_at: Schema.String,
	closed_at: Schema.NullOr(Schema.String).pipe(Schema.optional),
	assignee: Schema.NullOr(Schema.String).pipe(Schema.optional),
	labels: Schema.Array(Schema.String).pipe(Schema.optional),
	design: Schema.String.pipe(Schema.optional),
	notes: Schema.String.pipe(Schema.optional),
	acceptance: Schema.String.pipe(Schema.optional),
	estimate: Schema.Number.pipe(Schema.optional),
	dependent_count: Schema.Number.pipe(Schema.optional),
	dependency_count: Schema.Number.pipe(Schema.optional),
	dependents: Schema.Array(DependencyRefSchema).pipe(Schema.optional),
	dependencies: Schema.Array(DependencyRefSchema).pipe(Schema.optional),
})

export type Issue = Schema.Schema.Type<typeof IssueSchema>

/**
 * Sync result schema
 */
const SyncResultSchema = Schema.Struct({
	pushed: Schema.Number,
	pulled: Schema.Number,
})

export type SyncResult = Schema.Schema.Type<typeof SyncResultSchema>

// ============================================================================
// Error Types
// ============================================================================

/**
 * Generic bd command execution error
 */
export class BeadsError extends Data.TaggedError("BeadsError")<{
	readonly message: string
	readonly command: string
	readonly stderr?: string
}> {}

/**
 * Specific error for when an issue is not found
 */
export class NotFoundError extends Data.TaggedError("NotFoundError")<{
	readonly issueId: string
}> {}

/**
 * JSON parsing error from bd output
 */
export class ParseError extends Data.TaggedError("ParseError")<{
	readonly message: string
	readonly output: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * BeadsClient service interface
 *
 * Provides typed access to bd CLI commands with Effect error handling.
 * Note: All methods require CommandExecutor in their context.
 */
export interface BeadsClientService {
	/**
	 * List issues with optional filters
	 *
	 * @example
	 * ```ts
	 * // Get all in-progress tasks
	 * BeadsClient.list({ status: "in_progress", type: "task" })
	 * ```
	 */
	readonly list: (
		filters?: {
			status?: string
			priority?: number
			type?: string
		},
		cwd?: string,
	) => Effect.Effect<Issue[], BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Show details for a single issue
	 *
	 * @example
	 * ```ts
	 * BeadsClient.show("az-05y")
	 * ```
	 */
	readonly show: (
		id: string,
		cwd?: string,
	) => Effect.Effect<
		Issue,
		BeadsError | NotFoundError | ParseError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Update issue fields
	 *
	 * @example
	 * ```ts
	 * BeadsClient.update("az-05y", {
	 *   status: "in_progress",
	 *   notes: "Started working on this",
	 *   title: "Updated title"
	 * })
	 * ```
	 */
	readonly update: (
		id: string,
		fields: {
			status?: string
			notes?: string
			priority?: number
			title?: string
			description?: string
			design?: string
			acceptance?: string
			assignee?: string
			estimate?: number
			labels?: string[]
		},
		cwd?: string,
	) => Effect.Effect<void, BeadsError, CommandExecutor.CommandExecutor>

	/**
	 * Close an issue with optional reason
	 *
	 * @example
	 * ```ts
	 * BeadsClient.close("az-05y", "Implementation complete")
	 * ```
	 */
	readonly close: (
		id: string,
		reason?: string,
		cwd?: string,
	) => Effect.Effect<void, BeadsError, CommandExecutor.CommandExecutor>

	/**
	 * Sync beads database (push/pull)
	 *
	 * @example
	 * ```ts
	 * BeadsClient.sync().pipe(
	 *   Effect.tap(result => Console.log(`Synced: ${result.pushed} pushed, ${result.pulled} pulled`))
	 * )
	 * ```
	 */
	readonly sync: (
		cwd?: string,
	) => Effect.Effect<SyncResult, BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Import-only sync - re-imports beads from JSONL into database without git operations.
	 * Use after git merge to recover any beads incorrectly removed by the merge driver.
	 */
	readonly syncImportOnly: (
		cwd?: string,
	) => Effect.Effect<SyncResult, BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Recover tombstoned issues from JSONL.
	 * Workaround for bd sync bug where issues get incorrectly tombstoned during merge.
	 * See issue az-zby for details.
	 *
	 * @returns Number of issues recovered
	 */
	readonly recoverTombstones: (
		cwd?: string,
	) => Effect.Effect<number, BeadsError, CommandExecutor.CommandExecutor>

	/**
	 * Get ready (unblocked) issues
	 *
	 * @example
	 * ```ts
	 * BeadsClient.ready()
	 * ```
	 */
	readonly ready: (
		cwd?: string,
	) => Effect.Effect<Issue[], BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Search issues by query string
	 *
	 * @example
	 * ```ts
	 * BeadsClient.search("beads client")
	 * ```
	 */
	readonly search: (
		query: string,
		cwd?: string,
	) => Effect.Effect<Issue[], BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Create a new issue
	 *
	 * @example
	 * ```ts
	 * BeadsClient.create({
	 *   title: "Implement feature X",
	 *   type: "task",
	 *   priority: 2,
	 *   design: "Use existing auth patterns"
	 * })
	 * ```
	 */
	readonly create: (params: {
		title: string
		type?: string
		priority?: number
		description?: string
		design?: string
		acceptance?: string
		assignee?: string
		estimate?: number
		labels?: string[]
		cwd?: string
	}) => Effect.Effect<Issue, BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Delete an issue entirely
	 *
	 * @example
	 * ```ts
	 * BeadsClient.delete("az-05y")
	 * ```
	 */
	readonly delete: (
		id: string,
		cwd?: string,
	) => Effect.Effect<void, BeadsError, CommandExecutor.CommandExecutor>

	/**
	 * Get an epic with its child tasks
	 *
	 * Fetches an epic issue and filters its dependents to return only parent-child relationships.
	 *
	 * @example
	 * ```ts
	 * BeadsClient.getEpicWithChildren("az-05y")
	 * ```
	 */
	readonly getEpicWithChildren: (
		epicId: string,
		cwd?: string,
	) => Effect.Effect<
		{ epic: Issue; children: ReadonlyArray<DependencyRef> },
		BeadsError | NotFoundError | ParseError,
		CommandExecutor.CommandExecutor
	>
}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Execute a bd command and return stdout as string
 */
const runBd = (
	args: readonly string[],
	cwd?: string,
): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		// Always add --json flag for structured output
		const allArgs = [...args, "--json"]

		const command = cwd
			? Command.make("bd", ...allArgs).pipe(Command.workingDirectory(cwd))
			: Command.make("bd", ...allArgs)

		const result = yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new BeadsError({
					message: `bd command failed: ${stderr}`,
					command: `bd ${allArgs.join(" ")}`,
					stderr,
				})
			}),
		)

		return result
	})

/**
 * Execute a bd command directly (bypasses daemon, no JSON output)
 * Used for commands like delete that aren't supported by the daemon
 */
const runBdDirect = (
	args: readonly string[],
	cwd?: string,
): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		// Add --no-daemon to bypass daemon (daemon doesn't support all operations)
		const allArgs = ["--no-daemon", ...args]

		const command = cwd
			? Command.make("bd", ...allArgs).pipe(Command.workingDirectory(cwd))
			: Command.make("bd", ...allArgs)

		const result = yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new BeadsError({
					message: `bd command failed: ${stderr}`,
					command: `bd ${allArgs.join(" ")}`,
					stderr,
				})
			}),
		)

		return result
	})

/**
 * Parse JSON output with schema validation
 */
const parseJson = <A, I, R>(
	schema: Schema.Schema<A, I, R>,
	output: string,
): Effect.Effect<A, ParseError, R> =>
	Effect.try({
		try: () => JSON.parse(output),
		catch: (error) =>
			new ParseError({
				message: `Failed to parse JSON: ${error}`,
				output,
			}),
	}).pipe(
		Effect.flatMap((json) =>
			Schema.decodeUnknown(schema)(json).pipe(
				Effect.mapError(
					(error) =>
						new ParseError({
							message: `Schema validation failed: ${error}`,
							output,
						}),
				),
			),
		),
	)

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * BeadsClient service
 *
 * Creates a service implementation that captures CommandExecutor from the scope.
 * The Layer automatically provides BunContext for command execution.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const client = yield* BeadsClient
 *   const issues = yield* client.ready()
 *   return issues
 * }).pipe(Effect.provide(BeadsClient.Default))
 * ```
 */
export class BeadsClient extends Effect.Service<BeadsClient>()("BeadsClient", {
	dependencies: [ProjectService.Default],
	effect: Effect.gen(function* () {
		const projectService = yield* ProjectService

		/**
		 * Get effective cwd for bd commands:
		 * - If explicit cwd provided, use it
		 * - Otherwise, use current project path from ProjectService
		 * - Falls back to undefined (process.cwd()) if no project selected
		 */
		const getEffectiveCwd = (explicitCwd?: string): Effect.Effect<string | undefined> =>
			explicitCwd ? Effect.succeed(explicitCwd) : projectService.getCurrentPath()

		return {
			list: (filters?: { status?: string; priority?: number; type?: string }, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const args: string[] = ["list"]

					if (filters?.status) {
						args.push("--status", filters.status)
					}
					if (filters?.priority !== undefined) {
						args.push("--priority", String(filters.priority))
					}
					if (filters?.type) {
						args.push("--type", filters.type)
					}

					const output = yield* runBd(args, effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					// Filter out tombstone (deleted) issues
					return parsed.filter((issue) => issue.status !== "tombstone") as Issue[]
				}),

			show: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["show", id], effectiveCwd)

					// bd returns an array with a single item for show command
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)

					if (parsed.length === 0) {
						return yield* Effect.fail(new NotFoundError({ issueId: id }))
					}

					const issue = parsed[0]!
					// Tombstone issues are effectively deleted
					if (issue.status === "tombstone") {
						return yield* Effect.fail(new NotFoundError({ issueId: id }))
					}

					return issue
				}),

			update: (
				id: string,
				fields: {
					status?: string
					notes?: string
					priority?: number
					title?: string
					description?: string
					design?: string
					acceptance?: string
					assignee?: string
					estimate?: number
					labels?: string[]
				},
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const args: string[] = ["update", id]

					if (fields.status) {
						args.push("--status", fields.status)
					}
					if (fields.notes) {
						args.push("--notes", fields.notes)
					}
					if (fields.priority !== undefined) {
						args.push("--priority", String(fields.priority))
					}
					if (fields.title) {
						args.push("--title", fields.title)
					}
					if (fields.description) {
						args.push("--description", fields.description)
					}
					if (fields.design) {
						args.push("--design", fields.design)
					}
					if (fields.acceptance) {
						args.push("--acceptance", fields.acceptance)
					}
					if (fields.assignee !== undefined) {
						args.push("--assignee", fields.assignee)
					}
					if (fields.estimate !== undefined) {
						args.push("--estimate", String(fields.estimate))
					}
					if (fields.labels && fields.labels.length > 0) {
						// bd update uses --set-labels for each label
						for (const label of fields.labels) {
							args.push("--set-labels", label)
						}
					}

					yield* runBd(args, effectiveCwd)
				}),

			close: (id: string, reason?: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const args: string[] = ["close", id]

					if (reason) {
						args.push("--reason", reason)
					}

					yield* runBd(args, effectiveCwd)
				}),

			sync: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["sync"], effectiveCwd)

					// Parse sync output - bd sync returns statistics
					return yield* parseJson(SyncResultSchema, output)
				}),

			/**
			 * Import-only sync - re-imports beads from JSONL into database without git operations.
			 * Use after git merge to recover any beads that might have been incorrectly
			 * removed by the bd merge driver.
			 */
			syncImportOnly: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["sync", "--import-only"], effectiveCwd)
					return yield* parseJson(SyncResultSchema, output)
				}),

			recoverTombstones: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					// Run recovery script that fixes tombstoned issues from JSONL
					// This is a workaround for bd sync bug (see az-zby)
					const scriptPath = effectiveCwd
						? `${effectiveCwd}/.beads/recover-tombstones.sh`
						: ".beads/recover-tombstones.sh"

					const command = Command.make("bash", scriptPath).pipe(
						effectiveCwd ? Command.workingDirectory(effectiveCwd) : (x) => x,
					)

					const result = yield* Command.string(command).pipe(
						Effect.mapError((error) => {
							const stderr = "stderr" in error ? String(error.stderr) : String(error)
							return new BeadsError({
								message: `Tombstone recovery failed: ${stderr}`,
								command: `bash ${scriptPath}`,
								stderr,
							})
						}),
					)

					// Parse "=== Recovered N issues ===" from output
					const match = result.match(/Recovered (\d+) issues/)
					return match ? Number.parseInt(match[1]!, 10) : 0
				}),

			ready: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["ready"], effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					// Filter out tombstone (deleted) issues
					return parsed.filter((issue) => issue.status !== "tombstone") as Issue[]
				}),

			search: (query: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["search", query], effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					// Filter out tombstone (deleted) issues
					return parsed.filter((issue) => issue.status !== "tombstone") as Issue[]
				}),

			create: (params: {
				title: string
				type?: string
				priority?: number
				description?: string
				design?: string
				acceptance?: string
				assignee?: string
				estimate?: number
				labels?: string[]
				cwd?: string
			}) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(params.cwd)
					const args: string[] = ["create", params.title]

					if (params.type) {
						args.push("--type", params.type)
					}
					if (params.priority !== undefined) {
						args.push("--priority", String(params.priority))
					}
					if (params.description) {
						args.push("--description", params.description)
					}
					if (params.design) {
						args.push("--design", params.design)
					}
					if (params.acceptance) {
						args.push("--acceptance", params.acceptance)
					}
					if (params.assignee) {
						args.push("--assignee", params.assignee)
					}
					if (params.estimate !== undefined) {
						args.push("--estimate", String(params.estimate))
					}
					if (params.labels && params.labels.length > 0) {
						// bd create uses --labels with comma-separated values
						args.push("--labels", params.labels.join(","))
					}

					const output = yield* runBd(args, effectiveCwd)

					// bd create returns a single issue object (not an array)
					return yield* parseJson(IssueSchema, output)
				}),

			delete: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					// Use runBdDirect because:
					// 1. The daemon doesn't support the delete operation
					// 2. --force is required to actually delete (not just preview)
					yield* runBdDirect(["delete", id, "--force"], effectiveCwd)
				}),

			getEpicWithChildren: (epicId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["show", epicId], effectiveCwd)

					// bd returns an array with a single item for show command
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)

					if (parsed.length === 0) {
						return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
					}

					const epic = parsed[0]!
					// Tombstone issues are effectively deleted
					if (epic.status === "tombstone") {
						return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
					}

					// Filter dependents to only include parent-child relationships
					const children =
						epic.dependents?.filter((dep) => dep.dependency_type === "parent-child") ?? []

					return { epic, children }
				}),
		}
	}),
}) {}

/**
 * Complete BeadsClient layer with all platform dependencies (legacy alias)
 *
 * @deprecated Use BeadsClient.Default instead
 */
export const BeadsClientLiveWithPlatform = BeadsClient.Default

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Get all issues matching filters
 */
export const list = (
	filters?: {
		status?: string
		priority?: number
		type?: string
	},
	cwd?: string,
): Effect.Effect<Issue[], BeadsError | ParseError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.list(filters, cwd))

/**
 * Get a single issue by ID
 */
export const show = (
	id: string,
	cwd?: string,
): Effect.Effect<
	Issue,
	BeadsError | NotFoundError | ParseError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.show(id, cwd))

/**
 * Update an issue
 */
export const update = (
	id: string,
	fields: {
		status?: string
		notes?: string
		priority?: number
		title?: string
		description?: string
		design?: string
		acceptance?: string
		assignee?: string
		estimate?: number
		labels?: string[]
	},
	cwd?: string,
): Effect.Effect<void, BeadsError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.update(id, fields, cwd))

/**
 * Close an issue
 */
export const close = (
	id: string,
	reason?: string,
	cwd?: string,
): Effect.Effect<void, BeadsError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.close(id, reason, cwd))

/**
 * Sync beads database
 */
export const sync = (
	cwd?: string,
): Effect.Effect<
	SyncResult,
	BeadsError | ParseError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.sync(cwd))

/**
 * Get ready issues
 */
export const ready = (
	cwd?: string,
): Effect.Effect<Issue[], BeadsError | ParseError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.ready(cwd))

/**
 * Search issues
 */
export const search = (
	query: string,
	cwd?: string,
): Effect.Effect<Issue[], BeadsError | ParseError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.search(query, cwd))

/**
 * Create a new issue
 */
export const create = (params: {
	title: string
	type?: string
	priority?: number
	description?: string
	design?: string
	acceptance?: string
	assignee?: string
	estimate?: number
	labels?: string[]
	cwd?: string
}): Effect.Effect<Issue, BeadsError | ParseError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.create(params))

/**
 * Delete an issue
 */
export const deleteIssue = (
	id: string,
	cwd?: string,
): Effect.Effect<void, BeadsError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.delete(id, cwd))

/**
 * Get an epic with its child tasks
 */
export const getEpicWithChildren = (
	epicId: string,
	cwd?: string,
): Effect.Effect<
	{ epic: Issue; children: ReadonlyArray<DependencyRef> },
	BeadsError | NotFoundError | ParseError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.getEpicWithChildren(epicId, cwd))
