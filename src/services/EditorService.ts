// src/services/EditorService.ts

import { Effect, Ref } from "effect"

export type EditorMode =
  | { readonly _tag: "normal" }
  | { readonly _tag: "select"; readonly selectedIds: ReadonlyArray<string> }
  | { readonly _tag: "command"; readonly input: string }
  | { readonly _tag: "search"; readonly query: string }

export class EditorService extends Effect.Service<EditorService>()("EditorService", {
  effect: Effect.gen(function* () {
    const mode = yield* Ref.make<EditorMode>({ _tag: "normal" })

    const getMode = () => Ref.get(mode)

    return {
      mode,

      enterSelect: () => Ref.set(mode, { _tag: "select", selectedIds: [] }),

      exitSelect: () => Ref.set(mode, { _tag: "normal" }),

      toggleSelection: (taskId: string) =>
        Ref.update(mode, (m): EditorMode => {
          if (m._tag !== "select") return m
          const has = m.selectedIds.includes(taskId)
          return {
            _tag: "select" as const,
            selectedIds: has
              ? m.selectedIds.filter((id) => id !== taskId)
              : [...m.selectedIds, taskId],
          }
        }),

      enterCommand: () => Ref.set(mode, { _tag: "command", input: "" }),

      updateCommand: (input: string) =>
        Ref.update(mode, (m) =>
          m._tag === "command" ? { ...m, input } : m
        ),

      executeCommand: () =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          if (m._tag !== "command") return
          // Command execution logic here
          yield* Ref.set(mode, { _tag: "normal" })
        }),

      enterSearch: () => Ref.set(mode, { _tag: "search", query: "" }),

      updateSearch: (query: string) =>
        Ref.update(mode, (m) =>
          m._tag === "search" ? { ...m, query } : m
        ),

      exitToNormal: () => Ref.set(mode, { _tag: "normal" }),

      getMode,
    }
  }),
}) {}
