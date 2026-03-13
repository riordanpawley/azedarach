import { describe, expect, it } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Effect, Ref } from "effect"
import {
	type BackendSyncDaemonServiceApi,
	makeBackendSyncDaemonService,
} from "./BackendSyncDaemonService.js"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import { makeDaemonStateStore, resolveDaemonStateStorePaths } from "./DaemonStateStore.js"
import { IssueSyncError } from "./IssueSyncService.js"

const makeProgram = <A>(
	router: { readonly resolve: () => Effect.Effect<BackendSyncInterface | undefined> },
	stateStore: Parameters<typeof makeBackendSyncDaemonService>[1] | undefined,
	effect: (daemon: BackendSyncDaemonServiceApi) => Effect.Effect<A, never>,
): Effect.Effect<A, never> =>
	Effect.scoped(
		Effect.gen(function* () {
			const daemon = yield* makeBackendSyncDaemonService(router, stateStore)
			return yield* effect(daemon)
		}),
	)

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

describe("BackendSyncDaemonService", () => {
	it("records skipped runs when no backend runtime is available", async () => {
		const status = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(undefined),
				},
				undefined,
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
		expect(status.failureStreak).toBe(0)
		expect(status.restartStreak).toBe(0)
		expect(status.lastBackoffMs).toBeNull()
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
				undefined,
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
		expect(result.runningStatus.failureStreak).toBe(0)
		expect(result.runningStatus.restartStreak).toBe(0)
		expect(result.runningStatus.lastBackoffMs).toBeNull()
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
				undefined,
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
		expect(result.status.failureStreak).toBe(0)
		expect(result.status.restartStreak).toBe(0)
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
				undefined,
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
		expect(result.running.state).toBe("running")
		expect(result.running.failureStreak).toBe(0)
		expect(result.running.restartStreak).toBe(0)
		expect(result.stoppedOnce.state).toBe("stopped")
		expect(result.stoppedTwice.state).toBe("stopped")
		expect(result.stoppedTwice.generation).toBe(result.stoppedOnce.generation)
		expect(result.stoppedTwice.projectPath).toBeNull()
		expect(result.seenProjects).toContain("/tmp/alpha")
		expect(result.seenProjects).toContain("/tmp/beta")
	})

	it("applies bounded backoff and escalates from degraded to crashed on repeated failures", async () => {
		const backend: BackendSyncInterface = {
			target: "linear",
			bootstrap: () => Effect.succeed(0),
			flushQueue: () =>
				Effect.fail(
					new IssueSyncError({
						message: "flush exploded",
					}),
				),
		}

		const result = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(backend),
				},
				undefined,
				(daemon) =>
					Effect.gen(function* () {
						yield* daemon.start({ projectPath: "/tmp/project", intervalMs: 50 })
						yield* Effect.sleep("260 millis")
						const degraded = yield* daemon.getStatus()
						yield* Effect.sleep("500 millis")
						const crashed = yield* daemon.getStatus()
						yield* daemon.stop()
						return {
							degraded,
							crashed,
						}
					}),
			),
		)

		expect(result.degraded.state).toBe("degraded")
		expect(result.degraded.failureStreak).toBeGreaterThanOrEqual(2)
		expect(result.degraded.lastBackoffMs).toBeGreaterThanOrEqual(50)
		expect(result.degraded.lastBackoffMs).toBeLessThanOrEqual(200)
		expect(result.crashed.state).toBe("crashed")
		expect(result.crashed.failureStreak).toBeGreaterThanOrEqual(4)
		expect(result.crashed.restartStreak).toBe(result.crashed.failureStreak)
		expect(result.crashed.lastBackoffMs).toBe(200)
		expect(result.crashed.lastError).toContain("flush exploded")
	})

	it("recovers to running with streak reset after a subsequent successful flush", async () => {
		const callCountRef = await Effect.runPromise(Ref.make(0))
		const backend: BackendSyncInterface = {
			target: "linear",
			bootstrap: () => Effect.succeed(0),
			flushQueue: () =>
				Effect.gen(function* () {
					const callCount = yield* Ref.get(callCountRef)
					yield* Ref.set(callCountRef, callCount + 1)
					if (callCount < 3) {
						return yield* Effect.fail(
							new IssueSyncError({
								message: `transient ${callCount + 1}`,
							}),
						)
					}
					return {
						pushed: 1,
						pulled: 1,
					}
				}),
		}

		const recovered = await Effect.runPromise(
			makeProgram(
				{
					resolve: () => Effect.succeed(backend),
				},
				undefined,
				(daemon) =>
					Effect.gen(function* () {
						yield* daemon.start({ projectPath: "/tmp/project", intervalMs: 50 })
						yield* Effect.sleep("900 millis")
						const status = yield* daemon.getStatus()
						yield* daemon.stop()
						return status
					}),
			),
		)

		expect(recovered.state).toBe("running")
		expect(recovered.successCount).toBeGreaterThanOrEqual(1)
		expect(recovered.failureCount).toBeGreaterThanOrEqual(3)
		expect(recovered.failureStreak).toBe(0)
		expect(recovered.restartStreak).toBe(0)
		expect(recovered.lastBackoffMs).toBeNull()
		expect(recovered.lastRun?.result).toBe("flushed")
		expect(recovered.lastError).toBeNull()
		expect(recovered.lastSuccessfulRunAtMs).not.toBeNull()
	})

	it("recovers persisted daemon counters on restart and stays coherent", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-recovery-"))
		try {
			const firstRun = await runWithBunContext(
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const path = yield* Path.Path
					const stateStore = makeDaemonStateStore({ fs, path })
					return yield* makeProgram(
						{
							resolve: () =>
								Effect.succeed({
									target: "linear",
									bootstrap: () => Effect.succeed(0),
									flushQueue: () => Effect.succeed({ pushed: 1, pulled: 1 }),
								}),
						},
						stateStore,
						(daemon) =>
							Effect.gen(function* () {
								yield* daemon.start({ projectPath, intervalMs: 50 })
								yield* Effect.sleep("140 millis")
								const status = yield* daemon.getStatus()
								yield* daemon.stop()
								return status
							}),
					)
				}),
			)

			const secondRun = await runWithBunContext(
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const path = yield* Path.Path
					const stateStore = makeDaemonStateStore({ fs, path })
					return yield* makeProgram(
						{
							resolve: () =>
								Effect.succeed({
									target: "linear",
									bootstrap: () => Effect.succeed(0),
									flushQueue: () => Effect.succeed({ pushed: 1, pulled: 1 }),
								}),
						},
						stateStore,
						(daemon) =>
							Effect.gen(function* () {
								const started = yield* daemon.start({ projectPath, intervalMs: 50 })
								yield* daemon.stop()
								return started
							}),
					)
				}),
			)

			expect(firstRun.runCount).toBeGreaterThanOrEqual(2)
			expect(firstRun.successCount).toBeGreaterThanOrEqual(2)
			expect(secondRun.generation).toBeGreaterThan(firstRun.generation)
			expect(secondRun.runCount).toBeGreaterThanOrEqual(firstRun.runCount)
			expect(secondRun.successCount).toBeGreaterThanOrEqual(firstRun.successCount)
			expect(secondRun.state).toBe("running")
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("ignores corrupted persisted daemon state and starts from fresh baseline", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-corrupt-recovery-"))
		try {
			const status = await runWithBunContext(
				Effect.gen(function* () {
					const fs = yield* FileSystem.FileSystem
					const path = yield* Path.Path
					const stateStore = makeDaemonStateStore({ fs, path })
					const paths = resolveDaemonStateStorePaths(projectPath, path)
					yield* fs.makeDirectory(paths.daemonDirectory, { recursive: true })
					yield* fs.writeFileString(paths.statePath, "{corrupt")
					return yield* makeProgram(
						{
							resolve: () => Effect.succeed(undefined),
						},
						stateStore,
						(daemon) =>
							Effect.gen(function* () {
								const started = yield* daemon.start({ projectPath, intervalMs: 50 })
								yield* daemon.stop()
								return started
							}),
					)
				}),
			)

			expect(status.generation).toBe(1)
			expect(status.runCount).toBe(0)
			expect(status.state).toBe("running")
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})
})
