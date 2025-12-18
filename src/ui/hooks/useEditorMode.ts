/**
 * useEditorMode - Hook for editor mode management (Helix-style modal editing)
 *
 * Replaces the old editorReducer + useReducer pattern with atomic services.
 * Provides mode state and transition actions.
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import type { Record as R } from "effect"
import { useMemo } from "react"
import type { SortConfig, SortField } from "../../services/EditorService.js"
import {
	clearCommandAtom,
	clearSearchAtom,
	commandInputAtom,
	cycleSortAtom,
	enterActionAtom,
	enterCommandAtom,
	enterGotoAtom,
	enterJumpAtom,
	enterSearchAtom,
	enterSelectAtom,
	enterSortAtom,
	exitSelectAtom,
	exitToNormalAtom,
	modeAtom,
	searchQueryAtom,
	selectedIdsAtom,
	setPendingJumpKeyAtom,
	sortConfigAtom,
	toggleSelectionAtom,
	updateCommandAtom,
	updateSearchAtom,
} from "../atoms.js"
import type { JumpTarget } from "../types.js"

// Default mode when loading
const DEFAULT_MODE = { _tag: "normal" } as const

// Default sort config when loading
const DEFAULT_SORT_CONFIG: SortConfig = { field: "session", direction: "desc" }

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
	// State - modeResult is Result-wrapped, derived atoms are plain values
	const modeResult = useAtomValue(modeAtom)
	const sortConfigResult = useAtomValue(sortConfigAtom)

	// Derived atoms (selectedIdsAtom, etc.) now return plain values, not Result
	const selectedIds = useAtomValue(selectedIdsAtom)
	const searchQuery = useAtomValue(searchQueryAtom)
	const commandInput = useAtomValue(commandInputAtom)

	// Unwrap mode Result with default
	const mode = Result.isSuccess(modeResult) ? modeResult.value : DEFAULT_MODE

	// Unwrap sortConfig Result with default
	const sortConfig = Result.isSuccess(sortConfigResult)
		? sortConfigResult.value
		: DEFAULT_SORT_CONFIG

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
	const [, enterSort] = useAtom(enterSortAtom, { mode: "promise" })
	const [, cycleSort] = useAtom(cycleSortAtom, { mode: "promise" })

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
			isSort: mode._tag === "sort",
		}),
		[mode],
	)

	// Get pending jump key and jump labels from mode state
	const pendingJumpKey = mode._tag === "goto" ? mode.pendingJumpKey : undefined
	const jumpLabels = mode._tag === "goto" ? mode.jumpLabels : undefined

	// Wrapped actions - errors are logged in Effect layer
	const actions = useMemo(
		() => ({
			enterSelect: () => {
				enterSelect()
			},

			exitSelect: (clearSelections?: boolean) => {
				exitSelect(clearSelections)
			},

			toggleSelection: (taskId: string) => {
				toggleSelection(taskId)
			},

			enterGoto: () => {
				enterGoto()
			},

			enterJump: (labels: R.ReadonlyRecord<string, JumpTarget>) => {
				enterJump(labels)
			},

			setPendingJumpKey: (key: string) => {
				setPendingJumpKey(key)
			},

			enterAction: () => {
				enterAction()
			},

			enterSearch: () => {
				enterSearch()
			},

			updateSearch: (query: string) => {
				updateSearch(query)
			},

			clearSearch: () => {
				clearSearch()
			},

			enterCommand: () => {
				enterCommand()
			},

			updateCommand: (input: string) => {
				updateCommand(input)
			},

			clearCommand: () => {
				clearCommand()
			},

			exitToNormal: () => {
				exitToNormal()
			},

			enterSort: () => {
				enterSort().catch(console.error)
			},

			cycleSort: (field: SortField) => {
				cycleSort(field).catch(console.error)
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
			enterSort,
			cycleSort,
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
		sortConfig,

		// Mode checks
		...modeFlags,

		// Actions
		...actions,
	}
}
