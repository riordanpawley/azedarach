/**
 * ActionPalette component - non-intrusive action menu (bottom-right, like Helix)
 */
import { theme } from "./theme"
import type { TaskWithSession } from "./types"

export interface ActionPaletteProps {
	task?: TaskWithSession
}

const ATTR_BOLD = 1
const ATTR_DIM = 2

/**
 * ActionPalette component
 *
 * Displays a small floating panel in the bottom-right corner showing available
 * actions. Non-intrusive design allows seeing the board while moving tasks.
 */
export const ActionPalette = (props: ActionPaletteProps) => {
	const sessionState = props.task?.sessionState ?? "idle"

	// Helper to check if an action is available
	const isAvailable = (action: string): boolean => {
		switch (action) {
			case "s": // Start - only if idle
				return sessionState === "idle"
			case "a": // Attach - only if not idle
				return sessionState !== "idle"
			case "p": // Pause - only if busy
				return sessionState === "busy"
			case "r": // Resume - only if paused
				return sessionState === "paused"
			case "x": // Stop - only if not idle
				return sessionState !== "idle"
			case "P": // Create PR - only if session has worktree (not idle)
				return sessionState !== "idle"
			case "d": // Cleanup/Delete worktree - only if session exists
				return sessionState !== "idle"
			case "D": // Delete bead - always available
				return true
			case "h": // Move left - always available
			case "l": // Move right - always available
				return true
			default:
				return false
		}
	}

	// Action line component
	const ActionLine = ({ keyName, description }: { keyName: string; description: string }) => {
		const available = isAvailable(keyName)
		const fgColor = available ? theme.text : theme.overlay0
		const keyColor = available ? theme.lavender : theme.overlay0
		const attrs = available ? 0 : ATTR_DIM

		return (
			<box flexDirection="row">
				<text fg={keyColor} attributes={attrs}>
					{keyName}
				</text>
				<text fg={fgColor} attributes={attrs}>
					{" " + description}
				</text>
			</box>
		)
	}

	return (
		<box position="absolute" right={1} bottom={4}>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.surface1}
				backgroundColor={theme.base}
				paddingLeft={1}
				paddingRight={1}
				flexDirection="column"
			>
				{/* Move actions - most common, at top */}
				<ActionLine keyName="h" description="← move" />
				<ActionLine keyName="l" description="→ move" />
				<text fg={theme.surface1}>{"─────────"}</text>

				{/* Session actions */}
				<ActionLine keyName="s" description="start" />
				<ActionLine keyName="a" description="attach" />
				<ActionLine keyName="p" description="pause" />
				<ActionLine keyName="r" description="resume" />
				<ActionLine keyName="x" description="stop" />
				<text fg={theme.surface1}>{"─────────"}</text>

				{/* Git/PR */}
				<ActionLine keyName="P" description="PR" />
				<ActionLine keyName="d" description="cleanup" />
				<ActionLine keyName="D" description="delete" />
			</box>
		</box>
	)
}
