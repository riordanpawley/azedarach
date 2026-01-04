/**
 * PhaseSeparator component - horizontal divider for dependency phases
 *
 * Displayed between groups of tasks in a column during epic drill-down.
 * Phase 1 shows "Ready" to indicate unblocked tasks.
 */

import { theme } from "./theme.js"

export interface PhaseSeparatorProps {
	/** Phase number (1 = ready, 2+ = blocked by earlier phases) */
	phase: number
	/** Available width for the separator line */
	width?: number
}

/** Height of the phase separator in terminal rows */
export const PHASE_SEPARATOR_HEIGHT = 1

/**
 * PhaseSeparator component
 *
 * Renders a horizontal line with phase label:
 * - Phase 1: "── Ready ──" in green (tasks with no blockers)
 * - Phase 2+: "── Phase N ──" in dim text
 */
export const PhaseSeparator = ({ phase, width = 20 }: PhaseSeparatorProps) => {
	const isReady = phase === 1
	const label = isReady ? " Ready " : ` Phase ${phase} `

	// Calculate line widths (evenly split remaining space)
	const labelWidth = label.length
	const remainingWidth = Math.max(0, width - labelWidth)
	const leftWidth = Math.floor(remainingWidth / 2)
	const rightWidth = remainingWidth - leftWidth

	// Use ─ character for horizontal line
	const leftLine = "─".repeat(Math.max(1, leftWidth))
	const rightLine = "─".repeat(Math.max(1, rightWidth))

	// Ready phase gets green color, others get dim gray
	const color = isReady ? theme.green : theme.overlay0

	return (
		<box height={PHASE_SEPARATOR_HEIGHT} flexDirection="row">
			<text fg={color}>
				{leftLine}
				{label}
				{rightLine}
			</text>
		</box>
	)
}
