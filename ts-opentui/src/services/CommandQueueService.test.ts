import { describe, expect, it } from "bun:test"
import type { CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Duration, Effect, Layer } from "effect"
import {
	DaemonRpcClient,
	type DaemonRpcClientApi,
	type DaemonRpcClientError,
} from "../rpc/DaemonRpcClient.js"
import { DAEMON_RPC_PROTOCOL_VERSION } from "../rpc/DaemonRpcSchemas.js"
import { buildTaskQueueKey, CommandQueueService } from "./CommandQueueService.js"

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
	queueEnqueue: options?.queueEnqueue,
	queueQuery: options?.queueQuery,
	queueCancel: options?.queueCancel,
})

const runWithQueue = <A, E>(
	program: Effect.Effect<A, E, CommandQueueService | CommandExecutor.CommandExecutor>,
	daemonLayer?: Layer.Layer<DaemonRpcClient, never, never>,
): Promise<A> => {
	const daemonLayerOrDefault =
		daemonLayer ?? Layer.succeed(DaemonRpcClient, makeDaemonRpcClientStub())
	return Effect.runPromise(
		program.pipe(
			Effect.provide(CommandQueueService.Default.pipe(Layer.provideMerge(daemonLayerOrDefault))),
			Effect.provide(BunContext.layer),
		),
	)
}

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

	it("fails closed when daemon queue RPC methods are unavailable", async () => {
		let operationExecuted = false
		const daemonLayer = Layer.succeed(DaemonRpcClient, makeDaemonRpcClientStub())

		await expect(
			runWithQueue(
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
				}),
				daemonLayer,
			),
		).rejects.toBeTruthy()
		expect(operationExecuted).toBe(false)
	})

	it("converges queue visibility across independent clients via daemon query", async () => {
		const projectPath = "/tmp/daemon-queue-converge"
		const taskId = "az-converge"
		const queueKey = buildTaskQueueKey(taskId, projectPath)
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
						const item = {
							domain: "command" as const,
							operationId: `op-${queueItems.length + 1}`,
							operation: request.operation,
							projectPath: request.projectPath,
							issueId: request.issueId ?? null,
							dedupeKey: request.dedupeKey ?? null,
							payloadJson: request.payloadJson ?? null,
							state: "queued" as const,
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
					Effect.sync(() => ({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						queriedAtMs: Date.now(),
						items: queueItems.filter((item) => {
							if (request.domain !== undefined && item.domain !== request.domain) return false
							if (request.projectPath !== item.projectPath) return false
							if (request.issueId !== undefined && item.issueId !== request.issueId) return false
							return true
						}),
					})),
				queueCancel: (_request) =>
					Effect.succeed({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						cancelledAtMs: Date.now(),
						cancelledOperationIds: [],
					}),
			}),
		)

		await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService
				yield* queue.enqueue({
					taskId,
					queueKey,
					label: "sync-a",
					effect: Effect.sync(() => {
						operationExecuted = true
					}),
				})
			}),
			daemonLayer,
		)

		const observedFromSecondClient = await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService
				return yield* queue.getQueueInfo(taskId, queueKey)
			}),
			daemonLayer,
		)

		expect(operationExecuted).toBe(false)
		expect(observedFromSecondClient.queuedCount).toBe(1)
		expect(observedFromSecondClient.queuedLabels).toEqual(["sync-a"])
	})
})

describe("CommandQueueService stale recovery", () => {
	it("returns false when daemon-authoritative queue has no local running command state", async () => {
		const daemonLayer = Layer.succeed(
			DaemonRpcClient,
			makeDaemonRpcClientStub({
				queueEnqueue: (_request) =>
					Effect.succeed({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						acceptedAtMs: Date.now(),
						item: {
							domain: "command",
							operationId: "op-1",
							operation: "merge",
							projectPath: "/tmp/project",
							issueId: "az-stale",
							dedupeKey: "az-stale",
							payloadJson: null,
							state: "queued",
							enqueuedAtMs: Date.now(),
							startedAtMs: null,
							finishedAtMs: null,
							error: null,
						},
					}),
				queueQuery: (_request) =>
					Effect.succeed({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						queriedAtMs: Date.now(),
						items: [],
					}),
				queueCancel: (_request) =>
					Effect.succeed({
						rpcProtocolVersion: DAEMON_RPC_PROTOCOL_VERSION,
						cancelledAtMs: Date.now(),
						cancelledOperationIds: [],
					}),
			}),
		)

		const result = await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService
				const taskId = "az-stale"

				const before = yield* queue.getQueueInfo(taskId)
				const recovered = yield* queue.recoverStaleRunning(taskId, {
					grace: Duration.millis(0),
				})
				const after = yield* queue.getQueueInfo(taskId)
				return { before, recovered, after }
			}),
			daemonLayer,
		)

		expect(result.before.runningLabel).toBeNull()
		expect(result.recovered).toBe(false)
		expect(result.after.runningLabel).toBeNull()
	})
})
