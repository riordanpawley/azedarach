/**
 * Mode Service Atoms
 *
 * Handles editor mode state: normal, select, goto, jump, action, search, command, sort.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect, type Record } from "effect"
import { ModeService } from "../../atoms/runtime.js"
import type { SortField } from "../../services/EditorService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Mode State Atoms
// ============================================================================

/**
 * Editor mode atom - subscribes to ModeService mode changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const mode = useAtomValue(modeAtom)
 */
export const modeAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* ModeService
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
 * Sort configuration atom - subscribes to ModeService sortConfig changes
 *
 * Usage: const sortConfig = useAtomValue(sortConfigAtom)
 */
export const sortConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
			const editor = yield* ModeService
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
		const editor = yield* ModeService
		yield* editor.setPendingJumpKey(key)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Enter action mode
 *
 * Usage: const [, enterAction] = useAtom(enterActionAtom, { mode: "promise" })
 *        await enterAction()
 */
export const enterActionAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const editor = yield* ModeService
		yield* editor.enterAction()
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
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
		const editor = yield* ModeService
		yield* editor.cycleSort(field)
	}).pipe(Effect.catchAll(Effect.logError)),
)
