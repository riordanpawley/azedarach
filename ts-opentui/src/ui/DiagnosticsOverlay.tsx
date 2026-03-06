/**
 * DiagnosticsOverlay component - modal showing system health and fiber status
 */
import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import type { MouseEvent, ScrollBoxRenderable } from "@opentui/core"
import { useEffect, useMemo, useRef } from "react"
import type {
	DiagnosticSeverity,
	FiberStatus,
	IssueSyncLastStatus,
	IssueSyncRuntimeReason,
	LinearWebhookStrategy,
} from "../services/DiagnosticsService.js"
import type { LinearWebhookMode } from "../services/LinearWebhookService.js"
import { diagnosticsAtom, diagnosticsScrollAtom } from "./atoms.js"
import { sanitizeDiagnosticInlineText, sanitizeDiagnosticTextLines } from "./diagnosticsText.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1
const PANEL_CHROME_HEIGHT = 10

/**
 * Format a date as relative time (e.g., "2s ago", "5m ago")
 */
const formatRelativeTime = (date: Date): string => {
	const now = Date.now()
	const diff = now - date.getTime()
	const seconds = Math.floor(diff / 1000)
	const minutes = Math.floor(seconds / 60)
	const hours = Math.floor(minutes / 60)

	if (hours > 0) return `${hours}h ago`
	if (minutes > 0) return `${minutes}m ago`
	return `${seconds}s ago`
}

/**
 * Get color for fiber status
 */
const statusColor = (status: FiberStatus): string => {
	switch (status) {
		case "running":
			return theme.green
		case "completed":
			return theme.blue
		case "interrupted":
			return theme.yellow
		case "failed":
			return theme.red
		default:
			return theme.subtext0
	}
}

/**
 * Get color for service health
 */
const healthColor = (status: "healthy" | "degraded" | "unhealthy"): string => {
	switch (status) {
		case "healthy":
			return theme.green
		case "degraded":
			return theme.yellow
		case "unhealthy":
			return theme.red
		default:
			return theme.subtext0
	}
}

/**
 * Get color for event severity
 */
const severityColor = (severity: DiagnosticSeverity): string => {
	switch (severity) {
		case "info":
			return theme.blue
		case "warning":
			return theme.yellow
		case "error":
			return theme.red
		default:
			return theme.subtext0
	}
}

/**
 * Get icon for event severity
 */
const severityIcon = (severity: DiagnosticSeverity): string => {
	switch (severity) {
		case "info":
			return "ℹ"
		case "warning":
			return "⚠"
		case "error":
			return "✗"
		default:
			return "•"
	}
}

const issueSyncStatusColor = (status: IssueSyncLastStatus): string => {
	switch (status) {
		case "success":
			return theme.green
		case "failure":
			return theme.red
		case "skipped":
			return theme.yellow
		default:
			return theme.subtext0
	}
}

const issueSyncRuntimeReasonColor = (reason: IssueSyncRuntimeReason): string => {
	switch (reason) {
		case "ready":
			return theme.green
		case "missing_api_key":
		case "sync_disabled":
			return theme.yellow
		case "backend_not_linear":
		case "config_error":
			return theme.red
		default:
			return theme.subtext0
	}
}

const linearWebhookModeColor = (mode: LinearWebhookMode): string => {
	switch (mode) {
		case "sdk":
			return theme.green
		case "cli":
			return theme.blue
		case "misconfigured":
			return theme.yellow
		case "failed":
			return theme.red
		default:
			return theme.subtext0
	}
}

const linearWebhookStrategyColor = (strategy: LinearWebhookStrategy): string => {
	switch (strategy) {
		case "sdk-events":
		case "cli-listener":
			return theme.green
		case "cli-fallback-listener":
		case "polling-fallback":
			return theme.yellow
		default:
			return theme.subtext0
	}
}

/**
 * Section header component
 */
const SectionHeader = ({ title }: { title: string }) => (
	<text fg={theme.blue} attributes={ATTR_BOLD}>
		{title}
	</text>
)

/**
 * Service health row
 */
const ServiceRow = ({
	name,
	status,
	details,
	lastActivity,
}: {
	name: string
	status: "healthy" | "degraded" | "unhealthy"
	details?: string
	lastActivity?: Date
}) => {
	const safeName = sanitizeDiagnosticInlineText(name)
	const safeDetails = details === undefined ? "" : sanitizeDiagnosticInlineText(details)

	return (
		<box flexDirection="row">
			<text fg={healthColor(status)}>{status === "healthy" ? "●" : "○"}</text>
			<text fg={theme.text}>{` ${safeName.padEnd(16)}`}</text>
			<text fg={theme.subtext0}>{safeDetails.length > 0 ? ` ${safeDetails}` : ""}</text>
			{lastActivity && <text fg={theme.subtext1}>{` (${formatRelativeTime(lastActivity)})`}</text>}
		</box>
	)
}

/**
 * Fiber status row
 */
const FiberRow = ({
	name,
	status,
	description,
	startedAt,
	endedAt,
	error,
}: {
	name: string
	status: FiberStatus
	description: string
	startedAt: Date
	endedAt?: Date
	error?: string
}) => {
	const safeName = sanitizeDiagnosticInlineText(name)
	const safeDescription = sanitizeDiagnosticInlineText(description)
	const safeError = error === undefined ? undefined : sanitizeDiagnosticInlineText(error)

	return (
		<box flexDirection="column">
			<box flexDirection="row">
				<text fg={statusColor(status)}>
					{status === "running" ? "▶" : status === "failed" ? "✗" : "■"}
				</text>
				<text fg={theme.text}>{` ${safeName.padEnd(24)}`}</text>
				<text fg={statusColor(status)}>{`[${status}]`}</text>
			</box>
			<box flexDirection="row" paddingLeft={2}>
				<text fg={theme.subtext0}>{safeDescription}</text>
			</box>
			<box flexDirection="row" paddingLeft={2}>
				<text fg={theme.subtext1}>
					{`Started: ${formatRelativeTime(startedAt)}`}
					{endedAt ? ` | Ended: ${formatRelativeTime(endedAt)}` : ""}
				</text>
			</box>
			{safeError && (
				<box flexDirection="row" paddingLeft={2}>
					<text fg={theme.red}>{`Error: ${safeError.slice(0, 60)}...`}</text>
				</box>
			)}
		</box>
	)
}

/**
 * Diagnostic event row
 */
const EventRow = ({
	severity,
	source,
	message,
	details,
	timestamp,
}: {
	severity: DiagnosticSeverity
	source: string
	message: string
	details?: string
	timestamp: Date
}) => {
	const safeSource = sanitizeDiagnosticInlineText(source)
	const safeMessage = sanitizeDiagnosticInlineText(message)
	const safeDetailsLines = details === undefined ? [] : sanitizeDiagnosticTextLines(details)

	return (
		<box flexDirection="column">
			<box flexDirection="row">
				<text fg={severityColor(severity)}>{severityIcon(severity)}</text>
				<text fg={theme.text}>{` [${safeSource}] `}</text>
				<text fg={severityColor(severity)}>{safeMessage}</text>
				<text fg={theme.subtext1}>{` (${formatRelativeTime(timestamp)})`}</text>
			</box>
			{safeDetailsLines.length > 0 && (
				<box flexDirection="column" paddingLeft={2}>
					{safeDetailsLines.map((line) => (
						<text key={line} fg={theme.subtext0}>
							{line}
						</text>
					))}
				</box>
			)}
		</box>
	)
}

const IssueDbPerfRow = ({
	backend,
	operation,
	kind,
	count,
	failureCount,
	avgMs,
	p95Ms,
	maxMs,
	lastMs,
	lastStatus,
	lastAt,
}: {
	backend: string
	operation: string
	kind: "read" | "write"
	count: number
	failureCount: number
	avgMs: number
	p95Ms: number
	maxMs: number
	lastMs: number
	lastStatus: "success" | "failure"
	lastAt: Date
}) => {
	const failureRate = count > 0 ? Math.round((failureCount / count) * 100) : 0
	const statusColorValue = lastStatus === "success" ? theme.green : theme.red

	return (
		<box flexDirection="row">
			<text fg={theme.text}>{`${backend.padEnd(6)} ${operation.padEnd(9)}`}</text>
			<text fg={theme.subtext0}>{` ${kind.padEnd(5)} `}</text>
			<text fg={theme.subtext1}>{`n=${String(count).padStart(4)} `}</text>
			<text fg={failureRate > 0 ? theme.yellow : theme.subtext1}>{`fail=${failureRate}% `}</text>
			<text fg={theme.subtext1}>{`avg=${String(avgMs).padStart(4)}ms `}</text>
			<text
				fg={p95Ms >= 300 ? theme.yellow : theme.subtext1}
			>{`p95=${String(p95Ms).padStart(4)}ms `}</text>
			<text
				fg={maxMs >= 500 ? theme.red : theme.subtext1}
			>{`max=${String(maxMs).padStart(4)}ms `}</text>
			<text fg={statusColorValue}>{`last=${lastMs}ms`}</text>
			<text fg={theme.subtext1}>{` (${formatRelativeTime(lastAt)})`}</text>
		</box>
	)
}

/**
 * DiagnosticsOverlay component
 *
 * Displays a centered modal overlay with system diagnostics including:
 * - Service health status
 * - Long-running fiber status
 * - Recent activity
 */
export const DiagnosticsOverlay = () => {
	const diagnosticsResult = useAtomValue(diagnosticsAtom)
	const scrollCommandResult = useAtomValue(diagnosticsScrollAtom)
	const scrollboxRef = useRef<ScrollBoxRenderable>(null)

	// Handle loading/error states - extract fibers, services, and events with defaults
	const fibers = Result.isSuccess(diagnosticsResult) ? diagnosticsResult.value.fibers : []
	const services = Result.isSuccess(diagnosticsResult) ? diagnosticsResult.value.services : []
	const events = Result.isSuccess(diagnosticsResult) ? diagnosticsResult.value.events : []
	const issueDbPerf = Result.isSuccess(diagnosticsResult) ? diagnosticsResult.value.issueDbPerf : []
	const issueSync = Result.isSuccess(diagnosticsResult)
		? diagnosticsResult.value.issueSync
		: undefined
	const linearWebhook = Result.isSuccess(diagnosticsResult)
		? diagnosticsResult.value.linearWebhook
		: undefined
	const sortedIssueDbPerf = [...issueDbPerf].sort((left, right) => right.p95Ms - left.p95Ms)
	const scrollCommand = useMemo(() => {
		if (Result.isSuccess(scrollCommandResult)) {
			return scrollCommandResult.value
		}
		return null
	}, [scrollCommandResult])
	const maxScrollHeight = useMemo(() => {
		const terminalRows = process.stdout.rows || 24
		return Math.max(10, terminalRows - PANEL_CHROME_HEIGHT)
	}, [])

	useEffect(() => {
		if (!scrollboxRef.current || !scrollCommand || scrollCommand.target !== "diagnostics") return
		if (scrollCommand.type === "line") {
			scrollboxRef.current.scrollBy(scrollCommand.amount, "step")
			return
		}
		scrollboxRef.current.scrollBy(scrollCommand.amount * 0.5, "viewport")
	}, [scrollCommand])

	const handleMouseScroll = (event: MouseEvent) => {
		const scroll = event.scroll
		if (!scrollboxRef.current || !scroll) return
		if (scroll.direction !== "up" && scroll.direction !== "down") return
		event.preventDefault()
		event.stopPropagation()
		const delta = Math.max(1, Math.trunc(Math.abs(scroll.delta)))
		const amount = scroll.direction === "down" ? delta : -delta
		scrollboxRef.current.scrollBy(amount, "step")
	}

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
			onMouseScroll={handleMouseScroll}
		>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.teal}
				backgroundColor={theme.base}
				paddingLeft={2}
				paddingRight={2}
				paddingTop={1}
				paddingBottom={1}
				minWidth={70}
				flexDirection="column"
			>
				{/* Header */}
				<text fg={theme.teal} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<text fg={theme.teal} attributes={ATTR_BOLD}>
					{"  SYSTEM DIAGNOSTICS"}
				</text>
				<text fg={theme.teal} attributes={ATTR_BOLD}>
					{"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"}
				</text>
				<scrollbox
					ref={scrollboxRef}
					scrollY={true}
					maxHeight={maxScrollHeight}
					flexDirection="column"
					flexGrow={1}
					onMouseScroll={handleMouseScroll}
				>
					<text> </text>

					{/* Services section */}
					<SectionHeader title="Services:" />
					{services.length === 0 ? (
						<text fg={theme.subtext0}>{"  No services registered"}</text>
					) : (
						services.map((svc) => (
							<ServiceRow
								key={svc.name}
								name={svc.name}
								status={svc.status}
								details={svc.details}
								lastActivity={svc.lastActivity}
							/>
						))
					)}
					<text> </text>

					{/* Fibers section */}
					<SectionHeader title="Long-Running Fibers:" />
					{fibers.length === 0 ? (
						<text fg={theme.subtext0}>{"  No fibers registered"}</text>
					) : (
						fibers.map((fiber) => (
							<FiberRow
								key={fiber.id}
								name={fiber.name}
								status={fiber.status}
								description={fiber.description}
								startedAt={fiber.startedAt}
								endedAt={fiber.endedAt}
								error={fiber.error}
							/>
						))
					)}
					<text> </text>

					{/* Issue database performance section */}
					<SectionHeader title="Linear Webhooks:" />
					{linearWebhook === undefined ? (
						<text fg={theme.subtext0}>{"  No webhook diagnostics yet"}</text>
					) : (
						<box flexDirection="column">
							<box flexDirection="row">
								<text fg={linearWebhookModeColor(linearWebhook.mode)}>
									{`mode=${linearWebhook.mode} `}
								</text>
								<text fg={linearWebhookStrategyColor(linearWebhook.strategy)}>
									{`strategy=${linearWebhook.strategy}`}
								</text>
							</box>
							<box flexDirection="row" paddingLeft={2}>
								<text fg={linearWebhook.healthy ? theme.green : theme.red}>
									{`healthy=${linearWebhook.healthy ? "yes" : "no"}`}
								</text>
								<text
									fg={theme.subtext1}
								>{` (${formatRelativeTime(linearWebhook.updatedAt)})`}</text>
								<text fg={theme.subtext0}>
									{` ${sanitizeDiagnosticInlineText(linearWebhook.message)}`}
								</text>
							</box>
						</box>
					)}
					<text> </text>

					{/* Issue database performance section */}
					<SectionHeader title="Issue Sync:" />
					{issueSync === undefined ? (
						<text fg={theme.subtext0}>{"  No sync diagnostics yet"}</text>
					) : (
						<box flexDirection="column">
							<box flexDirection="row">
								<text fg={theme.text}>{`backend=${issueSync.backend.padEnd(6)} `}</text>
								<text fg={issueSync.syncEnabled ? theme.green : theme.yellow}>
									{`enabled=${issueSync.syncEnabled ? "yes" : "no"} `}
								</text>
								<text fg={theme.subtext1}>{`queue=${issueSync.queueDepth} `}</text>
								<text fg={issueSync.failedCount > 0 ? theme.red : theme.subtext1}>
									{`failed=${issueSync.failedCount}`}
								</text>
							</box>
							<box flexDirection="row" paddingLeft={2}>
								<text
									fg={issueSyncStatusColor(issueSync.lastStatus)}
								>{`last=${issueSync.lastStatus}`}</text>
								{issueSync.lastSyncedAt && (
									<text
										fg={theme.subtext1}
									>{` (${formatRelativeTime(issueSync.lastSyncedAt)})`}</text>
								)}
								<text fg={theme.subtext0}>
									{` ${sanitizeDiagnosticInlineText(issueSync.lastMessage)}`}
								</text>
							</box>
							{issueSync.runtime && (
								<box flexDirection="column">
									<box flexDirection="row" paddingLeft={2}>
										<text
											fg={issueSync.runtime.status === "ready" ? theme.green : theme.yellow}
										>{`runtime=${issueSync.runtime.status} `}</text>
										<text fg={issueSyncRuntimeReasonColor(issueSync.runtime.reason)}>
											{`reason=${issueSync.runtime.reason}`}
										</text>
										<text fg={theme.subtext1}>
											{` (${formatRelativeTime(issueSync.runtime.updatedAt)})`}
										</text>
									</box>
									<box flexDirection="row" paddingLeft={4}>
										<text fg={theme.subtext0}>
											{sanitizeDiagnosticInlineText(
												`path=${issueSync.runtime.projectPath} team=${issueSync.runtime.configuredTeam ?? "<none>"} project=${issueSync.runtime.configuredProject ?? "<none>"} apiKey=${issueSync.runtime.apiKeySource}`,
											)}
										</text>
									</box>
								</box>
							)}
							{issueSync.queue && (
								<box flexDirection="column">
									<box flexDirection="row" paddingLeft={2}>
										<text fg={theme.subtext1}>
											{`queue total=${issueSync.queue.total} ready=${issueSync.queue.pendingReady} delayed=${issueSync.queue.pendingDelayed} active=${issueSync.queue.processingActive} stale=${issueSync.queue.processingStale} failed=${issueSync.queue.failed}`}
										</text>
										<text fg={theme.subtext1}>
											{` (${formatRelativeTime(issueSync.queue.updatedAt)})`}
										</text>
									</box>
								</box>
							)}
							{issueSync.lastRun && (
								<box flexDirection="column">
									<box flexDirection="row" paddingLeft={2}>
										<text fg={theme.subtext1}>
											{`run=${issueSync.lastRun.runId} op=${issueSync.lastRun.operation} `}
										</text>
										<text fg={issueSyncStatusColor(issueSync.lastRun.status)}>
											{`status=${issueSync.lastRun.status}`}
										</text>
										<text fg={theme.subtext1}>
											{` (${formatRelativeTime(issueSync.lastRun.finishedAt)})`}
										</text>
									</box>
									<box flexDirection="row" paddingLeft={4}>
										<text fg={theme.subtext0}>
											{sanitizeDiagnosticInlineText(
												`pushed=${issueSync.lastRun.pushed} pulled=${issueSync.lastRun.pulled} ${issueSync.lastRun.message}`,
											)}
										</text>
									</box>
								</box>
							)}
							{issueSync.lastFailure && (
								<box flexDirection="row" paddingLeft={2}>
									<text fg={theme.red}>
										{sanitizeDiagnosticInlineText(
											`failure ${issueSync.lastFailure.issueId} ${issueSync.lastFailure.operation}: ${issueSync.lastFailure.error}`,
										)}
									</text>
								</box>
							)}
						</box>
					)}
					<text> </text>

					{/* Issue database performance section */}
					<SectionHeader title="Issue DB Perf:" />
					{sortedIssueDbPerf.length === 0 ? (
						<text fg={theme.subtext0}>{"  No issue db metrics yet"}</text>
					) : (
						sortedIssueDbPerf.map((perf) => (
							<IssueDbPerfRow
								key={`${perf.backend}:${perf.operation}`}
								backend={perf.backend}
								operation={perf.operation}
								kind={perf.kind}
								count={perf.count}
								failureCount={perf.failureCount}
								avgMs={perf.avgMs}
								p95Ms={perf.p95Ms}
								maxMs={perf.maxMs}
								lastMs={perf.lastMs}
								lastStatus={perf.lastStatus}
								lastAt={perf.lastAt}
							/>
						))
					)}
					<text> </text>

					{/* Events section - show warnings/errors from system */}
					<SectionHeader title="Recent Events:" />
					{events.length === 0 ? (
						<text fg={theme.subtext0}>{"  No events"}</text>
					) : (
						events
							.slice(-10) // Show last 10 events
							.reverse() // Most recent first
							.map((event) => (
								<EventRow
									key={event.id}
									severity={event.severity}
									source={event.source}
									message={event.message}
									details={event.details}
									timestamp={event.timestamp}
								/>
							))
					)}
					<text> </text>
				</scrollbox>

				{/* Footer */}
				<text fg={theme.subtext0}>
					{"j/k or ↑/↓:scroll  Ctrl+u/d:half page  mouse wheel:scroll  Esc:dismiss"}
				</text>
			</box>
		</box>
	)
}
