/**
 * Keyboard Service Types
 *
 * Shared type definitions for keyboard handling, including:
 * - Mode types for keybinding matching
 * - Keybinding interface
 */

import type { CommandExecutor } from "@effect/platform"
import type { Effect } from "effect"
import type { BeadsError } from "../../core/BeadsClient.js"

// ============================================================================
// Mode Types
// ============================================================================

/**
 * Keyboard mode for keybinding matching
 *
 * Modes map to EditorService modes:
 * - normal: Default navigation
 * - select: Multi-selection
 * - action: Action palette (Space menu)
 * - goto-pending: Waiting for second key after 'g'
 * - goto-jump: Jump label mode (2-char input)
 * - search: Search/filter with text input
 * - command: VC command with text input
 * - overlay: Any overlay is open
 * - sort: Sort menu
 * - orchestrate: Epic orchestration mode for managing child tasks
 * - *: Universal (matches any mode)
 */
export type KeyMode =
	| "normal"
	| "select"
	| "action"
	| "goto-pending"
	| "goto-jump"
	| "search"
	| "command"
	| "overlay"
	| "sort"
	| "orchestrate"
	| "*"

// ============================================================================
// Keybinding Types
// ============================================================================

/**
 * Platform dependencies that keybinding actions may require.
 * CommandExecutor is the one platform dependency allowed to leak through method return types.
 */
export type KeybindingDeps = CommandExecutor.CommandExecutor

/**
 * Keybinding definition with mode-specific action
 *
 * Actions may have platform requirements (CommandExecutor, FileSystem, BeadsClient)
 * which are satisfied by the runtime layer when KeyboardService is used.
 */
export interface Keybinding {
	readonly key: string
	readonly mode: KeyMode
	readonly description: string
	readonly action: Effect.Effect<void, BeadsError, KeybindingDeps>
}
