import * as Rpc from "@effect/rpc/Rpc"
import * as RpcGroup from "@effect/rpc/RpcGroup"
import {
	DaemonAttachReconnectResultSchema,
	DaemonAttachRequestSchema,
	DaemonBoardReadModelRequestSchema,
	DaemonBoardReadModelResultSchema,
	DaemonControlStatusResultSchema,
	DaemonDevServerListRequestSchema,
	DaemonDevServerListResultSchema,
	DaemonDevServerMutationResultSchema,
	DaemonDevServerStartRequestSchema,
	DaemonDevServerStatusRequestSchema,
	DaemonDevServerStatusResultSchema,
	DaemonDevServerStopRequestSchema,
	DaemonEventStreamRequestSchema,
	DaemonEventStreamResultSchema,
	DaemonHealthRequestSchema,
	DaemonHealthResultSchema,
	DaemonHeartbeatRequestSchema,
	DaemonHeartbeatResultSchema,
	DaemonLogsRequestSchema,
	DaemonLogsResultSchema,
	DaemonQueueCancelRequestSchema,
	DaemonQueueCancelResultSchema,
	DaemonQueueEnqueueRequestSchema,
	DaemonQueueEnqueueResultSchema,
	DaemonQueueQueryRequestSchema,
	DaemonQueueQueryResultSchema,
	DaemonReconnectRequestSchema,
	DaemonRestartRequestSchema,
	DaemonRpcActionErrorSchema,
	DaemonSessionMutationResultSchema,
	DaemonSessionPauseRequestSchema,
	DaemonSessionRecoverRequestSchema,
	DaemonSessionResumeRequestSchema,
	DaemonSessionSnapshotRequestSchema,
	DaemonSessionSnapshotResultSchema,
	DaemonSessionStartRequestSchema,
	DaemonSessionStopRequestSchema,
	DaemonSessionUpdateStateRequestSchema,
	DaemonStatusRequestSchema,
	DaemonStopRequestSchema,
} from "./DaemonRpcSchemas.js"

export const DaemonStatusRpc = Rpc.make("daemonStatus", {
	payload: DaemonStatusRequestSchema,
	success: DaemonControlStatusResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonHealthRpc = Rpc.make("daemonHealth", {
	payload: DaemonHealthRequestSchema,
	success: DaemonHealthResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonLogsRpc = Rpc.make("daemonLogs", {
	payload: DaemonLogsRequestSchema,
	success: DaemonLogsResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonStopRpc = Rpc.make("daemonStop", {
	payload: DaemonStopRequestSchema,
	success: DaemonControlStatusResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonRestartRpc = Rpc.make("daemonRestart", {
	payload: DaemonRestartRequestSchema,
	success: DaemonControlStatusResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonAttachRpc = Rpc.make("daemonAttach", {
	payload: DaemonAttachRequestSchema,
	success: DaemonAttachReconnectResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonReconnectRpc = Rpc.make("daemonReconnect", {
	payload: DaemonReconnectRequestSchema,
	success: DaemonAttachReconnectResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonHeartbeatRpc = Rpc.make("daemonHeartbeat", {
	payload: DaemonHeartbeatRequestSchema,
	success: DaemonHeartbeatResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionSnapshotRpc = Rpc.make("daemonSessionSnapshot", {
	payload: DaemonSessionSnapshotRequestSchema,
	success: DaemonSessionSnapshotResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonBoardReadModelRpc = Rpc.make("daemonBoardReadModel", {
	payload: DaemonBoardReadModelRequestSchema,
	success: DaemonBoardReadModelResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionStartRpc = Rpc.make("daemonSessionStart", {
	payload: DaemonSessionStartRequestSchema,
	success: DaemonSessionMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionStopRpc = Rpc.make("daemonSessionStop", {
	payload: DaemonSessionStopRequestSchema,
	success: DaemonSessionMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionPauseRpc = Rpc.make("daemonSessionPause", {
	payload: DaemonSessionPauseRequestSchema,
	success: DaemonSessionMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionResumeRpc = Rpc.make("daemonSessionResume", {
	payload: DaemonSessionResumeRequestSchema,
	success: DaemonSessionMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionRecoverRpc = Rpc.make("daemonSessionRecover", {
	payload: DaemonSessionRecoverRequestSchema,
	success: DaemonSessionMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSessionUpdateStateRpc = Rpc.make("daemonSessionUpdateState", {
	payload: DaemonSessionUpdateStateRequestSchema,
	success: DaemonSessionMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonDevServerStatusRpc = Rpc.make("daemonDevServerStatus", {
	payload: DaemonDevServerStatusRequestSchema,
	success: DaemonDevServerStatusResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonDevServerListRpc = Rpc.make("daemonDevServerList", {
	payload: DaemonDevServerListRequestSchema,
	success: DaemonDevServerListResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonDevServerStartRpc = Rpc.make("daemonDevServerStart", {
	payload: DaemonDevServerStartRequestSchema,
	success: DaemonDevServerMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonDevServerStopRpc = Rpc.make("daemonDevServerStop", {
	payload: DaemonDevServerStopRequestSchema,
	success: DaemonDevServerMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonQueueEnqueueRpc = Rpc.make("daemonQueueEnqueue", {
	payload: DaemonQueueEnqueueRequestSchema,
	success: DaemonQueueEnqueueResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonQueueQueryRpc = Rpc.make("daemonQueueQuery", {
	payload: DaemonQueueQueryRequestSchema,
	success: DaemonQueueQueryResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonQueueCancelRpc = Rpc.make("daemonQueueCancel", {
	payload: DaemonQueueCancelRequestSchema,
	success: DaemonQueueCancelResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonEventStreamRpc = Rpc.make("daemonEventStream", {
	payload: DaemonEventStreamRequestSchema,
	success: DaemonEventStreamResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonRpcGroup = RpcGroup.make(
	DaemonStatusRpc,
	DaemonHealthRpc,
	DaemonLogsRpc,
	DaemonStopRpc,
	DaemonRestartRpc,
	DaemonAttachRpc,
	DaemonReconnectRpc,
	DaemonHeartbeatRpc,
	DaemonSessionSnapshotRpc,
	DaemonBoardReadModelRpc,
	DaemonSessionStartRpc,
	DaemonSessionStopRpc,
	DaemonSessionPauseRpc,
	DaemonSessionResumeRpc,
	DaemonSessionRecoverRpc,
	DaemonSessionUpdateStateRpc,
	DaemonDevServerStatusRpc,
	DaemonDevServerListRpc,
	DaemonDevServerStartRpc,
	DaemonDevServerStopRpc,
	DaemonQueueEnqueueRpc,
	DaemonQueueQueryRpc,
	DaemonQueueCancelRpc,
	DaemonEventStreamRpc,
)

export type DaemonRpcContract = RpcGroup.Rpcs<typeof DaemonRpcGroup>
