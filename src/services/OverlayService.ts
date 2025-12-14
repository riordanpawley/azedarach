// src/services/OverlayService.ts

import { Effect, Ref } from "effect"

export type Overlay =
  | { readonly _tag: "help" }
  | { readonly _tag: "detail"; readonly taskId: string }
  | { readonly _tag: "create" }
  | { readonly _tag: "settings" }
  | { readonly _tag: "confirm"; readonly message: string; readonly onConfirm: Effect.Effect<void> }

export class OverlayService extends Effect.Service<OverlayService>()("OverlayService", {
  effect: Effect.gen(function* () {
    const stack = yield* Ref.make<ReadonlyArray<Overlay>>([])

    return {
      stack,

      push: (overlay: Overlay) => Ref.update(stack, (s) => [...s, overlay]),

      pop: () =>
        Ref.modify(stack, (s) => {
          if (s.length === 0) return [undefined, s]
          return [s[s.length - 1], s.slice(0, -1)]
        }),

      clear: () => Ref.set(stack, []),

      current: () =>
        Ref.get(stack).pipe(
          Effect.map((s) => (s.length > 0 ? s[s.length - 1] : undefined))
        ),

      isOpen: () => Ref.get(stack).pipe(Effect.map((s) => s.length > 0)),
    }
  }),
}) {}
