import * as Rpc from "@effect/rpc/Rpc"
import * as RpcGroup from "@effect/rpc/RpcGroup"
import {
	DaemonAttachmentAttachClipboardRequestSchema,
	DaemonAttachmentAttachFileRequestSchema,
	DaemonAttachmentAttachResultSchema,
	DaemonAttachmentCountBatchRequestSchema,
	DaemonAttachmentCountBatchResultSchema,
	DaemonAttachmentListRequestSchema,
	DaemonAttachmentListResultSchema,
	DaemonAttachmentMaterializePathRequestSchema,
	DaemonAttachmentMaterializePathResultSchema,
	DaemonAttachmentRemoveRequestSchema,
	DaemonAttachmentRemoveResultSchema,
} from "./DaemonAttachmentRpcSchemas.js"
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
	DaemonPlanningCreateIssuesRequestSchema,
	DaemonPlanningCreateIssuesResultSchema,
	DaemonPlanningGenerateRequestSchema,
	DaemonPlanningGenerateResultSchema,
	DaemonPlanningRefineRequestSchema,
	DaemonPlanningRefineResultSchema,
	DaemonPlanningReviewRequestSchema,
	DaemonPlanningReviewResultSchema,
} from "./DaemonPlanningRpcSchemas.js"
import {
	DaemonPrAbortMergeRequestSchema,
	DaemonPrAbortMergeResultSchema,
	DaemonPrCheckBranchBehindBaseRequestSchema,
	DaemonPrCheckBranchBehindBaseResultSchema,
	DaemonPrCheckGhCliRequestSchema,
	DaemonPrCheckGhCliResultSchema,
	DaemonPrCheckMergeConflictsRequestSchema,
	DaemonPrCheckMergeConflictsResultSchema,
	DaemonPrCheckUncommittedChangesRequestSchema,
	DaemonPrCheckUncommittedChangesResultSchema,
	DaemonPrCleanupRequestSchema,
	DaemonPrCleanupResultSchema,
	DaemonPrCreateRequestSchema,
	DaemonPrCreateResultSchema,
	DaemonPrGetEffectiveBaseBranchRequestSchema,
	DaemonPrGetEffectiveBaseBranchResultSchema,
	DaemonPrGetTargetBranchRequestSchema,
	DaemonPrGetTargetBranchResultSchema,
	DaemonPrMergeBaseIntoBranchRequestSchema,
	DaemonPrMergeBaseIntoBranchResultSchema,
	DaemonPrMergeIssueIntoIssueRequestSchema,
	DaemonPrMergeIssueIntoIssueResultSchema,
	DaemonPrMergeToMainRequestSchema,
	DaemonPrMergeToMainResultSchema,
	DaemonPrUpdateFromBaseRequestSchema,
	DaemonPrUpdateFromBaseResultSchema,
} from "./DaemonPrRpcSchemas.js"
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
	DaemonSpecLinkAddRequestSchema,
	DaemonSpecLinkAddResultSchema,
	DaemonSpecLinkListRequestSchema,
	DaemonSpecLinkListResultSchema,
	DaemonSpecLinkRemoveRequestSchema,
	DaemonSpecLinkRemoveResultSchema,
	DaemonSpecLinkUpdateRequestSchema,
	DaemonSpecLinkUpdateResultSchema,
	DaemonSpecLintRequestSchema,
	DaemonSpecLintResultSchema,
	DaemonSpecParityRequestSchema,
	DaemonSpecParityResultSchema,
	DaemonSpecPublishConfigGetRequestSchema,
	DaemonSpecPublishConfigGetResultSchema,
	DaemonSpecPublishConfigSetRequestSchema,
	DaemonSpecPublishConfigSetResultSchema,
	DaemonSpecPublishOutcomeGetRequestSchema,
	DaemonSpecPublishOutcomeGetResultSchema,
	DaemonSpecPublishRequestSchema,
	DaemonSpecPublishResultSchema,
	DaemonSpecReadRequestSchema,
	DaemonSpecReadResultSchema,
	DaemonSpecRequirementCreateRequestSchema,
	DaemonSpecRequirementCreateResultSchema,
	DaemonSpecRequirementDeleteRequestSchema,
	DaemonSpecRequirementDeleteResultSchema,
	DaemonSpecRequirementGetRequestSchema,
	DaemonSpecRequirementGetResultSchema,
	DaemonSpecRequirementIssuesRequestSchema,
	DaemonSpecRequirementIssuesResultSchema,
	DaemonSpecRequirementListRequestSchema,
	DaemonSpecRequirementListResultSchema,
	DaemonSpecRequirementUpdateRequestSchema,
	DaemonSpecRequirementUpdateResultSchema,
	DaemonSpecSyncMarkdownRequestSchema,
	DaemonSpecSyncMarkdownResultSchema,
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

export const DaemonAttachmentListRpc = Rpc.make("daemonAttachmentList", {
	payload: DaemonAttachmentListRequestSchema,
	success: DaemonAttachmentListResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonAttachmentCountBatchRpc = Rpc.make("daemonAttachmentCountBatch", {
	payload: DaemonAttachmentCountBatchRequestSchema,
	success: DaemonAttachmentCountBatchResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonAttachmentAttachFileRpc = Rpc.make("daemonAttachmentAttachFile", {
	payload: DaemonAttachmentAttachFileRequestSchema,
	success: DaemonAttachmentAttachResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonAttachmentAttachClipboardRpc = Rpc.make("daemonAttachmentAttachClipboard", {
	payload: DaemonAttachmentAttachClipboardRequestSchema,
	success: DaemonAttachmentAttachResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonAttachmentRemoveRpc = Rpc.make("daemonAttachmentRemove", {
	payload: DaemonAttachmentRemoveRequestSchema,
	success: DaemonAttachmentRemoveResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonAttachmentMaterializePathRpc = Rpc.make("daemonAttachmentMaterializePath", {
	payload: DaemonAttachmentMaterializePathRequestSchema,
	success: DaemonAttachmentMaterializePathResultSchema,
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

export const DaemonSpecRequirementCreateRpc = Rpc.make("daemonSpecRequirementCreate", {
	payload: DaemonSpecRequirementCreateRequestSchema,
	success: DaemonSpecRequirementCreateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecRequirementUpdateRpc = Rpc.make("daemonSpecRequirementUpdate", {
	payload: DaemonSpecRequirementUpdateRequestSchema,
	success: DaemonSpecRequirementUpdateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecRequirementDeleteRpc = Rpc.make("daemonSpecRequirementDelete", {
	payload: DaemonSpecRequirementDeleteRequestSchema,
	success: DaemonSpecRequirementDeleteResultSchema,
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

export const DaemonSpecLinkListRpc = Rpc.make("daemonSpecLinkList", {
	payload: DaemonSpecLinkListRequestSchema,
	success: DaemonSpecLinkListResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecLinkAddRpc = Rpc.make("daemonSpecLinkAdd", {
	payload: DaemonSpecLinkAddRequestSchema,
	success: DaemonSpecLinkAddResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecLinkRemoveRpc = Rpc.make("daemonSpecLinkRemove", {
	payload: DaemonSpecLinkRemoveRequestSchema,
	success: DaemonSpecLinkRemoveResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecLinkUpdateRpc = Rpc.make("daemonSpecLinkUpdate", {
	payload: DaemonSpecLinkUpdateRequestSchema,
	success: DaemonSpecLinkUpdateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecIssueLinksRpc = Rpc.make("daemonSpecIssueLinks", {
	payload: DaemonSpecIssueLinksRequestSchema,
	success: DaemonSpecIssueLinksResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecRequirementIssuesRpc = Rpc.make("daemonSpecRequirementIssues", {
	payload: DaemonSpecRequirementIssuesRequestSchema,
	success: DaemonSpecRequirementIssuesResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecPublishConfigGetRpc = Rpc.make("daemonSpecPublishConfigGet", {
	payload: DaemonSpecPublishConfigGetRequestSchema,
	success: DaemonSpecPublishConfigGetResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecPublishConfigSetRpc = Rpc.make("daemonSpecPublishConfigSet", {
	payload: DaemonSpecPublishConfigSetRequestSchema,
	success: DaemonSpecPublishConfigSetResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecPublishOutcomeGetRpc = Rpc.make("daemonSpecPublishOutcomeGet", {
	payload: DaemonSpecPublishOutcomeGetRequestSchema,
	success: DaemonSpecPublishOutcomeGetResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecSyncMarkdownRpc = Rpc.make("daemonSpecSyncMarkdown", {
	payload: DaemonSpecSyncMarkdownRequestSchema,
	success: DaemonSpecSyncMarkdownResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonSpecPublishRpc = Rpc.make("daemonSpecPublish", {
	payload: DaemonSpecPublishRequestSchema,
	success: DaemonSpecPublishResultSchema,
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

export const DaemonAttachmentRpcGroup = RpcGroup.make(
	DaemonAttachmentListRpc,
	DaemonAttachmentCountBatchRpc,
	DaemonAttachmentAttachFileRpc,
	DaemonAttachmentAttachClipboardRpc,
	DaemonAttachmentRemoveRpc,
	DaemonAttachmentMaterializePathRpc,
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

export const DaemonPlanningGenerateRpc = Rpc.make("daemonPlanningGenerate", {
	payload: DaemonPlanningGenerateRequestSchema,
	success: DaemonPlanningGenerateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPlanningReviewRpc = Rpc.make("daemonPlanningReview", {
	payload: DaemonPlanningReviewRequestSchema,
	success: DaemonPlanningReviewResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPlanningRefineRpc = Rpc.make("daemonPlanningRefine", {
	payload: DaemonPlanningRefineRequestSchema,
	success: DaemonPlanningRefineResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPlanningCreateIssuesRpc = Rpc.make("daemonPlanningCreateIssues", {
	payload: DaemonPlanningCreateIssuesRequestSchema,
	success: DaemonPlanningCreateIssuesResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPlanningRpcGroup = RpcGroup.make(
	DaemonPlanningGenerateRpc,
	DaemonPlanningReviewRpc,
	DaemonPlanningRefineRpc,
	DaemonPlanningCreateIssuesRpc,
)

export const DaemonPrCreateRpc = Rpc.make("daemonPrCreate", {
	payload: DaemonPrCreateRequestSchema,
	success: DaemonPrCreateResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrCleanupRpc = Rpc.make("daemonPrCleanup", {
	payload: DaemonPrCleanupRequestSchema,
	success: DaemonPrCleanupResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrMergeToMainRpc = Rpc.make("daemonPrMergeToMain", {
	payload: DaemonPrMergeToMainRequestSchema,
	success: DaemonPrMergeToMainResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrCheckGhCliRpc = Rpc.make("daemonPrCheckGhCli", {
	payload: DaemonPrCheckGhCliRequestSchema,
	success: DaemonPrCheckGhCliResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrUpdateFromBaseRpc = Rpc.make("daemonPrUpdateFromBase", {
	payload: DaemonPrUpdateFromBaseRequestSchema,
	success: DaemonPrUpdateFromBaseResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrMergeBaseIntoBranchRpc = Rpc.make("daemonPrMergeBaseIntoBranch", {
	payload: DaemonPrMergeBaseIntoBranchRequestSchema,
	success: DaemonPrMergeBaseIntoBranchResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrAbortMergeRpc = Rpc.make("daemonPrAbortMerge", {
	payload: DaemonPrAbortMergeRequestSchema,
	success: DaemonPrAbortMergeResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrCheckMergeConflictsRpc = Rpc.make("daemonPrCheckMergeConflicts", {
	payload: DaemonPrCheckMergeConflictsRequestSchema,
	success: DaemonPrCheckMergeConflictsResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrCheckUncommittedChangesRpc = Rpc.make("daemonPrCheckUncommittedChanges", {
	payload: DaemonPrCheckUncommittedChangesRequestSchema,
	success: DaemonPrCheckUncommittedChangesResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrCheckBranchBehindBaseRpc = Rpc.make("daemonPrCheckBranchBehindBase", {
	payload: DaemonPrCheckBranchBehindBaseRequestSchema,
	success: DaemonPrCheckBranchBehindBaseResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrGetEffectiveBaseBranchRpc = Rpc.make("daemonPrGetEffectiveBaseBranch", {
	payload: DaemonPrGetEffectiveBaseBranchRequestSchema,
	success: DaemonPrGetEffectiveBaseBranchResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrMergeIssueIntoIssueRpc = Rpc.make("daemonPrMergeIssueIntoIssue", {
	payload: DaemonPrMergeIssueIntoIssueRequestSchema,
	success: DaemonPrMergeIssueIntoIssueResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrGetTargetBranchRpc = Rpc.make("daemonPrGetTargetBranch", {
	payload: DaemonPrGetTargetBranchRequestSchema,
	success: DaemonPrGetTargetBranchResultSchema,
	error: DaemonRpcActionErrorSchema,
})

export const DaemonPrRpcGroup = RpcGroup.make(
	DaemonPrCreateRpc,
	DaemonPrCleanupRpc,
	DaemonPrMergeToMainRpc,
	DaemonPrCheckGhCliRpc,
	DaemonPrUpdateFromBaseRpc,
	DaemonPrMergeBaseIntoBranchRpc,
	DaemonPrAbortMergeRpc,
	DaemonPrCheckMergeConflictsRpc,
	DaemonPrCheckUncommittedChangesRpc,
	DaemonPrCheckBranchBehindBaseRpc,
	DaemonPrGetEffectiveBaseBranchRpc,
	DaemonPrMergeIssueIntoIssueRpc,
	DaemonPrGetTargetBranchRpc,
)

export const DaemonSpecRequirementRpcGroup = RpcGroup.make(
	DaemonSpecRequirementListRpc,
	DaemonSpecRequirementGetRpc,
)

export const DaemonSpecRequirementMutationRpcGroup = RpcGroup.make(
	DaemonSpecRequirementCreateRpc,
	DaemonSpecRequirementUpdateRpc,
	DaemonSpecRequirementDeleteRpc,
)

export const DaemonSpecReadRpcGroup = RpcGroup.make(
	DaemonSpecReadRpc,
	DaemonSpecLintRpc,
	DaemonSpecParityRpc,
	DaemonSpecIssueLinksRpc,
	DaemonSpecRequirementIssuesRpc,
)

export const DaemonSpecLinksRpcGroup = RpcGroup.make(
	DaemonSpecLinkListRpc,
	DaemonSpecLinkAddRpc,
	DaemonSpecLinkRemoveRpc,
	DaemonSpecLinkUpdateRpc,
)

export const DaemonSpecPublishConfigRpcGroup = RpcGroup.make(
	DaemonSpecPublishConfigGetRpc,
	DaemonSpecPublishConfigSetRpc,
	DaemonSpecPublishOutcomeGetRpc,
)

export const DaemonSpecSyncRpcGroup = RpcGroup.make(DaemonSpecSyncMarkdownRpc, DaemonSpecPublishRpc)

export const DaemonSpecRpcGroup = DaemonSpecRequirementRpcGroup.merge(DaemonSpecReadRpcGroup)
	.merge(DaemonSpecRequirementMutationRpcGroup)
	.merge(DaemonSpecLinksRpcGroup)
	.merge(DaemonSpecPublishConfigRpcGroup)
	.merge(DaemonSpecSyncRpcGroup)

export const DaemonRpcGroup = DaemonControlRpcGroup.merge(
	DaemonClientRpcGroup,
	DaemonSessionRpcGroup,
	DaemonDevServerRpcGroup,
	DaemonQueueRpcGroup,
	DaemonAttachmentRpcGroup,
	DaemonIssueRpcGroup,
)

export const DaemonAppRpcGroup = DaemonRpcGroup.merge(
	DaemonImplementationRpcGroup,
	DaemonPlanningRpcGroup,
	DaemonPrRpcGroup,
	DaemonSpecReadRpcGroup,
)

export const DaemonGlobalRpcGroup = DaemonRpcGroup.merge(
	DaemonImplementationRpcGroup,
	DaemonPlanningRpcGroup,
	DaemonPrRpcGroup,
	DaemonSpecRpcGroup,
)

export type DaemonRpcContract = RpcGroup.Rpcs<typeof DaemonGlobalRpcGroup>
