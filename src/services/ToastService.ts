/**
 * ToastService - Toast notification management with auto-expiration
 *
 * Manages toast notifications using Effect.Service pattern with:
 * - Fine-grained Refs for state management
 * - Auto-expiration fiber for cleaning up expired toasts
 * - Methods for showing, dismissing, and clearing toasts
 */

import { Effect, Ref } from "effect"

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
		// Initialize fine-grained refs
		const toasts = yield* Ref.make<ReadonlyArray<Toast>>([])
		const duration = yield* Ref.make(5000)
		const maxVisible = yield* Ref.make(3)

		// Auto-expiration fiber
		yield* Effect.forkScoped(
			Effect.forever(
				Effect.gen(function* () {
					yield* Effect.sleep("100 millis")
					const now = Date.now()
					const durationMs = yield* Ref.get(duration)
					yield* Ref.update(toasts, (ts) =>
						ts.filter((t) => now - t.createdAt < durationMs),
					)
				}),
			),
		)

		return {
			// State refs (fine-grained)
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
					yield* Ref.update(toasts, (ts) => [...ts.slice(-(max - 1)), toast])
					return toast
				}),

			dismiss: (id: string) =>
				Ref.update(toasts, (ts) => ts.filter((t) => t.id !== id)),

			clear: () => Ref.set(toasts, []),
		}
	}),
}) {}
