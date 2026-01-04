/**
 * Keyboard Handling Atoms
 *
 * Main entry point for keyboard input handling.
 */

import { Effect } from "effect"
import { KeyboardService } from "../../services/KeyboardService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Keyboard Handling Atom
// ============================================================================

/**
 * Handle keyboard input via KeyboardService
 *
 * This is the main entry point for keyboard handling. It delegates to
 * KeyboardService which has all keybindings defined as data.
 *
 * Usage: const [, handleKey] = useAtom(handleKeyAtom, { mode: "promise" })
 *        handleKey(event.name)
 */
export const handleKeyAtom = appRuntime.fn((key: string) =>
	Effect.gen(function* () {
		const keyboard = yield* KeyboardService
		yield* keyboard.handleKey(key)
	}).pipe(Effect.catchAll(Effect.logError)),
)
