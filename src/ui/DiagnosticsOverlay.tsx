/**
 * DiagnosticsOverlay component - modal showing system health and fiber status
 */
import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import type { FiberStatus } from "../services/DiagnosticsService.js"
import { diagnosticsAtom } from "./atoms.js"
import { theme } from "./theme.js"

const ATTR_BOLD = 1

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
}) => (
	<box flexDirection="row">
		<text fg={healthColor(status)}>{status === "healthy" ? "●" : "○"}</text>
		<text fg={theme.text}>{` ${name.padEnd(16)}`}</text>
		<text fg={theme.subtext0}>{details ? ` ${details}` : ""}</text>
		{lastActivity && <text fg={theme.subtext1}>{` (${formatRelativeTime(lastActivity)})`}</text>}
	</box>
)

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
}) => (
	<box flexDirection="column">
		<box flexDirection="row">
			<text fg={statusColor(status)}>
				{status === "running" ? "▶" : status === "failed" ? "✗" : "■"}
			</text>
			<text fg={theme.text}>{` ${name.padEnd(24)}`}</text>
			<text fg={statusColor(status)}>{`[${status}]`}</text>
		</box>
		<box flexDirection="row" paddingLeft={2}>
			<text fg={theme.subtext0}>{description}</text>
		</box>
		<box flexDirection="row" paddingLeft={2}>
			<text fg={theme.subtext1}>
				{`Started: ${formatRelativeTime(startedAt)}`}
				{endedAt ? ` | Ended: ${formatRelativeTime(endedAt)}` : ""}
			</text>
		</box>
		{error && (
			<box flexDirection="row" paddingLeft={2}>
				<text fg={theme.red}>{`Error: ${error.slice(0, 60)}...`}</text>
			</box>
		)}
	</box>
)

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

	// Handle loading/error states - extract fibers and services with defaults
	const fibers = Result.isSuccess(diagnosticsResult) ? diagnosticsResult.value.fibers : []
	const services = Result.isSuccess(diagnosticsResult) ? diagnosticsResult.value.services : []

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

				{/* Footer */}
				<text fg={theme.subtext0}>{"Press Esc to dismiss..."}</text>
			</box>
		</box>
	)
}
