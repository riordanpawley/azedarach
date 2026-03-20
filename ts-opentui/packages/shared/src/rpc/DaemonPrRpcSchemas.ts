import { Schema } from "effect"
import { DAEMON_RPC_PROTOCOL_VERSION } from "./DaemonRpcSchemas.js"

const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

export const DaemonPullRequestSchema = Schema.Struct({
	number: Schema.Number,
	url: Schema.String,
	title: Schema.String,
	state: Schema.Literal("open", "closed", "merged"),
	draft: Schema.Boolean,
	branch: Schema.String,
})
export type DaemonPullRequest = Schema.Schema.Type<typeof DaemonPullRequestSchema>

export const DaemonPrCreateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrCreateRequest = Schema.Schema.Type<typeof DaemonPrCreateRequestSchema>

export const DaemonPrCreateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	pullRequest: DaemonPullRequestSchema,
})
export type DaemonPrCreateResult = Schema.Schema.Type<typeof DaemonPrCreateResultSchema>

export const DaemonPrCleanupRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
	closeIssue: Schema.optional(Schema.Boolean),
})
export type DaemonPrCleanupRequest = Schema.Schema.Type<typeof DaemonPrCleanupRequestSchema>

export const DaemonPrCleanupResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	cleanedUp: Schema.Literal(true),
})
export type DaemonPrCleanupResult = Schema.Schema.Type<typeof DaemonPrCleanupResultSchema>

export const DaemonPrMergeToMainRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrMergeToMainRequest = Schema.Schema.Type<typeof DaemonPrMergeToMainRequestSchema>

export const DaemonPrMergeToMainResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	merged: Schema.Literal(true),
})
export type DaemonPrMergeToMainResult = Schema.Schema.Type<typeof DaemonPrMergeToMainResultSchema>

export const DaemonPrUpdateFromBaseRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrUpdateFromBaseRequest = Schema.Schema.Type<
	typeof DaemonPrUpdateFromBaseRequestSchema
>

export const DaemonPrUpdateFromBaseResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	updated: Schema.Literal(true),
})
export type DaemonPrUpdateFromBaseResult = Schema.Schema.Type<
	typeof DaemonPrUpdateFromBaseResultSchema
>

export const DaemonPrMergeBaseIntoBranchRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrMergeBaseIntoBranchRequest = Schema.Schema.Type<
	typeof DaemonPrMergeBaseIntoBranchRequestSchema
>

export const DaemonPrMergeBaseIntoBranchResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	merged: Schema.Literal(true),
})
export type DaemonPrMergeBaseIntoBranchResult = Schema.Schema.Type<
	typeof DaemonPrMergeBaseIntoBranchResultSchema
>

export const DaemonPrAbortMergeRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrAbortMergeRequest = Schema.Schema.Type<typeof DaemonPrAbortMergeRequestSchema>

export const DaemonPrAbortMergeResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	aborted: Schema.Literal(true),
})
export type DaemonPrAbortMergeResult = Schema.Schema.Type<typeof DaemonPrAbortMergeResultSchema>

export const DaemonPrCheckGhCliRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
})
export type DaemonPrCheckGhCliRequest = Schema.Schema.Type<typeof DaemonPrCheckGhCliRequestSchema>

export const DaemonPrCheckGhCliResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	available: Schema.Boolean,
})
export type DaemonPrCheckGhCliResult = Schema.Schema.Type<typeof DaemonPrCheckGhCliResultSchema>

export const DaemonPrCheckMergeConflictsRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrCheckMergeConflictsRequest = Schema.Schema.Type<
	typeof DaemonPrCheckMergeConflictsRequestSchema
>

export const DaemonPrCheckMergeConflictsResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	hasConflictRisk: Schema.Boolean,
	conflictingFiles: Schema.Array(Schema.String),
	baseBranch: Schema.String,
	issueBranch: Schema.String,
})
export type DaemonPrCheckMergeConflictsResult = Schema.Schema.Type<
	typeof DaemonPrCheckMergeConflictsResultSchema
>

export const DaemonPrCheckUncommittedChangesRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrCheckUncommittedChangesRequest = Schema.Schema.Type<
	typeof DaemonPrCheckUncommittedChangesRequestSchema
>

export const DaemonPrCheckUncommittedChangesResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	hasUncommittedChanges: Schema.Boolean,
	changedFiles: Schema.Array(Schema.String),
})
export type DaemonPrCheckUncommittedChangesResult = Schema.Schema.Type<
	typeof DaemonPrCheckUncommittedChangesResultSchema
>

export const DaemonPrCheckBranchBehindBaseRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrCheckBranchBehindBaseRequest = Schema.Schema.Type<
	typeof DaemonPrCheckBranchBehindBaseRequestSchema
>

export const DaemonPrCheckBranchBehindBaseResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	behind: Schema.Number,
	ahead: Schema.Number,
	baseBranch: Schema.String,
})
export type DaemonPrCheckBranchBehindBaseResult = Schema.Schema.Type<
	typeof DaemonPrCheckBranchBehindBaseResultSchema
>

export const DaemonPrGetEffectiveBaseBranchRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrGetEffectiveBaseBranchRequest = Schema.Schema.Type<
	typeof DaemonPrGetEffectiveBaseBranchRequestSchema
>

export const DaemonPrGetEffectiveBaseBranchResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	baseBranch: Schema.String,
	parentEpicId: Schema.optional(Schema.String),
})
export type DaemonPrGetEffectiveBaseBranchResult = Schema.Schema.Type<
	typeof DaemonPrGetEffectiveBaseBranchResultSchema
>

export const DaemonPrMergeIssueIntoIssueRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	sourceIssueId: Schema.String,
	targetIssueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrMergeIssueIntoIssueRequest = Schema.Schema.Type<
	typeof DaemonPrMergeIssueIntoIssueRequestSchema
>

export const DaemonPrMergeIssueIntoIssueResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	merged: Schema.Literal(true),
})
export type DaemonPrMergeIssueIntoIssueResult = Schema.Schema.Type<
	typeof DaemonPrMergeIssueIntoIssueResultSchema
>

export const DaemonPrGetTargetBranchRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonPrGetTargetBranchRequest = Schema.Schema.Type<
	typeof DaemonPrGetTargetBranchRequestSchema
>

export const DaemonPrGetTargetBranchResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	targetBranch: Schema.String,
	isEpicChild: Schema.Boolean,
})
export type DaemonPrGetTargetBranchResult = Schema.Schema.Type<
	typeof DaemonPrGetTargetBranchResultSchema
>
