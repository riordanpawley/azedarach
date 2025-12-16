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
	/** Number of image attachments for this task */
	attachmentCount?: number
}

/**
 * TaskCard component
 *
 * Simple two-line card: ID line + title line
 */
export const TaskCard = (props: TaskCardProps) => {
	const indicator = SESSION_INDICATORS[props.task.sessionState]

	// Border color based on selection state
	const getBorderColor = () => {
		if (props.isMultiSelected) return theme.mauve
		if (props.isSelected) return theme.lavender
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
			line += `${props.jumpLabel} `
		}
		line += `${props.task.id} [${props.task.issue_type}]`
		if (indicator) {
			line += ` ${indicator}`
		}
		// Show attachment indicator if there are attachments
		if (props.attachmentCount && props.attachmentCount > 0) {
			line += ` 📎${props.attachmentCount > 1 ? props.attachmentCount : ""}`
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
