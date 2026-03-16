/**
 * IssueTrackerClient - Effect service for interacting with the tracker CLI
 *
 * Wraps tracker commands with Effect for type-safe, composable issue tracking operations.
 * All tracker commands are executed with --json flag for structured output.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import type { LinearClient, Issue as LinearSdkIssue } from "@linear/sdk"
import { Data, Effect, Fiber, SubscriptionRef } from "effect"
import * as Schema from "effect/Schema"
import { AppConfig } from "../config/AppConfig.js"
import type { IssueDbPerfOperationKind } from "../services/DiagnosticsService.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { NetworkService } from "../services/NetworkService.js"
import { ProjectService } from "../services/ProjectService.js"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { BackendSyncRouter } from "./BackendSyncRouter.js"
import type { IssueSyncError } from "./IssueSyncService.js"
import { LinearSdk } from "./LinearSdk.js"
import {
	type ExternalIssueSnapshot,
	LocalIssueStore,
	type LocalIssueStoreError,
	type SyncTarget,
} from "./LocalIssueStore.js"

// ============================================================================
// Schema Definitions
// ============================================================================

export type IssueStatus = "open" | "in_progress" | "blocked" | "closed" | "tombstone"
export type IssueType = "bug" | "feature" | "task" | "epic" | "chore"
export type DependencyType = "blocks" | "related" | "parent-child" | "discovered-from"

/**
 * Dependency reference schema for issue dependencies/dependents
 *
 * Intentionally permissive for legacy compatibility:
 * - legacy can emit extra dependency types
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

type DependencyRaw = Schema.Schema.Type<typeof DependencySchema>

export interface DependencyRef {
	readonly id: string
	readonly title?: string
	readonly status?: IssueStatus
	readonly dependency_type: DependencyType
	readonly issue_type?: IssueType
}

/**
 * Issue schema matching tracker/legacy --json output
 *
 * Intentionally permissive for legacy compatibility:
 * - legacy can emit additional status / issue_type variants
 * - legacy can emit `estimated_minutes` instead of `estimate`
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
	implementations: Schema.Array(Schema.String).pipe(Schema.optional),
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
	readonly implementations: readonly string[]
	readonly dependent_count?: number
	readonly dependency_count?: number
	readonly dependents?: readonly DependencyRef[]
	readonly dependencies?: readonly DependencyRef[]
}

export interface IssueListFilters {
	readonly status?: string
	readonly priority?: number
	readonly type?: string
	readonly parent?: string
	readonly implementations?: readonly string[]
}

export type IssueListSortField = "updated_at" | "created_at" | "priority" | "title"

export interface IssueListOptions {
	readonly limit?: number
	readonly pageSize?: number
	readonly includeClosed?: boolean
	readonly sortBy?: IssueListSortField
	readonly sortDirection?: "asc" | "desc"
}

export interface IssueReadSyncOptions {
	readonly maxSyncWaitMs?: number
}

export interface ImplementationRecord {
	readonly name: string
	readonly description?: string
	readonly directory?: string
	readonly created_at: string
	readonly updated_at: string
	readonly is_default: boolean
	readonly is_builtin: boolean
}

export interface ImplementationRegistry {
	readonly default_implementation: string
	readonly implicit_default_allowed: boolean
	readonly implementations: readonly ImplementationRecord[]
}

const DEFAULT_IMPLEMENTATION = "default"

const normalizeIssueImplementations = (
	implementations: readonly string[] | undefined,
): readonly string[] => {
	const seen = new Set<string>()
	const normalized: string[] = []
	for (const implementation of implementations ?? [DEFAULT_IMPLEMENTATION]) {
		const value = implementation.trim().toLowerCase()
		if (value.length === 0 || seen.has(value)) {
			continue
		}
		seen.add(value)
		normalized.push(value)
	}
	return normalized.length > 0 ? normalized : [DEFAULT_IMPLEMENTATION]
}

const parseIssueStatus = (status: string | undefined): IssueStatus | undefined => {
	switch (status) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return status
		// legacy-native states: map into existing board model
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
		// legacy-specific types that do not exist in legacy UI model
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
		// legacy dependency variants mapped to existing relationship model
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
	implementations: normalizeIssueImplementations(issue.implementations),
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
 * Legacy tracker returns:
 *   { pushed: number, pulled: number }
 *
 * legacy returns import/export stats:
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

export interface SyncOptions {
	readonly hydrateRemote?: boolean
}

const normalizeLegacySyncResult = (result: LegacySyncResult): SyncResult => result

const normalizeBrSyncResult = (result: BrSyncResult): SyncResult => ({
	// legacy sync is local DB<->JSONL reconciliation; treat created+updated as "pulled"
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
const LINEAR_METADATA_PAGE_SIZE = 250
const _LINEAR_DETAIL_CACHE_TTL_MS = 5 * 60 * 1000
const _LINEAR_DETAIL_FETCH_LIMIT_PER_LIST = 80
const _LINEAR_DETAIL_FETCH_CHUNK_SIZE = 20

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

const _normalizeLinearIssue = (issue: LinearIssue): IssueRaw => {
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
 * Generic tracker command execution error
 */
export class IssueTrackerError extends Data.TaggedError("IssueTrackerError")<{
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
 * JSON parsing error from tracker output
 */
export class ParseError extends Data.TaggedError("ParseError")<{
	readonly message: string
	readonly output: string
}> {}

/**
 * Error when tracker database is out of sync with JSONL file.
 * This happens after git pull or when another worktree modifies issues.
 * Can be auto-recovered by running import-only sync.
 */
export class SyncRequiredError extends Data.TaggedError("SyncRequiredError")<{
	readonly message: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * IssueTrackerClient service interface
 *
 * Provides typed access to tracker CLI commands with Effect error handling.
 * Note: All methods require CommandExecutor in their context.
 */
export interface IssueTrackerClientService {
	/**
	 * List issues with optional filters
	 *
	 * @example
	 * ```ts
	 * // Get all in-progress tasks
	 * IssueTrackerClient.list({ status: "in_progress", type: "task" })
	 * ```
	 */
	readonly list: (
		filters?: IssueListFilters,
		cwd?: string,
		options?: IssueListOptions,
	) => Effect.Effect<
		Issue[],
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Show details for a single issue
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.show("az-05y")
	 * ```
	 */
	readonly show: (
		id: string,
		cwd?: string,
		syncOptions?: IssueReadSyncOptions,
	) => Effect.Effect<
		Issue,
		IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
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
	 * IssueTrackerClient.showMultiple(["az-05y", "az-06z", "az-07a"])
	 * ```
	 */
	readonly showMultiple: (
		ids: readonly string[],
		cwd?: string,
	) => Effect.Effect<
		Issue[],
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Update issue fields
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.update("az-05y", {
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
			implementations?: readonly string[]
			parent?: string
		},
		cwd?: string,
	) => Effect.Effect<void, IssueTrackerError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Close an issue with optional reason
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.close("az-05y", "Implementation complete")
	 * ```
	 */
	readonly close: (
		id: string,
		reason?: string,
		cwd?: string,
	) => Effect.Effect<void, IssueTrackerError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Sync tracker database (push/pull)
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.sync().pipe(
	 *   Effect.tap(result => Console.log(`Synced: ${result.pushed} pushed, ${result.pulled} pulled`))
	 * )
	 * ```
	 */
	readonly sync: (
		cwd?: string,
		options?: SyncOptions,
	) => Effect.Effect<
		SyncResult,
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Import-only sync - re-imports tracker from JSONL into database without git operations.
	 * Use after git merge to recover any tracker incorrectly removed by the merge driver.
	 */
	readonly syncImportOnly: (
		cwd?: string,
	) => Effect.Effect<
		SyncResult,
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Recover tombstoned issues from JSONL.
	 * Workaround for tracker sync bug where issues get incorrectly tombstoned during merge.
	 * See issue az-zby for details.
	 *
	 * @returns Number of issues recovered
	 */
	readonly recoverTombstones: (
		cwd?: string,
	) => Effect.Effect<number, IssueTrackerError, CommandExecutor.CommandExecutor>

	/**
	 * Get ready (unblocked) issues
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.ready()
	 * ```
	 */
	readonly ready: (
		cwd?: string,
	) => Effect.Effect<
		Issue[],
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Search issues by query string
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.search("tracker client")
	 * ```
	 */
	readonly search: (
		query: string,
		cwd?: string,
	) => Effect.Effect<
		Issue[],
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Create a new issue
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.create({
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
		status?: IssueStatus
		priority?: number
		description?: string
		design?: string
		acceptance?: string
		assignee?: string
		estimate?: number
		labels?: string[]
		implementations?: readonly string[]
		parent?: string
		cwd?: string
	}) => Effect.Effect<
		Issue,
		IssueTrackerError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly listImplementations: (
		cwd?: string,
	) => Effect.Effect<
		readonly ImplementationRecord[],
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly showImplementation: (
		name: string,
		cwd?: string,
	) => Effect.Effect<
		ImplementationRecord | undefined,
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly getImplementationRegistry: (
		cwd?: string,
	) => Effect.Effect<
		ImplementationRegistry,
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly createImplementation: (params: {
		name: string
		description?: string
		directory?: string
		setDefault?: boolean
		cwd?: string
	}) => Effect.Effect<
		ImplementationRecord,
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly updateImplementation: (
		currentName: string,
		fields: {
			name?: string
			description?: string | null
			directory?: string | null
			setDefault?: boolean
		},
		cwd?: string,
	) => Effect.Effect<
		ImplementationRecord,
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly deleteImplementation: (
		name: string,
		cwd?: string,
	) => Effect.Effect<
		boolean,
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	readonly setDefaultImplementation: (
		name: string,
		cwd?: string,
	) => Effect.Effect<
		ImplementationRegistry,
		IssueTrackerError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Delete an issue entirely
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.delete("az-05y")
	 * ```
	 */
	readonly delete: (
		id: string,
		cwd?: string,
	) => Effect.Effect<void, IssueTrackerError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Get children of an epic (issues with parent-child dependency)
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.getEpicChildren("az-gds")
	 * // Returns array of child issue IDs
	 * ```
	 */
	readonly getEpicChildren: (
		epicId: string,
		cwd?: string,
	) => Effect.Effect<
		DependencyRef[],
		IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Get an epic with its child tasks
	 *
	 * Fetches an epic issue and filters its dependents to return only parent-child relationships.
	 *
	 * @example
	 * ```ts
	 * IssueTrackerClient.getEpicWithChildren("az-05y")
	 * ```
	 */
	readonly getEpicWithChildren: (
		epicId: string,
		cwd?: string,
	) => Effect.Effect<
		{ epic: Issue; children: ReadonlyArray<DependencyRef> },
		IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
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
	 * IssueTrackerClient.addDependency("az-task", "az-epic", "parent-child")
	 *
	 * // Default "blocks" dependency
	 * IssueTrackerClient.addDependency("az-blocked", "az-blocker")
	 * ```
	 */
	readonly addDependency: (
		issueId: string,
		dependsOnId: string,
		type?: "blocks" | "related" | "parent-child" | "discovered-from",
		cwd?: string,
	) => Effect.Effect<void, IssueTrackerError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Remove dependency edge(s) between two issues
	 *
	 * Removes dependency records where `issueId` depends on `dependsOnId`.
	 * When `type` is omitted, all dependency types between the pair are removed.
	 */
	readonly removeDependency: (
		issueId: string,
		dependsOnId: string,
		type?: "blocks" | "related" | "parent-child" | "discovered-from",
		cwd?: string,
	) => Effect.Effect<void, IssueTrackerError | SyncRequiredError, CommandExecutor.CommandExecutor>

	/**
	 * Get the parent epic of an issue, if it has one
	 *
	 * Looks for a parent-child dependency where this issue depends on an epic.
	 * Returns the parent epic issue, or undefined if no parent epic exists.
	 *
	 * @example
	 * ```ts
	 * const parentEpic = yield* IssueTrackerClient.getParentEpic("az-task")
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
		IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
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
	message.includes("Run 'tracker sync --import-only'") ||
	message.includes("tracker sync --import-only")

type LegacyIssueExecutable = "tracker" | "legacy"
type IssueDbFlavor = "tracker" | "legacy" | "linear"
type ConfiguredIssueBackend = "tracker" | "legacy" | "local" | "linear"
type LocalFirstIssueBackend = "local" | "linear"

export interface IssueTrackerBackendConfigShape {
	readonly tracker?: unknown
	readonly legacy?: unknown
	readonly linear?: unknown
	readonly local?: unknown
}

export const resolveConfiguredIssueBackend = (
	issueTracker: IssueTrackerBackendConfigShape,
): ConfiguredIssueBackend => {
	if (issueTracker.tracker !== undefined) return "tracker"
	if (issueTracker.legacy !== undefined) return "legacy"
	if (issueTracker.linear !== undefined) return "linear"
	return "local"
}

export const isLocalFirstIssueBackend = (
	backend: ConfiguredIssueBackend,
): backend is LocalFirstIssueBackend => backend === "local" || backend === "linear"

export const getSyncTargetForBackend = (backend: ConfiguredIssueBackend): SyncTarget | undefined =>
	backend === "linear" ? "linear" : undefined

export interface LinearReadFallbackInput {
	readonly backend: ConfiguredIssueBackend
	readonly requestedCount: number
	readonly localResultCount: number
	readonly syncPulledCount: number
}

export const shouldUseLinearReadFallback = (input: LinearReadFallbackInput): boolean => {
	if (input.backend !== "linear") return false
	if (input.requestedCount <= 0) return false
	if (input.localResultCount >= input.requestedCount) return false
	return input.syncPulledCount === 0
}

export interface ResolveSyncProjectPathInput {
	readonly selectedPath?: string
	readonly fallbackProjectPath: string
}

export const resolveSyncProjectPathValue = (input: ResolveSyncProjectPathInput): string => {
	const trimmed = input.selectedPath?.trim()
	return trimmed && trimmed.length > 0 ? trimmed : input.fallbackProjectPath
}

const getParentLocalIdFromIssue = (issue: Issue): string | undefined =>
	issue.dependencies?.find((dependency) => dependency.dependency_type === "parent-child")?.id

export interface LinearFallbackExternalRef {
	readonly externalId: string
	readonly externalKey?: string
}

export const buildLinearFallbackSnapshots = (
	issues: readonly Issue[],
	externalRefsByIdentifier: ReadonlyMap<string, LinearFallbackExternalRef>,
): readonly ExternalIssueSnapshot[] => {
	const snapshots: ExternalIssueSnapshot[] = []
	for (const issue of issues) {
		const externalRef = externalRefsByIdentifier.get(issue.id)
		if (externalRef === undefined) {
			continue
		}
		snapshots.push({
			localId: issue.id,
			externalId: externalRef.externalId,
			externalKey: externalRef.externalKey ?? issue.id,
			title: issue.title,
			description: issue.description,
			status: issue.status,
			priority: issue.priority,
			issueType: issue.issue_type,
			createdAt: issue.created_at,
			updatedAt: issue.updated_at,
			closedAt: issue.closed_at,
			assignee: issue.assignee,
			labels: issue.labels ?? [],
			notes: issue.notes,
			design: issue.design,
			acceptance: issue.acceptance,
			estimate: issue.estimate,
			parentLocalId: getParentLocalIdFromIssue(issue),
		})
	}
	return snapshots
}

export const collectLinearFallbackIssuesById = <E, R>(
	issueIds: readonly string[],
	loadFallbackIssues: (issueId: string) => Effect.Effect<readonly Issue[], E, R>,
): Effect.Effect<readonly Issue[], never, R> =>
	Effect.forEach(
		issueIds,
		(issueId) =>
			loadFallbackIssues(issueId).pipe(
				Effect.map((issues) => issues[0]),
				Effect.catchAll((error) =>
					Effect.logWarning(
						`Linear direct read fallback failed for '${issueId}': ${String(error)}`,
					).pipe(Effect.as(undefined)),
				),
			),
		{ concurrency: 1 },
	).pipe(Effect.map((issues) => issues.filter((issue): issue is Issue => issue !== undefined)))

interface IssueDbClient {
	readonly flavor: IssueDbFlavor
	readonly executable: string
	readonly runJson: (
		args: readonly string[],
		cwd?: string,
	) => Effect.Effect<string, IssueTrackerError | SyncRequiredError, CommandExecutor.CommandExecutor>
	readonly runDirect: (
		args: readonly string[],
		cwd?: string,
	) => Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor>
	readonly parseSyncResult: (output: string) => Effect.Effect<SyncResult, ParseError>
	readonly resolveExternalRefsByIdentifier?: (
		identifiers: readonly string[],
	) => Effect.Effect<
		ReadonlyMap<string, LinearFallbackExternalRef>,
		IssueTrackerError,
		CommandExecutor.CommandExecutor
	>
}

interface IssueTrackerRuntime {
	readonly effectiveCwd: string | undefined
	readonly projectPath: string
	readonly configuredBackend: ConfiguredIssueBackend
	readonly useLocalFirstPath: boolean
	readonly mutationSyncTarget: SyncTarget | undefined
	readonly legacyIssueDbClient: IssueDbClient | undefined
	readonly linearIssueDbClient: IssueDbClient | undefined
	readonly syncEnabled: boolean
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

type LinearIssuesQuery = NonNullable<Parameters<LinearClient["issues"]>[0]>

export const buildLinearIssuesListPageQuery = (
	afterCursor: string | null | undefined,
): LinearIssuesQuery => ({
	first: 250,
	...(afterCursor == null ? {} : { after: afterCursor }),
})

const hasNoDaemonFlag = (args: readonly string[]): boolean =>
	args.some((arg) => arg === "--no-daemon")

const DEFAULT_ISSUE_LIST_PAGE_SIZE = 200
const DEFAULT_LINEAR_READ_SYNC_MAX_WAIT_MS = 250
const EMPTY_ISSUES: readonly Issue[] = []

interface LinearReadSyncAttempt {
	readonly completedWithinBudget: boolean
	readonly syncResult: SyncResult
}

interface IssueTrackerSyncEnabledStatus {
	readonly enabled: true
}

interface IssueTrackerSyncDisabledStatus {
	readonly enabled: false
	readonly reason: "both" | "config" | "offline"
}

type IssueTrackerSyncStatus = IssueTrackerSyncEnabledStatus | IssueTrackerSyncDisabledStatus

const DEFAULT_LINEAR_READ_SYNC_ATTEMPT: LinearReadSyncAttempt = {
	completedWithinBudget: true,
	syncResult: ZERO_SYNC_RESULT,
}

const clampPositiveInt = (value: number, fallback: number): number => {
	if (!Number.isFinite(value)) return fallback
	const rounded = Math.floor(value)
	return rounded > 0 ? rounded : fallback
}

const isUnsupportedSortFlagError = (error: IssueTrackerError): boolean => {
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
	if (filters?.parent) {
		args.push("--parent", filters.parent)
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
 * IssueTrackerClient service
 *
 * Creates a service implementation that captures CommandExecutor from the scope.
 * The Layer automatically provides BunContext for command execution.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const client = yield* IssueTrackerClient
 *   const issues = yield* client.ready()
 *   return issues
 * }).pipe(Effect.provide(IssueTrackerClient.Default))
 * ```
 */
export class IssueTrackerClient extends Effect.Service<IssueTrackerClient>()("IssueTrackerClient", {
	dependencies: [
		ProjectService.Default,
		NetworkService.Default,
		AppConfig.Default,
		DiagnosticsService.Default,
		LocalIssueStore.Default,
		BackendSyncRouter.Default,
		LinearSdk.Default,
	],
	scoped: Effect.gen(function* () {
		const projectService = yield* ProjectService
		const networkService = yield* NetworkService
		const appConfig = yield* AppConfig
		const diagnostics = yield* DiagnosticsService
		const localIssueStore = yield* LocalIssueStore
		const backendSyncRouter = yield* BackendSyncRouter
		const linearSdk = yield* LinearSdk
		const scope = yield* Effect.scope

		/**
		 * Get effective cwd for tracker commands:
		 * - If explicit cwd provided, use it
		 * - Otherwise, use current project path from ProjectService
		 * - Returns undefined when no project is selected (callers choose fallback behavior)
		 */
		const getEffectiveCwd = (explicitCwd?: string): Effect.Effect<string | undefined> =>
			explicitCwd ? Effect.succeed(explicitCwd) : projectService.getCurrentPath()

		const executeLegacyJsonCommand = (
			executable: LegacyIssueExecutable,
			args: readonly string[],
			cwd?: string,
			retryOnEmptyOutput = true,
		): Effect.Effect<
			string,
			IssueTrackerError | SyncRequiredError,
			CommandExecutor.CommandExecutor
		> =>
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
									"IssueTracker database out of sync with JSONL. Run `az sync` (or retry from board for auto-recovery).",
							})
						}

						return new IssueTrackerError({
							message: `tracker command failed: ${stderr}`,
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
									"IssueTracker database out of sync with JSONL. Run `az sync` (or retry from board for auto-recovery).",
							}),
						)
					}
					// If not a sync error, fail with descriptive error
					if (!trimmed) {
						if (retryOnEmptyOutput && !hasNoDaemonFlag(args)) {
							yield* Effect.logWarning(
								`${executable} returned empty output, retrying with --no-daemon: ${executable} ${allArgs.join(" ")}`,
							)
							return yield* executeLegacyJsonCommand(
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
							new IssueTrackerError({
								message: "tracker command returned empty output",
								command: `${executable} ${allArgs.join(" ")}`,
								stderr: "",
							}),
						)
					}
				}

				return result
			})

		const executeLegacyDirectCommand = (
			executable: LegacyIssueExecutable,
			args: readonly string[],
			cwd?: string,
		): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				// Add --no-daemon to bypass daemon (daemon doesn't support all operations)
				const allArgs = ["--no-daemon", ...args]

				const command = cwd
					? Command.make(executable, ...allArgs).pipe(Command.workingDirectory(cwd))
					: Command.make(executable, ...allArgs)

				return yield* Command.string(command).pipe(
					Effect.mapError((error) => {
						const stderr = "stderr" in error ? String(error.stderr) : String(error)
						return new IssueTrackerError({
							message: `tracker command failed: ${stderr}`,
							command: `${executable} ${allArgs.join(" ")}`,
							stderr,
						})
					}),
				)
			})

		const createBdIssueDbClient = (executable: LegacyIssueExecutable): IssueDbClient => ({
			flavor: "tracker",
			executable,
			runJson: (args, runCwd) => executeLegacyJsonCommand(executable, args, runCwd),
			runDirect: (args, runCwd) => executeLegacyDirectCommand(executable, args, runCwd),
			parseSyncResult: (output) =>
				parseJson(LegacySyncResultSchema, output).pipe(Effect.map(normalizeLegacySyncResult)),
		})

		const createBrIssueDbClient = (executable: LegacyIssueExecutable): IssueDbClient => ({
			flavor: "legacy",
			executable,
			runJson: (args, runCwd) => executeLegacyJsonCommand(executable, args, runCwd),
			runDirect: (args, runCwd) => executeLegacyDirectCommand(executable, args, runCwd),
			parseSyncResult: (output) =>
				parseJson(BrSyncResultSchema, output).pipe(Effect.map(normalizeBrSyncResult)),
		})

		interface LinearRuntimeConfig {
			readonly defaultTeam?: string
		}

		const _createLinearIssueDbClient = (config: LinearRuntimeConfig): IssueDbClient => {
			const withLinearSdkTiming = <A>(
				linearArgs: readonly string[],
				effect: Effect.Effect<A, IssueTrackerError, CommandExecutor.CommandExecutor>,
			): Effect.Effect<A, IssueTrackerError, CommandExecutor.CommandExecutor> => {
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
				IssueTrackerError,
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
						IssueTrackerError,
						CommandExecutor.CommandExecutor
					> =>
						linearSdk.issues(buildLinearIssuesListPageQuery(afterCursor)).pipe(
							Effect.mapError(
								(error) =>
									new IssueTrackerError({
										message: error.message,
										command: "linear-sdk issues",
									}),
							),
						)

					const collectPages = (
						afterCursor: string | null | undefined,
						accumulator: readonly LinearSdkIssue[],
					): Effect.Effect<
						readonly LinearSdkIssue[],
						IssueTrackerError,
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

			const fetchAllWorkflowStates = (): Effect.Effect<
				readonly {
					readonly id: string
					readonly teamId?: string
					readonly name: string
					readonly type: string
				}[],
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const fetchWorkflowStatesPage = (afterCursor: string | null | undefined) =>
						linearSdk
							.workflowStates({
								first: LINEAR_METADATA_PAGE_SIZE,
								after: afterCursor ?? undefined,
							})
							.pipe(
								Effect.mapError(
									(error) =>
										new IssueTrackerError({
											message: error.message,
											command: "linear-sdk workflowStates",
										}),
								),
							)

					const collectPages = (
						afterCursor: string | null | undefined,
						accumulator: readonly {
							readonly id: string
							readonly teamId?: string
							readonly name: string
							readonly type: string
						}[],
					): Effect.Effect<
						readonly {
							readonly id: string
							readonly teamId?: string
							readonly name: string
							readonly type: string
						}[],
						IssueTrackerError,
						CommandExecutor.CommandExecutor
					> =>
						fetchWorkflowStatesPage(afterCursor).pipe(
							Effect.flatMap((states) => {
								const nextAccumulator = [
									...accumulator,
									...states.nodes.map((state) => ({
										id: state.id,
										teamId: state.teamId,
										name: state.name,
										type: state.type,
									})),
								]
								if (!states.pageInfo.hasNextPage || !states.pageInfo.endCursor) {
									return Effect.succeed(nextAccumulator)
								}
								return collectPages(states.pageInfo.endCursor, nextAccumulator)
							}),
						)

					return yield* collectPages(undefined, [])
				})

			const fetchStateNameById = (): Effect.Effect<
				ReadonlyMap<string, string>,
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> =>
				fetchAllWorkflowStates().pipe(
					Effect.map((states) => new Map(states.map((state) => [state.id, state.name] as const))),
				)

			const fetchWorkflowStates = (): Effect.Effect<
				readonly {
					readonly id: string
					readonly teamId?: string
					readonly name: string
					readonly type: string
				}[],
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> => fetchAllWorkflowStates()

			const fetchAllIssueLabels = (): Effect.Effect<
				readonly { readonly id: string; readonly name: string }[],
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const fetchIssueLabelsPage = (afterCursor: string | null | undefined) =>
						linearSdk
							.issueLabels({
								first: LINEAR_METADATA_PAGE_SIZE,
								after: afterCursor ?? undefined,
							})
							.pipe(
								Effect.mapError(
									(error) =>
										new IssueTrackerError({
											message: error.message,
											command: "linear-sdk issueLabels",
										}),
								),
							)

					const collectPages = (
						afterCursor: string | null | undefined,
						accumulator: readonly { readonly id: string; readonly name: string }[],
					): Effect.Effect<
						readonly { readonly id: string; readonly name: string }[],
						IssueTrackerError,
						CommandExecutor.CommandExecutor
					> =>
						fetchIssueLabelsPage(afterCursor).pipe(
							Effect.flatMap((labels) => {
								const nextAccumulator = [
									...accumulator,
									...labels.nodes.map((label) => ({
										id: label.id,
										name: label.name,
									})),
								]
								if (!labels.pageInfo.hasNextPage || !labels.pageInfo.endCursor) {
									return Effect.succeed(nextAccumulator)
								}
								return collectPages(labels.pageInfo.endCursor, nextAccumulator)
							}),
						)

					return yield* collectPages(undefined, [])
				})

			const fetchLabelNameById = (): Effect.Effect<
				ReadonlyMap<string, string>,
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> =>
				fetchAllIssueLabels().pipe(
					Effect.map((labels) => new Map(labels.map((label) => [label.id, label.name] as const))),
				)

			const fetchUsers = (): Effect.Effect<
				readonly { readonly id: string; readonly name: string; readonly email: string }[],
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> =>
				linearSdk.users({ first: 250 }).pipe(
					Effect.map((users) =>
						users.nodes.map((user) => ({
							id: user.id,
							name: user.name ?? "",
							email: user.email ?? "",
						})),
					),
					Effect.mapError(
						(error) =>
							new IssueTrackerError({
								message: error.message,
								command: "linear-sdk users",
							}),
					),
				)

			const resolveTeamId = (
				reference: string,
			): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
				linearSdk.resolveTeamId(reference).pipe(
					Effect.mapError(
						(error) =>
							new IssueTrackerError({
								message: error.message,
								command: "linear-sdk team",
							}),
					),
				)

			const resolveAssigneeId = (
				reference: string,
			): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const trimmed = reference.trim()
					if (trimmed.length === 0) {
						return yield* Effect.fail(
							new IssueTrackerError({
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
							new IssueTrackerError({
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
				IssueTrackerError,
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
			): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const snapshot = yield* buildLinearIssueSnapshot()
					const linearId = snapshot.linearIdByIdentifier.get(identifier)
					if (!linearId) {
						return yield* Effect.fail(
							new IssueTrackerError({
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
			): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const workflowStates = yield* fetchWorkflowStates()
					const teamStates = workflowStates.filter((state) => state.teamId === teamId)
					if (teamStates.length === 0) {
						return yield* Effect.fail(
							new IssueTrackerError({
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
			): Effect.Effect<readonly string[], IssueTrackerError, CommandExecutor.CommandExecutor> =>
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
			): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
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
									new IssueTrackerError({
										message: "Create requires issue title",
										command: "linear-sdk createIssue",
									}),
								)
							}

							const configuredTeam = config.defaultTeam?.trim()
							if (!configuredTeam) {
								return yield* Effect.fail(
									new IssueTrackerError({
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
							const status = parseArgumentValue(rest, "--status")
							const priorityArg = parseArgumentValue(rest, "--priority")
							const priority = toLinearPriorityValue(
								priorityArg ? Number.parseInt(priorityArg, 10) : undefined,
							)
							const parsedStatus = parseIssueStatus(status)
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
							const stateId =
								parsedStatus !== undefined
									? yield* findTeamStateIdForStatus(teamId, parsedStatus)
									: undefined

							const extraSections: string[] = []
							if (design) extraSections.push(`## Design\n${design}`)
							if (acceptance) extraSections.push(`## Acceptance\n${acceptance}`)
							const mergedDescription = [description, ...extraSections]
								.filter((value): value is string => value !== undefined && value.length > 0)
								.join("\n\n")

							const createdPayload = yield* withLinearSdkTiming(
								["i", "create"],
								linearSdk
									.createIssue({
										teamId,
										title,
										description: mergedDescription.length > 0 ? mergedDescription : undefined,
										priority,
										assigneeId,
										estimate: estimateValue,
										parentId,
										stateId,
										labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
									})
									.pipe(
										Effect.mapError(
											(error) =>
												new IssueTrackerError({
													message: error.message,
													command: "linear-sdk createIssue",
												}),
										),
									),
							)
							const createdIssueId = createdPayload.issueId
							if (!createdIssueId) {
								return yield* Effect.fail(
									new IssueTrackerError({
										message: "Linear create returned no issue",
										command: "linear-sdk createIssue",
									}),
								)
							}
							const createdLinearIssue = yield* withLinearSdkTiming(
								["i", "get", createdIssueId],
								linearSdk.issue(createdIssueId).pipe(
									Effect.mapError(
										(error) =>
											new IssueTrackerError({
												message: error.message,
												command: "linear-sdk issue",
											}),
									),
								),
							)
							const snapshot = yield* buildLinearIssueSnapshot()
							const created = snapshot.issues.find(
								(issue) => issue.id === createdLinearIssue.identifier,
							)
							if (!created) {
								return JSON.stringify({
									id: createdLinearIssue.identifier,
									title: createdLinearIssue.title,
									status: parsedStatus ?? "open",
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
									new IssueTrackerError({
										message: "Update requires issue id",
										command: "linear-sdk updateIssue",
									}),
								)
							}

							const snapshot = yield* buildLinearIssueSnapshot()
							const linearId = snapshot.linearIdByIdentifier.get(issueIdentifier)
							if (!linearId) {
								return yield* Effect.fail(
									new IssueTrackerError({
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
								linearSdk
									.updateIssue(linearId, {
										title,
										description: mergedDescription.length > 0 ? mergedDescription : undefined,
										priority,
										assigneeId,
										estimate: estimateValue,
										labelIds: labelIds.length > 0 ? [...labelIds] : undefined,
										parentId,
										stateId,
									})
									.pipe(
										Effect.mapError(
											(error) =>
												new IssueTrackerError({
													message: error.message,
													command: "linear-sdk updateIssue",
												}),
										),
									),
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
									new IssueTrackerError({
										message: `Issue not found: ${issueIdentifier}`,
										command: "linear-sdk closeIssue",
									}),
								)
							}
							const closedStateId = yield* findTeamStateIdForStatus(teamId, "closed")
							yield* withLinearSdkTiming(
								["i", "close"],
								linearSdk.updateIssue(linearId, { stateId: closedStateId }).pipe(
									Effect.mapError(
										(error) =>
											new IssueTrackerError({
												message: error.message,
												command: "linear-sdk closeIssue",
											}),
									),
								),
							)
							return "{}"
						}

						case "sync":
							return JSON.stringify(ZERO_SYNC_RESULT)

						case "dep": {
							if (rest[0] !== "add" && rest[0] !== "remove") {
								return yield* Effect.fail(
									new IssueTrackerError({
										message: "Only dependency add/remove is supported",
										command: "linear-sdk dep",
									}),
								)
							}
							const issueIdentifier = rest[1]
							const dependsOnIdentifier = rest[2]
							const depType = parseArgumentValue(rest, "--type")
							if (!issueIdentifier || !dependsOnIdentifier) {
								return yield* Effect.fail(
									new IssueTrackerError({
										message: "Dependency operation requires child and parent ids",
										command: "linear-sdk dep",
									}),
								)
							}
							if (depType !== undefined && depType !== "parent-child") {
								return yield* Effect.fail(
									new IssueTrackerError({
										message: "Linear backend currently supports only parent-child dependencies",
										command: `linear-sdk dep ${rest[0]}`,
									}),
								)
							}
							const childId = yield* resolveLinearIssueId(issueIdentifier)
							if (rest[0] === "add") {
								const parentId = yield* resolveLinearIssueId(dependsOnIdentifier)
								yield* withLinearSdkTiming(
									["i", "update"],
									linearSdk.updateIssue(childId, { parentId }).pipe(
										Effect.mapError(
											(error) =>
												new IssueTrackerError({
													message: error.message,
													command: "linear-sdk dep add",
												}),
										),
									),
								)
							} else {
								yield* withLinearSdkTiming(
									["i", "update"],
									linearSdk.updateIssue(childId, { parentId: null }).pipe(
										Effect.mapError(
											(error) =>
												new IssueTrackerError({
													message: error.message,
													command: "linear-sdk dep remove",
												}),
										),
									),
								)
							}
							return "{}"
						}

						default:
							return yield* Effect.fail(
								new IssueTrackerError({
									message: `Linear backend does not support command: ${command}`,
									command: `linear-sdk ${args.join(" ")}`,
								}),
							)
					}
				})

			const resolveExternalRefsByIdentifier = (
				identifiers: readonly string[],
			): Effect.Effect<
				ReadonlyMap<string, LinearFallbackExternalRef>,
				IssueTrackerError,
				CommandExecutor.CommandExecutor
			> => {
				if (identifiers.length === 0) {
					return Effect.succeed(new Map())
				}

				const requested = [...new Set(identifiers)]
				return withLinearSdkTiming(["i", "get", ...requested], buildLinearIssueSnapshot()).pipe(
					Effect.map((snapshot) => {
						const refs = new Map<string, LinearFallbackExternalRef>()
						for (const identifier of requested) {
							const externalId = snapshot.linearIdByIdentifier.get(identifier)
							if (externalId !== undefined) {
								refs.set(identifier, {
									externalId,
									externalKey: identifier,
								})
							}
						}
						return refs
					}),
				)
			}

			return {
				flavor: "linear",
				executable: "linear-sdk",
				runJson,
				runDirect: (args, runCwd) => runJson(args, runCwd),
				parseSyncResult: () => Effect.succeed(ZERO_SYNC_RESULT),
				resolveExternalRefsByIdentifier,
			}
		}

		const mapLocalIssueStoreError = (
			command: string,
			error: LocalIssueStoreError,
		): IssueTrackerError =>
			new IssueTrackerError({
				message: error.message,
				command,
			})

		const mapIssueSyncError = (command: string, error: IssueSyncError): IssueTrackerError =>
			new IssueTrackerError({
				message: error.message,
				command,
			})

		const fromLocalStore = <A>(
			command: string,
			effect: Effect.Effect<A, LocalIssueStoreError>,
		): Effect.Effect<A, IssueTrackerError> =>
			effect.pipe(Effect.mapError((error) => mapLocalIssueStoreError(command, error)))

		const fromIssueSync = <A>(
			command: string,
			effect: Effect.Effect<A, IssueSyncError>,
		): Effect.Effect<A, IssueTrackerError> =>
			effect.pipe(Effect.mapError((error) => mapIssueSyncError(command, error)))

		const resolveIssueTrackerRuntime = (
			explicitCwd?: string,
		): Effect.Effect<IssueTrackerRuntime, IssueTrackerError> =>
			Effect.gen(function* () {
				const effectiveCwd = yield* getEffectiveCwd(explicitCwd)
				const projectPath = resolveSyncProjectPathValue({
					selectedPath: effectiveCwd,
					fallbackProjectPath: process.cwd(),
				})
				const syncConfig = yield* appConfig
					.getIssueTrackerSyncConfigForProjectPath(projectPath)
					.pipe(
						Effect.mapError(
							(error) =>
								new IssueTrackerError({
									message: `Failed to load issue tracker config for projectPath=${projectPath}: ${error.message}`,
									command: "config issueTracker",
									stderr: error.details,
								}),
						),
					)
				const configuredBackend = resolveConfiguredIssueBackend(syncConfig.issueTracker)
				return {
					effectiveCwd,
					projectPath,
					configuredBackend,
					useLocalFirstPath: isLocalFirstIssueBackend(configuredBackend),
					mutationSyncTarget: getSyncTargetForBackend(configuredBackend),
					legacyIssueDbClient:
						configuredBackend === "tracker"
							? createBdIssueDbClient("tracker")
							: configuredBackend === "legacy"
								? createBrIssueDbClient("legacy")
								: undefined,
					linearIssueDbClient:
						configuredBackend === "linear" ? _createLinearIssueDbClient({}) : undefined,
					syncEnabled: syncConfig.syncEnabled,
				}
			})

		const resolveLinearBackendSync = (
			runtime: IssueTrackerRuntime,
		): Effect.Effect<BackendSyncInterface | undefined, IssueTrackerError> =>
			runtime.configuredBackend !== "linear"
				? Effect.succeed(undefined)
				: backendSyncRouter.resolve().pipe(
						Effect.mapError(
							(error) =>
								new IssueTrackerError({
									message: `Failed to resolve backend sync route for projectPath=${runtime.projectPath}: ${String(error)}`,
									command: "backend-sync resolve",
								}),
						),
					)

		const ensureLinearReadSync = (
			runtime: IssueTrackerRuntime,
			maxSyncWaitMs = DEFAULT_LINEAR_READ_SYNC_MAX_WAIT_MS,
		): Effect.Effect<LinearReadSyncAttempt, IssueTrackerError> =>
			runtime.configuredBackend !== "linear"
				? Effect.succeed(DEFAULT_LINEAR_READ_SYNC_ATTEMPT)
				: Effect.gen(function* () {
						const backendSync = yield* resolveLinearBackendSync(runtime)
						if (backendSync === undefined) {
							yield* Effect.log(
								`Linear read sync skipped: backend route unavailable (projectPath=${runtime.projectPath})`,
							)
							return DEFAULT_LINEAR_READ_SYNC_ATTEMPT
						}
						const readSyncEffect = fromIssueSync(
							"issue-sync flushLinearQueue",
							backendSync.flushQueue(runtime.projectPath),
						).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Linear read sync failed: ${error.message}`).pipe(
									Effect.as(ZERO_SYNC_RESULT),
								),
							),
						)

						const syncFiber = yield* Effect.forkIn(readSyncEffect, scope)
						if (maxSyncWaitMs <= 0) {
							return {
								completedWithinBudget: false,
								syncResult: ZERO_SYNC_RESULT,
							}
						}

						const completedWithinBudget = yield* Effect.raceFirst(
							Fiber.await(syncFiber).pipe(Effect.as(true)),
							Effect.sleep(`${maxSyncWaitMs} millis`).pipe(Effect.as(false)),
						)
						if (!completedWithinBudget) {
							yield* Effect.log(
								`Linear read sync timed out after ${maxSyncWaitMs}ms; returning local-first data (projectPath=${runtime.projectPath})`,
							)
							return {
								completedWithinBudget: false,
								syncResult: ZERO_SYNC_RESULT,
							}
						}
						const syncResult = yield* Fiber.join(syncFiber)
						return {
							completedWithinBudget: true,
							syncResult,
						}
					})

		const runBd = (
			runtime: IssueTrackerRuntime,
			args: readonly string[],
			runCwd?: string,
		): Effect.Effect<
			string,
			IssueTrackerError | SyncRequiredError,
			CommandExecutor.CommandExecutor
		> =>
			runtime.legacyIssueDbClient !== undefined
				? runtime.legacyIssueDbClient.runJson(args, runCwd)
				: Effect.fail(
						new IssueTrackerError({
							message: `Legacy command path is unavailable for ${runtime.configuredBackend} backend`,
							command: `legacy ${args.join(" ")}`,
						}),
					)

		const runBdDirect = (
			runtime: IssueTrackerRuntime,
			args: readonly string[],
			runCwd?: string,
		): Effect.Effect<string, IssueTrackerError, CommandExecutor.CommandExecutor> =>
			runtime.legacyIssueDbClient !== undefined
				? runtime.legacyIssueDbClient.runDirect(args, runCwd)
				: Effect.fail(
						new IssueTrackerError({
							message: `Legacy command path is unavailable for ${runtime.configuredBackend} backend`,
							command: `legacy ${args.join(" ")}`,
						}),
					)

		const parseSyncResult = (
			runtime: IssueTrackerRuntime,
			output: string,
		): Effect.Effect<SyncResult, ParseError> =>
			runtime.legacyIssueDbClient !== undefined
				? runtime.legacyIssueDbClient.parseSyncResult(output)
				: Effect.succeed(ZERO_SYNC_RESULT)

		const runLinearReadFallback = (
			runtime: IssueTrackerRuntime,
			args: readonly string[],
			runCwd?: string,
		): Effect.Effect<readonly Issue[], never, CommandExecutor.CommandExecutor> =>
			runtime.linearIssueDbClient === undefined
				? Effect.succeed(EMPTY_ISSUES)
				: runtime.linearIssueDbClient.runJson(args, runCwd).pipe(
						Effect.flatMap((output) => parseJson(Schema.Array(IssueSchema), output)),
						Effect.map((parsed) => normalizeIssues(parsed)),
						Effect.map((issues) => issues.filter((issue) => issue.status !== "tombstone")),
						Effect.catchAll((error) =>
							Effect.logWarning(
								`Linear direct read fallback failed for '${args.join(" ")}': ${String(error)}`,
							).pipe(Effect.as(EMPTY_ISSUES)),
						),
					)

		const backfillLinearFallbackIssues = (
			runtime: IssueTrackerRuntime,
			issues: readonly Issue[],
			runCwd?: string,
		): Effect.Effect<void, never, CommandExecutor.CommandExecutor> => {
			if (runtime.configuredBackend !== "linear" || runtime.mutationSyncTarget !== "linear") {
				return Effect.void
			}
			if (issues.length === 0) {
				return Effect.void
			}
			if (runtime.linearIssueDbClient?.resolveExternalRefsByIdentifier === undefined) {
				return Effect.logWarning(
					"Linear fallback local-store backfill skipped: external ref resolver unavailable",
				).pipe(Effect.asVoid)
			}

			const identifiers = issues.map((issue) => issue.id)
			return runtime.linearIssueDbClient.resolveExternalRefsByIdentifier(identifiers).pipe(
				Effect.flatMap((externalRefsByIdentifier) => {
					const snapshots = buildLinearFallbackSnapshots(issues, externalRefsByIdentifier)
					if (snapshots.length === 0) {
						return Effect.logWarning(
							`Linear fallback local-store backfill skipped: no external refs resolved for ${identifiers.join(",")}`,
						).pipe(Effect.asVoid)
					}
					return fromLocalStore(
						"local-store importExternalSnapshot",
						localIssueStore.importExternalSnapshot("linear", snapshots, runCwd),
					).pipe(Effect.asVoid)
				}),
				Effect.catchAll((error) =>
					Effect.logWarning(`Linear fallback local-store backfill failed: ${error.message}`).pipe(
						Effect.asVoid,
					),
				),
			)
		}

		const getIssueTrackerSyncStatus = (
			runtime: IssueTrackerRuntime,
		): Effect.Effect<IssueTrackerSyncStatus> =>
			Effect.gen(function* () {
				const online = yield* networkService.getIsOnline()
				if (runtime.syncEnabled && online) {
					return { enabled: true }
				}
				if (!runtime.syncEnabled && !online) {
					return { enabled: false, reason: "both" }
				}
				if (!runtime.syncEnabled) {
					return { enabled: false, reason: "config" }
				}
				return { enabled: false, reason: "offline" }
			})

		const mergeIssuesByRequestedIds = (
			requestedIds: readonly string[],
			localIssues: readonly Issue[],
			fallbackIssues: readonly Issue[],
		): readonly Issue[] => {
			const issueById = new Map<string, Issue>()
			for (const issue of localIssues) {
				issueById.set(issue.id, issue)
			}
			for (const issue of fallbackIssues) {
				if (!issueById.has(issue.id)) {
					issueById.set(issue.id, issue)
				}
			}

			const ordered: Issue[] = []
			for (const requestedId of requestedIds) {
				const issue = issueById.get(requestedId)
				if (issue !== undefined) {
					ordered.push(issue)
				}
			}
			return ordered
		}

		return {
			list: (filters?: IssueListFilters, cwd?: string, options?: IssueListOptions) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* ensureLinearReadSync(runtime)
						const issues = yield* fromLocalStore(
							"local-store list",
							localIssueStore.list(filters, effectiveCwd, options),
						)
						return [...issues]
					}
					if (filters?.implementations !== undefined && filters.implementations.length > 0) {
						return yield* Effect.fail(
							new IssueTrackerError({
								message:
									"Issue implementation filters require the local-first tracker path in ts-opentui.",
								command: "list",
							}),
						)
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
						const output = yield* runBd(runtime, args, effectiveCwd).pipe(
							Effect.catchAll((error) => {
								if (
									error._tag === "IssueTrackerError" &&
									includeSortFlags &&
									isUnsupportedSortFlagError(error)
								) {
									includeSortFlags = false
									const fallbackArgs = buildListCommandArgs(currentLimit, filters, options, false)
									return Effect.logWarning(error).pipe(
										Effect.zipRight(runBd(runtime, fallbackArgs, effectiveCwd)),
									)
								}

								return Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.fail(error)),
								)
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

			show: (id: string, cwd?: string, syncOptions?: IssueReadSyncOptions) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					const maxSyncWaitMs = Math.max(
						0,
						Math.floor(syncOptions?.maxSyncWaitMs ?? DEFAULT_LINEAR_READ_SYNC_MAX_WAIT_MS),
					)
					if (runtime.useLocalFirstPath) {
						const localIssue = yield* fromLocalStore(
							"local-store show",
							localIssueStore.show(id, effectiveCwd),
						)
						if (localIssue !== undefined && localIssue.status !== "tombstone") {
							return localIssue
						}

						// On linear local-first backends, try refreshing the local cache from Linear once
						// before returning not found. If refresh fails, still surface not found rather than
						// leaking backend sync failures for missing issues.
						let syncAttempt = DEFAULT_LINEAR_READ_SYNC_ATTEMPT
						if (runtime.configuredBackend === "linear") {
							syncAttempt = yield* ensureLinearReadSync(runtime, maxSyncWaitMs)
						}

						const refreshedIssue = yield* fromLocalStore(
							"local-store show",
							localIssueStore.show(id, effectiveCwd),
						)
						if (refreshedIssue !== undefined && refreshedIssue.status !== "tombstone") {
							return refreshedIssue
						}

						if (
							shouldUseLinearReadFallback({
								backend: runtime.configuredBackend,
								requestedCount: 1,
								localResultCount: 0,
								syncPulledCount: syncAttempt.syncResult.pulled,
							})
						) {
							const fallbackIssues = yield* runLinearReadFallback(
								runtime,
								["show", id],
								effectiveCwd,
							)
							const fallbackIssue = fallbackIssues[0]
							if (fallbackIssue !== undefined) {
								yield* backfillLinearFallbackIssues(runtime, [fallbackIssue], effectiveCwd)
								const backfilledIssue = yield* fromLocalStore(
									"local-store show",
									localIssueStore.show(id, effectiveCwd),
								).pipe(Effect.catchAll(() => Effect.succeed(undefined)))
								if (backfilledIssue !== undefined && backfilledIssue.status !== "tombstone") {
									return backfilledIssue
								}
								return fallbackIssue
							}
						}

						return yield* Effect.fail(new NotFoundError({ issueId: id }))
					}

					const output = yield* runBd(runtime, ["show", id], effectiveCwd)

					// tracker returns an array with a single item for show command
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

					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						const syncAttempt = yield* ensureLinearReadSync(runtime)
						const issues = yield* fromLocalStore(
							"local-store showMultiple",
							localIssueStore.showMultiple(ids, effectiveCwd),
						)
						const localIssues = [...issues]

						if (
							shouldUseLinearReadFallback({
								backend: runtime.configuredBackend,
								requestedCount: ids.length,
								localResultCount: localIssues.length,
								syncPulledCount: syncAttempt.syncResult.pulled,
							})
						) {
							const localIds = new Set(localIssues.map((issue) => issue.id))
							const missingIds = ids.filter((issueId) => !localIds.has(issueId))
							if (missingIds.length > 0) {
								const fallbackIssues = yield* collectLinearFallbackIssuesById(
									missingIds,
									(missingId) => runLinearReadFallback(runtime, ["show", missingId], effectiveCwd),
								)
								yield* backfillLinearFallbackIssues(runtime, fallbackIssues, effectiveCwd)
								const backfilledLocalIssues = yield* fromLocalStore(
									"local-store showMultiple",
									localIssueStore.showMultiple(ids, effectiveCwd),
								).pipe(Effect.catchAll(() => Effect.succeed(localIssues)))
								return mergeIssuesByRequestedIds(ids, backfilledLocalIssues, fallbackIssues)
							}
						}

						return localIssues
					}

					// tracker show accepts multiple IDs: tracker show id1 id2 id3 --json
					const output = yield* runBd(runtime, ["show", ...ids], effectiveCwd)

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
					implementations?: readonly string[]
					parent?: string
				},
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						const updated = yield* fromLocalStore(
							"local-store update",
							localIssueStore.update(id, fields, runtime.mutationSyncTarget, effectiveCwd),
						)
						if (!updated) {
							return yield* Effect.fail(
								new IssueTrackerError({
									message: `Issue not found: ${id}`,
									command: `local-store update ${id}`,
								}),
							)
						}
						return
					}
					if (fields.implementations !== undefined && fields.implementations.length > 0) {
						return yield* Effect.fail(
							new IssueTrackerError({
								message:
									"Issue implementations require the local-first tracker path in ts-opentui.",
								command: `update ${id}`,
							}),
						)
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
						// tracker update uses --set-labels for each label
						for (const label of fields.labels) {
							args.push("--set-labels", label)
						}
					}
					if (fields.parent !== undefined) {
						args.push("--parent", fields.parent)
					}

					yield* runBd(runtime, args, effectiveCwd)
				}),

			close: (id: string, reason?: string, cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						const closed = yield* fromLocalStore(
							"local-store close",
							localIssueStore.close(id, runtime.mutationSyncTarget, effectiveCwd),
						)
						if (!closed) {
							return yield* Effect.fail(
								new IssueTrackerError({
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

					yield* runBd(runtime, args, effectiveCwd)
				}),

			sync: (cwd?: string, options?: SyncOptions) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					if (runtime.configuredBackend === "local") {
						yield* Effect.log(
							"IssueTrackerClient.sync skipped: backend=local reason=local_only_backend",
						)
						return ZERO_SYNC_RESULT
					}

					// Check if tracker sync is enabled for the target project (config + network)
					const syncStatus = yield* getIssueTrackerSyncStatus(runtime)
					if (!syncStatus.enabled) {
						// Return empty result when offline - issues are tracked locally
						yield* Effect.log(
							`IssueTrackerClient.sync skipped: backend=${runtime.configuredBackend} projectPath=${runtime.projectPath} reason=${syncStatus.reason}`,
						)
						return ZERO_SYNC_RESULT
					}

					const effectiveCwd = runtime.effectiveCwd
					if (runtime.configuredBackend === "linear") {
						const backendSync = yield* resolveLinearBackendSync(runtime)
						if (backendSync === undefined) {
							yield* Effect.log(
								`IssueTrackerClient.sync skipped: backend=linear projectPath=${runtime.projectPath} reason=backend_route_unavailable`,
							)
							return ZERO_SYNC_RESULT
						}
						yield* Effect.log(
							`IssueTrackerClient.sync linear flush start: projectPath=${runtime.projectPath}`,
						)
						const syncResult = yield* fromIssueSync(
							"issue-sync flushLinearQueue",
							backendSync.flushQueue(runtime.projectPath, {
								hydrateRemote: options?.hydrateRemote,
							}),
						)
						yield* Effect.log(
							`IssueTrackerClient.sync linear flush complete: projectPath=${runtime.projectPath} pushed=${syncResult.pushed} pulled=${syncResult.pulled}`,
						)
						return syncResult
					}

					const output = yield* runBd(runtime, ["sync"], effectiveCwd)
					return yield* parseSyncResult(runtime, output)
				}),

			/**
			 * Import-only sync - re-imports tracker from JSONL into database without git operations.
			 * Use after git merge to recover any tracker that might have been incorrectly
			 * removed by the tracker merge driver.
			 */
			syncImportOnly: (cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					if (runtime.useLocalFirstPath) {
						return ZERO_SYNC_RESULT
					}

					const effectiveCwd = runtime.effectiveCwd
					const output = yield* runBd(runtime, ["sync", "--import-only"], effectiveCwd)
					return yield* parseSyncResult(runtime, output)
				}),

			recoverTombstones: (cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					if (runtime.configuredBackend === "linear" || runtime.configuredBackend === "local") {
						return 0
					}

					const effectiveCwd = runtime.effectiveCwd
					// Run recovery script that fixes tombstoned issues from JSONL
					// This is a workaround for tracker sync bug (see az-zby)
					const scriptPath = effectiveCwd
						? `${effectiveCwd}/.azedarach/recover-tombstones.sh`
						: ".azedarach/recover-tombstones.sh"

					const command = Command.make("bash", scriptPath).pipe(
						effectiveCwd ? Command.workingDirectory(effectiveCwd) : (x) => x,
					)

					const result = yield* Command.string(command).pipe(
						Effect.mapError((error) => {
							const stderr = "stderr" in error ? String(error.stderr) : String(error)
							return new IssueTrackerError({
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
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* ensureLinearReadSync(runtime)
						const issues = yield* fromLocalStore(
							"local-store ready",
							localIssueStore.ready(effectiveCwd),
						)
						return [...issues]
					}

					const output = yield* runBd(runtime, ["ready"], effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)
					// Filter out tombstone (deleted) issues
					return normalized.filter((issue) => issue.status !== "tombstone")
				}),

			search: (query: string, cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* ensureLinearReadSync(runtime)
						const issues = yield* fromLocalStore(
							"local-store search",
							localIssueStore.search(query, effectiveCwd),
						)
						return [...issues]
					}

					const output = yield* runBd(runtime, ["search", query], effectiveCwd)
					const parsed = yield* parseJson(Schema.Array(IssueSchema), output)
					const normalized = normalizeIssues(parsed)
					// Filter out tombstone (deleted) issues
					return normalized.filter((issue) => issue.status !== "tombstone")
				}),

			create: (params: {
				title: string
				type?: string
				status?: IssueStatus
				priority?: number
				description?: string
				design?: string
				acceptance?: string
				assignee?: string
				estimate?: number
				labels?: string[]
				implementations?: readonly string[]
				parent?: string
				cwd?: string
			}) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(params.cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						return yield* fromLocalStore(
							"local-store create",
							localIssueStore.create(
								{
									title: params.title,
									type: params.type,
									status: params.status,
									priority: params.priority,
									description: params.description,
									design: params.design,
									acceptance: params.acceptance,
									assignee: params.assignee,
									estimate: params.estimate,
									labels: params.labels,
									implementations: params.implementations,
									parent: params.parent,
								},
								runtime.mutationSyncTarget,
								effectiveCwd,
							),
						)
					}
					if (params.implementations !== undefined && params.implementations.length > 0) {
						return yield* Effect.fail(
							new IssueTrackerError({
								message:
									"Issue implementations require the local-first tracker path in ts-opentui.",
								command: `create ${params.title}`,
							}),
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
						// tracker create uses --labels with comma-separated values
						args.push("--labels", params.labels.join(","))
					}
					if (params.parent !== undefined) {
						args.push("--parent", params.parent)
					}

					const output = yield* runBd(runtime, args, effectiveCwd)

					// tracker create returns a single issue object (not an array)
					const parsed = yield* parseJson(IssueSchema, output)
					const createdIssue = normalizeIssue(parsed)
					if (params.status !== undefined && params.status !== "open") {
						yield* runBd(
							runtime,
							["update", createdIssue.id, "--status", params.status],
							effectiveCwd,
						)
						return {
							...createdIssue,
							status: params.status,
						}
					}
					return createdIssue
				}),

			listImplementations: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					const implementations = yield* fromLocalStore(
						"local-store listImplementations",
						localIssueStore.listImplementations(effectiveCwd),
					)
					return [...implementations]
				}),

			showImplementation: (name: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					return yield* fromLocalStore(
						"local-store showImplementation",
						localIssueStore.showImplementation(name, effectiveCwd),
					)
				}),

			getImplementationRegistry: (cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					return yield* fromLocalStore(
						"local-store getImplementationRegistry",
						localIssueStore.getImplementationRegistry(effectiveCwd),
					)
				}),

			createImplementation: (params: {
				name: string
				description?: string
				directory?: string
				setDefault?: boolean
				cwd?: string
			}) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(params.cwd)
					return yield* fromLocalStore(
						"local-store createImplementation",
						localIssueStore.createImplementation(
							{
								name: params.name,
								description: params.description,
								directory: params.directory,
								setDefault: params.setDefault,
							},
							effectiveCwd,
						),
					)
				}),

			updateImplementation: (
				currentName: string,
				fields: {
					name?: string
					description?: string | null
					directory?: string | null
					setDefault?: boolean
				},
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					return yield* fromLocalStore(
						"local-store updateImplementation",
						localIssueStore.updateImplementation(currentName, fields, effectiveCwd),
					)
				}),

			deleteImplementation: (name: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					return yield* fromLocalStore(
						"local-store deleteImplementation",
						localIssueStore.deleteImplementation(name, effectiveCwd),
					)
				}),

			setDefaultImplementation: (name: string, cwd?: string) =>
				Effect.gen(function* () {
					const effectiveCwd = yield* getEffectiveCwd(cwd)
					return yield* fromLocalStore(
						"local-store setDefaultImplementation",
						localIssueStore.setDefaultImplementation(name, effectiveCwd),
					)
				}),

			delete: (id: string, cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						const deleted = yield* fromLocalStore(
							"local-store delete",
							localIssueStore.delete(id, runtime.mutationSyncTarget, effectiveCwd),
						)
						if (!deleted) {
							return yield* Effect.fail(
								new IssueTrackerError({
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
					yield* runBdDirect(runtime, ["delete", id, "--force"], effectiveCwd)
				}),

			getEpicChildren: (epicId: string, cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* ensureLinearReadSync(runtime)
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

					const output = yield* runBd(runtime, ["show", epicId], effectiveCwd)

					// tracker show returns an array with a single item
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
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* ensureLinearReadSync(runtime)
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

					const output = yield* runBd(runtime, ["show", epicId], effectiveCwd)

					// tracker returns an array with a single item for show command
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
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* fromLocalStore(
							"local-store addDependency",
							localIssueStore.addDependency(
								issueId,
								dependsOnId,
								type ?? "blocks",
								runtime.mutationSyncTarget,
								effectiveCwd,
							),
						)
						return
					}

					const args: string[] = ["dep", "add", issueId, dependsOnId]

					if (type) {
						args.push("--type", type)
					}

					yield* runBd(runtime, args, effectiveCwd)
				}),

			removeDependency: (
				issueId: string,
				dependsOnId: string,
				type?: "blocks" | "related" | "parent-child" | "discovered-from",
				cwd?: string,
			) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* fromLocalStore(
							"local-store removeDependency",
							localIssueStore.removeDependency(
								issueId,
								dependsOnId,
								type,
								runtime.mutationSyncTarget,
								effectiveCwd,
							),
						)
						return
					}

					const args: string[] = ["dep", "remove", issueId, dependsOnId]
					if (type) {
						args.push("--type", type)
					}
					yield* runBd(runtime, args, effectiveCwd)
				}),

			getParentEpic: (issueId: string, cwd?: string) =>
				Effect.gen(function* () {
					const runtime = yield* resolveIssueTrackerRuntime(cwd)
					const effectiveCwd = runtime.effectiveCwd
					if (runtime.useLocalFirstPath) {
						yield* ensureLinearReadSync(runtime)
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

					const output = yield* runBd(runtime, ["show", issueId], effectiveCwd)

					// tracker show returns an array with a single item
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
					const epicOutput = yield* runBd(runtime, ["show", parentChildDep.id], effectiveCwd)
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
 * Complete IssueTrackerClient layer with all platform dependencies (legacy alias)
 *
 * @deprecated Use IssueTrackerClient.Default instead
 */
export const IssueTrackerClientLiveWithPlatform = IssueTrackerClient.Default

/**
 * Get all issues matching filters
 */
export const list = (
	filters?: IssueListFilters,
	cwd?: string,
	options?: IssueListOptions,
): Effect.Effect<
	Issue[],
	IssueTrackerError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.list(filters, cwd, options))

/**
 * Get a single issue by ID
 */
export const show = (
	id: string,
	cwd?: string,
): Effect.Effect<
	Issue,
	IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.show(id, cwd))

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
		implementations?: readonly string[]
	},
	cwd?: string,
): Effect.Effect<
	void,
	IssueTrackerError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.update(id, fields, cwd))

/**
 * Close an issue
 */
export const close = (
	id: string,
	reason?: string,
	cwd?: string,
): Effect.Effect<
	void,
	IssueTrackerError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.close(id, reason, cwd))

/**
 * Sync tracker database
 */
export const sync = (
	cwd?: string,
	options?: SyncOptions,
): Effect.Effect<
	SyncResult,
	IssueTrackerError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.sync(cwd, options))

/**
 * Get ready issues
 */
export const ready = (
	cwd?: string,
): Effect.Effect<
	Issue[],
	IssueTrackerError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.ready(cwd))

/**
 * Search issues
 */
export const search = (
	query: string,
	cwd?: string,
): Effect.Effect<
	Issue[],
	IssueTrackerError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.search(query, cwd))

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
	implementations?: readonly string[]
	parent?: string
	cwd?: string
}): Effect.Effect<
	Issue,
	IssueTrackerError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.create(params))

/**
 * Delete an issue
 */
export const deleteIssue = (
	id: string,
	cwd?: string,
): Effect.Effect<
	void,
	IssueTrackerError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.delete(id, cwd))

/**
 * Get an epic with its child tasks
 */
export const getEpicWithChildren = (
	epicId: string,
	cwd?: string,
): Effect.Effect<
	{ epic: Issue; children: ReadonlyArray<DependencyRef> },
	IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.getEpicWithChildren(epicId, cwd))

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
	IssueTrackerError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> =>
	Effect.flatMap(IssueTrackerClient, (client) =>
		client.addDependency(issueId, dependsOnId, type, cwd),
	)

/**
 * Remove dependency edge(s) between two issues
 */
export const removeDependency = (
	issueId: string,
	dependsOnId: string,
	type?: "blocks" | "related" | "parent-child" | "discovered-from",
	cwd?: string,
): Effect.Effect<
	void,
	IssueTrackerError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> =>
	Effect.flatMap(IssueTrackerClient, (client) =>
		client.removeDependency(issueId, dependsOnId, type, cwd),
	)

/**
 * Get the parent epic of an issue, if it has one
 */
export const getParentEpic = (
	issueId: string,
	cwd?: string,
): Effect.Effect<
	Issue | undefined,
	IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
	IssueTrackerClient | CommandExecutor.CommandExecutor
> => Effect.flatMap(IssueTrackerClient, (client) => client.getParentEpic(issueId, cwd))
