/**
 * ClockService - provides a reactive clock tick for elapsed timer displays
 *
 * Emits the current DateTime.Utc every second via SubscriptionRef.
 * Components can subscribe to derive elapsed time from session start times.
 *
 * Uses a single interval for efficiency - all timers share the same tick.
 * Uses Effect's DateTime for consistent time handling via the Clock service.
 */

import { DateTime, Effect, Schedule, SubscriptionRef } from "effect"

/**
 * Format elapsed milliseconds as MM:SS
 *
 * Examples:
 * - 5000ms  -> "00:05"
 * - 65000ms -> "01:05"
 * - 3665000ms -> "61:05" (no hours, just large minutes)
 */
export const formatElapsedMs = (elapsedMs: number): string => {
	const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1000))
	const minutes = Math.floor(totalSeconds / 60)
	const seconds = totalSeconds % 60
	return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`
}

/**
 * Compute elapsed milliseconds between a start time and current time
 */
export const computeElapsedMs = (startedAt: string, now: DateTime.Utc): number => {
	const start = DateTime.unsafeMake(startedAt)
	return DateTime.distance(start, now)
}

/**
 * Compute formatted elapsed time string (MM:SS) from a start timestamp
 */
export const computeElapsedFormatted = (startedAt: string, now: DateTime.Utc): string => {
	return formatElapsedMs(computeElapsedMs(startedAt, now))
}

export class ClockService extends Effect.Service<ClockService>()("ClockService", {
	scoped: Effect.gen(function* () {
		// Get initial timestamp using Effect's DateTime (uses Clock service)
		const initial = yield* DateTime.now

		// Current DateTime.Utc, updated every second
		const now = yield* SubscriptionRef.make<DateTime.Utc>(initial)

		// Start the ticker - scoped by the runtime, auto-interrupted on shutdown
		yield* Effect.scheduleForked(Schedule.spaced("1 second"))(
			Effect.flatMap(DateTime.now, (dt) => SubscriptionRef.set(now, dt)),
		)

		return {
			/** Current DateTime.Utc SubscriptionRef - subscribe for reactive updates */
			now,
		}
	}),
}) {}
