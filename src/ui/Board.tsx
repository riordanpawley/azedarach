/**
 * Board component - main Kanban board layout
 *
 * Supports two view modes:
 * - kanban: Traditional column-based view with task cards
 * - compact: Linear list view with minimal row height
 */
import { useMemo } from "react"
import { Column } from "./Column"
import { CompactView } from "./CompactView"
import type { ColumnStatus, JumpTarget, TaskWithSession, ViewMode } from "./types"
import { COLUMNS } from "./types"

export interface BoardProps {
	tasks: TaskWithSession[]
	selectedTaskId?: string
	activeColumnIndex?: number
	activeTaskIndex?: number
	selectedIds?: Set<string>
	jumpLabels?: ReadonlyMap<string, JumpTarget> | null
	pendingJumpKey?: string | null
	terminalHeight?: number
	viewMode?: ViewMode
}

/**
 * Board component
 *
 * Displays tasks in either Kanban (columns) or Compact (linear list) view.
 * The view mode is controlled by the viewMode prop.
 */
export const Board = (props: BoardProps) => {
	const viewMode = props.viewMode ?? "kanban"

	// Group tasks by status for Kanban view
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

	// Render compact view
	if (viewMode === "compact") {
		return (
			<CompactView
				tasks={props.tasks}
				selectedTaskId={props.selectedTaskId}
				activeColumnIndex={props.activeColumnIndex}
				activeTaskIndex={props.activeTaskIndex}
				selectedIds={props.selectedIds}
				jumpLabels={props.jumpLabels}
				pendingJumpKey={props.pendingJumpKey}
				terminalHeight={props.terminalHeight}
			/>
		)
	}

	// Render Kanban view (default)
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
