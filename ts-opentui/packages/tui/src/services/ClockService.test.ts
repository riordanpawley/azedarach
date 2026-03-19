import { describe, expect, it } from "bun:test"
import { DateTime, Effect, SubscriptionRef } from "effect"
import { computeElapsedFormatted } from "../utils/clockHelpers.js"
import { ClockService } from "./ClockService.js"

const runClock = <A>(effect: Effect.Effect<A, never, ClockService>) =>
	Effect.runPromise(effect.pipe(Effect.provide(ClockService.Default)))

describe("ClockService", () => {
	it("provides a current clock tick", async () => {
		const elapsedMs = await runClock(
			Effect.gen(function* () {
				const clock = yield* ClockService
				const observed = yield* SubscriptionRef.get(clock.now)
				const current = yield* DateTime.now
				return DateTime.distance(observed, current)
			}),
		)
		expect(elapsedMs).toBeLessThan(5000)
	})
})

describe("computeElapsedFormatted", () => {
	it("formats elapsed time in the compact elapsed display form", () => {
		expect(
			computeElapsedFormatted(
				"2026-03-19T12:00:00.000Z",
				DateTime.unsafeMake("2026-03-19T12:01:05.000Z"),
			),
		).toBe("1m 5s")
	})
})
