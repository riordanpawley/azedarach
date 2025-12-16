/**
 * KeyboardService - Data-driven keyboard handler in Effect-land
 *
 * Manages keyboard bindings using a data-driven approach with Effect.Service pattern.
 * Keybindings are stored as data structures with associated Effect actions,
 * allowing for dynamic registration, mode-based filtering, and overlay precedence.
 *
 * This service handles ALL keyboard input for the application, replacing the
 * inline useKeyboard handlers that were previously in App.tsx.
 */

import type { CommandExecutor } from "@effect/platform/CommandExecutor"
import type { FileSystem } from "@effect/platform/FileSystem"
import { Effect, Record, Ref } from "effect"
import { AttachmentService } from "../core/AttachmentService"
import { BeadsClient, type BeadsError } from "../core/BeadsClient"
import { BeadEditorService } from "../core/EditorService"
import { type MergeConflictError, PRWorkflow } from "../core/PRWorkflow"
import { SessionManager } from "../core/SessionManager"
import { VCService } from "../core/VCService"
import { COLUMNS, generateJumpLabels } from "../ui/types"
import { BoardService } from "./BoardService"
import { EditorService, type JumpTarget } from "./EditorService"
import { NavigationService } from "./NavigationService"
import { OverlayService } from "./OverlayService"
import { ToastService } from "./ToastService"
import { ViewService } from "./ViewService"

// ============================================================================
// Types
// ============================================================================

/**
 * Keyboard mode for keybinding matching
 *
 * Modes map to EditorService modes:
 * - normal: Default navigation
 * - select: Multi-selection
 * - action: Action palette (Space menu)
 * - goto-pending: Waiting for second key after 'g'
 * - goto-jump: Jump label mode (2-char input)
 * - search: Search/filter with text input
 * - command: VC command with text input
 * - overlay: Any overlay is open
 * - *: Universal (matches any mode)
 */
export type KeyMode =
	| "normal"
	| "select"
	| "action"
	| "goto-pending"
	| "goto-jump"
	| "search"
	| "command"
	| "overlay"
	| "sort"
	| "*"

/**
 * Platform dependencies that keybinding actions may require.
 * These are provided by BunContext.layer at runtime.
 */
export type KeybindingDeps = CommandExecutor | FileSystem

/**
 * Keybinding definition with mode-specific action
 *
 * Actions may have platform requirements (CommandExecutor, FileSystem, BeadsClient)
 * which are satisfied by the runtime layer when KeyboardService is used.
 */
export interface Keybinding {
	readonly key: string
	readonly mode: KeyMode
	readonly description: string
	readonly action: Effect.Effect<void, BeadsError, KeybindingDeps>
}

// ============================================================================
// Service Definition
// ============================================================================

export class KeyboardService extends Effect.Service<KeyboardService>()("KeyboardService", {
	// Declare ALL dependencies - Effect resolves the full graph
	dependencies: [
		ToastService.Default,
		OverlayService.Default,
		NavigationService.Default,
		EditorService.Default,
		BoardService.Default,
		SessionManager.Default,
		AttachmentService.Default,
		PRWorkflow.Default,
		VCService.Default,
		BeadsClient.Default,
		BeadEditorService.Default,
		ViewService.Default,
	],

	effect: Effect.gen(function* () {
		// Inject ALL services at construction time
		const toast = yield* ToastService
		const overlay = yield* OverlayService
		const nav = yield* NavigationService
		const editor = yield* EditorService
		const board = yield* BoardService
		const sessionManager = yield* SessionManager
		const attachment = yield* AttachmentService
		const prWorkflow = yield* PRWorkflow
		const vc = yield* VCService
		const beadsClient = yield* BeadsClient
		const beadEditor = yield* BeadEditorService
		const viewService = yield* ViewService

		// ========================================================================
		// Helper Functions
		// ========================================================================

		/**
		 * Get currently selected task by ID
		 *
		 * NavigationService stores the focused task ID directly,
		 * so we just look it up from the task list.
		 */
		const getSelectedTask = () =>
			Effect.gen(function* () {
				const taskId = yield* nav.getFocusedTaskId()
				if (!taskId) return undefined

				const allTasks = yield* board.getTasks()
				return allTasks.find((t) => t.id === taskId)
			})

		/**
		 * Get current cursor column index
		 */
		const getColumnIndex = () =>
			Effect.gen(function* () {
				const position = yield* nav.getPosition()
				return position.columnIndex
			})

		/**
		 * Show error toast for any failure
		 * Also logs the error via Effect.logError for debugging
		 */
		const showErrorToast =
			(prefix: string) =>
			(error: unknown): Effect.Effect<void> => {
				const message =
					error && typeof error === "object" && "message" in error
						? String((error as { message: string }).message)
						: String(error)
				return Effect.gen(function* () {
					yield* Effect.logError(`${prefix}: ${message}`, { error })
					yield* toast.show("error", `${prefix}: ${message}`)
				})
			}

		/**
		 * Open detail overlay for current cursor position
		 */
		const openCurrentDetail = (): Effect.Effect<void> =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (task) {
					yield* overlay.push({ _tag: "detail", taskId: task.id })
				}
			})

		/**
		 * Toggle selection for task at current cursor position
		 */
		const toggleCurrentSelection = (): Effect.Effect<void> =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (task) {
					yield* editor.toggleSelection(task.id)
				}
			})

		/**
		 * Handle escape key based on current context
		 *
		 * Priority:
		 * 1. Close overlay if one is open
		 * 2. Exit to normal mode if in another mode
		 */
		const handleEscape = (): Effect.Effect<void> =>
			Effect.gen(function* () {
				const hasOverlay = yield* overlay.isOpen()
				if (hasOverlay) {
					yield* overlay.pop()
					return
				}
				const mode = yield* editor.getMode()
				if (mode._tag !== "normal") {
					yield* editor.exitToNormal()
				}
			})

		/**
		 * Compute jump labels for all visible tasks
		 */
		const computeJumpLabels = (): Effect.Effect<Record.ReadonlyRecord<string, JumpTarget>> =>
			Effect.gen(function* () {
				const tasksByColumn = yield* board.getTasksByColumn()

				// Flatten tasks with position info
				const allTasks: Array<{
					taskId: string
					columnIndex: number
					taskIndex: number
				}> = []

				COLUMNS.forEach((col, colIdx) => {
					const tasks = tasksByColumn[col.status] ?? []
					tasks.forEach((task, taskIdx) => {
						allTasks.push({
							taskId: task.id,
							columnIndex: colIdx,
							taskIndex: taskIdx,
						})
					})
				})

				// Generate label strings and build the Record
				const labels = generateJumpLabels(allTasks.length)
				return Record.fromEntries(
					allTasks
						.map(({ taskId, columnIndex, taskIndex }, i) =>
							labels[i] ? [labels[i]!, { taskId, columnIndex, taskIndex }] : null,
						)
						.filter((entry): entry is [string, JumpTarget] => entry !== null),
				)
			})

		/**
		 * Handle jump label input (2-char sequence)
		 */
		const handleJumpInput = (key: string): Effect.Effect<void> =>
			Effect.gen(function* () {
				const mode = yield* editor.getMode()
				if (mode._tag !== "goto" || mode.gotoSubMode !== "jump") return

				if (!mode.pendingJumpKey) {
					// First character
					yield* editor.setPendingJumpKey(key)
				} else {
					// Second character - lookup and jump
					const label = mode.pendingJumpKey + key
					const target = mode.jumpLabels?.[label]
					if (target) {
						yield* nav.jumpTo(target.columnIndex, target.taskIndex)
					}
					yield* editor.exitToNormal()
				}
			})

		/**
		 * Move task(s) to adjacent column
		 */
		const moveTasksToColumn = (direction: "left" | "right") =>
			Effect.gen(function* () {
				const columnIndex = yield* getColumnIndex()
				const targetColIdx = direction === "left" ? columnIndex - 1 : columnIndex + 1

				// Bounds check
				if (targetColIdx < 0 || targetColIdx >= COLUMNS.length) {
					return
				}

				const targetStatus = COLUMNS[targetColIdx]?.status
				if (!targetStatus) {
					return
				}

				// Get selected IDs or current task
				const mode = yield* editor.getMode()
				const selectedIds = mode._tag === "select" ? mode.selectedIds : []
				const task = yield* getSelectedTask()

				const taskIdsToMove = selectedIds.length > 0 ? [...selectedIds] : task ? [task.id] : []
				const firstTaskId = taskIdsToMove[0]

				if (taskIdsToMove.length > 0) {
					yield* Effect.all(
						taskIdsToMove.map((id) => beadsClient.update(id, { status: targetStatus })),
					)
					// Refresh board to reflect the move
					yield* board.refresh()
					// Follow the first task
					if (firstTaskId) {
						yield* nav.setFollow(firstTaskId)
					}
				}
			})

		// ========================================================================
		// Action Mode Handlers - Use injected services directly
		// ========================================================================

		/**
		 * Start session action (Space+s)
		 */
		const actionStartSession = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState !== "idle") {
					yield* toast.show("error", `Cannot start: task is ${task.sessionState}`)
					return
				}

				yield* sessionManager.start({ beadId: task.id, projectPath: process.cwd() }).pipe(
					Effect.tap(() => toast.show("success", `Started session for ${task.id}`)),
					Effect.catchAll(showErrorToast("Failed to start")),
				)
			})

		/**
		 * Start session with initial prompt (Space+S)
		 * Starts Claude and tells it to "work on bead {beadId}"
		 */
		const actionStartSessionWithPrompt = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState !== "idle") {
					yield* toast.show("error", `Cannot start: task is ${task.sessionState}`)
					return
				}

				yield* sessionManager
					.start({
						beadId: task.id,
						projectPath: process.cwd(),
						initialPrompt: `work on ${task.id}`,
					})
					.pipe(
						Effect.tap(() => toast.show("success", `Started session for ${task.id} with prompt`)),
						Effect.catchAll(showErrorToast("Failed to start")),
					)
			})

		/**
		 * Attach external action (Space+a)
		 */
		const actionAttachExternal = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				yield* attachment.attachExternal(task.id).pipe(
					Effect.tap(() => toast.show("info", "Switched! Ctrl-a ) to return")),
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "_tag" in error
								? error._tag === "SessionNotFoundError"
									? `No session for ${task.id} - press Space+s to start`
									: String((error as { message?: string }).message || error)
								: String(error)
						return Effect.gen(function* () {
							yield* Effect.logError(`Attach external: ${msg}`, { error })
							yield* toast.show("error", msg)
						})
					}),
				)
			})

		/**
		 * Attach inline action (Space+A)
		 */
		const actionAttachInline = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				yield* attachment
					.attachInline(task.id)
					.pipe(Effect.catchAll(showErrorToast("Failed to attach")))
			})

		/**
		 * Pause session action (Space+p)
		 */
		const actionPauseSession = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState !== "busy") {
					yield* toast.show("error", `Cannot pause: task is ${task.sessionState}`)
					return
				}

				yield* sessionManager.pause(task.id).pipe(
					Effect.tap(() => toast.show("success", `Paused session for ${task.id}`)),
					Effect.catchAll(showErrorToast("Failed to pause")),
				)
			})

		/**
		 * Resume session action (Space+r)
		 */
		const actionResumeSession = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState !== "paused") {
					yield* toast.show("error", `Cannot resume: task is ${task.sessionState}`)
					return
				}

				yield* sessionManager.resume(task.id).pipe(
					Effect.tap(() => toast.show("success", `Resumed session for ${task.id}`)),
					Effect.catchAll(showErrorToast("Failed to resume")),
				)
			})

		/**
		 * Stop session action (Space+x)
		 */
		const actionStopSession = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", "No session to stop")
					return
				}

				yield* sessionManager.stop(task.id).pipe(
					Effect.tap(() => toast.show("success", `Stopped session for ${task.id}`)),
					Effect.catchAll(showErrorToast("Failed to stop")),
				)
			})

		/**
		 * Edit bead action (Space+e)
		 */
		const actionEditBead = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				yield* beadEditor.editBead(task).pipe(
					Effect.tap(() => toast.show("success", `Updated ${task.id}`)),
					Effect.tap(() => board.refresh()),
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "_tag" in error
								? error._tag === "ParseMarkdownError"
									? `Invalid format: ${(error as { message: string }).message}`
									: error._tag === "EditorError"
										? `Editor error: ${(error as { message: string }).message}`
										: `Failed to edit: ${error}`
								: `Failed to edit: ${error}`
						return Effect.gen(function* () {
							yield* Effect.logError(`Edit bead: ${msg}`, { error })
							yield* toast.show("error", msg)
						})
					}),
				)
			})

		/**
		 * Create bead via $EDITOR action (c key)
		 */
		const actionCreateBead = () =>
			Effect.gen(function* () {
				yield* beadEditor.createBead().pipe(
					Effect.tap(() => board.refresh()),
					Effect.tap((result) => nav.jumpToTask(result.id)),
					Effect.tap((result) => toast.show("success", `Created ${result.id}`)),
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "_tag" in error
								? error._tag === "ParseMarkdownError"
									? `Invalid format: ${(error as { message: string }).message}`
									: error._tag === "EditorError"
										? `Editor error: ${(error as { message: string }).message}`
										: `Failed to create: ${error}`
								: `Failed to create: ${error}`
						return Effect.gen(function* () {
							yield* Effect.logError(`Create bead: ${msg}`, { error })
							yield* toast.show("error", msg)
						})
					}),
				)
			})

		/**
		 * Create PR action (Space+P)
		 */
		const actionCreatePR = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				yield* toast.show("info", `Creating PR for ${task.id}...`)

				yield* prWorkflow.createPR({ beadId: task.id, projectPath: process.cwd() }).pipe(
					Effect.tap((pr) => toast.show("success", `PR created: ${pr.url}`)),
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "_tag" in error && error._tag === "GHCLIError"
								? String((error as { message: string }).message)
								: `Failed to create PR: ${error}`
						return Effect.gen(function* () {
							yield* Effect.logError(`Create PR: ${msg}`, { error })
							yield* toast.show("error", msg)
						})
					}),
				)
			})

		/**
		 * Cleanup worktree action (Space+d)
		 */
		const actionCleanup = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree to delete for ${task.id}`)
					return
				}

				yield* toast.show("info", `Cleaning up ${task.id}...`)

				yield* prWorkflow.cleanup({ beadId: task.id, projectPath: process.cwd() }).pipe(
					Effect.tap(() => toast.show("success", `Cleaned up ${task.id}`)),
					Effect.catchAll(showErrorToast("Failed to cleanup")),
				)
			})

		/**
		 * Merge worktree to main action (Space+m)
		 *
		 * Merges the worktree branch to main locally without creating a PR.
		 * Handles merge conflicts gracefully by showing error and preserving worktree.
		 */
		const actionMergeToMain = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				yield* toast.show("info", `Merging ${task.id} to main...`)

				yield* prWorkflow.mergeToMain({ beadId: task.id, projectPath: process.cwd() }).pipe(
					Effect.tap(() => board.refresh()),
					Effect.tap(() => toast.show("success", `Merged ${task.id} to main`)),
					Effect.catchAll((error: MergeConflictError | { _tag?: string; message?: string }) => {
						if (error._tag === "MergeConflictError") {
							return toast.show("error", `Merge conflict: ${error.message}`)
						}
						const msg =
							error && typeof error === "object" && "message" in error
								? String(error.message)
								: String(error)
						return toast.show("error", `Merge failed: ${msg}`)
					}),
				)
			})

		/**
		 * Delete bead action (Space+D)
		 */
		const actionDeleteBead = () =>
			Effect.gen(function* () {
				const task = yield* getSelectedTask()
				if (!task) return

				yield* beadsClient.delete(task.id).pipe(
					Effect.tap(() => toast.show("success", `Deleted ${task.id}`)),
					Effect.tap(() => board.refresh()),
					// Move cursor to a valid task after deletion
					Effect.tap(() => nav.initialize()),
					Effect.catchAll(showErrorToast("Failed to delete")),
				)
			})

		/**
		 * Toggle VC auto-pilot action (a key)
		 */
		const actionToggleVC = () =>
			vc.toggleAutoPilot().pipe(
				Effect.tap((status) => {
					const message =
						status.status === "running" ? "VC auto-pilot started" : "VC auto-pilot stopped"
					return toast.show("success", message)
				}),
				Effect.catchAll(showErrorToast("Failed to toggle VC")),
			)

		/**
		 * Handle text input for search/command modes
		 *
		 * Returns true if the key was handled as text input
		 */
		const handleTextInput = (key: string) =>
			Effect.gen(function* () {
				const mode = yield* editor.getMode()

				// Search mode text input
				if (mode._tag === "search") {
					if (key === "return") {
						yield* editor.exitToNormal()
						return true
					}
					if (key === "backspace") {
						if (mode.query.length > 0) {
							yield* editor.updateSearch(mode.query.slice(0, -1))
						}
						return true
					}
					// Single printable character
					if (key.length === 1 && !key.startsWith("C-")) {
						yield* editor.updateSearch(mode.query + key)
						return true
					}
					return false
				}

				// Command mode text input
				if (mode._tag === "command") {
					if (key === "return") {
						if (!mode.input.trim()) {
							yield* editor.clearCommand()
							return true
						}

						// Send command to VC using injected service
						yield* vc.sendCommand(mode.input).pipe(
							Effect.tap(() => toast.show("success", `Sent to VC: ${mode.input}`)),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "_tag" in error
										? error._tag === "VCNotRunningError"
											? "VC is not running - start it with 'a' key"
											: String((error as { message?: string }).message || error)
										: String(error)
								return Effect.gen(function* () {
									yield* Effect.logError(`VC command: ${msg}`, { error })
									yield* toast.show("error", msg)
								})
							}),
						)
						yield* editor.clearCommand()
						return true
					}
					if (key === "backspace") {
						if (mode.input.length > 0) {
							yield* editor.updateCommand(mode.input.slice(0, -1))
						}
						return true
					}
					// Single printable character
					if (key.length === 1 && !key.startsWith("C-")) {
						yield* editor.updateCommand(mode.input + key)
						return true
					}
					return false
				}

				return false
			})

		// ========================================================================
		// Default Keybindings
		// ========================================================================

		const defaultBindings: ReadonlyArray<Keybinding> = [
			// ======================================================================
			// Normal Mode - Navigation
			// ======================================================================
			{
				key: "j",
				mode: "normal",
				description: "Move down",
				action: nav.move("down"),
			},
			{
				key: "k",
				mode: "normal",
				description: "Move up",
				action: nav.move("up"),
			},
			{
				key: "h",
				mode: "normal",
				description: "Move left",
				action: nav.move("left"),
			},
			{
				key: "l",
				mode: "normal",
				description: "Move right",
				action: nav.move("right"),
			},
			{
				key: "down",
				mode: "normal",
				description: "Move down",
				action: nav.move("down"),
			},
			{
				key: "up",
				mode: "normal",
				description: "Move up",
				action: nav.move("up"),
			},
			{
				key: "left",
				mode: "normal",
				description: "Move left",
				action: nav.move("left"),
			},
			{
				key: "right",
				mode: "normal",
				description: "Move right",
				action: nav.move("right"),
			},
			{
				key: "C-d",
				mode: "normal",
				description: "Half page down",
				action: nav.halfPageDown(),
			},
			{
				key: "C-u",
				mode: "normal",
				description: "Half page up",
				action: nav.halfPageUp(),
			},

			// ======================================================================
			// Normal Mode - Mode Transitions
			// ======================================================================
			{
				key: "g",
				mode: "normal",
				description: "Enter goto mode",
				action: editor.enterGoto(),
			},
			{
				key: "v",
				mode: "normal",
				description: "Enter select mode",
				action: editor.enterSelect(),
			},
			{
				key: "space",
				mode: "normal",
				description: "Enter action mode",
				action: editor.enterAction(),
			},
			{
				key: "/",
				mode: "normal",
				description: "Enter search mode",
				action: editor.enterSearch(),
			},
			{
				key: ":",
				mode: "normal",
				description: "Enter command mode",
				action: editor.enterCommand(),
			},
			{
				key: ",",
				mode: "normal",
				description: "Enter sort mode",
				action: editor.enterSort(),
			},

			// ======================================================================
			// Normal Mode - Actions
			// ======================================================================
			{
				key: "q",
				mode: "normal",
				description: "Quit",
				action: Effect.sync(() => process.exit(0)),
			},
			{
				key: "?",
				mode: "normal",
				description: "Show help",
				action: overlay.push({ _tag: "help" }),
			},
			{
				key: "return",
				mode: "normal",
				description: "View detail",
				action: Effect.suspend(() => openCurrentDetail()),
			},
			{
				key: "c",
				mode: "normal",
				description: "Create bead via $EDITOR",
				action: Effect.suspend(() => actionCreateBead()),
			},
			{
				key: "S-c",
				mode: "normal",
				description: "Create bead via Claude",
				action: overlay.push({ _tag: "claudeCreate" }),
			},
			{
				key: "a",
				mode: "normal",
				description: "Toggle VC auto-pilot",
				action: Effect.suspend(() => actionToggleVC()),
			},
			{
				key: "tab",
				mode: "normal",
				description: "Toggle view mode (kanban/compact)",
				action: viewService.toggleViewMode(),
			},

			// ======================================================================
			// Action Mode (Space menu)
			// ======================================================================
			{
				key: "h",
				mode: "action",
				description: "Move task left",
				action: Effect.suspend(() =>
					moveTasksToColumn("left").pipe(
						Effect.tap(() => editor.exitToNormal()),
						Effect.catchAll(Effect.logError),
					),
				),
			},
			{
				key: "l",
				mode: "action",
				description: "Move task right",
				action: Effect.suspend(() =>
					moveTasksToColumn("right").pipe(
						Effect.tap(() => editor.exitToNormal()),
						Effect.catchAll(Effect.logError),
					),
				),
			},
			{
				key: "left",
				mode: "action",
				description: "Move task left",
				action: Effect.suspend(() =>
					moveTasksToColumn("left").pipe(
						Effect.tap(() => editor.exitToNormal()),
						Effect.catchAll(Effect.logError),
					),
				),
			},
			{
				key: "right",
				mode: "action",
				description: "Move task right",
				action: Effect.suspend(() =>
					moveTasksToColumn("right").pipe(
						Effect.tap(() => editor.exitToNormal()),
						Effect.catchAll(Effect.logError),
					),
				),
			},
			{
				key: "s",
				mode: "action",
				description: "Start session",
				action: Effect.suspend(() =>
					actionStartSession().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "S-s",
				mode: "action",
				description: "Start+work (prompt Claude)",
				action: Effect.suspend(() =>
					actionStartSessionWithPrompt().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "a",
				mode: "action",
				description: "Attach to session",
				action: Effect.suspend(() =>
					actionAttachExternal().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "S-a",
				mode: "action",
				description: "Attach inline",
				action: Effect.suspend(() =>
					actionAttachInline().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "p",
				mode: "action",
				description: "Pause session",
				action: Effect.suspend(() =>
					actionPauseSession().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "r",
				mode: "action",
				description: "Resume session",
				action: Effect.suspend(() =>
					actionResumeSession().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "x",
				mode: "action",
				description: "Stop session",
				action: Effect.suspend(() =>
					actionStopSession().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "e",
				mode: "action",
				description: "Edit bead ($EDITOR)",
				action: Effect.suspend(() =>
					actionEditBead().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "S-e",
				mode: "action",
				description: "Edit bead (Claude)",
				action: Effect.suspend(() =>
					toast
						.show("error", "Claude edit not yet implemented - use 'e' for $EDITOR")
						.pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "S-p",
				mode: "action",
				description: "Create PR",
				action: Effect.suspend(() =>
					actionCreatePR().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "d",
				mode: "action",
				description: "Cleanup worktree",
				action: Effect.suspend(() => actionCleanup().pipe(Effect.tap(() => editor.exitToNormal()))),
			},
			{
				key: "m",
				mode: "action",
				description: "Merge to main",
				action: Effect.suspend(() =>
					actionMergeToMain().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},
			{
				key: "S-d",
				mode: "action",
				description: "Delete bead",
				action: Effect.suspend(() =>
					actionDeleteBead().pipe(Effect.tap(() => editor.exitToNormal())),
				),
			},

			// ======================================================================
			// Goto-Pending Mode (after pressing 'g')
			// ======================================================================
			{
				key: "g",
				mode: "goto-pending",
				description: "Go to first",
				action: nav.goToFirst().pipe(Effect.tap(() => editor.exitToNormal())),
			},
			{
				key: "e",
				mode: "goto-pending",
				description: "Go to last",
				action: nav.goToLast().pipe(Effect.tap(() => editor.exitToNormal())),
			},
			{
				key: "h",
				mode: "goto-pending",
				description: "Go to first column",
				action: nav.goToFirstColumn().pipe(Effect.tap(() => editor.exitToNormal())),
			},
			{
				key: "l",
				mode: "goto-pending",
				description: "Go to last column",
				action: nav.goToLastColumn().pipe(Effect.tap(() => editor.exitToNormal())),
			},
			{
				key: "w",
				mode: "goto-pending",
				description: "Enter jump mode",
				action: Effect.gen(function* () {
					const labels = yield* computeJumpLabels()
					yield* editor.enterJump(labels)
				}),
			},

			// ======================================================================
			// Select Mode
			// ======================================================================
			{
				key: "j",
				mode: "select",
				description: "Move down",
				action: nav.move("down"),
			},
			{
				key: "k",
				mode: "select",
				description: "Move up",
				action: nav.move("up"),
			},
			{
				key: "h",
				mode: "select",
				description: "Move left",
				action: nav.move("left"),
			},
			{
				key: "l",
				mode: "select",
				description: "Move right",
				action: nav.move("right"),
			},
			{
				key: "down",
				mode: "select",
				description: "Move down",
				action: nav.move("down"),
			},
			{
				key: "up",
				mode: "select",
				description: "Move up",
				action: nav.move("up"),
			},
			{
				key: "left",
				mode: "select",
				description: "Move left",
				action: nav.move("left"),
			},
			{
				key: "right",
				mode: "select",
				description: "Move right",
				action: nav.move("right"),
			},
			{
				key: "space",
				mode: "select",
				description: "Toggle selection",
				action: Effect.suspend(() => toggleCurrentSelection()),
			},
			{
				key: "v",
				mode: "select",
				description: "Exit select mode",
				action: editor.exitSelect(),
			},

			// ======================================================================
			// Sort Mode
			// ======================================================================
			{
				key: "s",
				mode: "sort",
				description: "Sort by session status",
				action: editor.cycleSort("session").pipe(
					Effect.tap(() => editor.exitToNormal()),
					Effect.catchAll(Effect.logError),
				),
			},
			{
				key: "p",
				mode: "sort",
				description: "Sort by priority",
				action: editor.cycleSort("priority").pipe(
					Effect.tap(() => editor.exitToNormal()),
					Effect.catchAll(Effect.logError),
				),
			},
			{
				key: "u",
				mode: "sort",
				description: "Sort by updated at",
				action: editor.cycleSort("updated").pipe(
					Effect.tap(() => editor.exitToNormal()),
					Effect.catchAll(Effect.logError),
				),
			},

			// ======================================================================
			// Universal (*)
			// ======================================================================
			{
				key: "escape",
				mode: "*",
				description: "Exit/cancel",
				action: Effect.suspend(() => handleEscape()),
			},

			// ======================================================================
			// Overlay Mode
			// ======================================================================
			{
				key: "escape",
				mode: "overlay",
				description: "Close overlay",
				action: overlay.pop().pipe(Effect.asVoid),
			},
		]

		const keybindings = yield* Ref.make<ReadonlyArray<Keybinding>>(defaultBindings)

		/**
		 * Get effective mode for keybinding matching
		 */
		const getEffectiveMode = (): Effect.Effect<KeyMode> =>
			Effect.gen(function* () {
				const hasOverlay = yield* overlay.isOpen()
				if (hasOverlay) return "overlay"

				const mode = yield* editor.getMode()
				switch (mode._tag) {
					case "normal":
						return "normal"
					case "select":
						return "select"
					case "action":
						return "action"
					case "goto":
						return mode.gotoSubMode === "pending" ? "goto-pending" : "goto-jump"
					case "search":
						return "search"
					case "command":
						return "command"
					case "sort":
						return "sort"
					default:
						return mode satisfies never
				}
			})

		/**
		 * Find matching keybinding for key and mode
		 *
		 * Priority: specific mode > wildcard "*"
		 */
		const findBinding = (
			key: string,
			effectiveMode: KeyMode,
		): Effect.Effect<Keybinding | undefined> =>
			Effect.gen(function* () {
				const bindings = yield* Ref.get(keybindings)
				return (
					bindings.find((b) => b.key === key && b.mode === effectiveMode) ??
					bindings.find((b) => b.key === key && b.mode === "*")
				)
			})

		return {
			// State refs
			keybindings,

			/**
			 * Handle a key press
			 *
			 * Gets current context (mode, overlay status), finds matching binding,
			 * and executes its action if found.
			 */
			handleKey: (key: string) =>
				Effect.gen(function* () {
					const effectiveMode = yield* getEffectiveMode()

					// Special handling for goto-jump mode (any key is label input)
					if (effectiveMode === "goto-jump") {
						yield* handleJumpInput(key)
						return
					}

					// Check for text input handling (search/command modes)
					const handledAsText = yield* handleTextInput(key)
					if (handledAsText) return

					// Find and execute keybinding
					const binding = yield* findBinding(key, effectiveMode)
					if (binding) {
						yield* binding.action
					}
				}),

			/**
			 * Register a new keybinding
			 */
			register: (binding: Keybinding): Effect.Effect<void> =>
				Ref.update(keybindings, (bs) => [...bs, binding]),

			/**
			 * Unregister a keybinding
			 */
			unregister: (key: string, mode: KeyMode): Effect.Effect<void> =>
				Ref.update(keybindings, (bs) => bs.filter((b) => !(b.key === key && b.mode === mode))),

			/**
			 * Get all registered keybindings
			 */
			getBindings: (): Effect.Effect<ReadonlyArray<Keybinding>> => Ref.get(keybindings),
		}
	}),
}) {}
