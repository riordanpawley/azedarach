/**
 * Navigation Atoms
 *
 * Handles cursor navigation and position tracking.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect } from "effect"
import { BeadsClient } from "../../core/BeadsClient.js"
import {
	computeDependencyPhases,
	type PhaseComputationResult,
} from "../../core/dependencyPhases.js"
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

// ============================================================================
// Dependency Phase Atoms
// ============================================================================

/**
 * Drill-down child details atom - subscribes to NavigationService drillDownChildDetails
 *
 * Contains full Issue objects for each child (needed for phase computation).
 *
 * Usage: const childDetails = useAtomValue(drillDownChildDetailsAtom)
 */
export const drillDownChildDetailsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const nav = yield* NavigationService
		return nav.drillDownChildDetails
	}),
)

/**
 * Empty phase result for when not in drill-down or no child details
 */
const EMPTY_PHASES: PhaseComputationResult = {
	phases: new Map(),
	maxPhase: 0,
	phaseCounts: new Map(),
}

/**
 * Drill-down phases atom - computed from child IDs and details
 *
 * Uses Kahn's algorithm to compute which tasks can be worked in parallel
 * and which are blocked by other tasks.
 *
 * Usage: const phases = useAtomValue(drillDownPhasesAtom)
 *        const taskPhase = phases.phases.get(taskId)
 */
export const drillDownPhasesAtom = Atom.readable<PhaseComputationResult>((get) => {
	// Get child IDs and details from subscriptionRef atoms
	const childIdsResult = get(drillDownChildIdsAtom)
	const childDetailsResult = get(drillDownChildDetailsAtom)

	// If either is not ready or not in drill-down, return empty
	if (!Result.isSuccess(childIdsResult) || !Result.isSuccess(childDetailsResult)) {
		return EMPTY_PHASES
	}

	const childIds = childIdsResult.value
	const childDetails = childDetailsResult.value

	// Not in drill-down mode
	if (childIds.size === 0) {
		return EMPTY_PHASES
	}

	// No details available (shouldn't happen, but handle gracefully)
	if (childDetails.size === 0) {
		// Return all tasks in phase 1 (no blocking info available)
		const phases = new Map<string, { phase: number; blockedBy: readonly string[] }>()
		for (const id of childIds) {
			phases.set(id, { phase: 1, blockedBy: [] })
		}
		return {
			phases,
			maxPhase: 1,
			phaseCounts: new Map([[1, childIds.size]]),
		}
	}

	// Compute phases using Kahn's algorithm
	return computeDependencyPhases(childIds, childDetails)
})

/**
 * Get phase info for a specific task
 *
 * Returns phase number and blockedBy list, or undefined if task not in drill-down.
 *
 * Usage: const phaseInfo = useAtomValue(taskPhaseInfoAtom(taskId))
 */
export const taskPhaseInfoAtom = (taskId: string) =>
	Atom.readable((get) => {
		const phases = get(drillDownPhasesAtom)
		return phases.phases.get(taskId)
	})

/**
 * Check if a task is blocked (phase > 1)
 *
 * Usage: const isBlocked = useAtomValue(isTaskBlockedAtom(taskId))
 */
export const isTaskBlockedAtom = (taskId: string) =>
	Atom.readable((get) => {
		const phaseInfo = get(taskPhaseInfoAtom(taskId))
		return phaseInfo !== undefined && phaseInfo.phase > 1
	})

/**
 * Get blocker titles for a blocked task
 *
 * Usage: const blockerTitles = useAtomValue(blockerTitlesAtom(taskId))
 */
export const blockerTitlesAtom = (taskId: string) =>
	Atom.readable((get) => {
		const phasesResult = get(drillDownPhasesAtom)
		const childDetailsResult = get(drillDownChildDetailsAtom)

		const phaseInfo = phasesResult.phases.get(taskId)
		if (!phaseInfo || phaseInfo.blockedBy.length === 0) {
			return []
		}

		if (!Result.isSuccess(childDetailsResult)) {
			return phaseInfo.blockedBy // Return IDs if details not available
		}

		const childDetails = childDetailsResult.value
		return phaseInfo.blockedBy.map((blockerId) => {
			const blocker = childDetails.get(blockerId)
			return blocker?.title ?? blockerId
		})
	})
