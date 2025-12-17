/**
 * App component - root component with Helix-style modal keybindings
 *
 * Migrated to use atomic Effect services via custom hooks.
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useEffect, useMemo } from "react"
import { killActivePopup } from "../core/EditorService"
import { ActionPalette } from "./ActionPalette"
import {
	claudeCreateSessionAtom,
	createTaskAtom,
	filteredTasksByColumnAtom,
	handleKeyAtom,
	hookReceiverStarterAtom,
	refreshBoardAtom,
	vcStatusAtom,
	viewModeAtom,
} from "./atoms"
import { Board } from "./Board"
import { ClaudeCreatePrompt } from "./ClaudeCreatePrompt"
import { CommandInput } from "./CommandInput"
import { ConfirmOverlay } from "./ConfirmOverlay"
import { CreateTaskPrompt } from "./CreateTaskPrompt"
import { DetailPanel } from "./DetailPanel"
import { DiagnosticsOverlay } from "./DiagnosticsOverlay"
import { HelpOverlay } from "./HelpOverlay"
import { useEditorMode, useNavigation, useOverlays, useToasts } from "./hooks"
import { ImageAttachOverlay } from "./ImageAttachOverlay"
import { SearchInput } from "./SearchInput"
import { SortMenu } from "./SortMenu"
import { StatusBar } from "./StatusBar"
import { TASK_CARD_HEIGHT } from "./TaskCard"
import { ToastContainer } from "./Toast"
import { theme } from "./theme"

// ============================================================================
// Constants
// ============================================================================

// UI chrome heights - these sum to CHROME_HEIGHT for maxVisibleTasks calculation
const STATUS_BAR_HEIGHT = 3 // border-top + content + border-bottom
const COLUMN_HEADER_HEIGHT = 1
const COLUMN_UNDERLINE_HEIGHT = 0 // underline now rendered as text attribute
const SCROLL_INDICATORS_HEIGHT = 2 // top "↑ N more" + bottom "↓ M more"
const TMUX_STATUS_BAR_HEIGHT = process.env.TMUX ? 1 : 0

const CHROME_HEIGHT =
	STATUS_BAR_HEIGHT +
	COLUMN_HEADER_HEIGHT +
	COLUMN_UNDERLINE_HEIGHT +
	SCROLL_INDICATORS_HEIGHT +
	TMUX_STATUS_BAR_HEIGHT

// ============================================================================
// App Component
// ============================================================================

export const App = () => {
	// ═══════════════════════════════════════════════════════════════════════════
	// Hooks - Atomic State Management
	// ═══════════════════════════════════════════════════════════════════════════

	const { toasts, dismissToast } = useToasts()
	const {
		dismiss: dismissOverlay,
		showingHelp,
		showingDetail,
		showingCreate,
		showingClaudeCreate,
		showingImageAttach,
		showingConfirm,
		showingDiagnostics,
	} = useOverlays()

	const {
		mode,
		selectedIds,
		searchQuery,
		commandInput,
		pendingJumpKey,
		jumpLabels,
		sortConfig,
		isJump,
		isAction,
		isSearch,
		isCommand,
		isSort,
	} = useEditorMode()

	// ═══════════════════════════════════════════════════════════════════════════
	// Data Atoms
	// ═══════════════════════════════════════════════════════════════════════════

	// Use BoardService as single source of truth for task data
	// BoardService handles all filtering and sorting - React just renders the result
	const filteredTasksResult = useAtomValue(filteredTasksByColumnAtom)
	const tasksByColumn = Result.isSuccess(filteredTasksResult) ? filteredTasksResult.value : []

	const vcStatusResult = useAtomValue(vcStatusAtom)
	const refreshBoard = useAtomSet(refreshBoardAtom, { mode: "promise" })

	// Start the hook receiver for Claude Code native hook integration
	// This watches for notification files and updates session state
	useAtomValue(hookReceiverStarterAtom)

	// Initialize BoardService data on mount
	// This is required for NavigationService to work (ID-based cursor needs task data)
	useEffect(() => {
		refreshBoard()
	}, [refreshBoard])

	// Actions for prompts (these bypass keyboard handling)
	// Full orchestration (dismiss, create, navigate, toast) happens in the atoms
	const createTask = useAtomSet(createTaskAtom, { mode: "promise" })
	const claudeCreateSession = useAtomSet(claudeCreateSessionAtom, { mode: "promise" })

	// Keyboard handling via KeyboardService
	const handleKey = useAtomSet(handleKeyAtom, { mode: "promise" })

	// View mode state via ViewService
	const viewModeResult = useAtomValue(viewModeAtom)
	const viewMode = Result.isSuccess(viewModeResult) ? viewModeResult.value : "kanban"

	// Terminal size
	const maxVisibleTasks = useMemo(() => {
		const rows = process.stdout.rows || 24
		return Math.max(1, Math.floor((rows - CHROME_HEIGHT) / TASK_CARD_HEIGHT))
	}, [])

	// Navigation hook (needs tasksByColumn)
	const { columnIndex, taskIndex, selectedTask } = useNavigation(tasksByColumn)

	// ═══════════════════════════════════════════════════════════════════════════
	// Keyboard Handler - Delegates to KeyboardService
	// ═══════════════════════════════════════════════════════════════════════════

	useKeyboard((event) => {
		// Ctrl-C: Kill active editor popup (MUST be first - works in any state)
		if (event.ctrl && event.name === "c") {
			killActivePopup()
			return
		}

		// Create prompt handling - CreateTaskPrompt handles its own keyboard input
		if (showingCreate) {
			return
		}

		// Claude create prompt handling - ClaudeCreatePrompt handles its own keyboard input
		if (showingClaudeCreate) {
			return
		}

		// Note: imageAttach overlay keyboard is handled by KeyboardService

		// Build key sequence with modifiers (e.g., "C-d" for Ctrl+d, "S-c" for Shift+c, "CS-u" for Ctrl+Shift+u)
		let keySeq = event.name
		if (event.ctrl && event.shift) {
			keySeq = `CS-${event.name}`
		} else if (event.ctrl) {
			keySeq = `C-${event.name}`
		} else if (event.shift) {
			keySeq = `S-${event.name}`
		}

		// Delegate all keyboard handling to KeyboardService
		// KeyboardService handles: navigation, mode transitions, actions, overlays, escape, view toggle
		handleKey(keySeq)
	})

	// ═══════════════════════════════════════════════════════════════════════════
	// Computed Values
	// ═══════════════════════════════════════════════════════════════════════════

	// Flatten tasksByColumn to get all tasks for computing totals
	const allTasks = useMemo(() => tasksByColumn.flat(), [tasksByColumn])
	const totalTasks = allTasks.length

	const activeSessions = useMemo(() => {
		return allTasks.filter((t) => t.sessionState === "busy" || t.sessionState === "waiting").length
	}, [allTasks])

	// Mode display text
	const modeDisplay = useMemo(() => {
		switch (mode._tag) {
			case "action":
				return "action"
			case "command":
				return "command"
			case "goto":
				if (mode.gotoSubMode === "pending") return "g..."
				if (mode.gotoSubMode === "jump")
					return mode.pendingJumpKey ? `g w ${mode.pendingJumpKey}_` : "g w ..."
				return "goto"
			case "normal":
				return searchQuery ? `filter: ${searchQuery}` : "normal"
			case "search":
				return "search"
			case "select":
				return `select (${selectedIds.length})`
			case "sort":
				return "sort"
		}
	}, [mode, searchQuery, selectedIds])

	// ═══════════════════════════════════════════════════════════════════════════
	// Render
	// ═══════════════════════════════════════════════════════════════════════════

	const renderContent = () => {
		if (Result.isInitial(filteredTasksResult)) {
			return (
				<box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
					<text fg={theme.sky}>Loading tasks...</text>
				</box>
			)
		}

		if (Result.isFailure(filteredTasksResult)) {
			return (
				<box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
					<text fg={theme.red}>Error loading tasks:</text>
					<text fg={theme.red}>{String(filteredTasksResult.cause)}</text>
				</box>
			)
		}

		return (
			<box flexGrow={1}>
				<Board
					tasks={tasksByColumn.flat()}
					selectedTaskId={selectedTask?.id}
					activeColumnIndex={columnIndex}
					activeTaskIndex={taskIndex}
					selectedIds={new Set(selectedIds)}
					jumpLabels={isJump ? jumpLabels : null}
					pendingJumpKey={pendingJumpKey ?? null}
					terminalHeight={maxVisibleTasks}
					viewMode={viewMode}
					isActionMode={isAction}
				/>
			</box>
		)
	}

	return (
		<box flexDirection="column" width="100%" height="100%" backgroundColor={theme.base}>
			{renderContent()}

			{/* Status bar at bottom */}
			<StatusBar
				totalTasks={totalTasks}
				activeSessions={activeSessions}
				mode={mode._tag}
				modeDisplay={modeDisplay}
				selectedCount={selectedIds.length}
				vcStatus={Result.isSuccess(vcStatusResult) ? vcStatusResult.value.status : undefined}
				viewMode={viewMode}
			/>

			{/* Help overlay */}
			{showingHelp && <HelpOverlay />}

			{/* Diagnostics overlay */}
			{showingDiagnostics && <DiagnosticsOverlay />}

			{/* Action palette */}
			{isAction && <ActionPalette task={selectedTask} />}

			{/* Sort menu */}
			{isSort && <SortMenu currentSort={sortConfig} />}

			{/* Search input */}
			{isSearch && <SearchInput query={searchQuery} />}

			{/* Command input */}
			{isCommand && <CommandInput input={commandInput} />}

			{/* Detail panel */}
			{showingDetail && selectedTask && <DetailPanel task={selectedTask} />}

			{/* Create task prompt */}
			{showingCreate && (
				<CreateTaskPrompt
					onSubmit={(params) => {
						createTask(params)
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Claude create prompt */}
			{showingClaudeCreate && (
				<ClaudeCreatePrompt
					onSubmit={(description) => {
						claudeCreateSession(description)
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Image attach overlay */}
			{showingImageAttach && <ImageAttachOverlay />}
			{/* Confirm overlay */}
			{showingConfirm && <ConfirmOverlay />}

			{/* Toast notifications */}
			<ToastContainer toasts={toasts} onDismiss={dismissToast} />
		</box>
	)
}
