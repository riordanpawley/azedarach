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

export const DaemonPrCheckGhCliRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
})
export type DaemonPrCheckGhCliRequest = Schema.Schema.Type<typeof DaemonPrCheckGhCliRequestSchema>

export const DaemonPrCheckGhCliResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	available: Schema.Boolean,
})
export type DaemonPrCheckGhCliResult = Schema.Schema.Type<typeof DaemonPrCheckGhCliResultSchema>
