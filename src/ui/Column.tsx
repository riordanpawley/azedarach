/**
 * Column component - displays a vertical list of tasks for a single status
 */
import { useMemo } from "react"
import type { PhaseComputationResult } from "../core/dependencyPhases.js"
import { PhaseSeparator } from "./PhaseSeparator.js"
import { TaskCard } from "./TaskCard.js"
import { columnColors, theme } from "./theme.js"
import type { ColumnStatus, TaskWithSession } from "./types.js"

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
	/** Whether action mode is active (selected card gets prominent border) */
	isActionMode?: boolean
	/** Source bead ID when in merge select mode (highlighted differently) */
	mergeSelectSourceId?: string
	/** Phase computation result for dependency visualization (drill-down mode only) */
	phases?: PhaseComputationResult
}

/**
 * Row item type for interleaved phase separators and tasks
 */
type RowItem =
	| { type: "separator"; phase: number; key: string }
	| { type: "task"; task: TaskWithSession; isBlocked: boolean; key: string }

/**
 * Column component
 *
 * Displays a column header and a windowed list of tasks.
 * Only shows maxVisible tasks at a time, scrolling to keep selection visible.
 *
 * When phases are provided (drill-down mode), tasks are grouped by phase
 * with separators between groups.
 */
export const Column = (props: ColumnProps) => {
	const taskCount = props.tasks.length
	const headerColor = columnColors[props.status as keyof typeof columnColors] || theme.blue
	const maxVisible = props.maxVisible ?? 5

	// DEBUG: Log jump label state for each column
	if (props.taskJumpLabels) {
		const sampleLabels = props.tasks.slice(0, 3).map((t) => ({
			id: t.id,
			label: props.taskJumpLabels?.get(t.id),
		}))
		console.log(
			`[Column ${props.title}] taskJumpLabels size: ${props.taskJumpLabels.size}, samples:`,
			sampleLabels,
		)
	}

	// Combined header text to avoid multi-text rendering issues
	const headerText = `${props.title} (${taskCount})`

	// Build interleaved row items when phases are provided
	const rowItems = useMemo((): RowItem[] => {
		const hasPhases = props.phases && props.phases.maxPhase > 0

		if (!hasPhases) {
			// No phases - just tasks
			return props.tasks.map((task) => ({
				type: "task" as const,
				task,
				isBlocked: false,
				key: task.id,
			}))
		}

		// Group tasks by phase
		const tasksByPhase = new Map<number, TaskWithSession[]>()
		for (const task of props.tasks) {
			const phaseInfo = props.phases!.phases.get(task.id)
			const phase = phaseInfo?.phase ?? 1
			const existing = tasksByPhase.get(phase) ?? []
			existing.push(task)
			tasksByPhase.set(phase, existing)
		}

		// Build interleaved list
		const items: RowItem[] = []
		const sortedPhases = [...tasksByPhase.keys()].sort((a, b) => a - b)

		for (const phase of sortedPhases) {
			const phaseTasks = tasksByPhase.get(phase) ?? []
			if (phaseTasks.length === 0) continue

			// Add separator before each phase group
			items.push({
				type: "separator",
				phase,
				key: `sep-${phase}`,
			})

			// Add tasks for this phase
			for (const task of phaseTasks) {
				items.push({
					type: "task",
					task,
					isBlocked: phase > 1,
					key: task.id,
				})
			}
		}

		return items
	}, [props.tasks, props.phases])

	// Calculate visible window based on selected task index
	// When using phases, we need to account for separator height
	const visibleData = useMemo(() => {
		const max = maxVisible

		// Find the task items (for windowing calculation)
		const taskItems = rowItems.filter(
			(item): item is Extract<RowItem, { type: "task" }> => item.type === "task",
		)

		if (taskItems.length <= max) {
			// All tasks fit - show all row items
			return {
				items: rowItems,
				hasMore: false,
				hasPrev: false,
				hiddenBefore: 0,
				hiddenAfter: 0,
			}
		}

		// Find selected task index
		const selectedIdx = props.selectedTaskIndex ?? 0

		// Calculate window to keep selection visible
		let startTaskIdx = 0
		if (selectedIdx >= max - 1) {
			// Scroll so selection is near bottom of window
			startTaskIdx = Math.min(selectedIdx - max + 2, taskItems.length - max)
		}
		startTaskIdx = Math.max(0, startTaskIdx)
		const endTaskIdx = startTaskIdx + max

		// Get the task IDs that should be visible
		const visibleTaskIds = new Set(
			taskItems.slice(startTaskIdx, endTaskIdx).map((item) => item.task.id),
		)

		// Filter rowItems to only include:
		// 1. Tasks in the visible window
		// 2. Separators for phases that have visible tasks
		const visiblePhases = new Set<number>()
		for (let i = startTaskIdx; i < endTaskIdx && i < taskItems.length; i++) {
			const phaseInfo = props.phases?.phases.get(taskItems[i]!.task.id)
			if (phaseInfo) {
				visiblePhases.add(phaseInfo.phase)
			}
		}

		const visibleItems = rowItems.filter((item) => {
			if (item.type === "task") {
				return visibleTaskIds.has(item.task.id)
			}
			// Include separator if its phase has visible tasks
			return visiblePhases.has(item.phase)
		})

		return {
			items: visibleItems,
			hasMore: endTaskIdx < taskItems.length,
			hasPrev: startTaskIdx > 0,
			hiddenBefore: startTaskIdx,
			hiddenAfter: taskItems.length - endTaskIdx,
		}
	}, [rowItems, props.selectedTaskIndex, maxVisible, props.phases])

	return (
		<box flexDirection="column" width="25%" marginRight={1}>
			{/* Column header */}
			<box paddingLeft={1}>
				<text fg={headerColor} attributes={props.isActiveColumn ? ATTR_BOLD | ATTR_UNDERLINE : 0}>
					{headerText}
				</text>
			</box>

			{/* Scroll indicator - top */}
			{visibleData.hasPrev && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>{`  ↑ ${visibleData.hiddenBefore} more`}</text>
				</box>
			)}

			{/* Task list - windowed with optional phase separators */}
			<box flexDirection="column" flexGrow={1}>
				{visibleData.items.map((item) =>
					item.type === "separator" ? (
						<PhaseSeparator key={item.key} phase={item.phase} />
					) : (
						<TaskCard
							key={item.key}
							task={item.task}
							isSelected={props.selectedTaskId === item.task.id}
							isMultiSelected={props.selectedIds?.has(item.task.id)}
							isActionMode={props.isActionMode}
							jumpLabel={props.taskJumpLabels?.get(item.task.id)}
							pendingJumpKey={props.pendingJumpKey}
							isMergeSource={props.mergeSelectSourceId === item.task.id}
							isBlocked={item.isBlocked}
						/>
					),
				)}
			</box>

			{/* Scroll indicator - bottom */}
			{visibleData.hasMore && (
				<box paddingLeft={1}>
					<text fg={theme.overlay0}>{`  ↓ ${visibleData.hiddenAfter} more`}</text>
				</box>
			)}
		</box>
	)
}

/**
 * Text attributes (bit flags from @opentui/core TextAttributes)
 */
const ATTR_BOLD = 1
const ATTR_UNDERLINE = 8
