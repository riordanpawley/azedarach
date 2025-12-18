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
import { AppConfig, type ResolvedConfig } from "../config/index"
import { AttachmentService } from "../core/AttachmentService"
import { BeadsClient } from "../core/BeadsClient"
import { BeadEditorService } from "../core/EditorService"
import { ImageAttachmentService } from "../core/ImageAttachmentService"
import { PRWorkflow } from "../core/PRWorkflow"
import { SessionManager } from "../core/SessionManager"
import { TmuxService } from "../core/TmuxService"
import { VCService } from "../core/VCService"
import { BoardService } from "./BoardService"
import { CommandQueueService } from "./CommandQueueService"
import { EditorService } from "./EditorService"
// Import handler modules
import { createDefaultBindings } from "./keyboard/bindings"
import {
	createCheckBusy,
	createGetColumnIndex,
	createGetProjectPath,
	createGetSelectedTask,
	createOpenCurrentDetail,
	createShowErrorToast,
	createToggleCurrentSelection,
	createWithQueue,
} from "./keyboard/helpers"
import { createInputHandlers } from "./keyboard/inputHandlers"
import { createPRHandlers } from "./keyboard/prHandlers"
import { createSessionHandlers } from "./keyboard/sessionHandlers"
import { createTaskHandlers } from "./keyboard/taskHandlers"
import type { HandlerContext, Keybinding, KeyMode } from "./keyboard/types"
import { NavigationService } from "./NavigationService"
import { OverlayService } from "./OverlayService"
import { ProjectService } from "./ProjectService"
import { ToastService } from "./ToastService"
import { ViewService } from "./ViewService"

// Re-export types for backwards compatibility
export type { Keybinding, KeybindingDeps, KeyMode } from "./keyboard/types"

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
		ProjectService.Default,
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
		const projectService = yield* ProjectService
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
		const getProjectPath = createGetProjectPath(projectService)

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
			projectService,

			// Config
			resolvedConfig,

			// Pre-bound helpers
			getSelectedTask,
			getColumnIndex,
			getProjectPath,
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

					// Check for detail overlay (handles attachment navigation)
					const handledAsDetail = yield* inputHandlers.handleDetailOverlayInput(key)
					if (handledAsDetail) return

					// Check for projectSelector overlay (handles number key selection)
					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag === "projectSelector") {
						const handledAsProjectSelector = yield* inputHandlers.handleProjectSelectorInput(key)
						if (handledAsProjectSelector) return
					}

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
