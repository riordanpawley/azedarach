/**
 * CompactView component - displays tasks in a linear list format
 *
 * A space-efficient alternative to the Kanban board that shows all tasks
 * in a single scrollable list, with one row per task.
 */

import { useMemo } from "react"
import { columnColors, getPriorityColor, theme } from "./theme"
import type { ColumnStatus, JumpTarget, TaskWithSession } from "./types"
import { COLUMNS, SESSION_INDICATORS } from "./types"

/** Height of each row in compact view (terminal rows) */
export const COMPACT_ROW_HEIGHT = 1

export interface CompactViewProps {
	tasks: TaskWithSession[]
	selectedTaskId?: string
	activeColumnIndex?: number
	activeTaskIndex?: number
	selectedIds?: Set<string>
	jumpLabels?: ReadonlyMap<string, JumpTarget> | null
	pendingJumpKey?: string | null
	terminalHeight?: number
}

/**
 * Get the column color for a status
 */
function getStatusColor(status: string): string {
	return columnColors[status as keyof typeof columnColors] ?? theme.overlay0
}

/**
 * Get short status label
 */
function getStatusLabel(status: string): string {
	switch (status) {
		case "open":
			return "OPEN"
		case "in_progress":
			return "PROG"
		case "blocked":
			return "BLKD"
		case "closed":
			return "DONE"
		default:
			return status.slice(0, 4).toUpperCase()
	}
}

/**
 * Get short type label
 */
function getTypeLabel(type: string): string {
	switch (type) {
		case "task":
			return "task"
		case "bug":
			return "bug "
		case "feature":
			return "feat"
		case "epic":
			return "epic"
		case "chore":
			return "chor"
		default:
			return type.slice(0, 4)
	}
}

/**
 * Get type color
 */
function getTypeColor(type: string): string {
	switch (type) {
		case "bug":
			return theme.red
		case "feature":
			return theme.green
		case "epic":
			return theme.mauve
		case "chore":
			return theme.overlay0
		default:
			return theme.blue
	}
}

/**
 * CompactRow - displays a single task as a compact row
 */
interface CompactRowProps {
	task: TaskWithSession
	isSelected?: boolean
	isMultiSelected?: boolean
	jumpLabel?: string
	pendingJumpKey?: string | null
}

const CompactRow = (props: CompactRowProps) => {
	const indicator = SESSION_INDICATORS[props.task.sessionState]
	const priorityLabel = `P${props.task.priority}`
	const statusLabel = getStatusLabel(props.task.status)
	const statusColor = getStatusColor(props.task.status)
	const typeLabel = getTypeLabel(props.task.issue_type)
	const typeColor = getTypeColor(props.task.issue_type)

	// Background color based on selection state
	const getBackgroundColor = () => {
		if (props.isMultiSelected) return theme.surface1
		if (props.isSelected) return theme.surface0
		return undefined
	}

	// Truncate title to fit in available space
	const maxTitleLength = 45
	const truncatedTitle =
		props.task.title.length > maxTitleLength
			? props.task.title.slice(0, maxTitleLength - 1) + "…"
			: props.task.title

	return (
		<box
			flexDirection="row"
			gap={1}
			backgroundColor={getBackgroundColor()}
			paddingLeft={1}
			paddingRight={1}
		>
			{/* Jump label (if in jump mode) */}
			{props.jumpLabel && <text fg={theme.mauve}>{props.jumpLabel}</text>}

			{/* Priority */}
			<text fg={getPriorityColor(props.task.priority)}>{priorityLabel}</text>

			{/* Status */}
			<text fg={statusColor}>{statusLabel}</text>

			{/* Type */}
			<text fg={typeColor}>{typeLabel}</text>

			{/* Session indicator */}
			<text>{indicator || " "}</text>

			{/* Task ID */}
			<text fg={theme.overlay0}>{props.task.id}</text>

			{/* Multi-select marker */}
			{props.isMultiSelected && <text fg={theme.mauve}>*</text>}

			{/* Title */}
			<text fg={theme.text}>{truncatedTitle}</text>
		</box>
	)
}

/**
 * CompactView component
 *
 * Displays all tasks in a linear list format, sorted by status then priority.
 * More compact than the Kanban view - shows more tasks at once.
 */
export const CompactView = (props: CompactViewProps) => {
	// Flatten all tasks and sort by status (column order) then priority
	const sortedTasks = useMemo(() => {
		const statusOrder = COLUMNS.reduce(
			(acc, col, idx) => {
				acc[col.status] = idx
				return acc
			},
			{} as Record<string, number>,
		)

		return [...props.tasks].sort((a, b) => {
			// First sort by status (column order)
			const statusDiff = (statusOrder[a.status] ?? 99) - (statusOrder[b.status] ?? 99)
			if (statusDiff !== 0) return statusDiff
			// Then sort by priority (lower = higher priority)
			return a.priority - b.priority
		})
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

	// Calculate visible window for virtual scrolling
	const { visibleTasks, scrollOffset, hasMoreAbove, hasMoreBelow } = useMemo(() => {
		const maxVisible = props.terminalHeight ?? 20

		// Find the index of the selected task in the sorted list
		let selectedIndex = 0
		if (props.selectedTaskId) {
			const idx = sortedTasks.findIndex((t) => t.id === props.selectedTaskId)
			if (idx >= 0) selectedIndex = idx
		}

		// Calculate scroll window to keep selection visible
		let startIndex = 0
		if (sortedTasks.length > maxVisible) {
			// Keep selection near the middle of the visible area
			startIndex = Math.max(0, selectedIndex - Math.floor(maxVisible / 2))
			startIndex = Math.min(startIndex, sortedTasks.length - maxVisible)
		}

		const endIndex = Math.min(startIndex + maxVisible, sortedTasks.length)

		return {
			visibleTasks: sortedTasks.slice(startIndex, endIndex),
			scrollOffset: startIndex,
			hasMoreAbove: startIndex > 0,
			hasMoreBelow: endIndex < sortedTasks.length,
		}
	}, [sortedTasks, props.selectedTaskId, props.terminalHeight])

	return (
		<box flexDirection="column" width="100%" height="100%" padding={1}>
			{/* Header row */}
			<box flexDirection="row" gap={1} paddingLeft={1} paddingRight={1}>
				{props.jumpLabels && <text fg={theme.overlay0}>{"  "}</text>}
				<text fg={theme.overlay0}>Pri</text>
				<text fg={theme.overlay0}>Stat</text>
				<text fg={theme.overlay0}>Type</text>
				<text fg={theme.overlay0}> </text>
				<text fg={theme.overlay0}>ID</text>
				<text fg={theme.overlay0}>{"       "}</text>
				<text fg={theme.overlay0}>Title</text>
			</box>

			{/* Scroll indicator (above) */}
			{hasMoreAbove && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>↑ {scrollOffset} more</text>
				</box>
			)}

			{/* Task rows */}
			{visibleTasks.map((task) => (
				<CompactRow
					key={task.id}
					task={task}
					isSelected={task.id === props.selectedTaskId}
					isMultiSelected={props.selectedIds?.has(task.id)}
					jumpLabel={taskJumpLabels?.get(task.id)}
					pendingJumpKey={props.pendingJumpKey}
				/>
			))}

			{/* Scroll indicator (below) */}
			{hasMoreBelow && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>↓ {sortedTasks.length - scrollOffset - visibleTasks.length} more</text>
				</box>
			)}

			{/* Empty state */}
			{sortedTasks.length === 0 && (
				<box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
					<text fg={theme.overlay0}>No tasks</text>
				</box>
			)}
		</box>
	)
}
