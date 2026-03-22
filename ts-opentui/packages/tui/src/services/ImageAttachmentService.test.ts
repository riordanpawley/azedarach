import { describe, expect, it } from "bun:test"
import { AppConfigProjectContext, type AppConfigProjectContextApi } from "@azedarach/config"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer, Stream, SubscriptionRef } from "effect"
import type { ImageAttachment } from "../contracts.js"
import { ImageAttachmentService } from "./ImageAttachmentService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.dieMessage("Unexpected daemon rpc call in ImageAttachmentService test")

const makeAttachment = (id: string, filename: string): ImageAttachment => ({
	id,
	filename,
	originalPath: `/tmp/${filename}`,
	mimeType: "image/png",
	size: 4,
	createdAt: "2026-03-20T00:00:00.000Z",
})

const projectContext: AppConfigProjectContextApi = {
	getCurrentPath: () => Effect.succeed("/tmp/project"),
	currentProjectPathChanges: Stream.succeed("/tmp/project"),
}

const makeDaemonRpcClientStub = (options?: {
	readonly attachmentList?: DaemonRpcClientApi["attachmentList"]
	readonly attachmentCountBatch?: DaemonRpcClientApi["attachmentCountBatch"]
	readonly attachmentRemove?: DaemonRpcClientApi["attachmentRemove"]
	readonly attachmentMaterializePath?: DaemonRpcClientApi["attachmentMaterializePath"]
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
	attachmentList: options?.attachmentList ?? (() => unexpectedDaemonRpcCall()),
	attachmentCountBatch: options?.attachmentCountBatch ?? (() => unexpectedDaemonRpcCall()),
	attachmentAttachFile: () => unexpectedDaemonRpcCall(),
	attachmentAttachClipboard: () => unexpectedDaemonRpcCall(),
	attachmentRemove: options?.attachmentRemove ?? (() => unexpectedDaemonRpcCall()),
	attachmentMaterializePath:
		options?.attachmentMaterializePath ?? (() => unexpectedDaemonRpcCall()),
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
	prCreate: () => unexpectedDaemonRpcCall(),
	prCleanup: () => unexpectedDaemonRpcCall(),
	prMergeToMain: () => unexpectedDaemonRpcCall(),
	prCheckGhCli: () => unexpectedDaemonRpcCall(),
	prUpdateFromBase: () => unexpectedDaemonRpcCall(),
	prMergeBaseIntoBranch: () => unexpectedDaemonRpcCall(),
	prAbortMerge: () => unexpectedDaemonRpcCall(),
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
	ImageAttachmentService.Default.pipe(
		Layer.provideMerge(Layer.succeed(DaemonRpcClient, daemonRpcClient)),
		Layer.provideMerge(Layer.succeed(AppConfigProjectContext, projectContext)),
	)

describe("ImageAttachmentService", () => {
	it("removes the selected attachment and refreshes local selection state", async () => {
		let attachments: ReadonlyArray<ImageAttachment> = [
			makeAttachment("att-1", "one.png"),
			makeAttachment("att-2", "two.png"),
		]

		const daemonRpcClient = makeDaemonRpcClientStub({
			attachmentList: ({ issueId, projectPath }) => {
				expect(issueId).toBe("AZ-1")
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					attachments,
				})
			},
			attachmentRemove: ({ issueId, attachmentId, projectPath }) =>
				Effect.sync(() => {
					expect(issueId).toBe("AZ-1")
					expect(projectPath).toBe("/tmp/project")
					attachments = attachments.filter((attachment) => attachment.id !== attachmentId)
					return {
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						removed: true as const,
					}
				}),
		})

		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const service = yield* ImageAttachmentService
				yield* service.loadForTask("AZ-1")
				yield* service.selectNextAttachment()
				const removed = yield* service.removeSelectedAttachment()
				const state = yield* SubscriptionRef.get(service.currentAttachments)
				return { removed, state }
			}).pipe(Effect.provide(makeLayer(daemonRpcClient))),
		)

		expect(result.removed.id).toBe("att-1")
		expect(result.state?.attachments.map((attachment) => attachment.id)).toEqual(["att-2"])
		expect(result.state?.selectedIndex).toBe(0)
	})

	it("delegates count and materialized path requests to daemon rpc with project context", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			attachmentCountBatch: ({ issueIds, projectPath }) => {
				expect(issueIds).toEqual(["AZ-1", "AZ-2"])
				expect(projectPath).toBe("/tmp/project")
				return Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					counts: {
						"AZ-1": 2,
						"AZ-2": 0,
					},
				})
			},
			attachmentMaterializePath: ({ issueId, attachmentId, projectPath }) => {
				expect(issueId).toBe("AZ-1")
				expect(attachmentId).toBe("att-1")
				expect(projectPath).toBe("/tmp/project-root")
				return Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					path: "/tmp/project-root/.azedarach/tmp/attachments/AZ-1/att-1/one.png",
				})
			},
		})

		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const service = yield* ImageAttachmentService
				const counts = yield* service.countBatch(["AZ-1", "AZ-2"])
				const path = yield* service.getPathForProjectRoot("AZ-1", "att-1", "/tmp/project-root")
				return { counts, path }
			}).pipe(Effect.provide(makeLayer(daemonRpcClient))),
		)

		expect(result.counts).toEqual({
			"AZ-1": 2,
			"AZ-2": 0,
		})
		expect(result.path).toBe("/tmp/project-root/.azedarach/tmp/attachments/AZ-1/att-1/one.png")
	})

	it("maps daemon rpc failures to image attachment errors", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			attachmentList: () =>
				Effect.fail({
					_tag: "DaemonRpcActionError",
					action: "attachmentList",
					code: "test-failure",
					message: "attachment lookup failed",
				} satisfies DaemonRpcClientError),
		})

		const exit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const service = yield* ImageAttachmentService
				return yield* service.list("AZ-1")
			}).pipe(Effect.provide(makeLayer(daemonRpcClient))),
		)

		expect(exit._tag).toBe("Failure")
		if (exit._tag === "Failure") {
			expect(exit.cause._tag).toBe("Fail")
			if (exit.cause._tag === "Fail") {
				expect(exit.cause.error._tag).toBe("ImageAttachmentError")
				expect(exit.cause.error.message).toBe("attachment lookup failed")
			}
		}
	})
})
