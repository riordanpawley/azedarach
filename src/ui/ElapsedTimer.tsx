/**
 * ElapsedTimer component - displays MM:SS elapsed time since a start timestamp
 *
 * Pure render component. All logic lives in atoms/services.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { elapsedFormattedAtom } from "./atoms"
import { theme } from "./theme"

export interface ElapsedTimerProps {
	/** ISO 8601 timestamp when the session started */
	startedAt: string
	/** Optional text color (defaults to overlay0) */
	color?: string
}

export const ElapsedTimer = ({ startedAt, color = theme.overlay0 }: ElapsedTimerProps) => {
	const elapsed = useAtomValue(elapsedFormattedAtom(startedAt))

	return <text fg={color}>{elapsed}</text>
}
