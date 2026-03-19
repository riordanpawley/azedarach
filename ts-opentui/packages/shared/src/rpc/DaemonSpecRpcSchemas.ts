import { Schema } from "effect"
import { DAEMON_RPC_PROTOCOL_VERSION } from "./DaemonRpcSchemas.js"

const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

export const SpecRequirementKindSchema = Schema.Literal("functional", "acceptance", "other")
export type SpecRequirementKind = Schema.Schema.Type<typeof SpecRequirementKindSchema>

export const SpecLinkTypeSchema = Schema.Literal("implements", "tests", "blocks", "relates")
export type SpecLinkType = Schema.Schema.Type<typeof SpecLinkTypeSchema>

export const SpecLinkFulfillmentStatusSchema = Schema.Literal(
	"planned",
	"partial",
	"complete",
	"verified",
)
export type SpecLinkFulfillmentStatus = Schema.Schema.Type<typeof SpecLinkFulfillmentStatusSchema>

export const SpecRequirementLookupSelectorSchema = Schema.Literal(
	"auto",
	"id",
	"local_id",
	"external_code",
)
export type SpecRequirementLookupSelector = Schema.Schema.Type<
	typeof SpecRequirementLookupSelectorSchema
>

export const SpecRequirementSchema = Schema.Struct({
	id: Schema.String,
	local_id: Schema.String,
	external_code: Schema.NullOr(Schema.String),
	title: Schema.String,
	body: Schema.String,
	kind: SpecRequirementKindSchema,
	status: Schema.String,
	priority: Schema.Number,
	created_at: Schema.String,
	updated_at: Schema.String,
})
export type SpecRequirement = Schema.Schema.Type<typeof SpecRequirementSchema>

export const SpecIssueLinkSchema = Schema.Struct({
	issue_id: Schema.String,
	requirement_id: Schema.String,
	requirement_local_id: Schema.String,
	requirement_external_code: Schema.NullOr(Schema.String),
	link_type: SpecLinkTypeSchema,
	implementations: Schema.Array(Schema.String),
	fulfillment_status: SpecLinkFulfillmentStatusSchema,
	fulfillment_percent: Schema.NullOr(Schema.Number),
	evidence_note: Schema.NullOr(Schema.String),
	created_at: Schema.String,
	updated_at: Schema.String,
})
export type SpecIssueLink = Schema.Schema.Type<typeof SpecIssueLinkSchema>

export const SpecIssueRefSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.optional(Schema.String),
	status: Schema.optional(Schema.String),
	issue_type: Schema.optional(Schema.String),
	link_type: SpecLinkTypeSchema,
	implementations: Schema.Array(Schema.String),
	fulfillment_status: SpecLinkFulfillmentStatusSchema,
	fulfillment_percent: Schema.NullOr(Schema.Number),
	evidence_note: Schema.NullOr(Schema.String),
})
export type SpecIssueRef = Schema.Schema.Type<typeof SpecIssueRefSchema>

export const SpecRequirementRefSchema = Schema.Struct({
	id: Schema.String,
	local_id: Schema.String,
	external_code: Schema.NullOr(Schema.String),
	title: Schema.String,
	kind: SpecRequirementKindSchema,
	link_type: SpecLinkTypeSchema,
	implementations: Schema.Array(Schema.String),
	fulfillment_status: SpecLinkFulfillmentStatusSchema,
	fulfillment_percent: Schema.NullOr(Schema.Number),
	evidence_note: Schema.NullOr(Schema.String),
})
export type SpecRequirementRef = Schema.Schema.Type<typeof SpecRequirementRefSchema>

export const SpecRequirementWithStatsSchema = Schema.Struct({
	id: Schema.String,
	local_id: Schema.String,
	external_code: Schema.NullOr(Schema.String),
	title: Schema.String,
	body: Schema.String,
	kind: SpecRequirementKindSchema,
	status: Schema.String,
	priority: Schema.Number,
	created_at: Schema.String,
	updated_at: Schema.String,
	linked_issue_count: Schema.Number,
	implemented_issue_count: Schema.Number,
})
export type SpecRequirementWithStats = Schema.Schema.Type<typeof SpecRequirementWithStatsSchema>

export const SpecRequirementListFiltersSchema = Schema.Struct({
	query: Schema.optional(Schema.String),
	kind: Schema.optional(SpecRequirementKindSchema),
	status: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
})
export type SpecRequirementListFilters = Schema.Schema.Type<typeof SpecRequirementListFiltersSchema>

export const SpecCoverageGapSchema = Schema.Struct({
	kind: Schema.Literal("unlinked_requirement", "missing_issue", "missing_requirement"),
	requirement_id: Schema.optional(Schema.String),
	issue_id: Schema.optional(Schema.String),
	message: Schema.String,
})
export type SpecCoverageGap = Schema.Schema.Type<typeof SpecCoverageGapSchema>

export const SpecCoverageReportSchema = Schema.Struct({
	requirements: Schema.Array(SpecRequirementWithStatsSchema),
	unlinked_requirement_ids: Schema.Array(Schema.String),
	fully_implemented_requirement_ids: Schema.Array(Schema.String),
	partially_implemented_requirement_ids: Schema.Array(Schema.String),
	integrity_gaps: Schema.Array(SpecCoverageGapSchema),
})
export type SpecCoverageReport = Schema.Schema.Type<typeof SpecCoverageReportSchema>

export const SpecParityRequirementSchema = Schema.Struct({
	id: Schema.String,
	local_id: Schema.String,
	external_code: Schema.NullOr(Schema.String),
	title: Schema.String,
	implements_issue_ids: Schema.Array(Schema.String),
	partial_issue_ids: Schema.Array(Schema.String),
	tests_issue_ids: Schema.Array(Schema.String),
	other_issue_ids: Schema.Array(Schema.String),
})
export type SpecParityRequirement = Schema.Schema.Type<typeof SpecParityRequirementSchema>

export const SpecParityReportSchema = Schema.Struct({
	implementation: Schema.String,
	total_requirements: Schema.Number,
	implemented_requirement_ids: Schema.Array(Schema.String),
	partially_implemented_requirement_ids: Schema.Array(Schema.String),
	tested_requirement_ids: Schema.Array(Schema.String),
	uncovered_requirement_ids: Schema.Array(Schema.String),
	related_only_requirement_ids: Schema.Array(Schema.String),
	requirements: Schema.Array(SpecParityRequirementSchema),
})
export type SpecParityReport = Schema.Schema.Type<typeof SpecParityReportSchema>

export const SpecLintResultSchema = Schema.Struct({
	ok: Schema.Boolean,
	requirement_count: Schema.Number,
	linked_requirement_count: Schema.Number,
	unlinked_requirement_count: Schema.Number,
	integrity_gap_count: Schema.Number,
	gap_counts: Schema.Struct({
		unlinked_requirement: Schema.Number,
		missing_issue: Schema.Number,
		missing_requirement: Schema.Number,
	}),
	report: SpecCoverageReportSchema,
})
export type SpecLintResult = Schema.Schema.Type<typeof SpecLintResultSchema>

export const DaemonSpecRequirementListRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	query: Schema.optional(Schema.String),
	kind: Schema.optional(SpecRequirementKindSchema),
	status: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
})
export type DaemonSpecRequirementListRequest = Schema.Schema.Type<
	typeof DaemonSpecRequirementListRequestSchema
>

export const DaemonSpecRequirementListResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	requirements: Schema.Array(SpecRequirementSchema),
})
export type DaemonSpecRequirementListResult = Schema.Schema.Type<
	typeof DaemonSpecRequirementListResultSchema
>

export const DaemonSpecRequirementGetRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	reference: Schema.String,
	selector: Schema.optional(SpecRequirementLookupSelectorSchema),
})
export type DaemonSpecRequirementGetRequest = Schema.Schema.Type<
	typeof DaemonSpecRequirementGetRequestSchema
>

export const DaemonSpecRequirementGetResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	requirement: Schema.NullOr(SpecRequirementSchema),
})
export type DaemonSpecRequirementGetResult = Schema.Schema.Type<
	typeof DaemonSpecRequirementGetResultSchema
>

export const DaemonSpecReadRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSpecReadRequest = Schema.Schema.Type<typeof DaemonSpecReadRequestSchema>

export const DaemonSpecReadResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	requirements: Schema.Array(SpecRequirementSchema),
	links: Schema.Array(SpecIssueLinkSchema),
	coverage: SpecCoverageReportSchema,
})
export type DaemonSpecReadResult = Schema.Schema.Type<typeof DaemonSpecReadResultSchema>

export const DaemonSpecLintRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSpecLintRequest = Schema.Schema.Type<typeof DaemonSpecLintRequestSchema>

export const DaemonSpecLintResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	lint: SpecLintResultSchema,
})
export type DaemonSpecLintResult = Schema.Schema.Type<typeof DaemonSpecLintResultSchema>

export const DaemonSpecParityRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	implementation: Schema.optional(Schema.String),
})
export type DaemonSpecParityRequest = Schema.Schema.Type<typeof DaemonSpecParityRequestSchema>

export const DaemonSpecParityResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	report: SpecParityReportSchema,
})
export type DaemonSpecParityResult = Schema.Schema.Type<typeof DaemonSpecParityResultSchema>

export const DaemonSpecIssueLinksRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	issueId: Schema.String,
})
export type DaemonSpecIssueLinksRequest = Schema.Schema.Type<
	typeof DaemonSpecIssueLinksRequestSchema
>

export const DaemonSpecIssueLinksResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	linkedRequirements: Schema.Array(SpecRequirementRefSchema),
})
export type DaemonSpecIssueLinksResult = Schema.Schema.Type<typeof DaemonSpecIssueLinksResultSchema>
