import * as BunSocket from "@effect/platform-bun/BunSocket"
import * as RpcClient from "@effect/rpc/RpcClient"
import { RpcClientError } from "@effect/rpc/RpcClientError"
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

export type DaemonRpcClientError =
	| DaemonRpcProtocolVersionMismatchError
	| DaemonRpcTransportError
	| DaemonRpcRemoteActionError

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

export interface DaemonRpcWireClient {
	readonly daemonStatus: (
		input: DaemonStatusRequest,
	) => Effect.Effect<DaemonControlStatusResult, RpcClientError | DaemonRpcActionError>
	readonly daemonHealth: (
		input: DaemonHealthRequest,
	) => Effect.Effect<DaemonHealthResult, RpcClientError | DaemonRpcActionError>
	readonly daemonLogs: (
		input: DaemonLogsRequest,
	) => Effect.Effect<DaemonLogsResult, RpcClientError | DaemonRpcActionError>
	readonly daemonStop: (
		input: DaemonStopRequest,
	) => Effect.Effect<DaemonControlStatusResult, RpcClientError | DaemonRpcActionError>
	readonly daemonRestart: (
		input: DaemonRestartRequest,
	) => Effect.Effect<DaemonControlStatusResult, RpcClientError | DaemonRpcActionError>
	readonly daemonAttach: (
		input: DaemonAttachRequest,
	) => Effect.Effect<DaemonAttachReconnectResult, RpcClientError | DaemonRpcActionError>
	readonly daemonReconnect: (
		input: DaemonReconnectRequest,
	) => Effect.Effect<DaemonAttachReconnectResult, RpcClientError | DaemonRpcActionError>
	readonly daemonHeartbeat: (
		input: DaemonHeartbeatRequest,
	) => Effect.Effect<DaemonHeartbeatResult, RpcClientError | DaemonRpcActionError>
	readonly daemonEventStream: (
		input: DaemonEventStreamRequest,
	) => Effect.Effect<DaemonEventStreamResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionSnapshot: (
		input: DaemonSessionSnapshotRequest,
	) => Effect.Effect<DaemonSessionSnapshotResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionStart: (
		input: DaemonSessionStartRequest,
	) => Effect.Effect<DaemonSessionMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionStop: (
		input: DaemonSessionStopRequest,
	) => Effect.Effect<DaemonSessionMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionPause: (
		input: DaemonSessionPauseRequest,
	) => Effect.Effect<DaemonSessionMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionResume: (
		input: DaemonSessionResumeRequest,
	) => Effect.Effect<DaemonSessionMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionRecover: (
		input: DaemonSessionRecoverRequest,
	) => Effect.Effect<DaemonSessionMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonSessionUpdateState: (
		input: DaemonSessionUpdateStateRequest,
	) => Effect.Effect<DaemonSessionMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonDevServerStatus: (
		input: DaemonDevServerStatusRequest,
	) => Effect.Effect<DaemonDevServerStatusResult, RpcClientError | DaemonRpcActionError>
	readonly daemonDevServerList: (
		input: DaemonDevServerListRequest,
	) => Effect.Effect<DaemonDevServerListResult, RpcClientError | DaemonRpcActionError>
	readonly daemonDevServerStart: (
		input: DaemonDevServerStartRequest,
	) => Effect.Effect<DaemonDevServerMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonDevServerStop: (
		input: DaemonDevServerStopRequest,
	) => Effect.Effect<DaemonDevServerMutationResult, RpcClientError | DaemonRpcActionError>
	readonly daemonQueueEnqueue: (
		input: DaemonQueueEnqueueRequest,
	) => Effect.Effect<DaemonQueueEnqueueResult, RpcClientError | DaemonRpcActionError>
	readonly daemonQueueQuery: (
		input: DaemonQueueQueryRequest,
	) => Effect.Effect<DaemonQueueQueryResult, RpcClientError | DaemonRpcActionError>
	readonly daemonQueueCancel: (
		input: DaemonQueueCancelRequest,
	) => Effect.Effect<DaemonQueueCancelResult, RpcClientError | DaemonRpcActionError>
}

const isRecord = (value: unknown): value is Readonly<Record<string, unknown>> =>
	typeof value === "object" && value !== null

const hasTaggedActionError = (error: unknown): error is DaemonRpcActionError =>
	isRecord(error) &&
	error["_tag"] === "DaemonRpcActionError" &&
	typeof error["code"] === "string" &&
	typeof error["message"] === "string" &&
	(error["action"] === undefined || typeof error["action"] === "string")

const mapWireError = (operation: DaemonRpcOperation, error: unknown): DaemonRpcClientError => {
	if (error instanceof DaemonRpcProtocolVersionMismatchError) {
		return error
	}
	if (error instanceof RpcClientError) {
		return new DaemonRpcTransportError({
			operation,
			reason: "transport",
			message: error.message,
			suggestion:
				"Verify daemon socket availability, then run `az daemon health` and `az daemon logs`.",
		})
	}
	if (hasTaggedActionError(error)) {
		return new DaemonRpcRemoteActionError({
			operation,
			code: error.code,
			message: error.message,
			action: error.action,
		})
	}
	return new DaemonRpcTransportError({
		operation,
		reason: "unknown",
		message: error instanceof Error ? error.message : String(error),
		suggestion: "Retry the command and inspect daemon diagnostics with `az daemon status`.",
	})
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
	const wire: DaemonRpcWireClient = {
		daemonStatus: (input) => raw.daemonStatus(input),
		daemonHealth: (input) => raw.daemonHealth(input),
		daemonLogs: (input) => raw.daemonLogs(input),
		daemonStop: (input) => raw.daemonStop(input),
		daemonRestart: (input) => raw.daemonRestart(input),
		daemonAttach: (input) => raw.daemonAttach(input),
		daemonReconnect: (input) => raw.daemonReconnect(input),
		daemonHeartbeat: (input) => raw.daemonHeartbeat(input),
		daemonEventStream: (input) => raw.daemonEventStream(input),
		daemonSessionSnapshot: (input) => raw.daemonSessionSnapshot(input),
		daemonSessionStart: (input) => raw.daemonSessionStart(input),
		daemonSessionStop: (input) => raw.daemonSessionStop(input),
		daemonSessionPause: (input) => raw.daemonSessionPause(input),
		daemonSessionResume: (input) => raw.daemonSessionResume(input),
		daemonSessionRecover: (input) => raw.daemonSessionRecover(input),
		daemonSessionUpdateState: (input) => raw.daemonSessionUpdateState(input),
		daemonDevServerStatus: (input) => raw.daemonDevServerStatus(input),
		daemonDevServerList: (input) => raw.daemonDevServerList(input),
		daemonDevServerStart: (input) => raw.daemonDevServerStart(input),
		daemonDevServerStop: (input) => raw.daemonDevServerStop(input),
		daemonQueueEnqueue: (input) => raw.daemonQueueEnqueue(input),
		daemonQueueQuery: (input) => raw.daemonQueueQuery(input),
		daemonQueueCancel: (input) => raw.daemonQueueCancel(input),
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
