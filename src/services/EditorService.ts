// src/services/EditorService.ts

import { Data, Effect, type Record, SubscriptionRef } from "effect"

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
 * Sort criteria for tasks
 */
export type SortField = "session" | "priority" | "updated"

/**
 * Sort direction
 */
export type SortDirection = "asc" | "desc"

/**
 * Sort configuration
 */
export interface SortConfig {
	readonly field: SortField
	readonly direction: SortDirection
}

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
 * - sort: Sort menu mode triggered by ','
 */
export type EditorMode =
	| { readonly _tag: "normal" }
	| { readonly _tag: "select"; readonly selectedIds: ReadonlyArray<string> }
	| {
			readonly _tag: "goto"
			readonly gotoSubMode: GotoSubMode
			readonly jumpLabels: Record.ReadonlyRecord<string, JumpTarget> | null
			readonly pendingJumpKey: string | null
	  }
	| { readonly _tag: "action" }
	| { readonly _tag: "search"; readonly query: string }
	| { readonly _tag: "command"; readonly input: string }
	| { readonly _tag: "sort" }

/**
 * Default sort configuration: session status > priority > updated_at
 */
export const DEFAULT_SORT_CONFIG: SortConfig = Data.struct({
	field: "session" as const,
	direction: "desc" as const,
})

export class EditorService extends Effect.Service<EditorService>()("EditorService", {
	effect: Effect.gen(function* () {
		const mode = yield* SubscriptionRef.make<EditorMode>(Data.struct({ _tag: "normal" as const }))
		const sortConfig = yield* SubscriptionRef.make<SortConfig>(DEFAULT_SORT_CONFIG)

		return {
			// Expose SubscriptionRef for atom subscription
			mode,
			sortConfig,

			// ========================================================================
			// Mode Getters
			// ========================================================================

			getMode: () => SubscriptionRef.get(mode),

			/**
			 * Get currently selected task IDs (only in select mode)
			 */
			getSelectedIds: (): Effect.Effect<ReadonlyArray<string>> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					return m._tag === "select" ? m.selectedIds : []
				}),

			/**
			 * Get current search query (only in search mode or normal mode with active filter)
			 */
			getSearchQuery: (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					return m._tag === "search" ? m.query : ""
				}),

			/**
			 * Get current command input (only in command mode)
			 */
			getCommandInput: (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					return m._tag === "command" ? m.input : ""
				}),

			// ========================================================================
			// Normal Mode
			// ========================================================================

			exitToNormal: () => SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const })),

			// ========================================================================
			// Select Mode
			// ========================================================================

			enterSelect: () =>
				SubscriptionRef.set(mode, Data.struct({ _tag: "select" as const, selectedIds: [] })),

			exitSelect: (clearSelections = false): Effect.Effect<void> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					if (m._tag !== "select") {
						yield* SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const }))
						return
					}
					// If clearSelections is false, preserve selectedIds by re-entering select mode
					// This matches the editorFSM behavior where selections can persist
					if (clearSelections) {
						yield* SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const }))
					} else {
						yield* SubscriptionRef.set(
							mode,
							Data.struct({ _tag: "select" as const, selectedIds: m.selectedIds }),
						)
					}
				}),

			toggleSelection: (taskId: string) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "select") return m
					const has = m.selectedIds.includes(taskId)
					return Data.struct({
						_tag: "select" as const,
						selectedIds: has
							? m.selectedIds.filter((id) => id !== taskId)
							: [...m.selectedIds, taskId],
					})
				}),

			// ========================================================================
			// Goto Mode
			// ========================================================================

			enterGoto: () =>
				SubscriptionRef.set(
					mode,
					Data.struct({
						_tag: "goto" as const,
						gotoSubMode: "pending" as const,
						jumpLabels: null,
						pendingJumpKey: null,
					}),
				),

			enterJump: (labels: Record.ReadonlyRecord<string, JumpTarget>) =>
				SubscriptionRef.set(
					mode,
					Data.struct({
						_tag: "goto" as const,
						gotoSubMode: "jump" as const,
						jumpLabels: labels,
						pendingJumpKey: null,
					}),
				),

			setPendingJumpKey: (key: string) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "goto") return m
					return Data.struct({ ...m, pendingJumpKey: key })
				}),

			// ========================================================================
			// Action Mode
			// ========================================================================

			enterAction: () => SubscriptionRef.set(mode, Data.struct({ _tag: "action" as const })),

			// ========================================================================
			// Search Mode
			// ========================================================================

			enterSearch: () =>
				SubscriptionRef.set(mode, Data.struct({ _tag: "search" as const, query: "" })),

			updateSearch: (query: string) =>
				SubscriptionRef.update(
					mode,
					(m): EditorMode => (m._tag === "search" ? Data.struct({ ...m, query }) : m),
				),

			clearSearch: () => SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const })),

			// ========================================================================
			// Command Mode
			// ========================================================================

			enterCommand: () =>
				SubscriptionRef.set(mode, Data.struct({ _tag: "command" as const, input: "" })),

			updateCommand: (input: string) =>
				SubscriptionRef.update(
					mode,
					(m): EditorMode => (m._tag === "command" ? Data.struct({ ...m, input }) : m),
				),

			clearCommand: () => SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const })),

			executeCommand: () =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					if (m._tag !== "command") return
					// Command execution logic here (implemented in KeyboardService or App)
					yield* SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const }))
				}),

			// ========================================================================
			// Sort Mode
			// ========================================================================

			/**
			 * Enter sort menu mode
			 */
			enterSort: () => SubscriptionRef.set(mode, Data.struct({ _tag: "sort" as const })),

			/**
			 * Get current sort configuration
			 */
			getSortConfig: () => SubscriptionRef.get(sortConfig),

			/**
			 * Update sort configuration and exit to normal mode
			 */
			setSort: (field: SortField, direction: SortDirection) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(sortConfig, Data.struct({ field, direction }))
					yield* SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const }))
				}),

			/**
			 * Cycle sort direction for a field
			 * If already sorted by this field, toggle direction
			 * Otherwise, set to this field with default direction
			 */
			cycleSort: (field: SortField) =>
				Effect.gen(function* () {
					const current = yield* SubscriptionRef.get(sortConfig)
					const newDirection: SortDirection =
						current.field === field ? (current.direction === "desc" ? "asc" : "desc") : "desc"
					yield* SubscriptionRef.set(sortConfig, Data.struct({ field, direction: newDirection }))
					yield* SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const }))
				}),
		}
	}),
}) {}
