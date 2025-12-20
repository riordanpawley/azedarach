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
				<KeyLine keys="Space" description="Enter action mode (command menu)" />
				<KeyLine keys="v" description="Enter select mode (multi-select)" />
				<KeyLine keys="g" description="Enter goto mode (prefix)" />
				<KeyLine keys="," description="Enter sort mode" />
				<KeyLine keys="f" description="Enter filter mode" />
				<KeyLine keys="/" description="Enter search mode" />
				<KeyLine keys=":" description="Enter command mode (VC REPL)" />
				<text> </text>

				{/* Select mode section */}
				<SectionHeader title="Select Mode:" />
				<KeyLine keys="Space" description="Toggle selection of current task" />
				<KeyLine keys="v" description="Exit select mode" />
				<text> </text>

				{/* Action mode section */}
				<SectionHeader title="Action Mode (Space+):" />
				<KeyLine keys="s / S / !" description="Start / Start+work / Yolo" />
				<KeyLine keys="a / A" description="Attach external / Attach inline" />
				<KeyLine keys="p / r / x" description="Pause / Resume / Stop session" />
				<KeyLine keys="c" description="Chat (Haiku)" />
				<KeyLine keys="h / l" description="Move task(s) left / right" />
				<KeyLine keys="e / E" description="Edit ($EDITOR) / Edit (Claude)" />
				<KeyLine keys="i" description="Attach image" />
				<KeyLine keys="f" description="Show diff vs main" />
				<KeyLine keys="P" description="Create PR" />
				<KeyLine keys="m / M" description="Merge to main / Abort merge" />
				<KeyLine keys="d / D" description="Delete worktree / Delete bead" />
				<text> </text>

				{/* Create/Edit section */}
				<SectionHeader title="Create & Edit:" />
				<KeyLine keys="c / C" description="Create bead ($EDITOR / Claude)" />
				<KeyLine keys="Space+e/E" description="Edit bead ($EDITOR / Claude)" />
				<text> </text>

				{/* Sort mode section */}
				<SectionHeader title="Sort Mode (,):" />
				<KeyLine keys="s" description="Sort by session status" />
				<KeyLine keys="p" description="Sort by priority" />
				<KeyLine keys="u" description="Sort by updated at" />
				<text> </text>

				{/* Filter mode section */}
				<SectionHeader title="Filter Mode (f):" />
				<KeyLine keys="s/p/t/S" description="Status / Priority / Type / Session" />
				<KeyLine keys="0-4" description="Toggle P0-P4 priority" />
				<KeyLine keys="o/i/b/d" description="Open / In progress / Blocked / Closed" />
				<KeyLine keys="e" description="Toggle hide epic children" />
				<KeyLine keys="c" description="Clear all filters" />
				<text> </text>

				{/* General section */}
				<SectionHeader title="General:" />
				<KeyLine keys="Enter" description="Show task details / Enter epic" />
				<KeyLine keys="Tab" description="Toggle view (Kanban / Compact)" />
				<KeyLine keys="a" description="Toggle VC auto-pilot" />
				<KeyLine keys="d" description="Show diagnostics" />
				<KeyLine keys="L" description="View logs" />
				<KeyLine keys="?" description="Toggle this help screen" />
				<KeyLine keys="q" description="Quit (or exit drill-down)" />
				<KeyLine keys="Esc" description="Back to normal mode / dismiss" />
				<text> </text>

				{/* Footer */}
				<text fg={theme.subtext0}>{"Press any key to dismiss..."}</text>
			</box>
		</box>
	)
}
