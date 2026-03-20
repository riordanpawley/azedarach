import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
	type DaemonSessionSnapshotEntry,
} from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import { TuiSessionAdapterService } from "./TuiSessionAdapterService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.die("unexpected daemon rpc call")

const makeDaemonRpcClientStub = (options?: {
	readonly sessionSnapshot?: DaemonRpcClientApi["sessionSnapshot"]
	readonly sessionStart?: DaemonRpcClientApi["sessionStart"]
	readonly sessionStop?: DaemonRpcClientApi["sessionStop"]
	readonly sessionPause?: DaemonRpcClientApi["sessionPause"]
	readonly sessionResume?: DaemonRpcClientApi["sessionResume"]
	readonly sessionRecover?: DaemonRpcClientApi["sessionRecover"]
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
	sessionSnapshot: options?.sessionSnapshot ?? (() => unexpectedDaemonRpcCall()),
	boardReadModel: () => unexpectedDaemonRpcCall(),
	sessionStart: options?.sessionStart ?? (() => unexpectedDaemonRpcCall()),
	sessionStop: options?.sessionStop ?? (() => unexpectedDaemonRpcCall()),
	sessionPause: options?.sessionPause ?? (() => unexpectedDaemonRpcCall()),
	sessionResume: options?.sessionResume ?? (() => unexpectedDaemonRpcCall()),
	sessionRecover: options?.sessionRecover ?? (() => unexpectedDaemonRpcCall()),
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
	planningGenerate: () => unexpectedDaemonRpcCall(),
	planningReview: () => unexpectedDaemonRpcCall(),
	planningRefine: () => unexpectedDaemonRpcCall(),
	planningCreateIssues: () => unexpectedDaemonRpcCall(),
	prCreate: () => unexpectedDaemonRpcCall(),
	prCleanup: () => unexpectedDaemonRpcCall(),
	prMergeToMain: () => unexpectedDaemonRpcCall(),
	prCheckGhCli: () => unexpectedDaemonRpcCall(),
})

const makeLayer = (daemonRpcClient: DaemonRpcClientApi) =>
	TuiSessionAdapterService.Default.pipe(
		Layer.provide(Layer.succeed(DaemonRpcClient, daemonRpcClient)),
	)

const session: DaemonSessionSnapshotEntry = {
	issueId: "az-task",
	state: "busy",
	projectPath: "/tmp/project",
	tmuxSessionName: "az-task",
	worktreePath: "/tmp/worktree",
	startedAt: "2026-03-20T00:00:00.000Z",
}

describe("TuiSessionAdapterService", () => {
	it("adapts session lifecycle rpc calls and listActive", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* TuiSessionAdapterService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							sessionSnapshot: (request) => {
								expect(request.projectPath).toBe("/tmp/project")
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									capturedAtMs: 1,
									sessions: [session],
								})
							},
							sessionStart: (request) => {
								expect(request).toEqual({
									issueId: "az-task",
									projectPath: "/tmp/project",
									initialPrompt: "Start work",
									imagePaths: ["/tmp/diagram.png"],
									sessionEnv: { FOO: "bar" },
									dangerouslySkipPermissions: true,
								})
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									capturedAtMs: 1,
									session,
								})
							},
							sessionStop: (request) => {
								expect(request).toEqual({
									issueId: "az-task",
									projectPath: "/tmp/project",
								})
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									capturedAtMs: 1,
									session,
								})
							},
							sessionPause: (request) => {
								expect(request).toEqual({
									issueId: "az-task",
									projectPath: "/tmp/project",
								})
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									capturedAtMs: 1,
									session,
								})
							},
							sessionResume: (request) => {
								expect(request).toEqual({
									issueId: "az-task",
									projectPath: "/tmp/project",
								})
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									capturedAtMs: 1,
									session,
								})
							},
							sessionRecover: (request) => {
								expect(request).toEqual({
									issueId: "az-task",
									projectPath: "/tmp/project",
								})
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									capturedAtMs: 1,
									session,
								})
							},
						}),
					),
				),
			),
		)

		const active = await Effect.runPromise(service.listActive({ projectPath: "/tmp/project" }))
		expect(active).toEqual([session])
		expect(
			await Effect.runPromise(
				service.start("az-task", {
					projectPath: "/tmp/project",
					initialPrompt: "Start work",
					imagePaths: ["/tmp/diagram.png"],
					sessionEnv: { FOO: "bar" },
					dangerouslySkipPermissions: true,
				}),
			),
		).toEqual(session)
		expect(
			await Effect.runPromise(service.stop("az-task", { projectPath: "/tmp/project" })),
		).toEqual(session)
		expect(
			await Effect.runPromise(service.pause("az-task", { projectPath: "/tmp/project" })),
		).toEqual(session)
		expect(
			await Effect.runPromise(service.resume("az-task", { projectPath: "/tmp/project" })),
		).toEqual(session)
		expect(
			await Effect.runPromise(service.recover("az-task", { projectPath: "/tmp/project" })),
		).toEqual(session)
	})

	it("wraps daemon rpc failures", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* TuiSessionAdapterService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							sessionSnapshot: () =>
								Effect.fail({
									_tag: "DaemonRpcActionError",
									action: "sessionSnapshot",
									code: "test-failure",
									message: "snapshot failed",
								} satisfies DaemonRpcClientError),
						}),
					),
				),
			),
		)

		const exit = await Effect.runPromiseExit(service.listActive({ projectPath: "/tmp/project" }))
		expect(exit._tag).toBe("Failure")
	})
})
