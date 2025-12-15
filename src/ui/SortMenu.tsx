/**
 * SortMenu component - sort options popover (bottom-right, like ActionPalette)
 */
import type { SortConfig, SortField } from "../services/EditorService"
import { theme } from "./theme"

export interface SortMenuProps {
	currentSort: SortConfig
}

const ATTR_BOLD = 1
const ATTR_DIM = 2

/**
 * Get display name for sort field
 */
const getFieldName = (field: SortField): string => {
	switch (field) {
		case "session":
			return "Session"
		case "priority":
			return "Priority"
		case "updated":
			return "Updated"
	}
}

/**
 * Get direction indicator
 */
const getDirectionIndicator = (field: SortField, currentSort: SortConfig): string => {
	if (currentSort.field !== field) return " "
	return currentSort.direction === "desc" ? "↓" : "↑"
}

/**
 * SortMenu component
 *
 * Displays a small floating panel in the bottom-right corner showing sort options.
 * Non-intrusive design like ActionPalette.
 */
export const SortMenu = (props: SortMenuProps) => {
	const { currentSort } = props

	// Sort option line component
	const SortLine = ({ keyName, field }: { keyName: string; field: SortField }) => {
		const isActive = currentSort.field === field
		const fgColor = isActive ? theme.lavender : theme.text
		const keyColor = isActive ? theme.green : theme.mauve
		const attrs = isActive ? ATTR_BOLD : 0
		const dirIndicator = getDirectionIndicator(field, currentSort)

		return (
			<box flexDirection="row">
				<text fg={keyColor} attributes={attrs}>
					{keyName}
				</text>
				<text fg={fgColor} attributes={attrs}>
					{` ${getFieldName(field)}`}
				</text>
				<text fg={theme.yellow} attributes={attrs}>
					{` ${dirIndicator}`}
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
				<text fg={theme.subtext0} attributes={ATTR_DIM}>
					Sort by:
				</text>
				<text fg={theme.surface1}>{"─────────"}</text>

				{/* Sort options */}
				<SortLine keyName="s" field="session" />
				<SortLine keyName="p" field="priority" />
				<SortLine keyName="u" field="updated" />

				<text fg={theme.surface1}>{"─────────"}</text>
				<box flexDirection="row">
					<text fg={theme.overlay0}>Esc</text>
					<text fg={theme.subtext0}> cancel</text>
				</box>
			</box>
		</box>
	)
}
