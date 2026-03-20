import { describe, expect, it } from "bun:test"
import { BunContext } from "@effect/platform-bun"
import { Effect } from "effect"
import { configureTuiKeyboardService, KeyboardService } from "./KeyboardService.js"

describe("KeyboardService", () => {
	it("delegates handleKey to the configured keyboard service", async () => {
		const handledKeys: string[] = []
		configureTuiKeyboardService({
			handleKey: (key) =>
				Effect.sync(() => {
					handledKeys.push(key)
				}),
		})

		await Effect.runPromise(
			Effect.scoped(
				Effect.gen(function* () {
					const keyboard = yield* KeyboardService
					yield* keyboard.handleKey("space")
				}).pipe(Effect.provide(BunContext.layer), Effect.provide(KeyboardService.Default)),
			),
		)

		expect(handledKeys).toEqual(["space"])
	})
})
