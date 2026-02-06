/**
 * ForkOverlay - Modal dialog for forking a bead
 *
 * Options:
 * - 1: Convert current bead to epic, then create child
 * - 2: Create new parent epic, reparent current, then create child
 * - Esc: Cancel
 *
 * Keyboard handling is in InputHandlersService.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { currentOverlayAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1
const ATTR_DIM = 2

export const ForkOverlay = () => {
	const currentOverlay = useAtomValue(currentOverlayAtom)
	const forkOverlay = currentOverlay?._tag === "fork" ? currentOverlay : undefined

	const sourceTaskId = forkOverlay?.sourceTaskId ?? ""
	const sourceTaskTitle = forkOverlay?.sourceTaskTitle ?? ""
	const blockedReason = forkOverlay?.blockedReason
	const isBlocked = Boolean(blockedReason)

	const optionColor = isBlocked ? theme.overlay0 : theme.green
	const optionAttrs = isBlocked ? ATTR_DIM : 0

	const modalWidth = 64

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
				borderColor={theme.lavender}
				backgroundColor={theme.surface0}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={modalWidth}
				flexDirection="column"
			>
				<box>
					<text fg={theme.lavender} attributes={ATTR_BOLD}>
						Fork bead {sourceTaskId}
						{"\n"}
					</text>
				</box>

				{sourceTaskTitle && (
					<box marginTop={1}>
						<text fg={theme.subtext0}>{sourceTaskTitle}</text>
					</box>
				)}

				{blockedReason && (
					<box marginTop={1}>
						<text fg={theme.yellow}>{blockedReason}</text>
					</box>
				)}

				<box marginTop={2} flexDirection="column">
					<box>
						<text fg={optionColor} attributes={optionAttrs}>
							1
						</text>
						<text fg={theme.overlay0} attributes={optionAttrs}>
							: Convert to epic + create child
						</text>
					</box>
					<box marginTop={0}>
						<text fg={optionColor} attributes={optionAttrs}>
							2
						</text>
						<text fg={theme.overlay0} attributes={optionAttrs}>
							: Create new parent epic
						</text>
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
