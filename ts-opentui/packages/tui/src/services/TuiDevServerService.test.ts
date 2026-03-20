import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import { TuiDevServerService } from "./TuiDevServerService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.die("unexpected daemon rpc call")

const makeDaemonRpcClientStub = (options?: {
	readonly devServerStatus?: DaemonRpcClientApi["devServerStatus"]
	readonly devServerStart?: DaemonRpcClientApi["devServerStart"]
	readonly devServerStop?: DaemonRpcClientApi["devServerStop"]
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
	devServerStatus: options?.devServerStatus,
	devServerList: () => unexpectedDaemonRpcCall(),
	devServerStart: options?.devServerStart,
	devServerStop: options?.devServerStop,
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
	prUpdateFromBase: () => unexpectedDaemonRpcCall(),
	prMergeBaseIntoBranch: () => unexpectedDaemonRpcCall(),
	prAbortMerge: () => unexpectedDaemonRpcCall(),
})

const makeLayer = (daemonRpcClient: DaemonRpcClientApi) =>
	TuiDevServerService.Default.pipe(Layer.provide(Layer.succeed(DaemonRpcClient, daemonRpcClient)))

describe("TuiDevServerService", () => {
	it("reads and toggles dev server state through daemon rpc", async () => {
		const calls: string[] = []
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* TuiDevServerService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							devServerStatus: (request) => {
								calls.push(`status:${request.issueId}:${request.serverName}:${request.projectPath}`)
								const serverName = request.serverName ?? "default"
								return Effect.succeed({
									capturedAtMs: Date.parse("2026-03-20T06:44:00.000Z"),
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									server: {
										issueId: request.issueId,
										serverName,
										projectPath: request.projectPath,
										status: "stopped",
										port: null,
										windowName: null,
										tmuxSession: null,
										worktreePath: null,
										startedAt: null,
										error: null,
									},
								})
							},
							devServerStart: (request) => {
								calls.push(`start:${request.issueId}:${request.serverName}:${request.projectPath}`)
								const serverName = request.serverName ?? "default"
								return Effect.succeed({
									capturedAtMs: Date.parse("2026-03-20T06:45:00.000Z"),
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									server: {
										issueId: request.issueId,
										serverName,
										projectPath: request.projectPath,
										status: "running",
										port: 4321,
										windowName: "dev",
										tmuxSession: "az-123",
										worktreePath: "/tmp/project/.worktrees/az-123",
										startedAt: "2026-03-20T06:45:00.000Z",
										error: null,
									},
								})
							},
						}),
					),
				),
			),
		)

		const current = await Effect.runPromise(service.getStatus("az-123", "web", "/tmp/project"))
		expect(current).toEqual({
			name: "web",
			status: "stopped",
			port: undefined,
			windowName: undefined,
			tmuxSession: undefined,
			worktreePath: undefined,
			startedAt: undefined,
			error: undefined,
		})

		const toggled = await Effect.runPromise(service.toggle("az-123", "/tmp/project", "web"))
		expect(toggled.status).toBe("running")
		expect(toggled.port).toBe(4321)
		expect(calls).toEqual([
			"status:az-123:web:/tmp/project",
			"status:az-123:web:/tmp/project",
			"start:az-123:web:/tmp/project",
		])
	})

	it("surfaces unavailable rpc methods as typed errors", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* TuiDevServerService
			}).pipe(Effect.provide(makeLayer(makeDaemonRpcClientStub()))),
		)

		const error = await Effect.runPromise(
			Effect.flip(service.start("az-123", "/tmp/project", "web")),
		)
		expect(error).toMatchObject({
			_tag: "TuiDevServerServiceError",
			reason: "rpc-unavailable",
			operation: "start",
		})
	})
})
