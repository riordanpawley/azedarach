/**
 * InputHandlersService
 *
 * Handles special input processing:
 * - Escape (universal exit)
 * - Text input (search mode)
 * - Jump label input (2-char sequence)
 * - Confirm dialog input (y/n)
 * - Image attach overlay input
 * - Project selector overlay input
 * - Image preview overlay input
 * - Mode detection (getEffectiveMode)
 * - Jump label computation
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect, Record, SubscriptionRef } from "effect"
import { ImageAttachmentService } from "../../core/ImageAttachmentService.js"
import { generateJumpLabels } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { EditorService, type JumpTarget } from "../EditorService.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ProjectService } from "../ProjectService.js"
import {
	buildProjectUIState,
	extractFilterConfig,
	extractFocusedTaskId,
	extractSortConfig,
	extractViewMode,
	ProjectStateService,
} from "../ProjectStateService.js"
import { SettingsService } from "../SettingsService.js"
import { ToastService } from "../ToastService.js"
import { ViewService } from "../ViewService.js"
import type { KeyMode } from "./types.js"

// ============================================================================
// Service Definition
// ============================================================================

export class InputHandlersService extends Effect.Service<InputHandlersService>()(
	"InputHandlersService",
	{
		dependencies: [
			ToastService.Default,
			OverlayService.Default,
			EditorService.Default,
			NavigationService.Default,
			BoardService.Default,
			ImageAttachmentService.Default,
			ProjectService.Default,
			ProjectStateService.Default,
			ViewService.Default,
			SettingsService.Default,
		],

		effect: Effect.gen(function* () {
			// Inject services at construction time
			const toast = yield* ToastService
			const overlay = yield* OverlayService
			const editor = yield* EditorService
			const nav = yield* NavigationService
			const board = yield* BoardService
			const imageAttachment = yield* ImageAttachmentService
			const projectService = yield* ProjectService
			const projectState = yield* ProjectStateService
			const view = yield* ViewService
			const settings = yield* SettingsService

			// ================================================================
			// Input Handler Methods
			// ================================================================

			/**
			 * Handle escape key based on current context
			 *
			 * Priority:
			 * 1. Close overlay if one is open
			 * 2. Exit to normal mode if in another mode
			 */
			const handleEscape = () =>
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
			 * Handle text input for search/command modes
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled as text input
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

					return false
				})

			/**
			 * Handle jump label input (2-char sequence)
			 *
			 * In goto-jump mode, collects two characters to form a jump label,
			 * then jumps to the corresponding task.
			 *
			 * @param key - The key that was pressed
			 */
			const handleJumpInput = (key: string) =>
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
			 * Handle confirm overlay keyboard input
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled.
			 *
			 * y/Enter → execute onConfirm effect, pop overlay
			 * n/Escape → just pop overlay
			 */
			const handleConfirmInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "confirm") {
						return false
					}

					// DIAGNOSTIC: Log all keys received while confirm is open (az-f3iw)
					yield* Effect.log(`[confirm:key] Received key="${key}" while confirm overlay is open`)

					// y or Enter to confirm
					if (key === "y" || key === "return") {
						// Pop overlay FIRST for immediate UI feedback, then run the async operation
						// The onConfirm effect (e.g., merge) shows its own toast progress
						yield* Effect.log(`[confirm:execute] Executing onConfirm effect`)
						yield* overlay.pop()
						yield* currentOverlay.onConfirm
						return true
					}

					// n or Escape to cancel
					if (key === "n" || key === "escape") {
						yield* overlay.pop()
						return true
					}

					// Consume all other keys while overlay is open
					return true
				})

			/**
			 * Handle mergeChoice overlay keyboard input
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled.
			 *
			 * m → execute onMerge effect (merge main into branch, then attach), pop overlay
			 * s → execute onSkip effect (attach without merging), pop overlay
			 * Escape → just pop overlay (cancel)
			 */
			const handleMergeChoiceInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "mergeChoice") {
						return false
					}

					// m to merge and attach
					if (key === "m") {
						// Pop overlay FIRST for immediate UI feedback, then run the async operation
						yield* overlay.pop()
						yield* currentOverlay.onMerge
						return true
					}

					// s to skip and attach directly
					if (key === "s") {
						yield* overlay.pop()
						yield* currentOverlay.onSkip
						return true
					}

					// Escape to cancel
					if (key === "escape") {
						yield* overlay.pop()
						return true
					}

					// Consume all other keys while overlay is open
					return true
				})

			/**
			 * Handle bulkCleanup overlay keyboard input
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled.
			 *
			 * w → execute onWorktreeOnly effect (cleanup worktrees, keep beads open)
			 * f → execute onFullCleanup effect (cleanup worktrees AND close beads)
			 * Escape → just pop overlay (cancel)
			 */
			const handleBulkCleanupInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "bulkCleanup") {
						return false
					}

					// w for worktree-only cleanup
					if (key === "w") {
						yield* overlay.pop()
						yield* currentOverlay.onWorktreeOnly
						return true
					}

					// f for full cleanup (worktree + close bead)
					if (key === "f") {
						yield* overlay.pop()
						yield* currentOverlay.onFullCleanup
						return true
					}

					// Escape to cancel
					if (key === "escape") {
						yield* overlay.pop()
						return true
					}

					// Consume all other keys while overlay is open
					return true
				})

			/**
			 * Handle detail overlay keyboard input for attachment navigation and scrolling
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled
			 *
			 * Scroll keys (always scroll, even with attachments):
			 * - C-u: Scroll up half page
			 * - C-d: Scroll down half page
			 *
			 * Attachment navigation (when attachments exist):
			 * - j/down: Select next attachment
			 * - k/up: Select previous attachment (or deselect if at first)
			 * - o/return: Open selected attachment in viewer
			 * - x/delete: Remove selected attachment
			 * - i: Add new attachment (opens imageAttach overlay)
			 *
			 * General:
			 * - escape: Close detail overlay
			 */
			const handleDetailOverlayInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "detail") {
						return false
					}

					const taskId = currentOverlay.taskId

					// Escape closes the overlay
					if (key === "escape" || key === "return") {
						yield* overlay.pop()
						return true
					}

					// Scroll commands - always work, even with attachments
					if (key === "C-u") {
						yield* overlay.scroll("halfPage", -1) // up
						return true
					}
					if (key === "C-d") {
						yield* overlay.scroll("halfPage", 1) // down
						return true
					}

					// Navigate attachments with j/k or arrow keys
					if (key === "j" || key === "down") {
						yield* imageAttachment.selectNextAttachment()
						return true
					}

					if (key === "k" || key === "up") {
						yield* imageAttachment.selectPreviousAttachment()
						return true
					}

					// Open selected attachment with 'o'
					if (key === "o") {
						yield* imageAttachment.openSelectedAttachment().pipe(
							Effect.tap(() => toast.show("success", "Opening image...")),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return toast.show("error", msg)
							}),
						)
						return true
					}

					// Remove selected attachment with 'x'
					if (key === "x") {
						yield* imageAttachment.removeSelectedAttachment().pipe(
							Effect.tap((removed) => toast.show("success", `Removed: ${removed.filename}`)),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return toast.show("error", msg)
							}),
						)
						return true
					}

					// Add new attachment with 'i'
					if (key === "i") {
						yield* overlay.push({ _tag: "imageAttach", taskId })
						return true
					}

					// Preview selected attachment with 'v'
					if (key === "v") {
						// Check if there's a selected attachment
						const selected = yield* imageAttachment.getSelectedAttachment()
						if (selected) {
							// Open preview and load the image
							yield* overlay.push({ _tag: "imagePreview", taskId })
							yield* imageAttachment.openPreview().pipe(
								Effect.catchAll((error) => {
									const msg =
										error && typeof error === "object" && "message" in error
											? String(error.message)
											: String(error)
									return toast.show("error", `Preview: ${msg}`)
								}),
							)
						} else {
							yield* toast.show("info", "Select an attachment to preview (j/k)")
						}
						return true
					}

					// Don't consume other keys - let them fall through (e.g., for Space menu)
					return false
				})

			/**
			 * Handle imageAttach overlay keyboard input
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled
			 */
			const handleImageAttachInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "imageAttach") {
						return false
					}

					const overlayTaskId = currentOverlay.taskId

					// Auto-initialize ImageAttachmentService state from overlay if needed
					// This ensures state is ready regardless of how the overlay was opened
					let state = yield* SubscriptionRef.get(imageAttachment.overlayState)
					if (!state.taskId) {
						yield* Effect.logDebug("ImageAttach: auto-initializing state from overlay", {
							taskId: overlayTaskId,
						})
						yield* imageAttachment.openOverlay(overlayTaskId)
						state = yield* SubscriptionRef.get(imageAttachment.overlayState)
					}

					// Escape handling
					if (key === "escape") {
						if (state.mode === "path") {
							yield* imageAttachment.exitPathMode()
						} else {
							yield* imageAttachment.closeOverlay()
							yield* overlay.pop()
						}
						return true
					}

					// Path input mode
					if (state.mode === "path") {
						if (key === "return") {
							if (state.pathInput.trim() && !state.isAttaching) {
								yield* imageAttachment.setAttaching(true)
								yield* imageAttachment.attachFile(overlayTaskId, state.pathInput.trim()).pipe(
									Effect.tap((attachment) =>
										toast.show("success", `Image attached: ${attachment.filename}`),
									),
									Effect.tap(() => imageAttachment.closeOverlay()),
									Effect.tap(() => overlay.pop()),
									Effect.catchAll((error) => {
										const msg =
											error && typeof error === "object" && "message" in error
												? String(error.message)
												: String(error)
										return Effect.gen(function* () {
											yield* toast.show("error", `Failed to attach: ${msg}`)
											yield* imageAttachment.setAttaching(false)
										})
									}),
								)
							}
							return true
						}
						if (key === "backspace") {
							if (state.pathInput.length > 0) {
								yield* imageAttachment.setPathInput(state.pathInput.slice(0, -1))
							}
							return true
						}
						// Single printable character
						if (key.length === 1 && !key.startsWith("C-")) {
							yield* imageAttachment.setPathInput(state.pathInput + key)
							return true
						}
						return true // Consume all keys in path mode
					}

					// Menu mode
					if (key === "p" || key === "v") {
						// Paste from clipboard
						if (!state.isAttaching) {
							const hasClipboard = yield* imageAttachment.hasClipboardSupport()
							if (hasClipboard) {
								yield* Effect.logDebug("ImageAttach: initiating clipboard paste", {
									taskId: overlayTaskId,
								})
								yield* imageAttachment.setAttaching(true)
								yield* imageAttachment.attachFromClipboard(overlayTaskId).pipe(
									Effect.tap((attachment) =>
										toast.show("success", `Image attached: ${attachment.filename}`),
									),
									Effect.tap(() => imageAttachment.closeOverlay()),
									Effect.tap(() => overlay.pop()),
									Effect.catchAll((error) => {
										const msg =
											error && typeof error === "object" && "message" in error
												? String(error.message)
												: String(error)
										return Effect.gen(function* () {
											yield* toast.show("error", `Clipboard: ${msg}`)
											yield* imageAttachment.setAttaching(false)
										})
									}),
								)
							} else {
								// No clipboard tool available - show user feedback instead of silent failure
								yield* Effect.logWarning("ImageAttach: clipboard support not available", {
									platform: process.platform,
								})
								yield* toast.show(
									"error",
									process.platform === "darwin"
										? "Clipboard access not available"
										: "No clipboard tool found (install xclip or wl-clipboard)",
								)
							}
						}
						return true
					}

					if (key === "f") {
						// Enter file path mode
						yield* imageAttachment.enterPathMode()
						return true
					}

					return true // Consume all keys in overlay
				})

			/**
			 * Handle projectSelector overlay keyboard input
			 *
			 * Number keys 1-9 select a project by index.
			 * All other keys are consumed to prevent falling through.
			 *
			 * The refresh is forked (non-blocking) so the UI stays responsive.
			 * A loading indicator shows in the status bar during refresh.
			 *
			 * @returns true if key was handled, false otherwise
			 */
			const handleProjectSelectorInput = (key: string) =>
				Effect.gen(function* () {
					// Check if key is a number 1-9
					const num = Number.parseInt(key, 10)
					if (num >= 1 && num <= 9) {
						// Get projects list from ProjectService
						const projects = yield* projectService.getProjects()

						if (num <= projects.length) {
							const project = projects[num - 1]
							if (project) {
								// Save current project state before switching
								const currentProject = yield* SubscriptionRef.get(projectService.currentProject)
								if (currentProject) {
									const focusedTaskId = yield* SubscriptionRef.get(nav.focusedTaskId)
									const filterConfig = yield* SubscriptionRef.get(editor.filterConfig)
									const sortConfig = yield* SubscriptionRef.get(editor.sortConfig)
									const viewMode = yield* SubscriptionRef.get(view.viewMode)

									const state = buildProjectUIState(
										focusedTaskId,
										filterConfig,
										sortConfig,
										viewMode,
									)
									yield* projectState.saveState(currentProject.path, state)
									yield* board.saveToCache(currentProject.path)
								}

								// Exit epic drilldown before switching projects - drilldown state is project-specific
								yield* nav.exitDrillDown()

								yield* projectService.switchProject(project.name)
								yield* overlay.pop()

								// Create the state restoration effect to run after refresh completes
								const restoreState = Effect.gen(function* () {
									const savedState = yield* projectState.loadState(project.path)

									// Restore editor state (filters and sort)
									yield* editor.restoreState(
										extractSortConfig(savedState),
										extractFilterConfig(savedState),
									)

									// Restore view mode
									yield* view.setViewMode(extractViewMode(savedState))

									// Restore cursor position (navigation will validate if task still exists)
									const savedFocusId = extractFocusedTaskId(savedState)
									if (savedFocusId) {
										yield* nav.setFocusedTask(savedFocusId)
									}

									yield* toast.show("success", `Loaded: ${project.name}`)
								}).pipe(
									Effect.catchAllCause((cause) =>
										Effect.gen(function* () {
											yield* Effect.logError("Board refresh failed", cause)
											yield* toast.show("error", "Failed to load project data")
										}),
									),
								)

								// Use BoardService.switchToProject which spawns a properly scoped fiber
								const { cacheHit } = yield* board.switchToProject(project.path, restoreState)

								if (cacheHit) {
									yield* toast.show("success", `Loaded: ${project.name}`)
								} else {
									yield* toast.show("info", `Loading: ${project.name}...`)
								}
							}
						} else {
							yield* toast.show("error", `No project at position ${num}`)
						}
						return true
					}

					// Escape is handled elsewhere
					if (key === "escape") {
						return false
					}

					return true // Consume other keys in overlay
				}).pipe(
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							const msg =
								error && typeof error === "object" && "message" in error
									? String(error.message)
									: String(error)
							yield* toast.show("error", `Project switch failed: ${msg}`)
							return true
						}),
					),
				)

			/**
			 * Handle imagePreview overlay keyboard input
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled
			 *
			 * Keys:
			 * - j/down: Next attachment (load new preview)
			 * - k/up: Previous attachment (load new preview)
			 * - o: Open in external viewer
			 * - escape/q: Close preview
			 */
			const handleImagePreviewInput = (key: string) =>
				Effect.gen(function* () {
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag !== "imagePreview") {
						return false
					}

					// Escape or 'q' closes the preview
					if (key === "escape" || key === "q") {
						yield* imageAttachment.closePreview()
						yield* overlay.pop()
						return true
					}

					// Navigate to next attachment with j/down
					if (key === "j" || key === "down") {
						yield* imageAttachment.previewNext()
						// Re-render the new image
						yield* imageAttachment.openPreview().pipe(
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return toast.show("error", `Preview: ${msg}`)
							}),
						)
						return true
					}

					// Navigate to previous attachment with k/up
					if (key === "k" || key === "up") {
						yield* imageAttachment.previewPrevious()
						// Re-render the new image
						yield* imageAttachment.openPreview().pipe(
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return toast.show("error", `Preview: ${msg}`)
							}),
						)
						return true
					}

					// Open in external viewer with 'o'
					if (key === "o") {
						yield* imageAttachment.openSelectedAttachment().pipe(
							Effect.tap(() => toast.show("success", "Opening image...")),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return toast.show("error", msg)
							}),
						)
						return true
					}

					return true // Consume all other keys in overlay
				})

			/**
			 * Handle settings overlay keyboard input
			 *
			 * @param key - The key that was pressed
			 * @returns true if the key was handled
			 *
			 * Keys:
			 * - j/down: Move focus down
			 * - k/up: Move focus up
			 * - space/return: Toggle setting
			 * - e: Edit in external editor
			 * - escape: Close settings
			 */
			const handleSettingsInput = (key: string) =>
				Effect.gen(function* () {
					if (key === "escape") {
						yield* settings.close()
						yield* overlay.pop()
						return true
					}

					if (key === "j" || key === "down") {
						yield* settings.moveDown()
						return true
					}

					if (key === "k" || key === "up") {
						yield* settings.moveUp()
						return true
					}

					if (key === "space" || key === "return") {
						yield* settings.toggleCurrent()
						return true
					}

					if (key === "e") {
						const { configPath, backupContent } = yield* settings.openInEditor()
						// Use EditorService to open the file
						yield* editor.openFile(configPath)

						// After editor closes, validate the new config
						yield* settings.validateAfterEdit(configPath, backupContent)
						return true
					}

					return true // Consume all other keys in overlay
				})

			/**
			 * Compute jump labels for all visible tasks
			 *
			 * Generates 2-character labels (aa, ab, ac, ...) for each task on the board.
			 * Used by the goto-jump mode for quick navigation.
			 *
			 * IMPORTANT: Must use the same filtered/sorted data that the board renders,
			 * otherwise labels won't match visual positions.
			 */
			const computeJumpLabels = (): Effect.Effect<Record.ReadonlyRecord<string, JumpTarget>> =>
				Effect.gen(function* () {
					// Get current search query, sort config, and filter config to match board rendering
					const mode = yield* editor.getMode()
					const sortConfig = yield* editor.getSortConfig()
					const filterConfig = yield* editor.getFilterConfig()
					const searchQuery = mode._tag === "search" ? mode.query : ""

					// Get filtered and sorted tasks (matching what the board displays)
					const tasksByColumn = yield* board.getFilteredTasksByColumn(
						searchQuery,
						sortConfig,
						filterConfig,
					)

					// Apply drill-down filtering (matching drillDownFilteredTasksAtom)
					const drillDownChildIds = yield* SubscriptionRef.get(nav.drillDownChildIds)
					const filteredTasksByColumn =
						drillDownChildIds.size === 0
							? tasksByColumn
							: tasksByColumn.map((column) =>
									column.filter((task) => drillDownChildIds.has(task.id)),
								)

					// Flatten tasks with position info
					const allTasks: Array<{
						taskId: string
						columnIndex: number
						taskIndex: number
					}> = []

					// DEBUG: Log tasks per column
					yield* Effect.log(
						`[computeJumpLabels] Tasks per column: ${filteredTasksByColumn.map((tasks, idx) => `col${idx}:${tasks.length}`).join(", ")}`,
					)

					filteredTasksByColumn.forEach((tasks, colIdx) => {
						tasks.forEach((task, taskIdx) => {
							allTasks.push({
								taskId: task.id,
								columnIndex: colIdx,
								taskIndex: taskIdx,
							})
						})
					})

					// DEBUG: Log total tasks and sample
					yield* Effect.log(
						`[computeJumpLabels] Total tasks: ${allTasks.length}, sample: ${JSON.stringify(allTasks.slice(0, 5))}`,
					)

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
			 * Get effective mode for keybinding matching
			 *
			 * Maps EditorService mode + overlay state to KeyMode.
			 * Overlay takes precedence over any editor mode.
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
						case "sort":
							return "sort"
						case "filter":
							return "filter"
						case "orchestrate":
							return "orchestrate"
						case "mergeSelect":
							return "mergeSelect"
					}
				})

			// ================================================================
			// Public API
			// ================================================================

			return {
				handleEscape,
				handleTextInput,
				handleJumpInput,
				handleConfirmInput,
				handleMergeChoiceInput,
				handleBulkCleanupInput,
				handleDetailOverlayInput,
				handleImageAttachInput,
				handleProjectSelectorInput,
				handleImagePreviewInput,
				handleSettingsInput,
				computeJumpLabels,
				getEffectiveMode,
			}
		}),
	},
) {}
