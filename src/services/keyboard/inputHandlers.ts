/**
 * Input Key Handlers
 *
 * Handlers for special input processing:
 * - Escape (universal exit)
 * - Text input (search/command modes)
 * - Jump label input (2-char sequence)
 * - Confirm dialog input (y/n)
 * - Image attach overlay input
 * - Mode detection (getEffectiveMode)
 * - Jump label computation
 */

import { Effect, Record, SubscriptionRef } from "effect"
import { COLUMNS, generateJumpLabels } from "../../ui/types"
import type { JumpTarget } from "../EditorService"
import type { HandlerContext, KeyMode } from "./types"

// ============================================================================
// Input Handler Factory
// ============================================================================

/**
 * Create all input-related handlers
 *
 * These handlers manage special input processing: escape, text input,
 * jump labels, confirm dialogs, and image attachment overlay.
 */
export const createInputHandlers = (ctx: HandlerContext) => ({
	/**
	 * Handle escape key based on current context
	 *
	 * Priority:
	 * 1. Close overlay if one is open
	 * 2. Exit to normal mode if in another mode
	 */
	handleEscape: () =>
		Effect.gen(function* () {
			const hasOverlay = yield* ctx.overlay.isOpen()
			if (hasOverlay) {
				yield* ctx.overlay.pop()
				return
			}
			const mode = yield* ctx.editor.getMode()
			if (mode._tag !== "normal") {
				yield* ctx.editor.exitToNormal()
			}
		}),

	/**
	 * Handle text input for search/command modes
	 *
	 * @param key - The key that was pressed
	 * @returns true if the key was handled as text input
	 */
	handleTextInput: (key: string) =>
		Effect.gen(function* () {
			const mode = yield* ctx.editor.getMode()

			// Search mode text input
			if (mode._tag === "search") {
				if (key === "return") {
					yield* ctx.editor.exitToNormal()
					return true
				}
				if (key === "backspace") {
					if (mode.query.length > 0) {
						yield* ctx.editor.updateSearch(mode.query.slice(0, -1))
					}
					return true
				}
				// Single printable character
				if (key.length === 1 && !key.startsWith("C-")) {
					yield* ctx.editor.updateSearch(mode.query + key)
					return true
				}
				return false
			}

			// Command mode text input
			if (mode._tag === "command") {
				if (key === "return") {
					if (!mode.input.trim()) {
						yield* ctx.editor.clearCommand()
						return true
					}

					// Send command to VC using injected service
					yield* ctx.vc.sendCommand(mode.input).pipe(
						Effect.tap(() => ctx.toast.show("success", `Sent to VC: ${mode.input}`)),
						Effect.catchAll((error) => {
							const msg =
								error && typeof error === "object" && "_tag" in error
									? error._tag === "VCNotRunningError"
										? "VC is not running - start it with 'a' key"
										: String((error as { message?: string }).message || error)
									: String(error)
							return Effect.gen(function* () {
								yield* Effect.logError(`VC command: ${msg}`, { error })
								yield* ctx.toast.show("error", msg)
							})
						}),
					)
					yield* ctx.editor.clearCommand()
					return true
				}
				if (key === "backspace") {
					if (mode.input.length > 0) {
						yield* ctx.editor.updateCommand(mode.input.slice(0, -1))
					}
					return true
				}
				// Single printable character
				if (key.length === 1 && !key.startsWith("C-")) {
					yield* ctx.editor.updateCommand(mode.input + key)
					return true
				}
				return false
			}

			return false
		}),

	/**
	 * Handle jump label input (2-char sequence)
	 *
	 * In goto-jump mode, collects two characters to form a jump label,
	 * then jumps to the corresponding task.
	 *
	 * @param key - The key that was pressed
	 */
	handleJumpInput: (key: string) =>
		Effect.gen(function* () {
			const mode = yield* ctx.editor.getMode()
			if (mode._tag !== "goto" || mode.gotoSubMode !== "jump") return

			if (!mode.pendingJumpKey) {
				// First character
				yield* ctx.editor.setPendingJumpKey(key)
			} else {
				// Second character - lookup and jump
				const label = mode.pendingJumpKey + key
				const target = mode.jumpLabels?.[label]
				if (target) {
					yield* ctx.nav.jumpTo(target.columnIndex, target.taskIndex)
				}
				yield* ctx.editor.exitToNormal()
			}
		}),

	/**
	 * Handle confirm overlay keyboard input
	 *
	 * @param key - The key that was pressed
	 * @returns true if the key was handled.
	 *
	 * y/Enter → execute onConfirm effect, pop overlay
	 * n/Escape → just pop overlay
	 */
	handleConfirmInput: (key: string) =>
		Effect.gen(function* () {
			const currentOverlay = yield* ctx.overlay.current()
			if (currentOverlay?._tag !== "confirm") {
				return false
			}

			// y or Enter to confirm
			if (key === "y" || key === "return") {
				yield* currentOverlay.onConfirm
				yield* ctx.overlay.pop()
				return true
			}

			// n or Escape to cancel
			if (key === "n" || key === "escape") {
				yield* ctx.overlay.pop()
				return true
			}

			// Consume all other keys while overlay is open
			return true
		}),

	/**
	 * Handle detail overlay keyboard input for attachment navigation
	 *
	 * @param key - The key that was pressed
	 * @returns true if the key was handled
	 *
	 * Keys:
	 * - j/down: Select next attachment
	 * - k/up: Select previous attachment (or deselect if at first)
	 * - o/return: Open selected attachment in viewer
	 * - x/delete: Remove selected attachment
	 * - i: Add new attachment (opens imageAttach overlay)
	 * - escape: Close detail overlay
	 */
	handleDetailOverlayInput: (key: string) =>
		Effect.gen(function* () {
			const currentOverlay = yield* ctx.overlay.current()
			if (currentOverlay?._tag !== "detail") {
				return false
			}

			const taskId = currentOverlay.taskId

			// Escape closes the overlay
			if (key === "escape" || key === "return") {
				yield* ctx.overlay.pop()
				return true
			}

			// Navigate attachments with j/k or arrow keys
			if (key === "j" || key === "down") {
				yield* ctx.imageAttachment.selectNextAttachment()
				return true
			}

			if (key === "k" || key === "up") {
				yield* ctx.imageAttachment.selectPreviousAttachment()
				return true
			}

			// Open selected attachment with 'o'
			if (key === "o") {
				yield* ctx.imageAttachment.openSelectedAttachment().pipe(
					Effect.tap(() => ctx.toast.show("success", "Opening image...")),
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "message" in error
								? String(error.message)
								: String(error)
						return ctx.toast.show("error", msg)
					}),
				)
				return true
			}

			// Remove selected attachment with 'x'
			if (key === "x") {
				yield* ctx.imageAttachment.removeSelectedAttachment().pipe(
					Effect.tap((removed) => ctx.toast.show("success", `Removed: ${removed.filename}`)),
					Effect.catchAll((error) => {
						const msg =
							error && typeof error === "object" && "message" in error
								? String(error.message)
								: String(error)
						return ctx.toast.show("error", msg)
					}),
				)
				return true
			}

			// Add new attachment with 'i'
			if (key === "i") {
				yield* ctx.overlay.push({ _tag: "imageAttach", taskId })
				return true
			}

			// Don't consume other keys - let them fall through (e.g., for Space menu)
			return false
		}),

	/**
	 * Handle imageAttach overlay keyboard input
	 *
	 * @param key - The key that was pressed
	 * @returns true if the key was handled
	 */
	handleImageAttachInput: (key: string) =>
		Effect.gen(function* () {
			const currentOverlay = yield* ctx.overlay.current()
			if (currentOverlay?._tag !== "imageAttach") {
				return false
			}

			const overlayTaskId = currentOverlay.taskId

			// Auto-initialize ImageAttachmentService state from overlay if needed
			// This ensures state is ready regardless of how the overlay was opened
			let state = yield* SubscriptionRef.get(ctx.imageAttachmentOverlayState)
			if (!state.taskId) {
				yield* Effect.logDebug("ImageAttach: auto-initializing state from overlay", {
					taskId: overlayTaskId,
				})
				yield* ctx.imageAttachment.openOverlay(overlayTaskId)
				state = yield* SubscriptionRef.get(ctx.imageAttachmentOverlayState)
			}

			// Escape handling
			if (key === "escape") {
				if (state.mode === "path") {
					yield* ctx.imageAttachment.exitPathMode()
				} else {
					yield* ctx.imageAttachment.closeOverlay()
					yield* ctx.overlay.pop()
				}
				return true
			}

			// Path input mode
			if (state.mode === "path") {
				if (key === "return") {
					if (state.pathInput.trim() && !state.isAttaching) {
						yield* ctx.imageAttachment.setAttaching(true)
						yield* ctx.imageAttachment.attachFile(overlayTaskId, state.pathInput.trim()).pipe(
							Effect.tap((attachment) =>
								ctx.toast.show("success", `Image attached: ${attachment.filename}`),
							),
							Effect.tap(() => ctx.imageAttachment.closeOverlay()),
							Effect.tap(() => ctx.overlay.pop()),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return Effect.gen(function* () {
									yield* ctx.toast.show("error", `Failed to attach: ${msg}`)
									yield* ctx.imageAttachment.setAttaching(false)
								})
							}),
						)
					}
					return true
				}
				if (key === "backspace") {
					if (state.pathInput.length > 0) {
						yield* ctx.imageAttachment.setPathInput(state.pathInput.slice(0, -1))
					}
					return true
				}
				// Single printable character
				if (key.length === 1 && !key.startsWith("C-")) {
					yield* ctx.imageAttachment.setPathInput(state.pathInput + key)
					return true
				}
				return true // Consume all keys in path mode
			}

			// Menu mode
			if (key === "p" || key === "v") {
				// Paste from clipboard
				if (!state.isAttaching) {
					const hasClipboard = yield* ctx.imageAttachment.hasClipboardSupport()
					if (hasClipboard) {
						yield* Effect.logDebug("ImageAttach: initiating clipboard paste", {
							taskId: overlayTaskId,
						})
						yield* ctx.imageAttachment.setAttaching(true)
						yield* ctx.imageAttachment.attachFromClipboard(overlayTaskId).pipe(
							Effect.tap((attachment) =>
								ctx.toast.show("success", `Image attached: ${attachment.filename}`),
							),
							Effect.tap(() => ctx.imageAttachment.closeOverlay()),
							Effect.tap(() => ctx.overlay.pop()),
							Effect.catchAll((error) => {
								const msg =
									error && typeof error === "object" && "message" in error
										? String(error.message)
										: String(error)
								return Effect.gen(function* () {
									yield* ctx.toast.show("error", `Clipboard: ${msg}`)
									yield* ctx.imageAttachment.setAttaching(false)
								})
							}),
						)
					} else {
						// No clipboard tool available - show user feedback instead of silent failure
						yield* Effect.logWarning("ImageAttach: clipboard support not available", {
							platform: process.platform,
						})
						yield* ctx.toast.show(
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
				yield* ctx.imageAttachment.enterPathMode()
				return true
			}

			return true // Consume all keys in overlay
		}),

	/**
	 * Compute jump labels for all visible tasks
	 *
	 * Generates 2-character labels (aa, ab, ac, ...) for each task on the board.
	 * Used by the goto-jump mode for quick navigation.
	 */
	computeJumpLabels: (): Effect.Effect<Record.ReadonlyRecord<string, JumpTarget>> =>
		Effect.gen(function* () {
			const tasksByColumn = yield* ctx.board.getTasksByColumn()

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
		}),

	/**
	 * Get effective mode for keybinding matching
	 *
	 * Maps EditorService mode + overlay state to KeyMode.
	 * Overlay takes precedence over any editor mode.
	 */
	getEffectiveMode: (): Effect.Effect<KeyMode> =>
		Effect.gen(function* () {
			const hasOverlay = yield* ctx.overlay.isOpen()
			if (hasOverlay) return "overlay"

			const mode = yield* ctx.editor.getMode()
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
		}),
})

export type InputHandlers = ReturnType<typeof createInputHandlers>
