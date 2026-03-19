import * as Rpc from "@effect/rpc/Rpc"
import * as RpcGroup from "@effect/rpc/RpcGroup"
import {
	DaemonImplementationCreateRequestSchema,
	DaemonImplementationCreateResultSchema,
	DaemonImplementationDeleteRequestSchema,
	DaemonImplementationDeleteResultSchema,
	DaemonImplementationGetRegistryRequestSchema,
	DaemonImplementationGetRegistryResultSchema,
	DaemonImplementationSetDefaultRequestSchema,
	DaemonImplementationSetDefaultResultSchema,
	DaemonImplementationUpdateRequestSchema,
	DaemonImplementationUpdateResultSchema,
} from "./DaemonImplementationRpcSchemas.js"
import {
	DaemonIssueAddDependencyRequestSchema,
	DaemonIssueCloseRequestSchema,
	DaemonIssueCloseResultSchema,
	DaemonIssueCreateRequestSchema,
	DaemonIssueCreateResultSchema,
	DaemonIssueDeleteRequestSchema,
	DaemonIssueDeleteResultSchema,
	DaemonIssueDependencyMutationResultSchema,
	DaemonIssueGetRequestSchema,
	DaemonIssueGetResultSchema,
	DaemonIssueListRequestSchema,
	DaemonIssueListResultSchema,
	DaemonIssueRemoveDependencyRequestSchema,
	DaemonIssueSyncRequestSchema,
	DaemonIssueSyncResultEnvelopeSchema,
	DaemonIssueUpdateRequestSchema,
	DaemonIssueUpdateResultSchema,
} from "./DaemonIssueRpcSchemas.js"
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
import {
	DaemonSpecIssueLinksRequestSchema,
	DaemonSpecIssueLinksResultSchema,
	DaemonSpecLintRequestSchema,
	DaemonSpecLintResultSchema,
	DaemonSpecParityRequestSchema,
	DaemonSpecParityResultSchema,
	DaemonSpecReadRequestSchema,
	DaemonSpecReadResultSchema,
	DaemonSpecRequirementGetRequestSchema,
	DaemonSpecRequirementGetResultSchema,
	DaemonSpecRequirementListRequestSchema,
	DaemonSpecRequirementListResultSchema,
} from "./DaemonSpecRpcSchemas.js"

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

export const DaemonIssueGetRpc = Rpc.make("daemonIssueGet", {
	payload: DaemonIssueGetRequestSchema,
	success: DaemonIssueGetResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueListRpc = Rpc.make("daemonIssueList", {
	payload: DaemonIssueListRequestSchema,
	success: DaemonIssueListResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueCreateRpc = Rpc.make("daemonIssueCreate", {
	payload: DaemonIssueCreateRequestSchema,
	success: DaemonIssueCreateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueUpdateRpc = Rpc.make("daemonIssueUpdate", {
	payload: DaemonIssueUpdateRequestSchema,
	success: DaemonIssueUpdateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueAddDependencyRpc = Rpc.make("daemonIssueAddDependency", {
	payload: DaemonIssueAddDependencyRequestSchema,
	success: DaemonIssueDependencyMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueRemoveDependencyRpc = Rpc.make("daemonIssueRemoveDependency", {
	payload: DaemonIssueRemoveDependencyRequestSchema,
	success: DaemonIssueDependencyMutationResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueCloseRpc = Rpc.make("daemonIssueClose", {
	payload: DaemonIssueCloseRequestSchema,
	success: DaemonIssueCloseResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueDeleteRpc = Rpc.make("daemonIssueDelete", {
	payload: DaemonIssueDeleteRequestSchema,
	success: DaemonIssueDeleteResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonIssueSyncRpc = Rpc.make("daemonIssueSync", {
	payload: DaemonIssueSyncRequestSchema,
	success: DaemonIssueSyncResultEnvelopeSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonImplementationGetRegistryRpc = Rpc.make("daemonImplementationGetRegistry", {
	payload: DaemonImplementationGetRegistryRequestSchema,
	success: DaemonImplementationGetRegistryResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonImplementationCreateRpc = Rpc.make("daemonImplementationCreate", {
	payload: DaemonImplementationCreateRequestSchema,
	success: DaemonImplementationCreateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonImplementationUpdateRpc = Rpc.make("daemonImplementationUpdate", {
	payload: DaemonImplementationUpdateRequestSchema,
	success: DaemonImplementationUpdateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonImplementationDeleteRpc = Rpc.make("daemonImplementationDelete", {
	payload: DaemonImplementationDeleteRequestSchema,
	success: DaemonImplementationDeleteResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonImplementationSetDefaultRpc = Rpc.make("daemonImplementationSetDefault", {
	payload: DaemonImplementationSetDefaultRequestSchema,
	success: DaemonImplementationSetDefaultResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecRequirementListRpc = Rpc.make("daemonSpecRequirementList", {
	payload: DaemonSpecRequirementListRequestSchema,
	success: DaemonSpecRequirementListResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecRequirementGetRpc = Rpc.make("daemonSpecRequirementGet", {
	payload: DaemonSpecRequirementGetRequestSchema,
	success: DaemonSpecRequirementGetResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecReadRpc = Rpc.make("daemonSpecRead", {
	payload: DaemonSpecReadRequestSchema,
	success: DaemonSpecReadResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecLintRpc = Rpc.make("daemonSpecLint", {
	payload: DaemonSpecLintRequestSchema,
	success: DaemonSpecLintResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecParityRpc = Rpc.make("daemonSpecParity", {
	payload: DaemonSpecParityRequestSchema,
	success: DaemonSpecParityResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecIssueLinksRpc = Rpc.make("daemonSpecIssueLinks", {
	payload: DaemonSpecIssueLinksRequestSchema,
	success: DaemonSpecIssueLinksResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonEventStreamRpc = Rpc.make("daemonEventStream", {
	payload: DaemonEventStreamRequestSchema,
	success: DaemonEventStreamResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonControlRpcGroup = RpcGroup.make(
	DaemonStatusRpc,
	DaemonHealthRpc,
	DaemonLogsRpc,
	DaemonStopRpc,
	DaemonRestartRpc,
)

export const DaemonClientRpcGroup = RpcGroup.make(
	DaemonAttachRpc,
	DaemonReconnectRpc,
	DaemonHeartbeatRpc,
)

export const DaemonSessionRpcGroup = RpcGroup.make(
	DaemonSessionSnapshotRpc,
	DaemonBoardReadModelRpc,
	DaemonSessionStartRpc,
	DaemonSessionStopRpc,
	DaemonSessionPauseRpc,
	DaemonSessionResumeRpc,
	DaemonSessionRecoverRpc,
	DaemonSessionUpdateStateRpc,
	DaemonEventStreamRpc,
)

export const DaemonDevServerRpcGroup = RpcGroup.make(
	DaemonDevServerStatusRpc,
	DaemonDevServerListRpc,
	DaemonDevServerStartRpc,
	DaemonDevServerStopRpc,
)

export const DaemonQueueRpcGroup = RpcGroup.make(
	DaemonQueueEnqueueRpc,
	DaemonQueueQueryRpc,
	DaemonQueueCancelRpc,
)

export const DaemonIssueRpcGroup = RpcGroup.make(
	DaemonIssueGetRpc,
	DaemonIssueListRpc,
	DaemonIssueCreateRpc,
	DaemonIssueUpdateRpc,
	DaemonIssueAddDependencyRpc,
	DaemonIssueRemoveDependencyRpc,
	DaemonIssueCloseRpc,
	DaemonIssueDeleteRpc,
	DaemonIssueSyncRpc,
)

export const DaemonImplementationRpcGroup = RpcGroup.make(
	DaemonImplementationGetRegistryRpc,
	DaemonImplementationCreateRpc,
	DaemonImplementationUpdateRpc,
	DaemonImplementationDeleteRpc,
	DaemonImplementationSetDefaultRpc,
)

export const DaemonSpecRequirementRpcGroup = RpcGroup.make(
	DaemonSpecRequirementListRpc,
	DaemonSpecRequirementGetRpc,
)

export const DaemonSpecReadRpcGroup = RpcGroup.make(
	DaemonSpecReadRpc,
	DaemonSpecLintRpc,
	DaemonSpecParityRpc,
	DaemonSpecIssueLinksRpc,
)

export const DaemonSpecRpcGroup = DaemonSpecRequirementRpcGroup.merge(DaemonSpecReadRpcGroup)

export const DaemonRpcGroup = DaemonControlRpcGroup.merge(
	DaemonClientRpcGroup,
	DaemonSessionRpcGroup,
	DaemonDevServerRpcGroup,
	DaemonQueueRpcGroup,
	DaemonIssueRpcGroup,
)

export type DaemonRpcContract = RpcGroup.Rpcs<typeof DaemonRpcGroup>
