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

export interface DaemonRpcClientApi {
	readonly status: (
		request?: Omit<DaemonStatusRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonControlStatusResult, DaemonRpcClientError>
	readonly health: (
		request?: Omit<DaemonHealthRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonHealthResult, DaemonRpcClientError>
	readonly logs: (
		request?: Omit<DaemonLogsRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonLogsResult, DaemonRpcClientError>
	readonly stop: (
		request?: Omit<DaemonStopRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonControlStatusResult, DaemonRpcClientError>
	readonly restart: (
		request?: Omit<DaemonRestartRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonControlStatusResult, DaemonRpcClientError>
	readonly attach: (
		request: Omit<DaemonAttachRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachReconnectResult, DaemonRpcClientError>
	readonly reconnect: (
		request: Omit<DaemonReconnectRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachReconnectResult, DaemonRpcClientError>
	readonly heartbeat: (
		request: Omit<DaemonHeartbeatRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonHeartbeatResult, DaemonRpcClientError>
	readonly eventStream?: (
		request: Omit<DaemonEventStreamRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonEventStreamResult, DaemonRpcClientError>
	readonly sessionSnapshot?: (
		request: Omit<DaemonSessionSnapshotRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionSnapshotResult, DaemonRpcClientError>
	readonly boardReadModel?: (
		request: Omit<DaemonBoardReadModelRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonBoardReadModelResult, DaemonRpcClientError>
	readonly sessionStart?: (
		request: Omit<DaemonSessionStartRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionStop?: (
		request: Omit<DaemonSessionStopRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionPause?: (
		request: Omit<DaemonSessionPauseRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionResume?: (
		request: Omit<DaemonSessionResumeRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionRecover?: (
		request: Omit<DaemonSessionRecoverRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly sessionUpdateState?: (
		request: Omit<DaemonSessionUpdateStateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionMutationResult, DaemonRpcClientError>
	readonly devServerStatus?: (
		request: Omit<DaemonDevServerStatusRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonDevServerStatusResult, DaemonRpcClientError>
	readonly devServerList?: (
		request: Omit<DaemonDevServerListRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonDevServerListResult, DaemonRpcClientError>
	readonly devServerStart?: (
		request: Omit<DaemonDevServerStartRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonDevServerMutationResult, DaemonRpcClientError>
	readonly devServerStop?: (
		request: Omit<DaemonDevServerStopRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonDevServerMutationResult, DaemonRpcClientError>
	readonly queueEnqueue?: (
		request: Omit<DaemonQueueEnqueueRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonQueueEnqueueResult, DaemonRpcClientError>
	readonly queueQuery?: (
		request: Omit<DaemonQueueQueryRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonQueueQueryResult, DaemonRpcClientError>
	readonly queueCancel?: (
		request: Omit<DaemonQueueCancelRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonQueueCancelResult, DaemonRpcClientError>
}

const makeDaemonRpcClient = Effect.gen(function* () {
	const raw = yield* RpcClient.make(DaemonRpcGroup)
	return {
		status: (request) =>
			raw.daemonStatus(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		health: (request) =>
			raw.daemonHealth(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		logs: (request) =>
			raw.daemonLogs(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		stop: (request) =>
			raw.daemonStop(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		restart: (request) =>
			raw.daemonRestart(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		attach: (request) =>
			raw.daemonAttach({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		reconnect: (request) =>
			raw.daemonReconnect({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		heartbeat: (request) =>
			raw.daemonHeartbeat({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		eventStream: (request) =>
			raw.daemonEventStream({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionSnapshot: (request) =>
			raw.daemonSessionSnapshot({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		boardReadModel: (request) =>
			raw.daemonBoardReadModel({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionStart: (request) =>
			raw.daemonSessionStart({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionStop: (request) =>
			raw.daemonSessionStop({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionPause: (request) =>
			raw.daemonSessionPause({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionResume: (request) =>
			raw.daemonSessionResume({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionRecover: (request) =>
			raw.daemonSessionRecover({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		sessionUpdateState: (request) =>
			raw.daemonSessionUpdateState({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		devServerStatus: (request) =>
			raw.daemonDevServerStatus({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		devServerList: (request) =>
			raw.daemonDevServerList({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		devServerStart: (request) =>
			raw.daemonDevServerStart({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		devServerStop: (request) =>
			raw.daemonDevServerStop({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		queueEnqueue: (request) =>
			raw.daemonQueueEnqueue({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		queueQuery: (request) =>
			raw.daemonQueueQuery({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		queueCancel: (request) =>
			raw.daemonQueueCancel({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
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
