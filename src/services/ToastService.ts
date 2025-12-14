/**
 * ToastService - Toast notification management with auto-expiration
 *
 * Manages toast notifications using Effect.Service pattern with:
 * - SubscriptionRef for reactive toasts array
 * - Auto-expiration fiber for cleaning up expired toasts
 * - Methods for showing, dismissing, and clearing toasts
 */

import { Effect, Ref, SubscriptionRef } from "effect"

// ============================================================================
// Types
// ============================================================================

export interface Toast {
	readonly id: string
	readonly type: "success" | "error" | "info"
	readonly message: string
	readonly createdAt: number
}

// ============================================================================
// Service Definition
// ============================================================================

export class ToastService extends Effect.Service<ToastService>()("ToastService", {
	scoped: Effect.gen(function* () {
		// SubscriptionRef for reactive toasts array
		const toasts = yield* SubscriptionRef.make<ReadonlyArray<Toast>>([])
		// Config refs (don't need reactivity)
		const duration = yield* Ref.make(5000)
		const maxVisible = yield* Ref.make(3)

		// Auto-expiration fiber
		yield* Effect.forkScoped(
			Effect.forever(
				Effect.gen(function* () {
					yield* Effect.sleep("100 millis")
					const now = Date.now()
					const durationMs = yield* Ref.get(duration)
					yield* SubscriptionRef.update(toasts, (ts) =>
						ts.filter((t) => now - t.createdAt < durationMs),
					)
				}),
			),
		)

		return {
			// Expose SubscriptionRef for atom subscription
			toasts,
			duration,
			maxVisible,

			// Methods
			show: (type: Toast["type"], message: string) =>
				Effect.gen(function* () {
					const toast: Toast = {
						id: crypto.randomUUID(),
						type,
						message,
						createdAt: Date.now(),
					}
					const max = yield* Ref.get(maxVisible)
					yield* SubscriptionRef.update(toasts, (ts) => [...ts.slice(-(max - 1)), toast])
					return toast
				}),

			dismiss: (id: string) =>
				SubscriptionRef.update(toasts, (ts) => ts.filter((t) => t.id !== id)),

			clear: () => SubscriptionRef.set(toasts, []),
		}
	}),
}) {}
