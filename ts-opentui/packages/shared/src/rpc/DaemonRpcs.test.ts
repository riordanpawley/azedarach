import { describe, expect, it } from "bun:test"
import { Effect, Schema } from "effect"
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
	DaemonImplementationGetRegistryResultSchema,
} from "./DaemonImplementationRpcSchemas.js"
import {
	DaemonPlanningCreateIssuesRequestSchema,
	DaemonPlanningCreateIssuesResultSchema,
	DaemonPlanningGenerateRequestSchema,
	DaemonPlanningGenerateResultSchema,
	DaemonPlanningRefineRequestSchema,
	DaemonPlanningRefineResultSchema,
	DaemonPlanningReviewRequestSchema,
	DaemonPlanningReviewResultSchema,
	type PlanningPlan,
	PlanningPlanSchema,
	type PlanningReviewFeedback,
	PlanningReviewFeedbackSchema,
} from "./DaemonPlanningRpcSchemas.js"
import {
	DaemonPrAbortMergeRequestSchema,
	DaemonPrAbortMergeResultSchema,
	DaemonPrCheckGhCliRequestSchema,
	DaemonPrCheckGhCliResultSchema,
	DaemonPrCleanupRequestSchema,
	DaemonPrCleanupResultSchema,
	DaemonPrCreateRequestSchema,
	DaemonPrCreateResultSchema,
	DaemonPrMergeBaseIntoBranchRequestSchema,
	DaemonPrMergeBaseIntoBranchResultSchema,
	DaemonPrMergeToMainRequestSchema,
	DaemonPrMergeToMainResultSchema,
	DaemonPrUpdateFromBaseRequestSchema,
	DaemonPrUpdateFromBaseResultSchema,
} from "./DaemonPrRpcSchemas.js"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonAttachRequestSchema,
	DaemonBoardReadModelRequestSchema,
	DaemonBoardReadModelResultSchema,
	DaemonDevServerListResultSchema,
	DaemonDevServerStartRequestSchema,
	DaemonDevServerStatusRequestSchema,
	DaemonEventStreamResultSchema,
	DaemonHealthResultSchema,
	DaemonQueueCancelResultSchema,
	DaemonQueueEnqueueRequestSchema,
	DaemonQueueQueryResultSchema,
	DaemonSessionSnapshotResultSchema,
	DaemonSessionStartRequestSchema,
	DaemonSessionUpdateStateRequestSchema,
	DaemonStatusRequestSchema,
} from "./DaemonRpcSchemas.js"
import {
	DaemonAppRpcGroup,
	DaemonImplementationRpcGroup,
	DaemonPlanningRpcGroup,
	DaemonPrRpcGroup,
	DaemonRpcGroup,
	DaemonSpecLinksRpcGroup,
	DaemonSpecPublishConfigRpcGroup,
	DaemonSpecReadRpcGroup,
	DaemonSpecRequirementMutationRpcGroup,
	DaemonSpecRpcGroup,
	DaemonSpecSyncRpcGroup,
} from "./DaemonRpcs.js"
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

describe("DaemonRpcs", () => {
	it("registers all daemon rpc operations in a single group", () => {
		const keys = [...DaemonRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonAttach",
			"daemonAttachmentAttachClipboard",
			"daemonAttachmentAttachFile",
			"daemonAttachmentCountBatch",
			"daemonAttachmentList",
			"daemonAttachmentMaterializePath",
			"daemonAttachmentRemove",
			"daemonBoardReadModel",
			"daemonDevServerList",
			"daemonDevServerStart",
			"daemonDevServerStatus",
			"daemonDevServerStop",
			"daemonEventStream",
			"daemonHealth",
			"daemonHeartbeat",
			"daemonIssueAddDependency",
			"daemonIssueClose",
			"daemonIssueCreate",
			"daemonIssueDelete",
			"daemonIssueGet",
			"daemonIssueList",
			"daemonIssueRemoveDependency",
			"daemonIssueSync",
			"daemonIssueUpdate",
			"daemonLogs",
			"daemonQueueCancel",
			"daemonQueueEnqueue",
			"daemonQueueQuery",
			"daemonReconnect",
			"daemonRestart",
			"daemonSessionPause",
			"daemonSessionRecover",
			"daemonSessionResume",
			"daemonSessionSnapshot",
			"daemonSessionStart",
			"daemonSessionStop",
			"daemonSessionUpdateState",
			"daemonStatus",
			"daemonStop",
		])
	})

	it("registers implementation registry rpc operations in a dedicated group", () => {
		const keys = [...DaemonImplementationRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonImplementationCreate",
			"daemonImplementationDelete",
			"daemonImplementationGetRegistry",
			"daemonImplementationSetDefault",
			"daemonImplementationUpdate",
		])
	})

	it("registers planning rpc operations in a dedicated group", () => {
		const keys = [...DaemonPlanningRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonPlanningCreateIssues",
			"daemonPlanningGenerate",
			"daemonPlanningRefine",
			"daemonPlanningReview",
		])
	})

	it("registers PR rpc operations in a dedicated group", () => {
		const keys = [...DaemonPrRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonPrAbortMerge",
			"daemonPrCheckGhCli",
			"daemonPrCleanup",
			"daemonPrCreate",
			"daemonPrMergeBaseIntoBranch",
			"daemonPrMergeToMain",
			"daemonPrUpdateFromBase",
		])
	})

	it("registers spec rpc operations in a dedicated group", () => {
		const keys = [...DaemonSpecRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonSpecIssueLinks",
			"daemonSpecLinkAdd",
			"daemonSpecLinkList",
			"daemonSpecLinkRemove",
			"daemonSpecLinkUpdate",
			"daemonSpecLint",
			"daemonSpecParity",
			"daemonSpecPublish",
			"daemonSpecPublishConfigGet",
			"daemonSpecPublishConfigSet",
			"daemonSpecPublishOutcomeGet",
			"daemonSpecRead",
			"daemonSpecRequirementCreate",
			"daemonSpecRequirementDelete",
			"daemonSpecRequirementGet",
			"daemonSpecRequirementIssues",
			"daemonSpecRequirementList",
			"daemonSpecRequirementUpdate",
			"daemonSpecSyncMarkdown",
		])
	})

	it("registers the spec rpc groups by concern", () => {
		expect([...DaemonSpecRequirementMutationRpcGroup.requests.keys()].sort()).toEqual([
			"daemonSpecRequirementCreate",
			"daemonSpecRequirementDelete",
			"daemonSpecRequirementUpdate",
		])
		expect([...DaemonSpecLinksRpcGroup.requests.keys()].sort()).toEqual([
			"daemonSpecLinkAdd",
			"daemonSpecLinkList",
			"daemonSpecLinkRemove",
			"daemonSpecLinkUpdate",
		])
		expect([...DaemonSpecPublishConfigRpcGroup.requests.keys()].sort()).toEqual([
			"daemonSpecPublishConfigGet",
			"daemonSpecPublishConfigSet",
			"daemonSpecPublishOutcomeGet",
		])
		expect([...DaemonSpecSyncRpcGroup.requests.keys()].sort()).toEqual([
			"daemonSpecPublish",
			"daemonSpecSyncMarkdown",
		])
	})

	it("exposes the composed daemon application rpc contract", () => {
		const keys = [...DaemonAppRpcGroup.requests.keys()].sort()
		expect(keys).toEqual(
			[
				...new Set([
					...DaemonRpcGroup.requests.keys(),
					...DaemonImplementationRpcGroup.requests.keys(),
					...DaemonPlanningRpcGroup.requests.keys(),
					...DaemonPrRpcGroup.requests.keys(),
					...DaemonSpecReadRpcGroup.requests.keys(),
				]),
			].sort(),
		)
	})

	it("exposes the expanded spec rpc contract", () => {
		const keys = [...DaemonSpecRpcGroup.requests.keys()].sort()
		expect(keys).toEqual([
			"daemonSpecIssueLinks",
			"daemonSpecLinkAdd",
			"daemonSpecLinkList",
			"daemonSpecLinkRemove",
			"daemonSpecLinkUpdate",
			"daemonSpecLint",
			"daemonSpecParity",
			"daemonSpecPublish",
			"daemonSpecPublishConfigGet",
			"daemonSpecPublishConfigSet",
			"daemonSpecPublishOutcomeGet",
			"daemonSpecRead",
			"daemonSpecRequirementCreate",
			"daemonSpecRequirementDelete",
			"daemonSpecRequirementGet",
			"daemonSpecRequirementIssues",
			"daemonSpecRequirementList",
			"daemonSpecRequirementUpdate",
			"daemonSpecSyncMarkdown",
		])
	})

	it("validates request schemas for protocol versioned operations", async () => {
		const decodeStatus = Schema.decodeUnknown(DaemonStatusRequestSchema)
		const decodeAttach = Schema.decodeUnknown(DaemonAttachRequestSchema)

		const status = await Effect.runPromise(
			decodeStatus({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		)
		const attach = await Effect.runPromise(
			decodeAttach({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				clientId: "client-a",
			}),
		)

		expect(status.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(attach.clientId).toBe("client-a")
	})

	it("validates session mutation request schemas", async () => {
		const decodeStart = Schema.decodeUnknown(DaemonSessionStartRequestSchema)
		const decodeUpdate = Schema.decodeUnknown(DaemonSessionUpdateStateRequestSchema)

		const start = await Effect.runPromise(
			decodeStart({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qc",
				projectPath: "/tmp/project",
				initialPrompt: "Continue the task",
				imagePaths: ["/tmp/image-1.png", "/tmp/image-2.png"],
				sessionEnv: {
					FOO: "bar",
				},
				dangerouslySkipPermissions: true,
			}),
		)
		const update = await Effect.runPromise(
			decodeUpdate({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qc",
				state: "waiting",
				projectPath: "/tmp/project",
				tmuxSessionName: "az-qc",
				worktreePath: "/tmp/project/.worktrees/qc",
				startedAt: "2026-03-20T00:00:00.000Z",
			}),
		)

		expect(start.issueId).toBe("qc")
		expect(start.projectPath).toBe("/tmp/project")
		expect(start.initialPrompt).toBe("Continue the task")
		expect(start.imagePaths).toEqual(["/tmp/image-1.png", "/tmp/image-2.png"])
		expect(start.sessionEnv).toEqual({
			FOO: "bar",
		})
		expect(start.dangerouslySkipPermissions).toBe(true)
		expect(update.tmuxSessionName).toBe("az-qc")
		expect(update.worktreePath).toBe("/tmp/project/.worktrees/qc")
		expect(update.startedAt).toBe("2026-03-20T00:00:00.000Z")
		expect(update.state).toBe("waiting")
	})

	it("validates devserver request and result schemas", async () => {
		const decodeStatusRequest = Schema.decodeUnknown(DaemonDevServerStatusRequestSchema)
		const decodeStartRequest = Schema.decodeUnknown(DaemonDevServerStartRequestSchema)
		const decodeListResult = Schema.decodeUnknown(DaemonDevServerListResultSchema)

		const statusRequest = await Effect.runPromise(
			decodeStatusRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qp",
				serverName: "default",
				projectPath: "/tmp/project",
			}),
		)
		const startRequest = await Effect.runPromise(
			decodeStartRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "qp",
				projectPath: "/tmp/project",
			}),
		)
		const listResult = await Effect.runPromise(
			decodeListResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				capturedAtMs: 500,
				servers: [
					{
						issueId: "qp",
						serverName: "default",
						status: "running",
						port: 3001,
						windowName: "dev-default",
						tmuxSession: "az-qp",
						worktreePath: "/tmp/project/.worktrees/qp",
						projectPath: "/tmp/project",
						startedAt: "2026-03-14T06:00:00.000Z",
						error: null,
					},
				],
			}),
		)

		expect(statusRequest.serverName).toBe("default")
		expect(startRequest.projectPath).toBe("/tmp/project")
		expect(listResult.servers[0]?.status).toBe("running")
	})

	it("validates read-only spec request and result schemas", async () => {
		const decodeListRequest = Schema.decodeUnknown(DaemonSpecRequirementListRequestSchema)
		const decodeListResult = Schema.decodeUnknown(DaemonSpecRequirementListResultSchema)
		const decodeGetRequest = Schema.decodeUnknown(DaemonSpecRequirementGetRequestSchema)
		const decodeGetResult = Schema.decodeUnknown(DaemonSpecRequirementGetResultSchema)
		const decodeReadRequest = Schema.decodeUnknown(DaemonSpecReadRequestSchema)
		const decodeReadResult = Schema.decodeUnknown(DaemonSpecReadResultSchema)
		const decodeLintRequest = Schema.decodeUnknown(DaemonSpecLintRequestSchema)
		const decodeLintResult = Schema.decodeUnknown(DaemonSpecLintResultSchema)
		const decodeParityRequest = Schema.decodeUnknown(DaemonSpecParityRequestSchema)
		const decodeParityResult = Schema.decodeUnknown(DaemonSpecParityResultSchema)
		const decodeIssueLinksRequest = Schema.decodeUnknown(DaemonSpecIssueLinksRequestSchema)
		const decodeIssueLinksResult = Schema.decodeUnknown(DaemonSpecIssueLinksResultSchema)
		const decodeRequirementIssuesRequest = Schema.decodeUnknown(
			DaemonSpecRequirementIssuesRequestSchema,
		)
		const decodeRequirementIssuesResult = Schema.decodeUnknown(
			DaemonSpecRequirementIssuesResultSchema,
		)

		const listRequest = await Effect.runPromise(
			decodeListRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				query: "board",
				kind: "functional",
				status: "open",
				priority: 1,
			}),
		)
		const requirement = {
			id: "req-1",
			local_id: "S1",
			external_code: "A-1",
			title: "Track board state",
			body: "Keep the board in sync.",
			kind: "functional",
			status: "draft",
			priority: 1,
			created_at: "2026-03-14T06:00:00.000Z",
			updated_at: "2026-03-14T06:01:00.000Z",
		}
		const listResult = await Effect.runPromise(
			decodeListResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				requirements: [requirement],
			}),
		)
		const getRequest = await Effect.runPromise(
			decodeGetRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				reference: "S1",
				selector: "local_id",
			}),
		)
		const getResult = await Effect.runPromise(
			decodeGetResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				requirement,
			}),
		)
		const readRequest = await Effect.runPromise(
			decodeReadRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const readResult = await Effect.runPromise(
			decodeReadResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				requirements: [requirement],
				links: [
					{
						issue_id: "te",
						requirement_id: "req-1",
						requirement_local_id: "S1",
						requirement_external_code: "A-1",
						link_type: "implements",
						implementations: ["default"],
						fulfillment_status: "complete",
						fulfillment_percent: 100,
						evidence_note: null,
						created_at: "2026-03-14T06:02:00.000Z",
						updated_at: "2026-03-14T06:03:00.000Z",
					},
				],
				coverage: {
					requirements: [
						{
							...requirement,
							linked_issue_count: 1,
							implemented_issue_count: 1,
						},
					],
					unlinked_requirement_ids: [],
					fully_implemented_requirement_ids: ["req-1"],
					partially_implemented_requirement_ids: [],
					integrity_gaps: [],
				},
			}),
		)
		const lintRequest = await Effect.runPromise(
			decodeLintRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const lintResult = await Effect.runPromise(
			decodeLintResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				lint: {
					ok: true,
					requirement_count: 1,
					linked_requirement_count: 1,
					unlinked_requirement_count: 0,
					integrity_gap_count: 0,
					gap_counts: {
						unlinked_requirement: 0,
						missing_issue: 0,
						missing_requirement: 0,
					},
					report: {
						requirements: [
							{
								...requirement,
								linked_issue_count: 1,
								implemented_issue_count: 1,
							},
						],
						unlinked_requirement_ids: [],
						fully_implemented_requirement_ids: ["req-1"],
						partially_implemented_requirement_ids: [],
						integrity_gaps: [],
					},
				},
			}),
		)
		const parityRequest = await Effect.runPromise(
			decodeParityRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				implementation: "default",
			}),
		)
		const parityResult = await Effect.runPromise(
			decodeParityResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				report: {
					implementation: "default",
					total_requirements: 1,
					implemented_requirement_ids: ["req-1"],
					partially_implemented_requirement_ids: [],
					tested_requirement_ids: ["req-1"],
					uncovered_requirement_ids: [],
					related_only_requirement_ids: [],
					requirements: [
						{
							id: "req-1",
							local_id: "S1",
							external_code: "A-1",
							title: "Track board state",
							implements_issue_ids: ["te"],
							partial_issue_ids: [],
							tests_issue_ids: ["te"],
							other_issue_ids: [],
						},
					],
				},
			}),
		)
		const issueLinksRequest = await Effect.runPromise(
			decodeIssueLinksRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				issueId: "te",
			}),
		)
		const issueLinksResult = await Effect.runPromise(
			decodeIssueLinksResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				linkedRequirements: [
					{
						id: "req-1",
						local_id: "S1",
						external_code: "A-1",
						title: "Track board state",
						kind: "functional",
						link_type: "implements",
						implementations: ["default"],
						fulfillment_status: "complete",
						fulfillment_percent: 100,
						evidence_note: null,
					},
				],
			}),
		)
		const requirementIssuesRequest = await Effect.runPromise(
			decodeRequirementIssuesRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				reference: "S1",
				selector: "local_id",
			}),
		)
		const requirementIssuesResult = await Effect.runPromise(
			decodeRequirementIssuesResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				linkedIssues: [
					{
						id: "te",
						title: "Track board state",
						status: "in_progress",
						issue_type: "task",
						link_type: "implements",
						implementations: ["default"],
						fulfillment_status: "complete",
						fulfillment_percent: 100,
						evidence_note: null,
					},
				],
			}),
		)

		expect(listRequest.query).toBe("board")
		expect(listResult.requirements[0]?.local_id).toBe("S1")
		expect(getRequest.selector).toBe("local_id")
		expect(getResult.requirement?.external_code).toBe("A-1")
		expect(readRequest.projectPath).toBe("/tmp/project")
		expect(readResult.links[0]?.link_type).toBe("implements")
		expect(lintRequest.projectPath).toBe("/tmp/project")
		expect(lintResult.lint.ok).toBe(true)
		expect(parityRequest.implementation).toBe("default")
		expect(parityResult.report.requirements[0]?.implements_issue_ids).toEqual(["te"])
		expect(issueLinksRequest.issueId).toBe("te")
		expect(issueLinksResult.linkedRequirements[0]?.kind).toBe("functional")
		expect(requirementIssuesRequest.reference).toBe("S1")
		expect(requirementIssuesResult.linkedIssues[0]?.status).toBe("in_progress")
	})

	it("validates spec mutation and publish request and result schemas", async () => {
		const decodeCreateRequest = Schema.decodeUnknown(DaemonSpecRequirementCreateRequestSchema)
		const decodeCreateResult = Schema.decodeUnknown(DaemonSpecRequirementCreateResultSchema)
		const decodeUpdateRequest = Schema.decodeUnknown(DaemonSpecRequirementUpdateRequestSchema)
		const decodeUpdateResult = Schema.decodeUnknown(DaemonSpecRequirementUpdateResultSchema)
		const decodeDeleteRequest = Schema.decodeUnknown(DaemonSpecRequirementDeleteRequestSchema)
		const decodeDeleteResult = Schema.decodeUnknown(DaemonSpecRequirementDeleteResultSchema)
		const decodeLinkListRequest = Schema.decodeUnknown(DaemonSpecLinkListRequestSchema)
		const decodeLinkListResult = Schema.decodeUnknown(DaemonSpecLinkListResultSchema)
		const decodeLinkAddRequest = Schema.decodeUnknown(DaemonSpecLinkAddRequestSchema)
		const decodeLinkAddResult = Schema.decodeUnknown(DaemonSpecLinkAddResultSchema)
		const decodeLinkRemoveRequest = Schema.decodeUnknown(DaemonSpecLinkRemoveRequestSchema)
		const decodeLinkRemoveResult = Schema.decodeUnknown(DaemonSpecLinkRemoveResultSchema)
		const decodeLinkUpdateRequest = Schema.decodeUnknown(DaemonSpecLinkUpdateRequestSchema)
		const decodeLinkUpdateResult = Schema.decodeUnknown(DaemonSpecLinkUpdateResultSchema)
		const decodePublishConfigGetRequest = Schema.decodeUnknown(
			DaemonSpecPublishConfigGetRequestSchema,
		)
		const decodePublishConfigGetResult = Schema.decodeUnknown(
			DaemonSpecPublishConfigGetResultSchema,
		)
		const decodePublishConfigSetRequest = Schema.decodeUnknown(
			DaemonSpecPublishConfigSetRequestSchema,
		)
		const decodePublishConfigSetResult = Schema.decodeUnknown(
			DaemonSpecPublishConfigSetResultSchema,
		)
		const decodePublishOutcomeGetRequest = Schema.decodeUnknown(
			DaemonSpecPublishOutcomeGetRequestSchema,
		)
		const decodePublishOutcomeGetResult = Schema.decodeUnknown(
			DaemonSpecPublishOutcomeGetResultSchema,
		)
		const decodeSyncMarkdownRequest = Schema.decodeUnknown(DaemonSpecSyncMarkdownRequestSchema)
		const decodeSyncMarkdownResult = Schema.decodeUnknown(DaemonSpecSyncMarkdownResultSchema)
		const decodePublishRequest = Schema.decodeUnknown(DaemonSpecPublishRequestSchema)
		const decodePublishResult = Schema.decodeUnknown(DaemonSpecPublishResultSchema)
		const requirement = {
			id: "req-2",
			local_id: "S2",
			external_code: "A-2",
			title: "Publish board spec",
			body: "Keep spec artifacts aligned.",
			kind: "functional",
			status: "draft",
			priority: 2,
			created_at: "2026-03-14T06:10:00.000Z",
			updated_at: "2026-03-14T06:11:00.000Z",
		}

		const createRequest = await Effect.runPromise(
			decodeCreateRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				input: {
					local_id: "s2",
					title: "Publish board spec",
					body: "Keep spec artifacts aligned.",
					kind: "functional",
					priority: 2,
				},
			}),
		)
		const createResult = await Effect.runPromise(
			decodeCreateResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				requirement,
			}),
		)
		const updateRequest = await Effect.runPromise(
			decodeUpdateRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				reference: "S2",
				selector: "local_id",
				fields: {
					title: "Publish board spec",
					priority: 3,
				},
			}),
		)
		const updateResult = await Effect.runPromise(
			decodeUpdateResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				updated: true,
			}),
		)
		const deleteRequest = await Effect.runPromise(
			decodeDeleteRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				reference: "S2",
			}),
		)
		const deleteResult = await Effect.runPromise(
			decodeDeleteResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				deleted: true,
			}),
		)
		const linkListRequest = await Effect.runPromise(
			decodeLinkListRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				filters: {
					issueId: "te",
				},
			}),
		)
		const linkListResult = await Effect.runPromise(
			decodeLinkListResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				links: [
					{
						issue_id: "te",
						requirement_id: "req-2",
						requirement_local_id: "S2",
						requirement_external_code: "A-2",
						link_type: "implements",
						implementations: ["default"],
						fulfillment_status: "planned",
						fulfillment_percent: null,
						evidence_note: null,
						created_at: "2026-03-14T06:12:00.000Z",
						updated_at: "2026-03-14T06:12:00.000Z",
					},
				],
			}),
		)
		const linkAddRequest = await Effect.runPromise(
			decodeLinkAddRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				issueId: "te",
				requirementReference: "S2",
				linkType: "implements",
				implementations: ["default"],
				fulfillment: {
					status: "complete",
					percent: 100,
					evidenceNote: "covered by daemon test",
				},
			}),
		)
		const linkAddResult = await Effect.runPromise(
			decodeLinkAddResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				added: true,
			}),
		)
		const linkRemoveRequest = await Effect.runPromise(
			decodeLinkRemoveRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				issueId: "te",
				requirementReference: "S2",
				linkType: "implements",
			}),
		)
		const linkRemoveResult = await Effect.runPromise(
			decodeLinkRemoveResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				removed: 1,
			}),
		)
		const linkUpdateRequest = await Effect.runPromise(
			decodeLinkUpdateRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				issueId: "te",
				requirementReference: "S2",
				linkType: "implements",
				fields: {
					status: "verified",
					percent: 100,
					evidenceNote: "verified",
				},
			}),
		)
		const linkUpdateResult = await Effect.runPromise(
			decodeLinkUpdateResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				updated: 1,
			}),
		)
		const publishConfigGetRequest = await Effect.runPromise(
			decodePublishConfigGetRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const publishConfigGetResult = await Effect.runPromise(
			decodePublishConfigGetResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				config: {
					enabled: true,
					debounce_ms: 2000,
					target_project: "team/spec",
					documents: {
						overview: "Spec Overview",
						requirements: "Requirements Index",
						acceptance: "Acceptance Index",
						change_log: "Change Log",
					},
				},
			}),
		)
		const publishConfigSetRequest = await Effect.runPromise(
			decodePublishConfigSetRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				config: publishConfigGetResult.config,
			}),
		)
		const publishConfigSetResult = await Effect.runPromise(
			decodePublishConfigSetResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				updated: true,
			}),
		)
		const publishOutcomeGetRequest = await Effect.runPromise(
			decodePublishOutcomeGetRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const publishOutcomeGetResult = await Effect.runPromise(
			decodePublishOutcomeGetResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				last_outcome: {
					started_at: "2026-03-14T06:13:00.000Z",
					finished_at: "2026-03-14T06:14:00.000Z",
					status: "success",
					total_requirements: 1,
					total_links: 1,
					outcomes: [
						{
							document_key: "overview",
							title: "Spec Overview",
							status: "success",
							message: "Created document 'Spec Overview'",
							requirement_count: 1,
							link_count: 1,
						},
					],
				},
			}),
		)
		const syncMarkdownRequest = await Effect.runPromise(
			decodeSyncMarkdownRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				outDir: "/tmp/project/docs/spec",
				check: true,
			}),
		)
		const syncMarkdownResult = await Effect.runPromise(
			decodeSyncMarkdownResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				sync: {
					out_dir: "/tmp/project/docs/spec",
					check: true,
					ok: true,
					total_documents: 4,
					changed_documents: 0,
					documents: [
						{
							key: "overview",
							path: "/tmp/project/docs/spec/overview.md",
							status: "unchanged",
							changed: false,
						},
					],
				},
			}),
		)
		const publishRequest = await Effect.runPromise(
			decodePublishRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const publishResult = await Effect.runPromise(
			decodePublishResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				outcome: {
					started_at: "2026-03-14T06:15:00.000Z",
					finished_at: "2026-03-14T06:16:00.000Z",
					status: "partial",
					total_requirements: 1,
					total_links: 1,
					outcomes: [
						{
							document_key: "overview",
							title: "Spec Overview",
							status: "success",
							message: "Created document 'Spec Overview'",
							requirement_count: 1,
							link_count: 1,
						},
					],
				},
			}),
		)

		expect(createRequest.input.local_id).toBe("s2")
		expect(createResult.requirement.title).toBe("Publish board spec")
		expect(updateRequest.fields.priority).toBe(3)
		expect(updateResult.updated).toBe(true)
		expect(deleteRequest.reference).toBe("S2")
		expect(deleteResult.deleted).toBe(true)
		expect(linkListRequest.filters?.issueId).toBe("te")
		expect(linkListResult.links[0]?.link_type).toBe("implements")
		expect(linkAddRequest.fulfillment?.status).toBe("complete")
		expect(linkAddResult.added).toBe(true)
		expect(linkRemoveRequest.linkType).toBe("implements")
		expect(linkRemoveResult.removed).toBe(1)
		expect(linkUpdateRequest.fields.status).toBe("verified")
		expect(linkUpdateResult.updated).toBe(1)
		expect(publishConfigGetRequest.projectPath).toBe("/tmp/project")
		expect(publishConfigGetResult.config.enabled).toBe(true)
		expect(publishConfigSetRequest.config.documents.overview).toBe("Spec Overview")
		expect(publishConfigSetResult.updated).toBe(true)
		expect(publishOutcomeGetRequest.projectPath).toBe("/tmp/project")
		expect(publishOutcomeGetResult.last_outcome?.status).toBe("success")
		expect(syncMarkdownRequest.check).toBe(true)
		expect(syncMarkdownResult.sync.ok).toBe(true)
		expect(publishRequest.projectPath).toBe("/tmp/project")
		expect(publishResult.outcome.status).toBe("partial")
	})

	it("validates daemon health response shape", async () => {
		const decode = Schema.decodeUnknown(DaemonHealthResultSchema)
		const decoded = await Effect.runPromise(
			decode({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				checkedAtMs: 10,
				state: "healthy",
				reason: "daemon runtime is healthy",
				status: {
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					checkedAtMs: 10,
					runtime: {
						protocolVersion: 1,
						runtimePhase: "ready",
						authoritativeRuntime: true,
						revision: 2,
						lifecycleGeneration: 1,
						lifecycleReason: "daemon bootstrap completed",
						recoveryGeneration: 0,
						capturedAtMs: 10,
						clients: {},
					},
					sync: {
						state: "running",
						generation: 1,
						projectPath: "/tmp/project",
						intervalMs: 5000,
						startedAtMs: 9,
						runCount: 1,
						successCount: 1,
						failureCount: 0,
						failureStreak: 0,
						restartStreak: 0,
						lastBackoffMs: null,
						lastSuccessfulRunAtMs: 10,
						lastRun: {
							runAtMs: 10,
							result: "flushed",
							pushed: 1,
							pulled: 1,
							message: null,
						},
						lastError: null,
					},
				},
			}),
		)

		expect(decoded.state).toBe("healthy")
		expect(decoded.status.runtime.runtimePhase).toBe("ready")
	})

	it("validates daemon session snapshot response shape", async () => {
		const decode = Schema.decodeUnknown(DaemonSessionSnapshotResultSchema)
		const decoded = await Effect.runPromise(
			decode({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				capturedAtMs: 123,
				sessions: [
					{
						issueId: "qc",
						worktreePath: "/tmp/project/.worktrees/qc",
						tmuxSessionName: "az-qc",
						state: "busy",
						startedAt: "2026-03-14T06:00:00.000Z",
						projectPath: "/tmp/project",
					},
					{
						issueId: "zz",
						worktreePath: null,
						tmuxSessionName: "az-zz",
						state: "waiting",
						startedAt: null,
						projectPath: "/tmp/project",
					},
				],
			}),
		)

		expect(decoded.sessions).toHaveLength(2)
		expect(decoded.sessions[0]?.issueId).toBe("qc")
		expect(decoded.sessions[0]?.state).toBe("busy")
		expect(decoded.sessions[1]?.startedAt).toBeNull()
	})

	it("validates daemon board read model request and response shape", async () => {
		const decodeRequest = Schema.decodeUnknown(DaemonBoardReadModelRequestSchema)
		const decodeResult = Schema.decodeUnknown(DaemonBoardReadModelResultSchema)
		const request = await Effect.runPromise(
			decodeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
			}),
		)
		const result = await Effect.runPromise(
			decodeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				capturedAtMs: 789,
				projectPath: "/tmp/project",
				tasks: [
					{
						id: "sm-1",
						title: "Daemon board projection task",
						status: "in_progress",
						priority: 1,
						issue_type: "task",
						created_at: "2026-03-14T06:00:00.000Z",
						updated_at: "2026-03-14T06:01:00.000Z",
						implementations: ["ts-opentui"],
						sessionState: "busy",
						hasWorktree: true,
						gitBehindCount: 2,
						hasUncommittedChanges: true,
						gitAdditions: 12,
						gitDeletions: 3,
					},
				],
			}),
		)

		expect(request.projectPath).toBe("/tmp/project")
		expect(result.tasks).toHaveLength(1)
		expect(result.tasks[0]?.id).toBe("sm-1")
		expect(result.tasks[0]?.gitBehindCount).toBe(2)
	})

	it("validates implementation registry request and result schemas", async () => {
		const decodeRequest = Schema.decodeUnknown(DaemonImplementationCreateRequestSchema)
		const decodeResult = Schema.decodeUnknown(DaemonImplementationGetRegistryResultSchema)

		const request = await Effect.runPromise(
			decodeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				projectPath: "/tmp/project",
				input: {
					name: "alpha",
					description: "Alpha implementation",
					directory: "packages/alpha",
					setDefault: true,
				},
			}),
		)
		const result = await Effect.runPromise(
			decodeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				registry: {
					default_implementation: "alpha",
					implicit_default_allowed: false,
					implementations: [
						{
							name: "alpha",
							description: "Alpha implementation",
							directory: "packages/alpha",
							created_at: "2026-03-14T06:00:00.000Z",
							updated_at: "2026-03-14T06:00:00.000Z",
							is_default: true,
							is_builtin: false,
						},
					],
				},
			}),
		)

		expect(request.input.name).toBe("alpha")
		expect(request.input.setDefault).toBe(true)
		expect(result.registry.default_implementation).toBe("alpha")
		expect(result.registry.implementations[0]?.is_default).toBe(true)
	})

	it("validates daemon event stream response shape", async () => {
		const decode = Schema.decodeUnknown(DaemonEventStreamResultSchema)
		const decoded = await Effect.runPromise(
			decode({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				polledAtMs: 456,
				nextCursor: 22,
				events: [
					{
						cursor: 21,
						emittedAtMs: 455,
						event: {
							_tag: "DaemonEventStreamSessionSnapshotEvent",
							capturedAtMs: 455,
							sessions: [],
						},
					},
				],
			}),
		)

		expect(decoded.nextCursor).toBe(22)
		expect(decoded.events[0]?.event._tag).toBe("DaemonEventStreamSessionSnapshotEvent")
	})

	it("validates daemon queue request and result schemas", async () => {
		const decodeEnqueue = Schema.decodeUnknown(DaemonQueueEnqueueRequestSchema)
		const decodeQueryResult = Schema.decodeUnknown(DaemonQueueQueryResultSchema)
		const decodeCancelResult = Schema.decodeUnknown(DaemonQueueCancelResultSchema)

		const enqueue = await Effect.runPromise(
			decodeEnqueue({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				domain: "command",
				operation: "sessionStart",
				projectPath: "/tmp/project",
				issueId: "qm",
				payloadJson: '{"issueId":"qm"}',
			}),
		)
		const query = await Effect.runPromise(
			decodeQueryResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				queriedAtMs: 777,
				items: [
					{
						domain: "mutation",
						operationId: "queue-op-1",
						operation: "applyTaskMutation",
						projectPath: "/tmp/project",
						issueId: "qm",
						dedupeKey: "qm:apply",
						payloadJson: '{"state":"busy"}',
						state: "queued",
						enqueuedAtMs: 770,
						startedAtMs: null,
						finishedAtMs: null,
						error: null,
					},
				],
			}),
		)
		const cancel = await Effect.runPromise(
			decodeCancelResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				cancelledAtMs: 778,
				cancelledOperationIds: ["queue-op-1"],
			}),
		)

		expect(enqueue.operation).toBe("sessionStart")
		expect(query.items[0]?.operationId).toBe("queue-op-1")
		expect(cancel.cancelledOperationIds).toEqual(["queue-op-1"])
	})

	it("validates attachment request and result schemas", async () => {
		const decodeListRequest = Schema.decodeUnknown(DaemonAttachmentListRequestSchema)
		const decodeListResult = Schema.decodeUnknown(DaemonAttachmentListResultSchema)
		const decodeCountRequest = Schema.decodeUnknown(DaemonAttachmentCountBatchRequestSchema)
		const decodeCountResult = Schema.decodeUnknown(DaemonAttachmentCountBatchResultSchema)
		const decodeAttachFileRequest = Schema.decodeUnknown(DaemonAttachmentAttachFileRequestSchema)
		const decodeAttachClipboardRequest = Schema.decodeUnknown(
			DaemonAttachmentAttachClipboardRequestSchema,
		)
		const decodeAttachResult = Schema.decodeUnknown(DaemonAttachmentAttachResultSchema)
		const decodeRemoveRequest = Schema.decodeUnknown(DaemonAttachmentRemoveRequestSchema)
		const decodeRemoveResult = Schema.decodeUnknown(DaemonAttachmentRemoveResultSchema)
		const decodeMaterializeRequest = Schema.decodeUnknown(
			DaemonAttachmentMaterializePathRequestSchema,
		)
		const decodeMaterializeResult = Schema.decodeUnknown(
			DaemonAttachmentMaterializePathResultSchema,
		)

		const listRequest = await Effect.runPromise(
			decodeListRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
			}),
		)
		const listResult = await Effect.runPromise(
			decodeListResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				attachments: [
					{
						id: "att-1",
						filename: "att-1.png",
						originalPath: "/tmp/source.png",
						mimeType: "image/png",
						size: 42,
						createdAt: "2026-03-20T00:00:00.000Z",
					},
				],
			}),
		)
		const countRequest = await Effect.runPromise(
			decodeCountRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueIds: ["te-1", "te-2"],
				projectPath: "/tmp/project",
			}),
		)
		const countResult = await Effect.runPromise(
			decodeCountResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				counts: {
					"te-1": 2,
					"te-2": 0,
				},
			}),
		)
		const attachFileRequest = await Effect.runPromise(
			decodeAttachFileRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				filePath: "/tmp/source.png",
				projectPath: "/tmp/project",
			}),
		)
		const attachClipboardRequest = await Effect.runPromise(
			decodeAttachClipboardRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				base64Content: "aGVsbG8=",
				filename: "clipboard.png",
				mimeType: "image/png",
				projectPath: "/tmp/project",
			}),
		)
		const attachResult = await Effect.runPromise(
			decodeAttachResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				attachment: {
					id: "att-1",
					filename: "att-1.png",
					originalPath: "clipboard",
					mimeType: "image/png",
					size: 42,
					createdAt: "2026-03-20T00:00:00.000Z",
				},
			}),
		)
		const removeRequest = await Effect.runPromise(
			decodeRemoveRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				attachmentId: "att-1",
				projectPath: "/tmp/project",
			}),
		)
		const removeResult = await Effect.runPromise(
			decodeRemoveResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				removed: true,
			}),
		)
		const materializeRequest = await Effect.runPromise(
			decodeMaterializeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				attachmentId: "att-1",
				projectPath: "/tmp/project",
			}),
		)
		const materializeResult = await Effect.runPromise(
			decodeMaterializeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				path: "/tmp/project/.azedarach/tmp/attachments/te-1/att-1/att-1.png",
			}),
		)

		expect(listRequest.issueId).toBe("te-1")
		expect(listResult.attachments[0]?.filename).toBe("att-1.png")
		expect(countRequest.issueIds).toEqual(["te-1", "te-2"])
		expect(countResult.counts["te-1"]).toBe(2)
		expect(attachFileRequest.filePath).toBe("/tmp/source.png")
		expect(attachClipboardRequest.mimeType).toBe("image/png")
		expect(attachResult.attachment.originalPath).toBe("clipboard")
		expect(removeRequest.attachmentId).toBe("att-1")
		expect(removeResult.removed).toBe(true)
		expect(materializeRequest.issueId).toBe("te-1")
		expect(materializeResult.path).toContain(".azedarach/tmp/attachments")
	})

	it("validates planning request and result schemas", async () => {
		const decodeGenerate = Schema.decodeUnknown(DaemonPlanningGenerateRequestSchema)
		const decodeReview = Schema.decodeUnknown(DaemonPlanningReviewRequestSchema)
		const decodeRefine = Schema.decodeUnknown(DaemonPlanningRefineRequestSchema)
		const decodeCreateIssues = Schema.decodeUnknown(DaemonPlanningCreateIssuesRequestSchema)
		const decodeGenerateResult = Schema.decodeUnknown(DaemonPlanningGenerateResultSchema)
		const decodeReviewResult = Schema.decodeUnknown(DaemonPlanningReviewResultSchema)
		const decodeRefineResult = Schema.decodeUnknown(DaemonPlanningRefineResultSchema)
		const decodeCreateIssuesResult = Schema.decodeUnknown(DaemonPlanningCreateIssuesResultSchema)

		const plan: PlanningPlan = {
			epicTitle: "Add oauth login",
			epicDescription: "Deliver an OAuth login flow.",
			summary: "Split the flow into UI, auth, and persistence tasks.",
			tasks: [
				{
					id: "task-1",
					title: "Implement OAuth callback",
					description: "Handle provider callbacks and token exchange.",
					type: "task",
					priority: 1,
					estimate: 2,
					dependsOn: [],
					canParallelize: false,
					design: "Keep callback handling isolated from UI.",
					acceptance: "Callback returns a persisted session.",
				},
			],
			reviewNotes: "Initial pass",
			parallelizationScore: 75,
		}
		const feedback: PlanningReviewFeedback = {
			score: 88,
			issues: ["Task boundaries need to be narrower"],
			suggestions: ["Split UI and auth setup"],
			parallelizationOpportunities: ["Move persistence work into a separate task"],
			tasksTooLarge: ["task-1"],
			missingDependencies: [
				{
					taskId: "task-1",
					shouldDependOn: "task-0",
					reason: "The callback depends on shared token bootstrap.",
				},
			],
			isApproved: false,
		}
		const encodedPlan = Schema.encodeSync(PlanningPlanSchema)(plan)
		const encodedFeedback = Schema.encodeSync(PlanningReviewFeedbackSchema)(feedback)

		const generate = await Effect.runPromise(
			decodeGenerate({
				featureDescription: "Add OAuth login",
			}),
		)
		const review = await Effect.runPromise(decodeReview({ plan }))
		const refine = await Effect.runPromise(decodeRefine({ plan, feedback }))
		const createIssues = await Effect.runPromise(decodeCreateIssues({ plan }))
		const generateResult = await Effect.runPromise(
			decodeGenerateResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				plan,
			}),
		)
		const reviewResult = await Effect.runPromise(
			decodeReviewResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				feedback,
			}),
		)
		const refineResult = await Effect.runPromise(
			decodeRefineResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				plan,
			}),
		)
		const createIssuesResult = await Effect.runPromise(
			decodeCreateIssuesResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				createdIssues: [
					{
						id: "az-1",
						title: "Implement OAuth callback",
						status: "open",
						priority: 1,
						issue_type: "task",
						created_at: "2026-03-20T00:00:00.000Z",
						updated_at: "2026-03-20T00:00:00.000Z",
						closed_at: null,
						assignee: null,
						design: "Keep callback handling isolated from UI.",
						acceptance: "Callback returns a persisted session.",
						implementations: ["ts-opentui"],
					},
				],
			}),
		)

		expect(generate.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(review.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(refine.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(createIssues.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(Schema.decodeUnknownSync(PlanningPlanSchema)(encodedPlan)).toEqual(plan)
		expect(Schema.decodeUnknownSync(PlanningReviewFeedbackSchema)(encodedFeedback)).toEqual(
			feedback,
		)
		expect(generateResult.plan.tasks[0]?.id).toBe("task-1")
		expect(reviewResult.feedback.isApproved).toBe(false)
		expect(refineResult.plan.summary).toContain("tasks")
		expect(createIssuesResult.createdIssues[0]?.id).toBe("az-1")
	})

	it("validates PR request and result schemas", async () => {
		const decodeCreateRequest = Schema.decodeUnknown(DaemonPrCreateRequestSchema)
		const decodeCreateResult = Schema.decodeUnknown(DaemonPrCreateResultSchema)
		const decodeCleanupRequest = Schema.decodeUnknown(DaemonPrCleanupRequestSchema)
		const decodeCleanupResult = Schema.decodeUnknown(DaemonPrCleanupResultSchema)
		const decodeMergeRequest = Schema.decodeUnknown(DaemonPrMergeToMainRequestSchema)
		const decodeMergeResult = Schema.decodeUnknown(DaemonPrMergeToMainResultSchema)
		const decodeCheckRequest = Schema.decodeUnknown(DaemonPrCheckGhCliRequestSchema)
		const decodeCheckResult = Schema.decodeUnknown(DaemonPrCheckGhCliResultSchema)
		const decodeUpdateFromBaseRequest = Schema.decodeUnknown(DaemonPrUpdateFromBaseRequestSchema)
		const decodeUpdateFromBaseResult = Schema.decodeUnknown(DaemonPrUpdateFromBaseResultSchema)
		const decodeMergeBaseIntoBranchRequest = Schema.decodeUnknown(
			DaemonPrMergeBaseIntoBranchRequestSchema,
		)
		const decodeMergeBaseIntoBranchResult = Schema.decodeUnknown(
			DaemonPrMergeBaseIntoBranchResultSchema,
		)
		const decodeAbortMergeRequest = Schema.decodeUnknown(DaemonPrAbortMergeRequestSchema)
		const decodeAbortMergeResult = Schema.decodeUnknown(DaemonPrAbortMergeResultSchema)

		const createRequest = await Effect.runPromise(
			decodeCreateRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
			}),
		)
		const createResult = await Effect.runPromise(
			decodeCreateResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				pullRequest: {
					number: 42,
					url: "https://example.com/pr/42",
					title: "[task] Test PR (te-1)",
					state: "open",
					draft: true,
					branch: "author/te-1/test-pr",
				},
			}),
		)
		const cleanupRequest = await Effect.runPromise(
			decodeCleanupRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
				closeIssue: true,
			}),
		)
		const cleanupResult = await Effect.runPromise(
			decodeCleanupResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				cleanedUp: true,
			}),
		)
		const mergeRequest = await Effect.runPromise(
			decodeMergeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
			}),
		)
		const mergeResult = await Effect.runPromise(
			decodeMergeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				merged: true,
			}),
		)
		const checkRequest = await Effect.runPromise(
			decodeCheckRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
			}),
		)
		const checkResult = await Effect.runPromise(
			decodeCheckResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				available: true,
			}),
		)
		const updateFromBaseRequest = await Effect.runPromise(
			decodeUpdateFromBaseRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
			}),
		)
		const updateFromBaseResult = await Effect.runPromise(
			decodeUpdateFromBaseResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				updated: true,
			}),
		)
		const mergeBaseIntoBranchRequest = await Effect.runPromise(
			decodeMergeBaseIntoBranchRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
			}),
		)
		const mergeBaseIntoBranchResult = await Effect.runPromise(
			decodeMergeBaseIntoBranchResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				merged: true,
			}),
		)
		const abortMergeRequest = await Effect.runPromise(
			decodeAbortMergeRequest({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				issueId: "te-1",
				projectPath: "/tmp/project",
			}),
		)
		const abortMergeResult = await Effect.runPromise(
			decodeAbortMergeResult({
				rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
				aborted: true,
			}),
		)

		expect(createRequest.issueId).toBe("te-1")
		expect(createResult.pullRequest.number).toBe(42)
		expect(cleanupRequest.closeIssue).toBe(true)
		expect(cleanupResult.cleanedUp).toBe(true)
		expect(mergeRequest.projectPath).toBe("/tmp/project")
		expect(mergeResult.merged).toBe(true)
		expect(checkRequest.rpcProtocolVersion).toBe(DAEMON_RPC_PROTOCOL_VERSION)
		expect(checkResult.available).toBe(true)
		expect(updateFromBaseRequest.issueId).toBe("te-1")
		expect(updateFromBaseResult.updated).toBe(true)
		expect(mergeBaseIntoBranchRequest.issueId).toBe("te-1")
		expect(mergeBaseIntoBranchResult.merged).toBe(true)
		expect(abortMergeRequest.issueId).toBe("te-1")
		expect(abortMergeResult.aborted).toBe(true)
	})
})
