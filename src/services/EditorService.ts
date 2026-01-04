// src/services/EditorService.ts

import { Command, CommandExecutor } from "@effect/platform"
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
export type FilterSessionState =
	| "idle"
	| "initializing"
	| "busy"
	| "waiting"
	| "done"
	| "error"
	| "paused"

/**
 * Filter field categories
 */
export type FilterField = "status" | "priority" | "type" | "session" | "age"

/**
 * Filter configuration for filtering tasks
 * Empty sets mean "show all" for that field
 */
export interface FilterConfig {
	readonly status: ReadonlySet<IssueStatus>
	readonly priority: ReadonlySet<number>
	readonly type: ReadonlySet<IssueType>
	readonly session: ReadonlySet<FilterSessionState>
	/**
	 * Filter to tasks not updated in N days.
	 * null means no age filter.
	 * A value of 7 means "show tasks not updated in the last 7 days"
	 */
	readonly updatedDaysAgo: number | null
}

/**
 * Default filter config (show all)
 */
export const DEFAULT_FILTER_CONFIG: FilterConfig = Data.struct({
	status: new Set<IssueStatus>(),
	priority: new Set<number>(),
	type: new Set<IssueType>(),
	session: new Set<FilterSessionState>(),
	updatedDaysAgo: null,
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
 * - mergeSelect: Mode for selecting a target bead to merge the current bead into
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
	| {
			readonly _tag: "action"
			readonly targetTaskId: string | null
			readonly selectedIds: ReadonlyArray<string>
	  }
	| { readonly _tag: "search"; readonly query: string }
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
	| {
			readonly _tag: "mergeSelect"
			readonly sourceBeadId: string
	  }

/**
 * Default sort configuration: session status > priority > updated_at
 */
export const DEFAULT_SORT_CONFIG: SortConfig = Data.struct({
	field: "session" as const,
	direction: "desc" as const,
})

/**
 * Per-project editor state - stores mode, sort, and filter configuration
 * for each project path to preserve state when switching projects
 */
export interface PerProjectEditorState {
	readonly mode: EditorMode
	readonly sortConfig: SortConfig
	readonly filterConfig: FilterConfig
}

export class EditorService extends Effect.Service<EditorService>()("EditorService", {
	effect: Effect.gen(function* () {
		const mode = yield* SubscriptionRef.make<EditorMode>(Data.struct({ _tag: "normal" as const }))
		const sortConfig = yield* SubscriptionRef.make<SortConfig>(DEFAULT_SORT_CONFIG)
		const filterConfig = yield* SubscriptionRef.make<FilterConfig>(DEFAULT_FILTER_CONFIG)

		// Per-project state storage
		const perProjectState = yield* SubscriptionRef.make<Map<string, PerProjectEditorState>>(
			new Map(),
		)
		const currentProjectPath = yield* SubscriptionRef.make<string | null>(null)

		// Helper: get default state for a new project
		const getDefaultState = (): PerProjectEditorState => ({
			mode: { _tag: "normal" },
			sortConfig: DEFAULT_SORT_CONFIG,
			filterConfig: DEFAULT_FILTER_CONFIG,
		})

		// Helper: get or create project state in the map
		const getOrCreateProjectState = (projectPath: string) =>
			Effect.gen(function* () {
				const stateMap = yield* SubscriptionRef.get(perProjectState)
				if (stateMap.has(projectPath)) {
					return stateMap.get(projectPath)!
				}
				const newState = getDefaultState()
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					copy.set(projectPath, newState)
					return copy
				})
				return newState
			})

		// Helper: save current derived state to the per-project map
		const saveCurrentToMap = () =>
			Effect.gen(function* () {
				const path = yield* SubscriptionRef.get(currentProjectPath)
				if (!path) return
				const currentMode = yield* SubscriptionRef.get(mode)
				const currentSort = yield* SubscriptionRef.get(sortConfig)
				const currentFilter = yield* SubscriptionRef.get(filterConfig)
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					copy.set(path, {
						mode: currentMode,
						sortConfig: currentSort,
						filterConfig: currentFilter,
					})
					return copy
				})
			})

		// Helper: sync derived SubscriptionRefs from project state
		const syncDerivedFromProject = (projectPath: string) =>
			Effect.gen(function* () {
				const state = yield* getOrCreateProjectState(projectPath)
				yield* SubscriptionRef.set(mode, state.mode)
				yield* SubscriptionRef.set(sortConfig, state.sortConfig)
				yield* SubscriptionRef.set(filterConfig, state.filterConfig)
			})

		// Helper: update per-project map when sort config changes
		const updateSortInMap = (newSort: SortConfig) =>
			Effect.gen(function* () {
				const path = yield* SubscriptionRef.get(currentProjectPath)
				if (!path) return
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					const existing = copy.get(path) ?? getDefaultState()
					copy.set(path, { ...existing, sortConfig: newSort })
					return copy
				})
			})

		// Helper: update per-project map when filter config changes
		const updateFilterInMap = (newFilter: FilterConfig) =>
			Effect.gen(function* () {
				const path = yield* SubscriptionRef.get(currentProjectPath)
				if (!path) return
				yield* SubscriptionRef.update(perProjectState, (m) => {
					const copy = new Map(m)
					const existing = copy.get(path) ?? getDefaultState()
					copy.set(path, { ...existing, filterConfig: newFilter })
					return copy
				})
			})

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
			 * Get currently selected task IDs (from select or action mode)
			 */
			getSelectedIds: (): Effect.Effect<ReadonlyArray<string>> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					if (m._tag === "select") return m.selectedIds
					if (m._tag === "action") return m.selectedIds
					return []
				}),

			/**
			 * Get current search query (only in search mode or normal mode with active filter)
			 */
			getSearchQuery: (): Effect.Effect<string> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					return m._tag === "search" ? m.query : ""
				}),

			openFile: (filePath: string) =>
				Effect.gen(function* () {
					const editorCmd = process.env.EDITOR || "vi"
					const executor = yield* CommandExecutor.CommandExecutor
					yield* Command.make(editorCmd, filePath).pipe(
						Command.stdin("inherit"),
						Command.stdout("inherit"),
						Command.stderr("inherit"),
						executor.exitCode,
						Effect.orDie,
					)
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

			/**
			 * Select all tasks by their IDs.
			 * Used by the % keybinding to select all visible tasks.
			 * If not in select mode, enters select mode first.
			 */
			selectAll: (taskIds: ReadonlyArray<string>) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					// If not in select mode, enter it with all tasks selected
					if (m._tag !== "select") {
						return Data.struct({
							_tag: "select" as const,
							selectedIds: [...taskIds],
						})
					}
					// If already in select mode, replace selection with all tasks
					return Data.struct({
						_tag: "select" as const,
						selectedIds: [...taskIds],
					})
				}),

			/**
			 * Add task IDs to the current selection (without replacing).
			 * Used for "select all in column" to add column tasks to existing selection.
			 * If not in select mode, enters select mode first.
			 */
			addToSelection: (taskIds: ReadonlyArray<string>) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "select") {
						return Data.struct({
							_tag: "select" as const,
							selectedIds: [...taskIds],
						})
					}
					// Merge with existing selection, avoiding duplicates
					const existingSet = new Set(m.selectedIds)
					const newIds = taskIds.filter((id) => !existingSet.has(id))
					return Data.struct({
						_tag: "select" as const,
						selectedIds: [...m.selectedIds, ...newIds],
					})
				}),

			/**
			 * Clear all selections in select mode.
			 */
			clearSelection: () =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					if (m._tag !== "select") return m
					return Data.struct({
						_tag: "select" as const,
						selectedIds: [],
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

			/**
			 * Enter action mode with the target task ID captured.
			 * This ensures all action menu commands operate on the task that was
			 * focused when Space was pressed, not the current cursor position.
			 * Fixes race condition where cursor could move between Space and action key.
			 * Preserves selectedIds from select mode so bulk actions work correctly.
			 */
			enterAction: (targetTaskId: string | null) =>
				SubscriptionRef.update(mode, (m): EditorMode => {
					// Preserve selectedIds when entering action mode from select mode
					const selectedIds = m._tag === "select" ? m.selectedIds : []
					return Data.struct({ _tag: "action" as const, targetTaskId, selectedIds })
				}),

			/**
			 * Get the target task ID from action mode.
			 * Returns null if not in action mode or no task was focused.
			 */
			getActionTargetTaskId: (): Effect.Effect<string | null> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					return m._tag === "action" ? m.targetTaskId : null
				}),

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
					const newSort = Data.struct({ field, direction })
					yield* SubscriptionRef.set(sortConfig, newSort)
					yield* updateSortInMap(newSort)
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
					const newSort = Data.struct({ field, direction: newDirection })
					yield* SubscriptionRef.set(sortConfig, newSort)
					yield* updateSortInMap(newSort)
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
				Effect.gen(function* () {
					yield* SubscriptionRef.update(filterConfig, (config): FilterConfig => {
						const newSet = new Set(config.status)
						if (newSet.has(status)) {
							newSet.delete(status)
						} else {
							newSet.add(status)
						}
						return Data.struct({ ...config, status: newSet })
					})
					const newFilter = yield* SubscriptionRef.get(filterConfig)
					yield* updateFilterInMap(newFilter)
				}),

			/**
			 * Toggle a priority filter value
			 */
			toggleFilterPriority: (priority: number) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.update(filterConfig, (config): FilterConfig => {
						const newSet = new Set(config.priority)
						if (newSet.has(priority)) {
							newSet.delete(priority)
						} else {
							newSet.add(priority)
						}
						return Data.struct({ ...config, priority: newSet })
					})
					const newFilter = yield* SubscriptionRef.get(filterConfig)
					yield* updateFilterInMap(newFilter)
				}),

			/**
			 * Toggle a type filter value
			 */
			toggleFilterType: (issueType: IssueType) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.update(filterConfig, (config): FilterConfig => {
						const newSet = new Set(config.type)
						if (newSet.has(issueType)) {
							newSet.delete(issueType)
						} else {
							newSet.add(issueType)
						}
						return Data.struct({ ...config, type: newSet })
					})
					const newFilter = yield* SubscriptionRef.get(filterConfig)
					yield* updateFilterInMap(newFilter)
				}),

			/**
			 * Toggle a session filter value
			 */
			toggleFilterSession: (session: FilterSessionState) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.update(filterConfig, (config): FilterConfig => {
						const newSet = new Set(config.session)
						if (newSet.has(session)) {
							newSet.delete(session)
						} else {
							newSet.add(session)
						}
						return Data.struct({ ...config, session: newSet })
					})
					const newFilter = yield* SubscriptionRef.get(filterConfig)
					yield* updateFilterInMap(newFilter)
				}),

			/**
			 * Set age filter in days.
			 * Tasks not updated in N days will be shown.
			 * Pass null to clear the age filter.
			 */
			setAgeFilter: (days: number | null) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.update(
						filterConfig,
						(config): FilterConfig => Data.struct({ ...config, updatedDaysAgo: days }),
					)
					const newFilter = yield* SubscriptionRef.get(filterConfig)
					yield* updateFilterInMap(newFilter)
				}),

			/**
			 * Clear all filters
			 */
			clearFilters: () =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(filterConfig, DEFAULT_FILTER_CONFIG)
					yield* updateFilterInMap(DEFAULT_FILTER_CONFIG)
				}),

			/**
			 * Restore sort and filter configuration from saved state
			 * Used when switching projects to restore previous UI state
			 */
			restoreState: (savedSort: SortConfig, savedFilter: FilterConfig) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.set(sortConfig, savedSort)
					yield* SubscriptionRef.set(filterConfig, savedFilter)
					yield* updateSortInMap(savedSort)
					yield* updateFilterInMap(savedFilter)
				}),

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
					if (config.updatedDaysAgo !== null) count++
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
						config.session.size > 0 ||
						config.updatedDaysAgo !== null
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

			// ========================================================================
			// Merge Select Mode
			// ========================================================================

			/**
			 * Enter merge select mode for selecting a target bead to merge into.
			 * Called when 'M' is pressed in action mode for a bead with commits.
			 *
			 * @param sourceBeadId - The bead whose work will be merged into the target
			 */
			enterMergeSelect: (sourceBeadId: string) =>
				SubscriptionRef.set(
					mode,
					Data.struct({
						_tag: "mergeSelect" as const,
						sourceBeadId,
					}),
				),

			/**
			 * Get the source bead ID from merge select mode.
			 * Returns null if not in merge select mode.
			 */
			getMergeSelectSourceId: (): Effect.Effect<string | null> =>
				Effect.gen(function* () {
					const m = yield* SubscriptionRef.get(mode)
					return m._tag === "mergeSelect" ? m.sourceBeadId : null
				}),

			/**
			 * Exit merge select mode and return to normal
			 */
			exitMergeSelect: () => SubscriptionRef.set(mode, Data.struct({ _tag: "normal" as const })),

			// ========================================================================
			// Project Switching
			// ========================================================================

			/**
			 * Switch to a new project, saving current project's state and restoring the new project's state.
			 *
			 * This method:
			 * 1. Saves the current project's mode, sort, and filter state to the internal Map
			 * 2. Updates the current project path
			 * 3. Restores the new project's saved state - either from the internal Map (in-session memory)
			 *    or from explicitly provided saved state (from disk persistence)
			 *
			 * The internal Map preserves state per-project so switching back to a project within the
			 * same session restores the exact UI state you had before. For persistence across app restarts,
			 * the caller can pass savedSort and savedFilter loaded from disk.
			 *
			 * @param newProjectPath - The path of the project to switch to, or null to clear
			 * @param savedSort - Optional sort config loaded from disk (overrides in-memory state)
			 * @param savedFilter - Optional filter config loaded from disk (overrides in-memory state)
			 */
			switchProject: (
				newProjectPath: string | null,
				savedSort?: SortConfig,
				savedFilter?: FilterConfig,
			) =>
				Effect.gen(function* () {
					// Save current state before switching
					yield* saveCurrentToMap()
					// Update current project path
					yield* SubscriptionRef.set(currentProjectPath, newProjectPath)
					// Load new project's state
					if (newProjectPath) {
						// If explicit saved state provided (from disk), use that
						if (savedSort !== undefined || savedFilter !== undefined) {
							const state = yield* getOrCreateProjectState(newProjectPath)
							const newState = {
								mode: state.mode,
								sortConfig: savedSort ?? state.sortConfig,
								filterConfig: savedFilter ?? state.filterConfig,
							}
							// Update the map with restored state
							yield* SubscriptionRef.update(perProjectState, (m) => {
								const copy = new Map(m)
								copy.set(newProjectPath, newState)
								return copy
							})
							// Sync to derived refs
							yield* SubscriptionRef.set(mode, newState.mode)
							yield* SubscriptionRef.set(sortConfig, newState.sortConfig)
							yield* SubscriptionRef.set(filterConfig, newState.filterConfig)
						} else {
							// Otherwise use in-memory state or defaults
							yield* syncDerivedFromProject(newProjectPath)
						}
					}
				}),

			/**
			 * Get the current project path
			 */
			getCurrentProjectPath: () => SubscriptionRef.get(currentProjectPath),

			/**
			 * Get current state for saving before project switch.
			 * Returns sort and filter config (mode is not persisted).
			 */
			getStateForSave: () =>
				Effect.gen(function* () {
					const sort = yield* SubscriptionRef.get(sortConfig)
					const filter = yield* SubscriptionRef.get(filterConfig)
					return { sortConfig: sort, filterConfig: filter }
				}),
		}
	}),
}) {}
