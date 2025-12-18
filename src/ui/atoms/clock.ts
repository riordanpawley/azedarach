/**
 * Clock Atoms
 *
 * Handles time-based state for elapsed timers.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect } from "effect"
import { ClockService, computeElapsedFormatted } from "../../services/ClockService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Clock Atoms
// ============================================================================

/**
 * Clock tick atom - current DateTime.Utc updated every second
 *
 * Internal atom used by elapsedFormattedAtom. Components should use
 * elapsedFormattedAtom(startedAt) instead of this directly.
 */
export const clockTickAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const clock = yield* ClockService
		return clock.now
	}),
)

/**
 * Elapsed time atom factory - returns formatted MM:SS string
 *
 * Parameterized atom that derives elapsed time from clockTickAtom.
 * All computation happens in Effect - the atom returns a ready-to-render string.
 *
 * Usage: const elapsed = useAtomValue(elapsedFormattedAtom(startedAt))
 */
export const elapsedFormattedAtom = (startedAt: string) =>
	Atom.readable((get) => {
		const nowResult = get(clockTickAtom)
		if (!Result.isSuccess(nowResult)) return "00:00"
		return computeElapsedFormatted(startedAt, nowResult.value)
	})
