/**
 * KeyboardService - Data-driven keyboard handler in Effect-land
 *
 * Manages keyboard bindings using a data-driven approach with Effect.Service pattern.
 * Keybindings are stored as data structures with associated Effect actions,
 * allowing for dynamic registration, mode-based filtering, and overlay precedence.
 *
 * This service handles ALL keyboard input for the application, delegating
 * to domain-specific handler modules for complex actions.
 */

import { Effect, Ref } from "effect"
import { AppConfig, type ResolvedConfig } from "../config/index.js"
import { AttachmentService } from "../core/AttachmentService.js"
import { BeadsClient } from "../core/BeadsClient.js"
import { BeadEditorService } from "../core/EditorService.js"
import { ImageAttachmentService } from "../core/ImageAttachmentService.js"
import { PRWorkflow } from "../core/PRWorkflow.js"
import { SessionManager } from "../core/SessionManager.js"
import { TmuxService } from "../core/TmuxService.js"
import { VCService } from "../core/VCService.js"
import { BoardService } from "./BoardService.js"
import { CommandQueueService } from "./CommandQueueService.js"
import { EditorService } from "./EditorService.js"
// Import handler modules
import { createDefaultBindings } from "./keyboard/bindings.js"
import {
	createCheckBusy,
	createGetColumnIndex,
	createGetSelectedTask,
	createOpenCurrentDetail,
	createShowErrorToast,
	createToggleCurrentSelection,
	createWithQueue,
} from "./keyboard/helpers.js"
import { createInputHandlers } from "./keyboard/inputHandlers.js"
import { createPRHandlers } from "./keyboard/prHandlers.js"
import { createSessionHandlers } from "./keyboard/sessionHandlers.js"
import { createTaskHandlers } from "./keyboard/taskHandlers.js"
import type { HandlerContext, Keybinding, KeyMode } from "./keyboard/types.js"
import { NavigationService } from "./NavigationService.js"
import { OverlayService } from "./OverlayService.js"
import { ToastService } from "./ToastService.js"
import { ViewService } from "./ViewService.js"

// Re-export types for backwards compatibility
export type { Keybinding, KeybindingDeps, KeyMode } from "./keyboard/types.js"

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
		ImageAttachmentService.Default,
		TmuxService.Default,
		CommandQueueService.Default,
		AppConfig.Default,
	],

	effect: Effect.gen(function* () {
		// ====================================================================
		// Inject ALL services at construction time
		// ====================================================================
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
		const imageAttachment = yield* ImageAttachmentService
		const tmux = yield* TmuxService
		const commandQueue = yield* CommandQueueService
		const appConfig = yield* AppConfig
		const resolvedConfig: ResolvedConfig = appConfig.config

		// ====================================================================
		// Create shared helper functions (pre-bound to services)
		// ====================================================================
		const getSelectedTask = createGetSelectedTask(nav, board)
		const getColumnIndex = createGetColumnIndex(nav)
		const showErrorToast = createShowErrorToast(toast)
		const openCurrentDetail = createOpenCurrentDetail(overlay, getSelectedTask)
		const toggleCurrentSelection = createToggleCurrentSelection(editor, getSelectedTask)
		const withQueue = createWithQueue(commandQueue, toast)
		const checkBusy = createCheckBusy(commandQueue, toast)

		// ====================================================================
		// Build handler context
		// ====================================================================
		const ctx: HandlerContext = {
			// Services
			toast,
			overlay,
			nav,
			editor,
			board,
			sessionManager,
			attachment,
			prWorkflow,
			beadsClient,
			beadEditor,
			viewService,
			imageAttachment,
			tmux,
			vc,
			commandQueue,

			// Config
			resolvedConfig,

			// Pre-bound helpers
			getSelectedTask,
			getColumnIndex,
			showErrorToast,
			openCurrentDetail,
			toggleCurrentSelection,
			withQueue,
			checkBusy,

			// ImageAttachment state for inputHandlers
			imageAttachmentOverlayState: imageAttachment.overlayState,
		}

		// ====================================================================
		// Create domain-specific handlers
		// ====================================================================
		const sessionHandlers = createSessionHandlers(ctx)
		const taskHandlers = createTaskHandlers(ctx)
		const prHandlers = createPRHandlers(ctx)
		const inputHandlers = createInputHandlers(ctx)

		// ====================================================================
		// Create default keybindings
		// ====================================================================
		const defaultBindings = createDefaultBindings({
			sessionHandlers,
			taskHandlers,
			prHandlers,
			inputHandlers,
			ctx,
		})

		const keybindings = yield* Ref.make<ReadonlyArray<Keybinding>>(defaultBindings)

		// ====================================================================
		// Keybinding lookup
		// ====================================================================

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

		// ====================================================================
		// Public API
		// ====================================================================

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
					// Check for confirm overlay first (handles its own keys)
					const handledAsConfirm = yield* inputHandlers.handleConfirmInput(key)
					if (handledAsConfirm) return

					// Check for imageAttach overlay (handles its own keys)
					const handledAsImageAttach = yield* inputHandlers.handleImageAttachInput(key)
					if (handledAsImageAttach) return

					const effectiveMode = yield* inputHandlers.getEffectiveMode()

					// Special handling for goto-jump mode (any key is label input)
					if (effectiveMode === "goto-jump") {
						yield* inputHandlers.handleJumpInput(key)
						return
					}

					// Check for text input handling (search/command modes)
					const handledAsText = yield* inputHandlers.handleTextInput(key)
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
