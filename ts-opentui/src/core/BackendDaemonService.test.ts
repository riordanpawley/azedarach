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
		expect(state.auditEvents).toHaveLength(0)
	})

	it("tracks attach, heartbeat, reconnect, and runtime restart with audit hooks", async () => {
		const result = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 1_000,
				})
				const heartbeat = yield* daemon.registerClientHeartbeat("client-a", 1_100)
				const reconnect = yield* daemon.markClientReconnect({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					lastSeenRevision: attach.snapshot.revision,
					lastSeenLifecycleGeneration: attach.snapshot.lifecycleGeneration,
					requestedAtMs: 1_200,
				})
				const restart = yield* daemon.markRuntimeRestart(1_300)
				const state = yield* daemon.getState()
				const snapshot = yield* daemon.snapshot()
				return { attach, heartbeat, reconnect, restart, state, snapshot }
			}),
		)

		expect(result.attach.snapshot.revision).toBe(1)
		expect(result.attach.handshake).toMatchObject({
			operation: "attach",
			requestedAtMs: 1_000,
			negotiatedAtMs: 1_000,
			requestedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			negotiatedProtocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
			compatibilityDecision: "exact-match",
		})
		expect(result.attach.negotiatedCapabilities).toEqual({
			authoritativeRuntime: true,
			lifecycleGenerationTracking: true,
			recoveryGenerationTracking: true,
			resumeToken: true,
			clientCapabilities: ["session:attach", "session:heartbeat", "session:reconnect"],
		})
		expect(result.heartbeat.lastHeartbeatAtMs).toBe(1_100)
		expect(result.reconnect.snapshot.revision).toBe(3)
		expect(result.reconnect.snapshot.runtimePhase).toBe("ready")
		expect(result.restart.revision).toBe(4)
		expect(result.state.revision).toBe(4)
		const clientA = result.state.clients["client-a"]
		expect(clientA).toBeDefined()
		if (clientA === undefined) {
			throw new Error("Expected client-a in daemon state")
		}
		expect(clientA.lastReconnectAtMs).toBe(1_200)
		expect(clientA.lastSeenRevision).toBe(1)
		expect(clientA.lastSeenLifecycleGeneration).toBe(1)
		expect(clientA.auth).toBeDefined()
		if (clientA.auth === undefined) {
			throw new Error("Expected client-a auth context")
		}
		expect(clientA.auth.actorId).toBe("local-client")
		expect(result.snapshot.revision).toBe(4)
		const snapshotAuditEvents = result.snapshot.auditEvents ?? []
		expect(snapshotAuditEvents.length).toBeGreaterThanOrEqual(4)
		expect(snapshotAuditEvents.at(-1)).toMatchObject({
			operation: "runtime.restart",
			outcome: "allowed",
			capability: "runtime:restart",
		})
	})

	it("keeps multiple clients coherent against shared revision state", async () => {
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
				return { attachA, attachB, reconnectA, heartbeatB, state }
			}),
		)

		expect(result.attachA.snapshot.revision).toBe(1)
		expect(result.attachB.snapshot.revision).toBe(2)
		expect(result.reconnectA.snapshot.revision).toBe(3)
		expect(result.heartbeatB.lastHeartbeatAtMs).toBe(1_030)
		expect(result.state.revision).toBe(4)
		expect(result.state.runtimePhase).toBe("ready")
		const clientA = result.state.clients["client-a"]
		expect(clientA).toBeDefined()
		if (clientA === undefined) {
			throw new Error("Expected client-a in daemon state")
		}
		expect(clientA.lastSeenRevision).toBe(1)
		expect(result.state.clients["client-b"]?.lastHeartbeatAtMs).toBe(1_030)
		expect(clientA.auth).toBeDefined()
		if (clientA.auth === undefined) {
			throw new Error("Expected client-a auth context")
		}
		expect(clientA.auth.capabilities).toEqual([
			"session:attach",
			"session:heartbeat",
			"session:reconnect",
		])
		expect(Object.keys(result.state.clients).sort()).toEqual(["client-a", "client-b"])
	})

	it("keeps reconnect cursor explicit by preserving prior values when reconnect omits them", async () => {
		const result = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 5_000,
				})
				yield* daemon.markClientReconnect({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					lastSeenRevision: attach.snapshot.revision,
					lastSeenLifecycleGeneration: attach.snapshot.lifecycleGeneration,
					requestedAtMs: 5_100,
				})
				const reconnectWithoutCursor = yield* daemon.markClientReconnect({
					clientId: "client-a",
					protocolVersion: BACKEND_DAEMON_PROTOCOL_VERSION,
					requestedAtMs: 5_200,
				})
				const state = yield* daemon.getState()
				return { reconnectWithoutCursor, state }
			}),
		)

		expect(result.reconnectWithoutCursor.handshake.operation).toBe("reconnect")
		expect(result.state.clients["client-a"]?.lastSeenRevision).toBe(1)
		expect(result.state.clients["client-a"]?.lastSeenLifecycleGeneration).toBe(1)
		expect(result.state.clients["client-a"]?.lastReconnectAtMs).toBe(5_200)
		const stateAuditEvents = result.state.auditEvents ?? []
		expect(stateAuditEvents.at(-1)).toMatchObject({
			operation: "client.reconnect",
			outcome: "allowed",
		})
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

	it("denies privileged runtime restart for non-privileged actors and audits denial", async () => {
		const exit = await Effect.runPromiseExit(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				yield* daemon.registerClientAttach({
					clientId: "client-a",
					requestedAtMs: 6_000,
				})
				yield* daemon.markRuntimeRestart(6_100, {
					actorId: "client-a",
					trustLevel: "trusted-local",
					capabilities: ["session:attach", "session:heartbeat", "session:reconnect"],
				})
				return yield* daemon.getState()
			}).pipe(Effect.provide(BackendDaemonService.Default)),
		)

		expect(Exit.isFailure(exit)).toBe(true)
		if (!Exit.isFailure(exit)) {
			throw new Error("Expected runtime restart denial")
		}
		const defect = Cause.dieOption(exit.cause)
		expect(Option.isSome(defect)).toBe(true)
		if (!Option.isSome(defect)) {
			throw new Error("Expected runtime restart denial defect")
		}
		expect(defect.value).toMatchObject({
			_tag: "BackendDaemonAuthorizationError",
			operation: "runtime.restart",
			requiredCapability: "runtime:restart",
		})
	})
})
