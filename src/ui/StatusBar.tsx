/**
 * StatusBar component - bottom status bar with mode indicator and contextual keybinds
 */

import type { VCStatus } from "../core/VCService"
import { theme } from "./theme"
import type { EditorMode } from "../services/EditorService"

export interface StatusBarProps {
	totalTasks: number
	activeSessions: number
	mode: EditorMode["_tag"]
	modeDisplay: string
	selectedCount: number
	connected?: boolean
	vcStatus?: VCStatus
}

/**
 * Keybinding definition with display info
 */
interface KeyBinding {
	key: string
	action: string
}

/**
 * All keybindings per mode, ordered by priority (most important first)
 */
const MODE_KEYBINDINGS: Record<EditorMode["_tag"], KeyBinding[]> = {
	normal: [
		{ key: "Space", action: "Menu" },
		{ key: ",", action: "Sort" },
		{ key: "/", action: "Search" },
		{ key: "v", action: "Select" },
		{ key: "g", action: "Goto" },
		{ key: "hjkl", action: "Nav" },
		{ key: "Enter", action: "Details" },
		{ key: "c", action: "Create" },
		{ key: "a", action: "VC" },
		{ key: ":", action: "Cmd" },
		{ key: "C-d/u", action: "Page" },
		{ key: "q", action: "Quit" },
	],
	action: [
		{ key: "h/l", action: "Move" },
		{ key: "s", action: "Start" },
		{ key: "a", action: "Attach" },
		{ key: "A", action: "Inline" },
		{ key: "p", action: "Pause" },
		{ key: "r", action: "Resume" },
		{ key: "x", action: "Stop" },
		{ key: "e", action: "Edit" },
		{ key: "P", action: "PR" },
		{ key: "d", action: "Delete" },
		{ key: "Esc", action: "Cancel" },
	],
	goto: [
		{ key: "w", action: "Jump" },
		{ key: "g", action: "First" },
		{ key: "e", action: "Last" },
		{ key: "h", action: "Left" },
		{ key: "l", action: "Right" },
		{ key: "Esc", action: "Cancel" },
	],
	select: [
		{ key: "Space", action: "Toggle" },
		{ key: "hjkl", action: "Nav" },
		{ key: "v", action: "Exit" },
		{ key: "Esc", action: "Clear" },
	],
	search: [
		{ key: "Enter", action: "Confirm" },
		{ key: "Esc", action: "Clear" },
	],
	command: [
		{ key: "Enter", action: "Send" },
		{ key: "Esc", action: "Cancel" },
	],
	sort: [
		{ key: "s", action: "Session" },
		{ key: "p", action: "Priority" },
		{ key: "u", action: "Updated" },
		{ key: "Esc", action: "Cancel" },
	],
}

/**
 * Calculate display width for a keybinding hint
 * Format: "key action" with gap=1 between hints
 */
const getHintWidth = (binding: KeyBinding): number => {
	// key + space + action + gap between hints (2 chars for gap)
	return binding.key.length + 1 + binding.action.length + 2
}

/**
 * "? more" indicator width
 */
const MORE_INDICATOR_WIDTH = 7 // "? more" + gap

/**
 * StatusBar component
 *
 * Displays:
 * - Project name and connection status
 * - Current mode indicator (NOR/SEL/ACT/etc) like Helix
 * - Contextual keyboard shortcuts based on current mode (responsive)
 * - Application stats (tasks, active sessions)
 */
export const StatusBar = (props: StatusBarProps) => {
	// Get terminal width, default to 80 if not available
	const terminalWidth = process.stdout.columns || 80

	// Mode colors matching Helix conventions
	const getModeColor = () => {
		switch (props.mode) {
			case "action":
				return theme.green
			case "goto":
				return theme.yellow
			case "normal":
				return theme.blue
			case "search":
				return theme.peach
			case "select":
				return theme.mauve
			case "sort":
				return theme.teal
			case "command":
				return theme.pink
			default:
				return theme.text
		}
	}

	// Short mode label like Helix
	const getModeLabel = () => {
		switch (props.mode) {
			case "action":
				return "ACT"
			case "goto":
				return "GTO"
			case "normal":
				return "NOR"
			case "search":
				return "SRC"
			case "select":
				return "SEL"
			case "sort":
				return "SRT"
			case "command":
				return "CMD"
			default:
				return "???"
		}
	}

	// Connection status indicator
	const getConnectionIndicator = () => {
		const connected = props.connected ?? true
		const icon = connected ? "●" : "○"
		const color = connected ? theme.green : theme.overlay0
		return { icon, color }
	}

	// VC status indicator
	const getVCStatusColor = () => {
		switch (props.vcStatus) {
			case "running":
				return theme.green
			case "starting":
				return theme.yellow
			case "stopped":
				return theme.yellow
			case "not_installed":
				return theme.red
			case "error":
				return theme.red
			default:
				return theme.overlay0
		}
	}

	// Determine what to show based on terminal width
	const shouldShowModeDisplay = terminalWidth >= 80
	const shouldShowSelectedCount = terminalWidth >= 60

	const connIndicator = getConnectionIndicator()
	const modeColor = getModeColor()
	const modeLabel = getModeLabel()

	// Calculate available width for keybindings
	// Fixed elements: border(2) + padding(2) + "azedarach"(9) + gap(2) + conn(1) + gap(2) + mode(5) + gap(2)
	// Right side: gap(2) + "Tasks: X"(~10) + gap(2) + "Active: X"(~10) + VC status(~12)
	const fixedLeftWidth = 25
	const fixedRightWidth = 40 // Approximate, includes stats and potential VC status
	const modeDisplayWidth = shouldShowModeDisplay && props.modeDisplay ? props.modeDisplay.length + 2 : 0
	const availableWidth = terminalWidth - fixedLeftWidth - fixedRightWidth - modeDisplayWidth

	// Get keybindings for current mode and calculate how many fit
	const allBindings = MODE_KEYBINDINGS[props.mode] || []
	const visibleBindings: KeyBinding[] = []
	let usedWidth = MORE_INDICATOR_WIDTH // Always reserve space for "? more"

	for (const binding of allBindings) {
		const hintWidth = getHintWidth(binding)
		if (usedWidth + hintWidth <= availableWidth) {
			visibleBindings.push(binding)
			usedWidth += hintWidth
		}
	}

	// Check if we're showing all bindings (no need for "more" indicator)
	const showingAll = visibleBindings.length === allBindings.length

	return (
		<box
			borderStyle="single"
			border={true}
			borderColor={theme.surface1}
			backgroundColor={theme.mantle}
			paddingLeft={1}
			paddingRight={1}
		>
			<box flexDirection="row" gap={2} width="100%">
				{/* Project name and connection status - left side */}
				<text fg={theme.text} attributes={ATTR_BOLD}>
					azedarach
				</text>
				<text fg={connIndicator.color}>{connIndicator.icon}</text>

				{/* Mode indicator */}
				<box backgroundColor={modeColor} paddingLeft={1} paddingRight={1}>
					<text fg={theme.base} attributes={ATTR_BOLD}>
						{modeLabel}
					</text>
				</box>

				{/* Mode detail (shows pending keys, selection count, etc) */}
				{shouldShowModeDisplay && props.modeDisplay && (
					<text fg={theme.subtext0}>{props.modeDisplay}</text>
				)}

				{/* Contextual keyboard shortcuts - dynamically fit as many as possible */}
				<box flexDirection="row" gap={2}>
					{visibleBindings.map((binding) => (
						<KeyHint key={binding.key} keyName={binding.key} action={binding.action} />
					))}
					{/* Always show "? more" indicator unless all bindings are visible */}
					{!showingAll && <KeyHint keyName="?" action="more" />}
				</box>

				{/* Stats - right aligned */}
				<box flexGrow={1} />
				<box flexDirection="row" gap={2}>
					{shouldShowSelectedCount && props.selectedCount > 0 && (
						<text fg={theme.mauve}>Selected: {props.selectedCount}</text>
					)}
					{props.vcStatus && <text fg={getVCStatusColor()}>VC: {props.vcStatus}</text>}
					<text fg={theme.green}>Tasks: {props.totalTasks}</text>
					<text fg={theme.blue}>Active: {props.activeSessions}</text>
				</box>
			</box>
		</box>
	)
}

/**
 * KeyHint - displays a keyboard shortcut hint
 */
interface KeyHintProps {
	keyName: string
	action: string
}

const KeyHint = (props: KeyHintProps) => (
	<box flexDirection="row" gap={1}>
		<text fg={theme.mauve}>{props.keyName}</text>
		<text fg={theme.subtext0}>{props.action}</text>
	</box>
)

/**
 * Text attribute for bold
 */
const ATTR_BOLD = 1
