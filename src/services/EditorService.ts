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
 * Task representation for orchestration mode
 */
export interface OrchestrationTask {
	readonly id: string
	readonly title: string
	readonly status: "open" | "in_progress" | "blocked" | "closed"
	readonly hasSession: boolean
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

// ============================================================================
// Filter Types
// ============================================================================

/**
 * Issue status for filtering
 */
export type IssueStatus = "open" | "in_progress" | "blocked" | "closed"

/**
 * Issue type for filtering
 */
export type IssueType = "bug" | "feature" | "task" | "epic" | "chore"

/**
 * Session state for filtering
 */
export type FilterSessionState = "idle" | "busy" | "waiting" | "done" | "error" | "paused"

/**
 * Filter field categories
 */
export type FilterField = "status" | "priority" | "type" | "session"

/**
 * Filter configuration for filtering tasks
 * Empty sets mean "show all" for that field
 */
export interface FilterConfig {
	readonly status: ReadonlySet<IssueStatus>
	readonly priority: ReadonlySet<number>
	readonly type: ReadonlySet<IssueType>
	readonly session: ReadonlySet<FilterSessionState>
	readonly hideEpicSubtasks: boolean
}

/**
 * Default filter config (show all)
 */
export const DEFAULT_FILTER_CONFIG: FilterConfig = Data.struct({
	status: new Set<IssueStatus>(),
	priority: new Set<number>(),
	type: new Set<IssueType>(),
	session: new Set<FilterSessionState>(),
	hideEpicSubtasks: true,
})

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
 * - filter: Filter menu mode triggered by 'f' - filter by status/priority/type/session
 * - orchestrate: Epic orchestration mode for managing child tasks ('o' from epic detail)
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
	| { readonly _tag: "filter"; readonly activeField: FilterField | null }
	| {
			readonly _tag: "orchestrate"
			readonly epicId: string
			readonly epicTitle: string
			readonly childTasks: ReadonlyArray<OrchestrationTask>
			readonly selectedIds: ReadonlyArray<string>
			readonly focusIndex: number
	  }

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
		const filterConfig = yield* SubscriptionRef.make<FilterConfig>(DEFAULT_FILTER_CONFIG)

		return {
			// Expose SubscriptionRef for atom subscription
			mode,
			sortConfig,
			filterConfig,

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

			// ========================================================================
			// Filter Mode
			// ========================================================================

			/**
			 * Enter filter menu mode
			 */
			enterFilter: () =>
				SubscriptionRef.set(mode, Data.struct({ _tag: "filter" as const, activeField: null })),

			/**
			 * Set the active filter sub-menu field
			 */
			setActiveFilterField: (field: FilterField | null) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "filter") return m
					return Data.struct({ _tag: "filter" as const, activeField: field })
				}),

			/**
			 * Get current filter configuration
			 */
			getFilterConfig: () => SubscriptionRef.get(filterConfig),

			/**
			 * Toggle a status filter value
			 */
			toggleFilterStatus: (status: IssueStatus) =>
				SubscriptionRef.update(filterConfig, (config): FilterConfig => {
					const newSet = new Set(config.status)
					if (newSet.has(status)) {
						newSet.delete(status)
					} else {
						newSet.add(status)
					}
					return Data.struct({ ...config, status: newSet })
				}),

			/**
			 * Toggle a priority filter value
			 */
			toggleFilterPriority: (priority: number) =>
				SubscriptionRef.update(filterConfig, (config): FilterConfig => {
					const newSet = new Set(config.priority)
					if (newSet.has(priority)) {
						newSet.delete(priority)
					} else {
						newSet.add(priority)
					}
					return Data.struct({ ...config, priority: newSet })
				}),

			/**
			 * Toggle a type filter value
			 */
			toggleFilterType: (issueType: IssueType) =>
				SubscriptionRef.update(filterConfig, (config): FilterConfig => {
					const newSet = new Set(config.type)
					if (newSet.has(issueType)) {
						newSet.delete(issueType)
					} else {
						newSet.add(issueType)
					}
					return Data.struct({ ...config, type: newSet })
				}),

			/**
			 * Toggle a session filter value
			 */
			toggleFilterSession: (session: FilterSessionState) =>
				SubscriptionRef.update(filterConfig, (config): FilterConfig => {
					const newSet = new Set(config.session)
					if (newSet.has(session)) {
						newSet.delete(session)
					} else {
						newSet.add(session)
					}
					return Data.struct({ ...config, session: newSet })
				}),

			/**
			 * Toggle hideEpicSubtasks setting
			 */
			toggleHideEpicSubtasks: () =>
				SubscriptionRef.update(
					filterConfig,
					(config): FilterConfig =>
						Data.struct({
							...config,
							hideEpicSubtasks: !config.hideEpicSubtasks,
						}),
				),

			/**
			 * Clear all filters
			 */
			clearFilters: () => SubscriptionRef.set(filterConfig, DEFAULT_FILTER_CONFIG),

			/**
			 * Get count of active filter fields (for status bar display)
			 */
			getActiveFilterCount: (): Effect.Effect<number> =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(filterConfig)
					let count = 0
					if (config.status.size > 0) count++
					if (config.priority.size > 0) count++
					if (config.type.size > 0) count++
					if (config.session.size > 0) count++
					return count
				}),

			/**
			 * Check if any filters are active
			 */
			hasActiveFilters: (): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(filterConfig)
					return (
						config.status.size > 0 ||
						config.priority.size > 0 ||
						config.type.size > 0 ||
						config.session.size > 0
					)
				}),

			// ========================================================================
			// Orchestrate Mode
			// ========================================================================

			/**
			 * Enter orchestrate mode for an epic
			 * Called when 'o' is pressed in the detail panel for an epic
			 */
			enterOrchestrate: (
				epicId: string,
				epicTitle: string,
				children: ReadonlyArray<OrchestrationTask>,
			) =>
				SubscriptionRef.set(
					mode,
					Data.struct({
						_tag: "orchestrate" as const,
						epicId,
						epicTitle,
						childTasks: children,
						selectedIds: [],
						focusIndex: 0,
					}),
				),

			/**
			 * Move focus down in orchestrate mode
			 */
			orchestrateMoveDown: () =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "orchestrate") return m
					const maxIndex = Math.max(0, m.childTasks.length - 1)
					const newIndex = Math.min(m.focusIndex + 1, maxIndex)
					return Data.struct({ ...m, focusIndex: newIndex })
				}),

			/**
			 * Move focus up in orchestrate mode
			 */
			orchestrateMoveUp: () =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "orchestrate") return m
					const newIndex = Math.max(m.focusIndex - 1, 0)
					return Data.struct({ ...m, focusIndex: newIndex })
				}),

			/**
			 * Toggle task selection in orchestrate mode
			 * Only allows selecting spawnable tasks (status === "open" && !hasSession)
			 */
			orchestrateToggle: (taskId: string) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "orchestrate") return m

					// Find the task
					const task = m.childTasks.find((t) => t.id === taskId)
					if (!task) return m

					// Only allow toggling spawnable tasks
					const isSpawnable = task.status === "open" && !task.hasSession
					if (!isSpawnable) return m

					// Toggle selection
					const has = m.selectedIds.includes(taskId)
					return Data.struct({
						...m,
						selectedIds: has
							? m.selectedIds.filter((id) => id !== taskId)
							: [...m.selectedIds, taskId],
					})
				}),

			/**
			 * Select all spawnable tasks in orchestrate mode
			 */
			orchestrateSelectAll: () =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "orchestrate") return m

					const spawnableIds = m.childTasks
						.filter((t) => t.status === "open" && !t.hasSession)
						.map((t) => t.id)

					return Data.struct({ ...m, selectedIds: spawnableIds })
				}),

			/**
			 * Clear all selections in orchestrate mode
			 */
			orchestrateSelectNone: () =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "orchestrate") return m
					return Data.struct({ ...m, selectedIds: [] })
				}),

			/**
			 * Exit orchestrate mode and return to normal
			 */
			exitOrchestrate: () => SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const })),
		}
	}),
}) {}
