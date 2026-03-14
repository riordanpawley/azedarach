import { describe, expect, it } from "bun:test"
import { Cause, Effect, Exit, Option, Ref } from "effect"
import { makeBackendDaemonControlService } from "./BackendDaemonControlService.js"
import type { BackendDaemonServiceApi, BackendDaemonSnapshot } from "./BackendDaemonService.js"
import type {
	BackendSyncDaemonServiceApi,
	BackendSyncDaemonStatus,
} from "./BackendSyncDaemonService.js"
import { makeDevServerDaemonService } from "./DevServerDaemonService.js"

const makeRuntimeSnapshot = (
	overrides: Partial<BackendDaemonSnapshot> = {},
): BackendDaemonSnapshot => ({
	protocolVersion: 1,
	runtimePhase: "ready",
	authoritativeRuntime: true,
	revision: 1,
	lifecycleGeneration: 1,
	lifecycleReason: "daemon bootstrap completed",
	recoveryGeneration: 0,
	capturedAtMs: 1_000,
	clients: {},
	...overrides,
})

const makeSyncStatus = (
	overrides: Partial<BackendSyncDaemonStatus> = {},
): BackendSyncDaemonStatus => ({
	state: "running",
	generation: 1,
	projectPath: "/tmp/project-a",
	intervalMs: 250,
	startedAtMs: 1_000,
	runCount: 4,
	successCount: 4,
	failureCount: 0,
	failureStreak: 0,
	restartStreak: 0,
	lastBackoffMs: null,
	lastSuccessfulRunAtMs: 1_200,
	lastRun: {
		runAtMs: 1_200,
		result: "flushed",
		pushed: 1,
		pulled: 1,
		message: null,
	},
	lastError: null,
	...overrides,
})

const makeRuntimeApi = (
	snapshotRef: Ref.Ref<BackendDaemonSnapshot>,
	restartCountRef: Ref.Ref<number>,
): BackendDaemonServiceApi => ({
	getState: () => Effect.die(new Error("getState not used by BackendDaemonControlService")),
	snapshot: () => Ref.get(snapshotRef),
	registerClientAttach: () =>
		Effect.die(new Error("registerClientAttach not used by BackendDaemonControlService")),
	registerClientHeartbeat: () =>
		Effect.die(new Error("registerClientHeartbeat not used by BackendDaemonControlService")),
	markClientReconnect: () =>
		Effect.die(new Error("markClientReconnect not used by BackendDaemonControlService")),
	markRuntimeRestart: (observedAtMs?: number) =>
		Effect.gen(function* () {
			yield* Ref.update(restartCountRef, (count) => count + 1)
			const previous = yield* Ref.get(snapshotRef)
			const next: BackendDaemonSnapshot = {
				...previous,
				runtimePhase: "ready",
				revision: previous.revision + 1,
				lifecycleGeneration: previous.lifecycleGeneration + 1,
				lifecycleReason: "restart requested",
				capturedAtMs: observedAtMs ?? previous.capturedAtMs,
			}
			yield* Ref.set(snapshotRef, next)
			return next
		}),
})

const makeSyncApi = (
	statusRef: Ref.Ref<BackendSyncDaemonStatus>,
	startedWithRef: Ref.Ref<
		ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
	>,
	stopCountRef: Ref.Ref<number>,
): BackendSyncDaemonServiceApi => ({
	getStatus: () => Ref.get(statusRef),
	start: (options) =>
		Effect.gen(function* () {
			yield* Ref.update(startedWithRef, (items) => [...items, options])
			const previous = yield* Ref.get(statusRef)
			const next = makeSyncStatus({
				...previous,
				state: "running",
				generation: previous.generation + 1,
				projectPath: options.projectPath,
				intervalMs: options.intervalMs ?? previous.intervalMs ?? 5_000,
			})
			yield* Ref.set(statusRef, next)
			return next
		}),
	stop: () =>
		Effect.gen(function* () {
			yield* Ref.update(stopCountRef, (count) => count + 1)
			const previous = yield* Ref.get(statusRef)
			const next = makeSyncStatus({
				...previous,
				state: "stopped",
				projectPath: null,
				intervalMs: null,
				startedAtMs: null,
			})
			yield* Ref.set(statusRef, next)
			return next
		}),
})

describe("BackendDaemonControlService", () => {
	it("returns aggregated daemon status and health", async () => {
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot())
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(makeSyncStatus())
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => 2_000,
					portBase: 3_000,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})
				const status = yield* service.status()
				const health = yield* service.health()
				return { status, health }
			}),
		)

		expect(result.status.runtime.runtimePhase).toBe("ready")
		expect(result.status.sync.state).toBe("running")
		expect(result.health.state).toBe("healthy")
		expect(result.health.reason).toBe("daemon runtime is healthy")
	})

	it("returns degraded health when sync/runtime are not fully ready", async () => {
		const health = await Effect.runPromise(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot({ runtimePhase: "recovering" }))
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(makeSyncStatus({ state: "degraded" }))
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => 2_000,
					portBase: 3_000,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})
				return yield* service.health()
			}),
		)

		expect(health.state).toBe("degraded")
		expect(health.reason).toBe("daemon runtime is degraded")
	})

	it("restart fails with typed error when no project path is available", async () => {
		const exit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot())
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(
					makeSyncStatus({
						state: "stopped",
						projectPath: null,
						intervalMs: null,
						startedAtMs: null,
					}),
				)
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => 2_000,
					portBase: 3_000,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})
				yield* service.restart({})
			}),
		)

		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected restart failure")
		}
		const failure = Cause.failureOption(exit.cause)
		expect(Option.isSome(failure)).toBe(true)
		if (!Option.isSome(failure)) {
			throw new Error("Expected typed restart failure")
		}
		expect(failure.value).toMatchObject({
			_tag: "BackendDaemonControlRestartConfigurationError",
			reason: "missing-project-path",
			daemonSyncState: "stopped",
		})
	})

	it("restart reuses current sync config and performs stop/start + runtime restart", async () => {
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot())
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(makeSyncStatus({ intervalMs: 600 }))
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => 2_000,
					portBase: 3_000,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})
				const status = yield* service.restart({})
				const startedWith = yield* Ref.get(startedWithRef)
				const stopCount = yield* Ref.get(stopCountRef)
				const restartCount = yield* Ref.get(restartCountRef)
				return { status, startedWith, stopCount, restartCount }
			}),
		)

		expect(result.stopCount).toBe(1)
		expect(result.restartCount).toBe(1)
		expect(result.startedWith).toEqual([{ projectPath: "/tmp/project-a", intervalMs: 600 }])
		expect(result.status.sync.state).toBe("running")
		expect(result.status.runtime.lifecycleReason).toBe("restart requested")
	})

	it("stop transitions sync daemon to stopped and returns status", async () => {
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot())
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(makeSyncStatus())
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => 2_000,
					portBase: 3_000,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})
				const stopped = yield* service.stop()
				const stopCount = yield* Ref.get(stopCountRef)
				return { stopped, stopCount }
			}),
		)

		expect(result.stopCount).toBe(1)
		expect(result.stopped.sync.state).toBe("stopped")
		expect(result.stopped.sync.projectPath).toBeNull()
	})

	it("provides queue scaffold enqueue/query/cancel behavior", async () => {
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot())
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(makeSyncStatus())
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => 2_000,
					portBase: 3_000,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})

				const first = yield* service.queueEnqueue({
					domain: "command",
					operation: "sessionStart",
					projectPath: "/tmp/project-a",
					issueId: "qm-a",
					dedupeKey: "qm-a:start",
				})
				const second = yield* service.queueEnqueue({
					domain: "mutation",
					operation: "taskUpdate",
					projectPath: "/tmp/project-a",
					issueId: "qm-b",
				})
				const all = yield* service.queueQuery()
				const commandOnly = yield* service.queueQuery({ domain: "command" })
				const cancelled = yield* service.queueCancel({
					operationId: first.item.operationId,
				})
				const afterCancel = yield* service.queueQuery({ operationId: first.item.operationId })
				const limited = yield* service.queueQuery({ limit: 1 })

				return { first, second, all, commandOnly, cancelled, afterCancel, limited }
			}),
		)

		expect(result.first.item.state).toBe("queued")
		expect(result.second.item.domain).toBe("mutation")
		expect(result.all.items).toHaveLength(2)
		expect(result.commandOnly.items.map((item) => item.domain)).toEqual(["command"])
		expect(result.cancelled.cancelledOperationIds).toEqual([result.first.item.operationId])
		expect(result.afterCancel.items[0]?.state).toBe("cancelled")
		expect(result.limited.items).toHaveLength(1)
	})

	it("routes devserver authority hooks through daemon control service", async () => {
		const result = await Effect.runPromise(
			Effect.gen(function* () {
				const snapshotRef = yield* Ref.make(makeRuntimeSnapshot())
				const restartCountRef = yield* Ref.make(0)
				const statusRef = yield* Ref.make(makeSyncStatus())
				const startedWithRef = yield* Ref.make<
					ReadonlyArray<{ readonly projectPath: string; readonly intervalMs?: number }>
				>([])
				const stopCountRef = yield* Ref.make(0)
				const nowValues = [3_000, 3_100, 3_200, 3_300, 3_400]
				const devServer = yield* makeDevServerDaemonService({
					nowMs: () => nowValues.shift() ?? 3_400,
					portBase: 3_600,
				})
				const service = makeBackendDaemonControlService({
					runtime: makeRuntimeApi(snapshotRef, restartCountRef),
					sync: makeSyncApi(statusRef, startedWithRef, stopCountRef),
					devServer,
				})

				const initial = yield* service.devServerStatus({ issueId: "qp" })
				const started = yield* service.devServerStart({
					issueId: "qp",
					projectPath: "/tmp/project-a",
				})
				const listed = yield* service.devServerList({ issueId: "qp" })
				const stopped = yield* service.devServerStop({ issueId: "qp" })
				const afterStop = yield* service.devServerStatus({ issueId: "qp" })
				return { initial, started, listed, stopped, afterStop }
			}),
		)

		expect(result.initial.server.status).toBe("idle")
		expect(result.started.server.status).toBe("running")
		expect(result.started.server.port).toBe(3_600)
		expect(result.listed.servers).toHaveLength(1)
		expect(result.listed.servers[0]?.tmuxSession).toBe("az-qp")
		expect(result.stopped.server.status).toBe("stopped")
		expect(result.afterStop.server.status).toBe("stopped")
		expect(result.afterStop.server.projectPath).toBe("/tmp/project-a")
	})
})
