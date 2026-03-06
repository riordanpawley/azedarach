import { describe, expect, it } from "bun:test"
import { BunContext } from "@effect/platform-bun"
import { Effect, Option, Ref } from "effect"
import { LinearSyncThrottle } from "./LinearSyncThrottle.js"

const withEnv = async <A>(
	env: Readonly<Record<string, string | undefined>>,
	run: () => Promise<A>,
): Promise<A> => {
	const previous = new Map<string, string | undefined>()
	for (const [key, value] of Object.entries(env)) {
		previous.set(key, process.env[key])
		if (value === undefined) {
			delete process.env[key]
		} else {
			process.env[key] = value
		}
	}

	try {
		return await run()
	} finally {
		for (const [key, value] of previous.entries()) {
			if (value === undefined) {
				delete process.env[key]
			} else {
				process.env[key] = value
			}
		}
	}
}

describe("LinearSyncThrottle", () => {
	it("processes queued tasks in order", async () => {
		await withEnv(
			{
				AZEDARACH_LINEAR_SYNC_MAX_PER_MINUTE: "6000",
				AZEDARACH_LINEAR_SYNC_BURST: "10",
				AZEDARACH_LINEAR_SYNC_WINDOW_MS: "1000",
			},
			async () => {
				const order = await Effect.runPromise(
					Effect.gen(function* () {
						const throttle = yield* LinearSyncThrottle
						const orderRef = yield* Ref.make<readonly number[]>([])

						yield* Effect.all(
							[
								throttle.enqueue({
									effect: Effect.sleep("30 millis").pipe(
										Effect.zipRight(Ref.update(orderRef, (current) => [...current, 1])),
									),
								}),
								throttle.enqueue({
									effect: Ref.update(orderRef, (current) => [...current, 2]),
								}),
								throttle.enqueue({
									effect: Ref.update(orderRef, (current) => [...current, 3]),
								}),
							],
							{ concurrency: "unbounded", discard: true },
						)

						return yield* Ref.get(orderRef)
					}).pipe(Effect.provide(LinearSyncThrottle.Default), Effect.provide(BunContext.layer)),
				)

				expect(order).toEqual([1, 2, 3])
			},
		)
	})

	it("returns none when max wait is exceeded", async () => {
		await withEnv(
			{
				AZEDARACH_LINEAR_SYNC_MAX_PER_MINUTE: "1",
				AZEDARACH_LINEAR_SYNC_BURST: "0",
				AZEDARACH_LINEAR_SYNC_WINDOW_MS: "1000",
			},
			async () => {
				const result = await Effect.runPromise(
					Effect.gen(function* () {
						const throttle = yield* LinearSyncThrottle
						const first = yield* throttle.enqueue({ effect: Effect.void })
						expect(Option.isSome(first)).toBe(true)

						return yield* throttle.enqueue({
							effect: Effect.void,
							maxWaitMs: 10,
						})
					}).pipe(Effect.provide(LinearSyncThrottle.Default), Effect.provide(BunContext.layer)),
				)

				expect(Option.isNone(result)).toBe(true)
			},
		)
	})
})
