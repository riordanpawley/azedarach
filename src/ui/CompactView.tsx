/**
 * CompactView component - displays tasks in a linear list format
 *
 * A space-efficient alternative to the Kanban board that shows all tasks
 * in a single scrollable list, with one row per task.
 */

import { useAtomValue } from "@effect-atom/atom-react"
import type { ReadonlyRecord } from "effect/Record"
import { useMemo } from "react"
import { taskRunningOperationAtom } from "./atoms.js"
import { columnColors, getPriorityColor, theme } from "./theme.js"
import type { JumpTarget, TaskWithSession } from "./types.js"
import { COLUMNS, SESSION_INDICATORS, WORKTREE_INDICATOR } from "./types.js"

/**
 * Operation indicators shown when an async operation is running on the task
 */
const OPERATION_INDICATORS: Record<string, string> = {
	merge: "⏳",
	"create-pr": "⏳",
	cleanup: "🧹",
	start: "⚡",
	stop: "⏹️",
}

/** Height of each row in compact view (terminal rows) */
export const COMPACT_ROW_HEIGHT = 1

export interface CompactViewProps {
	tasks: readonly TaskWithSession[]
	selectedTaskId?: string
	activeColumnIndex?: number
	activeTaskIndex?: number
	selectedIds?: Set<string>
	jumpLabels?: ReadonlyRecord<string, JumpTarget> | null
	pendingJumpKey?: string | null
	terminalHeight?: number
	/** Whether action mode is active (selected row gets prominent indicator) */
	isActionMode?: boolean
	/** Source bead ID when in merge select mode (highlighted differently) */
	mergeSelectSourceId?: string
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
	/** Whether action mode is active (selected row gets prominent indicator) */
	isActionMode?: boolean
	jumpLabel?: string
	pendingJumpKey?: string | null
	/** Whether this task is the source bead in merge select mode */
	isMergeSource?: boolean
}

const CompactRow = (props: CompactRowProps) => {
	// Show worktree indicator for idle tasks with worktrees, otherwise session indicator
	const indicator =
		props.task.hasWorktree && props.task.sessionState === "idle"
			? WORKTREE_INDICATOR
			: SESSION_INDICATORS[props.task.sessionState]
	const priorityLabel = `P${props.task.priority}`
	const statusLabel = getStatusLabel(props.task.status)
	const statusColor = getStatusColor(props.task.status)
	const typeLabel = getTypeLabel(props.task.issue_type)
	const typeColor = getTypeColor(props.task.issue_type)

	// Subscribe to running operation state for this task
	const runningOperation = useAtomValue(taskRunningOperationAtom(props.task.id))
	const operationIndicator = runningOperation
		? (OPERATION_INDICATORS[runningOperation] ?? "⏳")
		: ""

	// Action mode selected gets mauve background
	const isActionSelected = props.isSelected && props.isActionMode

	// Background color based on selection state
	// Cursor (isSelected) takes priority over multi-select for visibility
	const getBackgroundColor = () => {
		if (props.isMergeSource) return theme.surface1 // Merge source highlight
		if (isActionSelected) return theme.surface1 // More prominent when action menu open
		if (props.isSelected) return theme.surface0 // Cursor takes priority
		if (props.isMultiSelected) return theme.surface1
		return undefined
	}

	// Selection indicator: ⊕ when merge source, » when action mode, › when selected
	const getSelectionIndicator = () => {
		if (props.isMergeSource) return "⊕"
		if (isActionSelected) return "»"
		if (props.isSelected) return "›"
		return " "
	}

	// Indicator color
	const getIndicatorColor = () => {
		if (props.isMergeSource) return theme.flamingo
		if (isActionSelected) return theme.mauve
		return theme.lavender
	}

	// Truncate title to fit in available space
	const maxTitleLength = 45
	const truncatedTitle =
		props.task.title.length > maxTitleLength
			? `${props.task.title.slice(0, maxTitleLength - 1)}…`
			: props.task.title

	return (
		<box
			flexDirection="row"
			gap={1}
			backgroundColor={getBackgroundColor()}
			paddingLeft={1}
			paddingRight={1}
		>
			{/* Selection indicator (shows ⊕ when merge source, » when action menu open, › when selected) */}
			<text fg={getIndicatorColor()}>{getSelectionIndicator()}</text>

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

			{/* Operation indicator (when async operation is running) */}
			<text>{operationIndicator || " "}</text>

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
		for (const [label, target] of Object.entries(labels)) {
			taskToLabel.set(target.taskId, label)
		}
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
				{/* Space for selection indicator column */}
				<text fg={theme.overlay0}> </text>
				{props.jumpLabels && <text fg={theme.overlay0}>{"  "}</text>}
				<text fg={theme.overlay0}>Pri</text>
				<text fg={theme.overlay0}>Stat</text>
				<text fg={theme.overlay0}>Type</text>
				<text fg={theme.overlay0}> </text>
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
					isActionMode={props.isActionMode}
					jumpLabel={taskJumpLabels?.get(task.id)}
					pendingJumpKey={props.pendingJumpKey}
					isMergeSource={props.mergeSelectSourceId === task.id}
				/>
			))}

			{/* Scroll indicator (below) */}
			{hasMoreBelow && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>
						↓ {sortedTasks.length - scrollOffset - visibleTasks.length} more
					</text>
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
