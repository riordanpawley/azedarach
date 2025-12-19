/**
 * EpicHeader - Header bar shown during epic drill-down view
 *
 * Displays:
 * - Back navigation hint (q to exit)
 * - Epic ID and title
 * - Progress bar showing completed/total children
 */

import type { DependencyRef, Issue } from "../core/BeadsClient.js"
import { theme } from "./theme.js"

interface EpicHeaderProps {
	/** The epic being viewed */
	epic: Issue
	/** Child tasks of the epic (for progress calculation) */
	epicChildren: DependencyRef[]
}

/**
 * Generate a progress bar string
 *
 * @param completed - Number of completed items
 * @param total - Total number of items
 * @param width - Width of the bar in characters
 */
const progressBar = (completed: number, total: number, width: number = 10): string => {
	if (total === 0) return "░".repeat(width)
	const filledCount = Math.round((completed / total) * width)
	const filled = "█".repeat(filledCount)
	const empty = "░".repeat(width - filledCount)
	return filled + empty
}

export const EpicHeader = ({ epic, epicChildren }: EpicHeaderProps) => {
	// Calculate progress from children
	const total = epicChildren.length
	const completed = epicChildren.filter((c) => c.status === "closed").length

	return (
		<box
			width="100%"
			height={1}
			flexDirection="row"
			paddingLeft={1}
			paddingRight={1}
			backgroundColor={theme.surface0}
		>
			{/* Back hint */}
			<box flexDirection="row">
				<text fg={theme.sky}>◀</text>
				<text fg={theme.subtext0}>{` ${epic.id}`}</text>
			</box>

			{/* Spacer */}
			<box flexGrow={1} />

			{/* Title (centered visually) */}
			<text fg={theme.text}>
				{epic.title.length > 40 ? `${epic.title.slice(0, 37)}...` : epic.title}
			</text>

			{/* Spacer */}
			<box flexGrow={1} />

			{/* Progress */}
			<box flexDirection="row">
				<text fg={completed === total && total > 0 ? theme.green : theme.yellow}>
					{progressBar(completed, total)}
				</text>
				<text fg={theme.subtext0}>{` ${completed}/${total}`}</text>
			</box>
		</box>
	)
}
