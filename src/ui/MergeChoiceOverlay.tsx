/**
 * MergeChoiceOverlay - Modal dialog for merge choice when attaching to a session
 *
 * Displays when a worktree branch is behind main, offering three choices:
 * - m: Merge main into branch, then attach
 * - s: Skip merge and attach directly
 * - Esc: Cancel and return to board
 *
 * Note: Keyboard handling is in InputHandlersService, this component just renders.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { currentOverlayAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * MergeChoiceOverlay component
 *
 * Three-option dialog for merge decision. Press:
 * - 'm' to merge main into branch and attach
 * - 's' to skip merge and attach directly
 * - 'Esc' to cancel
 *
 * All keyboard handling is in the Effect layer (InputHandlersService).
 */
export const MergeChoiceOverlay = () => {
	const currentOverlay = useAtomValue(currentOverlayAtom)

	// Extract data from mergeChoice overlay
	const message =
		currentOverlay?._tag === "mergeChoice"
			? currentOverlay.message
			: "Branch is behind main. Merge latest?"

	const commitsBehind = currentOverlay?._tag === "mergeChoice" ? currentOverlay.commitsBehind : 0

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
				borderColor={theme.blue}
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
					<text fg={theme.blue} attributes={ATTR_BOLD}>
						↓ Branch Behind Main{"\n"}
					</text>
				</box>

				{/* Commits behind indicator */}
				<box marginTop={1}>
					<text fg={theme.yellow}>
						{commitsBehind} commit{commitsBehind === 1 ? "" : "s"} behind
					</text>
				</box>

				{/* Message */}
				<box marginTop={1}>
					<text fg={theme.text}>{message}</text>
				</box>

				{/* Options */}
				<box marginTop={2} flexDirection="column">
					<box>
						<text fg={theme.green}>m</text>
						<text fg={theme.overlay0}>: Merge & Attach </text>
						<text fg={theme.subtext0}>(pull latest main into branch)</text>
					</box>
					<box marginTop={0}>
						<text fg={theme.yellow}>s</text>
						<text fg={theme.overlay0}>: Skip & Attach </text>
						<text fg={theme.subtext0}>(attach without merging)</text>
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
