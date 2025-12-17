/**
 * DetailPanel component - expandable detail view for selected task
 */
import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { useMemo } from "react"
import { currentAttachmentsAtom } from "./atoms"
import { getPriorityColor, theme } from "./theme"
import type { TaskWithSession } from "./types"
import { PHASE_INDICATORS, PHASE_LABELS, SESSION_INDICATORS } from "./types"

export interface DetailPanelProps {
	task: TaskWithSession
}

const ATTR_BOLD = 1

/**
 * DetailPanel component
 *
 * Displays a centered modal overlay with full task details:
 * - Title, ID, type, priority, status
 * - Description (wrapped)
 * - Design notes (if present)
 * - Dependencies (future enhancement)
 * - Session status and recent output (future enhancement)
 * - Available actions based on state (future enhancement)
 */
export const DetailPanel = (props: DetailPanelProps) => {
	const indicator = SESSION_INDICATORS[props.task.sessionState]

	// Subscribe to current attachments from Effect layer
	const attachmentsResult = useAtomValue(currentAttachmentsAtom)
	const attachments = useMemo(() => {
		if (Result.isSuccess(attachmentsResult)) {
			const data = attachmentsResult.value
			// Only show attachments if they're for the current task
			if (data?.taskId === props.task.id) {
				return data.attachments
			}
		}
		return []
	}, [attachmentsResult, props.task.id])

	// Priority label like P1, P2, P3, P4
	const priorityLabel = `P${props.task.priority}`

	// Format timestamps
	const formatDate = (dateStr: string) => {
		try {
			const date = new Date(dateStr)
			return `${date.toLocaleDateString()} ${date.toLocaleTimeString()}`
		} catch {
			return dateStr
		}
	}

	// Status color based on task status
	const getStatusColor = () => {
		switch (props.task.status) {
			case "open":
				return theme.blue
			case "in_progress":
				return theme.mauve
			case "blocked":
				return theme.red
			case "closed":
				return theme.green
			default:
				return theme.text
		}
	}

	// Context % color based on health thresholds
	// lavender (<70%), yellow (70-90%), red (>90%)
	const getContextColor = (percent: number) => {
		if (percent >= 90) return theme.red
		if (percent >= 70) return theme.yellow
		return theme.lavender
	}

	// Format session duration from start time
	const formatDuration = (startedAt: string) => {
		try {
			const start = new Date(startedAt)
			const now = new Date()
			const diffMs = now.getTime() - start.getTime()
			const diffMins = Math.floor(diffMs / 60000)
			const hours = Math.floor(diffMins / 60)
			const mins = diffMins % 60
			if (hours > 0) {
				return `${hours}h ${mins}m`
			}
			return `${mins}m`
		} catch {
			return "Unknown"
		}
	}

	// Format token count with K suffix
	const formatTokens = (tokens: number) => {
		if (tokens >= 1000) {
			return `${(tokens / 1000).toFixed(1)}K`
		}
		return tokens.toString()
	}

	// Check if session is active (not idle)
	const isSessionActive = props.task.sessionState !== "idle"

	// Check if we have any session metrics to display
	const hasSessionMetrics =
		isSessionActive &&
		(props.task.contextPercent !== undefined ||
			props.task.sessionStartedAt !== undefined ||
			props.task.estimatedTokens !== undefined ||
			props.task.agentPhase !== undefined)

	// Available actions based on task state
	const availableActions = useMemo(() => {
		const actions: string[] = []

		switch (props.task.status) {
			case "open":
				actions.push("Space h - Move to previous column")
				actions.push("Space l - Move to next column (Start)")
				actions.push("s - Start session")
				break
			case "in_progress":
				actions.push("Space h - Move to Open")
				actions.push("Space l - Move to Blocked")
				actions.push("a - Attach to session")
				actions.push("p - Pause session")
				break
			case "blocked":
				actions.push("Space h - Move to In Progress")
				actions.push("Space l - Move to Closed")
				break
			case "closed":
				actions.push("Space h - Reopen")
				break
		}

		return actions
	}, [props.task.status])

	// Build header line
	const headerLine = `  ${props.task.id} [${props.task.issue_type}]${indicator ? ` ${indicator}` : ""}`

	return (
		<box
			position="absolute"
			left={0}
			right={0}
			top={0}
			bottom={0}
			alignItems="center"
			justifyContent="center"
			backgroundColor={`${theme.crust}CC`}
		>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.mauve}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={70}
				maxWidth={90}
				flexDirection="column"
			>
				{/* Header with task ID and type */}
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{headerLine}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text> </text>

				{/* Title */}
				<text fg={theme.text} attributes={ATTR_BOLD}>
					{props.task.title}
				</text>
				<text> </text>

				{/* Metadata row */}
				<box flexDirection="row" gap={2}>
					<text fg={getPriorityColor(props.task.priority)}>{`Priority: ${priorityLabel}`}</text>
					<text fg={getStatusColor()}>{`Status: ${props.task.status}`}</text>
					<text fg={theme.subtext0}>{`Session: ${props.task.sessionState}`}</text>
				</box>
				<text> </text>

				{/* Description */}
				{props.task.description && (
					<box flexDirection="column">
						<text fg={theme.blue} attributes={ATTR_BOLD}>
							{"Description:"}
						</text>
						<text fg={theme.text}>{props.task.description}</text>
						<text> </text>
					</box>
				)}

				{/* Design notes */}
				{props.task.design && (
					<box flexDirection="column">
						<text fg={theme.blue} attributes={ATTR_BOLD}>
							{"Design:"}
						</text>
						<text fg={theme.text}>{props.task.design}</text>
						<text> </text>
					</box>
				)}

				{/* Notes */}
				{props.task.notes && (
					<box flexDirection="column">
						<text fg={theme.blue} attributes={ATTR_BOLD}>
							{"Notes:"}
						</text>
						<text fg={theme.text}>{props.task.notes}</text>
						<text> </text>
					</box>
				)}

				{/* Image Attachments */}
				{attachments.length > 0 && (
					<box flexDirection="column">
						<text fg={theme.blue} attributes={ATTR_BOLD}>
							{`Attachments (${attachments.length}):`}
						</text>
						{attachments.map((attachment, index) => (
							<text key={attachment.id} fg={theme.text}>
								{`  ${index + 1}. ${attachment.originalPath === "clipboard" ? "📋 " : "📁 "}${attachment.filename}`}
							</text>
						))}
						<text fg={theme.subtext0}>{"  (Space+i to add more images)"}</text>
						<text> </text>
					</box>
				)}

				{/* Timestamps */}
				<text fg={theme.subtext0}>{`Created: ${formatDate(props.task.created_at)}`}</text>
				<text fg={theme.subtext0}>{`Updated: ${formatDate(props.task.updated_at)}`}</text>
				{props.task.closed_at && (
					<text fg={theme.subtext0}>{`Closed: ${formatDate(props.task.closed_at)}`}</text>
				)}
				<text> </text>

				{/* Session Metrics - only when session is active */}
				{hasSessionMetrics && (
					<box flexDirection="column">
						<text fg={theme.blue} attributes={ATTR_BOLD}>
							{"Session Metrics:"}
						</text>
						{/* Agent Phase row - displayed prominently when available */}
						{props.task.agentPhase && props.task.agentPhase !== "idle" && (
							<text fg={theme.mauve}>
								{`Phase: ${PHASE_INDICATORS[props.task.agentPhase]} ${PHASE_LABELS[props.task.agentPhase]}`}
							</text>
						)}
						<box flexDirection="row" gap={2}>
							{props.task.contextPercent !== undefined && (
								<text fg={getContextColor(props.task.contextPercent)}>
									{"Context: " +
										props.task.contextPercent +
										"%" +
										(props.task.contextPercent >= 90 ? " !" : "")}
								</text>
							)}
							{props.task.sessionStartedAt !== undefined && (
								<text
									fg={theme.text}
								>{`Duration: ${formatDuration(props.task.sessionStartedAt)}`}</text>
							)}
							{props.task.estimatedTokens !== undefined && (
								<text fg={theme.text}>{`Tokens: ${formatTokens(props.task.estimatedTokens)}`}</text>
							)}
						</box>
						<text> </text>
					</box>
				)}

				{/* Available actions */}
				<text fg={theme.blue} attributes={ATTR_BOLD}>
					{"Available Actions:"}
				</text>
				{availableActions.map((action) => (
					<text key={`availableActions:${action}`} fg={theme.text}>
						{`  ${action}`}
					</text>
				))}
				<text> </text>

				{/* Footer instructions */}
				<text fg={theme.subtext0}>{"Press Enter or Esc to close..."}</text>
			</box>
		</box>
	)
}
