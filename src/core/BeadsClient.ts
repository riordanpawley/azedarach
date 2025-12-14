/**
 * BeadsClient - Effect service for interacting with the bd CLI
 *
 * Wraps bd commands with Effect for type-safe, composable issue tracking operations.
 * All bd commands are executed with --json flag for structured output.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { BunCommandExecutor, BunContext, BunRuntime } from "@effect/platform-bun"
import { Context, Data, Effect, Layer } from "effect"
import * as Schema from "effect/Schema"

// ============================================================================
// Schema Definitions
// ============================================================================

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
	readonly list: (filters?: {
		status?: string
		priority?: number
		type?: string
	}) => Effect.Effect<Issue[], BeadsError | ParseError, CommandExecutor.CommandExecutor>

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
	 *   notes: "Started working on this"
	 * })
	 * ```
	 */
	readonly update: (
		id: string,
		fields: {
			status?: string
			notes?: string
			priority?: number
		},
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
	 * Get ready (unblocked) issues
	 *
	 * @example
	 * ```ts
	 * BeadsClient.ready()
	 * ```
	 */
	readonly ready: () => Effect.Effect<
		Issue[],
		BeadsError | ParseError,
		CommandExecutor.CommandExecutor
	>

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
	) => Effect.Effect<Issue[], BeadsError | ParseError, CommandExecutor.CommandExecutor>

	/**
	 * Create a new issue
	 *
	 * @example
	 * ```ts
	 * BeadsClient.create({
	 *   title: "Implement feature X",
	 *   type: "task",
	 *   priority: 2
	 * })
	 * ```
	 */
	readonly create: (params: {
		title: string
		type?: string
		priority?: number
		description?: string
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
	) => Effect.Effect<void, BeadsError, CommandExecutor.CommandExecutor>
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
	dependencies: [BunContext.layer],
	effect: Effect.gen(function* () {
		return {
			list: (filters?: {
				status?: string
				priority?: number
				type?: string
			}) =>
				Effect.gen(function* () {
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

					const output = yield* runBd(args)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					// Filter out tombstone (deleted) issues
					return parsed.filter((issue) => issue.status !== "tombstone") as Issue[]
				}),

			show: (id: string) =>
				Effect.gen(function* () {
					const output = yield* runBd(["show", id])

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
				},
			) =>
				Effect.gen(function* () {
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

					yield* runBd(args)
				}),

			close: (id: string, reason?: string) =>
				Effect.gen(function* () {
					const args: string[] = ["close", id]

					if (reason) {
						args.push("--reason", reason)
					}

					yield* runBd(args)
				}),

			sync: (cwd?: string) =>
				Effect.gen(function* () {
					const output = yield* runBd(["sync"], cwd)

					// Parse sync output - bd sync returns statistics
					return yield* parseJson(SyncResultSchema, output)
				}),

			ready: () =>
				Effect.gen(function* () {
					const output = yield* runBd(["ready"])
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					// Filter out tombstone (deleted) issues
					return parsed.filter((issue) => issue.status !== "tombstone") as Issue[]
				}),

			search: (query: string) =>
				Effect.gen(function* () {
					const output = yield* runBd(["search", query])
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					// Filter out tombstone (deleted) issues
					return parsed.filter((issue) => issue.status !== "tombstone") as Issue[]
				}),

			create: (params: {
				title: string
				type?: string
				priority?: number
				description?: string
			}) =>
				Effect.gen(function* () {
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

					const output = yield* runBd(args)

					// bd create returns an array with the created issue
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)

					if (parsed.length === 0) {
						return yield* Effect.fail(
							new BeadsError({
								message: "bd create returned no issue",
								command: `bd ${args.join(" ")}`,
							}),
						)
					}

					return parsed[0]!
				}),

			delete: (id: string) =>
				Effect.gen(function* () {
					yield* runBd(["delete", id])
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
export const list = (filters?: {
	status?: string
	priority?: number
	type?: string
}): Effect.Effect<
	Issue[],
	BeadsError | ParseError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.list(filters))

/**
 * Get a single issue by ID
 */
export const show = (
	id: string,
): Effect.Effect<
	Issue,
	BeadsError | NotFoundError | ParseError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.show(id))

/**
 * Update an issue
 */
export const update = (
	id: string,
	fields: {
		status?: string
		notes?: string
		priority?: number
	},
): Effect.Effect<void, BeadsError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.update(id, fields))

/**
 * Close an issue
 */
export const close = (
	id: string,
	reason?: string,
): Effect.Effect<void, BeadsError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.close(id, reason))

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
export const ready = (): Effect.Effect<
	Issue[],
	BeadsError | ParseError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.ready())

/**
 * Search issues
 */
export const search = (
	query: string,
): Effect.Effect<Issue[], BeadsError | ParseError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.search(query))

/**
 * Create a new issue
 */
export const create = (params: {
	title: string
	type?: string
	priority?: number
	description?: string
}): Effect.Effect<Issue, BeadsError | ParseError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.create(params))

/**
 * Delete an issue
 */
export const deleteIssue = (
	id: string,
): Effect.Effect<void, BeadsError, BeadsClient | CommandExecutor.CommandExecutor> =>
	Effect.flatMap(BeadsClient, (client) => client.delete(id))
