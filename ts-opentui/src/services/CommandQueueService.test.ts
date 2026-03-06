import { describe, expect, it } from "bun:test"
import type { CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Duration, Effect } from "effect"
import { CommandQueueService } from "./CommandQueueService.js"

const runWithQueue = <A>(
	program: Effect.Effect<A, never, CommandQueueService | CommandExecutor.CommandExecutor>,
): Promise<A> =>
	Effect.runPromise(
		program.pipe(Effect.provide(CommandQueueService.Default), Effect.provide(BunContext.layer)),
	)

describe("CommandQueueService stale recovery", () => {
	it("recovers stale running commands", async () => {
		const result = await runWithQueue(
			Effect.gen(function* () {
				const queue = yield* CommandQueueService
				const taskId = "az-stale"

				yield* Effect.fork(
					queue
						.enqueue({
							taskId,
							label: "merge",
							effect: Effect.sleep(Duration.hours(1)).pipe(Effect.asVoid),
							timeout: Duration.seconds(30),
						})
						.pipe(Effect.catchAll(() => Effect.void)),
				)

				yield* Effect.sleep(Duration.millis(5))
				const before = yield* queue.getQueueInfo(taskId)

				const originalNow = Date.now
				try {
					Date.now = () => originalNow() + Duration.toMillis(Duration.minutes(2))
					const recovered = yield* queue.recoverStaleRunning(taskId, {
						grace: Duration.millis(0),
					})
					const after = yield* queue.getQueueInfo(taskId)
					return { before, recovered, after }
				} finally {
					Date.now = originalNow
				}
			}),
		)

		expect(result.before.runningLabel).toBe("merge")
		expect(result.recovered).toBe(true)
		expect(result.after.runningLabel).toBeNull()
	})
})
