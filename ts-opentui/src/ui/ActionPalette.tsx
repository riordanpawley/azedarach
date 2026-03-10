/**
 * ActionPalette component - non-intrusive action menu (bottom-right, like Helix)
 */
import { MouseButton, type MouseEvent } from "@opentui/core"
import { useMemo, useState } from "react"
import type { WorkflowMode } from "../config/schema.js"
import { isActionPaletteNetworkAction } from "../lib/cleanupPolicy.js"
import type { DevServerStatus } from "../services/DevServerService.js"
import { theme } from "./theme.js"
import type { TaskWithSession } from "./types.js"

export interface ActionPaletteProps {
	task?: TaskWithSession
	/** Running operation label (e.g., "merge", "cleanup") - dims queued actions */
	runningOperation?: string | null
	/** Whether network is available for remote-dependent actions */
	isOnline?: boolean
	/** Dev server status for the current task */
	devServerStatus?: DevServerStatus
	/** Dev server port (if running) */
	devServerPort?: number
	/** Workflow mode: 'local' hides PR action, 'origin' hides merge action */
	workflowMode?: WorkflowMode
	/** Current drilldown epic ID (when viewing inside an epic) */
	drillDownEpicId?: string
	/** Compact palette variant for small terminals. */
	compact?: boolean
	/** Execute an action key sequence (same path as keyboard handler) */
	onActionSelect?: (keySeq: string) => void
}

const _ATTR_BOLD = 1
const ATTR_DIM = 2

/**
 * Actions that use the command queue and should be blocked when busy.
 * These are the actions that call ctx.withQueue() in their handlers.
 */
const QUEUED_ACTIONS = new Set(["s", "S", "!", "x", "P", "m", "d", "u"])

/**
 * ActionPalette component
 *
 * Displays a small floating panel in the bottom-right corner showing available
 * actions. Non-intrusive design allows seeing the board while moving tasks.
 *
 * When an operation is in progress (runningOperation is set), queued actions
 * are dimmed and will show an error toast if pressed.
 */
const ACTION_KEY_SEQUENCE_MAP: Readonly<Record<string, string>> = {
	h: "h",
	l: "l",
	s: "s",
	S: "S-s",
	"!": "!",
	a: "a",
	p: "p",
	R: "S-r",
	x: "x",
	r: "r",
	H: "S-h",
	i: "i",
	F: "S-f",
	u: "u",
	f: "f",
	P: "S-p",
	O: "S-o",
	m: "m",
	d: "d",
	D: "S-d",
}

interface ActionLineSpec {
	keyName: string
	description: string
}

export const ActionPalette = (props: ActionPaletteProps) => {
	const sessionState = props.task?.sessionState ?? "idle"
	const hasWorktree = props.task?.hasWorktree ?? false
	const runningOperation = props.runningOperation ?? null
	const isOnline = props.isOnline ?? true
	const devServerStatus = props.devServerStatus ?? "idle"
	const devServerPort = props.devServerPort
	const workflowMode = props.workflowMode ?? "origin"
	const drillDownEpicId = props.drillDownEpicId
	const compact = props.compact ?? false
	const parentEpicId = props.task?.parentEpicId
	const issueType = props.task?.issue_type
	const isEpic = issueType === "epic"
	const [showAllCompactActions, setShowAllCompactActions] = useState(false)

	// Check if this is an orphaned worktree (worktree exists but no session)
	const isOrphanedWorktree = hasWorktree && sessionState === "idle"

	// Helper to check if an action is available based on session state
	const isAvailableByState = (action: string): boolean => {
		switch (action) {
			case "s": // Start - only if idle
			case "S": // Start+work - only if idle
			case "!": // Start+work (skip permissions) - only if idle
				return sessionState === "idle"
			case "a": // Attach - only if not idle
				return sessionState !== "idle"
			case "p": // Pause - only if busy
				return sessionState === "busy"
			case "r": // Dev server toggle - only if worktree exists (session not idle)
				return sessionState !== "idle"
			case "R": // Resume - only if paused
				return sessionState === "paused"
			case "x": // Stop - only if not idle
				return sessionState !== "idle"
			case "P": // Create PR - only if session has worktree (not idle) OR orphaned worktree
				return sessionState !== "idle" || isOrphanedWorktree
			case "m": // Merge - only if session has worktree (not idle) OR orphaned worktree
				return sessionState !== "idle" || isOrphanedWorktree
			case "d": // Cleanup/Delete worktree - session exists OR orphaned worktree
				return sessionState !== "idle" || isOrphanedWorktree
			case "f": // Diff vs main - only if session has worktree (not idle) OR orphaned worktree
				return sessionState !== "idle" || isOrphanedWorktree
			case "u": // Update from main - only if session has worktree (not idle) OR orphaned worktree
				return sessionState !== "idle" || isOrphanedWorktree
			case "D": // Delete bead - always available
				return true
			case "i": // Image attach - always available
				return true
			case "F": // Fork bead - only if not epic and not epic child
				return props.task !== undefined && !isEpic && parentEpicId === undefined
			case "O": // Open PR - only if task has a PR
				return props.task?.hasPR === true
			case "H": // Helix editor - only if worktree exists (active session or orphaned)
				return sessionState !== "idle" || isOrphanedWorktree
			case "h": // Move left - always available
			case "l": // Move right - always available
				return true
			default:
				return false
		}
	}

	// Get dev server status text
	const getDevServerLabel = (): string => {
		switch (devServerStatus) {
			case "running":
				return devServerPort ? `dev :${devServerPort}` : "dev (running)"
			case "starting":
				return "dev (starting)"
			case "error":
				return "dev (error)"
			default:
				return "dev server"
		}
	}

	// Full availability check: state + queue busyness + network + workflow mode
	const isAvailable = (action: string): boolean => {
		// Merge is blocked in origin mode UNLESS task is epic child or in drilldown
		// (epic children merge to parent epic, not main - so they're allowed)
		if (action === "m" && workflowMode === "origin" && !parentEpicId && !drillDownEpicId)
			return false
		if (action === "P" && workflowMode === "local") return false

		// If task is busy with a queued operation, block queued actions
		if (runningOperation !== null && QUEUED_ACTIONS.has(action)) {
			return false
		}
		// Network actions unavailable when offline
		if (!isOnline && isActionPaletteNetworkAction(action)) {
			return false
		}
		return isAvailableByState(action)
	}

	// Check if action is disabled due to offline
	const isOfflineBlocked = (action: string): boolean => {
		return !isOnline && isActionPaletteNetworkAction(action)
	}

	const compactActions = useMemo<ReadonlyArray<ActionLineSpec>>(() => {
		const actions: ActionLineSpec[] = [
			{ keyName: "h", description: "← move" },
			{ keyName: "l", description: "→ move" },
		]

		if (sessionState === "idle") {
			actions.push({ keyName: "s", description: "start" })
			actions.push({ keyName: "S", description: "start+work" })
		} else {
			actions.push({ keyName: "a", description: "attach" })
			if (sessionState === "busy") {
				actions.push({ keyName: "p", description: "pause" })
			}
			if (sessionState === "paused") {
				actions.push({ keyName: "R", description: "resume" })
			}
			actions.push({ keyName: "x", description: "stop" })
		}

		actions.push({ keyName: "i", description: "image" })
		actions.push({ keyName: "f", description: "diff" })
		actions.push({ keyName: "d", description: "cleanup" })
		return actions
	}, [sessionState])

	// Action line component
	const ActionLine = ({ keyName, description }: { keyName: string; description: string }) => {
		const available = isAvailable(keyName)
		const offlineBlocked = isOfflineBlocked(keyName)
		const fgColor = available ? theme.text : theme.overlay0
		const keyColor = available ? theme.lavender : theme.overlay0
		const attrs = available ? 0 : ATTR_DIM

		// Show "(offline)" suffix for network actions when offline
		const displayDesc = offlineBlocked ? `${description} (offline)` : description
		const mappedKey = ACTION_KEY_SEQUENCE_MAP[keyName]

		const handleMouseDown = (event: MouseEvent) => {
			if (!available || !mappedKey || !props.onActionSelect) return
			if (event.button !== MouseButton.LEFT) return

			event.preventDefault()
			event.stopPropagation()
			props.onActionSelect(mappedKey)
		}

		return (
			// biome-ignore lint/a11y/noStaticElementInteractions: OpenTUI uses <box> as the interactive mouse hit target.
			<box flexDirection="row" onMouseDown={handleMouseDown}>
				<text fg={keyColor} attributes={attrs}>
					{keyName}
				</text>
				<text fg={fgColor} attributes={attrs}>
					{` ${displayDesc}`}
				</text>
			</box>
		)
	}

	const handleToggleCompactActions = (event: MouseEvent) => {
		if (event.button !== MouseButton.LEFT) return
		event.preventDefault()
		event.stopPropagation()
		setShowAllCompactActions((current) => !current)
	}

	const CompactToggleLine = () => (
		// biome-ignore lint/a11y/noStaticElementInteractions: OpenTUI uses <box> as the interactive mouse hit target.
		<box flexDirection="row" onMouseDown={handleToggleCompactActions}>
			<text fg={theme.lavender}>{showAllCompactActions ? "less actions" : "more actions"}</text>
		</box>
	)

	const renderFullPalette = () => (
		<>
			{/* Move actions - most common, at top */}
			<ActionLine keyName="h" description="← move" />
			<ActionLine keyName="l" description="→ move" />
			<text fg={theme.surface1}>{"─────────"}</text>

			{/* Session actions */}
			<ActionLine keyName="s" description="start" />
			<ActionLine keyName="S" description="start+work" />
			<ActionLine keyName="!" description="start (yolo)" />
			<ActionLine keyName="a" description="attach" />
			<ActionLine keyName="p" description="pause" />
			<ActionLine keyName="R" description="resume" />
			<ActionLine keyName="x" description="stop" />
			<text fg={theme.surface1}>{"─────────"}</text>

			{/* Dev server */}
			<ActionLine keyName="r" description={getDevServerLabel()} />
			<text fg={theme.surface1}>{"─────────"}</text>

			{/* Task actions */}
			<ActionLine keyName="H" description="helix" />
			<ActionLine keyName="i" description="image" />
			<ActionLine keyName="F" description="fork" />
			<text fg={theme.surface1}>{"─────────"}</text>

			{/* Git/PR */}
			<ActionLine keyName="u" description="update" />
			<ActionLine keyName="f" description="diff" />
			<ActionLine keyName="P" description="PR" />
			<ActionLine keyName="O" description="open PR" />
			<ActionLine keyName="m" description="merge" />
			<ActionLine keyName="d" description="cleanup" />
			<ActionLine keyName="D" description="delete" />
		</>
	)

	const renderCompactPalette = () => (
		<>
			{compactActions.map((action) => (
				<ActionLine
					key={`compact:${action.keyName}:${action.description}`}
					keyName={action.keyName}
					description={action.description}
				/>
			))}
		</>
	)

	return (
		<box position="absolute" right={1} bottom={4}>
			<box
				borderStyle="rounded"
				border={true}
				borderColor={theme.surface1}
				backgroundColor={theme.base}
				paddingLeft={1}
				paddingRight={1}
				flexDirection="column"
			>
				{compact ? (
					<>
						<CompactToggleLine />
						<text fg={theme.surface1}>{"─────────"}</text>
						{showAllCompactActions ? renderFullPalette() : renderCompactPalette()}
					</>
				) : (
					renderFullPalette()
				)}

				{/* Busy indicator */}
				{runningOperation && (
					<>
						<text fg={theme.surface1}>{"─────────"}</text>
						<text fg={theme.yellow}>⏳ {runningOperation}...</text>
					</>
				)}
			</box>
		</box>
	)
}
