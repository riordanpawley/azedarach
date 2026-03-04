/**
 * BeadsClient - Effect service for interacting with the bd CLI
 *
 * Wraps bd commands with Effect for type-safe, composable issue tracking operations.
 * All bd commands are executed with --json flag for structured output.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"
import * as Schema from "effect/Schema"
import { OfflineService } from "../services/OfflineService.js"
import { ProjectService } from "../services/ProjectService.js"

// ============================================================================
// Schema Definitions
// ============================================================================

export type IssueStatus = "open" | "in_progress" | "blocked" | "closed" | "tombstone"
export type IssueType = "bug" | "feature" | "task" | "epic" | "chore"
export type DependencyType = "blocks" | "related" | "parent-child" | "discovered-from"

/**
 * Dependency reference schema for issue dependencies/dependents
 *
 * Intentionally permissive for br compatibility:
 * - br can emit extra dependency types
 * - show/list can use either compact refs or full dependency links
 */
const DependencyRefSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.NullOr(Schema.String).pipe(Schema.optional),
	status: Schema.String.pipe(Schema.optional),
	dependency_type: Schema.String,
	issue_type: Schema.String.pipe(Schema.optional),
})

const DependencyLinkSchema = Schema.Struct({
	issue_id: Schema.String,
	depends_on_id: Schema.String,
	type: Schema.String,
	created_at: Schema.String.pipe(Schema.optional),
	created_by: Schema.String.pipe(Schema.optional),
})

const DependencySchema = Schema.Union(DependencyRefSchema, DependencyLinkSchema)

type DependencyRefRaw = Schema.Schema.Type<typeof DependencyRefSchema>
type DependencyLinkRaw = Schema.Schema.Type<typeof DependencyLinkSchema>
type DependencyRaw = Schema.Schema.Type<typeof DependencySchema>

export interface DependencyRef {
	readonly id: string
	readonly title?: string
	readonly status?: IssueStatus
	readonly dependency_type: DependencyType
	readonly issue_type?: IssueType
}

type DependencyLink = DependencyLinkRaw
type Dependency = DependencyRefRaw | DependencyLink

/**
 * Issue schema matching bd/br --json output
 *
 * Intentionally permissive for br compatibility:
 * - br can emit additional status / issue_type variants
 * - br can emit `estimated_minutes` instead of `estimate`
 * - some fields can be null or omitted depending on command
 */
const IssueSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	description: Schema.NullOr(Schema.String).pipe(Schema.optional),
	status: Schema.String.pipe(Schema.optional),
	priority: Schema.Number.pipe(Schema.optional),
	issue_type: Schema.String.pipe(Schema.optional),
	created_at: Schema.String,
	updated_at: Schema.String,
	closed_at: Schema.NullOr(Schema.String).pipe(Schema.optional),
	assignee: Schema.NullOr(Schema.String).pipe(Schema.optional),
	labels: Schema.Array(Schema.String).pipe(Schema.optional),
	design: Schema.NullOr(Schema.String).pipe(Schema.optional),
	notes: Schema.NullOr(Schema.String).pipe(Schema.optional),
	acceptance: Schema.NullOr(Schema.String).pipe(Schema.optional),
	acceptance_criteria: Schema.NullOr(Schema.String).pipe(Schema.optional),
	estimate: Schema.Number.pipe(Schema.optional),
	estimated_minutes: Schema.Number.pipe(Schema.optional),
	dependent_count: Schema.Number.pipe(Schema.optional),
	dependency_count: Schema.Number.pipe(Schema.optional),
	dependents: Schema.Array(DependencySchema).pipe(Schema.optional),
	dependencies: Schema.Array(DependencySchema).pipe(Schema.optional),
})

type IssueRaw = Schema.Schema.Type<typeof IssueSchema>

export interface Issue {
	readonly id: string
	readonly title: string
	readonly description?: string
	readonly status: IssueStatus
	readonly priority: number
	readonly issue_type: IssueType
	readonly created_at: string
	readonly updated_at: string
	readonly closed_at?: string | null
	readonly assignee?: string | null
	readonly labels?: readonly string[]
	readonly design?: string
	readonly notes?: string
	readonly acceptance?: string
	readonly estimate?: number
	readonly dependent_count?: number
	readonly dependency_count?: number
	readonly dependents?: readonly DependencyRef[]
	readonly dependencies?: readonly DependencyRef[]
}

export interface IssueListFilters {
	readonly status?: string
	readonly priority?: number
	readonly type?: string
}

export type IssueListSortField = "updated_at" | "created_at" | "priority" | "title"

export interface IssueListOptions {
	readonly limit?: number
	readonly pageSize?: number
	readonly includeClosed?: boolean
	readonly sortBy?: IssueListSortField
	readonly sortDirection?: "asc" | "desc"
}

const parseIssueStatus = (status: string | undefined): IssueStatus | undefined => {
	switch (status) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return status
		// br-native states: map into existing board model
		case "deferred":
			return "blocked"
		case "draft":
		case "pinned":
			return "open"
		default:
			return undefined
	}
}

const normalizeIssueStatus = (status: string | undefined): IssueStatus =>
	parseIssueStatus(status) ?? "open"

const parseIssueType = (issueType: string | undefined): IssueType | undefined => {
	switch (issueType) {
		case "bug":
		case "feature":
		case "task":
		case "epic":
		case "chore":
			return issueType
		// br-specific types that do not exist in legacy UI model
		case "docs":
		case "question":
			return "task"
		default:
			return undefined
	}
}

const normalizeIssueType = (issueType: string | undefined): IssueType =>
	parseIssueType(issueType) ?? "task"

const parseDependencyType = (dependencyType: string | undefined): DependencyType | undefined => {
	switch (dependencyType) {
		case "blocks":
		case "related":
		case "parent-child":
		case "discovered-from":
			return dependencyType
		// br dependency variants mapped to existing relationship model
		case "conditional-blocks":
		case "waits-for":
		case "caused-by":
			return "blocks"
		case "replies-to":
		case "relates-to":
		case "duplicates":
		case "supersedes":
			return "related"
		default:
			return undefined
	}
}

const normalizeDependencyType = (dependencyType: string | undefined): DependencyType =>
	parseDependencyType(dependencyType) ?? "related"

const normalizeDependencies = (
	deps: readonly DependencyRaw[] | undefined,
	kind: "dependencies" | "dependents",
	issueId: string,
): readonly DependencyRef[] | undefined =>
	deps?.map((dep) => {
		if ("id" in dep) {
			return {
				id: dep.id,
				title: dep.title ?? undefined,
				status: parseIssueStatus(dep.status),
				dependency_type: normalizeDependencyType(dep.dependency_type),
				issue_type: parseIssueType(dep.issue_type),
			}
		}

		// Link shape doesn't include display metadata, only IDs and relation type.
		// Infer counterpart ID based on whether we are normalizing dependencies or dependents.
		const relatedId =
			kind === "dependencies"
				? dep.issue_id === issueId
					? dep.depends_on_id
					: dep.issue_id
				: dep.depends_on_id === issueId
					? dep.issue_id
					: dep.depends_on_id

		return {
			id: relatedId,
			dependency_type: normalizeDependencyType(dep.type),
		}
	})

const normalizeIssue = (issue: IssueRaw): Issue => ({
	id: issue.id,
	title: issue.title,
	description: issue.description ?? undefined,
	status: normalizeIssueStatus(issue.status),
	priority: issue.priority ?? 2,
	issue_type: normalizeIssueType(issue.issue_type),
	created_at: issue.created_at,
	updated_at: issue.updated_at,
	closed_at: issue.closed_at,
	assignee: issue.assignee,
	labels: issue.labels,
	design: issue.design ?? undefined,
	notes: issue.notes ?? undefined,
	acceptance: issue.acceptance ?? issue.acceptance_criteria ?? undefined,
	estimate: issue.estimate ?? issue.estimated_minutes,
	dependent_count: issue.dependent_count,
	dependency_count: issue.dependency_count,
	dependents: normalizeDependencies(issue.dependents, "dependents", issue.id),
	dependencies: normalizeDependencies(issue.dependencies, "dependencies", issue.id),
})

const normalizeIssues = (issues: readonly IssueRaw[]): Issue[] =>
	issues.map((issue) => normalizeIssue(issue))

/**
 * Sync result schema
 *
 * Legacy bd returns:
 *   { pushed: number, pulled: number }
 *
 * br returns import/export stats:
 *   {
 *     created: number,
 *     updated: number,
 *     skipped: number,
 *     tombstone_skipped: number,
 *     orphans_removed: number,
 *     blocked_cache_rebuilt: boolean
 *   }
 */
const LegacySyncResultSchema = Schema.Struct({
	pushed: Schema.Number,
	pulled: Schema.Number,
})

const BrSyncResultSchema = Schema.Struct({
	created: Schema.Number.pipe(Schema.optional),
	updated: Schema.Number.pipe(Schema.optional),
	skipped: Schema.Number.pipe(Schema.optional),
	tombstone_skipped: Schema.Number.pipe(Schema.optional),
	orphans_removed: Schema.Number.pipe(Schema.optional),
	blocked_cache_rebuilt: Schema.Boolean.pipe(Schema.optional),
})

type LegacySyncResult = Schema.Schema.Type<typeof LegacySyncResultSchema>
type BrSyncResult = Schema.Schema.Type<typeof BrSyncResultSchema>

export interface SyncResult {
	readonly pushed: number
	readonly pulled: number
}

const normalizeLegacySyncResult = (result: LegacySyncResult): SyncResult => result

const normalizeBrSyncResult = (result: BrSyncResult): SyncResult => ({
	// br sync is local DB<->JSONL reconciliation; treat created+updated as "pulled"
	// for existing UI counters while keeping pushed at zero.
	pushed: 0,
	pulled: (result.created ?? 0) + (result.updated ?? 0),
})

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

/**
 * Error when beads database is out of sync with JSONL file.
 * This happens after git pull or when another worktree modifies issues.
 * Can be auto-recovered by running `bd sync --import-only`.
 */
export class SyncRequiredError extends Data.TaggedError("SyncRequiredError")<{
	readonly message: string
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
		filters?: IssueListFilters,
		cwd?: string,
		options?: IssueListOptions,
	) => Effect.Effect<
		Issue[],
		BeadsError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

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
		BeadsError | NotFoundError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Show details for multiple issues in a single call
	 *
	 * More efficient than calling show() multiple times when you need
	 * details for several issues at once. Returns all found issues.
	 *
	 * @example
	 * ```ts
	 * BeadsClient.showMultiple(["az-05y", "az-06z", "az-07a"])
	 * ```
	 */
	readonly showMultiple: (
		ids: readonly string[],
		cwd?: string,
	) => Effect.Effect<
		Issue[],
		BeadsError | ParseError | SyncRequiredError,
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
			type?: string
			description?: string
			design?: string
			acceptance?: string
			assignee?: string
			estimate?: number
			labels?: string[]
			parent?: string
		},
		cwd?: string,
	) => Effect.Effect<void, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor>

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
	) => Effect.Effect<void, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor>

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
	) => Effect.Effect<
		SyncResult,
		BeadsError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Import-only sync - re-imports beads from JSONL into database without git operations.
	 * Use after git merge to recover any beads incorrectly removed by the merge driver.
	 */
	readonly syncImportOnly: (
		cwd?: string,
	) => Effect.Effect<
		SyncResult,
		BeadsError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

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
	) => Effect.Effect<
		Issue[],
		BeadsError | ParseError | SyncRequiredError,
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
		cwd?: string,
	) => Effect.Effect<
		Issue[],
		BeadsError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

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
	}) => Effect.Effect<
		Issue,
		BeadsError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

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
	) => Effect.Effect<void, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Get children of an epic (issues with parent-child dependency)
	 *
	 * @example
	 * ```ts
	 * BeadsClient.getEpicChildren("az-gds")
	 * // Returns array of child issue IDs
	 * ```
	 */
	readonly getEpicChildren: (
		epicId: string,
		cwd?: string,
	) => Effect.Effect<
		DependencyRef[],
		BeadsError | NotFoundError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

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
		BeadsError | NotFoundError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Add a dependency between two issues
	 *
	 * Creates a dependency where `issueId` depends on `dependsOnId`.
	 * For epic-child relationships, use type "parent-child".
	 *
	 * @example
	 * ```ts
	 * // Make task a child of an epic
	 * BeadsClient.addDependency("az-task", "az-epic", "parent-child")
	 *
	 * // Default "blocks" dependency
	 * BeadsClient.addDependency("az-blocked", "az-blocker")
	 * ```
	 */
	readonly addDependency: (
		issueId: string,
		dependsOnId: string,
		type?: "blocks" | "related" | "parent-child" | "discovered-from",
		cwd?: string,
	) => Effect.Effect<void, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Get the parent epic of an issue, if it has one
	 *
	 * Looks for a parent-child dependency where this issue depends on an epic.
	 * Returns the parent epic issue, or undefined if no parent epic exists.
	 *
	 * @example
	 * ```ts
	 * const parentEpic = yield* BeadsClient.getParentEpic("az-task")
	 * if (parentEpic) {
	 *   console.log(`Task is child of epic: ${parentEpic.id}`)
	 * }
	 * ```
	 */
	readonly getParentEpic: (
		issueId: string,
		cwd?: string,
	) => Effect.Effect<
		Issue | undefined,
		BeadsError | NotFoundError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>
}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Check if error message indicates database sync is required
 */
const isSyncRequiredError = (message: string): boolean =>
	message.includes("Database out of sync") ||
	message.includes("Run 'bd sync --import-only'") ||
	message.includes("bd sync --import-only")

type BeadsExecutable = "bd" | "br"
type IssueDbFlavor = "bd" | "br"

interface IssueDbClient {
	readonly flavor: IssueDbFlavor
	readonly executable: BeadsExecutable
	readonly runJson: (
		args: readonly string[],
		cwd?: string,
	) => Effect.Effect<string, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor>
	readonly runDirect: (
		args: readonly string[],
		cwd?: string,
	) => Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor>
	readonly parseSyncResult: (output: string) => Effect.Effect<SyncResult, ParseError>
}

let cachedIssueDbClient: IssueDbClient | undefined

const hasNoDaemonFlag = (args: readonly string[]): boolean =>
	args.some((arg) => arg === "--no-daemon")

const DEFAULT_ISSUE_LIST_PAGE_SIZE = 200

const clampPositiveInt = (value: number, fallback: number): number => {
	if (!Number.isFinite(value)) return fallback
	const rounded = Math.floor(value)
	return rounded > 0 ? rounded : fallback
}

const isUnsupportedSortFlagError = (error: BeadsError): boolean => {
	const message = `${error.message}\n${error.stderr ?? ""}`.toLowerCase()
	const hasSortFlagReference = message.includes("--sort") || message.includes("--reverse")
	const isFlagError =
		message.includes("unexpected argument") ||
		message.includes("unknown option") ||
		message.includes("unknown argument") ||
		message.includes("unrecognized option")
	return hasSortFlagReference && isFlagError
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

const buildListCommandArgs = (
	limit: number,
	filters: IssueListFilters | undefined,
	options: IssueListOptions | undefined,
	includeSortFlags: boolean,
): string[] => {
	const args: string[] = ["list", "--limit", String(limit)]
	const includeClosed = options?.includeClosed ?? true
	if (includeClosed) {
		args.push("--all")
	}

	if (includeSortFlags) {
		const sortBy = options?.sortBy ?? "updated_at"
		const sortDirection = options?.sortDirection ?? "desc"
		args.push("--sort", sortBy)
		if (sortDirection === "desc") {
			args.push("--reverse")
		}
	}

	if (filters?.status) {
		args.push("--status", filters.status)
	}
	if (filters?.priority !== undefined) {
		args.push("--priority", String(filters.priority))
	}
	if (filters?.type) {
		args.push("--type", filters.type)
	}

	return args
}

const executeBeadsJsonCommand = (
	executable: BeadsExecutable,
	args: readonly string[],
	cwd?: string,
	retryOnEmptyOutput = true,
): Effect.Effect<string, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		// Always add --json flag for structured output
		const allArgs = [...args, "--json"]

		const command = cwd
			? Command.make(executable, ...allArgs).pipe(Command.workingDirectory(cwd))
			: Command.make(executable, ...allArgs)

		const result = yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)

				// Detect sync required errors specifically
				if (isSyncRequiredError(stderr)) {
					return new SyncRequiredError({
						message: "Beads database out of sync with JSONL. Run 'bd sync --import-only' to fix.",
					})
				}

				return new BeadsError({
					message: `beads command failed: ${stderr}`,
					command: `${executable} ${allArgs.join(" ")}`,
					stderr,
				})
			}),
		)

		// Check for empty output - daemon mode can return exit 0 with empty stdout
		const trimmed = result.trim()
		if (!trimmed || (!trimmed.startsWith("[") && !trimmed.startsWith("{"))) {
			// Output is empty or doesn't look like JSON - check if it's a sync error message
			if (isSyncRequiredError(trimmed)) {
				yield* Effect.log(`${executable} returned sync error in stdout, triggering auto-recovery`)
				return yield* Effect.fail(
					new SyncRequiredError({
						message: "Beads database out of sync with JSONL. Run 'bd sync --import-only' to fix.",
					}),
				)
			}
			// If not a sync error, fail with descriptive error
			if (!trimmed) {
				if (retryOnEmptyOutput && !hasNoDaemonFlag(args)) {
					yield* Effect.logWarning(
						`${executable} returned empty output, retrying with --no-daemon: ${executable} ${allArgs.join(" ")}`,
					)
					return yield* executeBeadsJsonCommand(
						executable,
						["--no-daemon", ...args],
						cwd,
						false,
					)
				}

				yield* Effect.logWarning(
					`${executable} command returned empty output: ${executable} ${allArgs.join(" ")}`,
				)
				return yield* Effect.fail(
					new BeadsError({
						message: "beads command returned empty output",
						command: `${executable} ${allArgs.join(" ")}`,
						stderr: "",
					}),
				)
			}
		}

		return result
	})

const executeBeadsDirectCommand = (
	executable: BeadsExecutable,
	args: readonly string[],
	cwd?: string,
): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		// Add --no-daemon to bypass daemon (daemon doesn't support all operations)
		const allArgs = ["--no-daemon", ...args]

		const command = cwd
			? Command.make(executable, ...allArgs).pipe(Command.workingDirectory(cwd))
			: Command.make(executable, ...allArgs)

		return yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new BeadsError({
					message: `beads command failed: ${stderr}`,
					command: `${executable} ${allArgs.join(" ")}`,
					stderr,
				})
			}),
		)
	})

const createBdIssueDbClient = (executable: BeadsExecutable): IssueDbClient => ({
	flavor: "bd",
	executable,
	runJson: (args, cwd) => executeBeadsJsonCommand(executable, args, cwd),
	runDirect: (args, cwd) => executeBeadsDirectCommand(executable, args, cwd),
	parseSyncResult: (output) =>
		parseJson(LegacySyncResultSchema, output).pipe(Effect.map(normalizeLegacySyncResult)),
})

const createBrIssueDbClient = (executable: BeadsExecutable): IssueDbClient => ({
	flavor: "br",
	executable,
	runJson: (args, cwd) => executeBeadsJsonCommand(executable, args, cwd),
	runDirect: (args, cwd) => executeBeadsDirectCommand(executable, args, cwd),
	parseSyncResult: (output) =>
		parseJson(BrSyncResultSchema, output).pipe(Effect.map(normalizeBrSyncResult)),
})

const detectIssueDbClient = (): Effect.Effect<
	IssueDbClient,
	BeadsError,
	CommandExecutor.CommandExecutor
> =>
	Effect.gen(function* () {
		if (cachedIssueDbClient) {
			return cachedIssueDbClient
		}

		const probeVersion = (
			executable: BeadsExecutable,
		): Effect.Effect<string | undefined, never, CommandExecutor.CommandExecutor> =>
			Command.string(Command.make(executable, "--version")).pipe(
				Effect.map((version) => version.trim()),
				Effect.catchAll(() => Effect.succeed(undefined)),
			)

		const bdVersion = yield* probeVersion("bd")
		if (bdVersion !== undefined) {
			const client = bdVersion.startsWith("br ")
				? createBrIssueDbClient("bd")
				: createBdIssueDbClient("bd")
			cachedIssueDbClient = client
			return client
		}

		const brVersion = yield* probeVersion("br")
		if (brVersion !== undefined) {
			const client = createBrIssueDbClient("br")
			cachedIssueDbClient = client
			return client
		}

		return yield* Effect.fail(
			new BeadsError({
				message: "No beads CLI found. Install either 'bd' or 'br'.",
				command: "bd --version / br --version",
			}),
		)
	})

/**
 * Execute a beads command and return stdout as string (JSON mode)
 */
const runBd = (
	args: readonly string[],
	cwd?: string,
): Effect.Effect<string, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor> =>
	Effect.flatMap(detectIssueDbClient(), (client) => client.runJson(args, cwd))

/**
 * Execute a beads command directly (bypasses daemon, no JSON output)
 * Used for commands like delete that aren't supported by the daemon
 */
const runBdDirect = (
	args: readonly string[],
	cwd?: string,
): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
	Effect.flatMap(detectIssueDbClient(), (client) => client.runDirect(args, cwd))

const parseSyncResult = (
	output: string,
): Effect.Effect<SyncResult, BeadsError | ParseError, CommandExecutor.CommandExecutor> =>
	Effect.flatMap(detectIssueDbClient(), (client) => client.parseSyncResult(output))

/**
 * Parse JSON output with schema validation
 */
const extractJsonPayload = (output: string): string => {
	const trimmed = output.trim()
	if (!trimmed) return trimmed
	if (trimmed.startsWith("{") || trimmed.startsWith("[")) return trimmed

	const firstObject = trimmed.indexOf("{")
	const firstArray = trimmed.indexOf("[")
	const startCandidates = [firstObject, firstArray].filter((idx) => idx >= 0)
	if (startCandidates.length === 0) return trimmed

	const start = Math.min(...startCandidates)
	const sliced = trimmed.slice(start)
	const lastObject = sliced.lastIndexOf("}")
	const lastArray = sliced.lastIndexOf("]")
	const end = Math.max(lastObject, lastArray)

	return end >= 0 ? sliced.slice(0, end + 1) : sliced
}

const parseJson = <A, I, R>(
	schema: Schema.Schema<A, I, R>,
	output: string,
): Effect.Effect<A, ParseError, R> =>
	Effect.try({
		try: () => JSON.parse(extractJsonPayload(output)),
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
	dependencies: [ProjectService.Default, OfflineService.Default],
	effect: Effect.gen(function* () {
		const projectService = yield* ProjectService
		const offlineService = yield* OfflineService

		/**
		 * Get effective cwd for bd commands:
		 * - If explicit cwd provided, use it
		 * - Otherwise, use current project path from ProjectService
		 * - Falls back to undefined (process.cwd()) if no project selected
		 */
		const getEffectiveCwd = (explicitCwd?: string): Effect.Effect<string | undefined> =>
			explicitCwd ? Effect.succeed(explicitCwd) : projectService.getCurrentPath()

		return {
			list: (filters?: IssueListFilters, cwd?: string, options?: IssueListOptions) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const pageSize = clampPositiveInt(
						options?.pageSize ?? DEFAULT_ISSUE_LIST_PAGE_SIZE,
						DEFAULT_ISSUE_LIST_PAGE_SIZE,
					)
					const targetLimit =
						options?.limit !== undefined
							? clampPositiveInt(options.limit, DEFAULT_ISSUE_LIST_PAGE_SIZE)
							: undefined

					let currentLimit =
						targetLimit !== undefined ? Math.min(targetLimit, pageSize) : pageSize
					let previousIssueCount = -1
					let includeSortFlags = true
					const sortBy = options?.sortBy ?? "updated_at"
					const sortDirection = options?.sortDirection ?? "desc"

					while (true) {
						const args = buildListCommandArgs(currentLimit, filters, options, includeSortFlags)
						const output = yield* runBd(args, effectiveCwd).pipe(
							Effect.catchAll((error) => {
								if (
									error._tag === "BeadsError" &&
									includeSortFlags &&
									isUnsupportedSortFlagError(error)
								) {
									includeSortFlags = false
									const fallbackArgs = buildListCommandArgs(
										currentLimit,
										filters,
										options,
										false,
									)
									return runBd(fallbackArgs, effectiveCwd)
								}

								return Effect.fail(error)
							}),
						)
						const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
						const normalized = normalizeIssues(parsed)
						const withoutTombstones = normalized.filter((issue) => issue.status !== "tombstone")
						const sorted = sortIssuesInMemory(withoutTombstones, sortBy, sortDirection)

						if (targetLimit !== undefined && sorted.length >= targetLimit) {
							return sorted.slice(0, targetLimit)
						}

						// If backend returned fewer than requested, we've exhausted available issues.
						if (sorted.length < currentLimit) {
							return sorted
						}

						// Guard against non-growing result sets to avoid infinite loops.
						if (sorted.length <= previousIssueCount) {
							return sorted
						}
						previousIssueCount = sorted.length

						const nextLimit =
							targetLimit !== undefined
								? Math.min(currentLimit + pageSize, targetLimit)
								: currentLimit + pageSize
						if (nextLimit === currentLimit) {
							return sorted
						}
						currentLimit = nextLimit
					}
				}),

			show: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["show", id], effectiveCwd)

					// bd returns an array with a single item for show command
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)

					if (normalized.length === 0) {
						return yield* Effect.fail(new NotFoundError({ issueId: id }))
					}

					const issue = normalized[0]!
					// Tombstone issues are effectively deleted
					if (issue.status === "tombstone") {
						return yield* Effect.fail(new NotFoundError({ issueId: id }))
					}

					return issue
				}),

			showMultiple: (ids: readonly string[], cwd?: string) =>
				Effect.gen(function* () {
					if (ids.length === 0) return []

					const effectiveCwd = yield* getEffectiveCwd(cwd)
					// bd show accepts multiple IDs: bd show id1 id2 id3 --json
					const output = yield* runBd(["show", ...ids], effectiveCwd)

					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)
					// Filter out tombstone (deleted) issues
					return normalized.filter((issue) => issue.status !== "tombstone")
				}),

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
					labels?: string[]
					parent?: string
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
					if (fields.type) {
						args.push("--type", fields.type)
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
					if (fields.parent !== undefined) {
						args.push("--parent", fields.parent)
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
					// Check if beads sync is enabled (config + network)
					const syncStatus = yield* offlineService.isBeadsSyncEnabled()
					if (!syncStatus.enabled) {
						// Return empty result when offline - issues are tracked locally
						return { pushed: 0, pulled: 0 }
					}

					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["sync"], effectiveCwd)
					return yield* parseSyncResult(output)
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
					return yield* parseSyncResult(output)
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
					const normalized = normalizeIssues(parsed)
					// Filter out tombstone (deleted) issues
					return normalized.filter((issue) => issue.status !== "tombstone")
				}),

			search: (query: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["search", query], effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)
					// Filter out tombstone (deleted) issues
					return normalized.filter((issue) => issue.status !== "tombstone")
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
					const parsed = yield* parseJson(IssueSchema, output)
					return normalizeIssue(parsed)
				}),

			delete: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					// Use runBdDirect because:
					// 1. The daemon doesn't support the delete operation
					// 2. --force is required to actually delete (not just preview)
					yield* runBdDirect(["delete", id, "--force"], effectiveCwd)
				}),

			getEpicChildren: (epicId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["show", epicId], effectiveCwd)

					// bd show returns an array with a single item
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)

					if (normalized.length === 0) {
						return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
					}

					const epic = normalized[0]!

					// Filter dependents to only parent-child relationships
					const children =
						epic.dependents?.filter((dep) => dep.dependency_type === "parent-child") ?? []

					return children
				}),

			getEpicWithChildren: (epicId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["show", epicId], effectiveCwd)

					// bd returns an array with a single item for show command
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)

					if (normalized.length === 0) {
						return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
					}

					const epic = normalized[0]!
					// Tombstone issues are effectively deleted
					if (epic.status === "tombstone") {
						return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
					}

					// Filter dependents to only include parent-child relationships
					const children =
						epic.dependents?.filter((dep) => dep.dependency_type === "parent-child") ?? []

					return { epic, children }
				}),

			addDependency: (
				issueId: string,
				dependsOnId: string,
				type?: "blocks" | "related" | "parent-child" | "discovered-from",
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const args: string[] = ["dep", "add", issueId, dependsOnId]

					if (type) {
						args.push("--type", type)
					}

					yield* runBd(args, effectiveCwd)
				}),

			getParentEpic: (issueId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["show", issueId], effectiveCwd)

					// bd show returns an array with a single item
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)

					if (normalized.length === 0) {
						return yield* Effect.fail(new NotFoundError({ issueId }))
					}

					const issue = normalized[0]!

					// Tombstone issues are effectively deleted
					if (issue.status === "tombstone") {
						return yield* Effect.fail(new NotFoundError({ issueId }))
					}

					// Find parent-child dependency (epic relationship)
					const parentChildDep = issue.dependencies?.find(
						(dep) => dep.dependency_type === "parent-child",
					)

					if (!parentChildDep) {
						return undefined
					}

					// Fetch the full epic issue
					const epicOutput = yield* runBd(["show", parentChildDep.id], effectiveCwd)
					const epicParsed = yield* parseJson(Schema.Array(IssueSchema), epicOutput)
					const epicNormalized = normalizeIssues(epicParsed)

					if (
						epicNormalized.length === 0 ||
						epicNormalized[0]!.status === "tombstone" ||
						epicNormalized[0]!.issue_type !== "epic"
					) {
						// Epic was deleted, treat as no parent
						return undefined
					}

					return epicNormalized[0]!
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
): Effect.Effect<
	Issue[],
	BeadsError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.list(filters, cwd))

/**
 * Get a single issue by ID
 */
export const show = (
	id: string,
	cwd?: string,
): Effect.Effect<
	Issue,
	BeadsError | NotFoundError | ParseError | SyncRequiredError,
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
): Effect.Effect<
	void,
	BeadsError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.update(id, fields, cwd))

/**
 * Close an issue
 */
export const close = (
	id: string,
	reason?: string,
	cwd?: string,
): Effect.Effect<
	void,
	BeadsError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.close(id, reason, cwd))

/**
 * Sync beads database
 */
export const sync = (
	cwd?: string,
): Effect.Effect<
	SyncResult,
	BeadsError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.sync(cwd))

/**
 * Get ready issues
 */
export const ready = (
	cwd?: string,
): Effect.Effect<
	Issue[],
	BeadsError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.ready(cwd))

/**
 * Search issues
 */
export const search = (
	query: string,
	cwd?: string,
): Effect.Effect<
	Issue[],
	BeadsError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.search(query, cwd))

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
}): Effect.Effect<
	Issue,
	BeadsError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.create(params))

/**
 * Delete an issue
 */
export const deleteIssue = (
	id: string,
	cwd?: string,
): Effect.Effect<
	void,
	BeadsError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.delete(id, cwd))

/**
 * Get an epic with its child tasks
 */
export const getEpicWithChildren = (
	epicId: string,
	cwd?: string,
): Effect.Effect<
	{ epic: Issue; children: ReadonlyArray<DependencyRef> },
	BeadsError | NotFoundError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.getEpicWithChildren(epicId, cwd))

/**
 * Add a dependency between two issues
 */
export const addDependency = (
	issueId: string,
	dependsOnId: string,
	type?: "blocks" | "related" | "parent-child" | "discovered-from",
	cwd?: string,
): Effect.Effect<
	void,
	BeadsError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.addDependency(issueId, dependsOnId, type, cwd))

/**
 * Get the parent epic of an issue, if it has one
 */
export const getParentEpic = (
	issueId: string,
	cwd?: string,
): Effect.Effect<
	Issue | undefined,
	BeadsError | NotFoundError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.getParentEpic(issueId, cwd))
