/**
 * TaskCard component - displays a single task in the Kanban board
 */

import { useAtomValue } from "@effect-atom/atom-react"
import { HashMap } from "effect"
import { beadDevServersAtom, taskRunningOperationAtom } from "./atoms.js"
import { ElapsedTimer } from "./ElapsedTimer.js"
import { getPriorityColor, theme } from "./theme.js"
import type { TaskWithSession } from "./types.js"
import {
	CONFLICT_INDICATOR,
	DEV_SERVER_INDICATOR,
	PHASE_INDICATORS,
	SESSION_INDICATORS,
} from "./types.js"

/**
 * Operation indicators shown when an async operation is running on the task
 *
 * Displayed as a spinning indicator in the header line to show the task
 * has a background operation in progress (merge, cleanup, etc.)
 */
const OPERATION_INDICATORS: Record<string, string> = {
	merge: "⏳",
	"create-pr": "⏳",
	cleanup: "🧹",
	start: "⚡",
	stop: "⏹️",
}

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

	// Subscribe to running operation state for this task
	const runningOperation = useAtomValue(taskRunningOperationAtom(props.task.id))
	const operationIndicator = runningOperation
		? (OPERATION_INDICATORS[runningOperation] ?? "⏳")
		: ""

	// Subscribe to dev server state for this task
	const devServers = useAtomValue(beadDevServersAtom(props.task.id))
	const hasDevServer = HashMap.some(
		devServers,
		(s) => s.status === "running" || s.status === "starting",
	)

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

	// Get phase indicator (only show when session is active and phase is detected)
	const phaseIndicator =
		props.task.sessionState !== "idle" && props.task.agentPhase
			? PHASE_INDICATORS[props.task.agentPhase]
			: ""

	// Show elapsed timer when session is active (initializing, busy or waiting) and we have a start time
	const showTimer =
		(props.task.sessionState === "initializing" ||
			props.task.sessionState === "busy" ||
			props.task.sessionState === "waiting") &&
		props.task.sessionStartedAt !== undefined

	// Threshold for showing line changes - need enough width for "+XXX/-XXX" (~12 chars)
	// At 25+ chars of content width, we have room for line changes
	const LINE_CHANGES_MIN_WIDTH = 25

	// Build git status parts for individual coloring
	// Returns { statusPart, additions, deletions } so each can be colored separately
	const getGitStatusParts = (availableWidth: number) => {
		const { gitBehindCount, hasUncommittedChanges, gitAdditions, gitDeletions } = props.task
		const statusParts: string[] = []

		// Behind count (↓N)
		if (gitBehindCount !== undefined && gitBehindCount > 0) {
			statusParts.push(`↓${gitBehindCount}`)
		}

		// Dirty indicator (●)
		if (hasUncommittedChanges) {
			statusParts.push("●")
		}

		const statusString = statusParts.join(" ")

		// Line changes - only compute if we have enough screen width
		let additions: number | undefined
		let deletions: number | undefined
		if (availableWidth >= LINE_CHANGES_MIN_WIDTH) {
			const add = gitAdditions ?? 0
			const del = gitDeletions ?? 0
			if (add > 0) additions = add
			if (del > 0) deletions = del
		}

		return { statusString, additions, deletions }
	}

	const gitStatus = getGitStatusParts(maxTitleWidth)
	const hasGitStatus =
		gitStatus.statusString || gitStatus.additions !== undefined || gitStatus.deletions !== undefined

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
		// Show conflict indicator when worktree has active merge conflict (e.g., "🔵 ⚔️")
		if (props.task.hasMergeConflict) {
			line += ` ${CONFLICT_INDICATOR}`
		}
		// Show phase indicator after session indicator (e.g., "🔵 📋" = busy + planning)
		if (phaseIndicator) {
			line += ` ${phaseIndicator}`
		}
		// Show dev server indicator when a dev server is running (e.g., "🔵 🖥️" = busy + dev server)
		if (hasDevServer) {
			line += ` ${DEV_SERVER_INDICATOR}`
		}
		// Show operation indicator when an async operation is running (e.g., merge, cleanup)
		if (operationIndicator) {
			line += ` ${operationIndicator}`
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

	// Color for status indicators (↓N behind, ● dirty)
	// Behind: yellow (needs attention), Dirty: red (uncommitted work), Both: red
	const getStatusColor = (): string => {
		const { gitBehindCount, hasUncommittedChanges } = props.task
		if (hasUncommittedChanges) return theme.red
		if (gitBehindCount !== undefined && gitBehindCount > 0) return theme.yellow
		return theme.overlay0
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
				{hasGitStatus && (
					<box flexDirection="row">
						{gitStatus.statusString && <text fg={getStatusColor()}>{gitStatus.statusString} </text>}
						{gitStatus.additions !== undefined && (
							<text fg={theme.green}>+{gitStatus.additions}</text>
						)}
						{gitStatus.additions !== undefined && gitStatus.deletions !== undefined && (
							<text fg={theme.overlay0}>/</text>
						)}
						{gitStatus.deletions !== undefined && (
							<text fg={theme.red}>-{gitStatus.deletions}</text>
						)}
					</box>
				)}
				{showTimer && props.task.sessionStartedAt && (
					<ElapsedTimer startedAt={props.task.sessionStartedAt} />
				)}
			</box>
			<text fg={theme.text}>{truncateText(props.task.title, maxTitleWidth)}</text>
		</box>
	)
}
