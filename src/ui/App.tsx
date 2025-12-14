/**
 * App component - root component with Helix-style modal keybindings
 *
 * Migrated to use atomic Effect services via custom hooks.
 */

import { Result } from "@effect-atom/atom"
import { useAtom, useAtomRefresh, useAtomValue } from "@effect-atom/atom-react"
import { useKeyboard } from "@opentui/react"
import { useCallback, useEffect, useMemo } from "react"
import { killActivePopup } from "../core/EditorService"
import { ActionPalette } from "./ActionPalette"
import {
	attachExternalAtom,
	attachInlineAtom,
	claudeCreateSessionAtom,
	cleanupAtom,
	createBeadViaEditorAtom,
	createPRAtom,
	createTaskAtom,
	deleteBeadAtom,
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
	viewModeAtom,
} from "./atoms"
import { Board } from "./Board"
import { ClaudeCreatePrompt } from "./ClaudeCreatePrompt"
import { CommandInput } from "./CommandInput"
import { CreateTaskPrompt } from "./CreateTaskPrompt"
import { DetailPanel } from "./DetailPanel"
import { HelpOverlay } from "./HelpOverlay"
import { useEditorMode, useNavigation, useOverlays, useToasts } from "./hooks"
import { SearchInput } from "./SearchInput"
import { StatusBar } from "./StatusBar"
import { TASK_CARD_HEIGHT } from "./TaskCard"
import { ToastContainer } from "./Toast"
import { theme } from "./theme"
import { COLUMNS, generateJumpLabels, type JumpTarget, type TaskWithSession, type ViewMode } from "./types"

// ============================================================================
// Constants
// ============================================================================

// UI chrome height: board padding (2) + status bar (3) + column header (1) = 6
// Add 1 more row when running inside tmux to account for tmux status bar
const TMUX_STATUS_BAR_HEIGHT = process.env.TMUX ? 1 : 0
const CHROME_HEIGHT = 6 + TMUX_STATUS_BAR_HEIGHT

// ============================================================================
// App Component
// ============================================================================

export const App = () => {
	// ═══════════════════════════════════════════════════════════════════════════
	// Hooks - Atomic State Management
	// ═══════════════════════════════════════════════════════════════════════════

	const { toasts, showError, showSuccess, showInfo, dismissToast } = useToasts()
	const {
		currentOverlay,
		showHelp,
		showDetail,
		showCreate,
		showClaudeCreate,
		dismiss: dismissOverlay,
		showingHelp,
		showingDetail,
		showingCreate,
		showingClaudeCreate,
	} = useOverlays()

	const {
		mode,
		selectedIds,
		searchQuery,
		commandInput,
		pendingJumpKey,
		jumpLabels,
		isNormal,
		isSelect,
		isGoto,
		isGotoPending,
		isJump,
		isAction,
		isSearch,
		isCommand,
		enterSelect,
		exitSelect,
		toggleSelection,
		enterGoto,
		enterJump,
		setPendingJumpKey,
		enterAction,
		enterSearch,
		updateSearch,
		clearSearch,
		enterCommand,
		updateCommand,
		clearCommand,
		exitToNormal,
	} = useEditorMode()

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
	const [, createBeadViaEditor] = useAtom(createBeadViaEditorAtom, { mode: "promise" })
	const [, claudeCreateSession] = useAtom(claudeCreateSessionAtom, { mode: "promise" })
	const [, createPR] = useAtom(createPRAtom, { mode: "promise" })
	const [, cleanup] = useAtom(cleanupAtom, { mode: "promise" })
	const [, deleteBead] = useAtom(deleteBeadAtom, { mode: "promise" })
	const [, toggleVCAutoPilot] = useAtom(toggleVCAutoPilotAtom, { mode: "promise" })
	const [, sendVCCommand] = useAtom(sendVCCommandAtom, { mode: "promise" })

	// View mode state
	const [viewMode, setViewMode] = useAtom(viewModeAtom)

	// Terminal size
	const maxVisibleTasks = useMemo(() => {
		const rows = process.stdout.rows || 24
		return Math.max(1, Math.floor((rows - CHROME_HEIGHT) / TASK_CARD_HEIGHT))
	}, [])

	// ═══════════════════════════════════════════════════════════════════════════
	// Task Grouping and Filtering
	// ═══════════════════════════════════════════════════════════════════════════

	// Group tasks by column for navigation, filtering by search query
	const tasksByColumn = useMemo(() => {
		if (!Result.isSuccess(tasksResult)) return []

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
	}, [tasksResult, searchQuery])

	// Navigation hook (needs tasksByColumn)
	const {
		cursor,
		columnIndex,
		taskIndex,
		selectedTask,
		moveUp,
		moveDown,
		moveLeft,
		moveRight,
		jumpTo,
		followTask,
		halfPageDown,
		halfPageUp,
		goToFirst,
		goToLast,
		goToFirstColumn,
		goToLastColumn,
	} = useNavigation(tasksByColumn)

	// Get all tasks as flat list for jump label generation
	const allTasks = useMemo(() => {
		const tasks: Array<{ task: TaskWithSession; columnIndex: number; taskIndex: number }> = []

		tasksByColumn.forEach((column, colIdx) => {
			column.forEach((task, taskIdx) => {
				tasks.push({ task, columnIndex: colIdx, taskIndex: taskIdx })
			})
		})

		return tasks
	}, [tasksByColumn])

	// Generate jump labels
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

	// Poll VC status every 5 seconds
	useEffect(() => {
		const interval = setInterval(() => {
			refreshVCStatus()
		}, 5000)
		return () => clearInterval(interval)
	}, [refreshVCStatus])

	// ═══════════════════════════════════════════════════════════════════════════
	// Jump Input Handler
	// ═══════════════════════════════════════════════════════════════════════════

	const handleJumpInput = useCallback(
		(key: string) => {
			if (!jumpLabels) {
				exitToNormal()
				return
			}

			if (pendingJumpKey) {
				// Second key - complete the jump
				const label = pendingJumpKey + key
				const target = jumpLabels.get(label)
				if (target) {
					jumpTo(target.columnIndex, target.taskIndex)
				}
				exitToNormal()
			} else {
				// First key - check if any labels start with this key
				const hasMatch = [...jumpLabels.keys()].some((l) => l.startsWith(key))
				if (hasMatch) {
					setPendingJumpKey(key)
				} else {
					exitToNormal()
				}
			}
		},
		[jumpLabels, pendingJumpKey, jumpTo, exitToNormal, setPendingJumpKey],
	)

	// ═══════════════════════════════════════════════════════════════════════════
	// Keyboard Handler
	// ═══════════════════════════════════════════════════════════════════════════

	useKeyboard((event) => {
		// DEBUG: Log every keypress and current mode
		console.error(`[KEY] "${event.name}" | mode=${mode._tag}`)

		// Ctrl-C: Kill active editor popup (MUST be first - works in any state)
		if (event.ctrl && event.name === "c") {
			killActivePopup()
			return
		}

		// Help overlay handling - dismiss on any key
		if (showingHelp) {
			dismissOverlay()
			return
		}

		// Detail panel handling - dismiss on Enter or Escape
		if (showingDetail) {
			if (event.name === "return" || event.name === "escape") {
				dismissOverlay()
			}
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

		// Escape always returns to normal mode
		if (event.name === "escape") {
			if (isSearch) {
				clearSearch()
			} else if (isCommand) {
				clearCommand()
			} else if (isSelect) {
				exitSelect(true)
			} else {
				exitToNormal()
			}
			return
		}

		// Handle action mode (Space menu)
		if (isAction) {
			handleActionMode(event)
			return
		}

		// Handle goto mode
		if (isGoto) {
			handleGotoMode(event)
			return
		}

		// Handle search mode
		if (isSearch) {
			handleSearchMode(event)
			return
		}

		// Handle command mode
		if (isCommand) {
			handleCommandMode(event)
			return
		}

		// Handle select mode
		if (isSelect) {
			handleSelectMode(event)
			return
		}

		// Normal mode
		handleNormalMode(event)
	})

	// ═══════════════════════════════════════════════════════════════════════════
	// Mode-Specific Keyboard Handlers
	// ═══════════════════════════════════════════════════════════════════════════

	const handleActionMode = useCallback(
		(event: any) => {
			switch (event.name) {
				case "left":
				case "h": {
					// Move selected tasks (or current task) to previous column
					if (columnIndex > 0) {
						const targetStatus = COLUMNS[columnIndex - 1]?.status
						if (targetStatus) {
							if (selectedIds.length > 0) {
								const firstTaskId = selectedIds[0]
								moveTasks({ taskIds: [...selectedIds], newStatus: targetStatus })
									.then(() => {
										followTask(firstTaskId ?? "")
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							} else if (selectedTask) {
								const taskToFollow = selectedTask.id
								moveTask({ taskId: selectedTask.id, newStatus: targetStatus })
									.then(() => {
										followTask(taskToFollow)
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							}
						}
					}
					break
				}
				case "right":
				case "l": {
					// Move selected tasks (or current task) to next column
					if (columnIndex < COLUMNS.length - 1) {
						const targetStatus = COLUMNS[columnIndex + 1]?.status
						if (targetStatus) {
							if (selectedIds.length > 0) {
								const firstTaskId = selectedIds[0]
								moveTasks({ taskIds: [...selectedIds], newStatus: targetStatus })
									.then(() => {
										followTask(firstTaskId ?? "")
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							} else if (selectedTask) {
								const taskToFollow = selectedTask.id
								moveTask({ taskId: selectedTask.id, newStatus: targetStatus })
									.then(() => {
										followTask(taskToFollow)
										refreshTasks()
									})
									.catch((error) => showError(`Move failed: ${error}`))
							}
						}
					}
					break
				}
				case "s": {
					// Start a new session
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
					exitToNormal()
					break
				}
				case "a": {
					// Attach to session - switches tmux client to Claude session
					if (selectedTask) {
						attachExternal(selectedTask.id)
							.then(() => {
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
					exitToNormal()
					break
				}
				case "A": {
					// Attach to session inline (replace TUI)
					if (selectedTask) {
						attachInline(selectedTask.id).catch((error: any) => {
							const msg =
								error && typeof error === "object" && error._tag === "SessionNotFoundError"
									? `No session for ${selectedTask.id} - press Space+s to start`
									: `Failed to attach: ${error}`
							showError(msg)
						})
					}
					exitToNormal()
					break
				}
				case "p": {
					// Pause a running session
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
					exitToNormal()
					break
				}
				case "r": {
					// Resume a paused session
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
					exitToNormal()
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
					exitToNormal()
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
					exitToNormal()
					break
				}
				case "E": {
					// Edit bead via Claude (AI-assisted)
					if (selectedTask) {
						// TODO: Implement Claude edit session
						showError("Claude edit not yet implemented - use 'e' for $EDITOR")
					}
					exitToNormal()
					break
				}
				case "P": {
					// Create PR
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
					exitToNormal()
					break
				}
				case "d": {
					// Cleanup/delete worktree
					if (selectedTask) {
						if (selectedTask.sessionState === "idle") {
							showError(`No worktree to delete for ${selectedTask.id}`)
						} else {
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
					exitToNormal()
					break
				}
				case "D": {
					// Delete bead entirely
					if (selectedTask) {
						deleteBead(selectedTask.id)
							.then(() => {
								refreshTasks()
								showSuccess(`Deleted ${selectedTask.id}`)
							})
							.catch((error) => {
								showError(`Failed to delete: ${error}`)
							})
					}
					exitToNormal()
					break
				}
			}
		},
		[
			columnIndex,
			selectedIds,
			selectedTask,
			moveTasks,
			moveTask,
			followTask,
			refreshTasks,
			showError,
			showSuccess,
			showInfo,
			startSession,
			attachExternal,
			attachInline,
			pauseSession,
			resumeSession,
			stopSession,
			editBead,
			createPR,
			cleanup,
			deleteBead,
			exitToNormal,
		],
	)

	const handleGotoMode = useCallback(
		(event: any) => {
			if (isGotoPending) {
				// Waiting for second key after 'g'
				switch (event.name) {
					case "w": {
						const labels = computeJumpLabels()
						enterJump(labels)
						break
					}
					case "g":
						goToFirst()
						exitToNormal()
						break
					case "e":
						goToLast()
						exitToNormal()
						break
					case "h":
						goToFirstColumn()
						exitToNormal()
						break
					case "l":
						goToLastColumn()
						exitToNormal()
						break
					default:
						exitToNormal()
				}
			} else if (isJump) {
				handleJumpInput(event.name)
			}
		},
		[
			isGotoPending,
			isJump,
			computeJumpLabels,
			enterJump,
			goToFirst,
			goToLast,
			goToFirstColumn,
			goToLastColumn,
			exitToNormal,
			handleJumpInput,
		],
	)

	const handleSearchMode = useCallback(
		(event: any) => {
			if (event.name === "return") {
				exitToNormal()
				return
			}

			if (event.name === "backspace") {
				if (searchQuery.length > 0) {
					updateSearch(searchQuery.slice(0, -1))
				}
				return
			}

			// Regular character input
			if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				updateSearch(searchQuery + event.sequence)
			}
		},
		[searchQuery, updateSearch, exitToNormal],
	)

	const handleCommandMode = useCallback(
		(event: any) => {
			const vcStatus = Result.isSuccess(vcStatusResult) ? vcStatusResult.value.status : undefined

			if (event.name === "return") {
				if (!commandInput.trim()) {
					clearCommand()
					return
				}

				if (vcStatus !== "running") {
					showError("VC is not running - start it with 'a' key")
					clearCommand()
					return
				}

				sendVCCommand(commandInput)
					.then(() => {
						showSuccess(`Sent to VC: ${commandInput}`)
						clearCommand()
					})
					.catch((error: any) => {
						const msg =
							error && typeof error === "object" && error._tag === "VCNotRunningError"
								? "VC is not running"
								: `Failed to send command: ${error}`
						showError(msg)
						clearCommand()
					})
				return
			}

			if (event.name === "backspace") {
				if (commandInput.length > 0) {
					updateCommand(commandInput.slice(0, -1))
				}
				return
			}

			// Regular character input
			if (event.sequence && event.sequence.length === 1 && !event.ctrl && !event.meta) {
				updateCommand(commandInput + event.sequence)
			}
		},
		[vcStatusResult, commandInput, sendVCCommand, showSuccess, showError, clearCommand, updateCommand],
	)

	const handleSelectMode = useCallback(
		(event: any) => {
			switch (event.name) {
				case "up":
				case "k":
					moveUp()
					break
				case "down":
				case "j":
					moveDown()
					break
				case "left":
				case "h":
					moveLeft()
					break
				case "right":
				case "l":
					moveRight()
					break
				case "space":
					if (selectedTask) {
						toggleSelection(selectedTask.id)
					}
					break
				case "v":
					exitSelect()
					break
			}
		},
		[moveUp, moveDown, moveLeft, moveRight, selectedTask, toggleSelection, exitSelect],
	)

	const handleNormalMode = useCallback(
		(event: any) => {
			switch (event.name) {
				case "up":
				case "k":
					moveUp()
					break
				case "down":
				case "j":
					moveDown()
					break
				case "left":
				case "h":
					moveLeft()
					break
				case "right":
				case "l":
					moveRight()
					break
				case "g":
					enterGoto()
					break
				case "v":
					enterSelect()
					break
				case "space":
					enterAction()
					break
				case "q":
					process.exit(0)
					break
				case "?":
					showHelp()
					break
				case "return":
					if (selectedTask) {
						showDetail(selectedTask.id)
					}
					break
				case "c": {
					// Create new bead via $EDITOR
					createBeadViaEditor()
						.then((result) => {
							refreshTasks()
							showSuccess(`Created ${result.id}`)
						})
						.catch((error: any) => {
							const msg =
								error && typeof error === "object" && error._tag === "ParseMarkdownError"
									? `Invalid format: ${error.message}`
									: error && typeof error === "object" && error._tag === "EditorError"
										? `Editor error: ${error.message}`
										: `Failed to create: ${error}`
							showError(msg)
						})
					break
				}
				case "C":
					// Create bead via Claude (natural language)
					showClaudeCreate()
					break
				case "a":
					toggleVCAutoPilot()
						.then((status) => {
							refreshVCStatus()
							const message =
								status.status === "running" ? "VC auto-pilot started" : "VC auto-pilot stopped"
							showSuccess(message)
						})
						.catch((error) => {
							showError(`Failed to toggle VC auto-pilot: ${error}`)
						})
					break
				case "tab":
					// Toggle view mode between kanban and compact
					setViewMode((current: ViewMode) => (current === "kanban" ? "compact" : "kanban"))
					break
			}

			// "/" to enter search mode
			if (event.sequence === "/") {
				enterSearch()
				return
			}

			// ":" to enter command mode
			if (event.sequence === ":") {
				enterCommand()
				return
			}

			// Ctrl-d: half page down
			if (event.ctrl && event.name === "d") {
				halfPageDown()
			}

			// Ctrl-u: half page up
			if (event.ctrl && event.name === "u") {
				halfPageUp()
			}
		},
		[
			moveUp,
			moveDown,
			moveLeft,
			moveRight,
			enterGoto,
			enterSelect,
			enterAction,
			showHelp,
			showDetail,
			showCreate,
			showClaudeCreate,
			createBeadViaEditor,
			refreshTasks,
			selectedTask,
			toggleVCAutoPilot,
			refreshVCStatus,
			showSuccess,
			showError,
			enterSearch,
			enterCommand,
			halfPageDown,
			halfPageUp,
			setViewMode,
		],
	)

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
					tasks={tasksResult.value}
					selectedTaskId={selectedTask?.id}
					activeColumnIndex={columnIndex}
					activeTaskIndex={taskIndex}
					selectedIds={new Set(selectedIds)}
					jumpLabels={isJump ? jumpLabels ?? null : null}
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
							.then((issue: any) => {
								dismissOverlay()
								refreshTasks()
								showSuccess(`Created task: ${issue.id}`)
							})
							.catch((error) => {
								dismissOverlay()
								showError(`Failed to create task: ${error}`)
							})
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Claude create prompt */}
			{showingClaudeCreate && (
				<ClaudeCreatePrompt
					onSubmit={(description) => {
						claudeCreateSession(description)
							.then((sessionName: string) => {
								dismissOverlay()
								showSuccess(`Claude session started: ${sessionName}`)
								showInfo(`Attach with: tmux attach -t ${sessionName}`)
							})
							.catch((error) => {
								dismissOverlay()
								showError(`Failed to start Claude session: ${error}`)
							})
					}}
					onCancel={() => dismissOverlay()}
				/>
			)}

			{/* Toast notifications */}
			<ToastContainer toasts={toasts} onDismiss={dismissToast} />
		</box>
	)
}
