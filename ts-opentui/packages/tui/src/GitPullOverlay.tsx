/**
 * GitPullOverlay - Modal notification for available git updates
 *
 * Displays commit count and accepts y/n key input.
 * Used to prompt user to pull when origin has new commits.
 *
 * Note: Keyboard handling is in KeyboardService, this component just renders.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { currentOverlayAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * GitPullOverlay component
 *
 * Simple y/n confirmation for pulling updates from origin.
 * Press 'y' or Enter to pull, 'n' or Escape to dismiss.
 * Gets commit count from current overlay state.
 * All keyboard handling is in the Effect layer (KeyboardService).
 */
export const GitPullOverlay = () => {
	const currentOverlay = useAtomValue(currentOverlayAtom)

	// Extract data from gitPull overlay
	if (currentOverlay?._tag !== "gitPull") {
		return null
	}

	const { commitsBehind, baseBranch, remote } = currentOverlay

	const commitText = commitsBehind === 1 ? "1 new commit" : `${commitsBehind} new commits`
	const branchRef = `${remote}/${baseBranch}`

	const modalWidth = 55

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
						↓ Updates Available{"\n"}
					</text>
				</box>

				{/* Message */}
				<box marginTop={1} flexDirection="column">
					<text fg={theme.text}>
						<span fg={theme.mauve}>{branchRef}</span> has {commitText}
					</text>
					<text fg={theme.overlay0}>Pull now to update your local base branch?</text>
				</box>

				{/* Help text */}
				<box marginTop={2}>
					<text fg={theme.green}>y</text>
					<text fg={theme.overlay0}>/</text>
					<text fg={theme.green}>Enter</text>
					<text fg={theme.overlay0}>: pull </text>
					<text fg={theme.red}>n</text>
					<text fg={theme.overlay0}>/</text>
					<text fg={theme.red}>Esc</text>
					<text fg={theme.overlay0}>: dismiss</text>
				</box>
			</box>
		</box>
	)
}
