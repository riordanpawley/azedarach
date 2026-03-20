import { Schema } from "effect"

export const DAEMON_RPC_PROTOCOL_VERSION = 2
const DaemonRpcProtocolVersionLiteralSchema = Schema.Literal(DAEMON_RPC_PROTOCOL_VERSION)
const DaemonRpcProtocolVersionRequestSchema = Schema.optionalWith(
	DaemonRpcProtocolVersionLiteralSchema,
	{ default: () => DAEMON_RPC_PROTOCOL_VERSION },
)

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
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	checkedAtMs: Schema.Number,
	runtime: DaemonRuntimeSnapshotSchema,
	sync: DaemonSyncStatusSchema,
})
export type DaemonControlStatusResult = Schema.Schema.Type<typeof DaemonControlStatusResultSchema>

export const DaemonHealthResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	checkedAtMs: Schema.Number,
	state: Schema.Literal("healthy", "degraded", "unhealthy"),
	reason: Schema.String,
	status: DaemonControlStatusResultSchema,
})
export type DaemonHealthResult = Schema.Schema.Type<typeof DaemonHealthResultSchema>

export const DaemonLogsResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
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
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
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
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
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
	worktreePath: Schema.NullOr(Schema.String),
	tmuxSessionName: Schema.String,
	state: DaemonSessionStateSchema,
	startedAt: Schema.NullOr(Schema.String),
	projectPath: Schema.String,
})
export type DaemonSessionSnapshotEntry = Schema.Schema.Type<typeof DaemonSessionSnapshotEntrySchema>

export const DaemonSessionSnapshotResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	capturedAtMs: Schema.Number,
	sessions: Schema.Array(DaemonSessionSnapshotEntrySchema),
})
export type DaemonSessionSnapshotResult = Schema.Schema.Type<
	typeof DaemonSessionSnapshotResultSchema
>

export const DaemonStatusRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
})
export type DaemonStatusRequest = Schema.Schema.Type<typeof DaemonStatusRequestSchema>

export const DaemonHealthRequestSchema = DaemonStatusRequestSchema
export type DaemonHealthRequest = Schema.Schema.Type<typeof DaemonHealthRequestSchema>

export const DaemonLogsRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	lines: Schema.optional(Schema.Number),
	projectPath: Schema.optional(Schema.String),
})
export type DaemonLogsRequest = Schema.Schema.Type<typeof DaemonLogsRequestSchema>

export const DaemonStopRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
})
export type DaemonStopRequest = Schema.Schema.Type<typeof DaemonStopRequestSchema>

export const DaemonRestartRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.optional(Schema.String),
	intervalMs: Schema.optional(Schema.Number),
})
export type DaemonRestartRequest = Schema.Schema.Type<typeof DaemonRestartRequestSchema>

export const DaemonAttachRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	clientId: Schema.String,
	protocolVersion: Schema.optional(Schema.Number),
	requestedAtMs: Schema.optional(Schema.Number),
})
export type DaemonAttachRequest = Schema.Schema.Type<typeof DaemonAttachRequestSchema>

export const DaemonReconnectRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	clientId: Schema.String,
	protocolVersion: Schema.optional(Schema.Number),
	lastSeenRevision: Schema.optional(Schema.Number),
	lastSeenLifecycleGeneration: Schema.optional(Schema.Number),
	requestedAtMs: Schema.optional(Schema.Number),
})
export type DaemonReconnectRequest = Schema.Schema.Type<typeof DaemonReconnectRequestSchema>

export const DaemonHeartbeatRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	clientId: Schema.String,
	observedAtMs: Schema.optional(Schema.Number),
})
export type DaemonHeartbeatRequest = Schema.Schema.Type<typeof DaemonHeartbeatRequestSchema>

export const DaemonSessionSnapshotRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.String,
})
export type DaemonSessionSnapshotRequest = Schema.Schema.Type<
	typeof DaemonSessionSnapshotRequestSchema
>

export const DaemonSessionStartRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionStartRequest = Schema.Schema.Type<typeof DaemonSessionStartRequestSchema>

export const DaemonSessionStopRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionStopRequest = Schema.Schema.Type<typeof DaemonSessionStopRequestSchema>

export const DaemonSessionPauseRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionPauseRequest = Schema.Schema.Type<typeof DaemonSessionPauseRequestSchema>

export const DaemonSessionResumeRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionResumeRequest = Schema.Schema.Type<typeof DaemonSessionResumeRequestSchema>

export const DaemonSessionRecoverRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
})
export type DaemonSessionRecoverRequest = Schema.Schema.Type<
	typeof DaemonSessionRecoverRequestSchema
>

export const DaemonSessionUpdateStateRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	state: DaemonSessionStateSchema,
	projectPath: Schema.String,
	tmuxSessionName: Schema.optional(Schema.String),
	worktreePath: Schema.optional(Schema.NullOr(Schema.String)),
	startedAt: Schema.optional(Schema.NullOr(Schema.String)),
})
export type DaemonSessionUpdateStateRequest = Schema.Schema.Type<
	typeof DaemonSessionUpdateStateRequestSchema
>

export const DaemonSessionMutationResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	capturedAtMs: Schema.Number,
	session: DaemonSessionSnapshotEntrySchema,
})
export type DaemonSessionMutationResult = Schema.Schema.Type<
	typeof DaemonSessionMutationResultSchema
>

export const DaemonBoardTaskSchema = Schema.Struct({
	id: Schema.String,
	title: Schema.String,
	description: Schema.optional(Schema.String),
	status: Schema.Literal("open", "in_progress", "blocked", "closed", "tombstone"),
	priority: Schema.Number,
	issue_type: Schema.Literal("bug", "feature", "task", "epic", "chore"),
	created_at: Schema.String,
	updated_at: Schema.String,
	closed_at: Schema.optional(Schema.NullOr(Schema.String)),
	assignee: Schema.optional(Schema.NullOr(Schema.String)),
	labels: Schema.optional(Schema.Array(Schema.String)),
	design: Schema.optional(Schema.String),
	notes: Schema.optional(Schema.String),
	acceptance: Schema.optional(Schema.String),
	estimate: Schema.optional(Schema.Number),
	implementations: Schema.Array(Schema.String),
	dependent_count: Schema.optional(Schema.Number),
	dependency_count: Schema.optional(Schema.Number),
	sessionState: DaemonSessionStateSchema,
	sessionStartedAt: Schema.optional(Schema.String),
	hasTmuxSession: Schema.optional(Schema.Boolean),
	hasWorktree: Schema.optional(Schema.Boolean),
	hasMergeConflict: Schema.optional(Schema.Boolean),
	parentEpicId: Schema.optional(Schema.String),
	estimatedTokens: Schema.optional(Schema.Number),
	recentOutput: Schema.optional(Schema.String),
	agentPhase: Schema.optional(
		Schema.Literal("idle", "planning", "action", "verification", "planMode"),
	),
	hasPR: Schema.optional(Schema.Boolean),
	prUrl: Schema.optional(Schema.String),
	prNumber: Schema.optional(Schema.Number),
	prState: Schema.optional(Schema.Literal("open", "draft", "merged", "closed")),
	gitBehindCount: Schema.optional(Schema.Number),
	hasUncommittedChanges: Schema.optional(Schema.Boolean),
	gitAdditions: Schema.optional(Schema.Number),
	gitDeletions: Schema.optional(Schema.Number),
	hasDevServer: Schema.optional(Schema.Boolean),
})
export type DaemonBoardTask = Schema.Schema.Type<typeof DaemonBoardTaskSchema>

export const DaemonBoardReadModelRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	projectPath: Schema.String,
})
export type DaemonBoardReadModelRequest = Schema.Schema.Type<
	typeof DaemonBoardReadModelRequestSchema
>

export const DaemonBoardReadModelResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	capturedAtMs: Schema.Number,
	projectPath: Schema.String,
	tasks: Schema.Array(DaemonBoardTaskSchema),
})
export type DaemonBoardReadModelResult = Schema.Schema.Type<typeof DaemonBoardReadModelResultSchema>

export const DaemonDevServerStatusSchema = Schema.Literal(
	"idle",
	"starting",
	"running",
	"stopped",
	"error",
)
export type DaemonDevServerStatus = Schema.Schema.Type<typeof DaemonDevServerStatusSchema>

export const DaemonDevServerStateSchema = Schema.Struct({
	issueId: Schema.String,
	serverName: Schema.String,
	status: DaemonDevServerStatusSchema,
	port: Schema.NullOr(Schema.Number),
	windowName: Schema.NullOr(Schema.String),
	tmuxSession: Schema.NullOr(Schema.String),
	worktreePath: Schema.NullOr(Schema.String),
	projectPath: Schema.NullOr(Schema.String),
	startedAt: Schema.NullOr(Schema.String),
	error: Schema.NullOr(Schema.String),
})
export type DaemonDevServerState = Schema.Schema.Type<typeof DaemonDevServerStateSchema>

export const DaemonDevServerStatusRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	serverName: Schema.optional(Schema.String),
	projectPath: Schema.String,
})
export type DaemonDevServerStatusRequest = Schema.Schema.Type<
	typeof DaemonDevServerStatusRequestSchema
>

export const DaemonDevServerStatusResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	capturedAtMs: Schema.Number,
	server: DaemonDevServerStateSchema,
})
export type DaemonDevServerStatusResult = Schema.Schema.Type<
	typeof DaemonDevServerStatusResultSchema
>

export const DaemonDevServerListRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.optional(Schema.String),
	projectPath: Schema.String,
})
export type DaemonDevServerListRequest = Schema.Schema.Type<typeof DaemonDevServerListRequestSchema>

export const DaemonDevServerListResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	capturedAtMs: Schema.Number,
	servers: Schema.Array(DaemonDevServerStateSchema),
})
export type DaemonDevServerListResult = Schema.Schema.Type<typeof DaemonDevServerListResultSchema>

export const DaemonDevServerStartRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	projectPath: Schema.String,
	serverName: Schema.optional(Schema.String),
})
export type DaemonDevServerStartRequest = Schema.Schema.Type<
	typeof DaemonDevServerStartRequestSchema
>

export const DaemonDevServerStopRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	issueId: Schema.String,
	serverName: Schema.optional(Schema.String),
	projectPath: Schema.String,
})
export type DaemonDevServerStopRequest = Schema.Schema.Type<typeof DaemonDevServerStopRequestSchema>

export const DaemonDevServerMutationResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	capturedAtMs: Schema.Number,
	server: DaemonDevServerStateSchema,
})
export type DaemonDevServerMutationResult = Schema.Schema.Type<
	typeof DaemonDevServerMutationResultSchema
>

export const DaemonQueueDomainSchema = Schema.Literal("command", "mutation")
export type DaemonQueueDomain = Schema.Schema.Type<typeof DaemonQueueDomainSchema>

export const DaemonQueueItemStateSchema = Schema.Literal(
	"queued",
	"running",
	"done",
	"failed",
	"cancelled",
)
export type DaemonQueueItemState = Schema.Schema.Type<typeof DaemonQueueItemStateSchema>

export const DaemonQueueItemSchema = Schema.Struct({
	domain: DaemonQueueDomainSchema,
	operationId: Schema.String,
	operation: Schema.String,
	projectPath: Schema.String,
	issueId: Schema.NullOr(Schema.String),
	dedupeKey: Schema.NullOr(Schema.String),
	payloadJson: Schema.NullOr(Schema.String),
	state: DaemonQueueItemStateSchema,
	enqueuedAtMs: Schema.Number,
	startedAtMs: Schema.NullOr(Schema.Number),
	finishedAtMs: Schema.NullOr(Schema.Number),
	error: Schema.NullOr(Schema.String),
})
export type DaemonQueueItem = Schema.Schema.Type<typeof DaemonQueueItemSchema>

export const DaemonQueueEnqueueRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	domain: DaemonQueueDomainSchema,
	operation: Schema.String,
	projectPath: Schema.String,
	issueId: Schema.optional(Schema.String),
	dedupeKey: Schema.optional(Schema.String),
	payloadJson: Schema.optional(Schema.String),
})
export type DaemonQueueEnqueueRequest = Schema.Schema.Type<typeof DaemonQueueEnqueueRequestSchema>

export const DaemonQueueEnqueueResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	acceptedAtMs: Schema.Number,
	item: DaemonQueueItemSchema,
})
export type DaemonQueueEnqueueResult = Schema.Schema.Type<typeof DaemonQueueEnqueueResultSchema>

export const DaemonQueueQueryRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	domain: Schema.optional(DaemonQueueDomainSchema),
	operationId: Schema.optional(Schema.String),
	projectPath: Schema.String,
	issueId: Schema.optional(Schema.String),
	limit: Schema.optional(Schema.Number),
})
export type DaemonQueueQueryRequest = Schema.Schema.Type<typeof DaemonQueueQueryRequestSchema>

export const DaemonQueueQueryResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	queriedAtMs: Schema.Number,
	items: Schema.Array(DaemonQueueItemSchema),
})
export type DaemonQueueQueryResult = Schema.Schema.Type<typeof DaemonQueueQueryResultSchema>

export const DaemonQueueCancelRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	domain: Schema.optional(DaemonQueueDomainSchema),
	operationId: Schema.optional(Schema.String),
	projectPath: Schema.String,
	issueId: Schema.optional(Schema.String),
})
export type DaemonQueueCancelRequest = Schema.Schema.Type<typeof DaemonQueueCancelRequestSchema>

export const DaemonQueueCancelResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	cancelledAtMs: Schema.Number,
	cancelledOperationIds: Schema.Array(Schema.String),
})
export type DaemonQueueCancelResult = Schema.Schema.Type<typeof DaemonQueueCancelResultSchema>

export const DaemonEventStreamSessionSnapshotEventSchema = Schema.TaggedStruct(
	"DaemonEventStreamSessionSnapshotEvent",
	{
		capturedAtMs: Schema.Number,
		sessions: Schema.Array(DaemonSessionSnapshotEntrySchema),
	},
)
export type DaemonEventStreamSessionSnapshotEvent = Schema.Schema.Type<
	typeof DaemonEventStreamSessionSnapshotEventSchema
>

export const DaemonEventStreamRuntimeSnapshotEventSchema = Schema.TaggedStruct(
	"DaemonEventStreamRuntimeSnapshotEvent",
	{
		runtime: DaemonRuntimeSnapshotSchema,
	},
)
export type DaemonEventStreamRuntimeSnapshotEvent = Schema.Schema.Type<
	typeof DaemonEventStreamRuntimeSnapshotEventSchema
>

export const DaemonEventStreamEventSchema = Schema.Union(
	DaemonEventStreamSessionSnapshotEventSchema,
	DaemonEventStreamRuntimeSnapshotEventSchema,
)
export type DaemonEventStreamEvent = Schema.Schema.Type<typeof DaemonEventStreamEventSchema>

export const DaemonEventStreamEntrySchema = Schema.Struct({
	cursor: Schema.Number,
	emittedAtMs: Schema.Number,
	event: DaemonEventStreamEventSchema,
})
export type DaemonEventStreamEntry = Schema.Schema.Type<typeof DaemonEventStreamEntrySchema>

export const DaemonEventStreamRequestSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionRequestSchema,
	clientId: Schema.String,
	cursor: Schema.optional(Schema.Number),
	batchSize: Schema.optional(Schema.Number),
	waitMs: Schema.optional(Schema.Number),
	projectPath: Schema.String,
})
export type DaemonEventStreamRequest = Schema.Schema.Type<typeof DaemonEventStreamRequestSchema>

export const DaemonEventStreamResultSchema = Schema.Struct({
	rpcProtocolVersion: DaemonRpcProtocolVersionLiteralSchema,
	polledAtMs: Schema.Number,
	nextCursor: Schema.Number,
	events: Schema.Array(DaemonEventStreamEntrySchema),
})
export type DaemonEventStreamResult = Schema.Schema.Type<typeof DaemonEventStreamResultSchema>
