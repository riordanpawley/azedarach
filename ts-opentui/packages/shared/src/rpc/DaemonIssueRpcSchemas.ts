import { Schema } from "effect"
import { DAEMON_RPC_PROTOCOL_VERSION } from "./DaemonRpcSchemas.js"

const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

export const IssueStatusSchema = Schema.Literal(
	"open",
	"in_progress",
	"blocked",
	"closed",
	"tombstone",
)
export type IssueStatus = Schema.Schema.Type<typeof IssueStatusSchema>

export const IssueTypeSchema = Schema.Literal("bug", "feature", "task", "epic", "chore")
export type IssueType = Schema.Schema.Type<typeof IssueTypeSchema>

export const DependencyTypeSchema = Schema.Literal(
	"blocks",
	"related",
	"parent-child",
	"discovered-from",
)
export type DependencyType = Schema.Schema.Type<typeof DependencyTypeSchema>

export const TrackedIssueRelationshipRefSchema = Schema.Struct({
	id: Schema.String,
	dependency_type: DependencyTypeSchema,
	title: Schema.optional(Schema.String),
	status: Schema.optional(IssueStatusSchema),
	issue_type: Schema.optional(IssueTypeSchema),
})
export type TrackedIssueRelationshipRef = Schema.Schema.Type<
	typeof TrackedIssueRelationshipRefSchema
>

export const TrackedIssueSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	status: IssueStatusSchema,
	priority: Schema.Number,
	issue_type: IssueTypeSchema,
	created_at: Schema.String,
	updated_at: Schema.String,
	closed_at: Schema.optional(Schema.NullOr(Schema.String)),
	assignee: Schema.optional(Schema.NullOr(Schema.String)),
	description: Schema.optional(Schema.String),
	design: Schema.optional(Schema.String),
	acceptance: Schema.optional(Schema.String),
	notes: Schema.optional(Schema.String),
	estimate: Schema.optional(Schema.Number),
	labels: Schema.optional(Schema.Array(Schema.String)),
	implementations: Schema.Array(Schema.String),
	dependencies: Schema.optional(Schema.Array(TrackedIssueRelationshipRefSchema)),
	dependents: Schema.optional(Schema.Array(TrackedIssueRelationshipRefSchema)),
	dependency_count: Schema.optional(Schema.Number),
	dependent_count: Schema.optional(Schema.Number),
})
export type TrackedIssue = Schema.Schema.Type<typeof TrackedIssueSchema>

export const IssueListFiltersSchema = Schema.Struct({
	status: Schema.optional(IssueStatusSchema),
	priority: Schema.optional(Schema.Number),
	type: Schema.optional(IssueTypeSchema),
	parent: Schema.optional(Schema.String),
	implementations: Schema.optional(Schema.Array(Schema.String)),
})
export type IssueListFilters = Schema.Schema.Type<typeof IssueListFiltersSchema>

export const IssueListOptionsSchema = Schema.Struct({
	limit: Schema.optional(Schema.Number),
	includeClosed: Schema.optional(Schema.Boolean),
	sortBy: Schema.optional(Schema.Literal("updated_at", "created_at")),
	sortDirection: Schema.optional(Schema.Literal("asc", "desc")),
})
export type IssueListOptions = Schema.Schema.Type<typeof IssueListOptionsSchema>

export const DaemonIssueSyncResultSchema = Schema.Struct({
	pushed: Schema.Number,
	pulled: Schema.Number,
})
export type DaemonIssueSyncResult = Schema.Schema.Type<typeof DaemonIssueSyncResultSchema>

export const DaemonIssueGetRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
	maxSyncWaitMs: Schema.optional(Schema.Number),
})
export type DaemonIssueGetRequest = Schema.Schema.Type<typeof DaemonIssueGetRequestSchema>

export const DaemonIssueGetResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	issue: TrackedIssueSchema,
})
export type DaemonIssueGetResult = Schema.Schema.Type<typeof DaemonIssueGetResultSchema>

export const DaemonIssueListRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	filters: Schema.optional(IssueListFiltersSchema),
	options: Schema.optional(IssueListOptionsSchema),
})
export type DaemonIssueListRequest = Schema.Schema.Type<typeof DaemonIssueListRequestSchema>

export const DaemonIssueListResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	issues: Schema.Array(TrackedIssueSchema),
})
export type DaemonIssueListResult = Schema.Schema.Type<typeof DaemonIssueListResultSchema>

export const DaemonIssueCreateInputSchema = Schema.Struct({
	title: Schema.String,
	type: Schema.optional(IssueTypeSchema),
	status: Schema.optional(IssueStatusSchema),
	priority: Schema.optional(Schema.Number),
	description: Schema.optional(Schema.String),
	design: Schema.optional(Schema.String),
	acceptance: Schema.optional(Schema.String),
	assignee: Schema.optional(Schema.String),
	estimate: Schema.optional(Schema.Number),
	labels: Schema.optional(Schema.Array(Schema.String)),
	implementations: Schema.optional(Schema.Array(Schema.String)),
	parent: Schema.optional(Schema.String),
})
export type DaemonIssueCreateInput = Schema.Schema.Type<typeof DaemonIssueCreateInputSchema>

export const DaemonIssueCreateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	input: DaemonIssueCreateInputSchema,
})
export type DaemonIssueCreateRequest = Schema.Schema.Type<typeof DaemonIssueCreateRequestSchema>

export const DaemonIssueCreateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	issue: TrackedIssueSchema,
})
export type DaemonIssueCreateResult = Schema.Schema.Type<typeof DaemonIssueCreateResultSchema>

export const DaemonIssueUpdatePatchSchema = Schema.Struct({
	status: Schema.optional(IssueStatusSchema),
	notes: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
	title: Schema.optional(Schema.String),
	type: Schema.optional(IssueTypeSchema),
	description: Schema.optional(Schema.String),
	design: Schema.optional(Schema.String),
	acceptance: Schema.optional(Schema.String),
	assignee: Schema.optional(Schema.String),
	estimate: Schema.optional(Schema.Number),
	labels: Schema.optional(Schema.Array(Schema.String)),
	implementations: Schema.optional(Schema.Array(Schema.String)),
	parent: Schema.optional(Schema.String),
})
export type DaemonIssueUpdatePatch = Schema.Schema.Type<typeof DaemonIssueUpdatePatchSchema>

export const DaemonIssueUpdateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
	patch: DaemonIssueUpdatePatchSchema,
})
export type DaemonIssueUpdateRequest = Schema.Schema.Type<typeof DaemonIssueUpdateRequestSchema>

export const DaemonIssueUpdateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	updated: Schema.Literal(true),
})
export type DaemonIssueUpdateResult = Schema.Schema.Type<typeof DaemonIssueUpdateResultSchema>

export const DaemonIssueAddDependencyRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	dependsOnId: Schema.String,
	dependencyType: DependencyTypeSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonIssueAddDependencyRequest = Schema.Schema.Type<
	typeof DaemonIssueAddDependencyRequestSchema
>

export const DaemonIssueRemoveDependencyRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	dependsOnId: Schema.String,
	dependencyType: Schema.optional(DependencyTypeSchema),
	projectPath: Schema.optional(Schema.String),
})
export type DaemonIssueRemoveDependencyRequest = Schema.Schema.Type<
	typeof DaemonIssueRemoveDependencyRequestSchema
>

export const DaemonIssueDependencyMutationResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	updated: Schema.Literal(true),
})
export type DaemonIssueDependencyMutationResult = Schema.Schema.Type<
	typeof DaemonIssueDependencyMutationResultSchema
>

export const DaemonIssueCloseRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	reason: Schema.optional(Schema.String),
	projectPath: Schema.optional(Schema.String),
})
export type DaemonIssueCloseRequest = Schema.Schema.Type<typeof DaemonIssueCloseRequestSchema>

export const DaemonIssueCloseResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	closed: Schema.Literal(true),
})
export type DaemonIssueCloseResult = Schema.Schema.Type<typeof DaemonIssueCloseResultSchema>

export const DaemonIssueDeleteRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonIssueDeleteRequest = Schema.Schema.Type<typeof DaemonIssueDeleteRequestSchema>

export const DaemonIssueDeleteResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	deleted: Schema.Literal(true),
})
export type DaemonIssueDeleteResult = Schema.Schema.Type<typeof DaemonIssueDeleteResultSchema>

export const DaemonIssueSyncRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	hydrateRemote: Schema.optional(Schema.Boolean),
})
export type DaemonIssueSyncRequest = Schema.Schema.Type<typeof DaemonIssueSyncRequestSchema>

export const DaemonIssueSyncResultEnvelopeSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	sync: DaemonIssueSyncResultSchema,
})
export type DaemonIssueSyncResultEnvelope = Schema.Schema.Type<
	typeof DaemonIssueSyncResultEnvelopeSchema
>
