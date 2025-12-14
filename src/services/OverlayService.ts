// src/services/OverlayService.ts

import { Effect, SubscriptionRef } from "effect"

export type Overlay =
  | { readonly _tag: "help" }
  | { readonly _tag: "detail"; readonly taskId: string }
  | { readonly _tag: "create" }
  | { readonly _tag: "settings" }
  | { readonly _tag: "confirm"; readonly message: string; readonly onConfirm: Effect.Effect<void> }

export class OverlayService extends Effect.Service<OverlayService>()("OverlayService", {
  effect: Effect.gen(function* () {
    // SubscriptionRef for reactive overlay stack
    const stack = yield* SubscriptionRef.make<ReadonlyArray<Overlay>>([])

    return {
      // Expose SubscriptionRef for atom subscription
      stack,

      push: (overlay: Overlay) => SubscriptionRef.update(stack, (s) => [...s, overlay]),

      pop: () =>
        SubscriptionRef.modify(stack, (s) => {
          if (s.length === 0) return [undefined, s]
          return [s[s.length - 1], s.slice(0, -1)]
        }),

      clear: () => SubscriptionRef.set(stack, []),

      current: () =>
        SubscriptionRef.get(stack).pipe(
          Effect.map((s) => (s.length > 0 ? s[s.length - 1] : undefined))
        ),

      isOpen: () => SubscriptionRef.get(stack).pipe(Effect.map((s) => s.length > 0)),
    }
  }),
}) {}
