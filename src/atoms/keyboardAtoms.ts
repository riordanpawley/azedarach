/**
 * Keyboard action atoms for KeyboardService integration
 *
 * These atoms provide React integration with the Effect-based KeyboardService.
 * The KeyboardService handles the actual keybinding logic; these atoms just
 * provide the bridge for React components to call it.
 */

import { Effect } from "effect"
import { KeyboardService } from "../services/KeyboardService"
import { appRuntime } from "../ui/atoms"

/**
 * Action atom to handle a key press
 *
 * Forwards the key to KeyboardService.handleKey which looks up the
 * appropriate action in the data-driven keybindings and executes it.
 *
 * Usage:
 * ```tsx
 * const [, handleKey] = useAtom(handleKeyAtom, { mode: "promise" })
 * handleKey("Escape")
 * ```
 */
export const handleKeyAtom = appRuntime.fn((key: string) =>
	Effect.gen(function* () {
		const keyboard = yield* KeyboardService
		yield* keyboard.handleKey(key)
	}),
)

/**
 * Action atom to register a new keybinding
 *
 * Usage:
 * ```tsx
 * const [, registerKey] = useAtom(registerKeyAtom, { mode: "promise" })
 * registerKey({
 *   key: "x",
 *   mode: "normal",
 *   description: "Custom action",
 *   action: Effect.void
 * })
 * ```
 */
export const registerKeyAtom = appRuntime.fn(
	(binding: {
		key: string
		mode: "normal" | "select" | "command" | "search" | "overlay" | "*"
		description: string
		action: Effect.Effect<void>
	}) =>
		Effect.gen(function* () {
			const keyboard = yield* KeyboardService
			yield* keyboard.register(binding)
		}),
)

/**
 * Action atom to unregister a keybinding
 *
 * Usage:
 * ```tsx
 * const [, unregisterKey] = useAtom(unregisterKeyAtom, { mode: "promise" })
 * unregisterKey({ key: "x", mode: "normal" })
 * ```
 */
export const unregisterKeyAtom = appRuntime.fn(
	(params: { key: string; mode: "normal" | "select" | "command" | "search" | "overlay" | "*" }) =>
		Effect.gen(function* () {
			const keyboard = yield* KeyboardService
			yield* keyboard.unregister(params.key, params.mode)
		}),
)
