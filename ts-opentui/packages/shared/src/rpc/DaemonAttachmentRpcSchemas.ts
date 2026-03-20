import { Schema } from "effect"
import { DAEMON_RPC_PROTOCOL_VERSION } from "./DaemonRpcSchemas.js"

const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

export const ImageAttachmentSchema = Schema.Struct({
	id: Schema.String,
	filename: Schema.String,
	originalPath: Schema.String,
	mimeType: Schema.String,
	size: Schema.Number,
	createdAt: Schema.String,
})
export type ImageAttachment = Schema.Schema.Type<typeof ImageAttachmentSchema>

export const DaemonAttachmentListRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonAttachmentListRequest = Schema.Schema.Type<
	typeof DaemonAttachmentListRequestSchema
>

export const DaemonAttachmentListResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	attachments: Schema.Array(ImageAttachmentSchema),
})
export type DaemonAttachmentListResult = Schema.Schema.Type<typeof DaemonAttachmentListResultSchema>

export const DaemonAttachmentCountBatchRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueIds: Schema.Array(Schema.String),
	projectPath: Schema.optional(Schema.String),
})
export type DaemonAttachmentCountBatchRequest = Schema.Schema.Type<
	typeof DaemonAttachmentCountBatchRequestSchema
>

export const DaemonAttachmentCountBatchResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	counts: Schema.Record({ key: Schema.String, value: Schema.Number }),
})
export type DaemonAttachmentCountBatchResult = Schema.Schema.Type<
	typeof DaemonAttachmentCountBatchResultSchema
>

export const DaemonAttachmentAttachFileRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	filePath: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonAttachmentAttachFileRequest = Schema.Schema.Type<
	typeof DaemonAttachmentAttachFileRequestSchema
>

export const DaemonAttachmentAttachClipboardRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	base64Content: Schema.String,
	filename: Schema.String,
	mimeType: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonAttachmentAttachClipboardRequest = Schema.Schema.Type<
	typeof DaemonAttachmentAttachClipboardRequestSchema
>

export const DaemonAttachmentAttachResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	attachment: ImageAttachmentSchema,
})
export type DaemonAttachmentAttachResult = Schema.Schema.Type<
	typeof DaemonAttachmentAttachResultSchema
>

export const DaemonAttachmentRemoveRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	attachmentId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonAttachmentRemoveRequest = Schema.Schema.Type<
	typeof DaemonAttachmentRemoveRequestSchema
>

export const DaemonAttachmentRemoveResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	removed: Schema.Literal(true),
})
export type DaemonAttachmentRemoveResult = Schema.Schema.Type<
	typeof DaemonAttachmentRemoveResultSchema
>

export const DaemonAttachmentMaterializePathRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	attachmentId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonAttachmentMaterializePathRequest = Schema.Schema.Type<
	typeof DaemonAttachmentMaterializePathRequestSchema
>

export const DaemonAttachmentMaterializePathResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	path: Schema.String,
})
export type DaemonAttachmentMaterializePathResult = Schema.Schema.Type<
	typeof DaemonAttachmentMaterializePathResultSchema
>
