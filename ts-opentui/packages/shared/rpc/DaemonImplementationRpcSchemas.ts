import { Schema } from "effect"
import { DAEMON_RPC_PROTOCOL_VERSION } from "./DaemonRpcSchemas.js"

const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

export const ImplementationRecordSchema = Schema.Struct({
	name: Schema.String,
	description: Schema.optional(Schema.String),
	directory: Schema.optional(Schema.String),
	created_at: Schema.String,
	updated_at: Schema.String,
	is_default: Schema.Boolean,
	is_builtin: Schema.Boolean,
})
export type ImplementationRecord = Schema.Schema.Type<typeof ImplementationRecordSchema>

export const ImplementationRegistrySchema = Schema.Struct({
	default_implementation: Schema.String,
	implicit_default_allowed: Schema.Boolean,
	implementations: Schema.Array(ImplementationRecordSchema),
})
export type ImplementationRegistry = Schema.Schema.Type<typeof ImplementationRegistrySchema>

export const DaemonImplementationGetRegistryRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonImplementationGetRegistryRequest = Schema.Schema.Type<
	typeof DaemonImplementationGetRegistryRequestSchema
>

export const DaemonImplementationGetRegistryResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	registry: ImplementationRegistrySchema,
})
export type DaemonImplementationGetRegistryResult = Schema.Schema.Type<
	typeof DaemonImplementationGetRegistryResultSchema
>

export const DaemonImplementationCreateInputSchema = Schema.Struct({
	name: Schema.String,
	description: Schema.optional(Schema.String),
	directory: Schema.optional(Schema.String),
	setDefault: Schema.optional(Schema.Boolean),
})
export type DaemonImplementationCreateInput = Schema.Schema.Type<
	typeof DaemonImplementationCreateInputSchema
>

export const DaemonImplementationCreateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	input: DaemonImplementationCreateInputSchema,
})
export type DaemonImplementationCreateRequest = Schema.Schema.Type<
	typeof DaemonImplementationCreateRequestSchema
>

export const DaemonImplementationCreateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	implementation: ImplementationRecordSchema,
})
export type DaemonImplementationCreateResult = Schema.Schema.Type<
	typeof DaemonImplementationCreateResultSchema
>

export const DaemonImplementationUpdateFieldsSchema = Schema.Struct({
	name: Schema.optional(Schema.String),
	description: Schema.optional(Schema.String),
	directory: Schema.optional(Schema.String),
	setDefault: Schema.optional(Schema.Boolean),
})
export type DaemonImplementationUpdateFields = Schema.Schema.Type<
	typeof DaemonImplementationUpdateFieldsSchema
>

export const DaemonImplementationUpdateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	currentName: Schema.String,
	fields: DaemonImplementationUpdateFieldsSchema,
})
export type DaemonImplementationUpdateRequest = Schema.Schema.Type<
	typeof DaemonImplementationUpdateRequestSchema
>

export const DaemonImplementationUpdateResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	implementation: ImplementationRecordSchema,
})
export type DaemonImplementationUpdateResult = Schema.Schema.Type<
	typeof DaemonImplementationUpdateResultSchema
>

export const DaemonImplementationDeleteRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	name: Schema.String,
})
export type DaemonImplementationDeleteRequest = Schema.Schema.Type<
	typeof DaemonImplementationDeleteRequestSchema
>

export const DaemonImplementationDeleteResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	deleted: Schema.Literal(true),
})
export type DaemonImplementationDeleteResult = Schema.Schema.Type<
	typeof DaemonImplementationDeleteResultSchema
>

export const DaemonImplementationSetDefaultRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	name: Schema.String,
})
export type DaemonImplementationSetDefaultRequest = Schema.Schema.Type<
	typeof DaemonImplementationSetDefaultRequestSchema
>

export const DaemonImplementationSetDefaultResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	registry: ImplementationRegistrySchema,
})
export type DaemonImplementationSetDefaultResult = Schema.Schema.Type<
	typeof DaemonImplementationSetDefaultResultSchema
>
