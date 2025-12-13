/**
 * Column component - displays a vertical list of tasks for a single status
 */
import { useMemo } from "react"
import { TaskCard } from "./TaskCard"
import { columnColors, theme } from "./theme"
import type { ColumnStatus, TaskWithSession } from "./types"

export interface ColumnProps {
	title: string
	status: ColumnStatus
	tasks: TaskWithSession[]
	selectedTaskId?: string
	selectedTaskIndex?: number
	isActiveColumn?: boolean
	selectedIds?: Set<string>
	taskJumpLabels?: Map<string, string> | null
	pendingJumpKey?: string | null
	maxVisible?: number
}

/**
 * Column component
 *
 * Displays a column header and a windowed list of tasks.
 * Only shows maxVisible tasks at a time, scrolling to keep selection visible.
 */
export const Column = (props: ColumnProps) => {
	const taskCount = props.tasks.length
	const headerColor = columnColors[props.status as keyof typeof columnColors] || theme.blue
	const maxVisible = props.maxVisible ?? 5

	// Combined header text to avoid multi-text rendering issues
	const headerText = `${props.title} (${taskCount})`

	// Calculate visible window based on selected task index
	const visibleTasks = useMemo(() => {
		const tasks = props.tasks
		const max = maxVisible

		if (tasks.length <= max) {
			return { tasks, startIndex: 0, hasMore: false, hasPrev: false }
		}

		// Find selected task index in this column
		const selectedIdx = props.selectedTaskIndex ?? 0

		// Calculate window to keep selection visible
		let startIndex = 0
		if (selectedIdx >= max - 1) {
			// Scroll so selection is near bottom of window
			startIndex = Math.min(selectedIdx - max + 2, tasks.length - max)
		}
		startIndex = Math.max(0, startIndex)

		return {
			tasks: tasks.slice(startIndex, startIndex + max),
			startIndex,
			hasMore: startIndex + max < tasks.length,
			hasPrev: startIndex > 0,
		}
	}, [props.tasks, props.selectedTaskIndex, maxVisible])

	return (
		<box flexDirection="column" width="25%" marginRight={1}>
			{/* Column header */}
			<box paddingLeft={1}>
				<text fg={headerColor} attributes={props.isActiveColumn ? ATTR_BOLD : 0}>
					{headerText}
				</text>
			</box>

			{/* Scroll indicator - top */}
			{visibleTasks.hasPrev && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>{"  ↑ " + visibleTasks.startIndex + " more"}</text>
				</box>
			)}

			{/* Task list - windowed */}
			<box flexDirection="column" flexGrow={1}>
				{visibleTasks.tasks.map((task) => (
					<TaskCard
						key={task.id}
						task={task}
						isSelected={props.selectedTaskId === task.id}
						isMultiSelected={props.selectedIds?.has(task.id)}
						jumpLabel={props.taskJumpLabels?.get(task.id)}
						pendingJumpKey={props.pendingJumpKey}
					/>
				))}
			</box>

			{/* Scroll indicator - bottom */}
			{visibleTasks.hasMore && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>
						{"  ↓ " + (taskCount - visibleTasks.startIndex - maxVisible) + " more"}
					</text>
				</box>
			)}
		</box>
	)
}

/**
 * Text attribute for bold
 */
const ATTR_BOLD = 1
