/**
 * Navigation Atoms
 *
 * Handles cursor navigation and position tracking.
 */

import { Effect } from "effect"
import { BeadsClient } from "../../core/BeadsClient.js"
import { NavigationService } from "../../services/NavigationService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Navigation State Atoms
// ============================================================================

/**
 * Focused task ID atom - subscribes to NavigationService focusedTaskId
 *
 * This is the source of truth for which task is selected.
 * Position (columnIndex, taskIndex) is derived in useNavigation.
 *
 * Usage: const focusedTaskId = useAtomValue(focusedTaskIdAtom)
 */
export const focusedTaskIdAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.focusedTaskId
	}),
)

// ============================================================================
// Navigation Action Atoms
// ============================================================================

/**
 * Initialize navigation - ensures a task is focused
 *
 * Called when the app starts or when no task is focused.
 * Sets focusedTaskId to the first available task.
 *
 * Usage: const initNav = useAtomSet(initializeNavigationAtom, { mode: "promise" })
 *        initNav()
 */
export const initializeNavigationAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.initialize()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Navigate cursor atom - move cursor in a direction
 *
 * Usage: const [, navigate] = useAtom(navigateAtom, { mode: "promise" })
 *        await navigate("down")
 */
export const navigateAtom = appRuntime.fn((direction: "up" | "down" | "left" | "right") =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.move(direction)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Jump to position atom - jump cursor to specific column/task
 *
 * Usage: const [, jumpTo] = useAtom(jumpToAtom, { mode: "promise" })
 *        await jumpTo({ column: 0, task: 5 })
 */
export const jumpToAtom = appRuntime.fn(({ column, task }: { column: number; task: number }) =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.jumpTo(column, task)
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Jump to task by ID - move cursor directly to a specific task
 *
 * Useful after creating a bead when you know the ID but not the position.
 *
 * Usage: const [, jumpToTask] = useAtom(jumpToTaskAtom, { mode: "promise" })
 *        await jumpToTask("az-123")
 */
export const jumpToTaskAtom = appRuntime.fn((taskId: string) =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.jumpToTask(taskId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Epic Drill-Down Atoms
// ============================================================================

/**
 * Drill-down epic atom - subscribes to NavigationService drillDownEpic
 *
 * When set, the board shows only children of this epic.
 * When null, normal board view is shown.
 *
 * Usage: const drillDownEpic = useAtomValue(drillDownEpicAtom)
 */
export const drillDownEpicAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.drillDownEpic
	}),
)

/**
 * Drill-down child IDs atom - subscribes to NavigationService drillDownChildIds
 *
 * Contains the set of task IDs to show when in drill-down mode.
 *
 * Usage: const childIds = useAtomValue(drillDownChildIdsAtom)
 */
export const drillDownChildIdsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.drillDownChildIds
	}),
)

/**
 * Enter drill-down mode for an epic
 *
 * Takes an object with epicId and childIds since appRuntime.fn supports single arg.
 * Note: The normal keybinding handles drill-down entry directly - this is for
 * programmatic use if needed.
 */
export const enterDrillDownAtom = appRuntime.fn(
	(params: { epicId: string; childIds: ReadonlySet<string> }) =>
		Effect.gen(function* () {
			const nav = yield* NavigationService
			yield* nav.enterDrillDown(params.epicId, params.childIds)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Exit drill-down mode
 *
 * Usage: const exitDrillDown = useAtomSet(exitDrillDownAtom, { mode: "promise" })
 *        await exitDrillDown()
 */
export const exitDrillDownAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const nav = yield* NavigationService
		yield* nav.exitDrillDown()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Get epic children - fetches children for the current drill-down epic
 *
 * Usage: const epicChildren = useAtomValue(epicChildrenAtom(epicId))
 */
export const getEpicChildrenAtom = appRuntime.fn((epicId: string) =>
	Effect.gen(function* () {
		const beads = yield* BeadsClient
		return yield* beads.getEpicChildren(epicId)
	}).pipe(Effect.catchAll((e) => Effect.logError(e).pipe(Effect.as([])))),
)

/**
 * Epic info atom - fetches epic details for the header
 *
 * Usage: const epic = useAtomValue(epicInfoAtom(epicId))
 */
export const getEpicInfoAtom = appRuntime.fn((epicId: string) =>
	Effect.gen(function* () {
		const beads = yield* BeadsClient
		return yield* beads.show(epicId)
	}).pipe(
		Effect.catchAll((e) =>
			Effect.logError(e).pipe(
				Effect.as({
					id: epicId,
					title: "Unknown Epic",
					status: "open" as const,
					priority: 2,
					issue_type: "epic" as const,
					created_at: "",
					updated_at: "",
				}),
			),
		),
	),
)
