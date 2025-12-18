/**
 * Board State Atoms
 *
 * Handles board data and filtering.
 */

import { Atom } from "@effect-atom/atom"
import { Effect } from "effect"
import { BoardService } from "../../services/BoardService.js"
import { ViewService } from "../../services/ViewService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Board State Atoms
// ============================================================================

/**
 * Board tasks atom - subscribes to BoardService tasks changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const tasks = useAtomValue(boardTasksAtom)
 */
export const boardTasksAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const board = yield* BoardService
		return board.tasks
	}),
)

/**
 * Board tasks by column atom - subscribes to BoardService tasksByColumn changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const tasksByColumn = useAtomValue(boardTasksByColumnAtom)
 */
export const boardTasksByColumnAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const board = yield* BoardService
		return board.tasksByColumn
	}),
)

/**
 * Filtered and sorted tasks by column - single source of truth for UI rendering
 *
 * This atom subscribes to BoardService's filteredTasksByColumn which:
 * - Automatically updates when tasks change (every 2 seconds)
 * - Automatically updates when sortConfig changes
 * - Automatically updates when search query changes
 *
 * Usage: const tasksByColumn = useAtomValue(filteredTasksByColumnAtom)
 */
export const filteredTasksByColumnAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const board = yield* BoardService
		return board.filteredTasksByColumn
	}),
)

/**
 * Refresh board data from BeadsClient
 *
 * Must be called before navigation can work.
 *
 * Usage: const refreshBoard = useAtomSet(refreshBoardAtom, { mode: "promise" })
 *        refreshBoard()
 */
export const refreshBoardAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const board = yield* BoardService
		yield* board.refresh()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Atom for currently selected task ID
 */
export const selectedTaskIdAtom = Atom.make<string | undefined>(undefined)

/**
 * Atom for UI error state
 */
export const errorAtom = Atom.make<string | undefined>(undefined)

/**
 * Atom for board view mode (kanban vs compact)
 *
 * - kanban: Traditional column-based view with task cards
 * - compact: Linear list view with minimal row height
 *
 * Uses ViewService for reactive state via SubscriptionRef.
 *
 * Usage: const viewMode = useAtomValue(viewModeAtom)
 */
export const viewModeAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const viewService = yield* ViewService
		return viewService.viewMode
	}),
)
