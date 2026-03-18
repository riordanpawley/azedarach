/**
 * ConfirmOverlay - Modal confirmation dialog for destructive actions
 *
 * Displays a message and accepts y/n key input.
 * Used for confirming merge operations that may have conflicts.
 *
 * Note: Keyboard handling is in KeyboardService, this component just renders.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { currentOverlayAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * ConfirmOverlay component
 *
 * Simple y/n confirmation dialog. Press 'y' or Enter to confirm,
 * 'n' or Escape to cancel. Gets message from current overlay state.
 * All keyboard handling is in the Effect layer (KeyboardService).
 */
export const ConfirmOverlay = () => {
	const currentOverlay = useAtomValue(currentOverlayAtom)

	// Extract message from confirm overlay
	const message =
		currentOverlay?._tag === "confirm"
			? currentOverlay.message
			: "Are you sure you want to proceed?"

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
				borderColor={theme.yellow}
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
					<text fg={theme.yellow} attributes={ATTR_BOLD}>
						⚠ Confirm Action{"\n"}
					</text>
				</box>

				{/* Message */}
				<box marginTop={1}>
					<text fg={theme.text}>{message}</text>
				</box>

				{/* Help text */}
				<box marginTop={2}>
					<text fg={theme.green}>y</text>
					<text fg={theme.overlay0}>/</text>
					<text fg={theme.green}>Enter</text>
					<text fg={theme.overlay0}>: confirm </text>
					<text fg={theme.red}>n</text>
					<text fg={theme.overlay0}>/</text>
					<text fg={theme.red}>Esc</text>
					<text fg={theme.overlay0}>: cancel</text>
				</box>
			</box>
		</box>
	)
}
