/**
 * TaskCard component - displays a single task in the Kanban board
 */

import { getPriorityColor, theme } from "./theme"
import type { TaskWithSession } from "./types"
import { SESSION_INDICATORS } from "./types"

/** Height of each task card in terminal rows */
export const TASK_CARD_HEIGHT = 6

export interface TaskCardProps {
	task: TaskWithSession
	isSelected?: boolean
	isMultiSelected?: boolean
	jumpLabel?: string
	pendingJumpKey?: string | null
}

/**
 * TaskCard component
 *
 * Simple two-line card: ID line + title line
 */
export const TaskCard = (props: TaskCardProps) => {
	const indicator = SESSION_INDICATORS[props.task.sessionState]

	// Get context health border color based on contextPercent
	// Only applies when session is active (not idle) and has context data
	const getContextHealthColor = (): string | undefined => {
		const { sessionState, contextPercent } = props.task
		if (sessionState === "idle" || contextPercent === undefined) {
			return undefined
		}

		// Thresholds: 0-70% healthy, 70-90% warning, 90%+ critical
		if (contextPercent >= 90) return theme.red
		if (contextPercent >= 70) return theme.yellow
		return theme.lavender
	}

	// Border color: selection takes priority, then context health
	const getBorderColor = () => {
		if (props.isMultiSelected) return theme.mauve
		if (props.isSelected) return theme.lavender
		const healthColor = getContextHealthColor()
		if (healthColor) return healthColor
		return theme.surface1
	}

	// Background color based on selection state
	const getBackgroundColor = () => {
		if (props.isMultiSelected) return theme.surface1
		if (props.isSelected) return theme.surface0
		return undefined
	}

	// Priority label like P1, P2, P3, P4
	const priorityLabel = `P${props.task.priority}`

	// Build the header line: "az-xxx [type]" or "aa az-xxx [type]" in jump mode
	const getHeaderLine = () => {
		let line = ""
		if (props.jumpLabel) {
			line += props.jumpLabel + " "
		}
		line += props.task.id + " [" + props.task.issue_type + "]"
		if (indicator) {
			line += " " + indicator
		}
		if (props.isMultiSelected) {
			line += " *"
		}
		return line
	}

	return (
		<box
			borderStyle="single"
			border={true}
			borderColor={getBorderColor()}
			backgroundColor={getBackgroundColor()}
			paddingLeft={1}
			paddingRight={1}
			height={TASK_CARD_HEIGHT}
			flexDirection="column"
		>
			<box flexDirection="row" gap={1}>
				<text fg={getPriorityColor(props.task.priority)}>{priorityLabel}</text>
				<text fg={theme.overlay0}>{getHeaderLine()}</text>
			</box>
			<text fg={theme.text}>{props.task.title}</text>
		</box>
	)
}
