import * as BunSocket from "@effect/platform-bun/BunSocket"
import * as RpcClient from "@effect/rpc/RpcClient"
import type { RpcClientError } from "@effect/rpc/RpcClientError"
import * as RpcSerialization from "@effect/rpc/RpcSerialization"
import { Context, Effect, Layer } from "effect"
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
	type DaemonImplementationRegistryResult,
	type DaemonIssueCreateRequest,
	type DaemonIssueCreateResult,
	type DaemonIssueDeleteRequest,
	type DaemonIssueDeleteResult,
	type DaemonIssueEpicChildrenRequest,
	type DaemonIssueEpicChildrenResult,
	type DaemonIssueEpicWithChildrenResult,
	type DaemonIssueParentEpicRequest,
	type DaemonIssueParentEpicResult,
	type DaemonIssueShowRequest,
	type DaemonIssueShowResult,
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
} from "./DaemonRpcSchemas.js"
import { DaemonRpcGroup } from "./DaemonRpcs.js"

export type DaemonRpcClientError = RpcClientError | DaemonRpcActionError
export type DaemonRpcClientFailureKind = "protocol-mismatch" | "transport" | "remote-action"

export const classifyDaemonRpcClientError = (
	error: DaemonRpcClientError,
): DaemonRpcClientFailureKind => {
	switch (error._tag) {
		case "DaemonRpcActionError":
			return "remote-action"
		case "RpcClientError":
			return error.reason === "Protocol" ? "protocol-mismatch" : "transport"
	}
}

export const isDaemonRpcClientProtocolMismatch = (
	error: DaemonRpcClientError,
): error is RpcClientError => classifyDaemonRpcClientError(error) === "protocol-mismatch"

export const isDaemonRpcClientRetryableTransport = (
	error: DaemonRpcClientError,
): error is RpcClientError => classifyDaemonRpcClientError(error) === "transport"

export interface DaemonRpcClientApi {
	readonly status: (
		request?: DaemonStatusRequest,
	) => Effect.Effect<DaemonControlStatusResult, DaemonRpcClientError>
	readonly health: (
		request?: DaemonHealthRequest,
	) => Effect.Effect<DaemonHealthResult, DaemonRpcClientError>
	readonly logs: (
		request?: DaemonLogsRequest,
	) => Effect.Effect<DaemonLogsResult, DaemonRpcClientError>
	readonly stop: (
		request?: DaemonStopRequest,
	) => Effect.Effect<DaemonControlStatusResult, DaemonRpcClientError>
	readonly restart: (
		request?: DaemonRestartRequest,
	) => Effect.Effect<DaemonControlStatusResult, DaemonRpcClientError>
	readonly attach: (
		request: DaemonAttachRequest,
	) => Effect.Effect<DaemonAttachReconnectResult, DaemonRpcClientError>
	readonly reconnect: (
		request: DaemonReconnectRequest,
	) => Effect.Effect<DaemonAttachReconnectResult, DaemonRpcClientError>
	readonly heartbeat: (
		request: DaemonHeartbeatRequest,
	) => Effect.Effect<DaemonHeartbeatResult, DaemonRpcClientError>
	readonly eventStream?: (
		request: DaemonEventStreamRequest,
	) => Effect.Effect<DaemonEventStreamResult, DaemonRpcClientError>
	readonly sessionSnapshot?: (
		request: DaemonSessionSnapshotRequest,
	) => Effect.Effect<DaemonSessionSnapshotResult, DaemonRpcClientError>
	readonly boardReadModel?: (
		request: DaemonBoardReadModelRequest,
	) => Effect.Effect<DaemonBoardReadModelResult, DaemonRpcClientError>
	readonly sessionStart?: (
		request: DaemonSessionStartRequest,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionStop?: (
		request: DaemonSessionStopRequest,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionPause?: (
		request: DaemonSessionPauseRequest,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionResume?: (
		request: DaemonSessionResumeRequest,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionRecover?: (
		request: DaemonSessionRecoverRequest,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionUpdateState?: (
		request: DaemonSessionUpdateStateRequest,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly devServerStatus?: (
		request: DaemonDevServerStatusRequest,
	) => Effect.Effect<DaemonDevServerStatusResult, DaemonRpcClientError>
	readonly devServerList?: (
		request: DaemonDevServerListRequest,
	) => Effect.Effect<DaemonDevServerListResult, DaemonRpcClientError>
	readonly devServerStart?: (
		request: DaemonDevServerStartRequest,
	) => Effect.Effect<DaemonDevServerMutationResult, DaemonRpcClientError>
	readonly devServerStop?: (
		request: DaemonDevServerStopRequest,
	) => Effect.Effect<DaemonDevServerMutationResult, DaemonRpcClientError>
	readonly queueEnqueue?: (
		request: DaemonQueueEnqueueRequest,
	) => Effect.Effect<DaemonQueueEnqueueResult, DaemonRpcClientError>
	readonly queueQuery?: (
		request: DaemonQueueQueryRequest,
	) => Effect.Effect<DaemonQueueQueryResult, DaemonRpcClientError>
	readonly queueCancel?: (
		request: DaemonQueueCancelRequest,
	) => Effect.Effect<DaemonQueueCancelResult, DaemonRpcClientError>
	readonly issueCreate?: (
		request: DaemonIssueCreateRequest,
	) => Effect.Effect<DaemonIssueCreateResult, DaemonRpcClientError>
	readonly issueUpdate?: (
		request: DaemonIssueUpdateRequest,
	) => Effect.Effect<DaemonIssueUpdateResult, DaemonRpcClientError>
	readonly issueDelete?: (
		request: DaemonIssueDeleteRequest,
	) => Effect.Effect<DaemonIssueDeleteResult, DaemonRpcClientError>
	readonly issueShow?: (
		request: DaemonIssueShowRequest,
	) => Effect.Effect<DaemonIssueShowResult, DaemonRpcClientError>
	readonly issueEpicChildren?: (
		request: DaemonIssueEpicChildrenRequest,
	) => Effect.Effect<DaemonIssueEpicChildrenResult, DaemonRpcClientError>
	readonly issueEpicWithChildren?: (
		request: DaemonIssueEpicChildrenRequest,
	) => Effect.Effect<DaemonIssueEpicWithChildrenResult, DaemonRpcClientError>
	readonly issueParentEpic?: (
		request: DaemonIssueParentEpicRequest,
	) => Effect.Effect<DaemonIssueParentEpicResult, DaemonRpcClientError>
	readonly issueImplementationRegistry?: () => Effect.Effect<
		DaemonImplementationRegistryResult,
		DaemonRpcClientError
	>
}

const makeDaemonRpcClient = Effect.gen(function* () {
	const raw = yield* RpcClient.make(DaemonRpcGroup)
	return {
		status: (request) => raw.daemonStatus(request ?? {}),
		health: (request) => raw.daemonHealth(request ?? {}),
		logs: (request) => raw.daemonLogs(request ?? {}),
		stop: (request) => raw.daemonStop(request ?? {}),
		restart: (request) => raw.daemonRestart(request ?? {}),
		attach: (request) =>
			raw.daemonAttach({
				...request,
				protocolVersion: request.protocolVersion ?? DAEMON_RPC_PROTOCOL_VERSION,
			}),
		reconnect: (request) =>
			raw.daemonReconnect({
				...request,
				protocolVersion: request.protocolVersion ?? DAEMON_RPC_PROTOCOL_VERSION,
			}),
		heartbeat: (request) => raw.daemonHeartbeat(request),
		eventStream: (request) => raw.daemonEventStream(request),
		sessionSnapshot: (request) => raw.daemonSessionSnapshot(request),
		boardReadModel: (request) => raw.daemonBoardReadModel(request),
		sessionStart: (request) => raw.daemonSessionStart(request),
		sessionStop: (request) => raw.daemonSessionStop(request),
		sessionPause: (request) => raw.daemonSessionPause(request),
		sessionResume: (request) => raw.daemonSessionResume(request),
		sessionRecover: (request) => raw.daemonSessionRecover(request),
		sessionUpdateState: (request) => raw.daemonSessionUpdateState(request),
		devServerStatus: (request) => raw.daemonDevServerStatus(request),
		devServerList: (request) => raw.daemonDevServerList(request),
		devServerStart: (request) => raw.daemonDevServerStart(request),
		devServerStop: (request) => raw.daemonDevServerStop(request),
		queueEnqueue: (request) => raw.daemonQueueEnqueue(request),
		queueQuery: (request) => raw.daemonQueueQuery(request),
		queueCancel: (request) => raw.daemonQueueCancel(request),
		issueCreate: (request) => raw.daemonIssueCreate(request),
		issueUpdate: (request) => raw.daemonIssueUpdate(request),
		issueDelete: (request) => raw.daemonIssueDelete(request),
		issueShow: (request) => raw.daemonIssueShow(request),
		issueEpicChildren: (request) => raw.daemonIssueEpicChildren(request),
		issueEpicWithChildren: (request) => raw.daemonIssueEpicWithChildren(request),
		issueParentEpic: (request) => raw.daemonIssueParentEpic(request),
		issueImplementationRegistry: () => raw.daemonIssueImplementationRegistry({}),
	} satisfies DaemonRpcClientApi
})

export class DaemonRpcClient extends Context.Tag("DaemonRpcClient")<
	DaemonRpcClient,
	DaemonRpcClientApi
>() {}

export const layerSocket = (url: string) =>
	Layer.scoped(DaemonRpcClient, makeDaemonRpcClient).pipe(
		Layer.provide(
			RpcClient.layerProtocolSocket().pipe(
				Layer.provideMerge(BunSocket.layerWebSocket(url)),
				Layer.provideMerge(RpcSerialization.layerMsgPack),
			),
		),
	)
