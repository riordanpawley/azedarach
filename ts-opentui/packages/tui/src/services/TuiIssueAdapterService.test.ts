import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonIssueUpdatePatch,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer } from "effect"
import type { Issue } from "../contracts.js"
import { TuiIssueAdapterService } from "./TuiIssueAdapterService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.die("unexpected daemon rpc call")

const makeDaemonRpcClientStub = (options?: {
	readonly issueGet?: DaemonRpcClientApi["issueGet"]
	readonly issueUpdate?: DaemonRpcClientApi["issueUpdate"]
	readonly issueAddDependency?: DaemonRpcClientApi["issueAddDependency"]
	readonly issueRemoveDependency?: DaemonRpcClientApi["issueRemoveDependency"]
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
	issueGet: options?.issueGet ?? (() => unexpectedDaemonRpcCall()),
	issueList: () => unexpectedDaemonRpcCall(),
	issueCreate: () => unexpectedDaemonRpcCall(),
	issueUpdate: options?.issueUpdate ?? (() => unexpectedDaemonRpcCall()),
	issueAddDependency: options?.issueAddDependency ?? (() => unexpectedDaemonRpcCall()),
	issueRemoveDependency: options?.issueRemoveDependency ?? (() => unexpectedDaemonRpcCall()),
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
	TuiIssueAdapterService.Default.pipe(
		Layer.provide(Layer.succeed(DaemonRpcClient, daemonRpcClient)),
	)

const issue: Issue = {
	id: "az-epic",
	title: "Epic",
	description: "Epic description",
	status: "open",
	priority: 2,
	issue_type: "epic",
	created_at: "2026-03-20T00:00:00.000Z",
	updated_at: "2026-03-20T00:00:00.000Z",
	closed_at: null,
	assignee: null,
	labels: ["tui"],
	design: "Design",
	notes: "Notes",
	acceptance: "Acceptance",
	estimate: 3,
	implementations: ["ts-opentui"],
	dependent_count: 2,
	dependency_count: 1,
	dependents: [
		{
			id: "az-child-1",
			title: "Child 1",
			status: "open",
			dependency_type: "parent-child",
			issue_type: "task",
		},
		{
			id: "az-child-2",
			title: "Child 2",
			status: "blocked",
			dependency_type: "blocks",
			issue_type: "task",
		},
	],
	dependencies: [
		{
			id: "az-parent",
			title: "Parent",
			status: "open",
			dependency_type: "parent-child",
			issue_type: "epic",
		},
	],
}

describe("TuiIssueAdapterService", () => {
	it("shows, updates, mutates dependencies, and loads epic children through daemon rpc", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* TuiIssueAdapterService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							issueGet: (request) => {
								expect(request.issueId).toBe("az-epic")
								expect(request.projectPath).toBe("/tmp/project")
								expect(request.maxSyncWaitMs).toBe(250)
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									issue,
								})
							},
							issueUpdate: (request) => {
								expect(request.issueId).toBe("az-epic")
								expect(request.projectPath).toBe("/tmp/project")
								expect(request.patch).toEqual({
									title: "Updated epic",
									notes: "Updated notes",
								} satisfies DaemonIssueUpdatePatch)
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									updated: true,
								})
							},
							issueAddDependency: (request) => {
								expect(request.issueId).toBe("az-child-1")
								expect(request.dependsOnId).toBe("az-epic")
								expect(request.dependencyType).toBe("parent-child")
								expect(request.projectPath).toBe("/tmp/project")
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									updated: true,
								})
							},
							issueRemoveDependency: (request) => {
								expect(request.issueId).toBe("az-child-1")
								expect(request.dependsOnId).toBe("az-epic")
								expect(request.dependencyType).toBe("parent-child")
								expect(request.projectPath).toBe("/tmp/project")
								return Effect.succeed({
									rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
									updated: true,
								})
							},
						}),
					),
				),
			),
		)

		const shown = await Effect.runPromise(
			service.show("az-epic", { projectPath: "/tmp/project", maxSyncWaitMs: 250 }),
		)
		expect(shown).toEqual(issue)

		await Effect.runPromise(
			service.update(
				"az-epic",
				{ title: "Updated epic", notes: "Updated notes" },
				{ projectPath: "/tmp/project" },
			),
		)
		await Effect.runPromise(
			service.addDependency("az-child-1", "az-epic", "parent-child", {
				projectPath: "/tmp/project",
			}),
		)
		await Effect.runPromise(
			service.removeDependency("az-child-1", "az-epic", {
				dependencyType: "parent-child",
				projectPath: "/tmp/project",
			}),
		)

		const epicWithChildren = await Effect.runPromise(
			service.getEpicWithChildren("az-epic", {
				projectPath: "/tmp/project",
				maxSyncWaitMs: 250,
			}),
		)
		expect(epicWithChildren.epic).toEqual(issue)
		expect(epicWithChildren.children).toEqual([
			{
				id: "az-child-1",
				title: "Child 1",
				status: "open",
				dependency_type: "parent-child",
				issue_type: "task",
			},
		])

		const epicChildren = await Effect.runPromise(
			service.getEpicChildren("az-epic", {
				projectPath: "/tmp/project",
				maxSyncWaitMs: 250,
			}),
		)
		expect(epicChildren).toEqual([
			{
				id: "az-child-1",
				title: "Child 1",
				status: "open",
				dependency_type: "parent-child",
				issue_type: "task",
			},
		])
	})

	it("wraps daemon rpc failures", async () => {
		const service = await Effect.runPromise(
			Effect.gen(function* () {
				return yield* TuiIssueAdapterService
			}).pipe(
				Effect.provide(
					makeLayer(
						makeDaemonRpcClientStub({
							issueGet: () =>
								Effect.fail({
									_tag: "DaemonRpcActionError",
									action: "issueGet",
									code: "test-failure",
									message: "issue lookup failed",
								} satisfies DaemonRpcClientError),
						}),
					),
				),
			),
		)

		const exit = await Effect.runPromiseExit(service.show("az-epic"))
		expect(exit._tag).toBe("Failure")
	})
})
