import { describe, expect, it } from "bun:test"
import {
	DAEMON_RPC_PROTOCOL_VERSION,
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "@azedarach/shared/rpc"
import type { CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Duration, Effect, Layer } from "effect"
import { buildTaskQueueKey } from "../utils/queueKey.js"
import { CommandQueueService } from "./CommandQueueService.js"

const unexpectedDaemonRpcCall = <A>(): Effect.Effect<A, DaemonRpcClientError> =>
	Effect.dieMessage("Unexpected daemon rpc call in CommandQueueService test")

const makeDaemonRpcClientStub = (options?: {
	readonly queueEnqueue?: DaemonRpcClientApi["queueEnqueue"]
	readonly queueQuery?: DaemonRpcClientApi["queueQuery"]
	readonly queueCancel?: DaemonRpcClientApi["queueCancel"]
}): DaemonRpcClientApi => ({
	status: () => unexpectedDaemonRpcCall(),
	health: () => unexpectedDaemonRpcCall(),
	logs: () => unexpectedDaemonRpcCall(),
	stop: () => unexpectedDaemonRpcCall(),
	restart: () => unexpectedDaemonRpcCall(),
	attach: () => unexpectedDaemonRpcCall(),
	reconnect: () => unexpectedDaemonRpcCall(),
	heartbeat: () => unexpectedDaemonRpcCall(),
	issueGet: () => unexpectedDaemonRpcCall(),
	issueList: () => unexpectedDaemonRpcCall(),
	issueCreate: () => unexpectedDaemonRpcCall(),
	issueUpdate: () => unexpectedDaemonRpcCall(),
	implementationGetRegistry: () => unexpectedDaemonRpcCall(),
	implementationCreate: () => unexpectedDaemonRpcCall(),
	implementationUpdate: () => unexpectedDaemonRpcCall(),
	implementationDelete: () => unexpectedDaemonRpcCall(),
	implementationSetDefault: () => unexpectedDaemonRpcCall(),
	issueAddDependency: () => unexpectedDaemonRpcCall(),
	issueRemoveDependency: () => unexpectedDaemonRpcCall(),
	issueClose: () => unexpectedDaemonRpcCall(),
	issueDelete: () => unexpectedDaemonRpcCall(),
	issueSync: () => unexpectedDaemonRpcCall(),
	queueEnqueue: options?.queueEnqueue,
	queueQuery: options?.queueQuery,
	queueCancel: options?.queueCancel,
	attachmentList: () => unexpectedDaemonRpcCall(),
	attachmentCountBatch: () => unexpectedDaemonRpcCall(),
	attachmentAttachFile: () => unexpectedDaemonRpcCall(),
	attachmentAttachClipboard: () => unexpectedDaemonRpcCall(),
	attachmentRemove: () => unexpectedDaemonRpcCall(),
	attachmentMaterializePath: () => unexpectedDaemonRpcCall(),
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

const runWithQueue = <A, E>(
	program: Effect.Effect<A, E, CommandQueueService | CommandExecutor.CommandExecutor>,
	daemonLayer?: Layer.Layer<DaemonRpcClient, never, never>,
): Promise<A> =>
	Effect.runPromise(
		program.pipe(
			Effect.provide(
				daemonLayer
					? CommandQueueService.Default.pipe(Layer.provideMerge(daemonLayer))
					: CommandQueueService.Default,
			),
			Effect.provide(BunContext.layer),
		),
	)

describe("CommandQueueService daemon adapter", () => {
	it("delegates enqueue/query/cancel to daemon queue RPC when available", async () => {
		const projectPath = "/tmp/daemon-queue-project"
		const taskId = "az-daemon"
		const queueKey = buildTaskQueueKey(taskId, projectPath)
		let enqueueCalls = 0
		let queryCalls = 0
		let cancelCalls = 0
		let operationExecuted = false

		const queueItems: Array<{
			readonly domain: "command"
			readonly operationId: string
			readonly operation: string
			readonly projectPath: string
			readonly issueId: string | null
			readonly dedupeKey: string | null
			readonly payloadJson: string | null
			readonly enqueuedAtMs: number
			readonly startedAtMs: number | null
			readonly finishedAtMs: number | null
			readonly error: string | null
			state: "queued" | "running" | "done" | "failed" | "cancelled"
		}> = []

		const daemonLayer = Layer.succeed(
			DaemonRpcClient,
			makeDaemonRpcClientStub({
				queueEnqueue: (request) =>
					Effect.sync(() => {
						enqueueCalls += 1
						const item: {
							domain: "command"
							operationId: string
							operation: string
							projectPath: string
							issueId: string | null
							dedupeKey: string | null
							payloadJson: string | null
							state: "queued"
							enqueuedAtMs: number
							startedAtMs: number | null
							finishedAtMs: number | null
							error: string | null
						} = {
							domain: "command",
							operationId: `op-${enqueueCalls}`,
							operation: request.operation,
							projectPath: request.projectPath,
							issueId: request.issueId ?? null,
							dedupeKey: request.dedupeKey ?? null,
							payloadJson: request.payloadJson ?? null,
							state: "queued",
							enqueuedAtMs: Date.now(),
							startedAtMs: null,
							finishedAtMs: null,
							error: null,
						}
						queueItems.push(item)
						return {
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							acceptedAtMs: item.enqueuedAtMs,
							item,
						}
					}),
				queueQuery: (request) =>
					Effect.sync(() => {
						queryCalls += 1
						const items = queueItems.filter((item) => {
							if (request.domain !== undefined && item.domain !== request.domain) return false
							if (request.operationId !== undefined && item.operationId !== request.operationId)
								return false
							if (item.projectPath !== request.projectPath) return false
							if (request.issueId !== undefined && item.issueId !== request.issueId) return false
							return true
						})
						const limited = request.limit !== undefined ? items.slice(0, request.limit) : [...items]
						return {
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							queriedAtMs: Date.now(),
							items: limited,
						}
					}),
				queueCancel: (request) =>
					Effect.sync(() => {
						cancelCalls += 1
						const cancelledOperationIds: Array<string> = []
						for (const item of queueItems) {
							if (request.domain !== undefined && item.domain !== request.domain) continue
							if (request.operationId !== undefined && item.operationId !== request.operationId)
								continue
							if (item.projectPath !== request.projectPath) continue
							if (request.issueId !== undefined && item.issueId !== request.issueId) continue
							if (item.state === "queued") {
								item.state = "cancelled"
								cancelledOperationIds.push(item.operationId)
							}
						}
						return {
							rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
							cancelledAtMs: Date.now(),
							cancelledOperationIds,
						}
					}),
			}),
		)

		const result = await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService

				yield* queue.enqueue({
					taskId,
					queueKey,
					label: "merge",
					effect: Effect.sync(() => {
						operationExecuted = true
					}),
				})

				const beforeCancel = yield* queue.getQueueInfo(taskId, queueKey)
				yield* queue.cancelAll(taskId, "test cancel", queueKey)
				const afterCancel = yield* queue.getQueueInfo(taskId, queueKey)
				return { beforeCancel, afterCancel }
			}),
			daemonLayer,
		)

		expect(enqueueCalls).toBe(1)
		expect(queryCalls).toBeGreaterThanOrEqual(2)
		expect(cancelCalls).toBe(1)
		expect(operationExecuted).toBe(false)
		expect(result.beforeCancel.runningLabel).toBeNull()
		expect(result.beforeCancel.queuedCount).toBe(1)
		expect(result.beforeCancel.queuedLabels).toEqual(["merge"])
		expect(result.afterCancel.queuedCount).toBe(0)
	})

	it("falls back to local queue when daemon queue RPC is unavailable", async () => {
		let operationExecuted = false
		const daemonLayer = Layer.succeed(DaemonRpcClient, makeDaemonRpcClientStub())

		const result = await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService
				const taskId = "az-local-fallback"
				const projectPath = "/tmp/local-fallback-project"

				yield* queue.enqueue({
					taskId,
					queueKey: buildTaskQueueKey(taskId, projectPath),
					label: "cleanup",
					effect: Effect.sync(() => {
						operationExecuted = true
					}),
				})

				return yield* queue.getTaskQueueInfo(taskId, projectPath)
			}),
			daemonLayer,
		)

		expect(operationExecuted).toBe(true)
		expect(result.runningLabel).toBeNull()
		expect(result.queuedCount).toBe(0)
		expect(result.queuedLabels).toEqual([])
	})
})

describe("CommandQueueService stale recovery", () => {
	it("recovers stale running commands", async () => {
		const result = await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService
				const taskId = "az-stale"

				yield* Effect.fork(
					queue
						.enqueue({
							taskId,
							label: "merge",
							effect: Effect.sleep(Duration.hours(1)).pipe(Effect.asVoid),
							timeout: Duration.seconds(30),
						})
						.pipe(Effect.catchAll(() => Effect.void)),
				)

				yield* Effect.sleep(Duration.millis(5))
				const before = yield* queue.getQueueInfo(taskId)

				const originalNow = Date.now
				try {
					Date.now = () => originalNow() + Duration.toMillis(Duration.minutes(2))
					const recovered = yield* queue.recoverStaleRunning(taskId, {
						grace: Duration.millis(0),
					})
					const after = yield* queue.getQueueInfo(taskId)
					return { before, recovered, after }
				} finally {
					Date.now = originalNow
				}
			}),
		)

		expect(result.before.runningLabel).toBe("merge")
		expect(result.recovered).toBe(true)
		expect(result.after.runningLabel).toBeNull()
	})
})
