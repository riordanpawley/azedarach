import type { DateTime } from "effect"

export type IssueStatus = "open" | "in_progress" | "blocked" | "closed" | "tombstone"
export type IssueType = "bug" | "feature" | "task" | "epic" | "chore"
export type DependencyType = "blocks" | "related" | "parent-child" | "discovered-from"

export interface DependencyRef {
	readonly id: string
	readonly title?: string
	readonly status?: IssueStatus
	readonly dependency_type: DependencyType
	readonly issue_type?: IssueType
}

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

export interface TaskPhaseInfo {
	readonly phase: number
	readonly blockedBy: readonly string[]
}

export interface PhaseComputationResult {
	readonly phases: ReadonlyMap<string, TaskPhaseInfo>
	readonly maxPhase: number
	readonly phaseCounts: ReadonlyMap<number, number>
}

export type SpecRequirementKind = "functional" | "acceptance" | "other"
export type SpecLinkType = "implements" | "tests" | "blocks" | "relates"
export type SpecLinkFulfillmentStatus = "planned" | "partial" | "complete" | "verified"
export type SpecRequirementLookupSelector = "auto" | "id" | "local_id" | "external_code"

export interface SpecRequirement {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly body: string
	readonly kind: SpecRequirementKind
	readonly status: string
	readonly priority: number
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueLink {
	readonly issue_id: string
	readonly requirement_id: string
	readonly requirement_local_id: string
	readonly requirement_external_code: string | null
	readonly link_type: SpecLinkType
	readonly implementations: readonly string[]
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueRef {
	readonly id: string
	readonly title?: string
	readonly status?: string
	readonly issue_type?: string
	readonly link_type: SpecLinkType
	readonly implementations: readonly string[]
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
}

export interface SpecRequirementRef {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly kind: SpecRequirementKind
	readonly link_type: SpecLinkType
	readonly implementations: readonly string[]
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
}

export interface SpecRequirementWithStats extends SpecRequirement {
	readonly linked_issue_count: number
	readonly implemented_issue_count: number
}

export interface SpecRequirementListFilters {
	readonly query?: string
	readonly kind?: SpecRequirementKind
	readonly status?: string
	readonly priority?: number
}

export interface SpecCoverageGap {
	readonly kind: "unlinked_requirement" | "missing_issue" | "missing_requirement"
	readonly requirement_id?: string
	readonly issue_id?: string
	readonly message: string
}

export interface SpecCoverageReport {
	readonly requirements: readonly SpecRequirementWithStats[]
	readonly unlinked_requirement_ids: readonly string[]
	readonly fully_implemented_requirement_ids: readonly string[]
	readonly partially_implemented_requirement_ids: readonly string[]
	readonly integrity_gaps: readonly SpecCoverageGap[]
}

export interface SpecParityRequirement {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly implements_issue_ids: readonly string[]
	readonly partial_issue_ids: readonly string[]
	readonly tests_issue_ids: readonly string[]
	readonly other_issue_ids: readonly string[]
}

export interface SpecParityReport {
	readonly implementation: string
	readonly total_requirements: number
	readonly implemented_requirement_ids: readonly string[]
	readonly partially_implemented_requirement_ids: readonly string[]
	readonly tested_requirement_ids: readonly string[]
	readonly uncovered_requirement_ids: readonly string[]
	readonly related_only_requirement_ids: readonly string[]
	readonly requirements: readonly SpecParityRequirement[]
}

export type SpecSnapshotDocumentKey = "overview" | "requirements" | "acceptance" | "change_log"

export interface SpecMarkdownSyncDocumentResult {
	readonly key: SpecSnapshotDocumentKey
	readonly path: string
	readonly status: "unchanged" | "updated"
	readonly changed: boolean
}

export interface SpecMarkdownSyncResult {
	readonly out_dir: string
	readonly check: boolean
	readonly ok: boolean
	readonly total_documents: number
	readonly changed_documents: number
	readonly documents: readonly SpecMarkdownSyncDocumentResult[]
}

export interface SpecPublishConfig {
	readonly enabled: boolean
	readonly debounce_ms: number
	readonly target_project: string | null
	readonly documents: {
		readonly overview: string
		readonly requirements: string
		readonly acceptance: string
		readonly change_log: string
	}
}

export interface SpecPublishDocumentOutcome {
	readonly document_key: SpecSnapshotDocumentKey
	readonly title: string
	readonly status: "success" | "failed" | "skipped"
	readonly message: string
	readonly requirement_count: number
	readonly link_count: number
}

export interface SpecPublishOutcome {
	readonly started_at: DateTime.Utc
	readonly finished_at: DateTime.Utc
	readonly status: "success" | "partial" | "failed"
	readonly total_requirements: number
	readonly total_links: number
	readonly outcomes: readonly SpecPublishDocumentOutcome[]
}

export const DEFAULT_SPEC_PUBLISH_CONFIG: SpecPublishConfig = {
	enabled: false,
	debounce_ms: 2000,
	target_project: null,
	documents: {
		overview: "Spec Overview",
		requirements: "Requirements Index",
		acceptance: "Acceptance Index",
		change_log: "Change Log",
	},
}

export interface PlannedTask {
	readonly id: string
	readonly title: string
	readonly description: string
	readonly type: "task" | "bug" | "feature" | "chore"
	readonly priority: number
	readonly estimate?: number
	readonly dependsOn: readonly string[]
	readonly canParallelize: boolean
	readonly design?: string
	readonly acceptance?: string
}

export interface Plan {
	readonly epicTitle: string
	readonly epicDescription: string
	readonly summary: string
	readonly tasks: readonly PlannedTask[]
	readonly reviewNotes?: string
	readonly parallelizationScore?: number
}

export interface ReviewFeedback {
	readonly score: number
	readonly issues: readonly string[]
	readonly suggestions: readonly string[]
	readonly parallelizationOpportunities: readonly string[]
	readonly tasksTooLarge: readonly string[]
	readonly missingDependencies: ReadonlyArray<{
		readonly taskId: string
		readonly shouldDependOn: string
		readonly reason: string
	}>
	readonly isApproved: boolean
}

export interface PlanningState {
	readonly status:
		| "idle"
		| "generating"
		| "reviewing"
		| "refining"
		| "creating_issues"
		| "complete"
		| "error"
	readonly featureDescription: string | null
	readonly currentPlan: Plan | null
	readonly reviewPass: number
	readonly maxReviewPasses: number
	readonly reviewHistory: ReadonlyArray<ReviewFeedback>
	readonly createdIssues: ReadonlyArray<Issue>
	readonly error: string | null
}

export interface TmuxCapabilities {
	readonly inTmuxContext: boolean
	readonly tmuxActionsEnabled: boolean
}

export type VCStatus = "not_installed" | "stopped" | "starting" | "running" | "error"

export interface VCExecutorInfo {
	readonly status: VCStatus
	readonly sessionName: string
	readonly pid?: number
	readonly startedAt?: Date
	readonly lastActivity?: Date
}

export interface ImageAttachment {
	readonly id: string
	readonly filename: string
	readonly originalPath: string
	readonly mimeType: string
	readonly size: number
	readonly createdAt: string
}

export type DevServerStatus = "idle" | "starting" | "running" | "stopped" | "error"

export interface Project {
	readonly name: string
	readonly path: string
	readonly issueStorePath?: string
}

export type GotoSubMode = "pending" | "jump"
export interface JumpTarget {
	readonly taskId: string
	readonly columnIndex: number
	readonly taskIndex: number
}
export type SpecSubview = "requirements" | "coverage" | "parity" | "publish"
export type SortField = "session" | "priority" | "updated"
export type SortDirection = "asc" | "desc"
export type FilterSessionState =
	| "idle"
	| "initializing"
	| "busy"
	| "waiting"
	| "done"
	| "error"
	| "paused"
export type FilterField = "status" | "priority" | "type" | "session" | "age"

export interface FilterConfig {
	readonly status: ReadonlySet<IssueStatus>
	readonly priority: ReadonlySet<number>
	readonly type: ReadonlySet<IssueType>
	readonly session: ReadonlySet<FilterSessionState>
	readonly updatedDaysAgo: number | null
}

export const DEFAULT_FILTER_CONFIG: FilterConfig = {
	status: new Set<IssueStatus>(),
	priority: new Set<number>(),
	type: new Set<IssueType>(),
	session: new Set<FilterSessionState>(),
	updatedDaysAgo: null,
}

export interface SortConfig {
	readonly field: SortField
	readonly direction: SortDirection
}

export interface OrchestrationTask {
	readonly id: string
	readonly title: string
	readonly status: "open" | "in_progress" | "blocked" | "closed"
	readonly hasSession: boolean
}

export type EditorMode =
	| { readonly _tag: "normal" }
	| { readonly _tag: "select"; readonly selectedIds: ReadonlyArray<string> }
	| {
			readonly _tag: "goto"
			readonly gotoSubMode: GotoSubMode
			readonly jumpLabels: Readonly<Record<string, JumpTarget>> | null
			readonly pendingJumpKey: string | null
	  }
	| {
			readonly _tag: "action"
			readonly targetTaskId: string | null
			readonly selectedIds: ReadonlyArray<string>
	  }
	| { readonly _tag: "search"; readonly query: string }
	| { readonly _tag: "sort" }
	| { readonly _tag: "filter"; readonly activeField: FilterField | null }
	| {
			readonly _tag: "spec"
			readonly subview: SpecSubview
			readonly availableImplementations: ReadonlyArray<string>
			readonly selectedImplementation: string | null
	  }
	| {
			readonly _tag: "orchestrate"
			readonly epicId: string
			readonly epicTitle: string
			readonly childTasks: ReadonlyArray<OrchestrationTask>
			readonly selectedIds: ReadonlyArray<string>
			readonly focusIndex: number
	  }
	| {
			readonly _tag: "mergeSelect"
			readonly sourceIssueId: string
	  }

export type SettingValue = boolean | string | number

export interface SettingDefinition {
	readonly key: string
	readonly group: readonly string[]
	readonly label: string
	readonly getValue: (config: unknown) => SettingValue
	readonly nextValue?: (config: unknown) => unknown
	readonly isVisible?: (config: unknown) => boolean
}

export interface ScrollCommand {
	readonly target: "detail" | "diagnostics"
	readonly type: "line" | "halfPage"
	readonly amount: number
	readonly timestamp: number
}

export type DiagnosticSeverity = "info" | "warning" | "error"
export type FiberStatus = "running" | "completed" | "interrupted" | "failed"

export interface DiagnosticEvent {
	readonly id: string
	readonly timestamp: Date
	readonly severity: DiagnosticSeverity
	readonly source: string
	readonly message: string
	readonly details?: string
}

export interface RegisteredFiber {
	readonly id: string
	readonly name: string
	readonly description: string
	readonly startedAt: Date
	readonly status: FiberStatus
	readonly fiberId: string
	readonly endedAt?: Date
	readonly error?: string
}

export interface ServiceHealth {
	readonly name: string
	readonly status: "healthy" | "degraded" | "unhealthy"
	readonly lastActivity?: Date
	readonly details?: string
}

export type IssueDbPerfBackend = "linear"
export type IssueDbPerfOperationKind = "read" | "write"
export type IssueDbPerfLastStatus = "success" | "failure"

export type IssueSyncBackend = "linear" | "none"
export type IssueSyncLastStatus = "idle" | "success" | "failure" | "skipped"
export type IssueSyncRuntimeStatus = "ready" | "unavailable"
export type IssueSyncRuntimeReason =
	| "ready"
	| "backend_not_linear"
	| "sync_disabled"
	| "missing_api_key"
	| "config_error"
export type IssueSyncApiKeySource = "direnv" | "config-provider" | "none" | "unknown"
export type IssueSyncRunOperation = "bootstrap" | "flush"

export interface IssueSyncRuntimeHealth {
	readonly status: IssueSyncRuntimeStatus
	readonly reason: IssueSyncRuntimeReason
	readonly projectPath: string
	readonly configuredTeam?: string
	readonly configuredProject?: string
	readonly apiKeySource: IssueSyncApiKeySource
	readonly updatedAt: Date
}

export interface IssueSyncQueueHealth {
	readonly total: number
	readonly pendingReady: number
	readonly pendingDelayed: number
	readonly processingActive: number
	readonly processingStale: number
	readonly failed: number
	readonly updatedAt: Date
}

export interface IssueSyncRunHealth {
	readonly runId: string
	readonly operation: IssueSyncRunOperation
	readonly status: Exclude<IssueSyncLastStatus, "idle">
	readonly startedAt: Date
	readonly finishedAt: Date
	readonly message: string
	readonly pushed: number
	readonly pulled: number
}

export interface IssueSyncFailure {
	readonly issueId: string
	readonly operation: "bootstrap" | "flush" | "upsert" | "close" | "delete"
	readonly error: string
	readonly attempts: number
	readonly occurredAt: Date
}

export interface IssueSyncHealth {
	readonly backend: IssueSyncBackend
	readonly syncEnabled: boolean
	readonly queueDepth: number
	readonly failedCount: number
	readonly lastSyncedAt?: Date
	readonly lastStatus: IssueSyncLastStatus
	readonly lastMessage: string
	readonly lastFailure?: IssueSyncFailure
	readonly runtime?: IssueSyncRuntimeHealth
	readonly queue?: IssueSyncQueueHealth
	readonly lastRun?: IssueSyncRunHealth
}

export type LinearWebhookMode = "disabled" | "cli" | "misconfigured" | "sdk" | "failed"

export type LinearWebhookStrategy =
	| "disabled"
	| "sdk-events"
	| "cli-listener"
	| "cli-fallback-listener"
	| "polling-fallback"

export interface LinearWebhookHealth {
	readonly mode: LinearWebhookMode
	readonly strategy: LinearWebhookStrategy
	readonly healthy: boolean
	readonly message: string
	readonly updatedAt: Date
}

export interface IssueDbPerfStats {
	readonly backend: IssueDbPerfBackend
	readonly operation: string
	readonly kind: IssueDbPerfOperationKind
	readonly count: number
	readonly failureCount: number
	readonly avgMs: number
	readonly p50Ms: number
	readonly p95Ms: number
	readonly maxMs: number
	readonly lastMs: number
	readonly lastStatus: IssueDbPerfLastStatus
	readonly lastAt: Date
}

export interface DiagnosticsState {
	readonly fibers: readonly RegisteredFiber[]
	readonly services: readonly ServiceHealth[]
	readonly events: readonly DiagnosticEvent[]
	readonly issueDbPerf: readonly IssueDbPerfStats[]
	readonly issueSync?: IssueSyncHealth
	readonly linearWebhook?: LinearWebhookHealth
	readonly lastUpdated: Date
}
