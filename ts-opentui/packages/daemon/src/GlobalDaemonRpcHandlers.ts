import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonAttachReconnectResult,
	type DaemonAttachRequest,
	type DaemonBoardReadModelRequest,
	type DaemonBoardReadModelResult,
	type DaemonControlStatusResult,
	type DaemonDevServerListRequest,
	type DaemonDevServerListResult,
	type DaemonDevServerMutationResult,
	type DaemonDevServerStartRequest,
	type DaemonDevServerStatusRequest,
	type DaemonDevServerStatusResult,
	type DaemonDevServerStopRequest,
	type DaemonEventStreamRequest,
	type DaemonEventStreamResult,
	type DaemonHealthRequest,
	type DaemonHealthResult,
	type DaemonHeartbeatRequest,
	type DaemonHeartbeatResult,
	type DaemonImplementationCreateRequest,
	type DaemonImplementationCreateResult,
	type DaemonImplementationDeleteRequest,
	type DaemonImplementationDeleteResult,
	type DaemonImplementationGetRegistryRequest,
	type DaemonImplementationGetRegistryResult,
	type DaemonImplementationSetDefaultRequest,
	type DaemonImplementationSetDefaultResult,
	type DaemonImplementationUpdateRequest,
	type DaemonImplementationUpdateResult,
	type DaemonIssueAddDependencyRequest,
	type DaemonIssueCloseRequest,
	type DaemonIssueCloseResult,
	type DaemonIssueCreateRequest,
	type DaemonIssueCreateResult,
	type DaemonIssueDeleteRequest,
	type DaemonIssueDeleteResult,
	type DaemonIssueDependencyMutationResult,
	type DaemonIssueGetRequest,
	type DaemonIssueGetResult,
	type DaemonIssueListRequest,
	type DaemonIssueListResult,
	type DaemonIssueRemoveDependencyRequest,
	type DaemonIssueSyncRequest,
	type DaemonIssueSyncResultEnvelope,
	type DaemonIssueUpdateRequest,
	type DaemonIssueUpdateResult,
	type DaemonLogsRequest,
	type DaemonLogsResult,
	type DaemonQueueCancelRequest,
	type DaemonQueueCancelResult,
	type DaemonQueueEnqueueRequest,
	type DaemonQueueEnqueueResult,
	type DaemonQueueQueryRequest,
	type DaemonQueueQueryResult,
	type DaemonReconnectRequest,
	type DaemonRestartRequest,
	type DaemonRpcActionError,
	DaemonRpcGroup,
	type DaemonRuntimeSnapshot,
	type DaemonSessionMutationResult,
	type DaemonSessionPauseRequest,
	type DaemonSessionRecoverRequest,
	type DaemonSessionResumeRequest,
	type DaemonSessionSnapshotRequest,
	type DaemonSessionSnapshotResult,
	type DaemonSessionStartRequest,
	type DaemonSessionStopRequest,
	type DaemonSessionUpdateStateRequest,
	type DaemonStatusRequest,
	type DaemonStopRequest,
} from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import {
	type BackendDaemonControlHealth,
	BackendDaemonControlRestartConfigurationError,
	BackendDaemonControlService,
	type BackendDaemonControlStatus,
} from "./BackendDaemonControlService.js"
import {
	type BackendDaemonAttachResponse,
	type BackendDaemonClientState,
	BackendDaemonService,
	type BackendDaemonSnapshot,
} from "./BackendDaemonService.js"
import { BackendDaemonSessionRecovery } from "./BackendDaemonSessionRecovery.js"
import {
	ImplementationRegistryDaemonError,
	ImplementationRegistryDaemonService,
} from "./ImplementationRegistryDaemonService.js"
import { TrackerIssueDaemonError, TrackerIssueDaemonService } from "./TrackerIssueDaemonService.js"

const daemonRpcActionError = (params: {
	readonly action: string
	readonly code: string
	readonly message: string
}): DaemonRpcActionError => ({
	_tag: "DaemonRpcActionError",
	action: params.action,
	code: params.code,
	message: params.message,
})

const mapClientState = (client: BackendDaemonClientState): DaemonHeartbeatResult["client"] => ({
	clientId: client.clientId,
	connectedAtMs: client.connectedAtMs,
	lastHeartbeatAtMs: client.lastHeartbeatAtMs,
	lastReconnectAtMs: client.lastReconnectAtMs,
	lastSeenRevision: client.lastSeenRevision,
	lastSeenLifecycleGeneration: client.lastSeenLifecycleGeneration,
	lastRecoveryGeneration: client.lastRecoveryGeneration,
})

const mapRuntimeSnapshot = (
	snapshot: Pick<
		BackendDaemonSnapshot,
		| "protocolVersion"
		| "runtimePhase"
		| "authoritativeRuntime"
		| "revision"
		| "lifecycleGeneration"
		| "lifecycleReason"
		| "recoveryGeneration"
		| "capturedAtMs"
		| "clients"
	>,
): DaemonRuntimeSnapshot => ({
	protocolVersion: snapshot.protocolVersion,
	runtimePhase: snapshot.runtimePhase,
	authoritativeRuntime: snapshot.authoritativeRuntime,
	revision: snapshot.revision,
	lifecycleGeneration: snapshot.lifecycleGeneration,
	lifecycleReason: snapshot.lifecycleReason,
	recoveryGeneration: snapshot.recoveryGeneration,
	capturedAtMs: snapshot.capturedAtMs,
	clients: Object.fromEntries(
		Object.entries(snapshot.clients).map(([clientId, client]) => [
			clientId,
			mapClientState(client),
		]),
	),
})

const mapControlStatus = (status: BackendDaemonControlStatus): DaemonControlStatusResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	checkedAtMs: status.checkedAtMs,
	runtime: mapRuntimeSnapshot(status.runtime),
	sync: status.sync,
})

const mapHealth = (health: BackendDaemonControlHealth): DaemonHealthResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	checkedAtMs: health.checkedAtMs,
	state: health.state,
	reason: health.reason,
	status: mapControlStatus(health.status),
})

const mapAttachResponse = (response: BackendDaemonAttachResponse): DaemonAttachReconnectResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	clientId: response.clientId,
	acceptedAtMs: response.acceptedAtMs,
	resumeToken: response.resumeToken,
	negotiatedCapabilities: {
		authoritativeRuntime: response.negotiatedCapabilities.authoritativeRuntime,
		lifecycleGenerationTracking: response.negotiatedCapabilities.lifecycleGenerationTracking,
		recoveryGenerationTracking: response.negotiatedCapabilities.recoveryGenerationTracking,
		resumeToken: response.negotiatedCapabilities.resumeToken,
	},
	handshake: response.handshake,
	snapshot: mapRuntimeSnapshot(response.snapshot),
})

const unsupportedDaemonRpc = <A>(action: string): Effect.Effect<A, DaemonRpcActionError> =>
	Effect.fail(
		daemonRpcActionError({
			action,
			code: "unsupported",
			message: `Daemon RPC '${action}' is not implemented in the daemon package yet.`,
		}),
	)

type DaemonRpcMappedError =
	| BackendDaemonControlRestartConfigurationError
	| ImplementationRegistryDaemonError
	| TrackerIssueDaemonError
	| Error
	| { readonly _tag: string; readonly message?: string }

const mapControlError = (action: string, error: DaemonRpcMappedError): DaemonRpcActionError => {
	if (error instanceof BackendDaemonControlRestartConfigurationError) {
		switch (error.reason) {
			case "missing-project-path":
				return daemonRpcActionError({
					action,
					code: "missing-project-path",
					message: "Daemon restart requires a project path because no sync runtime is active yet.",
				})
		}
	}

	if (error instanceof TrackerIssueDaemonError) {
		switch (error.reason) {
			case "unsupported-backend":
				return daemonRpcActionError({
					action,
					code: "unsupported-backend",
					message: error.message,
				})
			case "unsupported-field":
				return daemonRpcActionError({
					action,
					code: "unsupported-field",
					message: error.message,
				})
			case "command-failed":
				return daemonRpcActionError({
					action,
					code: "command-failed",
					message: error.message,
				})
			case "json-parse":
				return daemonRpcActionError({
					action,
					code: "invalid-response",
					message: error.message,
				})
			case "not-found":
				return daemonRpcActionError({
					action,
					code: "not-found",
					message: error.message,
				})
		}
	}

	if (error instanceof ImplementationRegistryDaemonError) {
		switch (error.reason) {
			case "invalid-name":
				return daemonRpcActionError({
					action,
					code: "invalid-name",
					message: error.message,
				})
			case "already-exists":
				return daemonRpcActionError({
					action,
					code: "already-exists",
					message: error.message,
				})
			case "not-found":
				return daemonRpcActionError({
					action,
					code: "not-found",
					message: error.message,
				})
			case "in-use":
				return daemonRpcActionError({
					action,
					code: "in-use",
					message: error.message,
				})
			case "storage":
				return daemonRpcActionError({
					action,
					code: "storage",
					message: error.message,
				})
		}
	}

	if ("_tag" in error) {
		switch (error._tag) {
			case "BackendDaemonProtocolVersionMismatchError":
				return daemonRpcActionError({
					action,
					code: "protocol-mismatch",
					message: "Client and daemon protocol versions are incompatible.",
				})
			case "BackendDaemonAuthorizationError":
				return daemonRpcActionError({
					action,
					code: "authorization-denied",
					message: "Client capability check denied the daemon RPC operation.",
				})
			default:
				break
		}
	}

	return daemonRpcActionError({
		action,
		code: "daemon-operation-failed",
		message: error.message ?? `Daemon RPC '${action}' failed.`,
	})
}

const catchDaemonRpcError =
	<A, E extends DaemonRpcMappedError>(action: string) =>
	(effect: Effect.Effect<A, E>) =>
		effect.pipe(Effect.mapError((error) => mapControlError(action, error)))

export const makeGlobalDaemonRpcHandlers = Effect.gen(function* () {
	const runtime = yield* BackendDaemonService
	const control = yield* BackendDaemonControlService
	const sessionRecovery = yield* BackendDaemonSessionRecovery
	const implementations = yield* ImplementationRegistryDaemonService
	const issues = yield* TrackerIssueDaemonService

	return {
		daemonStatus: (_request: DaemonStatusRequest) =>
			control.status().pipe(Effect.map(mapControlStatus), catchDaemonRpcError("status")),
		daemonHealth: (_request: DaemonHealthRequest) =>
			control.health().pipe(Effect.map(mapHealth), catchDaemonRpcError("health")),
		daemonLogs: (_request: DaemonLogsRequest) => unsupportedDaemonRpc<DaemonLogsResult>("logs"),
		daemonStop: (_request: DaemonStopRequest) =>
			control.stop().pipe(Effect.map(mapControlStatus), catchDaemonRpcError("stop")),
		daemonRestart: (request: DaemonRestartRequest) =>
			control
				.restart({
					projectPath: request.projectPath,
					intervalMs: request.intervalMs,
				})
				.pipe(Effect.map(mapControlStatus), catchDaemonRpcError("restart")),
		daemonAttach: (request: DaemonAttachRequest) =>
			runtime
				.registerClientAttach({
					clientId: request.clientId,
					protocolVersion: request.protocolVersion,
					requestedAtMs: request.requestedAtMs,
				})
				.pipe(Effect.map(mapAttachResponse), catchDaemonRpcError("attach")),
		daemonReconnect: (request: DaemonReconnectRequest) =>
			runtime
				.markClientReconnect({
					clientId: request.clientId,
					protocolVersion: request.protocolVersion,
					lastSeenRevision: request.lastSeenRevision,
					lastSeenLifecycleGeneration: request.lastSeenLifecycleGeneration,
					requestedAtMs: request.requestedAtMs,
				})
				.pipe(Effect.map(mapAttachResponse), catchDaemonRpcError("reconnect")),
		daemonHeartbeat: (request: DaemonHeartbeatRequest) =>
			runtime.registerClientHeartbeat(request.clientId, request.observedAtMs).pipe(
				Effect.map(
					(client): DaemonHeartbeatResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						client: mapClientState(client),
					}),
				),
				catchDaemonRpcError("heartbeat"),
			),
		daemonSessionSnapshot: (request: DaemonSessionSnapshotRequest) =>
			sessionRecovery.listActive(request.projectPath).pipe(
				Effect.map(
					(sessions): DaemonSessionSnapshotResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						capturedAtMs: Date.now(),
						sessions: sessions.map((session) => ({
							issueId: session.issueId,
							worktreePath: "",
							tmuxSessionName: "",
							state: session.state,
							startedAt: new Date(0).toISOString(),
							projectPath: request.projectPath,
						})),
					}),
				),
				catchDaemonRpcError("sessionSnapshot"),
			),
		daemonBoardReadModel: (_request: DaemonBoardReadModelRequest) =>
			unsupportedDaemonRpc<DaemonBoardReadModelResult>("boardReadModel"),
		daemonSessionStart: (_request: DaemonSessionStartRequest) =>
			unsupportedDaemonRpc<DaemonSessionMutationResult>("sessionStart"),
		daemonSessionStop: (_request: DaemonSessionStopRequest) =>
			unsupportedDaemonRpc<DaemonSessionMutationResult>("sessionStop"),
		daemonSessionPause: (_request: DaemonSessionPauseRequest) =>
			unsupportedDaemonRpc<DaemonSessionMutationResult>("sessionPause"),
		daemonSessionResume: (_request: DaemonSessionResumeRequest) =>
			unsupportedDaemonRpc<DaemonSessionMutationResult>("sessionResume"),
		daemonSessionRecover: (_request: DaemonSessionRecoverRequest) =>
			unsupportedDaemonRpc<DaemonSessionMutationResult>("sessionRecover"),
		daemonSessionUpdateState: (_request: DaemonSessionUpdateStateRequest) =>
			unsupportedDaemonRpc<DaemonSessionMutationResult>("sessionUpdateState"),
		daemonDevServerStatus: (request: DaemonDevServerStatusRequest) =>
			control
				.devServerStatus({
					issueId: request.issueId,
					serverName: request.serverName,
					projectPath: request.projectPath,
				})
				.pipe(
					Effect.map(
						(result): DaemonDevServerStatusResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							capturedAtMs: result.capturedAtMs,
							server: result.server,
						}),
					),
					catchDaemonRpcError("devServerStatus"),
				),
		daemonDevServerList: (request: DaemonDevServerListRequest) =>
			control
				.devServerList({
					issueId: request.issueId,
					projectPath: request.projectPath,
				})
				.pipe(
					Effect.map(
						(result): DaemonDevServerListResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							capturedAtMs: result.capturedAtMs,
							servers: result.servers,
						}),
					),
					catchDaemonRpcError("devServerList"),
				),
		daemonDevServerStart: (request: DaemonDevServerStartRequest) =>
			control
				.devServerStart({
					issueId: request.issueId,
					serverName: request.serverName,
					projectPath: request.projectPath,
				})
				.pipe(
					Effect.map(
						(result): DaemonDevServerMutationResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							capturedAtMs: result.capturedAtMs,
							server: result.server,
						}),
					),
					catchDaemonRpcError("devServerStart"),
				),
		daemonDevServerStop: (request: DaemonDevServerStopRequest) =>
			control
				.devServerStop({
					issueId: request.issueId,
					serverName: request.serverName,
					projectPath: request.projectPath,
				})
				.pipe(
					Effect.map(
						(result): DaemonDevServerMutationResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							capturedAtMs: result.capturedAtMs,
							server: result.server,
						}),
					),
					catchDaemonRpcError("devServerStop"),
				),
		daemonQueueEnqueue: (request: DaemonQueueEnqueueRequest) =>
			control
				.queueEnqueue({
					domain: request.domain,
					operation: request.operation,
					projectPath: request.projectPath,
					issueId: request.issueId,
					dedupeKey: request.dedupeKey,
					payloadJson: request.payloadJson,
				})
				.pipe(
					Effect.map(
						(result): DaemonQueueEnqueueResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							acceptedAtMs: result.acceptedAtMs,
							item: result.item,
						}),
					),
					catchDaemonRpcError("queueEnqueue"),
				),
		daemonQueueQuery: (request: DaemonQueueQueryRequest) =>
			control
				.queueQuery({
					projectPath: request.projectPath,
					domain: request.domain,
					operationId: request.operationId,
					issueId: request.issueId,
					limit: request.limit,
				})
				.pipe(
					Effect.map(
						(result): DaemonQueueQueryResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							queriedAtMs: result.queriedAtMs,
							items: result.items,
						}),
					),
					catchDaemonRpcError("queueQuery"),
				),
		daemonQueueCancel: (request: DaemonQueueCancelRequest) =>
			control
				.queueCancel({
					projectPath: request.projectPath,
					domain: request.domain,
					operationId: request.operationId,
					issueId: request.issueId,
				})
				.pipe(
					Effect.map(
						(result): DaemonQueueCancelResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							cancelledAtMs: result.cancelledAtMs,
							cancelledOperationIds: result.cancelledOperationIds,
						}),
					),
					catchDaemonRpcError("queueCancel"),
				),
		daemonImplementationGetRegistry: (request: DaemonImplementationGetRegistryRequest) =>
			implementations.getRegistry(request.projectPath).pipe(
				Effect.map(
					(registry): DaemonImplementationGetRegistryResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						registry,
					}),
				),
				catchDaemonRpcError("implementationGetRegistry"),
			),
		daemonImplementationCreate: (request: DaemonImplementationCreateRequest) =>
			implementations.create(request.input, request.projectPath).pipe(
				Effect.map(
					(implementation): DaemonImplementationCreateResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						implementation,
					}),
				),
				catchDaemonRpcError("implementationCreate"),
			),
		daemonImplementationUpdate: (request: DaemonImplementationUpdateRequest) =>
			implementations.update(request.currentName, request.fields, request.projectPath).pipe(
				Effect.map(
					(implementation): DaemonImplementationUpdateResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						implementation,
					}),
				),
				catchDaemonRpcError("implementationUpdate"),
			),
		daemonImplementationDelete: (request: DaemonImplementationDeleteRequest) =>
			implementations.delete(request.name, request.projectPath).pipe(
				Effect.map(
					(): DaemonImplementationDeleteResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						deleted: true,
					}),
				),
				catchDaemonRpcError("implementationDelete"),
			),
		daemonImplementationSetDefault: (request: DaemonImplementationSetDefaultRequest) =>
			implementations.setDefault(request.name, request.projectPath).pipe(
				Effect.map(
					(registry): DaemonImplementationSetDefaultResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						registry,
					}),
				),
				catchDaemonRpcError("implementationSetDefault"),
			),
		daemonIssueGet: (request: DaemonIssueGetRequest) =>
			issues.get(request.issueId, request.projectPath).pipe(
				Effect.map(
					(issue): DaemonIssueGetResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						issue,
					}),
				),
				catchDaemonRpcError("issueGet"),
			),
		daemonIssueList: (request: DaemonIssueListRequest) =>
			issues.list(request.filters, request.projectPath, request.options).pipe(
				Effect.map(
					(issuesList): DaemonIssueListResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						issues: [...issuesList],
					}),
				),
				catchDaemonRpcError("issueList"),
			),
		daemonIssueCreate: (request: DaemonIssueCreateRequest) =>
			issues.create(request.input, request.projectPath).pipe(
				Effect.map(
					(issue): DaemonIssueCreateResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						issue,
					}),
				),
				catchDaemonRpcError("issueCreate"),
			),
		daemonIssueUpdate: (request: DaemonIssueUpdateRequest) =>
			issues.update(request.issueId, request.patch, request.projectPath).pipe(
				Effect.map(
					(): DaemonIssueUpdateResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						updated: true,
					}),
				),
				catchDaemonRpcError("issueUpdate"),
			),
		daemonIssueAddDependency: (request: DaemonIssueAddDependencyRequest) =>
			issues
				.addDependency(
					request.issueId,
					request.dependsOnId,
					request.dependencyType,
					request.projectPath,
				)
				.pipe(
					Effect.map(
						(): DaemonIssueDependencyMutationResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							updated: true,
						}),
					),
					catchDaemonRpcError("issueAddDependency"),
				),
		daemonIssueRemoveDependency: (request: DaemonIssueRemoveDependencyRequest) =>
			issues
				.removeDependency(
					request.issueId,
					request.dependsOnId,
					request.dependencyType,
					request.projectPath,
				)
				.pipe(
					Effect.map(
						(): DaemonIssueDependencyMutationResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							updated: true,
						}),
					),
					catchDaemonRpcError("issueRemoveDependency"),
				),
		daemonIssueClose: (request: DaemonIssueCloseRequest) =>
			issues.close(request.issueId, request.reason, request.projectPath).pipe(
				Effect.map(
					(): DaemonIssueCloseResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						closed: true,
					}),
				),
				catchDaemonRpcError("issueClose"),
			),
		daemonIssueDelete: (request: DaemonIssueDeleteRequest) =>
			issues.delete(request.issueId, request.projectPath).pipe(
				Effect.map(
					(): DaemonIssueDeleteResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						deleted: true,
					}),
				),
				catchDaemonRpcError("issueDelete"),
			),
		daemonIssueSync: (request: DaemonIssueSyncRequest) =>
			issues.sync(request.projectPath).pipe(
				Effect.map(
					(sync): DaemonIssueSyncResultEnvelope => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						sync,
					}),
				),
				catchDaemonRpcError("issueSync"),
			),
		daemonEventStream: (_request: DaemonEventStreamRequest) =>
			unsupportedDaemonRpc<DaemonEventStreamResult>("eventStream"),
	}
})

export const GlobalDaemonRpcHandlersLive = Layer.scopedContext(
	DaemonRpcGroup.toHandlersContext(makeGlobalDaemonRpcHandlers),
)
