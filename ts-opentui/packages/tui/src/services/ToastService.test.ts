import { describe, expect, it } from "bun:test"
import { Effect, SubscriptionRef } from "effect"
import { ToastService } from "./ToastService.js"

const runToast = <A>(effect: Effect.Effect<A, never, ToastService>) =>
	Effect.runPromise(effect.pipe(Effect.provide(ToastService.Default)))

describe("ToastService", () => {
	it("shows and dismisses toasts", async () => {
		const result = await runToast(
			Effect.gen(function* () {
				const toastService = yield* ToastService
				const toast = yield* toastService.show("info", "hello")
				const visibleAfterShow = yield* SubscriptionRef.get(toastService.toasts)
				yield* toastService.dismiss(toast.id)
				const visibleAfterDismiss = yield* SubscriptionRef.get(toastService.toasts)
				return {
					visibleAfterShow,
					visibleAfterDismiss,
				}
			}),
		)

		expect(result.visibleAfterShow).toHaveLength(1)
		expect(result.visibleAfterShow[0]?.message).toBe("hello")
		expect(result.visibleAfterDismiss).toHaveLength(0)
	})
})
