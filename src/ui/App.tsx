/**
 * App component - root component with Helix-style modal keybindings
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomRefresh, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react"
import { ActionPalette } from "./ActionPalette"
import {
	attachExternalAtom,
	attachInlineAtom,
	cleanupAtom,
	createPRAtom,
	createTaskAtom,
	editBeadAtom,
	moveTaskAtom,
	moveTasksAtom,
	pauseSessionAtom,
	resumeSessionAtom,
	sendVCCommandAtom,
	startSessionAtom,
	stopSessionAtom,
	tasksAtom,
	toggleVCAutoPilotAtom,
	vcStatusAtom,
} from "./atoms"
import { Board } from "./Board"
import { CommandInput } from "./CommandInput"
import { CreateTaskPrompt } from "./CreateTaskPrompt"
import { DetailPanel } from "./DetailPanel"
import { type EditorAction, type EditorState, editorReducer, initialEditorState } from "./editorFSM"
import { HelpOverlay } from "./HelpOverlay"
import { SearchInput } from "./SearchInput"
import { StatusBar } from "./StatusBar"
import { TASK_CARD_HEIGHT } from "./TaskCard"
import { generateToastId, ToastContainer, type ToastMessage } from "./Toast"
import { theme } from "./theme"
import {
	COLUMNS,
	generateJumpLabels,
	type JumpTarget,
	type NavigationState,
	type TaskWithSession,
} from "./types"

/**
 * App component
 *
 * Root component implementing Helix-style modal editing using FSM.
 */
// UI chrome height: board padding (2) + status bar (3) + column header (1) = 6
// Add 1 more row when running inside tmux to account for tmux status bar
const TMUX_STATUS_BAR_HEIGHT = process.env.TMUX ? 1 : 0
const CHROME_HEIGHT = 6 + TMUX_STATUS_BAR_HEIGHT

export const App = () => {
	// ═══════════════════════════════════════════════════════════════════════════
	// FSM State - Single source of truth for editor mode
	// ═══════════════════════════════════════════════════════════════════════════
	const [editorState, baseDispatch] = useReducer(editorReducer, initialEditorState)

	// Ref to current state so keyboard handler always has latest
	const stateRef = useRef<EditorState>(editorState)
	stateRef.current = editorState // Sync on every render

	// Dispatch wrapper that updates ref IMMEDIATELY before React re-renders
	const dispatch = useCallback((action: EditorAction) => {
		// Compute new state immediately
		const newState = editorReducer(stateRef.current, action)
		stateRef.current = newState // Update ref NOW (sync)
		baseDispatch(action) // Trigger React re-render (async)
		console.error(`[FSM] ${action.type} → mode=${newState.mode}`)
	}, [])

	// ═══════════════════════════════════════════════════════════════════════════
	// Data Atoms
	// ═══════════════════════════════════════════════════════════════════════════
	const tasksResult = useAtomValue(tasksAtom)
	const refreshTasks = useAtomRefresh(tasksAtom)
	const vcStatusResult = useAtomValue(vcStatusAtom)
	const refreshVCStatus = useAtomRefresh(vcStatusAtom)
	const [, moveTask] = useAtom(moveTaskAtom, { mode: "promise" })
	const [, moveTasks] = useAtom(moveTasksAtom, { mode: "promise" })
	const [, attachExternal] = useAtom(attachExternalAtom, { mode: "promise" })
	const [, attachInline] = useAtom(attachInlineAtom, { mode: "promise" })
	const [, startSession] = useAtom(startSessionAtom, { mode: "promise" })
	const [, pauseSession] = useAtom(pauseSessionAtom, { mode: "promise" })
	const [, resumeSession] = useAtom(resumeSessionAtom, { mode: "promise" })
	const [, stopSession] = useAtom(stopSessionAtom, { mode: "promise" })
	const [, createTask] = useAtom(createTaskAtom, { mode: "promise" })
	const [, editBead] = useAtom(editBeadAtom, { mode: "promise" })
	const [, createPR] = useAtom(createPRAtom, { mode: "promise" })
	const [, cleanup] = useAtom(cleanupAtom, { mode: "promise" })
	const [, toggleVCAutoPilot] = useAtom(toggleVCAutoPilotAtom, { mode: "promise" })
	const [, sendVCCommand] = useAtom(sendVCCommandAtom, { mode: "promise" })

	// ═══════════════════════════════════════════════════════════════════════════
	// Navigation State (separate from FSM)
	// ═══════════════════════════════════════════════════════════════════════════
	const [nav, setNav] = useState<NavigationState>({ columnIndex: 0, taskIndex: 0 })
	const navRef = useRef(nav)
	navRef.current = nav

	// UI overlay state
	const [showHelp, setShowHelp] = useState(false)
	const [showDetail, setShowDetail] = useState(false)
	const [showCreatePrompt, setShowCreatePrompt] = useState(false)
	const showHelpRef = useRef(showHelp)
	const showDetailRef = useRef(showDetail)
	const showCreatePromptRef = useRef(showCreatePrompt)
	showHelpRef.current = showHelp
	showDetailRef.current = showDetail
	showCreatePromptRef.current = showCreatePrompt

	// Terminal size
	const maxVisibleTasks = useMemo(() => {
		const rows = process.stdout.rows || 24
		return Math.max(1, Math.floor((rows - CHROME_HEIGHT) / TASK_CARD_HEIGHT))
	}, [])

	// Track task ID to follow after move operations
	// This ensures we navigate to the task's new position after data refreshes
	const [followTaskId, setFollowTaskId] = useState<string | null>(null)

	// Toast notifications state
	const [toasts, setToasts] = useState<ToastMessage[]>([])

	// Helper to show an error toast
	const showError = useCallback((message: string) => {
		setToasts((prev) => [
			...prev,
			{ id: generateToastId(), message, type: "error", timestamp: Date.now() },
		])
	}, [])

	// Helper to show a success toast
	const showSuccess = useCallback((message: string) => {
		setToasts((prev) => [
			...prev,
			{ id: generateToastId(), message, type: "success", timestamp: Date.now() },
		])
	}, [])

	// Helper to show an info toast
	const showInfo = useCallback((message: string) => {
		setToasts((prev) => [
			...prev,
			{ id: generateToastId(), message, type: "info", timestamp: Date.now() },
		])
	}, [])

	// Dismiss a toast by ID
	const dismissToast = useCallback((id: string) => {
		setToasts((prev) => prev.filter((t) => t.id !== id))
	}, [])

	// Group tasks by column for navigation, filtering by search query
	const tasksByColumn = useMemo(() => {
		if (!Result.isSuccess(tasksResult)) return []

		const { searchQuery } = editorState
		const query = searchQuery.toLowerCase().trim()

		return COLUMNS.map((col) =>
			tasksResult.value.filter((task) => {
				// First filter by status
				if (task.status !== col.status) return false
				// Then filter by search query if present
				if (query) {
					const titleMatch = task.title.toLowerCase().includes(query)
					const idMatch = task.id.toLowerCase().includes(query)
					return titleMatch || idMatch
				}
				return true
			}),
		)
	}, [tasksResult, editorState.searchQuery])

	// Get all tasks as flat list for jump label generation
	const allTasks = useMemo(() => {
		const tasks: Array<{ task: TaskWithSession; columnIndex: number; taskIndex: number }> = []

		tasksByColumn.forEach((column, columnIndex) => {
			column.forEach((task, taskIndex) => {
				tasks.push({ task, columnIndex, taskIndex })
			})
		})

		return tasks
	}, [tasksByColumn])

	// Generate jump labels when entering goto mode
	const computeJumpLabels = useCallback(() => {
		const labels = generateJumpLabels(allTasks.length)
		const labelMap = new Map<string, JumpTarget>()

		allTasks.forEach(({ task, columnIndex, taskIndex }, i) => {
			if (labels[i]) {
				labelMap.set(labels[i], { taskId: task.id, columnIndex, taskIndex })
			}
		})

		return labelMap
	}, [allTasks])

	// Get currently selected task
	const selectedTask = useMemo((): TaskWithSession | undefined => {
		const { columnIndex, taskIndex } = nav
		const column = tasksByColumn[columnIndex]
		return column?.[taskIndex]
	}, [tasksByColumn, nav])

	// Helper to clamp task index to column bounds
	const clampTaskIndex = useCallback(
		(colIdx: number, preferredIndex: number): number => {
			const column = tasksByColumn[colIdx]
			if (!column || column.length === 0) return 0
			return Math.min(preferredIndex, column.length - 1)
		},
		[tasksByColumn],
	)

	// Navigate to a specific position
	const navigateTo = useCallback(
		(colIdx: number, taskIdx: number) => {
			const clampedCol = Math.max(0, Math.min(colIdx, COLUMNS.length - 1))
			const clampedTask = clampTaskIndex(clampedCol, taskIdx)
			navRef.current = { columnIndex: clampedCol, taskIndex: clampedTask }
			setNav({ columnIndex: clampedCol, taskIndex: clampedTask })
		},
		[clampTaskIndex],
	)

	// Effect to follow a task after move operations
	// When followTaskId is set, find the task in the updated data and navigate to it
	useEffect(() => {
		if (!followTaskId) return

		// Search all columns for the task
		for (let colIdx = 0; colIdx < tasksByColumn.length; colIdx++) {
			const taskIdx = tasksByColumn[colIdx].findIndex((t) => t.id === followTaskId)
			if (taskIdx >= 0) {
				navigateTo(colIdx, taskIdx)
				setFollowTaskId(null)
				return
			}
		}
		// Task not found (maybe deleted), clear the follow state
		setFollowTaskId(null)
	}, [followTaskId, tasksByColumn, navigateTo])

	// Poll VC status every 5 seconds
	useEffect(() => {
		const interval = setInterval(() => {
			refreshVCStatus()
		}, 5000)
		return () => clearInterval(interval)
	}, [refreshVCStatus])

	// ═══════════════════════════════════════════════════════════════════════════
	// Jump input handler (for goto → jump mode)
	// ═══════════════════════════════════════════════════════════════════════════
	const handleJumpInput = useCallback(
		(key: string) => {
			const state = stateRef.current
			const { pendingJumpKey, jumpLabels } = state

			if (!jumpLabels) {
				dispatch({ type: "EXIT_TO_NORMAL" })
				return
			}

			if (pendingJumpKey) {
				// Second key - complete the jump
				const label = pendingJumpKey + key
				const target = jumpLabels.get(label)
				if (target) {
					navigateTo(target.columnIndex, target.taskIndex)
				}
				dispatch({ type: "EXIT_TO_NORMAL" })
			} else {
				// First key - check if any labels start with this key
				const hasMatch = [...jumpLabels.keys()].some((l) => l.startsWith(key))
				if (hasMatch) {
					dispatch({ type: "SET_PENDING_JUMP_KEY", key })
				} else {
					dispatch({ type: "EXIT_TO_NORMAL" })
				}
			}
		},
		[navigateTo, dispatch],
	)

	// Keyboard navigation with Helix-style modal bindings
	// Uses stateRef.current for reading (always fresh) and dispatch() for writing
	useKeyboard((event) => {
		// Read current state from ref (bypasses React closure issues)
		const state = stateRef.current
		const { mode, gotoSubMode, selectedIds } = state
		const { columnIndex, taskIndex } = navRef.current

		// DEBUG: Log every keypress and current mode
		console.error(`[KEY] "${event.name}" | mode=${mode}`)

		// Help overlay handling - dismiss on any key
		if (showHelpRef.current) {
			setShowHelp(false)
			return
		}

		// Detail panel handling - dismiss on Enter or Escape
		if (showDetailRef.current) {
			if (event.name === "return" || event.name === "escape") {
				setShowDetail(false)
			}
			return
		}

		// Create prompt handling - CreateTaskPrompt handles its own keyboard input
		if (showCreatePromptRef.current) {
			return
		}

		// Escape always returns to normal mode
		if (event.name === "escape") {
			if (mode === "search") {
				// Clear search when exiting search mode
				dispatch({ type: "CLEAR_SEARCH" })
			} else if (mode === "command") {
				// Clear command input when exiting command mode
				dispatch({ type: "CLEAR_COMMAND" })
			} else if (mode === "select") {
				// Clear selections when exiting select mode
				dispatch({ type: "EXIT_SELECT", clearSelections: true })
			} else {
				dispatch({ type: "EXIT_TO_NORMAL" })
			}
			return
		}

		// Handle action mode (Space menu)
		// Stay in action mode after moves so user can continue moving h/l
		// Press Escape to exit action mode
		if (mode === "action") {
			switch (event.name) {
				case "left":
				case "h": {
					// Move selected tasks (or current task) to previous column
					if (columnIndex > 0) {
						const targetStatus = COLUMNS[columnIndex - 1]?.status
						if (targetStatus) {
							if (selectedIds.size > 0) {
								// For multi-select, follow the first task in selection
								const firstTaskId = [...selectedIds][0]
								moveTasks({ taskIds: [...selectedIds], newStatus: targetStatus })
									.then(() => {
										setFollowTaskId(firstTaskId ?? null)
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							} else if (selectedTask) {
								const taskToFollow = selectedTask.id
								moveTask({ taskId: selectedTask.id, newStatus: targetStatus })
									.then(() => {
										setFollowTaskId(taskToFollow)
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							}
						}
					}
					// Stay in action mode
					break
				}
				case "right":
				case "l": {
					// Move selected tasks (or current task) to next column
					if (columnIndex < COLUMNS.length - 1) {
						const targetStatus = COLUMNS[columnIndex + 1]?.status
						if (targetStatus) {
							if (selectedIds.size > 0) {
								// For multi-select, follow the first task in selection
								const firstTaskId = [...selectedIds][0]
								moveTasks({ taskIds: [...selectedIds], newStatus: targetStatus })
									.then(() => {
										setFollowTaskId(firstTaskId ?? null)
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							} else if (selectedTask) {
								const taskToFollow = selectedTask.id
								moveTask({ taskId: selectedTask.id, newStatus: targetStatus })
									.then(() => {
										setFollowTaskId(taskToFollow)
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							}
						}
					}
					// Stay in action mode
					break
				}
				case "s": {
					// Start a new session (only for idle tasks)
					if (selectedTask) {
						if (selectedTask.sessionState !== "idle") {
							showError(`Cannot start: task is ${selectedTask.sessionState}`)
						} else {
							startSession(selectedTask.id)
								.then(() => {
									refreshTasks()
									showSuccess(`Started session for ${selectedTask.id}`)
								})
								.catch((error) => {
									showError(`Failed to start: ${error}`)
								})
						}
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "a": {
					// Attach to session - switches tmux client to Claude session
					// Claude sessions use Ctrl-a prefix, so Ctrl-a ) switches back
					if (selectedTask) {
						attachExternal(selectedTask.id)
							.then(() => {
								// Switched! Show reminder about how to get back
								showInfo(`Switched! Ctrl-a ) to return`)
							})
							.catch((error: any) => {
								const msg =
									error && typeof error === "object" && error._tag === "SessionNotFoundError"
										? `No session for ${selectedTask.id} - press Space+s to start`
										: `Failed to attach: ${error}`
								showError(msg)
							})
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "A": {
					// Attach to session inline (replace TUI)
					// Don't pre-check sessionState - it may be stale. Let AttachmentService check tmux directly.
					if (selectedTask) {
						attachInline(selectedTask.id).catch((error: any) => {
							const msg =
								error && typeof error === "object" && error._tag === "SessionNotFoundError"
									? `No session for ${selectedTask.id} - press Space+s to start`
									: `Failed to attach: ${error}`
							showError(msg)
						})
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "p": {
					// Pause a running session (only for busy tasks)
					if (selectedTask) {
						if (selectedTask.sessionState !== "busy") {
							showError(`Cannot pause: task is ${selectedTask.sessionState}`)
						} else {
							pauseSession(selectedTask.id)
								.then(() => {
									refreshTasks()
									showSuccess(`Paused session for ${selectedTask.id}`)
								})
								.catch((error) => {
									showError(`Failed to pause: ${error}`)
								})
						}
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "r": {
					// Resume a paused session (only for paused tasks)
					if (selectedTask) {
						if (selectedTask.sessionState !== "paused") {
							showError(`Cannot resume: task is ${selectedTask.sessionState}`)
						} else {
							resumeSession(selectedTask.id)
								.then(() => {
									refreshTasks()
									showSuccess(`Resumed session for ${selectedTask.id}`)
								})
								.catch((error) => {
									showError(`Failed to resume: ${error}`)
								})
						}
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "x": {
					// Stop/kill a running session
					if (selectedTask) {
						if (selectedTask.sessionState === "idle") {
							showError(`No session to stop`)
						} else {
							stopSession(selectedTask.id)
								.then(() => {
									refreshTasks()
									showSuccess(`Stopped session for ${selectedTask.id}`)
								})
								.catch((error) => {
									showError(`Failed to stop: ${error}`)
								})
						}
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "e": {
					// Edit bead in $EDITOR
					if (selectedTask) {
						editBead(selectedTask)
							.then(() => {
								refreshTasks()
								showSuccess(`Updated ${selectedTask.id}`)
							})
							.catch((error: any) => {
								const msg =
									error && typeof error === "object" && error._tag === "ParseMarkdownError"
										? `Invalid format: ${error.message}`
										: error && typeof error === "object" && error._tag === "EditorError"
											? `Editor error: ${error.message}`
											: `Failed to edit: ${error}`
								showError(msg)
							})
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "P": {
					// Create PR (push branch + gh pr create)
					if (selectedTask) {
						if (selectedTask.sessionState === "idle") {
							showError(`No worktree for ${selectedTask.id} - start a session first`)
						} else {
							showInfo(`Creating PR for ${selectedTask.id}...`)
							createPR(selectedTask.id)
								.then((pr: any) => {
									showSuccess(`PR created: ${pr.url}`)
								})
								.catch((error: any) => {
									const msg =
										error && typeof error === "object" && error._tag === "GHCLIError"
											? error.message
											: `Failed to create PR: ${error}`
									showError(msg)
								})
						}
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				case "d": {
					// Cleanup/delete worktree and branches
					if (selectedTask) {
						if (selectedTask.sessionState === "idle") {
							showError(`No worktree to delete for ${selectedTask.id}`)
						} else {
							// TODO: Add confirmation dialog for destructive action
							showInfo(`Cleaning up ${selectedTask.id}...`)
							cleanup(selectedTask.id)
								.then(() => {
									refreshTasks()
									showSuccess(`Cleaned up ${selectedTask.id}`)
								})
								.catch((error) => {
									showError(`Failed to cleanup: ${error}`)
								})
						}
					}
					dispatch({ type: "EXIT_TO_NORMAL" })
					break
				}
				default:
					// Unknown key in action mode - ignore (don't exit)
					break
			}
			return
		}

		// Handle goto mode
		if (mode === "goto") {
			if (gotoSubMode === "pending") {
				// Waiting for second key after 'g'
				switch (event.name) {
					case "w": {
						// Enter jump mode with labels
						const labels = computeJumpLabels()
						dispatch({ type: "ENTER_JUMP", labels })
						break
					}
					case "g": {
						// Go to first task in first column
						navigateTo(0, 0)
						dispatch({ type: "EXIT_TO_NORMAL" })
						break
					}
					case "e": {
						// Go to last task in last column
						const lastColIdx = COLUMNS.length - 1
						const lastCol = tasksByColumn[lastColIdx]
						navigateTo(lastColIdx, lastCol ? lastCol.length - 1 : 0)
						dispatch({ type: "EXIT_TO_NORMAL" })
						break
					}
					case "h": {
						// Go to first column, keep task index
						navigateTo(0, taskIndex)
						dispatch({ type: "EXIT_TO_NORMAL" })
						break
					}
					case "l": {
						// Go to last column, keep task index
						navigateTo(COLUMNS.length - 1, taskIndex)
						dispatch({ type: "EXIT_TO_NORMAL" })
						break
					}
					default:
						dispatch({ type: "EXIT_TO_NORMAL" })
				}
				return
			}

			if (gotoSubMode === "jump") {
				// In jump mode - handle label input
				handleJumpInput(event.name)
				return
			}
			return
		}

		// Handle search mode
		if (mode === "search") {
			const { searchQuery } = state

			if (event.name === "return") {
				// Confirm search - stay in normal mode with active filter
				dispatch({ type: "EXIT_TO_NORMAL" })
				return
			}

			if (event.name === "backspace") {
				// Delete character from search query
				if (searchQuery.length > 0) {
					dispatch({ type: "UPDATE_SEARCH_QUERY", query: searchQuery.slice(0, -1) })
				}
				return
			}

			// Regular character input
			if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				dispatch({ type: "UPDATE_SEARCH_QUERY", query: searchQuery + event.sequence })
				return
			}

			return
		}

		// Handle command mode
		if (mode === "command") {
			const { commandInput } = state
			const vcStatus = Result.isSuccess(vcStatusResult) ? vcStatusResult.value.status : undefined

			if (event.name === "return") {
				// Send command to VC
				if (!commandInput.trim()) {
					// Empty command, just exit
					dispatch({ type: "CLEAR_COMMAND" })
					return
				}

				if (vcStatus !== "running") {
					showError("VC is not running - start it with 'a' key")
					dispatch({ type: "CLEAR_COMMAND" })
					return
				}

				// Send the command
				sendVCCommand(commandInput)
					.then(() => {
						showSuccess(`Sent to VC: ${commandInput}`)
						dispatch({ type: "CLEAR_COMMAND" })
					})
					.catch((error: any) => {
						const msg =
							error && typeof error === "object" && error._tag === "VCNotRunningError"
								? "VC is not running"
								: `Failed to send command: ${error}`
						showError(msg)
						dispatch({ type: "CLEAR_COMMAND" })
					})
				return
			}

			if (event.name === "backspace") {
				// Delete character from command input
				if (commandInput.length > 0) {
					dispatch({ type: "UPDATE_COMMAND_INPUT", input: commandInput.slice(0, -1) })
				}
				return
			}

			// Regular character input
			if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				dispatch({ type: "UPDATE_COMMAND_INPUT", input: commandInput + event.sequence })
				return
			}

			return
		}

		// Handle select mode
		if (mode === "select") {
			switch (event.name) {
				case "up":
				case "k": {
					const column = tasksByColumn[columnIndex]
					if (column && taskIndex > 0) {
						navRef.current = { columnIndex, taskIndex: taskIndex - 1 }
						setNav({ columnIndex, taskIndex: taskIndex - 1 })
					}
					break
				}
				case "down":
				case "j": {
					const column = tasksByColumn[columnIndex]
					if (column && taskIndex < column.length - 1) {
						navRef.current = { columnIndex, taskIndex: taskIndex + 1 }
						setNav({ columnIndex, taskIndex: taskIndex + 1 })
					}
					break
				}
				case "left":
				case "h": {
					if (columnIndex > 0) {
						navigateTo(columnIndex - 1, taskIndex)
					}
					break
				}
				case "right":
				case "l": {
					if (columnIndex < COLUMNS.length - 1) {
						navigateTo(columnIndex + 1, taskIndex)
					}
					break
				}
				case "space": {
					// Toggle selection of current task
					if (selectedTask) {
						dispatch({ type: "TOGGLE_SELECTION", taskId: selectedTask.id })
					}
					break
				}
				case "v": {
					// Exit select mode (back to normal)
					dispatch({ type: "EXIT_SELECT" })
					break
				}
			}
			return
		}

		// Normal mode
		switch (event.name) {
			case "up":
			case "k": {
				// Move up in current column
				const column = tasksByColumn[columnIndex]
				if (column && taskIndex > 0) {
					navRef.current = { columnIndex, taskIndex: taskIndex - 1 }
					setNav({ columnIndex, taskIndex: taskIndex - 1 })
				}
				break
			}
			case "down":
			case "j": {
				// Move down in current column
				const column = tasksByColumn[columnIndex]
				if (column && taskIndex < column.length - 1) {
					navRef.current = { columnIndex, taskIndex: taskIndex + 1 }
					setNav({ columnIndex, taskIndex: taskIndex + 1 })
				}
				break
			}
			case "left":
			case "h": {
				// Move to previous column
				if (columnIndex > 0) {
					navigateTo(columnIndex - 1, taskIndex)
				}
				break
			}
			case "right":
			case "l": {
				// Move to next column
				if (columnIndex < COLUMNS.length - 1) {
					navigateTo(columnIndex + 1, taskIndex)
				}
				break
			}
			case "g": {
				// Enter goto mode (waiting for next key)
				dispatch({ type: "ENTER_GOTO" })
				break
			}
			case "v": {
				// Enter select mode
				dispatch({ type: "ENTER_SELECT" })
				break
			}
			case "space": {
				// Enter action mode (command palette)
				dispatch({ type: "ENTER_ACTION" })
				break
			}
			case "q": {
				// Quit
				process.exit(0)
				break
			}
			case "?": {
				// Toggle help overlay
				setShowHelp(true)
				break
			}
			case "return": {
				// Show detail panel for selected task
				if (selectedTask) {
					setShowDetail(true)
				}
				break
			}
			case "c": {
				// Create new task
				setShowCreatePrompt(true)
				break
			}
			case "a": {
				// Toggle VC auto-pilot mode
				toggleVCAutoPilot()
					.then((status) => {
						// Refresh VC status to update StatusBar
						refreshVCStatus()
						const message =
							status.status === "running" ? "VC auto-pilot started" : "VC auto-pilot stopped"
						showSuccess(message)
					})
					.catch((error) => {
						showError(`Failed to toggle VC auto-pilot: ${error}`)
					})
				break
			}
		}

		// "/" to enter search mode
		if (event.sequence === "/") {
			dispatch({ type: "ENTER_SEARCH" })
			return
		}

		// ":" to enter command mode
		if (event.sequence === ":") {
			dispatch({ type: "ENTER_COMMAND" })
			return
		}

		// Ctrl-d: half page down
		if (event.ctrl && event.name === "d") {
			const column = tasksByColumn[columnIndex]
			if (column) {
				const halfPage = Math.floor(column.length / 2)
				const newIndex = Math.min(taskIndex + halfPage, column.length - 1)
				navRef.current = { columnIndex, taskIndex: newIndex }
				setNav({ columnIndex, taskIndex: newIndex })
			}
		}

		// Ctrl-u: half page up
		if (event.ctrl && event.name === "u") {
			const column = tasksByColumn[columnIndex]
			if (column) {
				const halfPage = Math.floor(column.length / 2)
				const newIndex = Math.max(taskIndex - halfPage, 0)
				navRef.current = { columnIndex, taskIndex: newIndex }
				setNav({ columnIndex, taskIndex: newIndex })
			}
		}
	})

	// Computed values (only when we have success)
	const totalTasks = Result.isSuccess(tasksResult) ? tasksResult.value.length : 0

	const activeSessions = useMemo(() => {
		if (!Result.isSuccess(tasksResult)) return 0
		return tasksResult.value.filter(
			(t) => t.sessionState === "busy" || t.sessionState === "waiting",
		).length
	}, [tasksResult])

	// Mode display text
	const modeDisplay = useMemo(() => {
		const { mode, gotoSubMode, pendingJumpKey, selectedIds, searchQuery } = editorState
		switch (mode) {
			case "action":
				return "action"
			case "command":
				return "command"
			case "goto":
				if (gotoSubMode === "pending") return "g..."
				if (gotoSubMode === "jump") return pendingJumpKey ? `g w ${pendingJumpKey}_` : "g w ..."
				return "goto"
			case "normal":
				// Show active filter in normal mode
				return searchQuery ? `filter: ${searchQuery}` : "normal"
			case "search":
				return "search"
			case "select":
				return `select (${selectedIds.size})`
		}
	}, [editorState])

	// Render based on Result state
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
					tasks={tasksResult.value}
					selectedTaskId={selectedTask?.id}
					activeColumnIndex={nav.columnIndex}
					activeTaskIndex={nav.taskIndex}
					selectedIds={editorState.selectedIds}
					jumpLabels={
						editorState.mode === "goto" && editorState.gotoSubMode === "jump"
							? editorState.jumpLabels
							: null
					}
					pendingJumpKey={editorState.pendingJumpKey}
					terminalHeight={maxVisibleTasks}
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
				mode={editorState.mode}
				modeDisplay={modeDisplay}
				selectedCount={editorState.selectedIds.size}
				vcStatus={Result.isSuccess(vcStatusResult) ? vcStatusResult.value.status : undefined}
			/>

			{/* Help overlay */}
			{showHelp && <HelpOverlay />}

			{/* Action palette */}
			{editorState.mode === "action" && <ActionPalette task={selectedTask} />}

			{/* Search input */}
			{editorState.mode === "search" && <SearchInput query={editorState.searchQuery} />}

			{/* Command input */}
			{editorState.mode === "command" && <CommandInput input={editorState.commandInput} />}

			{/* Detail panel */}
			{showDetail && selectedTask && <DetailPanel task={selectedTask} />}

			{/* Create task prompt */}
			{showCreatePrompt && (
				<CreateTaskPrompt
					onSubmit={(params) => {
						createTask(params)
							.then((issue: any) => {
								setShowCreatePrompt(false)
								refreshTasks()
								showSuccess(`Created task: ${issue.id}`)
							})
							.catch((error) => {
								setShowCreatePrompt(false)
								showError(`Failed to create task: ${error}`)
							})
					}}
					onCancel={() => setShowCreatePrompt(false)}
				/>
			)}

			{/* Toast notifications */}
			<ToastContainer toasts={toasts} onDismiss={dismissToast} />
		</box>
	)
}
