/**
 * useEditorMode - Hook for editor mode management (Helix-style modal editing)
 *
 * Replaces the old editorReducer + useReducer pattern with atomic services.
 * Provides mode state and transition actions.
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import { useCallback, useMemo } from "react"
import {
	clearCommandAtom,
	clearSearchAtom,
	commandInputAtom,
	enterActionAtom,
	enterCommandAtom,
	enterGotoAtom,
	enterJumpAtom,
	enterSearchAtom,
	enterSelectAtom,
	exitSelectAtom,
	exitToNormalAtom,
	modeAtom,
	searchQueryAtom,
	selectedIdsAtom,
	setPendingJumpKeyAtom,
	toggleSelectionAtom,
	updateCommandAtom,
	updateSearchAtom,
} from "../atoms"
import type { JumpTarget } from "../types"

// Default mode when loading
const DEFAULT_MODE = { _tag: "normal" } as const

/**
 * Hook for managing editor mode state
 *
 * @example
 * ```tsx
 * const { mode, isNormal, isAction, enterAction, exitToNormal } = useEditorMode()
 *
 * if (isAction) {
 *   // Handle action mode keys
 * }
 *
 * // Transition modes
 * enterAction()
 * ```
 */
export function useEditorMode() {
	// State (Result types)
	const modeResult = useAtomValue(modeAtom)
	const selectedIdsResult = useAtomValue(selectedIdsAtom)
	const searchQueryResult = useAtomValue(searchQueryAtom)
	const commandInputResult = useAtomValue(commandInputAtom)

	// Unwrap Result types with defaults
	const mode = Result.isSuccess(modeResult) ? modeResult.value : DEFAULT_MODE
	const selectedIds = Result.isSuccess(selectedIdsResult) ? selectedIdsResult.value : []
	const searchQuery = Result.isSuccess(searchQueryResult) ? searchQueryResult.value : ""
	const commandInput = Result.isSuccess(commandInputResult) ? commandInputResult.value : ""

	// Action atoms
	const [, enterSelect] = useAtom(enterSelectAtom, { mode: "promise" })
	const [, exitSelect] = useAtom(exitSelectAtom, { mode: "promise" })
	const [, toggleSelection] = useAtom(toggleSelectionAtom, { mode: "promise" })
	const [, enterGoto] = useAtom(enterGotoAtom, { mode: "promise" })
	const [, enterJump] = useAtom(enterJumpAtom, { mode: "promise" })
	const [, setPendingJumpKey] = useAtom(setPendingJumpKeyAtom, { mode: "promise" })
	const [, enterAction] = useAtom(enterActionAtom, { mode: "promise" })
	const [, enterSearch] = useAtom(enterSearchAtom, { mode: "promise" })
	const [, updateSearch] = useAtom(updateSearchAtom, { mode: "promise" })
	const [, clearSearch] = useAtom(clearSearchAtom, { mode: "promise" })
	const [, enterCommand] = useAtom(enterCommandAtom, { mode: "promise" })
	const [, updateCommand] = useAtom(updateCommandAtom, { mode: "promise" })
	const [, clearCommand] = useAtom(clearCommandAtom, { mode: "promise" })
	const [, exitToNormal] = useAtom(exitToNormalAtom, { mode: "promise" })

	// Mode convenience checks (memoized)
	const modeFlags = useMemo(
		() => ({
			isNormal: mode._tag === "normal",
			isSelect: mode._tag === "select",
			isGoto: mode._tag === "goto",
			isGotoPending: mode._tag === "goto" && mode.gotoSubMode === "pending",
			isJump: mode._tag === "goto" && mode.gotoSubMode === "jump",
			isAction: mode._tag === "action",
			isSearch: mode._tag === "search",
			isCommand: mode._tag === "command",
		}),
		[mode],
	)

	// Get pending jump key and jump labels from mode state
	const pendingJumpKey = mode._tag === "goto" ? mode.pendingJumpKey : undefined
	const jumpLabels = mode._tag === "goto" ? mode.jumpLabels : undefined

	// Wrapped actions with error handling
	const actions = useMemo(
		() => ({
			enterSelect: () => {
				enterSelect().catch(console.error)
			},

			exitSelect: (clearSelections?: boolean) => {
				exitSelect(clearSelections).catch(console.error)
			},

			toggleSelection: (taskId: string) => {
				toggleSelection(taskId).catch(console.error)
			},

			enterGoto: () => {
				enterGoto().catch(console.error)
			},

			enterJump: (labels: Map<string, JumpTarget>) => {
				enterJump(labels).catch(console.error)
			},

			setPendingJumpKey: (key: string) => {
				setPendingJumpKey(key).catch(console.error)
			},

			enterAction: () => {
				enterAction().catch(console.error)
			},

			enterSearch: () => {
				enterSearch().catch(console.error)
			},

			updateSearch: (query: string) => {
				updateSearch(query).catch(console.error)
			},

			clearSearch: () => {
				clearSearch().catch(console.error)
			},

			enterCommand: () => {
				enterCommand().catch(console.error)
			},

			updateCommand: (input: string) => {
				updateCommand(input).catch(console.error)
			},

			clearCommand: () => {
				clearCommand().catch(console.error)
			},

			exitToNormal: () => {
				exitToNormal().catch(console.error)
			},
		}),
		[
			enterSelect,
			exitSelect,
			toggleSelection,
			enterGoto,
			enterJump,
			setPendingJumpKey,
			enterAction,
			enterSearch,
			updateSearch,
			clearSearch,
			enterCommand,
			updateCommand,
			clearCommand,
			exitToNormal,
		],
	)

	return {
		// State
		mode,
		selectedIds,
		searchQuery,
		commandInput,
		pendingJumpKey,
		jumpLabels,

		// Mode checks
		...modeFlags,

		// Actions
		...actions,
	}
}
