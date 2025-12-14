/**
 * useKeyboardBridge - Bridges DOM keydown events to KeyboardService
 *
 * This thin hook connects OpenTUI's keyboard input to the Effect-based
 * KeyboardService. It forwards key events to KeyboardService.handleKey
 * which looks up the appropriate action in the data-driven keybindings.
 */

import { useAtom } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useCallback } from "react"
import { handleKeyAtom } from "../atoms/keyboardAtoms"

/**
 * Bridge hook that forwards keyboard events to KeyboardService
 *
 * Usage:
 * ```tsx
 * const App = () => {
 *   useKeyboardBridge()
 *   return <Board />
 * }
 * ```
 */
export const useKeyboardBridge = () => {
	const [, handleKey] = useAtom(handleKeyAtom, { mode: "promise" })

	const onKeyPress = useCallback(
		(event: { name: string; sequence?: string; ctrl?: boolean; meta?: boolean }) => {
			// Normalize key name for Effect KeyboardService
			// OpenTUI provides lowercase names, but we use descriptive names like "Escape"
			const keyName = normalizeKeyName(event.name, event)

			// Fire and forget - KeyboardService handles the action
			handleKey(keyName).catch((error) => {
				// Log errors but don't crash the UI
				console.error(`[KeyboardBridge] Error handling key "${keyName}":`, error)
			})
		},
		[handleKey],
	)

	useKeyboard(onKeyPress)
}

/**
 * Normalize OpenTUI key names to KeyboardService conventions
 *
 * OpenTUI provides lowercase key names like "escape", "return", "space"
 * KeyboardService uses capitalized names like "Escape", "Enter", "Space"
 */
const normalizeKeyName = (
	name: string,
	event: { sequence?: string; ctrl?: boolean; meta?: boolean },
): string => {
	// Handle Ctrl combinations
	if (event.ctrl) {
		return `C-${name}`
	}

	// Map common key names
	switch (name) {
		case "escape":
			return "Escape"
		case "return":
			return "Enter"
		case "space":
			return "Space"
		case "up":
			return "Up"
		case "down":
			return "Down"
		case "left":
			return "Left"
		case "right":
			return "Right"
		case "backspace":
			return "Backspace"
		case "tab":
			return "Tab"
		default:
			// For regular characters, use the sequence if available
			// This handles special characters like "/" and ":" correctly
			return event.sequence && event.sequence.length === 1 ? event.sequence : name
	}
}
