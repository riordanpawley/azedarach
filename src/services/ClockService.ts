/**
 * ClockService - provides a reactive clock tick for elapsed timer displays
 *
 * Emits the current timestamp every second via SubscriptionRef.
 * Components can subscribe to derive elapsed time from session start times.
 *
 * Uses a single interval for efficiency - all timers share the same tick.
 */

import { Effect, Schedule, SubscriptionRef } from "effect"

export class ClockService extends Effect.Service<ClockService>()("ClockService", {
	scoped: Effect.gen(function* () {
		// Current timestamp in milliseconds, updated every second
		const now = yield* SubscriptionRef.make<number>(Date.now())

		// Start the ticker - scoped by the runtime, auto-interrupted on shutdown
		yield* Effect.scheduleForked(Schedule.spaced("1 second"))(SubscriptionRef.set(now, Date.now()))

		return {
			/** Current timestamp SubscriptionRef - subscribe for reactive updates */
			now,
		}
	}),
}) {}
