/**
 * ElapsedTimer component - displays MM:SS elapsed time since a start timestamp
 *
 * Uses ClockService via clockTickAtom for efficient updates - a single
 * 1-second interval shared by all timer instances.
 */

import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { clockTickAtom } from "./atoms"
import { theme } from "./theme"

export interface ElapsedTimerProps {
	/** ISO 8601 timestamp when the session started */
	startedAt: string
	/** Optional text color (defaults to overlay0) */
	color?: string
}

/**
 * Format elapsed milliseconds as MM:SS
 *
 * Examples:
 * - 5000ms  -> "00:05"
 * - 65000ms -> "01:05"
 * - 3665000ms -> "61:05" (no hours, just large minutes)
 */
const formatElapsed = (elapsedMs: number): string => {
	const totalSeconds = Math.max(0, Math.floor(elapsedMs / 1000))
	const minutes = Math.floor(totalSeconds / 60)
	const seconds = totalSeconds % 60
	return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`
}

/**
 * ElapsedTimer component
 *
 * Displays a live-updating elapsed time counter. Subscribes to the
 * shared ClockService tick for efficient updates across all timers.
 */
export const ElapsedTimer = ({ startedAt, color = theme.overlay0 }: ElapsedTimerProps) => {
	const nowResult = useAtomValue(clockTickAtom)

	// Get current time from clock service, fallback to Date.now() if not ready
	const now = Result.isSuccess(nowResult) ? nowResult.value : Date.now()

	// Calculate elapsed time
	const startTime = new Date(startedAt).getTime()
	const elapsed = now - startTime

	return <text fg={color}>{formatElapsed(elapsed)}</text>
}
