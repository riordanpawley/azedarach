/**
 * TaskCard component - displays a single task in the Kanban board
 */

import { getPriorityColor, theme } from "./theme"
import type { TaskWithSession } from "./types"
import { SESSION_INDICATORS } from "./types"

/** Height of each task card in terminal rows */
export const TASK_CARD_HEIGHT = 6

/**
 * Calculate available width for title text inside a task card.
 *
 * Layout breakdown:
 * - Column width: 25% of terminal width, minus marginRight(1)
 * - Borders: 2 chars (left + right)
 * - Padding: 2 chars (paddingLeft + paddingRight)
 */
const getTitleMaxWidth = (): number => {
	const terminalWidth = process.stdout.columns || 80
	const columnWidth = Math.floor(terminalWidth * 0.25) - 1 // 25% minus marginRight
	const borderAndPadding = 4 // border(2) + padding(2)
	return Math.max(10, columnWidth - borderAndPadding)
}

/**
 * Truncate text to fit within two lines, adding ellipsis if needed.
 * Title is allowed to span 2 lines before truncation kicks in.
 */
const truncateText = (text: string, maxWidthPerLine: number): string => {
	const maxChars = maxWidthPerLine * 2 // Allow 2 lines of text
	if (text.length <= maxChars) return text
	return `${text.slice(0, maxChars - 1)}…`
}

export interface TaskCardProps {
	task: TaskWithSession
	isSelected?: boolean
	isMultiSelected?: boolean
	/** Whether the action menu is currently open (selected card gets prominent border) */
	isActionMode?: boolean
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
	const maxTitleWidth = getTitleMaxWidth()

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

	// Border color: action mode > multi-select > selection > context health > default
	const getBorderColor = () => {
		// Action mode selected card gets mauve (matches action palette)
		if (props.isSelected && props.isActionMode) return theme.mauve
		if (props.isMultiSelected) return theme.mauve
		if (props.isSelected) return theme.lavender
		const healthColor = getContextHealthColor()
		if (healthColor) return healthColor
		return theme.surface1
	}

	// Border style: double border when action mode is active on selected card
	const getBorderStyle = (): "single" | "double" => {
		if (props.isSelected && props.isActionMode) return "double"
		return "single"
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
			borderStyle={getBorderStyle()}
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
			<text fg={theme.text}>{truncateText(props.task.title, maxTitleWidth)}</text>
		</box>
	)
}
