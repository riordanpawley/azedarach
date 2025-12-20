/**
 * Break Into Epic Atoms
 *
 * Atoms for the break-into-epic overlay state.
 * State is managed by BreakIntoEpicService; these atoms expose it reactively.
 */

import { Effect } from "effect"
import { BreakIntoEpicService } from "../../core/BreakIntoEpicService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// State Atoms
// ============================================================================

/**
 * Break into epic overlay state.
 * Subscribe to this in BreakIntoEpicOverlay for reactive updates.
 */
export const breakIntoEpicStateAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const service = yield* BreakIntoEpicService
		return service.overlayState
	}),
)
