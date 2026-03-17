import * as BunSocket from "@effect/platform-bun/BunSocket"
import * as RpcClient from "@effect/rpc/RpcClient"
import type { RpcClientError } from "@effect/rpc/RpcClientError"
import * as RpcSerialization from "@effect/rpc/RpcSerialization"
import { Context, Data, Effect, Layer } from "effect"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonAttachReconnectResult,
	type DaemonAttachRequest,
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

export type DaemonRpcOperation =
	| "status"
	| "health"
	| "logs"
	| "stop"
	| "restart"
	| "attach"
	| "reconnect"
	| "heartbeat"
	| "eventStream"
	| "sessionSnapshot"
	| "sessionStart"
	| "sessionStop"
	| "sessionPause"
	| "sessionResume"
	| "sessionRecover"
	| "sessionUpdateState"
	| "devServerStatus"
	| "devServerList"
	| "devServerStart"
	| "devServerStop"
	| "queueEnqueue"
	| "queueQuery"
	| "queueCancel"

export class DaemonRpcProtocolVersionMismatchError extends Data.TaggedError(
	"DaemonRpcProtocolVersionMismatchError",
)<{
	readonly operation: DaemonRpcOperation
	readonly expectedProtocolVersion: number
	readonly receivedProtocolVersion: number
}> {}

export class DaemonRpcTransportError extends Data.TaggedError("DaemonRpcTransportError")<{
	readonly operation: DaemonRpcOperation
	readonly reason: "transport" | "unknown"
	readonly message: string
	readonly suggestion: string
}> {}

export class DaemonRpcRemoteActionError extends Data.TaggedError("DaemonRpcRemoteActionError")<{
	readonly operation: DaemonRpcOperation
	readonly code: string
	readonly message: string
	readonly action: string | undefined
}> {}

const DAEMON_RPC_CALL_TIMEOUT_MS = 5_000

export class DaemonRpcTimeoutError extends Data.TaggedError("DaemonRpcTimeoutError")<{
	readonly operation: DaemonRpcOperation
	readonly timeoutMs: number
}> {}

export type DaemonRpcClientError =
	| DaemonRpcProtocolVersionMismatchError
	| DaemonRpcTransportError
	| DaemonRpcRemoteActionError
	| DaemonRpcTimeoutError

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
		request?: Omit<DaemonSessionSnapshotRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSessionSnapshotResult, DaemonRpcClientError>
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
		request?: Omit<DaemonDevServerListRequest, "rpcProtocolVersion">,
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
		request?: Omit<DaemonQueueQueryRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonQueueQueryResult, DaemonRpcClientError>
	readonly queueCancel?: (
		request?: Omit<DaemonQueueCancelRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonQueueCancelResult, DaemonRpcClientError>
}

type WireRpcError = RpcClientError | DaemonRpcActionError | DaemonRpcTimeoutError

export interface DaemonRpcWireClient {
	readonly daemonStatus: (
		input: DaemonStatusRequest,
	) => Effect.Effect<DaemonControlStatusResult, WireRpcError>
	readonly daemonHealth: (
		input: DaemonHealthRequest,
	) => Effect.Effect<DaemonHealthResult, WireRpcError>
	readonly daemonLogs: (input: DaemonLogsRequest) => Effect.Effect<DaemonLogsResult, WireRpcError>
	readonly daemonStop: (
		input: DaemonStopRequest,
	) => Effect.Effect<DaemonControlStatusResult, WireRpcError>
	readonly daemonRestart: (
		input: DaemonRestartRequest,
	) => Effect.Effect<DaemonControlStatusResult, WireRpcError>
	readonly daemonAttach: (
		input: DaemonAttachRequest,
	) => Effect.Effect<DaemonAttachReconnectResult, WireRpcError>
	readonly daemonReconnect: (
		input: DaemonReconnectRequest,
	) => Effect.Effect<DaemonAttachReconnectResult, WireRpcError>
	readonly daemonHeartbeat: (
		input: DaemonHeartbeatRequest,
	) => Effect.Effect<DaemonHeartbeatResult, WireRpcError>
	readonly daemonEventStream: (
		input: DaemonEventStreamRequest,
	) => Effect.Effect<DaemonEventStreamResult, WireRpcError>
	readonly daemonSessionSnapshot: (
		input: DaemonSessionSnapshotRequest,
	) => Effect.Effect<DaemonSessionSnapshotResult, WireRpcError>
	readonly daemonSessionStart: (
		input: DaemonSessionStartRequest,
	) => Effect.Effect<DaemonSessionMutationResult, WireRpcError>
	readonly daemonSessionStop: (
		input: DaemonSessionStopRequest,
	) => Effect.Effect<DaemonSessionMutationResult, WireRpcError>
	readonly daemonSessionPause: (
		input: DaemonSessionPauseRequest,
	) => Effect.Effect<DaemonSessionMutationResult, WireRpcError>
	readonly daemonSessionResume: (
		input: DaemonSessionResumeRequest,
	) => Effect.Effect<DaemonSessionMutationResult, WireRpcError>
	readonly daemonSessionRecover: (
		input: DaemonSessionRecoverRequest,
	) => Effect.Effect<DaemonSessionMutationResult, WireRpcError>
	readonly daemonSessionUpdateState: (
		input: DaemonSessionUpdateStateRequest,
	) => Effect.Effect<DaemonSessionMutationResult, WireRpcError>
	readonly daemonDevServerStatus: (
		input: DaemonDevServerStatusRequest,
	) => Effect.Effect<DaemonDevServerStatusResult, WireRpcError>
	readonly daemonDevServerList: (
		input: DaemonDevServerListRequest,
	) => Effect.Effect<DaemonDevServerListResult, WireRpcError>
	readonly daemonDevServerStart: (
		input: DaemonDevServerStartRequest,
	) => Effect.Effect<DaemonDevServerMutationResult, WireRpcError>
	readonly daemonDevServerStop: (
		input: DaemonDevServerStopRequest,
	) => Effect.Effect<DaemonDevServerMutationResult, WireRpcError>
	readonly daemonQueueEnqueue: (
		input: DaemonQueueEnqueueRequest,
	) => Effect.Effect<DaemonQueueEnqueueResult, WireRpcError>
	readonly daemonQueueQuery: (
		input: DaemonQueueQueryRequest,
	) => Effect.Effect<DaemonQueueQueryResult, WireRpcError>
	readonly daemonQueueCancel: (
		input: DaemonQueueCancelRequest,
	) => Effect.Effect<DaemonQueueCancelResult, WireRpcError>
}

type MapWireError = WireRpcError | DaemonRpcProtocolVersionMismatchError

const mapWireError = (operation: DaemonRpcOperation, error: MapWireError): DaemonRpcClientError => {
	switch (error._tag) {
		case "DaemonRpcTimeoutError":
		case "DaemonRpcProtocolVersionMismatchError":
			return error
		case "DaemonRpcActionError":
			return new DaemonRpcRemoteActionError({
				operation,
				code: error.code,
				message: error.message,
				action: error.action,
			})
		case "RpcClientError":
			return new DaemonRpcTransportError({
				operation,
				reason: "transport",
				message: error.message,
				suggestion:
					"Verify daemon socket availability, then run `az daemon health` and `az daemon logs`.",
			})
	}
	const exhaustive: never = error
	return exhaustive
}

const ensureCompatibleRpcVersion = <T extends { readonly rpcProtocolVersion: number }>(
	operation: DaemonRpcOperation,
	response: T,
): Effect.Effect<T, DaemonRpcProtocolVersionMismatchError> => {
	if (response.rpcProtocolVersion !== DAEMON_RPC_PROTOCOL_VERSION) {
		return Effect.fail(
			new DaemonRpcProtocolVersionMismatchError({
				operation,
				expectedProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				receivedProtocolVersion: response.rpcProtocolVersion,
			}),
		)
	}
	return Effect.succeed(response)
}

export const makeDaemonRpcClientFromWire = (wire: DaemonRpcWireClient): DaemonRpcClientApi => ({
	status: (request) =>
		wire
			.daemonStatus(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("status", response)),
				Effect.mapError((error) => mapWireError("status", error)),
			),
	health: (request) =>
		wire
			.daemonHealth(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("health", response)),
				Effect.mapError((error) => mapWireError("health", error)),
			),
	logs: (request) =>
		wire
			.daemonLogs(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("logs", response)),
				Effect.mapError((error) => mapWireError("logs", error)),
			),
	stop: (request) =>
		wire
			.daemonStop(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("stop", response)),
				Effect.mapError((error) => mapWireError("stop", error)),
			),
	restart: (request) =>
		wire
			.daemonRestart({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("restart", response)),
				Effect.mapError((error) => mapWireError("restart", error)),
			),
	attach: (request) =>
		wire
			.daemonAttach({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("attach", response)),
				Effect.mapError((error) => mapWireError("attach", error)),
			),
	reconnect: (request) =>
		wire
			.daemonReconnect({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("reconnect", response)),
				Effect.mapError((error) => mapWireError("reconnect", error)),
			),
	heartbeat: (request) =>
		wire
			.daemonHeartbeat({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("heartbeat", response)),
				Effect.mapError((error) => mapWireError("heartbeat", error)),
			),
	eventStream: (request) =>
		wire
			.daemonEventStream({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("eventStream", response)),
				Effect.mapError((error) => mapWireError("eventStream", error)),
			),
	sessionSnapshot: (request) =>
		wire
			.daemonSessionSnapshot(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionSnapshot", response)),
				Effect.mapError((error) => mapWireError("sessionSnapshot", error)),
			),
	sessionStart: (request) =>
		wire
			.daemonSessionStart({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionStart", response)),
				Effect.mapError((error) => mapWireError("sessionStart", error)),
			),
	sessionStop: (request) =>
		wire
			.daemonSessionStop({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionStop", response)),
				Effect.mapError((error) => mapWireError("sessionStop", error)),
			),
	sessionPause: (request) =>
		wire
			.daemonSessionPause({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionPause", response)),
				Effect.mapError((error) => mapWireError("sessionPause", error)),
			),
	sessionResume: (request) =>
		wire
			.daemonSessionResume({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionResume", response)),
				Effect.mapError((error) => mapWireError("sessionResume", error)),
			),
	sessionRecover: (request) =>
		wire
			.daemonSessionRecover({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionRecover", response)),
				Effect.mapError((error) => mapWireError("sessionRecover", error)),
			),
	sessionUpdateState: (request) =>
		wire
			.daemonSessionUpdateState({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("sessionUpdateState", response)),
				Effect.mapError((error) => mapWireError("sessionUpdateState", error)),
			),
	devServerStatus: (request) =>
		wire
			.daemonDevServerStatus({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("devServerStatus", response)),
				Effect.mapError((error) => mapWireError("devServerStatus", error)),
			),
	devServerList: (request) =>
		wire
			.daemonDevServerList(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("devServerList", response)),
				Effect.mapError((error) => mapWireError("devServerList", error)),
			),
	devServerStart: (request) =>
		wire
			.daemonDevServerStart({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("devServerStart", response)),
				Effect.mapError((error) => mapWireError("devServerStart", error)),
			),
	devServerStop: (request) =>
		wire
			.daemonDevServerStop({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("devServerStop", response)),
				Effect.mapError((error) => mapWireError("devServerStop", error)),
			),
	queueEnqueue: (request) =>
		wire
			.daemonQueueEnqueue({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			})
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("queueEnqueue", response)),
				Effect.mapError((error) => mapWireError("queueEnqueue", error)),
			),
	queueQuery: (request) =>
		wire
			.daemonQueueQuery(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("queueQuery", response)),
				Effect.mapError((error) => mapWireError("queueQuery", error)),
			),
	queueCancel: (request) =>
		wire
			.daemonQueueCancel(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			)
			.pipe(
				Effect.flatMap((response) => ensureCompatibleRpcVersion("queueCancel", response)),
				Effect.mapError((error) => mapWireError("queueCancel", error)),
			),
})

const makeDaemonRpcClient = Effect.gen(function* () {
	const raw = yield* RpcClient.make(DaemonRpcGroup)
	const withRpcTimeout = <A, E>(
		operation: DaemonRpcOperation,
		effect: Effect.Effect<A, E>,
	): Effect.Effect<A, E | DaemonRpcTimeoutError> =>
		effect.pipe(
			Effect.disconnect,
			Effect.timeoutFail({
				duration: `${DAEMON_RPC_CALL_TIMEOUT_MS} millis`,
				onTimeout: () =>
					new DaemonRpcTimeoutError({
						operation,
						timeoutMs: DAEMON_RPC_CALL_TIMEOUT_MS,
					}),
			}),
		)

	const wire: DaemonRpcWireClient = {
		daemonStatus: (input) => withRpcTimeout("status", raw.daemonStatus(input)),
		daemonHealth: (input) => withRpcTimeout("health", raw.daemonHealth(input)),
		daemonLogs: (input) => withRpcTimeout("logs", raw.daemonLogs(input)),
		daemonStop: (input) => withRpcTimeout("stop", raw.daemonStop(input)),
		daemonRestart: (input) => withRpcTimeout("restart", raw.daemonRestart(input)),
		daemonAttach: (input) => withRpcTimeout("attach", raw.daemonAttach(input)),
		daemonReconnect: (input) => withRpcTimeout("reconnect", raw.daemonReconnect(input)),
		daemonHeartbeat: (input) => withRpcTimeout("heartbeat", raw.daemonHeartbeat(input)),
		daemonEventStream: (input) => withRpcTimeout("eventStream", raw.daemonEventStream(input)),
		daemonSessionSnapshot: (input) =>
			withRpcTimeout("sessionSnapshot", raw.daemonSessionSnapshot(input)),
		daemonSessionStart: (input) => withRpcTimeout("sessionStart", raw.daemonSessionStart(input)),
		daemonSessionStop: (input) => withRpcTimeout("sessionStop", raw.daemonSessionStop(input)),
		daemonSessionPause: (input) => withRpcTimeout("sessionPause", raw.daemonSessionPause(input)),
		daemonSessionResume: (input) => withRpcTimeout("sessionResume", raw.daemonSessionResume(input)),
		daemonSessionRecover: (input) =>
			withRpcTimeout("sessionRecover", raw.daemonSessionRecover(input)),
		daemonSessionUpdateState: (input) =>
			withRpcTimeout("sessionUpdateState", raw.daemonSessionUpdateState(input)),
		daemonDevServerStatus: (input) =>
			withRpcTimeout("devServerStatus", raw.daemonDevServerStatus(input)),
		daemonDevServerList: (input) => withRpcTimeout("devServerList", raw.daemonDevServerList(input)),
		daemonDevServerStart: (input) =>
			withRpcTimeout("devServerStart", raw.daemonDevServerStart(input)),
		daemonDevServerStop: (input) => withRpcTimeout("devServerStop", raw.daemonDevServerStop(input)),
		daemonQueueEnqueue: (input) => withRpcTimeout("queueEnqueue", raw.daemonQueueEnqueue(input)),
		daemonQueueQuery: (input) => withRpcTimeout("queueQuery", raw.daemonQueueQuery(input)),
		daemonQueueCancel: (input) => withRpcTimeout("queueCancel", raw.daemonQueueCancel(input)),
	}
	return makeDaemonRpcClientFromWire(wire)
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
