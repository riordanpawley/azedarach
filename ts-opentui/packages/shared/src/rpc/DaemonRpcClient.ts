import * as BunSocket from "@effect/platform-bun/BunSocket"
import * as RpcClient from "@effect/rpc/RpcClient"
import type { RpcClientError } from "@effect/rpc/RpcClientError"
import * as RpcSerialization from "@effect/rpc/RpcSerialization"
import { Context, Effect, Layer } from "effect"
import type {
	DaemonAttachmentAttachClipboardRequest,
	DaemonAttachmentAttachFileRequest,
	DaemonAttachmentAttachResult,
	DaemonAttachmentCountBatchRequest,
	DaemonAttachmentCountBatchResult,
	DaemonAttachmentListRequest,
	DaemonAttachmentListResult,
	DaemonAttachmentMaterializePathRequest,
	DaemonAttachmentMaterializePathResult,
	DaemonAttachmentRemoveRequest,
	DaemonAttachmentRemoveResult,
} from "./DaemonAttachmentRpcSchemas.js"
import type {
	DaemonImplementationCreateRequest,
	DaemonImplementationCreateResult,
	DaemonImplementationDeleteRequest,
	DaemonImplementationDeleteResult,
	DaemonImplementationGetRegistryRequest,
	DaemonImplementationGetRegistryResult,
	DaemonImplementationSetDefaultRequest,
	DaemonImplementationSetDefaultResult,
	DaemonImplementationUpdateRequest,
	DaemonImplementationUpdateResult,
} from "./DaemonImplementationRpcSchemas.js"
import type {
	DaemonIssueAddDependencyRequest,
	DaemonIssueCloseRequest,
	DaemonIssueCloseResult,
	DaemonIssueCreateRequest,
	DaemonIssueCreateResult,
	DaemonIssueDeleteRequest,
	DaemonIssueDeleteResult,
	DaemonIssueDependencyMutationResult,
	DaemonIssueGetRequest,
	DaemonIssueGetResult,
	DaemonIssueListRequest,
	DaemonIssueListResult,
	DaemonIssueRemoveDependencyRequest,
	DaemonIssueSyncRequest,
	DaemonIssueSyncResultEnvelope,
	DaemonIssueUpdateRequest,
	DaemonIssueUpdateResult,
} from "./DaemonIssueRpcSchemas.js"
import type {
	DaemonPlanningCreateIssuesRequest,
	DaemonPlanningCreateIssuesResult,
	DaemonPlanningGenerateRequest,
	DaemonPlanningGenerateResult,
	DaemonPlanningRefineRequest,
	DaemonPlanningRefineResult,
	DaemonPlanningReviewRequest,
	DaemonPlanningReviewResult,
} from "./DaemonPlanningRpcSchemas.js"
import type {
	DaemonPrAbortMergeRequest,
	DaemonPrAbortMergeResult,
	DaemonPrCheckBranchBehindBaseRequest,
	DaemonPrCheckBranchBehindBaseResult,
	DaemonPrCheckGhCliRequest,
	DaemonPrCheckGhCliResult,
	DaemonPrCheckMergeConflictsRequest,
	DaemonPrCheckMergeConflictsResult,
	DaemonPrCheckUncommittedChangesRequest,
	DaemonPrCheckUncommittedChangesResult,
	DaemonPrCleanupRequest,
	DaemonPrCleanupResult,
	DaemonPrCreateRequest,
	DaemonPrCreateResult,
	DaemonPrGetEffectiveBaseBranchRequest,
	DaemonPrGetEffectiveBaseBranchResult,
	DaemonPrGetTargetBranchRequest,
	DaemonPrGetTargetBranchResult,
	DaemonPrMergeBaseIntoBranchRequest,
	DaemonPrMergeBaseIntoBranchResult,
	DaemonPrMergeIssueIntoIssueRequest,
	DaemonPrMergeIssueIntoIssueResult,
	DaemonPrMergeToMainRequest,
	DaemonPrMergeToMainResult,
	DaemonPrUpdateFromBaseRequest,
	DaemonPrUpdateFromBaseResult,
} from "./DaemonPrRpcSchemas.js"
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
import { DaemonAppRpcGroup, DaemonSpecRpcGroup } from "./DaemonRpcs.js"
import type {
	DaemonSpecIssueLinksRequest,
	DaemonSpecIssueLinksResult,
	DaemonSpecLinkAddRequest,
	DaemonSpecLinkAddResult,
	DaemonSpecLinkListRequest,
	DaemonSpecLinkListResult,
	DaemonSpecLinkRemoveRequest,
	DaemonSpecLinkRemoveResult,
	DaemonSpecLinkUpdateRequest,
	DaemonSpecLinkUpdateResult,
	DaemonSpecLintRequest,
	DaemonSpecLintResult,
	DaemonSpecParityRequest,
	DaemonSpecParityResult,
	DaemonSpecPublishConfigGetRequest,
	DaemonSpecPublishConfigGetResult,
	DaemonSpecPublishConfigSetRequest,
	DaemonSpecPublishConfigSetResult,
	DaemonSpecPublishOutcomeGetRequest,
	DaemonSpecPublishOutcomeGetResult,
	DaemonSpecPublishRequest,
	DaemonSpecPublishResult,
	DaemonSpecReadRequest,
	DaemonSpecReadResult,
	DaemonSpecRequirementCreateRequest,
	DaemonSpecRequirementCreateResult,
	DaemonSpecRequirementDeleteRequest,
	DaemonSpecRequirementDeleteResult,
	DaemonSpecRequirementGetRequest,
	DaemonSpecRequirementGetResult,
	DaemonSpecRequirementIssuesRequest,
	DaemonSpecRequirementIssuesResult,
	DaemonSpecRequirementListRequest,
	DaemonSpecRequirementListResult,
	DaemonSpecRequirementUpdateRequest,
	DaemonSpecRequirementUpdateResult,
	DaemonSpecSyncMarkdownRequest,
	DaemonSpecSyncMarkdownResult,
} from "./DaemonSpecRpcSchemas.js"

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
	readonly attachmentList: (
		request: Omit<DaemonAttachmentListRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachmentListResult, DaemonRpcClientError>
	readonly attachmentCountBatch: (
		request: Omit<DaemonAttachmentCountBatchRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachmentCountBatchResult, DaemonRpcClientError>
	readonly attachmentAttachFile: (
		request: Omit<DaemonAttachmentAttachFileRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachmentAttachResult, DaemonRpcClientError>
	readonly attachmentAttachClipboard: (
		request: Omit<DaemonAttachmentAttachClipboardRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachmentAttachResult, DaemonRpcClientError>
	readonly attachmentRemove: (
		request: Omit<DaemonAttachmentRemoveRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachmentRemoveResult, DaemonRpcClientError>
	readonly attachmentMaterializePath: (
		request: Omit<DaemonAttachmentMaterializePathRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonAttachmentMaterializePathResult, DaemonRpcClientError>
	readonly issueGet: (
		request: Omit<DaemonIssueGetRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueGetResult, DaemonRpcClientError>
	readonly issueList: (
		request?: Omit<DaemonIssueListRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueListResult, DaemonRpcClientError>
	readonly issueCreate: (
		request: Omit<DaemonIssueCreateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueCreateResult, DaemonRpcClientError>
	readonly issueUpdate: (
		request: Omit<DaemonIssueUpdateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueUpdateResult, DaemonRpcClientError>
	readonly issueAddDependency: (
		request: Omit<DaemonIssueAddDependencyRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueDependencyMutationResult, DaemonRpcClientError>
	readonly issueRemoveDependency: (
		request: Omit<DaemonIssueRemoveDependencyRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueDependencyMutationResult, DaemonRpcClientError>
	readonly issueClose: (
		request: Omit<DaemonIssueCloseRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueCloseResult, DaemonRpcClientError>
	readonly issueDelete: (
		request: Omit<DaemonIssueDeleteRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueDeleteResult, DaemonRpcClientError>
	readonly issueSync: (
		request?: Omit<DaemonIssueSyncRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonIssueSyncResultEnvelope, DaemonRpcClientError>
	readonly implementationGetRegistry: (
		request?: Omit<DaemonImplementationGetRegistryRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonImplementationGetRegistryResult, DaemonRpcClientError>
	readonly implementationCreate: (
		request: Omit<DaemonImplementationCreateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonImplementationCreateResult, DaemonRpcClientError>
	readonly implementationUpdate: (
		request: Omit<DaemonImplementationUpdateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonImplementationUpdateResult, DaemonRpcClientError>
	readonly implementationDelete: (
		request: Omit<DaemonImplementationDeleteRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonImplementationDeleteResult, DaemonRpcClientError>
	readonly implementationSetDefault: (
		request: Omit<DaemonImplementationSetDefaultRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonImplementationSetDefaultResult, DaemonRpcClientError>
	readonly planningGenerate: (
		request: Omit<DaemonPlanningGenerateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPlanningGenerateResult, DaemonRpcClientError>
	readonly planningReview: (
		request: Omit<DaemonPlanningReviewRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPlanningReviewResult, DaemonRpcClientError>
	readonly planningRefine: (
		request: Omit<DaemonPlanningRefineRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPlanningRefineResult, DaemonRpcClientError>
	readonly planningCreateIssues: (
		request: Omit<DaemonPlanningCreateIssuesRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPlanningCreateIssuesResult, DaemonRpcClientError>
	readonly prCreate: (
		request: Omit<DaemonPrCreateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrCreateResult, DaemonRpcClientError>
	readonly prCleanup: (
		request: Omit<DaemonPrCleanupRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrCleanupResult, DaemonRpcClientError>
	readonly prMergeToMain: (
		request: Omit<DaemonPrMergeToMainRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrMergeToMainResult, DaemonRpcClientError>
	readonly prCheckGhCli: (
		request?: Omit<DaemonPrCheckGhCliRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrCheckGhCliResult, DaemonRpcClientError>
	readonly prUpdateFromBase: (
		request: Omit<DaemonPrUpdateFromBaseRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrUpdateFromBaseResult, DaemonRpcClientError>
	readonly prMergeBaseIntoBranch: (
		request: Omit<DaemonPrMergeBaseIntoBranchRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrMergeBaseIntoBranchResult, DaemonRpcClientError>
	readonly prAbortMerge: (
		request: Omit<DaemonPrAbortMergeRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrAbortMergeResult, DaemonRpcClientError>
	readonly prCheckMergeConflicts: (
		request: Omit<DaemonPrCheckMergeConflictsRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrCheckMergeConflictsResult, DaemonRpcClientError>
	readonly prCheckUncommittedChanges: (
		request: Omit<DaemonPrCheckUncommittedChangesRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrCheckUncommittedChangesResult, DaemonRpcClientError>
	readonly prCheckBranchBehindBase: (
		request: Omit<DaemonPrCheckBranchBehindBaseRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrCheckBranchBehindBaseResult, DaemonRpcClientError>
	readonly prGetEffectiveBaseBranch: (
		request: Omit<DaemonPrGetEffectiveBaseBranchRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrGetEffectiveBaseBranchResult, DaemonRpcClientError>
	readonly prMergeIssueIntoIssue: (
		request: Omit<DaemonPrMergeIssueIntoIssueRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrMergeIssueIntoIssueResult, DaemonRpcClientError>
	readonly prGetTargetBranch: (
		request: Omit<DaemonPrGetTargetBranchRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonPrGetTargetBranchResult, DaemonRpcClientError>
	readonly specRequirementList: (
		request?: Omit<DaemonSpecRequirementListRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecRequirementListResult, DaemonRpcClientError>
	readonly specRequirementGet: (
		request: Omit<DaemonSpecRequirementGetRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecRequirementGetResult, DaemonRpcClientError>
	readonly specRequirementCreate: (
		request: Omit<DaemonSpecRequirementCreateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecRequirementCreateResult, DaemonRpcClientError>
	readonly specRequirementUpdate: (
		request: Omit<DaemonSpecRequirementUpdateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecRequirementUpdateResult, DaemonRpcClientError>
	readonly specRequirementDelete: (
		request: Omit<DaemonSpecRequirementDeleteRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecRequirementDeleteResult, DaemonRpcClientError>
	readonly specRead: (
		request?: Omit<DaemonSpecReadRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecReadResult, DaemonRpcClientError>
	readonly specLint: (
		request?: Omit<DaemonSpecLintRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecLintResult, DaemonRpcClientError>
	readonly specParity: (
		request?: Omit<DaemonSpecParityRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecParityResult, DaemonRpcClientError>
	readonly specIssueLinks: (
		request: Omit<DaemonSpecIssueLinksRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecIssueLinksResult, DaemonRpcClientError>
	readonly specRequirementIssues: (
		request: Omit<DaemonSpecRequirementIssuesRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecRequirementIssuesResult, DaemonRpcClientError>
	readonly specLinkList: (
		request?: Omit<DaemonSpecLinkListRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecLinkListResult, DaemonRpcClientError>
	readonly specLinkAdd: (
		request: Omit<DaemonSpecLinkAddRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecLinkAddResult, DaemonRpcClientError>
	readonly specLinkRemove: (
		request: Omit<DaemonSpecLinkRemoveRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecLinkRemoveResult, DaemonRpcClientError>
	readonly specLinkUpdate: (
		request: Omit<DaemonSpecLinkUpdateRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecLinkUpdateResult, DaemonRpcClientError>
	readonly specPublishConfigGet: (
		request?: Omit<DaemonSpecPublishConfigGetRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecPublishConfigGetResult, DaemonRpcClientError>
	readonly specPublishConfigSet: (
		request: Omit<DaemonSpecPublishConfigSetRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecPublishConfigSetResult, DaemonRpcClientError>
	readonly specPublishOutcomeGet: (
		request?: Omit<DaemonSpecPublishOutcomeGetRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecPublishOutcomeGetResult, DaemonRpcClientError>
	readonly specSyncMarkdown: (
		request?: Omit<DaemonSpecSyncMarkdownRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecSyncMarkdownResult, DaemonRpcClientError>
	readonly specPublish: (
		request?: Omit<DaemonSpecPublishRequest, "rpcProtocolVersion">,
	) => Effect.Effect<DaemonSpecPublishResult, DaemonRpcClientError>
}

const makeDaemonRpcClient = Effect.gen(function* () {
	const raw = yield* RpcClient.make(DaemonAppRpcGroup.merge(DaemonSpecRpcGroup))
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
		attachmentList: (request) =>
			raw.daemonAttachmentList({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		attachmentCountBatch: (request) =>
			raw.daemonAttachmentCountBatch({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		attachmentAttachFile: (request) =>
			raw.daemonAttachmentAttachFile({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		attachmentAttachClipboard: (request) =>
			raw.daemonAttachmentAttachClipboard({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		attachmentRemove: (request) =>
			raw.daemonAttachmentRemove({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		attachmentMaterializePath: (request) =>
			raw.daemonAttachmentMaterializePath({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueGet: (request) =>
			raw.daemonIssueGet({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueList: (request) =>
			raw.daemonIssueList(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		issueCreate: (request) =>
			raw.daemonIssueCreate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueUpdate: (request) =>
			raw.daemonIssueUpdate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueAddDependency: (request) =>
			raw.daemonIssueAddDependency({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueRemoveDependency: (request) =>
			raw.daemonIssueRemoveDependency({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueClose: (request) =>
			raw.daemonIssueClose({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueDelete: (request) =>
			raw.daemonIssueDelete({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		issueSync: (request) =>
			raw.daemonIssueSync(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		implementationGetRegistry: (request) =>
			raw.daemonImplementationGetRegistry(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		implementationCreate: (request) =>
			raw.daemonImplementationCreate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		implementationUpdate: (request) =>
			raw.daemonImplementationUpdate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		implementationDelete: (request) =>
			raw.daemonImplementationDelete({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		implementationSetDefault: (request) =>
			raw.daemonImplementationSetDefault({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		planningGenerate: (request) =>
			raw.daemonPlanningGenerate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		planningReview: (request) =>
			raw.daemonPlanningReview({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		planningRefine: (request) =>
			raw.daemonPlanningRefine({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		planningCreateIssues: (request) =>
			raw.daemonPlanningCreateIssues({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prCreate: (request) =>
			raw.daemonPrCreate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prCleanup: (request) =>
			raw.daemonPrCleanup({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prMergeToMain: (request) =>
			raw.daemonPrMergeToMain({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prCheckGhCli: (request) =>
			raw.daemonPrCheckGhCli(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		prUpdateFromBase: (request) =>
			raw.daemonPrUpdateFromBase({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prMergeBaseIntoBranch: (request) =>
			raw.daemonPrMergeBaseIntoBranch({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prAbortMerge: (request) =>
			raw.daemonPrAbortMerge({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prCheckMergeConflicts: (request) =>
			raw.daemonPrCheckMergeConflicts({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prCheckUncommittedChanges: (request) =>
			raw.daemonPrCheckUncommittedChanges({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prCheckBranchBehindBase: (request) =>
			raw.daemonPrCheckBranchBehindBase({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prGetEffectiveBaseBranch: (request) =>
			raw.daemonPrGetEffectiveBaseBranch({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prMergeIssueIntoIssue: (request) =>
			raw.daemonPrMergeIssueIntoIssue({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		prGetTargetBranch: (request) =>
			raw.daemonPrGetTargetBranch({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specRequirementList: (request) =>
			raw.daemonSpecRequirementList(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specRequirementGet: (request) =>
			raw.daemonSpecRequirementGet({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specRequirementCreate: (request) =>
			raw.daemonSpecRequirementCreate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specRequirementUpdate: (request) =>
			raw.daemonSpecRequirementUpdate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specRequirementDelete: (request) =>
			raw.daemonSpecRequirementDelete({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specRead: (request) =>
			raw.daemonSpecRead(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specLint: (request) =>
			raw.daemonSpecLint(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specParity: (request) =>
			raw.daemonSpecParity(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specIssueLinks: (request) =>
			raw.daemonSpecIssueLinks({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specRequirementIssues: (request) =>
			raw.daemonSpecRequirementIssues({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specLinkList: (request) =>
			raw.daemonSpecLinkList(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specLinkAdd: (request) =>
			raw.daemonSpecLinkAdd({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specLinkRemove: (request) =>
			raw.daemonSpecLinkRemove({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specLinkUpdate: (request) =>
			raw.daemonSpecLinkUpdate({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specPublishConfigGet: (request) =>
			raw.daemonSpecPublishConfigGet(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specPublishConfigSet: (request) =>
			raw.daemonSpecPublishConfigSet({
				...request,
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		specPublishOutcomeGet: (request) =>
			raw.daemonSpecPublishOutcomeGet(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specSyncMarkdown: (request) =>
			raw.daemonSpecSyncMarkdown(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
		specPublish: (request) =>
			raw.daemonSpecPublish(
				request === undefined
					? { rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION }
					: { ...request, rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION },
			),
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
