// src/services/OverlayService.ts

import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, SubscriptionRef } from "effect"
import { emptyArray } from "../lib/empty"

// onConfirm effects require CommandExecutor (exception to no-leaking-requirements rule)
type AnyEffect = Effect.Effect<void, never, CommandExecutor.CommandExecutor>

export type Overlay =
	| { readonly _tag: "help" }
	| { readonly _tag: "detail"; readonly taskId: string }
	| { readonly _tag: "create" }
	| { readonly _tag: "claudeCreate" }
	| { readonly _tag: "settings" }
	| { readonly _tag: "imageAttach"; readonly taskId: string }
	| { readonly _tag: "confirm"; readonly message: string; readonly onConfirm: AnyEffect }
	| { readonly _tag: "diagnostics" }
	| { readonly _tag: "projectSelector" }

export class OverlayService extends Effect.Service<OverlayService>()("OverlayService", {
	effect: Effect.gen(function* () {
		// SubscriptionRef for reactive overlay stack
		const stack = yield* SubscriptionRef.make<ReadonlyArray<Overlay>>(emptyArray())

		return {
			// Expose SubscriptionRef for atom subscription
			stack,

			push: (overlay: Overlay) =>
				SubscriptionRef.update(stack, (s) => [...s, Data.struct(overlay)]),

			pop: () =>
				SubscriptionRef.modify(stack, (s) => {
					if (s.length === 0) return [undefined, s]
					return [s[s.length - 1], s.slice(0, -1)]
				}),

			clear: () => SubscriptionRef.set(stack, emptyArray()),

			current: () =>
				SubscriptionRef.get(stack).pipe(
					Effect.map((s) => (s.length > 0 ? s[s.length - 1] : undefined)),
				),

			isOpen: () => SubscriptionRef.get(stack).pipe(Effect.map((s) => s.length > 0)),
		}
	}),
}) {}
