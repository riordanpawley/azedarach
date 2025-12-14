/**
 * useNavigation - Hook for cursor navigation on the Kanban board
 *
 * Wraps NavigationService atoms for convenient React usage.
 * Provides cursor position and movement functions.
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import { useCallback, useEffect, useMemo, useState } from "react"
import { cursorAtom, jumpToAtom, navigateAtom } from "../atoms"
import type { TaskWithSession } from "../types"

// Default cursor when loading
const DEFAULT_CURSOR = { columnIndex: 0, taskIndex: 0 }

/**
 * Hook for managing cursor navigation
 *
 * @param tasksByColumn - Tasks grouped by column for bounds checking
 *
 * @example
 * ```tsx
 * const { cursor, selectedTask, moveUp, moveDown, jumpTo } = useNavigation(tasksByColumn)
 *
 * // Move cursor
 * moveDown()
 *
 * // Jump to specific position
 * jumpTo(2, 5) // column 2, task 5
 * ```
 */
export function useNavigation(tasksByColumn: TaskWithSession[][]) {
	const cursorResult = useAtomValue(cursorAtom)
	const [, navigate] = useAtom(navigateAtom, { mode: "promise" })
	const [, jump] = useAtom(jumpToAtom, { mode: "promise" })

	// Unwrap Result with default
	const cursor = Result.isSuccess(cursorResult) ? cursorResult.value : DEFAULT_CURSOR

	// Track task ID to follow after move operations
	const [followTaskId, setFollowTaskId] = useState<string | null>(null)

	// Get currently selected task
	const selectedTask = tasksByColumn[cursor.columnIndex]?.[cursor.taskIndex]

	// Effect to follow a task after move operations
	useEffect(() => {
		if (!followTaskId) return

		// Search all columns for the task
		for (let colIdx = 0; colIdx < tasksByColumn.length; colIdx++) {
			const taskIdx = tasksByColumn[colIdx].findIndex((t) => t.id === followTaskId)
			if (taskIdx >= 0) {
				jump({ column: colIdx, task: taskIdx }).catch(console.error)
				setFollowTaskId(null)
				return
			}
		}
		// Task not found (maybe deleted), clear the follow state
		setFollowTaskId(null)
	}, [followTaskId, tasksByColumn, jump])

	// Navigation actions (memoized)
	const actions = useMemo(
		() => ({
			moveUp: () => {
				navigate("up").catch(console.error)
			},

			moveDown: () => {
				navigate("down").catch(console.error)
			},

			moveLeft: () => {
				navigate("left").catch(console.error)
			},

			moveRight: () => {
				navigate("right").catch(console.error)
			},

			jumpTo: (column: number, task: number) => {
				jump({ column, task }).catch(console.error)
			},
		}),
		[navigate, jump],
	)

	// Follow a task after it moves (e.g., after status change)
	const followTask = useCallback((taskId: string) => {
		setFollowTaskId(taskId)
	}, [])

	// Half-page navigation
	const halfPageDown = useCallback(() => {
		const column = tasksByColumn[cursor.columnIndex]
		if (column) {
			const halfPage = Math.floor(column.length / 2)
			const newIndex = Math.min(cursor.taskIndex + halfPage, column.length - 1)
			actions.jumpTo(cursor.columnIndex, newIndex)
		}
	}, [tasksByColumn, cursor, actions])

	const halfPageUp = useCallback(() => {
		const column = tasksByColumn[cursor.columnIndex]
		if (column) {
			const halfPage = Math.floor(column.length / 2)
			const newIndex = Math.max(cursor.taskIndex - halfPage, 0)
			actions.jumpTo(cursor.columnIndex, newIndex)
		}
	}, [tasksByColumn, cursor, actions])

	// Go to extremes
	const goToFirst = useCallback(() => {
		actions.jumpTo(0, 0)
	}, [actions])

	const goToLast = useCallback(() => {
		const lastColIdx = tasksByColumn.length - 1
		const lastCol = tasksByColumn[lastColIdx]
		actions.jumpTo(lastColIdx, lastCol ? lastCol.length - 1 : 0)
	}, [tasksByColumn, actions])

	const goToFirstColumn = useCallback(() => {
		actions.jumpTo(0, cursor.taskIndex)
	}, [cursor.taskIndex, actions])

	const goToLastColumn = useCallback(() => {
		actions.jumpTo(tasksByColumn.length - 1, cursor.taskIndex)
	}, [tasksByColumn.length, cursor.taskIndex, actions])

	return {
		cursor,
		columnIndex: cursor.columnIndex,
		taskIndex: cursor.taskIndex,
		selectedTask,

		// Basic movement
		...actions,

		// Task following
		followTask,

		// Extended navigation
		halfPageDown,
		halfPageUp,
		goToFirst,
		goToLast,
		goToFirstColumn,
		goToLastColumn,
	}
}
