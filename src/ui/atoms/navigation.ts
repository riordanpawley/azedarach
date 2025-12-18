/**
 * Navigation Atoms
 *
 * Handles cursor navigation and position tracking.
 */

import { Effect } from "effect"
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
