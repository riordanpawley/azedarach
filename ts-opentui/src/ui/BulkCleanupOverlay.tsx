/**
 * BulkCleanupOverlay - Modal dialog for bulk worktree cleanup
 *
 * Displays when multiple tasks are selected and cleanup is requested.
 * Offers two choices:
 * - w: Worktree only - delete worktrees/sessions but keep beads open
 * - f: Full cleanup - delete worktrees AND close beads
 * - Esc: Cancel
 *
 * Note: Keyboard handling is in InputHandlersService, this component just renders.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { currentOverlayAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * BulkCleanupOverlay component
 *
 * Two-option dialog for bulk cleanup decision. Press:
 * - 'w' to delete worktrees only (keep beads open)
 * - 'f' to do full cleanup (delete worktrees and close beads)
 * - 'Esc' to cancel
 *
 * All keyboard handling is in the Effect layer (InputHandlersService).
 */
export const BulkCleanupOverlay = () => {
	const currentOverlay = useAtomValue(currentOverlayAtom)

	// Extract data from bulkCleanup overlay
	const taskIds = currentOverlay?._tag === "bulkCleanup" ? currentOverlay.taskIds : []
	const count = taskIds.length

	// Show up to 5 task IDs, then "..."
	const displayIds = taskIds.slice(0, 5)
	const hasMore = taskIds.length > 5
	const taskListStr = displayIds.join(", ") + (hasMore ? `, +${taskIds.length - 5} more` : "")

	const modalWidth = 60

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			alignItems="center"
			justifyContent="center"
			backgroundColor={`${theme.crust}CC`}
		>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.peach}
				backgroundColor={theme.surface0}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={modalWidth}
				flexDirection="column"
			>
				{/* Title */}
				<box>
					<text fg={theme.peach} attributes={ATTR_BOLD}>
						Cleanup {count} Worktree{count === 1 ? "" : "s"}?{"\n"}
					</text>
				</box>

				{/* Task list */}
				<box marginTop={1}>
					<text fg={theme.subtext0}>{taskListStr}</text>
				</box>

				{/* Warning */}
				<box marginTop={1}>
					<text fg={theme.yellow}>All uncommitted changes will be lost.</text>
				</box>

				{/* Options */}
				<box marginTop={2} flexDirection="column">
					<box>
						<text fg={theme.green}>w</text>
						<text fg={theme.overlay0}>: Worktrees only </text>
						<text fg={theme.subtext0}>(keep beads open)</text>
					</box>
					<box marginTop={0}>
						<text fg={theme.blue}>f</text>
						<text fg={theme.overlay0}>: Full cleanup </text>
						<text fg={theme.subtext0}>(close beads too)</text>
					</box>
					<box marginTop={0}>
						<text fg={theme.red}>Esc</text>
						<text fg={theme.overlay0}>: Cancel</text>
					</box>
				</box>
			</box>
		</box>
	)
}
