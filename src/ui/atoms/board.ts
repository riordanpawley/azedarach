/**
 * Board State Atoms
 *
 * Handles board data and filtering.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect, Stream, Subscribable, SubscriptionRef } from "effect"
import { BoardService } from "../../services/BoardService.js"
import { ViewService } from "../../services/ViewService.js"
import { drillDownChildIdsAtom, drillDownEpicAtom } from "./navigation.js"
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
 * - Event-driven updates (PTY changes, editor changes, explicit refresh)
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
 * Board loading state atom - debounced to prevent flashing during quick refreshes
 *
 * Only shows "Loading..." if refresh takes longer than 500ms. This prevents
 * the loading indicator from flashing during rapid updates.
 *
 * Uses Subscribable.make to wrap SubscriptionRef with a debounced changes stream.
 *
 * Usage: const isLoading = useAtomValue(boardIsLoadingAtom)
 */
export const boardIsLoadingAtom = appRuntime.subscribable(
	Effect.gen(function* () {
		const board = yield* BoardService
		const ref = board.isLoading

		// Create a Subscribable with debounced changes stream
		// Only emit after 500ms of stability - prevents flashing on quick polls
		return Subscribable.make({
			get: SubscriptionRef.get(ref),
			changes: ref.changes.pipe(Stream.debounce("500 millis")),
		})
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
 * Git stats refresh loading state - true when refreshing git stats
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const isRefreshing = useAtomValue(isRefreshingGitStatsAtom)
 */
export const isRefreshingGitStatsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const board = yield* BoardService
		return board.isRefreshingGitStats
	}),
)

/**
 * Refresh git stats for all beads with active sessions
 *
 * This is a lightweight refresh that only updates git-related fields
 * (behind count, uncommitted changes, line additions/deletions),
 * avoiding a full board reload.
 *
 * Usage: const refreshGitStats = useAtomSet(refreshGitStatsAtom, { mode: "promise" })
 *        refreshGitStats()
 */
export const refreshGitStatsAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const board = yield* BoardService
		yield* board.refreshGitStats()
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

// ============================================================================
// Drill-Down Filtered Atoms
// ============================================================================

/**
 * Drill-down filtered tasks by column - applies epic drill-down filtering
 *
 * Derives from filteredTasksByColumnAtom and drillDownChildIdsAtom:
 * - When drillDownChildIds is empty, returns all tasks (normal mode)
 * - When drillDownChildIds has values, filters to only those tasks
 *
 * This is the atom App.tsx should use for rendering the board.
 *
 * Usage: const tasksByColumn = useAtomValue(drillDownFilteredTasksAtom)
 */
export const drillDownFilteredTasksAtom = Atom.readable((get) => {
	// Get the child IDs for filtering
	const childIdsResult = get(drillDownChildIdsAtom)
	if (!Result.isSuccess(childIdsResult)) {
		// Debug: log when childIds is not ready
		console.log("[drillDownFilteredTasksAtom] childIds not ready:", childIdsResult._tag)
		return []
	}

	const childIds = childIdsResult.value

	// Get the filtered tasks
	const tasksResult = get(filteredTasksByColumnAtom)
	if (!Result.isSuccess(tasksResult)) {
		// Debug: log when tasks is not ready
		console.log("[drillDownFilteredTasksAtom] tasks not ready:", tasksResult._tag)
		return []
	}

	const tasksByColumn = tasksResult.value
	console.log("[drillDownFilteredTasksAtom] Got", tasksByColumn.flat().length, "tasks")

	// If no drill-down active (empty childIds), return all tasks
	if (childIds.size === 0) {
		return tasksByColumn
	}

	// Filter each column to only include children
	return tasksByColumn.map((column) => column.filter((task) => childIds.has(task.id)))
})

export const allTasksAtom = Atom.readable((get) => {
	const tasksByColumn = get(drillDownFilteredTasksAtom)
	return tasksByColumn.flat()
})

export const activeSessionsCountAtom = Atom.readable((get) => {
	const allTasks = get(allTasksAtom)
	return allTasks.filter((t) => t.sessionState === "busy" || t.sessionState === "waiting").length
})

export const totalTasksCountAtom = Atom.readable((get) => {
	const allTasks = get(allTasksAtom)
	return allTasks.length
})

export const maxVisibleTasksAtom = Atom.readable((get) => {
	const drillDownEpicId = get(drillDownEpicAtom)
	const CHROME_HEIGHT = 9
	const TASK_CARD_HEIGHT = 4
	const rows = process.stdout.rows || 24
	const baseMax = Math.max(1, Math.floor((rows - CHROME_HEIGHT) / TASK_CARD_HEIGHT))
	return Result.isSuccess(drillDownEpicId) && drillDownEpicId.value ? baseMax - 1 : baseMax
})
