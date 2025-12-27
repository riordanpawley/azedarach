/**
 * Planning Atoms
 *
 * Handles AI-powered task planning workflow state and actions.
 */

import { Effect } from "effect"
import { PlanningService } from "../../core/PlanningService.js"
import { BoardService } from "../../services/BoardService.js"
import { appRuntime } from "./runtime.js"

// Re-export types for consumers
export type { Plan, PlanningState, PlannedTask, ReviewFeedback } from "../../core/PlanningService.js"

// ============================================================================
// Planning State Atom
// ============================================================================

/**
 * Planning state atom - subscribes to PlanningService state
 *
 * Provides reactive access to planning workflow state including:
 * - Current status (idle, generating, reviewing, etc.)
 * - Current plan being worked on
 * - Review pass progress
 * - Created beads
 *
 * Usage: const planningState = useAtomValue(planningStateAtom)
 */
export const planningStateAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const planning = yield* PlanningService
		return planning.state
	}),
)

// ============================================================================
// Planning Actions
// ============================================================================

/**
 * Run the complete planning workflow
 *
 * Takes a feature description and:
 * 1. Generates initial plan with AI
 * 2. Reviews and refines up to 5 times
 * 3. Creates beads from the final plan
 *
 * Usage: const runPlanning = useSetAtom(runPlanningAtom)
 *        runPlanning("Add user authentication feature")
 */
export const runPlanningAtom = appRuntime.fn((featureDescription: string) =>
	Effect.gen(function* () {
		const planning = yield* PlanningService
		const board = yield* BoardService

		const createdBeads = yield* planning.runPlanningWorkflow(featureDescription)

		// Refresh the board to show new beads
		yield* board.refresh()

		return createdBeads
	}).pipe(
		Effect.catchAll((error) =>
			Effect.gen(function* () {
				yield* Effect.logError("Planning workflow failed", error)
				return []
			}),
		),
	),
)

/**
 * Reset planning state to initial
 *
 * Clears all planning state and returns to idle.
 *
 * Usage: const resetPlanning = useSetAtom(resetPlanningAtom)
 *        resetPlanning()
 */
export const resetPlanningAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const planning = yield* PlanningService
		yield* planning.reset()
	}),
)
