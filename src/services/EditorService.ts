// src/services/EditorService.ts

import { Effect, Ref } from "effect"

/**
 * Jump target for goto mode
 */
export interface JumpTarget {
  readonly taskId: string
  readonly columnIndex: number
  readonly taskIndex: number
}

/**
 * Goto mode sub-state
 * When 'g' is pressed, we wait for the next key:
 * - 'w' enters word/item jump mode (shows labels)
 * - 'g' goes to first item
 * - 'e' goes to last item
 * - 'h' goes to first column
 * - 'l' goes to last column
 */
export type GotoSubMode = "pending" | "jump"

/**
 * Editor mode with full state for Helix-style modal editing
 *
 * Maps directly to editorFSM.ts modes:
 * - normal: Default navigation mode (hjkl to move)
 * - select: Multi-selection mode triggered by 'v'
 * - goto: Jump mode triggered by 'g' - shows 2-char labels for instant jumping
 * - action: Action menu mode triggered by Space
 * - search: Search/filter mode triggered by '/'
 * - command: VC command input mode triggered by ':'
 */
export type EditorMode =
  | { readonly _tag: "normal" }
  | { readonly _tag: "select"; readonly selectedIds: ReadonlyArray<string> }
  | {
      readonly _tag: "goto"
      readonly gotoSubMode: GotoSubMode
      readonly jumpLabels: ReadonlyMap<string, JumpTarget> | null
      readonly pendingJumpKey: string | null
    }
  | { readonly _tag: "action" }
  | { readonly _tag: "search"; readonly query: string }
  | { readonly _tag: "command"; readonly input: string }

export class EditorService extends Effect.Service<EditorService>()("EditorService", {
  effect: Effect.gen(function* () {
    const mode = yield* Ref.make<EditorMode>({ _tag: "normal" })

    const getMode = () => Ref.get(mode)

    return {
      mode,

      // ========================================================================
      // Mode Getters
      // ========================================================================

      getMode,

      /**
       * Get currently selected task IDs (only in select mode)
       */
      getSelectedIds: (): Effect.Effect<ReadonlyArray<string>> =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          return m._tag === "select" ? m.selectedIds : []
        }),

      /**
       * Get current search query (only in search mode or normal mode with active filter)
       */
      getSearchQuery: (): Effect.Effect<string> =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          return m._tag === "search" ? m.query : ""
        }),

      /**
       * Get current command input (only in command mode)
       */
      getCommandInput: (): Effect.Effect<string> =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          return m._tag === "command" ? m.input : ""
        }),

      // ========================================================================
      // Normal Mode
      // ========================================================================

      exitToNormal: () => Ref.set(mode, { _tag: "normal" }),

      // ========================================================================
      // Select Mode
      // ========================================================================

      enterSelect: () => Ref.set(mode, { _tag: "select", selectedIds: [] }),

      exitSelect: (clearSelections = false): Effect.Effect<void> =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          if (m._tag !== "select") {
            yield* Ref.set(mode, { _tag: "normal" })
            return
          }
          // If clearSelections is false, preserve selectedIds by re-entering select mode
          // This matches the editorFSM behavior where selections can persist
          if (clearSelections) {
            yield* Ref.set(mode, { _tag: "normal" })
          } else {
            yield* Ref.set(mode, { _tag: "select", selectedIds: m.selectedIds })
          }
        }),

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

      // ========================================================================
      // Goto Mode
      // ========================================================================

      enterGoto: () =>
        Ref.set(mode, {
          _tag: "goto",
          gotoSubMode: "pending",
          jumpLabels: null,
          pendingJumpKey: null,
        }),

      enterJump: (labels: Map<string, JumpTarget>) =>
        Ref.set(mode, {
          _tag: "goto",
          gotoSubMode: "jump",
          jumpLabels: labels,
          pendingJumpKey: null,
        }),

      setPendingJumpKey: (key: string) =>
        Ref.update(mode, (m): EditorMode => {
          if (m._tag !== "goto") return m
          return { ...m, pendingJumpKey: key }
        }),

      // ========================================================================
      // Action Mode
      // ========================================================================

      enterAction: () => Ref.set(mode, { _tag: "action" }),

      // ========================================================================
      // Search Mode
      // ========================================================================

      enterSearch: () => Ref.set(mode, { _tag: "search", query: "" }),

      updateSearch: (query: string) =>
        Ref.update(mode, (m): EditorMode =>
          m._tag === "search" ? { ...m, query } : m
        ),

      clearSearch: () => Ref.set(mode, { _tag: "normal" }),

      // ========================================================================
      // Command Mode
      // ========================================================================

      enterCommand: () => Ref.set(mode, { _tag: "command", input: "" }),

      updateCommand: (input: string) =>
        Ref.update(mode, (m): EditorMode =>
          m._tag === "command" ? { ...m, input } : m
        ),

      clearCommand: () => Ref.set(mode, { _tag: "normal" }),

      executeCommand: () =>
        Effect.gen(function* () {
          const m = yield* Ref.get(mode)
          if (m._tag !== "command") return
          // Command execution logic here (implemented in KeyboardService or App)
          yield* Ref.set(mode, { _tag: "normal" })
        }),
    }
  }),
}) {}
