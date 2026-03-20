import { Schema } from "effect"
import { TrackedIssueSchema } from "./DaemonIssueRpcSchemas.js"
import { DAEMON_RPC_PROTOCOL_VERSION } from "./DaemonRpcSchemas.js"

const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

export const PlanningTaskSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	description: Schema.String,
	type: Schema.Literal("task", "bug", "feature", "chore"),
	priority: Schema.Number,
	estimate: Schema.optional(Schema.Number),
	dependsOn: Schema.Array(Schema.String),
	canParallelize: Schema.Boolean,
	design: Schema.optional(Schema.String),
	acceptance: Schema.optional(Schema.String),
})
export type PlanningTask = Schema.Schema.Type<typeof PlanningTaskSchema>

export const PlanningPlanSchema = Schema.Struct({
	epicTitle: Schema.String,
	epicDescription: Schema.String,
	summary: Schema.String,
	tasks: Schema.Array(PlanningTaskSchema),
	reviewNotes: Schema.optional(Schema.String),
	parallelizationScore: Schema.optional(Schema.Number),
})
export type PlanningPlan = Schema.Schema.Type<typeof PlanningPlanSchema>

export const PlanningReviewMissingDependencySchema = Schema.Struct({
	taskId: Schema.String,
	shouldDependOn: Schema.String,
	reason: Schema.String,
})
export type PlanningReviewMissingDependency = Schema.Schema.Type<
	typeof PlanningReviewMissingDependencySchema
>

export const PlanningReviewFeedbackSchema = Schema.Struct({
	score: Schema.Number,
	issues: Schema.Array(Schema.String),
	suggestions: Schema.Array(Schema.String),
	parallelizationOpportunities: Schema.Array(Schema.String),
	tasksTooLarge: Schema.Array(Schema.String),
	missingDependencies: Schema.Array(PlanningReviewMissingDependencySchema),
	isApproved: Schema.Boolean,
})
export type PlanningReviewFeedback = Schema.Schema.Type<typeof PlanningReviewFeedbackSchema>

export const DaemonPlanningGenerateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	featureDescription: Schema.String,
})
export type DaemonPlanningGenerateRequest = Schema.Schema.Type<
	typeof DaemonPlanningGenerateRequestSchema
>

export const DaemonPlanningGenerateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	plan: PlanningPlanSchema,
})
export type DaemonPlanningGenerateResult = Schema.Schema.Type<
	typeof DaemonPlanningGenerateResultSchema
>

export const DaemonPlanningReviewRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	plan: PlanningPlanSchema,
})
export type DaemonPlanningReviewRequest = Schema.Schema.Type<
	typeof DaemonPlanningReviewRequestSchema
>

export const DaemonPlanningReviewResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	feedback: PlanningReviewFeedbackSchema,
})
export type DaemonPlanningReviewResult = Schema.Schema.Type<typeof DaemonPlanningReviewResultSchema>

export const DaemonPlanningRefineRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	plan: PlanningPlanSchema,
	feedback: PlanningReviewFeedbackSchema,
})
export type DaemonPlanningRefineRequest = Schema.Schema.Type<
	typeof DaemonPlanningRefineRequestSchema
>

export const DaemonPlanningRefineResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	plan: PlanningPlanSchema,
})
export type DaemonPlanningRefineResult = Schema.Schema.Type<typeof DaemonPlanningRefineResultSchema>

export const DaemonPlanningCreateIssuesRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	plan: PlanningPlanSchema,
})
export type DaemonPlanningCreateIssuesRequest = Schema.Schema.Type<
	typeof DaemonPlanningCreateIssuesRequestSchema
>

export const DaemonPlanningCreateIssuesResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	createdIssues: Schema.Array(TrackedIssueSchema),
})
export type DaemonPlanningCreateIssuesResult = Schema.Schema.Type<
	typeof DaemonPlanningCreateIssuesResultSchema
>
