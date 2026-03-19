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

export const DaemonSpecRequirementIssuesRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	reference: Schema.String,
	selector: Schema.optional(SpecRequirementLookupSelectorSchema),
})
export type DaemonSpecRequirementIssuesRequest = Schema.Schema.Type<
	typeof DaemonSpecRequirementIssuesRequestSchema
>

export const DaemonSpecRequirementIssuesResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	linkedIssues: Schema.Array(SpecIssueRefSchema),
})
export type DaemonSpecRequirementIssuesResult = Schema.Schema.Type<
	typeof DaemonSpecRequirementIssuesResultSchema
>

export const SpecRequirementCreateInputSchema = Schema.Struct({
	id: Schema.optional(Schema.String),
	local_id: Schema.optional(Schema.String),
	external_code: Schema.optional(Schema.String),
	title: Schema.String,
	body: Schema.String,
	kind: Schema.optional(SpecRequirementKindSchema),
	status: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
})
export type SpecRequirementCreateInput = Schema.Schema.Type<typeof SpecRequirementCreateInputSchema>

export const SpecRequirementUpdateFieldsSchema = Schema.Struct({
	title: Schema.optional(Schema.String),
	body: Schema.optional(Schema.String),
	kind: Schema.optional(SpecRequirementKindSchema),
	status: Schema.optional(Schema.String),
	priority: Schema.optional(Schema.Number),
})
export type SpecRequirementUpdateFields = Schema.Schema.Type<
	typeof SpecRequirementUpdateFieldsSchema
>

export const SpecLinkListFiltersSchema = Schema.Struct({
	issueId: Schema.optional(Schema.String),
	requirementId: Schema.optional(Schema.String),
	requirementSelector: Schema.optional(SpecRequirementLookupSelectorSchema),
	implementation: Schema.optional(Schema.String),
})
export type SpecLinkListFilters = Schema.Schema.Type<typeof SpecLinkListFiltersSchema>

export const SpecLinkAddFulfillmentSchema = Schema.Struct({
	status: Schema.optional(SpecLinkFulfillmentStatusSchema),
	percent: Schema.optional(Schema.NullOr(Schema.Number)),
	evidenceNote: Schema.optional(Schema.NullOr(Schema.String)),
})
export type SpecLinkAddFulfillment = Schema.Schema.Type<typeof SpecLinkAddFulfillmentSchema>

export const SpecLinkUpdateFieldsSchema = Schema.Struct({
	status: Schema.optional(SpecLinkFulfillmentStatusSchema),
	percent: Schema.optional(Schema.NullOr(Schema.Number)),
	evidenceNote: Schema.optional(Schema.NullOr(Schema.String)),
})
export type SpecLinkUpdateFields = Schema.Schema.Type<typeof SpecLinkUpdateFieldsSchema>

export const SpecPublishConfigSchema = Schema.Struct({
	enabled: Schema.Boolean,
	debounce_ms: Schema.Number,
	target_project: Schema.NullOr(Schema.String),
	documents: Schema.Struct({
		overview: Schema.String,
		requirements: Schema.String,
		acceptance: Schema.String,
		change_log: Schema.String,
	}),
})
export type SpecPublishConfig = Schema.Schema.Type<typeof SpecPublishConfigSchema>

export const SpecPublishDocumentOutcomeSchema = Schema.Struct({
	document_key: Schema.Literal("overview", "requirements", "acceptance", "change_log"),
	title: Schema.String,
	status: Schema.Literal("success", "failed", "skipped"),
	message: Schema.String,
	requirement_count: Schema.Number,
	link_count: Schema.Number,
})
export type SpecPublishDocumentOutcome = Schema.Schema.Type<typeof SpecPublishDocumentOutcomeSchema>

export const SpecPublishOutcomeSchema = Schema.Struct({
	started_at: Schema.String,
	finished_at: Schema.String,
	status: Schema.Literal("success", "partial", "failed"),
	total_requirements: Schema.Number,
	total_links: Schema.Number,
	outcomes: Schema.Array(SpecPublishDocumentOutcomeSchema),
})
export type SpecPublishOutcome = Schema.Schema.Type<typeof SpecPublishOutcomeSchema>

export const SpecMarkdownSyncDocumentResultSchema = Schema.Struct({
	key: Schema.Literal("overview", "requirements", "acceptance", "change_log"),
	path: Schema.String,
	status: Schema.Literal("updated", "unchanged"),
	changed: Schema.Boolean,
})
export type SpecMarkdownSyncDocumentResult = Schema.Schema.Type<
	typeof SpecMarkdownSyncDocumentResultSchema
>

export const SpecMarkdownSyncResultSchema = Schema.Struct({
	out_dir: Schema.String,
	check: Schema.Boolean,
	ok: Schema.Boolean,
	total_documents: Schema.Number,
	changed_documents: Schema.Number,
	documents: Schema.Array(SpecMarkdownSyncDocumentResultSchema),
})
export type SpecMarkdownSyncResult = Schema.Schema.Type<typeof SpecMarkdownSyncResultSchema>

export const DaemonSpecRequirementCreateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	input: SpecRequirementCreateInputSchema,
})
export type DaemonSpecRequirementCreateRequest = Schema.Schema.Type<
	typeof DaemonSpecRequirementCreateRequestSchema
>

export const DaemonSpecRequirementCreateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	requirement: SpecRequirementSchema,
})
export type DaemonSpecRequirementCreateResult = Schema.Schema.Type<
	typeof DaemonSpecRequirementCreateResultSchema
>

export const DaemonSpecRequirementUpdateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	reference: Schema.String,
	selector: Schema.optional(SpecRequirementLookupSelectorSchema),
	fields: SpecRequirementUpdateFieldsSchema,
})
export type DaemonSpecRequirementUpdateRequest = Schema.Schema.Type<
	typeof DaemonSpecRequirementUpdateRequestSchema
>

export const DaemonSpecRequirementUpdateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	updated: Schema.Boolean,
})
export type DaemonSpecRequirementUpdateResult = Schema.Schema.Type<
	typeof DaemonSpecRequirementUpdateResultSchema
>

export const DaemonSpecRequirementDeleteRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	reference: Schema.String,
	selector: Schema.optional(SpecRequirementLookupSelectorSchema),
})
export type DaemonSpecRequirementDeleteRequest = Schema.Schema.Type<
	typeof DaemonSpecRequirementDeleteRequestSchema
>

export const DaemonSpecRequirementDeleteResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	deleted: Schema.Boolean,
})
export type DaemonSpecRequirementDeleteResult = Schema.Schema.Type<
	typeof DaemonSpecRequirementDeleteResultSchema
>

export const DaemonSpecLinkListRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	filters: Schema.optional(SpecLinkListFiltersSchema),
})
export type DaemonSpecLinkListRequest = Schema.Schema.Type<typeof DaemonSpecLinkListRequestSchema>

export const DaemonSpecLinkListResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	links: Schema.Array(SpecIssueLinkSchema),
})
export type DaemonSpecLinkListResult = Schema.Schema.Type<typeof DaemonSpecLinkListResultSchema>

export const DaemonSpecLinkAddRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	issueId: Schema.String,
	requirementReference: Schema.String,
	linkType: Schema.optional(SpecLinkTypeSchema),
	requirementSelector: Schema.optional(SpecRequirementLookupSelectorSchema),
	implementations: Schema.optional(Schema.Array(Schema.String)),
	fulfillment: Schema.optional(SpecLinkAddFulfillmentSchema),
})
export type DaemonSpecLinkAddRequest = Schema.Schema.Type<typeof DaemonSpecLinkAddRequestSchema>

export const DaemonSpecLinkAddResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	added: Schema.Boolean,
})
export type DaemonSpecLinkAddResult = Schema.Schema.Type<typeof DaemonSpecLinkAddResultSchema>

export const DaemonSpecLinkRemoveRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	issueId: Schema.String,
	requirementReference: Schema.String,
	linkType: Schema.optional(SpecLinkTypeSchema),
	requirementSelector: Schema.optional(SpecRequirementLookupSelectorSchema),
	implementations: Schema.optional(Schema.Array(Schema.String)),
})
export type DaemonSpecLinkRemoveRequest = Schema.Schema.Type<
	typeof DaemonSpecLinkRemoveRequestSchema
>

export const DaemonSpecLinkRemoveResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	removed: Schema.Number,
})
export type DaemonSpecLinkRemoveResult = Schema.Schema.Type<typeof DaemonSpecLinkRemoveResultSchema>

export const DaemonSpecLinkUpdateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	issueId: Schema.String,
	requirementReference: Schema.String,
	fields: SpecLinkUpdateFieldsSchema,
	linkType: Schema.optional(SpecLinkTypeSchema),
	requirementSelector: Schema.optional(SpecRequirementLookupSelectorSchema),
	implementations: Schema.optional(Schema.Array(Schema.String)),
})
export type DaemonSpecLinkUpdateRequest = Schema.Schema.Type<
	typeof DaemonSpecLinkUpdateRequestSchema
>

export const DaemonSpecLinkUpdateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	updated: Schema.Number,
})
export type DaemonSpecLinkUpdateResult = Schema.Schema.Type<typeof DaemonSpecLinkUpdateResultSchema>

export const DaemonSpecPublishConfigGetRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSpecPublishConfigGetRequest = Schema.Schema.Type<
	typeof DaemonSpecPublishConfigGetRequestSchema
>

export const DaemonSpecPublishConfigGetResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	config: SpecPublishConfigSchema,
})
export type DaemonSpecPublishConfigGetResult = Schema.Schema.Type<
	typeof DaemonSpecPublishConfigGetResultSchema
>

export const DaemonSpecPublishConfigSetRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	config: SpecPublishConfigSchema,
})
export type DaemonSpecPublishConfigSetRequest = Schema.Schema.Type<
	typeof DaemonSpecPublishConfigSetRequestSchema
>

export const DaemonSpecPublishConfigSetResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	updated: Schema.Boolean,
})
export type DaemonSpecPublishConfigSetResult = Schema.Schema.Type<
	typeof DaemonSpecPublishConfigSetResultSchema
>

export const DaemonSpecPublishOutcomeGetRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSpecPublishOutcomeGetRequest = Schema.Schema.Type<
	typeof DaemonSpecPublishOutcomeGetRequestSchema
>

export const DaemonSpecPublishOutcomeGetResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	last_outcome: Schema.NullOr(SpecPublishOutcomeSchema),
})
export type DaemonSpecPublishOutcomeGetResult = Schema.Schema.Type<
	typeof DaemonSpecPublishOutcomeGetResultSchema
>

export const DaemonSpecSyncMarkdownRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	outDir: Schema.optional(Schema.String),
	check: Schema.optional(Schema.Boolean),
})
export type DaemonSpecSyncMarkdownRequest = Schema.Schema.Type<
	typeof DaemonSpecSyncMarkdownRequestSchema
>

export const DaemonSpecSyncMarkdownResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	sync: SpecMarkdownSyncResultSchema,
})
export type DaemonSpecSyncMarkdownResult = Schema.Schema.Type<
	typeof DaemonSpecSyncMarkdownResultSchema
>

export const DaemonSpecPublishRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSpecPublishRequest = Schema.Schema.Type<typeof DaemonSpecPublishRequestSchema>

export const DaemonSpecPublishResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	outcome: SpecPublishOutcomeSchema,
})
export type DaemonSpecPublishResult = Schema.Schema.Type<typeof DaemonSpecPublishResultSchema>
