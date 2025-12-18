/**
 * HelpOverlay component - modal showing all keybindings
 */
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * Helper component for key hint lines
 */
const KeyLine = ({ keys, description }: { keys: string; description: string }) => (
	<box flexDirection="row">
		<text fg={theme.lavender}>{`  ${keys}`}</text>
		<text fg={theme.text}>{" ".repeat(Math.max(1, 12 - keys.length)) + description}</text>
	</box>
)

/**
 * Section header component
 */
const SectionHeader = ({ title }: { title: string }) => (
	<text fg={theme.blue} attributes={ATTR_BOLD}>
		{title}
	</text>
)

/**
 * HelpOverlay component
 *
 * Displays a centered modal overlay with all available keybindings
 * grouped by category. Uses Catppuccin theme colors.
 */
export const HelpOverlay = () => {
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
				borderColor={theme.mauve}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={60}
				flexDirection="column"
			>
				{/* Header */}
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"  KEYBINDINGS HELP"}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text> </text>

				{/* Navigation section */}
				<SectionHeader title="Navigation:" />
				<KeyLine keys="h j k l" description="Left / Down / Up / Right" />
				<KeyLine keys="Ctrl-d/u" description="Half page down / up" />
				<KeyLine keys="gg" description="Go to first task" />
				<KeyLine keys="ge" description="Go to last task" />
				<KeyLine keys="gh" description="Go to first column" />
				<KeyLine keys="gl" description="Go to last column" />
				<KeyLine keys="gw" description="Jump mode (shows labels)" />
				<text> </text>

				{/* Modes section */}
				<SectionHeader title="Modes:" />
				<KeyLine keys="v" description="Enter select mode (multi-select)" />
				<KeyLine keys="Space" description="Enter action mode (command menu)" />
				<KeyLine keys="g" description="Enter goto mode (prefix)" />
				<text> </text>

				{/* Select mode section */}
				<SectionHeader title="Select Mode:" />
				<KeyLine keys="Space" description="Toggle selection of current task" />
				<KeyLine keys="v" description="Exit select mode" />
				<text> </text>

				{/* Action mode section */}
				<SectionHeader title="Action Mode:" />
				<KeyLine keys="h / l" description="Move task(s) to prev / next column" />
				<KeyLine keys="s / S" description="Start session / Start+work" />
				<KeyLine keys="!" description="Start+work (skip permissions)" />
				<KeyLine keys="a" description="Attach to session" />
				<KeyLine keys="p" description="Pause session" />
				<KeyLine keys="D" description="Delete bead permanently" />
				<text> </text>

				{/* Create/Edit section */}
				<SectionHeader title="Create & Edit:" />
				<KeyLine keys="c" description="Create bead via $EDITOR" />
				<KeyLine keys="C" description="Create bead via Claude (AI)" />
				<KeyLine keys="e" description="Edit bead via $EDITOR" />
				<KeyLine keys="E" description="Edit bead via Claude (AI)" />
				<text> </text>

				{/* General section */}
				<SectionHeader title="General:" />
				<KeyLine keys="Enter" description="Show task details" />
				<KeyLine keys="q" description="Quit application" />
				<KeyLine keys="?" description="Toggle this help screen" />
				<KeyLine keys="Esc" description="Back to normal mode / dismiss" />
				<text> </text>

				{/* Footer */}
				<text fg={theme.subtext0}>{"Press any key to dismiss..."}</text>
			</box>
		</box>
	)
}
