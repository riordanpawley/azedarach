/**
 * Editor Mode Finite State Machine
 *
 * Clean state machine for Helix-style modal editing.
 * States: normal → select/goto/action → normal
 */

import type { EditorMode, GotoSubMode, JumpTarget } from "./types"

// FSM State
export interface EditorState {
  mode: EditorMode
  gotoSubMode: GotoSubMode | null
  jumpLabels: Map<string, JumpTarget> | null
  pendingJumpKey: string | null
  selectedIds: Set<string>
  searchQuery: string
}

// FSM Actions
export type EditorAction =
  | { type: "ENTER_SELECT" }
  | { type: "EXIT_SELECT"; clearSelections?: boolean }
  | { type: "TOGGLE_SELECTION"; taskId: string }
  | { type: "ENTER_GOTO" }
  | { type: "ENTER_JUMP"; labels: Map<string, JumpTarget> }
  | { type: "SET_PENDING_JUMP_KEY"; key: string }
  | { type: "ENTER_ACTION" }
  | { type: "ENTER_SEARCH" }
  | { type: "UPDATE_SEARCH_QUERY"; query: string }
  | { type: "CLEAR_SEARCH" }
  | { type: "EXIT_TO_NORMAL" }
  | { type: "RESET" }

// Initial state
export const initialEditorState: EditorState = {
  mode: "normal",
  gotoSubMode: null,
  jumpLabels: null,
  pendingJumpKey: null,
  selectedIds: new Set(),
  searchQuery: "",
}

// FSM Reducer
export function editorReducer(state: EditorState, action: EditorAction): EditorState {
  switch (action.type) {
    case "ENTER_SELECT":
      return { ...state, mode: "select" }

    case "EXIT_SELECT":
      return {
        ...state,
        mode: "normal",
        selectedIds: action.clearSelections ? new Set() : state.selectedIds,
      }

    case "TOGGLE_SELECTION": {
      const newSelected = new Set(state.selectedIds)
      if (newSelected.has(action.taskId)) {
        newSelected.delete(action.taskId)
      } else {
        newSelected.add(action.taskId)
      }
      return { ...state, selectedIds: newSelected }
    }

    case "ENTER_GOTO":
      return { ...state, mode: "goto", gotoSubMode: "pending" }

    case "ENTER_JUMP":
      return { ...state, gotoSubMode: "jump", jumpLabels: action.labels }

    case "SET_PENDING_JUMP_KEY":
      return { ...state, pendingJumpKey: action.key }

    case "ENTER_ACTION":
      return { ...state, mode: "action" }

    case "ENTER_SEARCH":
      return { ...state, mode: "search" }

    case "UPDATE_SEARCH_QUERY":
      return { ...state, searchQuery: action.query }

    case "CLEAR_SEARCH":
      return { ...state, mode: "normal", searchQuery: "" }

    case "EXIT_TO_NORMAL":
      return {
        ...state,
        mode: "normal",
        gotoSubMode: null,
        jumpLabels: null,
        pendingJumpKey: null,
      }

    case "RESET":
      return initialEditorState

    default:
      return state
  }
}

// Helper to check if in a specific mode
export function isMode(state: EditorState, mode: EditorMode): boolean {
  return state.mode === mode
}

// Helper to check goto sub-mode
export function isGotoSubMode(state: EditorState, subMode: GotoSubMode): boolean {
  return state.mode === "goto" && state.gotoSubMode === subMode
}
