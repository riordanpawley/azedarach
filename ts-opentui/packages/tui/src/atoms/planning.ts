/**
 * Planning Atoms
 *
 * Handles AI-powered task planning workflow state and actions.
 */

import { Effect } from "effect"
import type { Plan, PlannedTask, PlanningState, ReviewFeedback } from "../contracts.js"
import { TuiBoardStoreService } from "../services/TuiBoardStoreService.js"
import { PlanningService } from "../utils/runtimeServices.js"
import { appRuntime } from "./runtime.js"

// Re-export types for consumers
export type { Plan, PlannedTask, PlanningState, ReviewFeedback }

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
 * - Created tracker
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
 * 3. Creates issues from the final plan
 *
 * Usage: const runPlanning = useAtomSet(runPlanningAtom)
 *        runPlanning("Add user authentication feature")
 */
export const runPlanningAtom = appRuntime.fn((featureDescription: string) =>
	Effect.gen(function* () {
		const planning = yield* PlanningService
		const board = yield* TuiBoardStoreService

		const createdIssues = yield* planning.runPlanningWorkflow(featureDescription)
		const epicId = createdIssues.find((issue) => issue.issue_type === "epic")?.id
		for (const issue of createdIssues) {
			yield* board.upsertIssueFromMutation(issue, {
				parentEpicId: epicId && issue.id !== epicId ? epicId : undefined,
			})
		}

		return createdIssues
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
 * Usage: const resetPlanning = useAtomSet(resetPlanningAtom)
 *        resetPlanning()
 */
export const resetPlanningAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const planning = yield* PlanningService
		yield* planning.reset()
	}),
)
