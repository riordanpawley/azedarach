/**
 * Board component - main Kanban board layout
 */
import { useMemo } from "react"
import { Column } from "./Column"
import type { ColumnStatus, JumpTarget, TaskWithSession } from "./types"
import { COLUMNS } from "./types"

export interface BoardProps {
	tasks: TaskWithSession[]
	selectedTaskId?: string
	activeColumnIndex?: number
	activeTaskIndex?: number
	selectedIds?: Set<string>
	jumpLabels?: Map<string, JumpTarget> | null
	pendingJumpKey?: string | null
	terminalHeight?: number
}

/**
 * Board component
 *
 * Displays a horizontal flexbox layout of columns, one per status.
 * Tasks are grouped by status and displayed in their respective columns.
 */
export const Board = (props: BoardProps) => {
	// Group tasks by status for efficient rendering
	const tasksByStatus = useMemo(() => {
		const grouped = new Map<string, TaskWithSession[]>()

		// Initialize all columns with empty arrays
		COLUMNS.forEach((col) => {
			grouped.set(col.status, [])
		})

		// Group tasks by status
		props.tasks.forEach((task) => {
			const tasks = grouped.get(task.status) || []
			tasks.push(task)
			grouped.set(task.status, tasks)
		})

		return grouped
	}, [props.tasks])

	// Create a map from taskId to jump label for easy lookup
	const taskJumpLabels = useMemo(() => {
		const labels = props.jumpLabels
		if (!labels) return null

		const taskToLabel = new Map<string, string>()
		labels.forEach((target, label) => {
			taskToLabel.set(target.taskId, label)
		})
		return taskToLabel
	}, [props.jumpLabels])

	return (
		<box flexDirection="row" width="100%" height="100%" padding={1}>
			{COLUMNS.map((column, index) => (
				<Column
					key={column.status}
					title={column.title}
					status={column.status as ColumnStatus}
					tasks={tasksByStatus.get(column.status) || []}
					selectedTaskId={props.selectedTaskId}
					selectedTaskIndex={props.activeColumnIndex === index ? props.activeTaskIndex : undefined}
					isActiveColumn={props.activeColumnIndex === index}
					selectedIds={props.selectedIds}
					taskJumpLabels={taskJumpLabels}
					pendingJumpKey={props.pendingJumpKey}
					maxVisible={props.terminalHeight}
				/>
			))}
		</box>
	)
}
