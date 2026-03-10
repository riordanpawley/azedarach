import { describe, expect, it } from "@effect/vitest"
import { Data, Effect, Fiber, Ref, TestClock } from "effect"
import { makeTransientRetrySchedule } from "./transientRetrySchedule.js"

class TransientFailure extends Data.TaggedError("TransientFailure")<{
	readonly message: string
}> {}

class TerminalFailure extends Data.TaggedError("TerminalFailure")<{
	readonly message: string
}> {}

type RetryFailure = TransientFailure | TerminalFailure

const isRetryableFailure = (error: RetryFailure): boolean => error._tag === "TransientFailure"

const makeAlwaysFailingEffect = (attemptsRef: Ref.Ref<number>, failure: RetryFailure) =>
	Ref.updateAndGet(attemptsRef, (attempts) => attempts + 1).pipe(
		Effect.flatMap(() => Effect.fail(failure)),
	)

const makeRetryingFailure = (
	attemptsRef: Ref.Ref<number>,
	failure: RetryFailure,
	retryMaxDelayMs: number,
	retryMaxAttempts = 4,
) =>
	makeAlwaysFailingEffect(attemptsRef, failure).pipe(
		Effect.retry({
			schedule: makeTransientRetrySchedule({
				retryBaseDelayMs: 1000,
				retryMaxDelayMs,
				retryMaxAttempts,
				while: isRetryableFailure,
			}),
		}),
		// Deterministic jitter multiplier (max side of jitter range).
		Effect.withRandomFixed([1]),
	)

describe("transient retry schedule (Effect + TestClock)", () => {
	it.effect("retries transient failures on virtual time", () =>
		Effect.gen(function* () {
			const attemptsRef = yield* Ref.make(0)
			const fiber = yield* Effect.fork(
				makeRetryingFailure(
					attemptsRef,
					new TransientFailure({ message: "database is locked" }),
					5000,
				),
			)

			yield* Effect.yieldNow()
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			yield* TestClock.adjust("1199 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			yield* TestClock.adjust("1 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(2)

			yield* TestClock.adjust("2400 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(3)

			yield* Fiber.interrupt(fiber)
		}),
	)

	it.effect("caps retry delays and stops after the bounded attempts", () =>
		Effect.gen(function* () {
			const attemptsRef = yield* Ref.make(0)
			const fiber = yield* Effect.fork(
				Effect.exit(
					makeRetryingFailure(
						attemptsRef,
						new TransientFailure({ message: "resource busy" }),
						3000,
					),
				),
			)

			yield* Effect.yieldNow()
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			yield* TestClock.adjust("1200 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(2)

			yield* TestClock.adjust("2400 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(3)

			yield* TestClock.adjust("2999 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(3)

			yield* TestClock.adjust("1 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(4)

			yield* TestClock.adjust("3000 millis")
			expect(yield* Ref.get(attemptsRef)).toBe(4)

			const exit = yield* Fiber.join(fiber)
			expect(exit._tag).toBe("Failure")
		}),
	)

	it.effect("does not retry terminal failures", () =>
		Effect.gen(function* () {
			const attemptsRef = yield* Ref.make(0)
			const fiber = yield* Effect.fork(
				Effect.exit(
					makeRetryingFailure(
						attemptsRef,
						new TerminalFailure({ message: "permission denied" }),
						5000,
					),
				),
			)

			yield* Effect.yieldNow()
			expect(yield* Ref.get(attemptsRef)).toBe(1)

			const exit = yield* Fiber.join(fiber)
			expect(exit._tag).toBe("Failure")
			expect(yield* Ref.get(attemptsRef)).toBe(1)
		}),
	)
})
