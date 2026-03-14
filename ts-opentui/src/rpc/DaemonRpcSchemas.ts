import { Schema } from "effect"

export const DAEMON_RPC_PROTOCOL_VERSION = 1

export const DaemonRpcActionErrorSchema = Schema.TaggedStruct("DaemonRpcActionError", {
	code: Schema.String,
	message: Schema.String,
	action: Schema.optional(Schema.String),
})
export type DaemonRpcActionError = Schema.Schema.Type<typeof DaemonRpcActionErrorSchema>

export const DaemonRuntimePhaseSchema = Schema.Literal(
	"starting",
	"ready",
	"degraded",
	"recovering",
	"stopping",
	"crashed",
)

export const DaemonSyncStateSchema = Schema.Literal("stopped", "running", "degraded", "crashed")

export const DaemonClientStateSchema = Schema.Struct({
	clientId: Schema.String,
	connectedAtMs: Schema.Number,
	lastHeartbeatAtMs: Schema.Number,
	lastReconnectAtMs: Schema.NullOr(Schema.Number),
	lastSeenRevision: Schema.NullOr(Schema.Number),
	lastSeenLifecycleGeneration: Schema.NullOr(Schema.Number),
	lastRecoveryGeneration: Schema.NullOr(Schema.Number),
})
export type DaemonClientState = Schema.Schema.Type<typeof DaemonClientStateSchema>

export const DaemonRuntimeSnapshotSchema = Schema.Struct({
	protocolVersion: Schema.Number,
	runtimePhase: DaemonRuntimePhaseSchema,
	authoritativeRuntime: Schema.Literal(true),
	revision: Schema.Number,
	lifecycleGeneration: Schema.Number,
	lifecycleReason: Schema.String,
	recoveryGeneration: Schema.Number,
	capturedAtMs: Schema.Number,
	clients: Schema.Record({ key: Schema.String, value: DaemonClientStateSchema }),
})
export type DaemonRuntimeSnapshot = Schema.Schema.Type<typeof DaemonRuntimeSnapshotSchema>

export const DaemonSyncStatusSchema = Schema.Struct({
	state: DaemonSyncStateSchema,
	generation: Schema.Number,
	projectPath: Schema.NullOr(Schema.String),
	intervalMs: Schema.NullOr(Schema.Number),
	startedAtMs: Schema.NullOr(Schema.Number),
	runCount: Schema.Number,
	successCount: Schema.Number,
	failureCount: Schema.Number,
	failureStreak: Schema.Number,
	restartStreak: Schema.Number,
	lastBackoffMs: Schema.NullOr(Schema.Number),
	lastSuccessfulRunAtMs: Schema.NullOr(Schema.Number),
	lastRun: Schema.NullOr(
		Schema.Struct({
			runAtMs: Schema.Number,
			result: Schema.Literal("flushed", "skipped", "failed"),
			pushed: Schema.Number,
			pulled: Schema.Number,
			message: Schema.NullOr(Schema.String),
		}),
	),
	lastError: Schema.NullOr(Schema.String),
})
export type DaemonSyncStatus = Schema.Schema.Type<typeof DaemonSyncStatusSchema>

export const DaemonControlStatusResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	checkedAtMs: Schema.Number,
	runtime: DaemonRuntimeSnapshotSchema,
	sync: DaemonSyncStatusSchema,
})
export type DaemonControlStatusResult = Schema.Schema.Type<typeof DaemonControlStatusResultSchema>

export const DaemonHealthResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	checkedAtMs: Schema.Number,
	state: Schema.Literal("healthy", "degraded", "unhealthy"),
	reason: Schema.String,
	status: DaemonControlStatusResultSchema,
})
export type DaemonHealthResult = Schema.Schema.Type<typeof DaemonHealthResultSchema>

export const DaemonLogsResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	logPath: Schema.String,
	totalLines: Schema.Number,
	lines: Schema.Array(Schema.String),
})
export type DaemonLogsResult = Schema.Schema.Type<typeof DaemonLogsResultSchema>

export const DaemonHandshakeSchema = Schema.Struct({
	operation: Schema.Literal("attach", "reconnect"),
	requestedAtMs: Schema.Number,
	negotiatedAtMs: Schema.Number,
	requestedProtocolVersion: Schema.Number,
	negotiatedProtocolVersion: Schema.Number,
	serverSupportedProtocolVersions: Schema.Array(Schema.Number),
	compatibilityDecision: Schema.Literal(
		"exact-match",
		"client-older-compatible",
		"server-older-compatible",
	),
})
export type DaemonHandshake = Schema.Schema.Type<typeof DaemonHandshakeSchema>

export const DaemonNegotiatedCapabilitiesSchema = Schema.Struct({
	authoritativeRuntime: Schema.Literal(true),
	lifecycleGenerationTracking: Schema.Literal(true),
	recoveryGenerationTracking: Schema.Literal(true),
	resumeToken: Schema.Literal(true),
})
export type DaemonNegotiatedCapabilities = Schema.Schema.Type<
	typeof DaemonNegotiatedCapabilitiesSchema
>

export const DaemonAttachReconnectResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	clientId: Schema.String,
	acceptedAtMs: Schema.Number,
	resumeToken: Schema.String,
	negotiatedCapabilities: DaemonNegotiatedCapabilitiesSchema,
	handshake: DaemonHandshakeSchema,
	snapshot: DaemonRuntimeSnapshotSchema,
})
export type DaemonAttachReconnectResult = Schema.Schema.Type<
	typeof DaemonAttachReconnectResultSchema
>

export const DaemonHeartbeatResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	client: DaemonClientStateSchema,
})
export type DaemonHeartbeatResult = Schema.Schema.Type<typeof DaemonHeartbeatResultSchema>

export const DaemonSessionStateSchema = Schema.Literal(
	"idle",
	"initializing",
	"busy",
	"waiting",
	"done",
	"error",
	"paused",
	"warning",
	"crashed",
)

export const DaemonSessionSnapshotEntrySchema = Schema.Struct({
	issueId: Schema.String,
	worktreePath: Schema.String,
	tmuxSessionName: Schema.String,
	state: DaemonSessionStateSchema,
	startedAt: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionSnapshotEntry = Schema.Schema.Type<typeof DaemonSessionSnapshotEntrySchema>

export const DaemonSessionSnapshotResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	capturedAtMs: Schema.Number,
	sessions: Schema.Array(DaemonSessionSnapshotEntrySchema),
})
export type DaemonSessionSnapshotResult = Schema.Schema.Type<
	typeof DaemonSessionSnapshotResultSchema
>

export const DaemonStatusRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
})
export type DaemonStatusRequest = Schema.Schema.Type<typeof DaemonStatusRequestSchema>

export const DaemonHealthRequestSchema = DaemonStatusRequestSchema
export type DaemonHealthRequest = Schema.Schema.Type<typeof DaemonHealthRequestSchema>

export const DaemonLogsRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	lines: Schema.optional(Schema.Number),
	projectPath: Schema.optional(Schema.String),
})
export type DaemonLogsRequest = Schema.Schema.Type<typeof DaemonLogsRequestSchema>

export const DaemonStopRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
})
export type DaemonStopRequest = Schema.Schema.Type<typeof DaemonStopRequestSchema>

export const DaemonRestartRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	projectPath: Schema.optional(Schema.String),
	intervalMs: Schema.optional(Schema.Number),
})
export type DaemonRestartRequest = Schema.Schema.Type<typeof DaemonRestartRequestSchema>

export const DaemonAttachRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	clientId: Schema.String,
	protocolVersion: Schema.optional(Schema.Number),
	requestedAtMs: Schema.optional(Schema.Number),
})
export type DaemonAttachRequest = Schema.Schema.Type<typeof DaemonAttachRequestSchema>

export const DaemonReconnectRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	clientId: Schema.String,
	protocolVersion: Schema.optional(Schema.Number),
	lastSeenRevision: Schema.optional(Schema.Number),
	lastSeenLifecycleGeneration: Schema.optional(Schema.Number),
	requestedAtMs: Schema.optional(Schema.Number),
})
export type DaemonReconnectRequest = Schema.Schema.Type<typeof DaemonReconnectRequestSchema>

export const DaemonHeartbeatRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	clientId: Schema.String,
	observedAtMs: Schema.optional(Schema.Number),
})
export type DaemonHeartbeatRequest = Schema.Schema.Type<typeof DaemonHeartbeatRequestSchema>

export const DaemonSessionSnapshotRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSessionSnapshotRequest = Schema.Schema.Type<
	typeof DaemonSessionSnapshotRequestSchema
>

export const DaemonSessionStartRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionStartRequest = Schema.Schema.Type<typeof DaemonSessionStartRequestSchema>

export const DaemonSessionStopRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSessionStopRequest = Schema.Schema.Type<typeof DaemonSessionStopRequestSchema>

export const DaemonSessionPauseRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSessionPauseRequest = Schema.Schema.Type<typeof DaemonSessionPauseRequestSchema>

export const DaemonSessionResumeRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSessionResumeRequest = Schema.Schema.Type<typeof DaemonSessionResumeRequestSchema>

export const DaemonSessionRecoverRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	issueId: Schema.String,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSessionRecoverRequest = Schema.Schema.Type<
	typeof DaemonSessionRecoverRequestSchema
>

export const DaemonSessionUpdateStateRequestSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	issueId: Schema.String,
	state: DaemonSessionStateSchema,
	projectPath: Schema.optional(Schema.String),
})
export type DaemonSessionUpdateStateRequest = Schema.Schema.Type<
	typeof DaemonSessionUpdateStateRequestSchema
>

export const DaemonSessionMutationResultSchema = Schema.Struct({
	rpcProtocolVersion: Schema.Number,
	capturedAtMs: Schema.Number,
	session: DaemonSessionSnapshotEntrySchema,
})
export type DaemonSessionMutationResult = Schema.Schema.Type<
	typeof DaemonSessionMutationResultSchema
>
