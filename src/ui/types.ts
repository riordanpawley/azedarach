/**
 * Shared types for UI components
 */
import type { Issue } from "../core/BeadsClient"

/**
 * Session state for a task
 */
export type SessionState =
  | "idle"        // No session running
  | "busy"        // Claude is working
  | "waiting"     // Claude is waiting for input
  | "done"        // Task completed successfully
  | "error"       // Task failed
  | "paused"      // Session paused

/**
 * Task with session state
 */
export interface TaskWithSession extends Issue {
  sessionState: SessionState
}

/**
 * Kanban columns by status
 */
export const COLUMNS = [
  { id: "open", title: "Open", status: "open" },
  { id: "in_progress", title: "In Progress", status: "in_progress" },
  { id: "blocked", title: "Blocked", status: "blocked" },
  { id: "closed", title: "Closed", status: "closed" },
] as const

export type ColumnId = typeof COLUMNS[number]["id"]
export type ColumnStatus = typeof COLUMNS[number]["status"]

/**
 * Session state indicators
 */
export const SESSION_INDICATORS: Record<SessionState, string> = {
  idle: "",
  busy: "🔵",
  waiting: "🟡",
  done: "✅",
  error: "❌",
  paused: "⏸️",
}

/**
 * Navigation position in the board
 */
export interface NavigationState {
  columnIndex: number
  taskIndex: number
}

/**
 * Editor modes (Helix-style)
 *
 * - normal: Default navigation mode (hjkl to move)
 * - goto: Jump mode triggered by 'g' - shows 2-char labels for instant jumping
 * - select: Multi-selection mode triggered by 'v'
 * - action: Action menu mode triggered by Space
 */
export type EditorMode = "normal" | "goto" | "select" | "action"

/**
 * Goto mode sub-state
 * When 'g' is pressed, we wait for the next key:
 * - 'w' enters word/item jump mode (shows labels)
 * - 'g' goes to first item
 * - 'e' goes to last item
 * - 'h' goes to first column
 * - 'l' goes to last column
 */
export type GotoSubMode = "pending" | "jump"

/**
 * Application state including modal editing
 */
export interface AppState {
  mode: EditorMode
  gotoSubMode: GotoSubMode | null
  selectedIds: Set<string>
  jumpLabels: Map<string, JumpTarget> | null
  pendingJumpKey: string | null
}

/**
 * Jump target for goto mode
 */
export interface JumpTarget {
  taskId: string
  columnIndex: number
  taskIndex: number
}

/**
 * Generate 2-char jump labels (aa, ab, ac... ba, bb, bc...)
 * Uses home row keys for ergonomics
 */
export function generateJumpLabels(count: number): string[] {
  const chars = "asdfjkl;" // Home row keys
  const labels: string[] = []

  for (let i = 0; i < chars.length && labels.length < count; i++) {
    for (let j = 0; j < chars.length && labels.length < count; j++) {
      labels.push(chars[i] + chars[j])
    }
  }

  return labels
}
