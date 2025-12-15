/**
 * useNavigation - Hook for cursor navigation on the Kanban board
 *
 * Wraps NavigationService atoms for convenient React usage.
 *
 * The cursor state is ID-based (focusedTaskId). Position (columnIndex, taskIndex)
 * is derived locally from the focusedTaskId + tasksByColumn, ensuring it always
 * matches the rendered view.
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useMemo } from "react"
import { focusedTaskIdAtom, initializeNavigationAtom, jumpToAtom, navigateAtom } from "../atoms"
import type { TaskWithSession } from "../types"

/**
 * Find task position by ID in the filtered/sorted task columns
 */
function findTaskPosition(
	taskId: string | null,
	tasksByColumn: TaskWithSession[][],
): { columnIndex: number; taskIndex: number } | undefined {
	if (!taskId) return undefined

	for (let colIdx = 0; colIdx < tasksByColumn.length; colIdx++) {
		const column = tasksByColumn[colIdx]!
		const taskIdx = column.findIndex((t) => t.id === taskId)
		if (taskIdx >= 0) {
			return { columnIndex: colIdx, taskIndex: taskIdx }
		}
	}
	return undefined
}

/**
 * Hook for managing cursor navigation
 *
 * @param tasksByColumn - Tasks grouped by column (already filtered/sorted)
 *
 * @example
 * ```tsx
 * const { selectedTask, moveUp, moveDown, jumpTo } = useNavigation(tasksByColumn)
 *
 * // Move cursor - NavigationService handles all logic
 * moveDown()
 *
 * // Jump to specific position
 * jumpTo(2, 5) // column 2, task 5
 * ```
 */
export function useNavigation(tasksByColumn: TaskWithSession[][]) {
	// Read the focused task ID from NavigationService
	const focusedTaskIdResult = useAtomValue(focusedTaskIdAtom)
	const focusedTaskId = Result.isSuccess(focusedTaskIdResult) ? focusedTaskIdResult.value : null

	// Navigation actions from NavigationService
	const navigate = useAtomSet(navigateAtom, { mode: "promise" })
	const jump = useAtomSet(jumpToAtom, { mode: "promise" })
	const initializeNavigation = useAtomSet(initializeNavigationAtom, { mode: "promise" })

	// Derive position from focusedTaskId + local tasksByColumn
	// This ensures position always matches the rendered view
	const position = useMemo(
		() => findTaskPosition(focusedTaskId, tasksByColumn),
		[focusedTaskId, tasksByColumn],
	)

	const columnIndex = position?.columnIndex ?? 0
	const taskIndex = position?.taskIndex ?? 0

	// Get the selected task from our local data (matches what's rendered)
	const selectedTask = tasksByColumn[columnIndex]?.[taskIndex]

	// Navigation actions (memoized) - NavigationService handles all logic
	const actions = useMemo(
		() => ({
			moveUp: () => navigate("up"),
			moveDown: () => navigate("down"),
			moveLeft: () => navigate("left"),
			moveRight: () => navigate("right"),
			jumpTo: (column: number, task: number) => jump({ column, task }),
		}),
		[navigate, jump],
	)

	// Initialize navigation if no task is focused
	// This is called once when the hook mounts and there's no selection
	useMemo(() => {
		if (!focusedTaskId && tasksByColumn.some((col) => col.length > 0)) {
			initializeNavigation()
		}
	}, [focusedTaskId, tasksByColumn, initializeNavigation])

	return {
		// ID-based cursor (source of truth)
		focusedTaskId,

		// Position derived from focusedTaskId (for rendering)
		columnIndex,
		taskIndex,
		selectedTask,

		// Legacy cursor object for backward compatibility
		cursor: { columnIndex, taskIndex },

		// Basic movement (handled by NavigationService)
		...actions,

		// Follow a task after it moves (e.g., after status change)
		// This is now handled by NavigationService.setFollow
		followTask: (_taskId: string) => {
			// NavigationService handles following via setFollow
			// The task ID approach means the cursor automatically follows
		},
	}
}
