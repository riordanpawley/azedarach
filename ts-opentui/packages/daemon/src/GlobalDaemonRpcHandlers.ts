import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonAttachmentAttachClipboardRequest,
	type DaemonAttachmentAttachFileRequest,
	type DaemonAttachmentAttachResult,
	type DaemonAttachmentCountBatchRequest,
	type DaemonAttachmentCountBatchResult,
	type DaemonAttachmentListRequest,
	type DaemonAttachmentListResult,
	type DaemonAttachmentMaterializePathRequest,
	type DaemonAttachmentMaterializePathResult,
	type DaemonAttachmentRemoveRequest,
	type DaemonAttachmentRemoveResult,
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
	DaemonGlobalRpcGroup,
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
	type DaemonPlanningCreateIssuesRequest,
	type DaemonPlanningCreateIssuesResult,
	type DaemonPlanningGenerateRequest,
	type DaemonPlanningGenerateResult,
	type DaemonPlanningRefineRequest,
	type DaemonPlanningRefineResult,
	type DaemonPlanningReviewRequest,
	type DaemonPlanningReviewResult,
	type DaemonPrAbortMergeRequest,
	type DaemonPrAbortMergeResult,
	type DaemonPrCheckBranchBehindBaseRequest,
	type DaemonPrCheckBranchBehindBaseResult,
	type DaemonPrCheckGhCliRequest,
	type DaemonPrCheckGhCliResult,
	type DaemonPrCheckMergeConflictsRequest,
	type DaemonPrCheckMergeConflictsResult,
	type DaemonPrCheckUncommittedChangesRequest,
	type DaemonPrCheckUncommittedChangesResult,
	type DaemonPrCleanupRequest,
	type DaemonPrCleanupResult,
	type DaemonPrCreateRequest,
	type DaemonPrCreateResult,
	type DaemonPrGetEffectiveBaseBranchRequest,
	type DaemonPrGetEffectiveBaseBranchResult,
	type DaemonPrGetTargetBranchRequest,
	type DaemonPrGetTargetBranchResult,
	type DaemonPrMergeBaseIntoBranchRequest,
	type DaemonPrMergeBaseIntoBranchResult,
	type DaemonPrMergeIssueIntoIssueRequest,
	type DaemonPrMergeIssueIntoIssueResult,
	type DaemonPrMergeToMainRequest,
	type DaemonPrMergeToMainResult,
	type DaemonPrUpdateFromBaseRequest,
	type DaemonPrUpdateFromBaseResult,
	type DaemonQueueCancelRequest,
	type DaemonQueueCancelResult,
	type DaemonQueueEnqueueRequest,
	type DaemonQueueEnqueueResult,
	type DaemonQueueQueryRequest,
	type DaemonQueueQueryResult,
	type DaemonReconnectRequest,
	type DaemonRestartRequest,
	type DaemonRpcActionError,
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
	type DaemonSpecIssueLinksRequest,
	type DaemonSpecIssueLinksResult,
	type DaemonSpecLinkAddRequest,
	type DaemonSpecLinkAddResult,
	type DaemonSpecLinkListRequest,
	type DaemonSpecLinkListResult,
	type DaemonSpecLinkRemoveRequest,
	type DaemonSpecLinkRemoveResult,
	type DaemonSpecLinkUpdateRequest,
	type DaemonSpecLinkUpdateResult,
	type DaemonSpecLintRequest,
	type DaemonSpecLintResult,
	type DaemonSpecParityRequest,
	type DaemonSpecParityResult,
	type DaemonSpecPublishConfigGetRequest,
	type DaemonSpecPublishConfigGetResult,
	type DaemonSpecPublishConfigSetRequest,
	type DaemonSpecPublishConfigSetResult,
	type DaemonSpecPublishOutcomeGetRequest,
	type DaemonSpecPublishOutcomeGetResult,
	type DaemonSpecPublishRequest,
	type DaemonSpecPublishResult,
	type DaemonSpecReadRequest,
	type DaemonSpecReadResult,
	type DaemonSpecRequirementCreateRequest,
	type DaemonSpecRequirementCreateResult,
	type DaemonSpecRequirementDeleteRequest,
	type DaemonSpecRequirementDeleteResult,
	type DaemonSpecRequirementGetRequest,
	type DaemonSpecRequirementGetResult,
	type DaemonSpecRequirementIssuesRequest,
	type DaemonSpecRequirementIssuesResult,
	type DaemonSpecRequirementListRequest,
	type DaemonSpecRequirementListResult,
	type DaemonSpecRequirementUpdateRequest,
	type DaemonSpecRequirementUpdateResult,
	type DaemonSpecSyncMarkdownRequest,
	type DaemonSpecSyncMarkdownResult,
	type DaemonStatusRequest,
	type DaemonStopRequest,
	type ImageAttachment,
} from "@azedarach/shared/rpc"
import { DateTime, Effect, Layer } from "effect"
import {
	type BackendDaemonControlBoardReadModelRequest,
	type BackendDaemonControlBoardReadModelResult,
	type BackendDaemonControlHealth,
	BackendDaemonControlService,
	type BackendDaemonControlServiceApi,
	type BackendDaemonControlStatus,
} from "./BackendDaemonControlService.js"
import {
	type BackendDaemonAttachResponse,
	type BackendDaemonClientState,
	BackendDaemonService,
	type BackendDaemonSnapshot,
} from "./BackendDaemonService.js"
import {
	BackendDaemonSessionRecovery,
	type BackendDaemonSessionRecoveryApi,
	type BackendDaemonSessionSnapshot,
	type BackendDaemonSessionUpdate,
} from "./BackendDaemonSessionRecovery.js"
import {
	DaemonAttachmentError,
	DaemonAttachmentService,
	type DaemonAttachmentServiceApi,
} from "./DaemonAttachmentService.js"
import {
	type DaemonPlanningCreateIssuesResult as DaemonPlanningCreateIssuesServiceResult,
	DaemonPlanningError,
	type DaemonPlanningPlan,
	type DaemonPlanningReviewFeedback,
	DaemonPlanningService,
} from "./DaemonPlanningService.js"
import { DaemonPrError, DaemonPrService, type DaemonPrServiceApi } from "./DaemonPrService.js"
import {
	DaemonSessionError,
	DaemonSessionService,
	type DaemonSessionServiceApi,
} from "./DaemonSessionService.js"
import {
	ImplementationRegistryDaemonError,
	ImplementationRegistryDaemonService,
} from "./ImplementationRegistryDaemonService.js"
import { SpecDaemonError, SpecDaemonService } from "./SpecDaemonService.js"
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

const mapSpecPublishOutcome = (
	outcome: Awaited<ReturnType<SpecDaemonService["publish"]>> extends Effect.Effect<
		infer A,
		infer _E,
		infer _R
	>
		? A
		: never,
) => ({
	started_at: DateTime.formatIso(outcome.started_at),
	finished_at: DateTime.formatIso(outcome.finished_at),
	status: outcome.status,
	total_requirements: outcome.total_requirements,
	total_links: outcome.total_links,
	outcomes: outcome.outcomes.map((documentOutcome) => ({
		document_key: documentOutcome.document_key,
		title: documentOutcome.title,
		status: documentOutcome.status,
		message: documentOutcome.message,
		requirement_count: documentOutcome.requirement_count,
		link_count: documentOutcome.link_count,
	})),
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
	| DaemonAttachmentError
	| ImplementationRegistryDaemonError
	| DaemonPlanningError
	| DaemonPrError
	| DaemonSessionError
	| SpecDaemonError
	| TrackerIssueDaemonError
	| Error
	| { readonly _tag: string; readonly message?: string }

const mapControlError = (action: string, error: DaemonRpcMappedError): DaemonRpcActionError => {
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

	if (error instanceof DaemonPlanningError) {
		switch (error.reason) {
			case "invalid-input":
				return daemonRpcActionError({
					action,
					code: "invalid-input",
					message: error.message,
				})
			case "generation":
				return daemonRpcActionError({
					action,
					code: "generation-failed",
					message: error.message,
				})
			case "review":
				return daemonRpcActionError({
					action,
					code: "review-failed",
					message: error.message,
				})
			case "refinement":
				return daemonRpcActionError({
					action,
					code: "refinement-failed",
					message: error.message,
				})
			case "issues-creation":
				return daemonRpcActionError({
					action,
					code: "issues-creation-failed",
					message: error.message,
				})
		}
	}

	if (error instanceof DaemonPrError) {
		switch (error.reason) {
			case "worktree-missing":
				return daemonRpcActionError({
					action,
					code: "worktree-missing",
					message: error.message,
				})
			case "pr-disabled":
				return daemonRpcActionError({
					action,
					code: "pr-disabled",
					message: error.message,
				})
			case "validation-failed":
				return daemonRpcActionError({
					action,
					code: "validation-failed",
					message: error.message,
				})
			case "config":
				return daemonRpcActionError({
					action,
					code: "config-error",
					message: error.message,
				})
			case "issue-tracker":
				return daemonRpcActionError({
					action,
					code: "issue-tracker-error",
					message: error.message,
				})
			case "command-failed":
				return daemonRpcActionError({
					action,
					code: "command-failed",
					message: error.message,
				})
			case "merge-conflict":
				return daemonRpcActionError({
					action,
					code: "merge-conflict",
					message: error.message,
				})
		}
	}

	if (error instanceof DaemonAttachmentError) {
		switch (error.reason) {
			case "invalid-input":
				return daemonRpcActionError({
					action,
					code: "invalid-input",
					message: error.message,
				})
			case "not-found":
				return daemonRpcActionError({
					action,
					code: "not-found",
					message: error.message,
				})
			case "storage":
				return daemonRpcActionError({
					action,
					code: "storage",
					message: error.message,
				})
			case "issue-tracker":
				return daemonRpcActionError({
					action,
					code: "issue-tracker",
					message: error.message,
				})
		}
	}

	if (error instanceof DaemonSessionError) {
		switch (error.reason) {
			case "not-found":
				return daemonRpcActionError({
					action,
					code: "not-found",
					message: error.message,
				})
			case "invalid-state":
				return daemonRpcActionError({
					action,
					code: "invalid-state",
					message: error.message,
				})
			case "worktree-missing":
				return daemonRpcActionError({
					action,
					code: "worktree-missing",
					message: error.message,
				})
			case "session-limit":
				return daemonRpcActionError({
					action,
					code: "session-limit",
					message: error.message,
				})
			case "session-metadata":
				return daemonRpcActionError({
					action,
					code: "session-metadata",
					message: error.message,
				})
			case "tracker":
				return daemonRpcActionError({
					action,
					code: "tracker",
					message: error.message,
				})
			case "git":
				return daemonRpcActionError({
					action,
					code: "git",
					message: error.message,
				})
			case "tmux":
				return daemonRpcActionError({
					action,
					code: "tmux",
					message: error.message,
				})
			case "config":
				return daemonRpcActionError({
					action,
					code: "config",
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

	if (error instanceof SpecDaemonError) {
		switch (error.reason) {
			case "storage":
				return daemonRpcActionError({
					action,
					code: "storage",
					message: error.message,
				})
			case "ambiguous-reference":
				return daemonRpcActionError({
					action,
					code: "ambiguous-reference",
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

const mapPlanningGenerateResult = (plan: DaemonPlanningPlan): DaemonPlanningGenerateResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	plan,
})

const mapPlanningReviewResult = (
	feedback: DaemonPlanningReviewFeedback,
): DaemonPlanningReviewResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	feedback,
})

const mapPlanningRefineResult = (plan: DaemonPlanningPlan): DaemonPlanningRefineResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	plan,
})

const mapPlanningCreateIssuesResult = (
	result: DaemonPlanningCreateIssuesServiceResult,
): DaemonPlanningCreateIssuesResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	createdIssues: result.createdIssues,
})

const mapDaemonPrCreateResult = (
	pullRequest: DaemonPrCreateResult["pullRequest"],
): DaemonPrCreateResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	pullRequest,
})

const mapDaemonSessionMutationResult = (
	session: BackendDaemonSessionSnapshot,
): DaemonSessionMutationResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	capturedAtMs: Date.now(),
	session: toDaemonSessionSnapshotEntry(session),
})

const mapDaemonAttachmentAttachResult = (
	attachment: ImageAttachment,
): DaemonAttachmentAttachResult => ({
	rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
	attachment,
})

export const makeDaemonSessionStartRpcHandler =
	(sessionService: Pick<DaemonSessionServiceApi, "start">) =>
	(request: DaemonSessionStartRequest) =>
		sessionService
			.start({
				issueId: request.issueId,
				projectPath: request.projectPath,
				initialPrompt: request.initialPrompt,
				imagePaths: request.imagePaths,
				sessionEnv: request.sessionEnv,
				dangerouslySkipPermissions: request.dangerouslySkipPermissions,
			})
			.pipe(Effect.map(mapDaemonSessionMutationResult), catchDaemonRpcError("sessionStart"))

export const makeDaemonSessionStopRpcHandler =
	(sessionService: Pick<DaemonSessionServiceApi, "stop">) => (request: DaemonSessionStopRequest) =>
		sessionService
			.stop({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(Effect.map(mapDaemonSessionMutationResult), catchDaemonRpcError("sessionStop"))

export const makeDaemonSessionPauseRpcHandler =
	(sessionService: Pick<DaemonSessionServiceApi, "pause">) =>
	(request: DaemonSessionPauseRequest) =>
		sessionService
			.pause({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(Effect.map(mapDaemonSessionMutationResult), catchDaemonRpcError("sessionPause"))

export const makeDaemonSessionResumeRpcHandler =
	(sessionService: Pick<DaemonSessionServiceApi, "resume">) =>
	(request: DaemonSessionResumeRequest) =>
		sessionService
			.resume({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(Effect.map(mapDaemonSessionMutationResult), catchDaemonRpcError("sessionResume"))

export const makeDaemonSessionRecoverRpcHandler =
	(sessionService: Pick<DaemonSessionServiceApi, "recover">) =>
	(request: DaemonSessionRecoverRequest) =>
		sessionService
			.recover({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(Effect.map(mapDaemonSessionMutationResult), catchDaemonRpcError("sessionRecover"))

export const makeDaemonBoardReadModelRpcHandler =
	(control: {
		readonly boardReadModel: (
			request: BackendDaemonControlBoardReadModelRequest,
		) => Effect.Effect<BackendDaemonControlBoardReadModelResult, DaemonRpcMappedError>
	}) =>
	(request: DaemonBoardReadModelRequest) =>
		control
			.boardReadModel({
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(result): DaemonBoardReadModelResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						capturedAtMs: result.capturedAtMs,
						projectPath: result.projectPath,
						tasks: result.tasks,
					}),
				),
				catchDaemonRpcError("boardReadModel"),
			)

const toDaemonSessionSnapshotEntry = (
	session: BackendDaemonSessionSnapshot,
): DaemonSessionSnapshotResult["sessions"][number] => ({
	issueId: session.issueId,
	worktreePath: session.worktreePath,
	tmuxSessionName: session.tmuxSessionName,
	state: session.state,
	startedAt: session.startedAt,
	projectPath: session.projectPath,
})

export const makeDaemonSessionSnapshotRpcHandler =
	(sessionRecovery: Pick<BackendDaemonSessionRecoveryApi, "listActive">) =>
	(request: DaemonSessionSnapshotRequest) =>
		sessionRecovery.listActive(request.projectPath).pipe(
			Effect.map(
				(sessions): DaemonSessionSnapshotResult => ({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					capturedAtMs: Date.now(),
					sessions: sessions.map(toDaemonSessionSnapshotEntry),
				}),
			),
			catchDaemonRpcError("sessionSnapshot"),
		)

export const makeDaemonSessionUpdateStateRpcHandler =
	(sessionRecovery: Pick<BackendDaemonSessionRecoveryApi, "updateState">) =>
	(request: DaemonSessionUpdateStateRequest) => {
		const update: BackendDaemonSessionUpdate = {
			issueId: request.issueId,
			state: request.state,
			projectPath: request.projectPath,
			tmuxSessionName: request.tmuxSessionName,
			worktreePath: request.worktreePath,
			startedAt: request.startedAt,
		}
		return sessionRecovery.updateState(update).pipe(
			Effect.map(
				(session): DaemonSessionMutationResult => ({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					capturedAtMs: Date.now(),
					session: toDaemonSessionSnapshotEntry(session),
				}),
			),
			catchDaemonRpcError("sessionUpdateState"),
		)
	}

export const makeDaemonPlanningGenerateRpcHandler =
	(planning: Pick<DaemonPlanningService, "generate">) => (request: DaemonPlanningGenerateRequest) =>
		planning
			.generate(request.featureDescription)
			.pipe(Effect.map(mapPlanningGenerateResult), catchDaemonRpcError("planningGenerate"))

export const makeDaemonPlanningReviewRpcHandler =
	(planning: Pick<DaemonPlanningService, "review">) => (request: DaemonPlanningReviewRequest) =>
		planning
			.review(request.plan)
			.pipe(Effect.map(mapPlanningReviewResult), catchDaemonRpcError("planningReview"))

export const makeDaemonPlanningRefineRpcHandler =
	(planning: Pick<DaemonPlanningService, "refine">) => (request: DaemonPlanningRefineRequest) =>
		planning
			.refine(request.plan, request.feedback)
			.pipe(Effect.map(mapPlanningRefineResult), catchDaemonRpcError("planningRefine"))

export const makeDaemonPlanningCreateIssuesRpcHandler =
	(planning: Pick<DaemonPlanningService, "createIssues">) =>
	(request: DaemonPlanningCreateIssuesRequest) =>
		planning
			.createIssues(request)
			.pipe(Effect.map(mapPlanningCreateIssuesResult), catchDaemonRpcError("planningCreateIssues"))

export const makeDaemonPrCreateRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "create">) => (request: DaemonPrCreateRequest) =>
		prs
			.create(request.issueId, request.projectPath)
			.pipe(Effect.map(mapDaemonPrCreateResult), catchDaemonRpcError("prCreate"))

export const makeDaemonPrCleanupRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "cleanup">) => (request: DaemonPrCleanupRequest) =>
		prs
			.cleanup({
				issueId: request.issueId,
				projectPath: request.projectPath,
				closeIssue: request.closeIssue,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					cleanedUp: true,
				} satisfies DaemonPrCleanupResult),
				catchDaemonRpcError("prCleanup"),
			)

export const makeDaemonPrMergeToMainRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "mergeToMain">) => (request: DaemonPrMergeToMainRequest) =>
		prs
			.mergeToMain({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					merged: true,
				} satisfies DaemonPrMergeToMainResult),
				catchDaemonRpcError("prMergeToMain"),
			)

export const makeDaemonPrCheckGhCliRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "checkGhCli">) => (_request: DaemonPrCheckGhCliRequest) =>
		prs.checkGhCli().pipe(
			Effect.map(
				(available): DaemonPrCheckGhCliResult => ({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					available,
				}),
			),
			catchDaemonRpcError("prCheckGhCli"),
		)

export const makeDaemonPrUpdateFromBaseRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "updateFromBase">) => (request: DaemonPrUpdateFromBaseRequest) =>
		prs
			.updateFromBase({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					updated: true,
				} satisfies DaemonPrUpdateFromBaseResult),
				catchDaemonRpcError("prUpdateFromBase"),
			)

export const makeDaemonPrMergeBaseIntoBranchRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "mergeBaseIntoBranch">) =>
	(request: DaemonPrMergeBaseIntoBranchRequest) =>
		prs
			.mergeBaseIntoBranch({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					merged: true,
				} satisfies DaemonPrMergeBaseIntoBranchResult),
				catchDaemonRpcError("prMergeBaseIntoBranch"),
			)

export const makeDaemonPrAbortMergeRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "abortMerge">) => (request: DaemonPrAbortMergeRequest) =>
		prs
			.abortMerge({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					aborted: true,
				} satisfies DaemonPrAbortMergeResult),
				catchDaemonRpcError("prAbortMerge"),
			)

export const makeDaemonPrCheckMergeConflictsRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "checkMergeConflicts">) =>
	(request: DaemonPrCheckMergeConflictsRequest) =>
		prs
			.checkMergeConflicts({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(check): DaemonPrCheckMergeConflictsResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						hasConflictRisk: check.hasConflictRisk,
						conflictingFiles: [...check.conflictingFiles],
						baseBranch: check.baseBranch,
						issueBranch: check.issueBranch,
					}),
				),
				catchDaemonRpcError("prCheckMergeConflicts"),
			)

export const makeDaemonPrCheckUncommittedChangesRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "checkUncommittedChanges">) =>
	(request: DaemonPrCheckUncommittedChangesRequest) =>
		prs
			.checkUncommittedChanges({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(result): DaemonPrCheckUncommittedChangesResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						hasUncommittedChanges: result.hasUncommittedChanges,
						changedFiles: [...result.changedFiles],
					}),
				),
				catchDaemonRpcError("prCheckUncommittedChanges"),
			)

export const makeDaemonPrCheckBranchBehindBaseRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "checkBranchBehindBase">) =>
	(request: DaemonPrCheckBranchBehindBaseRequest) =>
		prs
			.checkBranchBehindBase({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(result): DaemonPrCheckBranchBehindBaseResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						behind: result.behind,
						ahead: result.ahead,
						baseBranch: result.baseBranch,
					}),
				),
				catchDaemonRpcError("prCheckBranchBehindBase"),
			)

export const makeDaemonPrGetEffectiveBaseBranchRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "getEffectiveBaseBranch">) =>
	(request: DaemonPrGetEffectiveBaseBranchRequest) =>
		prs
			.getEffectiveBaseBranch({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(result): DaemonPrGetEffectiveBaseBranchResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						baseBranch: result.baseBranch,
						parentEpicId: result.parentEpicId,
					}),
				),
				catchDaemonRpcError("prGetEffectiveBaseBranch"),
			)

export const makeDaemonPrMergeIssueIntoIssueRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "mergeIssueIntoIssue">) =>
	(request: DaemonPrMergeIssueIntoIssueRequest) =>
		prs
			.mergeIssueIntoIssue({
				sourceIssueId: request.sourceIssueId,
				targetIssueId: request.targetIssueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					merged: true,
				} satisfies DaemonPrMergeIssueIntoIssueResult),
				catchDaemonRpcError("prMergeIssueIntoIssue"),
			)

export const makeDaemonPrGetTargetBranchRpcHandler =
	(prs: Pick<DaemonPrServiceApi, "getTargetBranch">) => (request: DaemonPrGetTargetBranchRequest) =>
		prs
			.getTargetBranch({
				issueId: request.issueId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(result): DaemonPrGetTargetBranchResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						targetBranch: result.targetBranch,
						isEpicChild: result.isEpicChild,
					}),
				),
				catchDaemonRpcError("prGetTargetBranch"),
			)

export const makeDaemonAttachmentListRpcHandler =
	(attachments: Pick<DaemonAttachmentServiceApi, "list">) =>
	(request: DaemonAttachmentListRequest) =>
		attachments.list(request.issueId, request.projectPath).pipe(
			Effect.map(
				(result): DaemonAttachmentListResult => ({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					attachments: [...result],
				}),
			),
			catchDaemonRpcError("attachmentList"),
		)

export const makeDaemonAttachmentCountBatchRpcHandler =
	(attachments: Pick<DaemonAttachmentServiceApi, "countBatch">) =>
	(request: DaemonAttachmentCountBatchRequest) =>
		attachments.countBatch(request.issueIds, request.projectPath).pipe(
			Effect.map(
				(counts): DaemonAttachmentCountBatchResult => ({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					counts,
				}),
			),
			catchDaemonRpcError("attachmentCountBatch"),
		)

export const makeDaemonAttachmentAttachFileRpcHandler =
	(attachments: Pick<DaemonAttachmentServiceApi, "attachFile">) =>
	(request: DaemonAttachmentAttachFileRequest) =>
		attachments
			.attachFile({
				issueId: request.issueId,
				filePath: request.filePath,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(mapDaemonAttachmentAttachResult),
				catchDaemonRpcError("attachmentAttachFile"),
			)

export const makeDaemonAttachmentAttachClipboardRpcHandler =
	(attachments: Pick<DaemonAttachmentServiceApi, "attachClipboard">) =>
	(request: DaemonAttachmentAttachClipboardRequest) =>
		attachments
			.attachClipboard({
				issueId: request.issueId,
				filename: request.filename,
				mimeType: request.mimeType,
				base64Content: request.base64Content,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(mapDaemonAttachmentAttachResult),
				catchDaemonRpcError("attachmentAttachClipboard"),
			)

export const makeDaemonAttachmentRemoveRpcHandler =
	(attachments: Pick<DaemonAttachmentServiceApi, "remove">) =>
	(request: DaemonAttachmentRemoveRequest) =>
		attachments
			.remove({
				issueId: request.issueId,
				attachmentId: request.attachmentId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.as({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					removed: true,
				} satisfies DaemonAttachmentRemoveResult),
				catchDaemonRpcError("attachmentRemove"),
			)

export const makeDaemonAttachmentMaterializePathRpcHandler =
	(attachments: Pick<DaemonAttachmentServiceApi, "materializePath">) =>
	(request: DaemonAttachmentMaterializePathRequest) =>
		attachments
			.materializePath({
				issueId: request.issueId,
				attachmentId: request.attachmentId,
				projectPath: request.projectPath,
			})
			.pipe(
				Effect.map(
					(path): DaemonAttachmentMaterializePathResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						path,
					}),
				),
				catchDaemonRpcError("attachmentMaterializePath"),
			)

export const makeGlobalDaemonRpcHandlers = Effect.gen(function* () {
	const runtime = yield* BackendDaemonService
	const control = yield* BackendDaemonControlService
	const sessionRecovery = yield* BackendDaemonSessionRecovery
	const sessionService = yield* DaemonSessionService
	const attachments = yield* DaemonAttachmentService
	const planning = yield* DaemonPlanningService
	const prs = yield* DaemonPrService
	const implementations = yield* ImplementationRegistryDaemonService
	const issues = yield* TrackerIssueDaemonService
	const specs = yield* SpecDaemonService

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
		daemonSessionSnapshot: makeDaemonSessionSnapshotRpcHandler(sessionRecovery),
		daemonBoardReadModel: makeDaemonBoardReadModelRpcHandler(control),
		daemonSessionStart: makeDaemonSessionStartRpcHandler(sessionService),
		daemonSessionStop: makeDaemonSessionStopRpcHandler(sessionService),
		daemonSessionPause: makeDaemonSessionPauseRpcHandler(sessionService),
		daemonSessionResume: makeDaemonSessionResumeRpcHandler(sessionService),
		daemonSessionRecover: makeDaemonSessionRecoverRpcHandler(sessionService),
		daemonSessionUpdateState: makeDaemonSessionUpdateStateRpcHandler(sessionRecovery),
		daemonPlanningGenerate: makeDaemonPlanningGenerateRpcHandler(planning),
		daemonPlanningReview: makeDaemonPlanningReviewRpcHandler(planning),
		daemonPlanningRefine: makeDaemonPlanningRefineRpcHandler(planning),
		daemonPlanningCreateIssues: makeDaemonPlanningCreateIssuesRpcHandler(planning),
		daemonPrCreate: makeDaemonPrCreateRpcHandler(prs),
		daemonPrCleanup: makeDaemonPrCleanupRpcHandler(prs),
		daemonPrMergeToMain: makeDaemonPrMergeToMainRpcHandler(prs),
		daemonPrCheckGhCli: makeDaemonPrCheckGhCliRpcHandler(prs),
		daemonPrUpdateFromBase: makeDaemonPrUpdateFromBaseRpcHandler(prs),
		daemonPrMergeBaseIntoBranch: makeDaemonPrMergeBaseIntoBranchRpcHandler(prs),
		daemonPrAbortMerge: makeDaemonPrAbortMergeRpcHandler(prs),
		daemonPrCheckMergeConflicts: makeDaemonPrCheckMergeConflictsRpcHandler(prs),
		daemonPrCheckUncommittedChanges: makeDaemonPrCheckUncommittedChangesRpcHandler(prs),
		daemonPrCheckBranchBehindBase: makeDaemonPrCheckBranchBehindBaseRpcHandler(prs),
		daemonPrGetEffectiveBaseBranch: makeDaemonPrGetEffectiveBaseBranchRpcHandler(prs),
		daemonPrMergeIssueIntoIssue: makeDaemonPrMergeIssueIntoIssueRpcHandler(prs),
		daemonPrGetTargetBranch: makeDaemonPrGetTargetBranchRpcHandler(prs),
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
		daemonAttachmentList: makeDaemonAttachmentListRpcHandler(attachments),
		daemonAttachmentCountBatch: makeDaemonAttachmentCountBatchRpcHandler(attachments),
		daemonAttachmentAttachFile: makeDaemonAttachmentAttachFileRpcHandler(attachments),
		daemonAttachmentAttachClipboard: makeDaemonAttachmentAttachClipboardRpcHandler(attachments),
		daemonAttachmentRemove: makeDaemonAttachmentRemoveRpcHandler(attachments),
		daemonAttachmentMaterializePath: makeDaemonAttachmentMaterializePathRpcHandler(attachments),
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
		daemonSpecRequirementList: (request: DaemonSpecRequirementListRequest) =>
			specs
				.listRequirements(request.projectPath, {
					query: request.query,
					kind: request.kind,
					status: request.status,
					priority: request.priority,
				})
				.pipe(
					Effect.map(
						(requirements): DaemonSpecRequirementListResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							requirements: [...requirements],
						}),
					),
					catchDaemonRpcError("specRequirementList"),
				),
		daemonSpecRequirementGet: (request: DaemonSpecRequirementGetRequest) =>
			specs.getRequirement(request.reference, request.projectPath, request.selector).pipe(
				Effect.map(
					(requirement): DaemonSpecRequirementGetResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						requirement: requirement ?? null,
					}),
				),
				catchDaemonRpcError("specRequirementGet"),
			),
		daemonSpecRequirementCreate: (request: DaemonSpecRequirementCreateRequest) =>
			specs.createRequirement(request.input, request.projectPath).pipe(
				Effect.map(
					(requirement): DaemonSpecRequirementCreateResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						requirement,
					}),
				),
				catchDaemonRpcError("specRequirementCreate"),
			),
		daemonSpecRequirementUpdate: (request: DaemonSpecRequirementUpdateRequest) =>
			specs
				.updateRequirement(request.reference, request.fields, request.projectPath, request.selector)
				.pipe(
					Effect.map(
						(updated): DaemonSpecRequirementUpdateResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							updated,
						}),
					),
					catchDaemonRpcError("specRequirementUpdate"),
				),
		daemonSpecRequirementDelete: (request: DaemonSpecRequirementDeleteRequest) =>
			specs.deleteRequirement(request.reference, request.projectPath, request.selector).pipe(
				Effect.map(
					(deleted): DaemonSpecRequirementDeleteResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						deleted,
					}),
				),
				catchDaemonRpcError("specRequirementDelete"),
			),
		daemonSpecRead: (request: DaemonSpecReadRequest) =>
			specs.readSnapshot(request.projectPath).pipe(
				Effect.map(
					(snapshot): DaemonSpecReadResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						requirements: [...snapshot.requirements],
						links: [...snapshot.links],
						coverage: snapshot.coverage,
					}),
				),
				catchDaemonRpcError("specRead"),
			),
		daemonSpecLint: (request: DaemonSpecLintRequest) =>
			specs.lint(request.projectPath).pipe(
				Effect.map(
					(lint): DaemonSpecLintResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						lint,
					}),
				),
				catchDaemonRpcError("specLint"),
			),
		daemonSpecParity: (request: DaemonSpecParityRequest) =>
			specs.getParityReport(request.implementation?.trim() || "default", request.projectPath).pipe(
				Effect.map(
					(report): DaemonSpecParityResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						report,
					}),
				),
				catchDaemonRpcError("specParity"),
			),
		daemonSpecIssueLinks: (request: DaemonSpecIssueLinksRequest) =>
			specs.listIssueRequirements(request.issueId, request.projectPath).pipe(
				Effect.map(
					(linkedRequirements): DaemonSpecIssueLinksResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						linkedRequirements: [...linkedRequirements],
					}),
				),
				catchDaemonRpcError("specIssueLinks"),
			),
		daemonSpecRequirementIssues: (request: DaemonSpecRequirementIssuesRequest) =>
			specs.listRequirementIssues(request.reference, request.projectPath, request.selector).pipe(
				Effect.map(
					(linkedIssues): DaemonSpecRequirementIssuesResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						linkedIssues: [...linkedIssues],
					}),
				),
				catchDaemonRpcError("specRequirementIssues"),
			),
		daemonSpecLinkList: (request: DaemonSpecLinkListRequest) =>
			specs.listLinks(request.filters, request.projectPath).pipe(
				Effect.map(
					(links): DaemonSpecLinkListResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						links: [...links],
					}),
				),
				catchDaemonRpcError("specLinkList"),
			),
		daemonSpecLinkAdd: (request: DaemonSpecLinkAddRequest) =>
			specs
				.addIssueLink(
					request.issueId,
					request.requirementReference,
					request.linkType ?? "relates",
					request.projectPath,
					request.requirementSelector,
					request.implementations,
					request.fulfillment,
				)
				.pipe(
					Effect.map(
						(): DaemonSpecLinkAddResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							added: true,
						}),
					),
					catchDaemonRpcError("specLinkAdd"),
				),
		daemonSpecLinkRemove: (request: DaemonSpecLinkRemoveRequest) =>
			specs
				.removeIssueLink(
					request.issueId,
					request.requirementReference,
					request.linkType,
					request.projectPath,
					request.requirementSelector,
					request.implementations,
				)
				.pipe(
					Effect.map(
						(removed): DaemonSpecLinkRemoveResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							removed,
						}),
					),
					catchDaemonRpcError("specLinkRemove"),
				),
		daemonSpecLinkUpdate: (request: DaemonSpecLinkUpdateRequest) =>
			specs
				.updateIssueLink(
					request.issueId,
					request.requirementReference,
					request.fields,
					request.linkType,
					request.projectPath,
					request.requirementSelector,
				)
				.pipe(
					Effect.map(
						(updated): DaemonSpecLinkUpdateResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							updated,
						}),
					),
					catchDaemonRpcError("specLinkUpdate"),
				),
		daemonSpecPublishConfigGet: (request: DaemonSpecPublishConfigGetRequest) =>
			specs.getPublishConfig(request.projectPath).pipe(
				Effect.map(
					(config): DaemonSpecPublishConfigGetResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						config,
					}),
				),
				catchDaemonRpcError("specPublishConfigGet"),
			),
		daemonSpecPublishConfigSet: (request: DaemonSpecPublishConfigSetRequest) =>
			specs.setPublishConfig(request.config, request.projectPath).pipe(
				Effect.map(
					(): DaemonSpecPublishConfigSetResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						updated: true,
					}),
				),
				catchDaemonRpcError("specPublishConfigSet"),
			),
		daemonSpecPublishOutcomeGet: (request: DaemonSpecPublishOutcomeGetRequest) =>
			specs.getLastPublishOutcome(request.projectPath).pipe(
				Effect.map(
					(lastOutcome): DaemonSpecPublishOutcomeGetResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						last_outcome: lastOutcome === undefined ? null : mapSpecPublishOutcome(lastOutcome),
					}),
				),
				catchDaemonRpcError("specPublishOutcomeGet"),
			),
		daemonSpecSyncMarkdown: (request: DaemonSpecSyncMarkdownRequest) =>
			specs
				.syncMarkdown(
					{
						outDir: request.outDir,
						check: request.check,
					},
					request.projectPath,
				)
				.pipe(
					Effect.map(
						(sync): DaemonSpecSyncMarkdownResult => ({
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							sync,
						}),
					),
					catchDaemonRpcError("specSyncMarkdown"),
				),
		daemonSpecPublish: (request: DaemonSpecPublishRequest) =>
			specs.publish(request.projectPath).pipe(
				Effect.map(
					(outcome): DaemonSpecPublishResult => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						outcome: mapSpecPublishOutcome(outcome),
					}),
				),
				catchDaemonRpcError("specPublish"),
			),
		daemonEventStream: (_request: DaemonEventStreamRequest) =>
			unsupportedDaemonRpc<DaemonEventStreamResult>("eventStream"),
	}
})

export const GlobalDaemonRpcHandlersLive = Layer.scopedContext(
	DaemonGlobalRpcGroup.toHandlersContext(makeGlobalDaemonRpcHandlers),
)
