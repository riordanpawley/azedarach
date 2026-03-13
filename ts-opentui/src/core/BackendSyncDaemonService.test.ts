import { describe, expect, it } from "bun:test"
import { Effect, Ref } from "effect"
import {
	type BackendSyncDaemonServiceApi,
	makeBackendSyncDaemonService,
} from "./BackendSyncDaemonService.js"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"

const makeProgram = <A>(
	router: { readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined> },
	effect: (daemon: BackendSyncDaemonServiceApi) => Effect.Effect<A, never>,
): Effect.Effect<A, never> =>
	Effect.scoped(
		Effect.gen(function* () {
			const daemon = yield* makeBackendSyncDaemonService(router)
			return yield* effect(daemon)
		}),
	)

describe("BackendSyncDaemonService", () => {
	it("records skipped runs when no backend runtime is available", async () => {
		const status = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(undefined),
				},
				(daemon) =>
					Effect.gen(function* () {
						yield* daemon.start({ projectPath: "/tmp/project", intervalMs: 50 })
						yield* Effect.sleep("90 millis")
						const current = yield* daemon.getStatus()
						yield* daemon.stop()
						return current
					}),
			),
		)

		expect(status.state).toBe("running")
		expect(status.generation).toBe(1)
		expect(status.projectPath).toBe("/tmp/project")
		expect(status.runCount).toBeGreaterThanOrEqual(1)
		expect(status.successCount).toBe(0)
		expect(status.lastRun?.result).toBe("skipped")
		expect(status.lastRun?.pushed).toBe(0)
		expect(status.lastRun?.pulled).toBe(0)
		expect(status.lastSuccessfulRunAtMs).toBeNull()
	})

	it("runs periodic flushes and stops cleanly", async () => {
		const callCountRef = await Effect.runPromise(Ref.make(0))
		const backend: BackendSyncInterface = {
			target: "linear",
			bootstrap: () => Effect.succeed(0),
			flushQueue: () =>
				Effect.gen(function* () {
					const current = yield* Ref.get(callCountRef)
					yield* Ref.set(callCountRef, current + 1)
					return {
						pushed: current + 1,
						pulled: current + 1,
					}
				}),
		}

		const result = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(backend),
				},
				(daemon) =>
					Effect.gen(function* () {
						yield* daemon.start({ projectPath: "/tmp/project", intervalMs: 50 })
						yield* Effect.sleep("130 millis")
						const runningStatus = yield* daemon.getStatus()
						const callsBeforeStop = yield* Ref.get(callCountRef)
						yield* daemon.stop()
						yield* Effect.sleep("90 millis")
						const callsAfterStop = yield* Ref.get(callCountRef)
						const stoppedStatus = yield* daemon.getStatus()
						return {
							runningStatus,
							stoppedStatus,
							callsBeforeStop,
							callsAfterStop,
						}
					}),
			),
		)

		expect(result.runningStatus.state).toBe("running")
		expect(result.runningStatus.lastRun?.result).toBe("flushed")
		expect(result.runningStatus.successCount).toBeGreaterThanOrEqual(1)
		expect(result.runningStatus.lastSuccessfulRunAtMs).not.toBeNull()
		expect(result.callsBeforeStop).toBeGreaterThanOrEqual(2)
		expect(result.callsAfterStop).toBe(result.callsBeforeStop)
		expect(result.stoppedStatus.state).toBe("stopped")
		expect(result.stoppedStatus.projectPath).toBeNull()
		expect(result.stoppedStatus.intervalMs).toBeNull()
		expect(result.stoppedStatus.startedAtMs).toBeNull()
		expect(result.stoppedStatus.lastRun?.result).toBe("flushed")
	})

	it("keeps start idempotent for identical project and interval", async () => {
		const callCountRef = await Effect.runPromise(Ref.make(0))
		const backend: BackendSyncInterface = {
			target: "linear",
			bootstrap: () => Effect.succeed(0),
			flushQueue: () =>
				Effect.gen(function* () {
					const current = yield* Ref.get(callCountRef)
					yield* Ref.set(callCountRef, current + 1)
					return {
						pushed: 1,
						pulled: 1,
					}
				}),
		}

		const result = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(backend),
				},
				(daemon) =>
					Effect.gen(function* () {
						const first = yield* daemon.start({ projectPath: "/tmp/project", intervalMs: 80 })
						const second = yield* daemon.start({ projectPath: "/tmp/project", intervalMs: 80 })
						yield* Effect.sleep("180 millis")
						const status = yield* daemon.getStatus()
						yield* daemon.stop()
						return {
							first,
							second,
							status,
						}
					}),
			),
		)

		expect(result.first.generation).toBe(1)
		expect(result.second.generation).toBe(1)
		expect(result.second.startedAtMs).toBe(result.first.startedAtMs)
		expect(result.status.generation).toBe(1)
		expect(result.status.runCount).toBeGreaterThanOrEqual(2)
	})

	it("restarts predictably when project path changes and stop is idempotent", async () => {
		const seenProjectsRef = await Effect.runPromise(Ref.make<readonly string[]>([]))
		const backend: BackendSyncInterface = {
			target: "linear",
			bootstrap: () => Effect.succeed(0),
			flushQueue: (projectPath) =>
				Effect.gen(function* () {
					const current = yield* Ref.get(seenProjectsRef)
					yield* Ref.set(seenProjectsRef, [...current, projectPath])
					return {
						pushed: 1,
						pulled: 1,
					}
				}),
		}

		const result = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(backend),
				},
				(daemon) =>
					Effect.gen(function* () {
						yield* daemon.start({ projectPath: "/tmp/alpha", intervalMs: 60 })
						yield* Effect.sleep("130 millis")
						const restarted = yield* daemon.start({
							projectPath: "/tmp/beta",
							intervalMs: 60,
						})
						yield* Effect.sleep("130 millis")
						const running = yield* daemon.getStatus()
						const stoppedOnce = yield* daemon.stop()
						const stoppedTwice = yield* daemon.stop()
						const seenProjects = yield* Ref.get(seenProjectsRef)
						return {
							restarted,
							running,
							stoppedOnce,
							stoppedTwice,
							seenProjects,
						}
					}),
			),
		)

		expect(result.restarted.generation).toBe(2)
		expect(result.running.projectPath).toBe("/tmp/beta")
		expect(result.running.generation).toBe(2)
		expect(result.running.successCount).toBeGreaterThanOrEqual(2)
		expect(result.stoppedOnce.state).toBe("stopped")
		expect(result.stoppedTwice.state).toBe("stopped")
		expect(result.stoppedTwice.generation).toBe(result.stoppedOnce.generation)
		expect(result.stoppedTwice.projectPath).toBeNull()
		expect(result.seenProjects).toContain("/tmp/alpha")
		expect(result.seenProjects).toContain("/tmp/beta")
	})
})
