import { describe, expect, it } from "bun:test"
import { Cause, Effect, Exit, Option } from "effect"
import { BACKEND_DAEMON_PROTOCOL_VERSION, BackendDaemonService } from "./BackendDaemonService.js"

const run = <A, E>(effect: Effect.Effect<A, E, BackendDaemonService>) =>
	Effect.runPromise(effect.pipe(Effect.provide(BackendDaemonService.Default)))

describe("BackendDaemonService", () => {
	it("initializes authoritative runtime state with empty clients", async () => {
		const state = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				return yield* daemon.getState()
			}),
		)

		expect(state.authoritativeRuntime).toBe(true)
		expect(state.protocolVersion).toBe(BACKEND_DAEMON_PROTOCOL_VERSION)
		expect(state.revision).toBe(0)
		expect(state.runtimePhase).toBe("starting")
		expect(state.lifecycleReason).toBe("daemon bootstrapping")
		expect(state.lifecycleGeneration).toBe(0)
		expect(state.recoveryGeneration).toBe(0)
		expect(Object.keys(state.clients)).toHaveLength(0)
	})

	it("tracks attach, heartbeat, and reconnect transitions", async () => {
		const result = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1000,
				})
				const heartbeat = yield* daemon.registerClientHeartbeat("client-a", 1100)
				const reconnect = yield* daemon.markClientReconnect({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					lastSeenRevision: attach.snapshot.revision,
					lastSeenLifecycleGeneration: attach.snapshot.lifecycleGeneration,
					requestedAtMs: 1200,
				})
				const restart = yield* daemon.markRuntimeRestart(1300)
				const state = yield* daemon.getState()
				const snapshot = yield* daemon.snapshot()
				return {
					attach,
					heartbeat,
					reconnect,
					restart,
					state,
					snapshot,
				}
			}),
		)

		expect(result.attach.snapshot.revision).toBe(1)
		expect(result.attach.handshake).toMatchObject({
			operation: "attach",
			requestedAtMs: 1000,
			negotiatedAtMs: 1000,
			requestedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			negotiatedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			compatibilityDecision: "exact-match",
		})
		expect(result.attach.negotiatedCapabilities).toEqual({
			authoritativeRuntime: true,
			lifecycleGenerationTracking: true,
			recoveryGenerationTracking: true,
			resumeToken: true,
		})
		expect(result.heartbeat.lastHeartbeatAtMs).toBe(1100)
		expect(result.reconnect.snapshot.revision).toBe(3)
		expect(result.reconnect.handshake).toMatchObject({
			operation: "reconnect",
			requestedAtMs: 1200,
			negotiatedAtMs: 1200,
			requestedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			negotiatedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			compatibilityDecision: "exact-match",
		})
		expect(result.reconnect.snapshot.runtimePhase).toBe("ready")
		expect(result.reconnect.snapshot.lifecycleReason).toBe("recovery succeeded")
		expect(result.reconnect.snapshot.recoveryGeneration).toBe(1)
		expect(result.restart.revision).toBe(4)
		expect(result.restart.lifecycleGeneration).toBe(5)
		expect(result.restart.lifecycleReason).toBe("recovery succeeded")
		expect(result.state.revision).toBe(4)
		expect(result.state.lifecycleGeneration).toBe(5)
		expect(result.state.recoveryGeneration).toBe(1)
		expect(result.state.clients["client-a"]?.lastReconnectAtMs).toBe(1200)
		expect(result.state.clients["client-a"]?.lastSeenRevision).toBe(1)
		expect(result.state.clients["client-a"]?.lastSeenLifecycleGeneration).toBe(1)
		expect(result.state.clients["client-a"]?.lastRecoveryGeneration).toBe(1)
		expect(result.snapshot.revision).toBe(4)
	})

	it("keeps multiple clients coherent against shared backend revision state", async () => {
		const result = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attachA = yield* daemon.registerClientAttach({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_000,
				})
				const attachB = yield* daemon.registerClientAttach({
					clientId: "client-b",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_010,
				})
				const reconnectA = yield* daemon.markClientReconnect({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					lastSeenRevision: attachA.snapshot.revision,
					lastSeenLifecycleGeneration: attachA.snapshot.lifecycleGeneration,
					requestedAtMs: 1_020,
				})
				const heartbeatB = yield* daemon.registerClientHeartbeat("client-b", 1_030)
				const state = yield* daemon.getState()
				return {
					attachA,
					attachB,
					reconnectA,
					heartbeatB,
					state,
				}
			}),
		)

		expect(result.attachA.snapshot.revision).toBe(1)
		expect(result.attachB.snapshot.revision).toBe(2)
		expect(result.reconnectA.snapshot.revision).toBe(3)
		expect(result.heartbeatB.lastHeartbeatAtMs).toBe(1_030)
		expect(result.state.revision).toBe(4)
		expect(result.state.lifecycleGeneration).toBe(3)
		expect(result.state.runtimePhase).toBe("ready")
		expect(result.state.clients["client-a"]?.lastSeenRevision).toBe(1)
		expect(result.state.clients["client-b"]?.lastHeartbeatAtMs).toBe(1_030)
		expect(Object.keys(result.state.clients).sort()).toEqual(["client-a", "client-b"])
	})

	it("negotiates protocol metadata for attach/reconnect with default client version", async () => {
		const result = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "client-a",
					requestedAtMs: 2_000,
				})
				const reconnect = yield* daemon.markClientReconnect({
					clientId: "client-a",
					requestedAtMs: 2_100,
				})
				return { attach, reconnect }
			}),
		)

		expect(result.attach.handshake).toMatchObject({
			operation: "attach",
			requestedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			negotiatedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			compatibilityDecision: "exact-match",
		})
		expect(result.reconnect.handshake).toMatchObject({
			operation: "reconnect",
			requestedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			negotiatedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			compatibilityDecision: "exact-match",
		})
		expect(result.reconnect.negotiatedCapabilities).toEqual(result.attach.negotiatedCapabilities)
	})

	it("rejects attach/reconnect when protocolVersion mismatches", async () => {
		const attachExit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				yield* daemon.registerClientAttach({
					clientId: "client-a",
					protocolVersion: 99,
				})
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)
		expect(Exit.isFailure(attachExit)).toBe(true)
		if (!Exit.isFailure(attachExit)) {
			throw new Error("Expected attachExit to fail")
		}
		const attachDefect = Cause.dieOption(attachExit.cause)
		expect(Option.isSome(attachDefect)).toBe(true)
		if (!Option.isSome(attachDefect)) {
			throw new Error("Expected attach defect")
		}
		expect(attachDefect.value).toMatchObject({
			_tag: "BackendDaemonProtocolVersionMismatchError",
			operation: "attach",
			compatibilityDecision: "incompatible",
			serverSupportedProtocolVersions: [BACKEND_DAEMON_PROTOCOL_VERSION],
			expectedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			receivedProtocolVersion: 99,
		})

		const reconnectExit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				yield* daemon.registerClientAttach({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
				})
				yield* daemon.markClientReconnect({
					clientId: "client-a",
					protocolVersion: 77,
				})
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)
		expect(Exit.isFailure(reconnectExit)).toBe(true)
		if (!Exit.isFailure(reconnectExit)) {
			throw new Error("Expected reconnectExit to fail")
		}
		const reconnectDefect = Cause.dieOption(reconnectExit.cause)
		expect(Option.isSome(reconnectDefect)).toBe(true)
		if (!Option.isSome(reconnectDefect)) {
			throw new Error("Expected reconnect defect")
		}
		expect(reconnectDefect.value).toMatchObject({
			_tag: "BackendDaemonProtocolVersionMismatchError",
			operation: "reconnect",
			compatibilityDecision: "incompatible",
			serverSupportedProtocolVersions: [BACKEND_DAEMON_PROTOCOL_VERSION],
			expectedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			receivedProtocolVersion: 77,
		})
	})
})
