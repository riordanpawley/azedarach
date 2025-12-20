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
import { DiagnosticsService } from "./DiagnosticsService.js"

/**
 * Format elapsed milliseconds to human-readable string showing only the two largest units.
 * Never shows milliseconds.
 *
 * Examples:
 * - 5000ms     -> "5s"
 * - 65000ms    -> "1m 5s"
 * - 3665000ms  -> "1h 1m"
 * - 90061000ms -> "1d 1h"
 */
export const formatElapsedMs = (elapsedMs: number): string => {
	const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1000))

	const days = Math.floor(totalSeconds / 86400)
	const hours = Math.floor((totalSeconds % 86400) / 3600)
	const minutes = Math.floor((totalSeconds % 3600) / 60)
	const seconds = totalSeconds % 60

	// Show at most 2 units, largest first
	if (days > 0) {
		return hours > 0 ? `${days}d ${hours}h` : `${days}d`
	}
	if (hours > 0) {
		return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`
	}
	if (minutes > 0) {
		return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`
	}
	return `${seconds}s`
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
	dependencies: [DiagnosticsService.Default],
	scoped: Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		yield* diagnostics.trackService("ClockService", "1s clock tick for elapsed timers")

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
