import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import { PrWorkflowService } from "./PrWorkflowService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.dieMessage("Unexpected daemon rpc call in PrWorkflowService test")

const makeDaemonRpcClientStub = (options?: {
	readonly prCreate?: DaemonRpcClientApi["prCreate"]
	readonly prCleanup?: DaemonRpcClientApi["prCleanup"]
	readonly prMergeToMain?: DaemonRpcClientApi["prMergeToMain"]
	readonly prCheckGhCli?: DaemonRpcClientApi["prCheckGhCli"]
}): DaemonRpcClientApi => ({
	status: () => unexpectedDaemonRpcCall(),
	health: () => unexpectedDaemonRpcCall(),
	logs: () => unexpectedDaemonRpcCall(),
	stop: () => unexpectedDaemonRpcCall(),
	restart: () => unexpectedDaemonRpcCall(),
	attach: () => unexpectedDaemonRpcCall(),
	reconnect: () => unexpectedDaemonRpcCall(),
	heartbeat: () => unexpectedDaemonRpcCall(),
	eventStream: () => unexpectedDaemonRpcCall(),
	sessionSnapshot: () => unexpectedDaemonRpcCall(),
	boardReadModel: () => unexpectedDaemonRpcCall(),
	sessionStart: () => unexpectedDaemonRpcCall(),
	sessionStop: () => unexpectedDaemonRpcCall(),
	sessionPause: () => unexpectedDaemonRpcCall(),
	sessionResume: () => unexpectedDaemonRpcCall(),
	sessionRecover: () => unexpectedDaemonRpcCall(),
	sessionUpdateState: () => unexpectedDaemonRpcCall(),
	devServerStatus: () => unexpectedDaemonRpcCall(),
	devServerList: () => unexpectedDaemonRpcCall(),
	devServerStart: () => unexpectedDaemonRpcCall(),
	devServerStop: () => unexpectedDaemonRpcCall(),
	queueEnqueue: () => unexpectedDaemonRpcCall(),
	queueQuery: () => unexpectedDaemonRpcCall(),
	queueCancel: () => unexpectedDaemonRpcCall(),
	attachmentList: () => unexpectedDaemonRpcCall(),
	attachmentCountBatch: () => unexpectedDaemonRpcCall(),
	attachmentAttachFile: () => unexpectedDaemonRpcCall(),
	attachmentAttachClipboard: () => unexpectedDaemonRpcCall(),
	attachmentRemove: () => unexpectedDaemonRpcCall(),
	attachmentMaterializePath: () => unexpectedDaemonRpcCall(),
	issueGet: () => unexpectedDaemonRpcCall(),
	issueList: () => unexpectedDaemonRpcCall(),
	issueCreate: () => unexpectedDaemonRpcCall(),
	issueUpdate: () => unexpectedDaemonRpcCall(),
	issueAddDependency: () => unexpectedDaemonRpcCall(),
	issueRemoveDependency: () => unexpectedDaemonRpcCall(),
	issueClose: () => unexpectedDaemonRpcCall(),
	issueDelete: () => unexpectedDaemonRpcCall(),
	issueSync: () => unexpectedDaemonRpcCall(),
	implementationGetRegistry: () => unexpectedDaemonRpcCall(),
	implementationCreate: () => unexpectedDaemonRpcCall(),
	implementationUpdate: () => unexpectedDaemonRpcCall(),
	implementationDelete: () => unexpectedDaemonRpcCall(),
	implementationSetDefault: () => unexpectedDaemonRpcCall(),
	planningGenerate: () => unexpectedDaemonRpcCall(),
	planningReview: () => unexpectedDaemonRpcCall(),
	planningRefine: () => unexpectedDaemonRpcCall(),
	planningCreateIssues: () => unexpectedDaemonRpcCall(),
	prCreate: options?.prCreate ?? (() => unexpectedDaemonRpcCall()),
	prCleanup: options?.prCleanup ?? (() => unexpectedDaemonRpcCall()),
	prMergeToMain: options?.prMergeToMain ?? (() => unexpectedDaemonRpcCall()),
	prCheckGhCli: options?.prCheckGhCli ?? (() => unexpectedDaemonRpcCall()),
	specRequirementList: () => unexpectedDaemonRpcCall(),
	specRequirementGet: () => unexpectedDaemonRpcCall(),
	specRequirementCreate: () => unexpectedDaemonRpcCall(),
	specRequirementUpdate: () => unexpectedDaemonRpcCall(),
	specRequirementDelete: () => unexpectedDaemonRpcCall(),
	specRead: () => unexpectedDaemonRpcCall(),
	specLint: () => unexpectedDaemonRpcCall(),
	specParity: () => unexpectedDaemonRpcCall(),
	specIssueLinks: () => unexpectedDaemonRpcCall(),
	specRequirementIssues: () => unexpectedDaemonRpcCall(),
	specLinkList: () => unexpectedDaemonRpcCall(),
	specLinkAdd: () => unexpectedDaemonRpcCall(),
	specLinkRemove: () => unexpectedDaemonRpcCall(),
	specLinkUpdate: () => unexpectedDaemonRpcCall(),
	specPublishConfigGet: () => unexpectedDaemonRpcCall(),
	specPublishConfigSet: () => unexpectedDaemonRpcCall(),
	specPublishOutcomeGet: () => unexpectedDaemonRpcCall(),
	specSyncMarkdown: () => unexpectedDaemonRpcCall(),
	specPublish: () => unexpectedDaemonRpcCall(),
})

const makeLayer = (daemonRpcClient: DaemonRpcClientApi) =>
	PrWorkflowService.Default.pipe(
		Layer.provideMerge(Layer.succeed(DaemonRpcClient, daemonRpcClient)),
	)

describe("PrWorkflowService", () => {
	it("delegates PR mutations to daemon rpc", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			prCreate: ({ issueId, projectPath }) => {
				expect(issueId).toBe("AZ-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					pullRequest: {
						number: 12,
						url: "https://example.com/pr/12",
						title: "[task] Test PR (AZ-1)",
						state: "open",
						draft: true,
						branch: "author/AZ-1/test-pr",
					},
				})
			},
			prCleanup: ({ closeIssue }) => {
				expect(closeIssue).toBe(true)
				return Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					cleanedUp: true,
				})
			},
			prMergeToMain: ({ issueId }) => {
				expect(issueId).toBe("AZ-1")
				return Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					merged: true,
				})
			},
		})

		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const service = yield* PrWorkflowService
				const pullRequest = yield* service.createPR({
					issueId: "AZ-1",
					projectPath: "/tmp/project",
				})
				yield* service.cleanup({
					issueId: "AZ-1",
					projectPath: "/tmp/project",
					closeIssue: true,
				})
				yield* service.mergeToMain({
					issueId: "AZ-1",
					projectPath: "/tmp/project",
				})
				return pullRequest
			}).pipe(Effect.provide(makeLayer(daemonRpcClient))),
		)

		expect(result.url).toBe("https://example.com/pr/12")
		expect(result.draft).toBe(true)
	})

	it("delegates gh availability checks to daemon rpc", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			prCheckGhCli: () =>
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					available: true,
				}),
		})

		const available = await Effect.runPromise(
			Effect.gen(function* () {
				const service = yield* PrWorkflowService
				return yield* service.checkGHCLI()
			}).pipe(Effect.provide(makeLayer(daemonRpcClient))),
		)

		expect(available).toBe(true)
	})
})
