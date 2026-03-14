import * as Rpc from "@effect/rpc/Rpc"
import * as RpcGroup from "@effect/rpc/RpcGroup"
import {
	DaemonAttachReconnectResultSchema,
	DaemonAttachRequestSchema,
	DaemonControlStatusResultSchema,
	DaemonHealthRequestSchema,
	DaemonHealthResultSchema,
	DaemonHeartbeatRequestSchema,
	DaemonHeartbeatResultSchema,
	DaemonLogsRequestSchema,
	DaemonLogsResultSchema,
	DaemonReconnectRequestSchema,
	DaemonRestartRequestSchema,
	DaemonRpcActionErrorSchema,
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

export const DaemonRpcGroup = RpcGroup.make(
	DaemonStatusRpc,
	DaemonHealthRpc,
	DaemonLogsRpc,
	DaemonStopRpc,
	DaemonRestartRpc,
	DaemonAttachRpc,
	DaemonReconnectRpc,
	DaemonHeartbeatRpc,
)

export type DaemonRpcContract = RpcGroup.Rpcs<typeof DaemonRpcGroup>
