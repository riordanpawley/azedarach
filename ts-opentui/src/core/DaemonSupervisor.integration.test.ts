import { describe, expect, it } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import type { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Cause, Effect, Exit, Option, Ref } from "effect"
import { BACKEND_DAEMON_PROTOCOL_VERSION, BackendDaemonService } from "./BackendDaemonService.js"
import type {
	BackendSyncDaemonServiceApi,
	BackendSyncDaemonStatus,
} from "./BackendSyncDaemonService.js"
import { makeBackendSyncDaemonService } from "./BackendSyncDaemonService.js"
import type { BackendSyncInterface } from "./BackendSyncInterface.js"
import {
	acquireDaemonSyncInstanceLease,
	DaemonInstanceAlreadyRunningError,
	releaseDaemonSyncInstanceLease,
	resolveDaemonSyncLockPaths,
} from "./DaemonInstanceRegistry.js"
import { IssueSyncError } from "./IssueSyncService.js"

const runWithBunContext = <A, E>(effect: Effect.Effect<A, E, FileSystem.FileSystem | Path.Path>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BunContext.layer)))

const waitForSyncStatus = (
	daemon: BackendSyncDaemonServiceApi,
	predicate: (status: BackendSyncDaemonStatus) => boolean,
	timeoutMs: number,
): Effect.Effect<BackendSyncDaemonStatus, Error> =>
	Effect.gen(function* () {
		const startedAt = Date.now()
		while (true) {
			const status = yield* daemon.getStatus()
			if (predicate(status)) {
				return status
			}
			if (Date.now() - startedAt > timeoutMs) {
				return yield* Effect.fail(
					new Error(
						`Timed out waiting for daemon status; last state=${status.state}, runCount=${String(status.runCount)}`,
					),
				)
			}
			yield* Effect.sleep("20 millis")
		}
	})

describe("Daemon supervisor integration", () => {
	it("covers cli-only, tui-only, and mixed-client convergence on a shared daemon contract", async () => {
		const cliOnly = await Effect.runPromise(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "cli-client",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_000,
				})
				const state = yield* daemon.getState()
				return { attach, state }
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)

		const tuiOnly = await Effect.runPromise(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "tui-client",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_100,
				})
				const state = yield* daemon.getState()
				return { attach, state }
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)

		const mixed = await Effect.runPromise(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const cliAttach = yield* daemon.registerClientAttach({
					clientId: "cli-client",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_200,
				})
				const tuiAttach = yield* daemon.registerClientAttach({
					clientId: "tui-client",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_210,
				})
				const cliReconnect = yield* daemon.markClientReconnect({
					clientId: "cli-client",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					lastSeenRevision: cliAttach.snapshot.revision,
					lastSeenLifecycleGeneration: cliAttach.snapshot.lifecycleGeneration,
					requestedAtMs: 1_220,
				})
				const state = yield* daemon.getState()
				return {
					cliAttach,
					tuiAttach,
					cliReconnect,
					state,
				}
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)

		expect(cliOnly.attach.snapshot.runtimePhase).toBe("ready")
		expect(Object.keys(cliOnly.state.clients)).toEqual(["cli-client"])
		expect(tuiOnly.attach.snapshot.runtimePhase).toBe("ready")
		expect(Object.keys(tuiOnly.state.clients)).toEqual(["tui-client"])

		expect(mixed.cliAttach.snapshot.revision).toBe(1)
		expect(mixed.tuiAttach.snapshot.revision).toBe(2)
		expect(mixed.cliReconnect.snapshot.revision).toBe(3)
		expect(Object.keys(mixed.state.clients).sort()).toEqual(["cli-client", "tui-client"])
		expect(mixed.state.clients["cli-client"]?.lastReconnectAtMs).toBe(1_220)
		expect(mixed.state.clients["tui-client"]?.lastReconnectAtMs).toBeNull()
		expect(mixed.state.runtimePhase).toBe("ready")
	})

	it("enforces singleton lease ownership and allows deterministic reacquire after release", async () => {
		const projectPath = mkdtempSync(join(tmpdir(), "az-daemon-supervisor-singleton-"))

		try {
			const paths = resolveDaemonSyncLockPaths(projectPath, { join })
			mkdirSync(paths.daemonDirectory, { recursive: true })

			const firstLease = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}),
			)

			const secondAttempt = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}).pipe(
					Effect.catchTag("DaemonInstanceAlreadyRunningError", (error) => Effect.succeed(error)),
				),
			)

			expect(secondAttempt instanceof DaemonInstanceAlreadyRunningError).toBe(true)
			if (secondAttempt instanceof DaemonInstanceAlreadyRunningError) {
				expect(secondAttempt.owner.lockId).toBe(firstLease.lockId)
				expect(secondAttempt.owner.pid).toBe(process.pid)
			}

			await runWithBunContext(releaseDaemonSyncInstanceLease(firstLease))

			const reacquired = await runWithBunContext(
				acquireDaemonSyncInstanceLease({
					projectPath,
					endpoint: {
						protocol: "local-sync",
						address: projectPath,
					},
				}),
			)
			expect(reacquired.lockId).not.toBe(firstLease.lockId)
			await runWithBunContext(releaseDaemonSyncInstanceLease(reacquired))
		} finally {
			rmSync(projectPath, { recursive: true, force: true })
		}
	})

	it("covers lifecycle transitions and deterministic crash/backoff/recovery behavior", async () => {
		const callCountRef = await Effect.runPromise(Ref.make(0))
		const backend: BackendSyncInterface = {
			target: "linear",
			bootstrap: () => Effect.succeed(0),
			flushQueue: () =>
				Effect.gen(function* () {
					const callCount = yield* Ref.get(callCountRef)
					const nextCount = callCount + 1
					yield* Ref.set(callCountRef, nextCount)
					if (nextCount <= 4) {
						return yield* Effect.fail(
							new IssueSyncError({
								message: `integration failure ${String(nextCount)}`,
							}),
						)
					}
					return {
						pushed: 1,
						pulled: 1,
					}
				}),
		}

		const result = await Effect.runPromise(
			Effect.scoped(
				Effect.gen(function* () {
					const daemonSvc = yield* makeBackendSyncDaemonService({
						resolve: () => Effect.succeed(backend),
					})

					const lifecycle = yield* BackendDaemonService
					const attached = yield* lifecycle.registerClientAttach({
						clientId: "integration-client",
						protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
						requestedAtMs: 1_000,
					})
					const restarted = yield* lifecycle.markRuntimeRestart(1_100)
					const lifecycleState = yield* lifecycle.getState()

					yield* daemonSvc.start({
						projectPath: "/tmp/integration-supervisor",
						intervalMs: 50,
					})

					const crashed = yield* waitForSyncStatus(
						daemonSvc,
						(status) => status.state === "crashed",
						1_600,
					)
					const recovered = yield* waitForSyncStatus(
						daemonSvc,
						(status) => status.state === "running" && status.successCount >= 1,
						2_400,
					)
					const stopped = yield* daemonSvc.stop()

					return {
						attached,
						restarted,
						lifecycleState,
						crashed,
						recovered,
						stopped,
					}
				}).pipe(Effect.provide(BackendDaemonService.Default)),
			),
		)

		expect(result.attached.snapshot.runtimePhase).toBe("ready")
		expect(result.restarted.lifecycleReason).toBe("recovery succeeded")
		expect(result.lifecycleState.runtimePhase).toBe("ready")
		expect(result.lifecycleState.lifecycleGeneration).toBeGreaterThanOrEqual(3)
		expect(result.crashed.state).toBe("crashed")
		expect(result.crashed.failureStreak).toBeGreaterThanOrEqual(4)
		expect(result.crashed.lastBackoffMs).toBe(200)
		expect(result.recovered.state).toBe("running")
		expect(result.recovered.failureStreak).toBe(0)
		expect(result.recovered.restartStreak).toBe(0)
		expect(result.recovered.lastBackoffMs).toBeNull()
		expect(result.recovered.lastRun?.result).toBe("flushed")
		expect(result.stopped.state).toBe("stopped")
		expect(result.stopped.lastRun?.result).toBe("flushed")
	})

	it("exercises handshake mismatch failure path for attach and reconnect", async () => {
		const attachExit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				yield* daemon.registerClientAttach({
					clientId: "integration-client",
					protocolVersion: 99,
				})
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)
		expect(Exit.isFailure(attachExit)).toBe(true)
		if (!Exit.isFailure(attachExit)) {
			throw new Error("Expected attach mismatch to fail")
		}
		const attachDefect = Cause.dieOption(attachExit.cause)
		expect(Option.isSome(attachDefect)).toBe(true)
		if (!Option.isSome(attachDefect)) {
			throw new Error("Expected attach mismatch defect")
		}
		expect(attachDefect.value).toMatchObject({
			_tag: "BackendDaemonProtocolVersionMismatchError",
			operation: "attach",
			expectedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			receivedProtocolVersion: 99,
		})

		const reconnectExit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				yield* daemon.registerClientAttach({
					clientId: "integration-client",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
				})
				yield* daemon.markClientReconnect({
					clientId: "integration-client",
					protocolVersion: 77,
				})
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)
		expect(Exit.isFailure(reconnectExit)).toBe(true)
		if (!Exit.isFailure(reconnectExit)) {
			throw new Error("Expected reconnect mismatch to fail")
		}
		const reconnectDefect = Cause.dieOption(reconnectExit.cause)
		expect(Option.isSome(reconnectDefect)).toBe(true)
		if (!Option.isSome(reconnectDefect)) {
			throw new Error("Expected reconnect mismatch defect")
		}
		expect(reconnectDefect.value).toMatchObject({
			_tag: "BackendDaemonProtocolVersionMismatchError",
			operation: "reconnect",
			expectedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			receivedProtocolVersion: 77,
		})
	})
})
