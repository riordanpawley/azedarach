/**
 * Board component - main Kanban board layout
 *
 * Supports two view modes:
 * - kanban: Traditional column-based view with task cards
 * - compact: Linear list view with minimal row height
 */
import type { Record as R } from "effect"
import { useMemo } from "react"
import { Column } from "./Column.js"
import { CompactView } from "./CompactView.js"
import type { JumpTarget, TaskWithSession, ViewMode } from "./types.js"
import { COLUMNS } from "./types.js"

export interface BoardProps {
	tasks: readonly TaskWithSession[]
	selectedTaskId?: string
	activeColumnIndex?: number
	activeTaskIndex?: number
	selectedIds?: Set<string>
	jumpLabels?: R.ReadonlyRecord<string, JumpTarget> | null
	pendingJumpKey?: string | null
	terminalHeight?: number
	viewMode?: ViewMode
	/** Whether action mode is active (selected card gets prominent border) */
	isActionMode?: boolean
	/** Source bead ID when in merge select mode (highlighted differently) */
	mergeSelectSourceId?: string
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
		for (const [label, target] of Object.entries(labels)) {
			taskToLabel.set(target.taskId, label)
		}
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
				isActionMode={props.isActionMode}
				mergeSelectSourceId={props.mergeSelectSourceId}
			/>
		)
	}

	// Render kanban view
	return (
		<box flexDirection="row" width="100%" height="100%">
			{COLUMNS.map((col, colIndex) => {
				const columnTasks = tasksByStatus.get(col.status) || []
				const isActiveColumn = colIndex === props.activeColumnIndex

				return (
					<Column
						key={col.id}
						title={col.title}
						status={col.status}
						tasks={columnTasks}
						selectedTaskId={props.selectedTaskId}
						isActiveColumn={isActiveColumn}
						selectedTaskIndex={isActiveColumn ? (props.activeTaskIndex ?? 0) : undefined}
						selectedIds={props.selectedIds}
						taskJumpLabels={taskJumpLabels}
						pendingJumpKey={props.pendingJumpKey}
						maxVisible={props.terminalHeight}
						isActionMode={props.isActionMode}
						mergeSelectSourceId={props.mergeSelectSourceId}
					/>
				)
			})}
		</box>
	)
}
