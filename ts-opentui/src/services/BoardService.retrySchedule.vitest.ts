import { describe, expect, it } from "@effect/vitest"
import { Data, Effect, Fiber, Ref, TestClock } from "effect"
import { makeSessionRecoveryRetrySchedule } from "./sessionRecoveryRetrySchedule.js"

class TransientFailure extends Data.TaggedError("TransientFailure")<{
	readonly message: string
}> {}

const makeAlwaysFailingTransientRecovery = (
	attemptsRef: Ref.Ref<number>,
	baseDelayMs: number,
	maxDelayMs: number,
) =>
	Ref.updateAndGet(attemptsRef, (attempts) => attempts + 1).pipe(
		Effect.flatMap(() =>
			Effect.fail(
				new TransientFailure({
					message: "transient test failure",
				}),
			),
		),
		Effect.retry({
			schedule: makeSessionRecoveryRetrySchedule(baseDelayMs, maxDelayMs),
			while: () => true,
		}),
		// Deterministic jitter multiplier (max side of jitter range).
		Effect.withRandomFixed([1]),
	)

describe("BoardService recovery retry schedule (Effect + TestClock)", () => {
	it.effect("retries transient failures on virtual time", () =>
		Effect.gen(function* () {
			const attemptsRef = yield* Ref.make(0)
			const fiber = yield* Effect.fork(makeAlwaysFailingTransientRecovery(attemptsRef, 1000, 5000))

			yield* Effect.yieldNow()
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			// First jittered retry delay with Random=1 is 1200ms.
			yield* TestClock.adjust("1199 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			yield* TestClock.adjust("1 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(2)

			// Second jittered retry delay is 2400ms.
			yield* TestClock.adjust("2400 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(3)

			yield* Fiber.interrupt(fiber)
		}),
	)

	it.effect("caps retry wait duration at maxDelayMs", () =>
		Effect.gen(function* () {
			const attemptsRef = yield* Ref.make(0)
			const fiber = yield* Effect.fork(makeAlwaysFailingTransientRecovery(attemptsRef, 1000, 3000))

			yield* Effect.yieldNow()
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			// 1200ms then 2400ms for first two retries.
			yield* TestClock.adjust("1200 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(2)

			yield* TestClock.adjust("2400 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(3)

			// Next exponential step would be 4800ms jittered to 5760ms, but cap is 3000ms.
			yield* TestClock.adjust("2999 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(3)

			yield* TestClock.adjust("1 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(4)

			yield* TestClock.adjust("3000 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(5)

			yield* Fiber.interrupt(fiber)
		}),
	)
})
