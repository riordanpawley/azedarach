/**
 * DetailPanel component - expandable detail view for selected task
 *
 * Uses <scrollbox> for scrollable content when it exceeds the viewport.
 * Keyboard scrolling: Ctrl+u/d for half-page, arrows for single line.
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import type { ScrollBoxRenderable } from "@opentui/core"
import { useEffect, useMemo, useRef, useState } from "react"
import type { DependencyRef } from "../core/BeadsClient.js"
import { currentAttachmentsAtom, detailScrollAtom, epicChildrenAtom } from "./atoms.js"
import { getPriorityColor, theme } from "./theme.js"
import type { TaskWithSession } from "./types.js"
import { PHASE_INDICATORS, PHASE_LABELS, SESSION_INDICATORS } from "./types.js"

// Panel chrome heights for maxHeight calculation
const PANEL_CHROME_HEIGHT = 8 // borders (2) + padding (2) + header (3) + footer (1)

export interface DetailPanelProps {
	task: TaskWithSession
}

const ATTR_BOLD = 1

/**
 * Get status indicator for a child task
 * ○ = open
 * ● = in_progress or blocked
 * ✓ = closed
 */
const getChildStatusIndicator = (child: DependencyRef): string => {
	if (child.status === "closed") return "✓"
	if (child.status === "open") return "○"
	return "●"
}

/**
 * Get status color for a child task
 */
const getChildStatusColor = (status: DependencyRef["status"]): string => {
	switch (status) {
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

/**
 * DetailPanel component
 *
 * Displays a centered modal overlay with full task details:
 * - Title, ID, type, priority, status
 * - Description (wrapped)
 * - Design notes (if present)
 * - Epic children (if task is epic)
 * - Dependencies (future enhancement)
 * - Session status and recent output (future enhancement)
 * - Available actions based on state (future enhancement)
 */
export const DetailPanel = (props: DetailPanelProps) => {
	const indicator = SESSION_INDICATORS[props.task.sessionState]
	const scrollboxRef = useRef<ScrollBoxRenderable>(null)

	// Subscribe to scroll commands from Effect layer
	const scrollCommandResult = useAtomValue(detailScrollAtom)
	const scrollCommand = useMemo(() => {
		if (Result.isSuccess(scrollCommandResult)) {
			return scrollCommandResult.value
		}
		return null
	}, [scrollCommandResult])

	// Execute scroll commands when they change
	useEffect(() => {
		if (scrollboxRef.current && scrollCommand) {
			if (scrollCommand.type === "line") {
				// Scroll by lines (1 line = ~1 row)
				scrollboxRef.current.scrollBy(scrollCommand.amount, "step")
			} else if (scrollCommand.type === "halfPage") {
				// Scroll by half viewport
				scrollboxRef.current.scrollBy(scrollCommand.amount * 0.5, "viewport")
			}
		}
	}, [scrollCommand])

	// Calculate max height for scrollbox based on terminal size
	const maxScrollHeight = useMemo(() => {
		const terminalRows = process.stdout.rows || 24
		// Reserve space for panel chrome and some margin
		return Math.max(10, terminalRows - PANEL_CHROME_HEIGHT - 4)
	}, [])

	// Subscribe to current attachments from Effect layer
	const attachmentsResult = useAtomValue(currentAttachmentsAtom)
	const { attachments, selectedIndex } = useMemo(() => {
		if (Result.isSuccess(attachmentsResult)) {
			const data = attachmentsResult.value
			// Only show attachments if they're for the current task
			if (data?.taskId === props.task.id) {
				return { attachments: data.attachments, selectedIndex: data.selectedIndex }
			}
		}
		return { attachments: [] as readonly never[], selectedIndex: -1 }
	}, [attachmentsResult, props.task.id])

	// Fetch epic children if this is an epic
	const isEpic = props.task.issue_type === "epic"
	const [epicChildren, setEpicChildren] = useState<readonly DependencyRef[]>([])
	const fetchEpicChildren = useAtomSet(epicChildrenAtom(props.task.id), { mode: "promise" })

	useEffect(() => {
		if (isEpic) {
			fetchEpicChildren().then((result) => {
				// appRuntime.fn returns the actual value, not a Result wrapper
				setEpicChildren(result as readonly DependencyRef[])
			})
		} else {
			setEpicChildren([])
		}
	}, [isEpic, fetchEpicChildren])

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
				minWidth={80}
				maxWidth={110}
				flexDirection="column"
			>
				{/* Header with task ID and type - stays fixed */}
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{
						"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
					}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{headerLine}
				</text>
				<text fg={theme.mauve} attributes={ATTR_BOLD}>
					{
						"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
					}
				</text>

				{/* Scrollable content area */}
				<scrollbox
					ref={scrollboxRef}
					scrollY={true}
					maxHeight={maxScrollHeight}
					flexDirection="column"
					flexGrow={1}
				>
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

					{/* Epic Children */}
					{isEpic && epicChildren.length > 0 && (
						<box flexDirection="column">
							<text fg={theme.blue} attributes={ATTR_BOLD}>
								{`Children (${epicChildren.length}):`}
							</text>
							{epicChildren.map((child) => (
								<text key={child.id} fg={getChildStatusColor(child.status)}>
									{`  ${child.id}  ${getChildStatusIndicator(child)}  ${child.title}`}
								</text>
							))}
							<text fg={theme.subtext0}>{"  Press 'o' to orchestrate parallel workers"}</text>
							<text> </text>
						</box>
					)}

					{/* Image Attachments */}
					{attachments.length > 0 && (
						<box flexDirection="column">
							<text fg={theme.blue} attributes={ATTR_BOLD}>
								{`Attachments (${attachments.length}):`}
							</text>
							{attachments.map((attachment, index) => {
								const isSelected = index === selectedIndex
								const icon = attachment.originalPath === "clipboard" ? "📋" : "📁"
								const prefix = isSelected ? "▶ " : "  "
								return (
									<text
										key={attachment.id}
										fg={isSelected ? theme.mauve : theme.text}
										attributes={isSelected ? ATTR_BOLD : 0}
									>
										{`${prefix}${index + 1}. ${icon} ${attachment.filename}`}
									</text>
								)
							})}
							<text fg={theme.subtext0}>
								{selectedIndex >= 0
									? "  j/k:nav  v:preview  o:open  x:remove  i:add  Esc:close"
									: "  j/k:select  i:add  Esc:close"}
							</text>
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
									<text
										fg={theme.text}
									>{`Tokens: ${formatTokens(props.task.estimatedTokens)}`}</text>
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
				</scrollbox>

				{/* Footer instructions - stays fixed */}
				<text> </text>
				<text fg={theme.subtext0}>{"Ctrl+u/d:scroll  Enter/Esc:close"}</text>
			</box>
		</box>
	)
}
