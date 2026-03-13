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
		expect(status.projectPath).toBe("/tmp/project")
		expect(status.lastRun?.result).toBe("skipped")
		expect(status.lastRun?.pushed).toBe(0)
		expect(status.lastRun?.pulled).toBe(0)
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
		expect(result.callsBeforeStop).toBeGreaterThanOrEqual(2)
		expect(result.callsAfterStop).toBe(result.callsBeforeStop)
		expect(result.stoppedStatus.state).toBe("stopped")
		expect(result.stoppedStatus.lastRun?.result).toBe("flushed")
	})
})
