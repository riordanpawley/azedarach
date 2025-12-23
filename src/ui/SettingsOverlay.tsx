/**
 * SettingsOverlay component - interactive configuration menu
 *
 * Provides a keyboard-navigable interface for viewing and modifying Azedarach configuration.
 * Displays all EDITABLE_SETTINGS with their current values and allows real-time toggling.
 *
 * Navigation:
 * - j/k: Move up/down through settings
 * - Space/Enter: Toggle boolean settings or cycle enum values
 * - e: Open .azedarach.json in external editor for advanced changes
 * - Escape: Close overlay and return to normal mode
 *
 * Architecture:
 * - Uses appConfigAtom to display current config values
 * - Uses settingsStateAtom to track overlay state (open/closed, focus position)
 * - Calls SettingsService.toggleCurrent() to modify values
 * - Automatically reloads config after changes via AppConfig.reload()
 *
 * @see EDITABLE_SETTINGS for the list of configurable options
 * @see SettingsService for the backend logic
 */
import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { EDITABLE_SETTINGS } from "../services/SettingsService.js"
import { appConfigAtom, settingsStateAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

/**
 * Format a boolean for display
 */
const formatBool = (value: boolean): string => {
	return value ? "yes" : "no"
}

/**
 * Format a setting value for display based on value type
 */
const formatSettingValue = (value: boolean | string): string => {
	if (typeof value === "boolean") return formatBool(value)
	if (typeof value === "string") return value
	return String(value)
}

/**
 * SettingsOverlay component implementation
 *
 * Renders the settings overlay when settingsState.isOpen is true.
 * Only displays when both the overlay stack contains "settings" and the settings service is in open state.
 */
export const SettingsOverlay = () => {
	const config = useAtomValue(
		appConfigAtom,
		Result.getOrElse(() => null),
	)
	const settingsStateResult = useAtomValue(settingsStateAtom)

	if (!config) return null
	if (!Result.isSuccess(settingsStateResult)) return null

	const settingsState = settingsStateResult.value
	if (!settingsState.isOpen) return null

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
				minWidth={70}
				flexDirection="column"
			>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"  SETTINGS"}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text> </text>

				{EDITABLE_SETTINGS.map((setting, index) => {
					const value = setting.getValue(config)
					const isFocused = index === settingsState.focusIndex
					const fg = isFocused ? theme.mauve : theme.subtext0
					const prefix = isFocused ? "▶ " : "  "

					return (
						<box key={setting.key} flexDirection="row">
							<text fg={fg} attributes={isFocused ? ATTR_BOLD : 0}>
								{prefix}
							</text>
							<text fg={fg} attributes={isFocused ? ATTR_BOLD : 0}>
								{setting.label}
							</text>
							<text fg={theme.overlay0}>{".".repeat(Math.max(1, 35 - setting.label.length))}</text>
							<text fg={theme.green}>{formatSettingValue(value)}</text>
						</box>
					)
				})}

				<text> </text>

				<text fg={theme.subtext0}>
					{"j/k: move • Enter/Space: toggle • e: edit in editor • Esc: close"}
				</text>
			</box>
		</box>
	)
}
