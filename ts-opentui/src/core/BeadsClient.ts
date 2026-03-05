/**
 * BeadsClient - Effect service for interacting with the bd CLI
 *
 * Wraps bd commands with Effect for type-safe, composable issue tracking operations.
 * All bd commands are executed with --json flag for structured output.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import type { LinearClient, Issue as LinearSdkIssue } from "@linear/sdk"
import { Data, Effect, SubscriptionRef } from "effect"
import * as Schema from "effect/Schema"
import { AppConfig } from "../config/AppConfig.js"
import type { IssueDbPerfOperationKind } from "../services/DiagnosticsService.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { OfflineService } from "../services/OfflineService.js"
import { ProjectService } from "../services/ProjectService.js"
import { type IssueSyncError, IssueSyncService } from "./IssueSyncService.js"
import { LinearSdk } from "./LinearSdk.js"
import { LocalIssueStore, type LocalIssueStoreError, type SyncTarget } from "./LocalIssueStore.js"

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

const ZERO_SYNC_RESULT: SyncResult = {
	pushed: 0,
	pulled: 0,
}

const LINEAR_STATUS_CLOSED = "closed"
const LINEAR_STATUS_IN_PROGRESS = "in_progress"
const LINEAR_STATUS_BLOCKED = "blocked"
const LINEAR_DETAIL_CACHE_TTL_MS = 5 * 60 * 1000
const LINEAR_DETAIL_FETCH_LIMIT_PER_LIST = 80
const LINEAR_DETAIL_FETCH_CHUNK_SIZE = 20

const normalizeLinearStatus = (stateName: string | null | undefined): IssueStatus => {
	if (!stateName) return "open"
	const normalized = stateName.trim().toLowerCase()

	if (
		normalized.includes("done") ||
		normalized.includes("complete") ||
		normalized.includes("cancel") ||
		normalized.includes("duplicate")
	) {
		return LINEAR_STATUS_CLOSED
	}

	if (normalized.includes("block")) {
		return LINEAR_STATUS_BLOCKED
	}

	if (
		normalized.includes("progress") ||
		normalized.includes("review") ||
		normalized.includes("started")
	) {
		return LINEAR_STATUS_IN_PROGRESS
	}

	return "open"
}

const normalizeLinearPriority = (priority: number | null | undefined): number => {
	if (priority == null) return 2
	if (priority <= 0) return 2
	if (priority === 1) return 0
	if (priority === 2) return 1
	if (priority === 3) return 2
	if (priority >= 4) return 3
	return 2
}

const toLinearPriority = (priority: number | undefined): string | undefined => {
	if (priority === undefined) return undefined
	if (priority <= 0) return "1"
	if (priority === 1) return "2"
	if (priority === 2) return "3"
	if (priority >= 3) return "4"
	return "3"
}

const normalizeLinearTypeInput = (value: string | undefined): string | undefined => {
	if (!value) return undefined
	const normalized = value
		.trim()
		.toLowerCase()
		.replace(/\s*:\s*/g, ":")
	return normalized.length > 0 ? normalized : undefined
}

const parseLinearNamedType = (value: string | undefined): IssueType | undefined => {
	switch (normalizeLinearTypeInput(value)) {
		case "bug":
		case "type:bug":
			return "bug"
		case "feature":
		case "type:feature":
			return "feature"
		case "chore":
		case "type:chore":
			return "chore"
		case "epic":
		case "initiative":
		case "type:epic":
		case "type:initiative":
			return "epic"
		case "task":
		case "subtask":
		case "sub-task":
		case "type:task":
		case "type:subtask":
			return "task"
		default:
			return undefined
	}
}

export const inferLinearIssueType = (
	labels: readonly string[] | undefined,
	hasChildren: boolean,
	nativeTypeName: string | undefined,
): IssueType => {
	const explicitType = parseLinearNamedType(nativeTypeName)
	if (explicitType !== undefined) return explicitType

	const normalizedLabels = (labels ?? [])
		.map((label) => normalizeLinearTypeInput(label))
		.filter((label): label is string => label !== undefined)
	const hasLabelType = (issueType: IssueType): boolean =>
		normalizedLabels.some((label) => parseLinearNamedType(label) === issueType)

	if (hasLabelType("bug")) return "bug"
	if (hasLabelType("feature")) return "feature"
	if (hasLabelType("chore")) return "chore"
	if (hasChildren || hasLabelType("epic")) return "epic"
	if (hasLabelType("task")) return "task"

	return "task"
}

const typeToLinearLabel = (type: string | undefined): string | undefined => {
	switch (normalizeLinearTypeInput(type)) {
		case "bug":
			return "type:bug"
		case "feature":
			return "type:feature"
		case "chore":
			return "type:chore"
		case "epic":
			return "type:epic"
		case "task":
			return "type:task"
		default:
			return undefined
	}
}

const mergeLinearLabelsWithType = (
	labels: readonly string[],
	type: string | undefined,
): readonly string[] => {
	const typeLabel = typeToLinearLabel(type)
	if (!typeLabel) return labels
	const typePrefixes = ["type:bug", "type:feature", "type:chore", "type:epic", "type:task"]
	const next = labels.filter((label) => !typePrefixes.includes(label.trim().toLowerCase()))
	return [...next, typeLabel]
}

const toIsoNow = (): string => new Date().toISOString()

const LinearIssueLabelNodeSchema = Schema.Struct({
	name: Schema.NullOr(Schema.String).pipe(Schema.optional),
})

const LinearIssueTypeSchema = Schema.Union(
	Schema.String,
	Schema.Struct({
		name: Schema.NullOr(Schema.String).pipe(Schema.optional),
	}),
)

const LinearIssueSchema = Schema.Struct({
	id: Schema.String,
	identifier: Schema.NullOr(Schema.String).pipe(Schema.optional),
	title: Schema.String,
	description: Schema.NullOr(Schema.String).pipe(Schema.optional),
	priority: Schema.NullOr(Schema.Number).pipe(Schema.optional),
	createdAt: Schema.NullOr(Schema.String).pipe(Schema.optional),
	updatedAt: Schema.NullOr(Schema.String).pipe(Schema.optional),
	completedAt: Schema.NullOr(Schema.String).pipe(Schema.optional),
	canceledAt: Schema.NullOr(Schema.String).pipe(Schema.optional),
	state: Schema.NullOr(
		Schema.Struct({
			name: Schema.NullOr(Schema.String).pipe(Schema.optional),
		}),
	).pipe(Schema.optional),
	assignee: Schema.NullOr(
		Schema.Struct({
			name: Schema.NullOr(Schema.String).pipe(Schema.optional),
			email: Schema.NullOr(Schema.String).pipe(Schema.optional),
		}),
	).pipe(Schema.optional),
	labels: Schema.NullOr(
		Schema.Struct({
			nodes: Schema.NullOr(Schema.Array(Schema.NullOr(LinearIssueLabelNodeSchema))).pipe(
				Schema.optional,
			),
		}),
	).pipe(Schema.optional),
	project: Schema.NullOr(
		Schema.Struct({
			name: Schema.NullOr(Schema.String).pipe(Schema.optional),
		}),
	).pipe(Schema.optional),
	parent: Schema.NullOr(
		Schema.Struct({
			identifier: Schema.NullOr(Schema.String).pipe(Schema.optional),
			title: Schema.NullOr(Schema.String).pipe(Schema.optional),
		}),
	).pipe(Schema.optional),
	type: Schema.NullOr(LinearIssueTypeSchema).pipe(Schema.optional),
	issueType: Schema.NullOr(LinearIssueTypeSchema).pipe(Schema.optional),
	children: Schema.NullOr(
		Schema.Struct({
			nodes: Schema.NullOr(
				Schema.Array(
					Schema.NullOr(
						Schema.Struct({
							identifier: Schema.NullOr(Schema.String).pipe(Schema.optional),
							title: Schema.NullOr(Schema.String).pipe(Schema.optional),
							state: Schema.NullOr(
								Schema.Struct({
									name: Schema.NullOr(Schema.String).pipe(Schema.optional),
								}),
							).pipe(Schema.optional),
						}),
					),
				),
			).pipe(Schema.optional),
		}),
	).pipe(Schema.optional),
})

type LinearIssue = Schema.Schema.Type<typeof LinearIssueSchema>

const extractLinearIssueTypeName = (
	typeValue: Schema.Schema.Type<typeof LinearIssueTypeSchema> | null | undefined,
): string | undefined => {
	if (typeValue == null) return undefined
	return typeof typeValue === "string" ? typeValue : (typeValue.name ?? undefined)
}

const normalizeLinearIssue = (issue: LinearIssue): IssueRaw => {
	const labels = (issue.labels?.nodes ?? [])
		.map((label) => label?.name)
		.filter((value): value is string => value != null)

	const children = issue.children?.nodes ?? []
	const hasChildren = children.length > 0
	const nativeTypeName =
		extractLinearIssueTypeName(issue.issueType) ?? extractLinearIssueTypeName(issue.type)
	const status = normalizeLinearStatus(issue.state?.name)
	const identifier = issue.identifier ?? issue.id
	const createdAt = issue.createdAt ?? issue.updatedAt ?? toIsoNow()
	const updatedAt = issue.updatedAt ?? issue.createdAt ?? toIsoNow()
	const closedAt =
		status === "closed" ? (issue.completedAt ?? issue.canceledAt ?? updatedAt) : undefined

	const dependencies: DependencyRaw[] =
		issue.parent?.identifier != null
			? [
					{
						id: issue.parent.identifier,
						title: issue.parent.title ?? null,
						dependency_type: "parent-child",
						issue_type: "epic",
					},
				]
			: []

	const dependents: DependencyRaw[] = children
		.filter((child) => child?.identifier != null)
		.map((child) => {
			const identifier = child?.identifier ?? ""
			return {
				id: identifier,
				title: child?.title ?? null,
				dependency_type: "parent-child",
				issue_type: "task",
				status: normalizeLinearStatus(child?.state?.name),
			}
		})

	return {
		id: identifier,
		title: issue.title,
		description: issue.description ?? undefined,
		status,
		priority: normalizeLinearPriority(issue.priority),
		issue_type: inferLinearIssueType(labels, hasChildren, nativeTypeName),
		created_at: createdAt,
		updated_at: updatedAt,
		closed_at: closedAt,
		assignee: issue.assignee?.name ?? issue.assignee?.email ?? undefined,
		labels,
		dependency_count: dependencies.length,
		dependent_count: dependents.length,
		dependencies,
		dependents,
	}
}

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
		parent?: string
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
type IssueDbFlavor = "bd" | "br" | "linear"
type ConfiguredIssueBackend = "bd" | "br" | "local" | "linear"
type LocalFirstIssueBackend = "local" | "linear"

export interface IssueTrackerBackendConfigShape {
	readonly beads?: unknown
	readonly beads_rust?: unknown
	readonly linear?: unknown
	readonly local?: unknown
}

export const resolveConfiguredIssueBackend = (
	issueTracker: IssueTrackerBackendConfigShape,
): ConfiguredIssueBackend => {
	if (issueTracker.beads !== undefined) return "bd"
	if (issueTracker.beads_rust !== undefined) return "br"
	if (issueTracker.linear !== undefined) return "linear"
	return "local"
}

export const isLocalFirstIssueBackend = (
	backend: ConfiguredIssueBackend,
): backend is LocalFirstIssueBackend => backend === "local" || backend === "linear"

export const getSyncTargetForBackend = (backend: ConfiguredIssueBackend): SyncTarget | undefined =>
	backend === "linear" ? "linear" : undefined

interface IssueDbClient {
	readonly flavor: IssueDbFlavor
	readonly executable: string
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

interface IssueDbTimingRecorder {
	readonly recordIssueDbTiming: (options: {
		backend: "linear"
		operation: string
		kind: IssueDbPerfOperationKind
		durationMs: number
		success: boolean
	}) => Effect.Effect<void>
}

export interface LinearCommandPerfMetadata {
	readonly operation: string
	readonly kind: IssueDbPerfOperationKind
}

export const getLinearCommandPerfMetadata = (
	linearArgs: readonly string[],
): LinearCommandPerfMetadata => {
	if (linearArgs[0] !== "i") {
		return { operation: "other", kind: "write" }
	}

	switch (linearArgs[1]) {
		case "list":
			return { operation: "i.list", kind: "read" }
		case "get":
			return { operation: "i.get", kind: "read" }
		case "create":
			return { operation: "i.create", kind: "write" }
		case "update":
			return { operation: "i.update", kind: "write" }
		case "close":
			return { operation: "i.close", kind: "write" }
		case "start":
			return { operation: "i.start", kind: "write" }
		case "stop":
			return { operation: "i.stop", kind: "write" }
		default:
			return { operation: "other", kind: "write" }
	}
}

export const withIssueDbTiming = <A, E, R>(
	options: {
		readonly recorder: IssueDbTimingRecorder
		readonly backend: "linear"
		readonly operation: string
		readonly kind: IssueDbPerfOperationKind
	},
	effect: Effect.Effect<A, E, R>,
): Effect.Effect<A, E, R> =>
	Effect.gen(function* () {
		const startTimeMs = Date.now()
		return yield* effect.pipe(
			Effect.tap(() =>
				options.recorder.recordIssueDbTiming({
					backend: options.backend,
					operation: options.operation,
					kind: options.kind,
					durationMs: Date.now() - startTimeMs,
					success: true,
				}),
			),
			Effect.tapError(() =>
				options.recorder.recordIssueDbTiming({
					backend: options.backend,
					operation: options.operation,
					kind: options.kind,
					durationMs: Date.now() - startTimeMs,
					success: false,
				}),
			),
		)
	})

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

/**
 * Parse JSON output with schema validation
 */
const parseUnknownJsonEither = Schema.decodeUnknownEither(Schema.parseJson(Schema.Unknown))

const isParseableJsonPayload = (value: string): boolean =>
	parseUnknownJsonEither(value)._tag === "Right"

export const extractJsonPayload = (output: string): string => {
	const trimmed = output.trim()
	if (!trimmed) return trimmed
	if (isParseableJsonPayload(trimmed)) {
		return trimmed
	}

	for (let index = 0; index < trimmed.length; index += 1) {
		const char = trimmed[index]
		if (char !== "{" && char !== "[") continue

		const sliced = trimmed.slice(index)
		if (isParseableJsonPayload(sliced)) {
			return sliced
		}

		const lastObject = sliced.lastIndexOf("}")
		const lastArray = sliced.lastIndexOf("]")
		const end = Math.max(lastObject, lastArray)
		if (end >= 0) {
			const candidate = sliced.slice(0, end + 1)
			if (isParseableJsonPayload(candidate)) {
				return candidate
			}
		}
	}

	return trimmed
}

const parseJson = <A, I, R>(
	schema: Schema.Schema<A, I, R>,
	output: string,
): Effect.Effect<A, ParseError, R> => {
	const parseUnknown = Schema.decode(Schema.parseJson(Schema.Unknown))
	return parseUnknown(extractJsonPayload(output)).pipe(
		Effect.mapError(
			(error) =>
				new ParseError({
					message: `Failed to parse JSON: ${String(error)}`,
					output,
				}),
		),
		Effect.flatMap((json) =>
			Schema.decodeUnknown(schema)(json).pipe(
				Effect.mapError(
					(error) =>
						new ParseError({
							message: `Schema validation failed: ${String(error)}`,
							output,
						}),
				),
			),
		),
	)
}

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
	dependencies: [
		ProjectService.Default,
		OfflineService.Default,
		AppConfig.Default,
		DiagnosticsService.Default,
		LocalIssueStore.Default,
		IssueSyncService.Default,
		LinearSdk.Default,
	],
	effect: Effect.gen(function* () {
		const projectService = yield* ProjectService
		const offlineService = yield* OfflineService
		const appConfig = yield* AppConfig
		const diagnostics = yield* DiagnosticsService
		const localIssueStore = yield* LocalIssueStore
		const issueSyncService = yield* IssueSyncService
		const linearSdk = yield* LinearSdk

		/**
		 * Get effective cwd for bd commands:
		 * - If explicit cwd provided, use it
		 * - Otherwise, use current project path from ProjectService
		 * - Falls back to undefined (process.cwd()) if no project selected
		 */
		const getEffectiveCwd = (explicitCwd?: string): Effect.Effect<string | undefined> =>
			explicitCwd ? Effect.succeed(explicitCwd) : projectService.getCurrentPath()

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
								message:
									"Beads database out of sync with JSONL. Run 'bd sync --import-only' to fix.",
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
						yield* Effect.log(
							`${executable} returned sync error in stdout, triggering auto-recovery`,
						)
						return yield* Effect.fail(
							new SyncRequiredError({
								message:
									"Beads database out of sync with JSONL. Run 'bd sync --import-only' to fix.",
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
			runJson: (args, runCwd) => executeBeadsJsonCommand(executable, args, runCwd),
			runDirect: (args, runCwd) => executeBeadsDirectCommand(executable, args, runCwd),
			parseSyncResult: (output) =>
				parseJson(LegacySyncResultSchema, output).pipe(Effect.map(normalizeLegacySyncResult)),
		})

		const createBrIssueDbClient = (executable: BeadsExecutable): IssueDbClient => ({
			flavor: "br",
			executable,
			runJson: (args, runCwd) => executeBeadsJsonCommand(executable, args, runCwd),
			runDirect: (args, runCwd) => executeBeadsDirectCommand(executable, args, runCwd),
			parseSyncResult: (output) =>
				parseJson(BrSyncResultSchema, output).pipe(Effect.map(normalizeBrSyncResult)),
		})

		interface LinearRuntimeConfig {
			readonly linearClient: LinearClient
			readonly defaultTeam?: string
		}

		const createLinearIssueDbClient = (config: LinearRuntimeConfig): IssueDbClient => {
			const linearClient = config.linearClient

			const withLinearSdkTiming = <A>(
				linearArgs: readonly string[],
				effect: Effect.Effect<A, BeadsError, CommandExecutor.CommandExecutor>,
			): Effect.Effect<A, BeadsError, CommandExecutor.CommandExecutor> => {
				const perf = getLinearCommandPerfMetadata(linearArgs)
				return withIssueDbTiming(
					{
						recorder: diagnostics,
						backend: "linear",
						operation: perf.operation,
						kind: perf.kind,
					},
					effect,
				)
			}

			const toIso = (value: Date | null | undefined): string | undefined =>
				value ? value.toISOString() : undefined

			const parseArgumentValue = (args: readonly string[], flag: string): string | undefined => {
				const index = args.indexOf(flag)
				if (index === -1) return undefined
				return args[index + 1]
			}

			const parseRepeatedArgumentValues = (args: readonly string[], flag: string): string[] => {
				const values: string[] = []
				for (let index = 0; index < args.length; index += 1) {
					const value = args[index + 1]
					if (args[index] === flag && value !== undefined) {
						values.push(value)
					}
				}
				return values
			}

			const fetchAllIssues = (): Effect.Effect<
				readonly LinearSdkIssue[],
				BeadsError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const fetchIssuesPage = (
						afterCursor: string | null | undefined,
					): Effect.Effect<
						{
							readonly nodes: readonly LinearSdkIssue[]
							readonly pageInfo: {
								readonly hasNextPage: boolean
								readonly endCursor?: string | null
							}
						},
						BeadsError,
						CommandExecutor.CommandExecutor
					> =>
						Effect.tryPromise({
							try: () =>
								linearClient.issues({
									first: 250,
									after: afterCursor,
								}),
							catch: (error) =>
								new BeadsError({
									message:
										error instanceof Error
											? error.message
											: `Failed to fetch Linear issues: ${String(error)}`,
									command: "linear-sdk issues",
								}),
						}).pipe(linearSdk.rateLimit)

					const collectPages = (
						afterCursor: string | null | undefined,
						accumulator: readonly LinearSdkIssue[],
					): Effect.Effect<
						readonly LinearSdkIssue[],
						BeadsError,
						CommandExecutor.CommandExecutor
					> =>
						fetchIssuesPage(afterCursor).pipe(
							Effect.flatMap((page) => {
								const nextAccumulator = [...accumulator, ...page.nodes]
								if (!page.pageInfo.hasNextPage || !page.pageInfo.endCursor) {
									return Effect.succeed(nextAccumulator)
								}
								return collectPages(page.pageInfo.endCursor, nextAccumulator)
							}),
						)

					return yield* collectPages(undefined, [])
				})

			const fetchStateNameById = (): Effect.Effect<
				ReadonlyMap<string, string>,
				BeadsError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.tryPromise({
					try: async () => {
						const states = await linearClient.workflowStates({ first: 500 })
						return new Map(states.nodes.map((state) => [state.id, state.name] as const))
					},
					catch: (error) =>
						new BeadsError({
							message:
								error instanceof Error
									? error.message
									: `Failed to fetch Linear workflow states: ${String(error)}`,
							command: "linear-sdk workflowStates",
						}),
				}).pipe(linearSdk.rateLimit)

			const fetchWorkflowStates = (): Effect.Effect<
				readonly {
					readonly id: string
					readonly teamId?: string
					readonly name: string
					readonly type: string
				}[],
				BeadsError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.tryPromise({
					try: async () => {
						const states = await linearClient.workflowStates({ first: 500 })
						return states.nodes.map((state) => ({
							id: state.id,
							teamId: state.teamId,
							name: state.name,
							type: state.type,
						}))
					},
					catch: (error) =>
						new BeadsError({
							message:
								error instanceof Error
									? error.message
									: `Failed to fetch Linear workflow states: ${String(error)}`,
							command: "linear-sdk workflowStates",
						}),
				}).pipe(linearSdk.rateLimit)

			const fetchLabelNameById = (): Effect.Effect<
				ReadonlyMap<string, string>,
				BeadsError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.tryPromise({
					try: async () => {
						const labels = await linearClient.issueLabels({ first: 500 })
						return new Map(labels.nodes.map((label) => [label.id, label.name] as const))
					},
					catch: (error) =>
						new BeadsError({
							message:
								error instanceof Error
									? error.message
									: `Failed to fetch Linear labels: ${String(error)}`,
							command: "linear-sdk issueLabels",
						}),
				}).pipe(linearSdk.rateLimit)

			const fetchUsers = (): Effect.Effect<
				readonly { readonly id: string; readonly name: string; readonly email: string }[],
				BeadsError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.tryPromise({
					try: async () => {
						const users = await linearClient.users({ first: 250 })
						return users.nodes.map((user) => ({
							id: user.id,
							name: user.name ?? "",
							email: user.email ?? "",
						}))
					},
					catch: (error) =>
						new BeadsError({
							message:
								error instanceof Error
									? error.message
									: `Failed to fetch Linear users: ${String(error)}`,
							command: "linear-sdk users",
						}),
				}).pipe(linearSdk.rateLimit)

			const resolveTeamId = (
				reference: string,
			): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
				linearSdk.resolveTeamId(linearClient, reference).pipe(
					Effect.mapError(
						(error) =>
							new BeadsError({
								message: error.message,
								command: "linear-sdk team",
							}),
					),
				)

			const resolveAssigneeId = (
				reference: string,
			): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const trimmed = reference.trim()
					if (trimmed.length === 0) {
						return yield* Effect.fail(
							new BeadsError({
								message: "Assignee reference cannot be empty",
								command: "linear-sdk users",
							}),
						)
					}
					if (
						/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(
							trimmed,
						)
					) {
						return trimmed
					}
					const users = yield* fetchUsers()
					const normalizedReference = trimmed.toLowerCase()
					const matched = users.find((user) => {
						const byName = user.name.trim().toLowerCase() === normalizedReference
						const byEmail = user.email.trim().toLowerCase() === normalizedReference
						return byName || byEmail
					})
					if (!matched) {
						return yield* Effect.fail(
							new BeadsError({
								message: `Could not resolve Linear assignee '${reference}'`,
								command: "linear-sdk users",
							}),
						)
					}
					return matched.id
				})

			const normalizeLinearSdkIssues = (
				issues: readonly LinearSdkIssue[],
				stateNameById: ReadonlyMap<string, string>,
				labelNameById: ReadonlyMap<string, string>,
			): readonly IssueRaw[] => {
				const issuesByLinearId = new Map(issues.map((issue) => [issue.id, issue]))
				const childCountByParentId = new Map<string, number>()
				const childrenByParentId = new Map<string, LinearSdkIssue[]>()

				for (const issue of issues) {
					const parentId = issue.parentId
					if (!parentId) continue
					childCountByParentId.set(parentId, (childCountByParentId.get(parentId) ?? 0) + 1)
					const parentChildren = childrenByParentId.get(parentId) ?? []
					parentChildren.push(issue)
					childrenByParentId.set(parentId, parentChildren)
				}

				return issues.map((issue) => {
					const labels = issue.labelIds
						.map((labelId) => labelNameById.get(labelId))
						.filter((label): label is string => label !== undefined)

					const hasChildren = (childCountByParentId.get(issue.id) ?? 0) > 0
					const stateName = issue.stateId ? stateNameById.get(issue.stateId) : undefined
					const status = normalizeLinearStatus(stateName)
					const parent = issue.parentId ? issuesByLinearId.get(issue.parentId) : undefined
					const parentIdentifier = parent?.identifier
					const children = childrenByParentId.get(issue.id) ?? []

					const dependencies: DependencyRaw[] =
						parentIdentifier !== undefined
							? [
									{
										id: parentIdentifier,
										title: parent?.title,
										dependency_type: "parent-child",
										issue_type: "epic",
									},
								]
							: []

					const dependents: DependencyRaw[] = children.map((child) => ({
						id: child.identifier,
						title: child.title,
						dependency_type: "parent-child",
						issue_type: "task",
						status: normalizeLinearStatus(
							child.stateId ? stateNameById.get(child.stateId) : undefined,
						),
					}))

					return {
						id: issue.identifier,
						title: issue.title,
						description: issue.description ?? undefined,
						status,
						priority: normalizeLinearPriority(issue.priority),
						issue_type: inferLinearIssueType(labels, hasChildren, undefined),
						created_at: issue.createdAt.toISOString(),
						updated_at: issue.updatedAt.toISOString(),
						closed_at:
							status === "closed" ? toIso(issue.completedAt ?? issue.canceledAt) : undefined,
						assignee: issue.assigneeId ?? undefined,
						labels,
						dependency_count: dependencies.length,
						dependent_count: dependents.length,
						dependencies,
						dependents,
					}
				})
			}

			interface LinearIssueSnapshot {
				readonly issues: readonly IssueRaw[]
				readonly linearIdByIdentifier: ReadonlyMap<string, string>
				readonly teamIdByIdentifier: ReadonlyMap<string, string | undefined>
			}

			const buildLinearIssueSnapshot = (): Effect.Effect<
				LinearIssueSnapshot,
				BeadsError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const [linearIssues, stateNameById, labelNameById] = yield* Effect.all([
						fetchAllIssues(),
						fetchStateNameById(),
						fetchLabelNameById(),
					])
					const normalizedIssues = normalizeLinearSdkIssues(
						linearIssues,
						stateNameById,
						labelNameById,
					)
					return {
						issues: normalizedIssues,
						linearIdByIdentifier: new Map(
							linearIssues.map((issue) => [issue.identifier, issue.id] as const),
						),
						teamIdByIdentifier: new Map(
							linearIssues.map((issue) => [issue.identifier, issue.teamId] as const),
						),
					}
				})

			const resolveLinearIssueId = (
				identifier: string,
			): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const snapshot = yield* buildLinearIssueSnapshot()
					const linearId = snapshot.linearIdByIdentifier.get(identifier)
					if (!linearId) {
						return yield* Effect.fail(
							new BeadsError({
								message: `Could not resolve Linear issue id: ${identifier}`,
								command: "linear-sdk issue",
							}),
						)
					}
					return linearId
				})

			const findTeamStateIdForStatus = (
				teamId: string,
				targetStatus: IssueStatus,
			): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const workflowStates = yield* fetchWorkflowStates()
					const teamStates = workflowStates.filter((state) => state.teamId === teamId)
					if (teamStates.length === 0) {
						return yield* Effect.fail(
							new BeadsError({
								message: `No workflow states available for team ${teamId}`,
								command: "linear-sdk workflowStates",
							}),
						)
					}

					const findByType = (types: readonly string[]) =>
						teamStates.find((state) => types.includes(state.type))
					const findByName = (needle: string) =>
						teamStates.find((state) => state.name.toLowerCase().includes(needle))

					switch (targetStatus) {
						case "closed":
							return (findByType(["completed"]) ?? findByType(["canceled"]) ?? teamStates[0]).id
						case "in_progress":
							return (
								findByType(["started"]) ??
								findByName("progress") ??
								findByName("review") ??
								teamStates[0]
							).id
						case "blocked":
							return (findByName("block") ?? findByType(["started"]) ?? teamStates[0]).id
						case "open":
						case "tombstone":
							return (findByType(["unstarted", "backlog", "triage"]) ?? teamStates[0]).id
					}
				})

			const resolveLabelIds = (
				labelNames: readonly string[],
			): Effect.Effect<readonly string[], BeadsError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const labelMap = yield* fetchLabelNameById()
					const ids: string[] = []
					for (const name of labelNames) {
						const normalized = name.trim().toLowerCase()
						const match = [...labelMap.entries()].find(
							([, labelName]) => labelName.trim().toLowerCase() === normalized,
						)
						if (match) {
							ids.push(match[0])
						}
					}
					return ids
				})

			const toLinearPriorityValue = (priority: number | undefined): number | undefined => {
				const mapped = toLinearPriority(priority)
				if (mapped === undefined) return undefined
				const parsed = Number.parseInt(mapped, 10)
				return Number.isNaN(parsed) ? undefined : parsed
			}

			const runJson = (
				args: readonly string[],
				_runCwd?: string,
			): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const [command, ...rest] = args
					if (!command) {
						return "{}"
					}

					switch (command) {
						case "list": {
							const limitValue = parseArgumentValue(rest, "--limit")
							const parsedLimit = limitValue ? Number.parseInt(limitValue, 10) : undefined
							const statusFilter = parseArgumentValue(rest, "--status")
							const priorityFilterValue = parseArgumentValue(rest, "--priority")
							const priorityFilter = priorityFilterValue
								? Number.parseInt(priorityFilterValue, 10)
								: undefined
							const typeFilter = parseArgumentValue(rest, "--type")
							const snapshot = yield* withLinearSdkTiming(["i", "list"], buildLinearIssueSnapshot())
							const filtered = snapshot.issues.filter((issue) => {
								if (statusFilter !== undefined && issue.status !== statusFilter) return false
								if (priorityFilter !== undefined && issue.priority !== priorityFilter) return false
								if (typeFilter !== undefined && issue.issue_type !== typeFilter) return false
								return true
							})
							const limited =
								parsedLimit !== undefined && parsedLimit > 0
									? filtered.slice(0, parsedLimit)
									: filtered
							return JSON.stringify(limited)
						}

						case "show": {
							if (rest.length === 0) return JSON.stringify([])
							const snapshot = yield* withLinearSdkTiming(
								["i", "get", ...rest],
								buildLinearIssueSnapshot(),
							)
							const requested = new Set(rest)
							const matches = snapshot.issues.filter((issue) => requested.has(issue.id))
							return JSON.stringify(matches)
						}

						case "ready": {
							const snapshot = yield* withLinearSdkTiming(["i", "list"], buildLinearIssueSnapshot())
							const filtered = snapshot.issues.filter(
								(issue) => issue.status === "open" || issue.status === "in_progress",
							)
							return JSON.stringify(filtered)
						}

						case "search": {
							const query = rest[0]?.trim().toLowerCase() ?? ""
							const snapshot = yield* withLinearSdkTiming(["i", "list"], buildLinearIssueSnapshot())
							const filtered = snapshot.issues.filter((issue) => {
								const haystack =
									`${issue.id} ${issue.title} ${issue.description ?? ""}`.toLowerCase()
								return haystack.includes(query)
							})
							return JSON.stringify(filtered)
						}

						case "create": {
							const title = rest[0]
							if (!title) {
								return yield* Effect.fail(
									new BeadsError({
										message: "Create requires issue title",
										command: "linear-sdk createIssue",
									}),
								)
							}

							const configuredTeam = config.defaultTeam?.trim()
							if (!configuredTeam) {
								return yield* Effect.fail(
									new BeadsError({
										message: "Linear create requires issueTracker.linear.team in config.",
										command: "linear-sdk createIssue",
									}),
								)
							}
							const teamId = yield* resolveTeamId(configuredTeam)

						const description = parseArgumentValue(rest, "--description")
						const design = parseArgumentValue(rest, "--design")
						const acceptance = parseArgumentValue(rest, "--acceptance")
						const type = parseArgumentValue(rest, "--type")
						const assignee = parseArgumentValue(rest, "--assignee")
						const estimate = parseArgumentValue(rest, "--estimate")
						const parent = parseArgumentValue(rest, "--parent")
						const priorityArg = parseArgumentValue(rest, "--priority")
						const priority = toLinearPriorityValue(
							priorityArg ? Number.parseInt(priorityArg, 10) : undefined,
						)
							const labelArgs = parseRepeatedArgumentValues(rest, "--labels")
							const labels = labelArgs
								.flatMap((value) => value.split(","))
								.map((value) => value.trim())
								.filter((value) => value.length > 0)
							const labelsWithType = mergeLinearLabelsWithType(labels, type)
							const labelIds = yield* resolveLabelIds(labelsWithType)
							const assigneeId =
								assignee !== undefined && assignee.trim().length > 0
									? yield* resolveAssigneeId(assignee)
									: undefined
							const parsedEstimate =
								estimate !== undefined ? Number.parseInt(estimate, 10) : undefined
						const estimateValue =
							parsedEstimate !== undefined && !Number.isNaN(parsedEstimate)
								? parsedEstimate
								: undefined
						const parentId =
							parent !== undefined && parent.trim().length > 0
								? yield* resolveLinearIssueId(parent)
								: undefined

						const extraSections: string[] = []
						if (design) extraSections.push(`## Design\n${design}`)
						if (acceptance) extraSections.push(`## Acceptance\n${acceptance}`)
							const mergedDescription = [description, ...extraSections]
								.filter((value): value is string => value !== undefined && value.length > 0)
								.join("\n\n")

							const createdPayload = yield* withLinearSdkTiming(
								["i", "create"],
								Effect.tryPromise({
									try: () =>
										linearClient.createIssue({
											teamId,
											title,
											description: mergedDescription.length > 0 ? mergedDescription : undefined,
											priority,
											assigneeId,
											estimate: estimateValue,
											parentId,
											labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
										}),
									catch: (error) =>
										new BeadsError({
											message: error instanceof Error ? error.message : String(error),
											command: "linear-sdk createIssue",
										}),
								}).pipe(linearSdk.rateLimit),
							)
							const createdIssueId = createdPayload.issueId
							if (!createdIssueId) {
								return yield* Effect.fail(
									new BeadsError({
										message: "Linear create returned no issue",
										command: "linear-sdk createIssue",
									}),
								)
							}
							const createdLinearIssue = yield* withLinearSdkTiming(
								["i", "get", createdIssueId],
								Effect.tryPromise({
									try: () => linearClient.issue(createdIssueId),
									catch: (error) =>
										new BeadsError({
											message: error instanceof Error ? error.message : String(error),
											command: "linear-sdk issue",
										}),
								}).pipe(linearSdk.rateLimit),
							)
							const snapshot = yield* buildLinearIssueSnapshot()
							const created = snapshot.issues.find(
								(issue) => issue.id === createdLinearIssue.identifier,
							)
							if (!created) {
								return JSON.stringify({
									id: createdLinearIssue.identifier,
									title: createdLinearIssue.title,
									status: "open",
									priority: normalizeLinearPriority(createdLinearIssue.priority),
									issue_type: inferLinearIssueType([], false, undefined),
									created_at: createdLinearIssue.createdAt.toISOString(),
									updated_at: createdLinearIssue.updatedAt.toISOString(),
								} satisfies IssueRaw)
							}
							return JSON.stringify(created)
						}

						case "update": {
							const issueIdentifier = rest[0]
							if (!issueIdentifier) {
								return yield* Effect.fail(
									new BeadsError({
										message: "Update requires issue id",
										command: "linear-sdk updateIssue",
									}),
								)
							}

							const snapshot = yield* buildLinearIssueSnapshot()
							const linearId = snapshot.linearIdByIdentifier.get(issueIdentifier)
							if (!linearId) {
								return yield* Effect.fail(
									new BeadsError({
										message: `Issue not found: ${issueIdentifier}`,
										command: "linear-sdk updateIssue",
									}),
								)
							}

							const teamId = snapshot.teamIdByIdentifier.get(issueIdentifier)
							const status = parseArgumentValue(rest, "--status")
							const title = parseArgumentValue(rest, "--title")
							const description = parseArgumentValue(rest, "--description")
							const notes = parseArgumentValue(rest, "--notes")
							const design = parseArgumentValue(rest, "--design")
							const acceptance = parseArgumentValue(rest, "--acceptance")
							const type = parseArgumentValue(rest, "--type")
							const assignee = parseArgumentValue(rest, "--assignee")
							const estimate = parseArgumentValue(rest, "--estimate")
							const priorityArg = parseArgumentValue(rest, "--priority")
							const parent = parseArgumentValue(rest, "--parent")
							const labels = parseRepeatedArgumentValues(rest, "--set-labels")
							const labelsWithType = mergeLinearLabelsWithType(labels, type)
							const labelIds = yield* resolveLabelIds(labelsWithType)

							const extraSections: string[] = []
							if (notes) extraSections.push(`## Notes\n${notes}`)
							if (design) extraSections.push(`## Design\n${design}`)
							if (acceptance) extraSections.push(`## Acceptance\n${acceptance}`)
							const mergedDescription = [description, ...extraSections]
								.filter((value): value is string => value !== undefined && value.length > 0)
								.join("\n\n")

							const priority = toLinearPriorityValue(
								priorityArg ? Number.parseInt(priorityArg, 10) : undefined,
							)
							const assigneeId =
								assignee !== undefined && assignee.trim().length > 0
									? yield* resolveAssigneeId(assignee)
									: assignee === undefined
										? undefined
										: null
							const parsedEstimate =
								estimate !== undefined ? Number.parseInt(estimate, 10) : undefined
							const estimateValue =
								parsedEstimate !== undefined && !Number.isNaN(parsedEstimate)
									? parsedEstimate
									: undefined
							const parentId =
								parent !== undefined && parent.trim().length > 0
									? yield* resolveLinearIssueId(parent)
									: parent !== undefined
										? null
										: undefined
							const parsedStatus = parseIssueStatus(status)
							const stateId =
								parsedStatus !== undefined && teamId !== undefined
									? yield* findTeamStateIdForStatus(teamId, parsedStatus)
									: undefined

							yield* withLinearSdkTiming(
								["i", "update"],
								Effect.tryPromise({
									try: () =>
										linearClient.updateIssue(linearId, {
											title,
											description: mergedDescription.length > 0 ? mergedDescription : undefined,
											priority,
											assigneeId,
											estimate: estimateValue,
											labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
											parentId,
											stateId,
										}),
									catch: (error) =>
										new BeadsError({
											message: error instanceof Error ? error.message : String(error),
											command: "linear-sdk updateIssue",
										}),
								}).pipe(linearSdk.rateLimit),
							)
							return "{}"
						}

						case "close": {
							const issueIdentifier = rest[0]
							if (!issueIdentifier) return "{}"
							const snapshot = yield* buildLinearIssueSnapshot()
							const linearId = snapshot.linearIdByIdentifier.get(issueIdentifier)
							const teamId = snapshot.teamIdByIdentifier.get(issueIdentifier)
							if (!linearId || !teamId) {
								return yield* Effect.fail(
									new BeadsError({
										message: `Issue not found: ${issueIdentifier}`,
										command: "linear-sdk closeIssue",
									}),
								)
							}
							const closedStateId = yield* findTeamStateIdForStatus(teamId, "closed")
							yield* withLinearSdkTiming(
								["i", "close"],
								Effect.tryPromise({
									try: () => linearClient.updateIssue(linearId, { stateId: closedStateId }),
									catch: (error) =>
										new BeadsError({
											message: error instanceof Error ? error.message : String(error),
											command: "linear-sdk closeIssue",
										}),
								}).pipe(linearSdk.rateLimit),
							)
							return "{}"
						}

						case "sync":
							return JSON.stringify(ZERO_SYNC_RESULT)

						case "dep": {
							if (rest[0] !== "add") {
								return yield* Effect.fail(
									new BeadsError({
										message: "Only dependency add is supported",
										command: "linear-sdk dep add",
									}),
								)
							}
							const issueIdentifier = rest[1]
							const dependsOnIdentifier = rest[2]
							const depType = parseArgumentValue(rest, "--type")
							if (!issueIdentifier || !dependsOnIdentifier) {
								return yield* Effect.fail(
									new BeadsError({
										message: "Dependency add requires child and parent ids",
										command: "linear-sdk dep add",
									}),
								)
							}
							if (depType !== undefined && depType !== "parent-child") {
								return yield* Effect.fail(
									new BeadsError({
										message: "Linear backend currently supports only parent-child dependencies",
										command: "linear-sdk dep add",
									}),
								)
							}
							const childId = yield* resolveLinearIssueId(issueIdentifier)
							const parentId = yield* resolveLinearIssueId(dependsOnIdentifier)
							yield* withLinearSdkTiming(
								["i", "update"],
								Effect.tryPromise({
									try: () => linearClient.updateIssue(childId, { parentId }),
									catch: (error) =>
										new BeadsError({
											message: error instanceof Error ? error.message : String(error),
											command: "linear-sdk dep add",
										}),
								}).pipe(linearSdk.rateLimit),
							)
							return "{}"
						}

						default:
							return yield* Effect.fail(
								new BeadsError({
									message: `Linear backend does not support command: ${command}`,
									command: `linear-sdk ${args.join(" ")}`,
								}),
							)
					}
				})

			return {
				flavor: "linear",
				executable: "linear-sdk",
				runJson,
				runDirect: (args, runCwd) => runJson(args, runCwd),
				parseSyncResult: () => Effect.succeed(ZERO_SYNC_RESULT),
			}
		}

		const startupConfig = yield* SubscriptionRef.get(appConfig.config)
		const configuredBackend = resolveConfiguredIssueBackend(startupConfig.issueTracker)
		const useLocalFirstPath = isLocalFirstIssueBackend(configuredBackend)
		const mutationSyncTarget = getSyncTargetForBackend(configuredBackend)
		const legacyIssueDbClient: IssueDbClient | undefined =
			configuredBackend === "bd"
				? createBdIssueDbClient("bd")
				: configuredBackend === "br"
					? createBrIssueDbClient("br")
					: undefined

		const mapLocalIssueStoreError = (command: string, error: LocalIssueStoreError): BeadsError =>
			new BeadsError({
				message: error.message,
				command,
				stderr: error.cause === undefined ? undefined : String(error.cause),
			})

		const mapIssueSyncError = (command: string, error: IssueSyncError): BeadsError =>
			new BeadsError({
				message: error.message,
				command,
				stderr: error.cause === undefined ? undefined : String(error.cause),
			})

		const fromLocalStore = <A>(
			command: string,
			effect: Effect.Effect<A, LocalIssueStoreError>,
		): Effect.Effect<A, BeadsError> =>
			effect.pipe(Effect.mapError((error) => mapLocalIssueStoreError(command, error)))

		const fromIssueSync = <A>(
			command: string,
			effect: Effect.Effect<A, IssueSyncError>,
		): Effect.Effect<A, BeadsError> =>
			effect.pipe(Effect.mapError((error) => mapIssueSyncError(command, error)))

		const ensureLinearBootstrapForRead = (cwd?: string): Effect.Effect<void, BeadsError> =>
			configuredBackend !== "linear"
				? Effect.void
				: fromIssueSync("issue-sync bootstrapLinear", issueSyncService.bootstrapLinear(cwd)).pipe(
						Effect.asVoid,
					)

		const runBd = (
			args: readonly string[],
			runCwd?: string,
		): Effect.Effect<string, BeadsError | SyncRequiredError, CommandExecutor.CommandExecutor> =>
			legacyIssueDbClient !== undefined
				? legacyIssueDbClient.runJson(args, runCwd)
				: Effect.fail(
						new BeadsError({
							message: `Legacy command path is unavailable for ${configuredBackend} backend`,
							command: `legacy ${args.join(" ")}`,
						}),
					)

		const runBdDirect = (
			args: readonly string[],
			runCwd?: string,
		): Effect.Effect<string, BeadsError, CommandExecutor.CommandExecutor> =>
			legacyIssueDbClient !== undefined
				? legacyIssueDbClient.runDirect(args, runCwd)
				: Effect.fail(
						new BeadsError({
							message: `Legacy command path is unavailable for ${configuredBackend} backend`,
							command: `legacy ${args.join(" ")}`,
						}),
					)

		const parseSyncResult = (output: string): Effect.Effect<SyncResult, ParseError> =>
			legacyIssueDbClient !== undefined
				? legacyIssueDbClient.parseSyncResult(output)
				: Effect.succeed(ZERO_SYNC_RESULT)

		return {
			list: (filters?: IssueListFilters, cwd?: string, options?: IssueListOptions) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const issues = yield* fromLocalStore(
							"local-store list",
							localIssueStore.list(filters, effectiveCwd, options),
						)
						return [...issues]
					}

					const pageSize = clampPositiveInt(
						options?.pageSize ?? DEFAULT_ISSUE_LIST_PAGE_SIZE,
						DEFAULT_ISSUE_LIST_PAGE_SIZE,
					)
					const targetLimit =
						options?.limit !== undefined && options.limit > 0
							? Math.floor(options.limit)
							: undefined

					let currentLimit = targetLimit !== undefined ? Math.min(targetLimit, pageSize) : pageSize
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
									const fallbackArgs = buildListCommandArgs(currentLimit, filters, options, false)
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
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const issue = yield* fromLocalStore(
							"local-store show",
							localIssueStore.show(id, effectiveCwd),
						)
						if (issue === undefined || issue.status === "tombstone") {
							return yield* Effect.fail(new NotFoundError({ issueId: id }))
						}
						return issue
					}

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
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const issues = yield* fromLocalStore(
							"local-store showMultiple",
							localIssueStore.showMultiple(ids, effectiveCwd),
						)
						return [...issues]
					}

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
					if (useLocalFirstPath) {
						const updated = yield* fromLocalStore(
							"local-store update",
							localIssueStore.update(id, fields, mutationSyncTarget, effectiveCwd),
						)
						if (!updated) {
							return yield* Effect.fail(
								new BeadsError({
									message: `Issue not found: ${id}`,
									command: `local-store update ${id}`,
								}),
							)
						}
						return
					}

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
					if (useLocalFirstPath) {
						const closed = yield* fromLocalStore(
							"local-store close",
							localIssueStore.close(id, mutationSyncTarget, effectiveCwd),
						)
						if (!closed) {
							return yield* Effect.fail(
								new BeadsError({
									message: `Issue not found: ${id}`,
									command: `local-store close ${id}`,
								}),
							)
						}
						return
					}

					const args: string[] = ["close", id]

					if (reason) {
						args.push("--reason", reason)
					}

					yield* runBd(args, effectiveCwd)
				}),

			sync: (cwd?: string) =>
				Effect.gen(function* () {
					if (configuredBackend === "local") {
						return ZERO_SYNC_RESULT
					}

					// Check if beads sync is enabled (config + network)
					const syncStatus = yield* offlineService.isIssueTrackerSyncEnabled()
					if (!syncStatus.enabled) {
						// Return empty result when offline - issues are tracked locally
						return ZERO_SYNC_RESULT
					}

					const effectiveCwd = yield* getEffectiveCwd(cwd)
					if (configuredBackend === "linear") {
						return yield* fromIssueSync(
							"issue-sync flushLinearQueue",
							issueSyncService.flushLinearQueue(effectiveCwd),
						)
					}

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
					if (useLocalFirstPath) {
						return ZERO_SYNC_RESULT
					}

					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const output = yield* runBd(["sync", "--import-only"], effectiveCwd)
					return yield* parseSyncResult(output)
				}),

			recoverTombstones: (cwd?: string) =>
				Effect.gen(function* () {
					if (configuredBackend === "linear" || configuredBackend === "local") {
						return 0
					}

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
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const issues = yield* fromLocalStore(
							"local-store ready",
							localIssueStore.ready(effectiveCwd),
						)
						return [...issues]
					}

					const output = yield* runBd(["ready"], effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)
					// Filter out tombstone (deleted) issues
					return normalized.filter((issue) => issue.status !== "tombstone")
				}),

			search: (query: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const issues = yield* fromLocalStore(
							"local-store search",
							localIssueStore.search(query, effectiveCwd),
						)
						return [...issues]
					}

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
				parent?: string
				cwd?: string
			}) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(params.cwd)
					if (useLocalFirstPath) {
						return yield* fromLocalStore(
							"local-store create",
							localIssueStore.create(
								{
									title: params.title,
									type: params.type,
									priority: params.priority,
									description: params.description,
									design: params.design,
									acceptance: params.acceptance,
									assignee: params.assignee,
									estimate: params.estimate,
									labels: params.labels,
									parent: params.parent,
								},
								mutationSyncTarget,
								effectiveCwd,
							),
						)
					}

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
					if (params.parent !== undefined) {
						args.push("--parent", params.parent)
					}

					const output = yield* runBd(args, effectiveCwd)

					// bd create returns a single issue object (not an array)
					const parsed = yield* parseJson(IssueSchema, output)
					return normalizeIssue(parsed)
				}),

			delete: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					if (useLocalFirstPath) {
						const deleted = yield* fromLocalStore(
							"local-store delete",
							localIssueStore.delete(id, mutationSyncTarget, effectiveCwd),
						)
						if (!deleted) {
							return yield* Effect.fail(
								new BeadsError({
									message: `Issue not found: ${id}`,
									command: `local-store delete ${id}`,
								}),
							)
						}
						return
					}

					// Use runBdDirect because:
					// 1. The daemon doesn't support the delete operation
					// 2. --force is required to actually delete (not just preview)
					yield* runBdDirect(["delete", id, "--force"], effectiveCwd)
				}),

			getEpicChildren: (epicId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const epic = yield* fromLocalStore(
							"local-store show",
							localIssueStore.show(epicId, effectiveCwd),
						)
						if (epic === undefined || epic.status === "tombstone") {
							return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
						}

						const children = yield* fromLocalStore(
							"local-store getEpicChildren",
							localIssueStore.getEpicChildren(epicId, effectiveCwd),
						)
						return [...children]
					}

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
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const epic = yield* fromLocalStore(
							"local-store show",
							localIssueStore.show(epicId, effectiveCwd),
						)
						if (epic === undefined || epic.status === "tombstone") {
							return yield* Effect.fail(new NotFoundError({ issueId: epicId }))
						}

						const children = yield* fromLocalStore(
							"local-store getEpicChildren",
							localIssueStore.getEpicChildren(epicId, effectiveCwd),
						)
						return { epic, children: [...children] }
					}

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
					if (useLocalFirstPath) {
						yield* fromLocalStore(
							"local-store addDependency",
							localIssueStore.addDependency(
								issueId,
								dependsOnId,
								type ?? "blocks",
								mutationSyncTarget,
								effectiveCwd,
							),
						)
						return
					}

					const args: string[] = ["dep", "add", issueId, dependsOnId]

					if (type) {
						args.push("--type", type)
					}

					yield* runBd(args, effectiveCwd)
				}),

			getParentEpic: (issueId: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					if (useLocalFirstPath) {
						yield* ensureLinearBootstrapForRead(effectiveCwd)
						const issue = yield* fromLocalStore(
							"local-store show",
							localIssueStore.show(issueId, effectiveCwd),
						)
						if (issue === undefined || issue.status === "tombstone") {
							return yield* Effect.fail(new NotFoundError({ issueId }))
						}

						return yield* fromLocalStore(
							"local-store getParentEpic",
							localIssueStore.getParentEpic(issueId, effectiveCwd),
						)
					}

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
	filters?: IssueListFilters,
	cwd?: string,
	options?: IssueListOptions,
): Effect.Effect<
	Issue[],
	BeadsError | ParseError | SyncRequiredError,
	BeadsClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(BeadsClient, (client) => client.list(filters, cwd, options))

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
	parent?: string
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
