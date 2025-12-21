/**
 * Mode Service Atoms
 *
 * Handles editor mode state: normal, select, goto, jump, action, search, command, sort, filter, orchestrate.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect, type Record } from "effect"
import {
	EditorService,
	type FilterField,
	type OrchestrationTask,
	type SortField,
} from "../../services/EditorService.js"
import { NavigationService } from "../../services/NavigationService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Mode State Atoms
// ============================================================================

/**
 * Editor mode atom - subscribes to EditorService mode changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const mode = useAtomValue(modeAtom)
 */
export const modeAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* EditorService
		return editor.mode
	}),
)

/**
 * Selected task IDs atom - derived from modeAtom
 *
 * Usage: const selectedIds = useAtomValue(selectedIdsAtom)
 */
export const selectedIdsAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return []
	const mode = modeResult.value
	return mode._tag === "select" ? mode.selectedIds : []
})

/**
 * Search query atom - derived from modeAtom
 *
 * Usage: const searchQuery = useAtomValue(searchQueryAtom)
 */
export const searchQueryAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return ""
	const mode = modeResult.value
	return mode._tag === "search" ? mode.query : ""
})

/**
 * Command input atom - derived from modeAtom
 *
 * Usage: const commandInput = useAtomValue(commandInputAtom)
 */
export const commandInputAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return ""
	const mode = modeResult.value
	return mode._tag === "command" ? mode.input : ""
})

/**
 * Sort configuration atom - subscribes to EditorService sortConfig changes
 *
 * Usage: const sortConfig = useAtomValue(sortConfigAtom)
 */
export const sortConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* EditorService
		return editor.sortConfig
	}),
)

// ============================================================================
// Mode Action Atoms
// ============================================================================

/**
 * Enter select mode
 *
 * Usage: const [, enterSelect] = useAtom(enterSelectAtom, { mode: "promise" })
 *        await enterSelect()
 */
export const enterSelectAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.enterSelect()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit select mode
 *
 * Usage: const [, exitSelect] = useAtom(exitSelectAtom, { mode: "promise" })
 *        await exitSelect(true) // clearSelections
 */
export const exitSelectAtom = appRuntime.fn((clearSelections: boolean | undefined) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.exitSelect(clearSelections ?? false)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Toggle selection of a task
 *
 * Usage: const [, toggleSelection] = useAtom(toggleSelectionAtom, { mode: "promise" })
 *        await toggleSelection(taskId)
 */
export const toggleSelectionAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.toggleSelection(taskId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter goto mode
 *
 * Usage: const [, enterGoto] = useAtom(enterGotoAtom, { mode: "promise" })
 *        await enterGoto()
 */
export const enterGotoAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.enterGoto()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter jump mode with labels
 *
 * Usage: const [, enterJump] = useAtom(enterJumpAtom, { mode: "promise" })
 *        await enterJump(labelsRecord)
 */
export const enterJumpAtom = appRuntime.fn(
	(
		labels: Record.ReadonlyRecord<
			string,
			{ taskId: string; columnIndex: number; taskIndex: number }
		>,
	) =>
		Effect.gen(function* () {
			const editor = yield* EditorService
			yield* editor.enterJump(labels)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Set pending jump key
 *
 * Usage: const [, setPendingJumpKey] = useAtom(setPendingJumpKeyAtom, { mode: "promise" })
 *        await setPendingJumpKey("a")
 */
export const setPendingJumpKeyAtom = appRuntime.fn((key: string) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.setPendingJumpKey(key)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter action mode with task ID captured at entry time
 *
 * Captures the currently focused task ID to prevent race conditions
 * where cursor moves between Space and action key press.
 *
 * Usage: const [, enterAction] = useAtom(enterActionAtom, { mode: "promise" })
 *        await enterAction()
 */
export const enterActionAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		const nav = yield* NavigationService
		const taskId = yield* nav.getFocusedTaskId()
		yield* editor.enterAction(taskId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter search mode
 *
 * Usage: const [, enterSearch] = useAtom(enterSearchAtom, { mode: "promise" })
 *        await enterSearch()
 */
export const enterSearchAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.enterSearch()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Update search query
 *
 * Usage: const [, updateSearch] = useAtom(updateSearchAtom, { mode: "promise" })
 *        await updateSearch("new query")
 */
export const updateSearchAtom = appRuntime.fn((query: string) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.updateSearch(query)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Clear search and return to normal mode
 *
 * Usage: const [, clearSearch] = useAtom(clearSearchAtom, { mode: "promise" })
 *        await clearSearch()
 */
export const clearSearchAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.clearSearch()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter command mode
 *
 * Usage: const [, enterCommand] = useAtom(enterCommandAtom, { mode: "promise" })
 *        await enterCommand()
 */
export const enterCommandAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.enterCommand()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Update command input
 *
 * Usage: const [, updateCommand] = useAtom(updateCommandAtom, { mode: "promise" })
 *        await updateCommand("new command")
 */
export const updateCommandAtom = appRuntime.fn((input: string) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.updateCommand(input)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Clear command and return to normal mode
 *
 * Usage: const [, clearCommand] = useAtom(clearCommandAtom, { mode: "promise" })
 *        await clearCommand()
 */
export const clearCommandAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.clearCommand()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit to normal mode
 *
 * Usage: const [, exitToNormal] = useAtom(exitToNormalAtom, { mode: "promise" })
 *        await exitToNormal()
 */
export const exitToNormalAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.exitToNormal()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter sort mode
 *
 * Usage: const [, enterSort] = useAtom(enterSortAtom, { mode: "promise" })
 *        await enterSort()
 */
export const enterSortAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.enterSort()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Cycle sort configuration for a field
 *
 * Usage: const [, cycleSort] = useAtom(cycleSortAtom, { mode: "promise" })
 *        await cycleSort("priority")
 */
export const cycleSortAtom = appRuntime.fn((field: SortField) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.cycleSort(field)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Filter Mode Atoms
// ============================================================================

/**
 * Filter configuration atom - subscribes to EditorService filterConfig changes
 *
 * Usage: const filterConfig = useAtomValue(filterConfigAtom)
 */
export const filterConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* EditorService
		return editor.filterConfig
	}),
)

/**
 * Active filter field atom - derived from modeAtom
 *
 * Usage: const activeFilterField = useAtomValue(activeFilterFieldAtom)
 */
export const activeFilterFieldAtom = Atom.readable((get): FilterField | null => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return null
	const mode = modeResult.value
	return mode._tag === "filter" ? mode.activeField : null
})

/**
 * Check if currently in filter mode
 *
 * Usage: const isFilter = useAtomValue(isFilterAtom)
 */
export const isFilterAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return false
	return modeResult.value._tag === "filter"
})

/**
 * Enter filter mode
 *
 * Usage: const [, enterFilter] = useAtom(enterFilterAtom, { mode: "promise" })
 *        await enterFilter()
 */
export const enterFilterAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.enterFilter()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Clear all filters
 *
 * Usage: const [, clearFilters] = useAtom(clearFiltersAtom, { mode: "promise" })
 *        await clearFilters()
 */
export const clearFiltersAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.clearFilters()
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Orchestrate Mode Atoms
// ============================================================================

/**
 * Check if currently in orchestrate mode
 *
 * Usage: const isOrchestrate = useAtomValue(isOrchestrateAtom)
 */
export const isOrchestrateAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return false
	return modeResult.value._tag === "orchestrate"
})

/**
 * Get full orchestrate state or null
 *
 * Usage: const orchestrateState = useAtomValue(orchestrateStateAtom)
 */
export const orchestrateStateAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return null
	const mode = modeResult.value
	return mode._tag === "orchestrate" ? mode : null
})

/**
 * Get selected task IDs in orchestrate mode
 *
 * Usage: const selectedIds = useAtomValue(orchestrateSelectedIdsAtom)
 */
export const orchestrateSelectedIdsAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return []
	const mode = modeResult.value
	return mode._tag === "orchestrate" ? mode.selectedIds : []
})

/**
 * Get focus index in orchestrate mode
 *
 * Usage: const focusIndex = useAtomValue(orchestrateFocusIndexAtom)
 */
export const orchestrateFocusIndexAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return 0
	const mode = modeResult.value
	return mode._tag === "orchestrate" ? mode.focusIndex : 0
})

/**
 * Count spawnable tasks in orchestrate mode
 * Spawnable = status is "open" and no active session
 *
 * Usage: const spawnableCount = useAtomValue(orchestrateSpawnableCountAtom)
 */
export const orchestrateSpawnableCountAtom = Atom.readable((get) => {
	const modeResult = get(modeAtom)
	if (!Result.isSuccess(modeResult)) return 0
	const mode = modeResult.value
	if (mode._tag !== "orchestrate") return 0
	return mode.childTasks.filter((t) => t.status === "open" && !t.hasSession).length
})

/**
 * Enter orchestrate mode for an epic
 *
 * Usage: const [, enterOrchestrate] = useAtom(enterOrchestrateAtom, { mode: "promise" })
 *        await enterOrchestrate({ epicId, epicTitle, children })
 */
export const enterOrchestrateAtom = appRuntime.fn(
	({
		epicId,
		epicTitle,
		children,
	}: {
		epicId: string
		epicTitle: string
		children: ReadonlyArray<OrchestrationTask>
	}) =>
		Effect.gen(function* () {
			const editor = yield* EditorService
			yield* editor.enterOrchestrate(epicId, epicTitle, children)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Move focus down in orchestrate mode
 *
 * Usage: const [, orchestrateMoveDown] = useAtom(orchestrateMoveDownAtom, { mode: "promise" })
 *        await orchestrateMoveDown()
 */
export const orchestrateMoveDownAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.orchestrateMoveDown()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Move focus up in orchestrate mode
 *
 * Usage: const [, orchestrateMoveUp] = useAtom(orchestrateMoveUpAtom, { mode: "promise" })
 *        await orchestrateMoveUp()
 */
export const orchestrateMoveUpAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.orchestrateMoveUp()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Toggle task selection in orchestrate mode
 *
 * Usage: const [, orchestrateToggle] = useAtom(orchestrateToggleAtom, { mode: "promise" })
 *        await orchestrateToggle(taskId)
 */
export const orchestrateToggleAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.orchestrateToggle(taskId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Select all spawnable tasks in orchestrate mode
 *
 * Usage: const [, orchestrateSelectAll] = useAtom(orchestrateSelectAllAtom, { mode: "promise" })
 *        await orchestrateSelectAll()
 */
export const orchestrateSelectAllAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.orchestrateSelectAll()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Clear all selections in orchestrate mode
 *
 * Usage: const [, orchestrateSelectNone] = useAtom(orchestrateSelectNoneAtom, { mode: "promise" })
 *        await orchestrateSelectNone()
 */
export const orchestrateSelectNoneAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.orchestrateSelectNone()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit orchestrate mode and return to normal
 *
 * Usage: const [, exitOrchestrate] = useAtom(exitOrchestrateAtom, { mode: "promise" })
 *        await exitOrchestrate()
 */
export const exitOrchestrateAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* EditorService
		yield* editor.exitOrchestrate()
	}).pipe(Effect.catchAll(Effect.logError)),
)
