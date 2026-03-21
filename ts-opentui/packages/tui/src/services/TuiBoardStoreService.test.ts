import { describe, expect, it } from "bun:test"
import { AppConfigProjectContext, type AppConfigProjectContextApi } from "@azedarach/config"
import {
	type DaemonBoardTask,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import { RpcClientError } from "@effect/rpc/RpcClientError"
import { Effect, Layer, type Scope, Stream, SubscriptionRef } from "effect"
import type { Issue } from "../contracts.js"
import { DEFAULT_FILTER_CONFIG, DEFAULT_SORT_CONFIG } from "./EditorService.js"
import { TuiBoardStoreService } from "./TuiBoardStoreService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.dieMessage("Unexpected daemon rpc call in TuiBoardStoreService test")

const makeBoardTask = (overrides: Partial<DaemonBoardTask> = {}): DaemonBoardTask => ({
	id: "az-1",
	title: "Task one",
	status: "open" as const,
	priority: 2,
	issue_type: "task" as const,
	created_at: "2026-01-01T00:00:00.000Z",
	updated_at: "2026-01-01T00:00:00.000Z",
	implementations: [],
	sessionState: "idle" as const,
	...overrides,
})

const makeIssue = (overrides: Partial<Issue> = {}): Issue => ({
	id: "az-2",
	title: "Task two",
	status: "open",
	priority: 2,
	issue_type: "task",
	created_at: "2026-01-01T00:00:00.000Z",
	updated_at: "2026-01-01T00:00:00.000Z",
	implementations: [],
	...overrides,
})

const makeDaemonRpcClientStub = (options?: {
	readonly boardReadModel?: DaemonRpcClientApi["boardReadModel"]
	readonly issueGet?: DaemonRpcClientApi["issueGet"]
	readonly issueList?: DaemonRpcClientApi["issueList"]
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
	boardReadModel: options?.boardReadModel,
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
	issueList: options?.issueList ?? (() => unexpectedDaemonRpcCall()),
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

const makeProjectContext = (projectPath: string): AppConfigProjectContextApi => ({
	getCurrentPath: () => Effect.succeed(projectPath),
	currentProjectPathChanges: Stream.empty,
})

const runWithBoardStore = <A, E>(
	program: Effect.Effect<A, E, TuiBoardStoreService | Scope.Scope>,
	options: {
		readonly daemonRpcClient: DaemonRpcClientApi
		readonly projectContext: AppConfigProjectContextApi
	},
): Promise<A> =>
	Effect.runPromise(
		Effect.scoped(
			program.pipe(
				Effect.provide(
					TuiBoardStoreService.Default.pipe(
						Layer.provideMerge(Layer.succeed(DaemonRpcClient, options.daemonRpcClient)),
						Layer.provideMerge(Layer.succeed(AppConfigProjectContext, options.projectContext)),
					),
				),
			),
		),
	)

describe("TuiBoardStoreService", () => {
	it("retains optimistic locally created tasks across a refresh grace window", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: ({ projectPath: _projectPath }) =>
				Stream.succeed(makeBoardTask({ id: "az-1" })),
		})

		const tasks = await runWithBoardStore(
			Effect.gen(function* () {
				const board = yield* TuiBoardStoreService
				yield* board.upsertIssueFromMutation(makeIssue())
				yield* board.refresh()
				return yield* SubscriptionRef.get(board.tasks)
			}),
			{
				daemonRpcClient,
				projectContext: makeProjectContext("/tmp/project-a"),
			},
		)

		expect(tasks.map((task) => task.id)).toEqual(["az-1", "az-2"])
	})

	it("loads cached project data immediately when switching back to a prior project", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: ({ projectPath }) =>
				Stream.fromIterable(
					projectPath === "/tmp/project-a"
						? [makeBoardTask({ id: "az-a" })]
						: [makeBoardTask({ id: "az-b" })],
				),
		})

		const result = await runWithBoardStore(
			Effect.gen(function* () {
				const board = yield* TuiBoardStoreService
				yield* board.switchToProject("/tmp/project-b", Effect.void)
				yield* board.refresh()
				const switchedBack = yield* board.switchToProject("/tmp/project-a", Effect.void)
				const cachedTasks = yield* SubscriptionRef.get(board.tasks)
				return { switchedBack, cachedTasks }
			}),
			{
				daemonRpcClient,
				projectContext: makeProjectContext("/tmp/project-a"),
			},
		)

		expect(result.switchedBack.cacheHit).toBe(true)
		expect(result.cachedTasks.map((task) => task.id)).toEqual(["az-a"])
	})

	it("exposes keyboard compatibility read/update APIs", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: ({ projectPath: _projectPath }) =>
				Stream.fromIterable([
					makeBoardTask({ id: "az-open", status: "open" }),
					makeBoardTask({ id: "az-blocked", status: "blocked" }),
				]),
		})

		const result = await runWithBoardStore(
			Effect.gen(function* () {
				const board = yield* TuiBoardStoreService
				const beforeMove = yield* board.findTaskById("az-open")
				yield* board.applyOptimisticMove("az-open", "in_progress")
				const afterMove = yield* board.findTaskById("az-open")
				const allTasks = yield* board.getTasks()
				const filtered = yield* board.getFilteredTasksByColumn("", DEFAULT_SORT_CONFIG, {
					...DEFAULT_FILTER_CONFIG,
					status: new Set(["in_progress"]),
				})
				return { beforeMove, afterMove, allTasks, filtered }
			}),
			{
				daemonRpcClient,
				projectContext: makeProjectContext("/tmp/project-a"),
			},
		)

		expect(result.beforeMove?.status).toBe("open")
		expect(result.afterMove?.status).toBe("in_progress")
		expect(result.allTasks.map((task) => task.id)).toEqual(["az-open", "az-blocked"])
		expect(result.filtered[1]?.map((task) => task.id)).toEqual(["az-open"])
		expect(result.filtered[0]?.length).toBe(0)
		expect(result.filtered[2]?.length).toBe(0)
		expect(result.filtered[3]?.length).toBe(0)
	})

	it("hydrates board tasks from streamed daemon chunks", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: () =>
				Stream.concat(
					Stream.succeed(makeBoardTask({ id: "az-stream-1", status: "open" })),
					Stream.succeed(makeBoardTask({ id: "az-stream-2", status: "blocked" })),
				),
		})

		const tasks = await runWithBoardStore(
			Effect.gen(function* () {
				const board = yield* TuiBoardStoreService
				yield* board.refresh()
				return yield* SubscriptionRef.get(board.tasks)
			}),
			{
				daemonRpcClient,
				projectContext: makeProjectContext("/tmp/project-a"),
			},
		)

		expect(tasks.map((task) => task.id)).toEqual(["az-stream-1", "az-stream-2"])
	})

	it("falls back to issue list when board read model fails", async () => {
		const daemonRpcClient = makeDaemonRpcClientStub({
			boardReadModel: () =>
				Stream.fail(
					new RpcClientError({
						reason: "Protocol",
						message: "decode",
					}),
				),
			issueList: () =>
				Effect.succeed({
					rpcProtocolVersion: 2,
					issues: [
						makeIssue({ id: "az-fallback-1", status: "open" }),
						makeIssue({ id: "az-fallback-2", status: "in_progress" }),
					],
				}),
		})

		const tasks = await runWithBoardStore(
			Effect.gen(function* () {
				const board = yield* TuiBoardStoreService
				yield* board.refresh()
				return yield* SubscriptionRef.get(board.tasks)
			}),
			{
				daemonRpcClient,
				projectContext: makeProjectContext("/tmp/project-a"),
			},
		)

		expect(tasks.map((task) => task.id)).toEqual(["az-fallback-1", "az-fallback-2"])
		expect(tasks.map((task) => task.sessionState)).toEqual(["idle", "idle"])
	})
})
