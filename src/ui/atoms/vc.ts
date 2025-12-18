/**
 * VC (Virtual Coordinator) Status Atoms
 *
 * Manages VC auto-pilot status polling and control.
 */

import { Effect, pipe, Schedule, SubscriptionRef } from "effect"
import { type VCExecutorInfo, VCService } from "../../core/VCService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// VC Status Atoms (scoped polling with SubscriptionRef)
// ============================================================================

const VC_STATUS_INITIAL: VCExecutorInfo = {
	status: "stopped",
	sessionName: "vc-autopilot",
}

/**
 * SubscriptionRef that holds the current VC status
 *
 * This is the single source of truth for VC status.
 * Updated by both the poller and toggle actions.
 */
export const vcStatusRefAtom = appRuntime.atom(
	SubscriptionRef.make<VCExecutorInfo>(VC_STATUS_INITIAL),
	{ initialValue: undefined },
)

/**
 * Scoped poller that updates vcStatusRefAtom every 5 seconds
 *
 * The polling fiber is automatically interrupted when the atom unmounts
 * because effect-atom provides the Scope.
 */
export const vcStatusPollerAtom = appRuntime.atom(
	(get) =>
		Effect.gen(function* () {
			const vc = yield* VCService
			const ref = yield* get.result(vcStatusRefAtom)

			// Get initial status immediately
			const initial = yield* vc.getStatus()
			yield* SubscriptionRef.set(ref, initial)

			// Fork polling loop - scoped by effect-atom, auto-interrupted on unmount
			yield* Effect.scheduleForked(Schedule.spaced("5 seconds"))(
				vc.getStatus().pipe(
					Effect.flatMap((status) => SubscriptionRef.set(ref, status)),
					Effect.catchAll(() => Effect.void), // Don't crash on transient errors
				),
			)
		}),
	{ initialValue: undefined },
)

/**
 * Read-only atom that subscribes to VC status changes
 *
 * Streams the SubscriptionRef's changes so UI updates reactively.
 *
 * Usage: const vcStatus = useAtomValue(vcStatusAtom)
 */
export const vcStatusAtom = appRuntime.subscriptionRef((get) => pipe(get.result(vcStatusRefAtom)))

// ============================================================================
// VC Auto-Pilot Action Atoms
// ============================================================================

/**
 * Toggle VC auto-pilot mode
 *
 * If running, stops it. If stopped, starts it.
 * Updates the vcStatusRefAtom immediately so UI reflects the change.
 *
 * Usage: const [, toggleVCAutoPilot] = useAtom(toggleVCAutoPilotAtom, { mode: "promise" })
 *        await toggleVCAutoPilot()
 */
export const toggleVCAutoPilotAtom = appRuntime.fn((_: undefined, get) =>
	Effect.gen(function* () {
		const vc = yield* VCService
		const newStatus = yield* vc.toggleAutoPilot()

		// Update the ref immediately so UI reflects the change
		const ref = yield* get.result(vcStatusRefAtom)
		yield* SubscriptionRef.set(ref, newStatus)

		return newStatus
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Send a command to the VC REPL
 *
 * Usage: const sendVCCommand = useAtom(sendVCCommandAtom, { mode: "promise" })
 *        await sendVCCommand("What's ready to work on?")
 */
export const sendVCCommandAtom = appRuntime.fn((command: string) =>
	Effect.gen(function* () {
		const vcService = yield* VCService
		yield* vcService.sendCommand(command)
	}).pipe(Effect.catchAll(Effect.logError)),
)
