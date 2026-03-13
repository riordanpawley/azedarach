import { describe, expect, it } from "bun:test"
import { Effect } from "effect"
import { BackendDaemonService } from "./BackendDaemonService.js"

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
		expect(state.protocolVersion).toBe(1)
		expect(state.revision).toBe(0)
		expect(state.runtimePhase).toBe("starting")
		expect(Object.keys(state.clients)).toHaveLength(0)
	})

	it("tracks attach, heartbeat, and reconnect transitions", async () => {
		const result = await run(
			Effect.gen(function* () {
				const daemon = yield* BackendDaemonService
				const attach = yield* daemon.registerClientAttach({
					clientId: "client-a",
					requestedAtMs: 1000,
				})
				const heartbeat = yield* daemon.registerClientHeartbeat("client-a", 1100)
				const reconnect = yield* daemon.markClientReconnect({
					clientId: "client-a",
					lastSeenRevision: attach.snapshot.revision,
					requestedAtMs: 1200,
				})
				const state = yield* daemon.getState()
				const snapshot = yield* daemon.snapshot()
				return {
					attach,
					heartbeat,
					reconnect,
					state,
					snapshot,
				}
			}),
		)

		expect(result.attach.snapshot.revision).toBe(1)
		expect(result.heartbeat.lastHeartbeatAtMs).toBe(1100)
		expect(result.reconnect.snapshot.revision).toBe(3)
		expect(result.reconnect.snapshot.runtimePhase).toBe("running")
		expect(result.state.revision).toBe(3)
		expect(result.state.clients["client-a"]?.lastReconnectAtMs).toBe(1200)
		expect(result.state.clients["client-a"]?.lastSeenRevision).toBe(1)
		expect(result.snapshot.revision).toBe(3)
	})
})
