/**
 * App component - root component with Helix-style modal keybindings
 *
 * Migrated to use atomic Effect services via custom hooks.
 */

import { Result } from "@effect-atom/atom"
import { useAtomSet, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useCallback, useEffect, useMemo } from "react"
import { killActivePopup } from "../core/EditorService"
import { ActionPalette } from "./ActionPalette"
import {
	boardTasksAtom,
	claudeCreateSessionAtom,
	createTaskAtom,
	handleKeyAtom,
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
import { HelpOverlay } from "./HelpOverlay"
import { useEditorMode, useNavigation, useOverlays, useToasts } from "./hooks"
import { SearchInput } from "./SearchInput"
import { SortMenu } from "./SortMenu"
import { StatusBar } from "./StatusBar"
import { TASK_CARD_HEIGHT } from "./TaskCard"
import { ToastContainer } from "./Toast"
import { theme } from "./theme"
import { COLUMNS, type TaskWithSession } from "./types"

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

// Helper function for session state sorting (defined outside component for stable reference)
const getSessionSortValue = (state: TaskWithSession["sessionState"]): number => {
	// Active sessions (busy, waiting) first, then paused, then done/error, then idle
	switch (state) {
		case "busy":
			return 0
		case "waiting":
			return 1
		case "paused":
			return 2
		case "done":
			return 3
		case "error":
			return 4
		case "idle":
			return 5
	}
}

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
		showingConfirm,
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
	// This ensures UI updates when KeyboardService calls board.refresh()
	const tasksResult = useAtomValue(boardTasksAtom)
	const vcStatusResult = useAtomValue(vcStatusAtom)
	const refreshBoard = useAtomSet(refreshBoardAtom, { mode: "promise" })

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

	// ═══════════════════════════════════════════════════════════════════════════
	// Task Grouping and Filtering
	// ═══════════════════════════════════════════════════════════════════════════

	const sortTasks = useCallback(
		(tasks: TaskWithSession[]): TaskWithSession[] => {
			return [...tasks].sort((a, b) => {
				const direction = sortConfig.direction === "desc" ? -1 : 1

				switch (sortConfig.field) {
					case "session": {
						// Sort by session status (active first when desc)
						const sessionDiff =
							getSessionSortValue(a.sessionState) - getSessionSortValue(b.sessionState)
						if (sessionDiff !== 0) return sessionDiff * direction
						// Then by priority (lower number = higher priority)
						const priorityDiff = a.priority - b.priority
						if (priorityDiff !== 0) return priorityDiff
						// Then by updated_at (more recent first)
						return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
					}
					case "priority": {
						// Sort by priority (lower number = higher priority, so desc shows P1 first)
						const priorityDiff = a.priority - b.priority
						if (priorityDiff !== 0) return priorityDiff * direction
						// Then by session status
						const sessionDiff =
							getSessionSortValue(a.sessionState) - getSessionSortValue(b.sessionState)
						if (sessionDiff !== 0) return sessionDiff
						// Then by updated_at
						return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
					}
					case "updated": {
						// Sort by updated_at (desc = most recent first)
						const dateDiff = new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
						if (dateDiff !== 0) return dateDiff * direction
						// Then by session status
						const sessionDiff =
							getSessionSortValue(a.sessionState) - getSessionSortValue(b.sessionState)
						if (sessionDiff !== 0) return sessionDiff
						// Then by priority
						return a.priority - b.priority
					}
					default:
						return 0
				}
			})
		},
		[sortConfig],
	)

	// Group tasks by column for navigation, filtering by search query, then sorting
	const tasksByColumn = useMemo(() => {
		if (!Result.isSuccess(tasksResult)) return []

		const query = searchQuery.toLowerCase().trim()

		return COLUMNS.map((col) => {
			const filtered = tasksResult.value.filter((task) => {
				// First filter by status
				if (task.status !== col.status) return false
				// Then filter by search query if present
				if (query) {
					const titleMatch = task.title.toLowerCase().includes(query)
					const idMatch = task.id.toLowerCase().includes(query)
					return titleMatch || idMatch
				}
				return true
			})
			// Apply sorting
			return sortTasks(filtered)
		})
	}, [tasksResult, searchQuery, sortTasks])

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

		// Build key sequence with modifiers (e.g., "C-d" for Ctrl+d, "S-c" for Shift+c)
		let keySeq = event.name
		if (event.ctrl) {
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

	const totalTasks = Result.isSuccess(tasksResult) ? tasksResult.value.length : 0

	const activeSessions = useMemo(() => {
		if (!Result.isSuccess(tasksResult)) return 0
		return tasksResult.value.filter(
			(t) => t.sessionState === "busy" || t.sessionState === "waiting",
		).length
	}, [tasksResult])

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
		if (Result.isInitial(tasksResult)) {
			return (
				<box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
					<text fg={theme.sky}>Loading tasks...</text>
				</box>
			)
		}

		if (Result.isFailure(tasksResult)) {
			return (
				<box flexDirection="column" alignItems="center" justifyContent="center" flexGrow={1}>
					<text fg={theme.red}>Error loading tasks:</text>
					<text fg={theme.red}>{String(tasksResult.cause)}</text>
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

			{/* Confirm overlay */}
			{showingConfirm && <ConfirmOverlay />}

			{/* Toast notifications */}
			<ToastContainer toasts={toasts} onDismiss={dismissToast} />
		</box>
	)
}
