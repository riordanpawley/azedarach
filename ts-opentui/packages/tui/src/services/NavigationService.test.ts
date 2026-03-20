import { describe, expect, it } from "bun:test"
import { AppConfigProjectContext, type AppConfigProjectContextApi } from "@azedarach/config"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	type DaemonBoardReadModelResult,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { Effect, Layer, Stream, SubscriptionRef } from "effect"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { EditorService } from "./EditorService.js"
import { NavigationService } from "./NavigationService.js"
import { TuiBoardStoreService } from "./TuiBoardStoreService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.dieMessage("Unexpected daemon rpc call in NavigationService test")

const makeDaemonRpcClientStub = (options: {
	readonly boardReadModel: DaemonRpcClientApi["boardReadModel"]
	readonly issueList: DaemonRpcClientApi["issueList"]
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
	boardReadModel: options.boardReadModel,
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
	issueList: options.issueList,
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

const makeBoardTask = (
	overrides: Partial<DaemonBoardReadModelResult["tasks"][number]> = {},
): DaemonBoardReadModelResult["tasks"][number] => ({
	id: "AZ-1",
	title: "Task 1",
	status: "open",
	priority: 2,
	issue_type: "task",
	created_at: "2026-01-01T00:00:00.000Z",
	updated_at: "2026-01-01T00:00:00.000Z",
	implementations: [],
	sessionState: "idle",
	...overrides,
})

const projectContext: AppConfigProjectContextApi = {
	getCurrentPath: () => Effect.succeed("/tmp/project"),
	currentProjectPathChanges: Stream.succeed("/tmp/project"),
}

const makeLayer = (daemonRpcClient: DaemonRpcClientApi) => {
	const boardLayer = TuiBoardStoreService.Default.pipe(
		Layer.provideMerge(Layer.succeed(DaemonRpcClient, daemonRpcClient)),
		Layer.provideMerge(Layer.succeed(AppConfigProjectContext, projectContext)),
		Layer.provideMerge(EditorService.Default),
	)

	const navigationLayer = NavigationService.Default.pipe(
		Layer.provideMerge(boardLayer),
		Layer.provideMerge(Layer.succeed(DaemonRpcClient, daemonRpcClient)),
	)

	return Layer.mergeAll(
		DiagnosticsService.Default,
		EditorService.Default,
		boardLayer,
		navigationLayer,
	)
}

describe("NavigationService", () => {
	it("initializes focus from the board store", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: () =>
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					capturedAtMs: 1,
					projectPath: "/tmp/project",
					tasks: [makeBoardTask()],
				}),
			issueList: () =>
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					issues: [],
				}),
		})

		const effect = Effect.gen(function* () {
			const board = yield* TuiBoardStoreService
			const navigation = yield* NavigationService
			yield* board.refresh()
			yield* navigation.initialize()
			return yield* navigation.getFocusedTaskId()
		})

		const focusedTaskId = await Effect.runPromise(
			effect.pipe(Effect.provide(makeLayer(daemonRpcClient))),
		)
		expect(focusedTaskId).toBe("AZ-1")
	})

	it("restores drill-down child details from daemon issue list", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: () =>
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					capturedAtMs: 1,
					projectPath: "/tmp/project",
					tasks: [
						makeBoardTask({
							id: "AZ-EPIC-1",
							title: "Epic",
							issue_type: "epic",
						}),
						makeBoardTask({
							id: "AZ-2",
							title: "Child",
							parentEpicId: "AZ-EPIC-1",
						}),
					],
				}),
			issueList: () =>
				Effect.succeed({
					rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
					issues: [
						{
							id: "AZ-2",
							title: "Child",
							status: "open",
							priority: 2,
							issue_type: "task",
							created_at: "2026-01-01T00:00:00.000Z",
							updated_at: "2026-01-01T00:00:00.000Z",
							implementations: [],
							dependencies: [{ id: "AZ-3", dependency_type: "blocks" }],
						},
					],
				}),
		})

		const effect = Effect.gen(function* () {
			const board = yield* TuiBoardStoreService
			const navigation = yield* NavigationService
			yield* board.refresh()
			yield* navigation.restorePersistedState({
				focusedTaskId: "AZ-2",
				drillDownEpicId: "AZ-EPIC-1",
			})
			return {
				childIds: yield* SubscriptionRef.get(navigation.drillDownChildIds),
				childDetails: yield* SubscriptionRef.get(navigation.drillDownChildDetails),
			}
		})

		const result = await Effect.runPromise(effect.pipe(Effect.provide(makeLayer(daemonRpcClient))))
		expect([...result.childIds]).toEqual(["AZ-2"])
		expect(result.childDetails.get("AZ-2")?.title).toBe("Child")
	})
})
