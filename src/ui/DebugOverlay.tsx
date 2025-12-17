/**
 * DebugOverlay - Developer tool for debugging service state
 *
 * Shows current state from multiple services:
 * - EditorService: Current mode
 * - NavigationService: Cursor position and focused task ID
 * - SessionManager: Active sessions with states
 * - CommandQueueService: Running/queued operations
 * - BoardService: Task counts and filter state
 *
 * Toggle with 'd' key in normal mode.
 * Auto-refreshes via SubscriptionRef atom subscriptions.
 */

import { Result } from "@effect-atom/atom"
import { useAtomValue } from "@effect-atom/atom-react"
import { HashMap } from "effect"
import {
	boardTasksAtom,
	commandQueueStateAtom,
	filteredTasksByColumnAtom,
	focusedTaskIdAtom,
	modeAtom,
	searchQueryAtom,
	sessionMetricsAtom,
	sortConfigAtom,
} from "./atoms"
import { theme } from "./theme"

const ATTR_BOLD = 1

/**
 * Section header for debug info
 */
const DebugSection = ({ title }: { title: string }) => (
	<text fg={theme.blue} attributes={ATTR_BOLD}>
		{title}
	</text>
)

/**
 * Key-value line for debug info
 */
const DebugLine = ({ label, value }: { label: string; value: string }) => (
	<box flexDirection="row">
		<text fg={theme.subtext0}>{`  ${label}: `}</text>
		<text fg={theme.text}>{value}</text>
	</box>
)

/**
 * DebugOverlay component
 *
 * Semi-transparent overlay on the right side showing service state.
 * Subscribes to multiple atoms for automatic updates.
 */
export const DebugOverlay = () => {
	// Mode state
	const modeResult = useAtomValue(modeAtom)
	const mode = Result.isSuccess(modeResult) ? modeResult.value : { _tag: "normal" as const }

	// Navigation state
	const focusedTaskIdResult = useAtomValue(focusedTaskIdAtom)
	const focusedTaskId = Result.isSuccess(focusedTaskIdResult)
		? focusedTaskIdResult.value
		: "unknown"

	// Sort config
	const sortConfigResult = useAtomValue(sortConfigAtom)
	const sortConfig = Result.isSuccess(sortConfigResult)
		? sortConfigResult.value
		: { field: "session", direction: "desc" }

	// Search query
	const searchQuery = useAtomValue(searchQueryAtom)

	// Board tasks (contains session state info)
	const tasksResult = useAtomValue(boardTasksAtom)
	const tasks = Result.isSuccess(tasksResult) ? tasksResult.value : []
	const totalTasks = tasks.length

	// Filtered tasks
	const filteredResult = useAtomValue(filteredTasksByColumnAtom)
	const filteredTasks = Result.isSuccess(filteredResult) ? filteredResult.value.flat().length : 0

	// Session metrics (Effect HashMap from PTYMonitor - provides estimatedTokens, agentPhase)
	const metricsResult = useAtomValue(sessionMetricsAtom)
	const metricsMap = Result.isSuccess(metricsResult) ? metricsResult.value : HashMap.empty()
	const monitoredSessionCount = HashMap.size(metricsMap)

	// Command queue state (Effect HashMap)
	const queueStateResult = useAtomValue(commandQueueStateAtom)
	const queueState = Result.isSuccess(queueStateResult)
		? queueStateResult.value
		: HashMap.empty<string, { running: unknown | null; queue: readonly unknown[] }>()
	const queuedTaskCount = Array.from(HashMap.values(queueState)).filter(
		(q) => q.running !== null || q.queue.length > 0,
	).length

	// Format mode info
	const modeInfo = (() => {
		switch (mode._tag) {
			case "normal":
				return "normal"
			case "select":
				return `select (${mode.selectedIds.length} selected)`
			case "goto":
				if (mode.gotoSubMode === "pending") return "goto (pending)"
				if (mode.gotoSubMode === "jump") {
					const labelCount = mode.jumpLabels ? Object.keys(mode.jumpLabels).length : 0
					return `goto (jump, ${labelCount} labels)`
				}
				return "goto"
			case "action":
				return "action"
			case "search":
				return `search: "${mode.query}"`
			case "command":
				return `command: "${mode.input}"`
			case "sort":
				return "sort"
		}
	})()

	// Compute session state counts from tasks (tasks have sessionState property)
	const busyCount = tasks.filter((t) => t.sessionState === "busy").length
	const waitingCount = tasks.filter((t) => t.sessionState === "waiting").length
	const activeSessionCount = tasks.filter(
		(t) => t.sessionState === "busy" || t.sessionState === "waiting",
	).length

	return (
		<box
			position="absolute"
			right={1}
			top={1}
			bottom={4}
			width={45}
			flexDirection="column"
			backgroundColor={`${theme.mantle}E8`}
			borderStyle="rounded"
			border={true}
			borderColor={theme.overlay0}
			paddingLeft={1}
			paddingRight={1}
		>
			{/* Header */}
			<text fg={theme.peach} attributes={ATTR_BOLD}>
				{"DEBUG OVERLAY"}
			</text>
			<text fg={theme.overlay0}>{"─".repeat(41)}</text>

			{/* Editor Mode */}
			<DebugSection title="Mode:" />
			<DebugLine label="_tag" value={mode._tag} />
			<DebugLine label="full" value={modeInfo} />
			<text> </text>

			{/* Navigation */}
			<DebugSection title="Navigation:" />
			<DebugLine label="focusedTaskId" value={focusedTaskId ?? "null"} />
			<text> </text>

			{/* Board State */}
			<DebugSection title="Board:" />
			<DebugLine label="totalTasks" value={String(totalTasks)} />
			<DebugLine label="filteredTasks" value={String(filteredTasks)} />
			<DebugLine label="searchQuery" value={searchQuery ? `"${searchQuery}"` : "(none)"} />
			<DebugLine label="sortConfig" value={`${sortConfig.field} ${sortConfig.direction}`} />
			<text> </text>

			{/* Sessions */}
			<DebugSection title="Sessions:" />
			<DebugLine label="active" value={String(activeSessionCount)} />
			<DebugLine label="busy" value={String(busyCount)} />
			<DebugLine label="waiting" value={String(waitingCount)} />
			<DebugLine label="ptyMonitored" value={String(monitoredSessionCount)} />
			<text> </text>

			{/* Command Queue */}
			<DebugSection title="CommandQueue:" />
			<DebugLine label="tasksWithOps" value={String(queuedTaskCount)} />
			<text> </text>

			{/* Footer hint */}
			<text fg={theme.subtext0}>{"Press 'd' or Esc to close"}</text>
		</box>
	)
}
