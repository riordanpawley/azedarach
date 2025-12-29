/**
 * KeyboardService - Data-driven keyboard handler in Effect-land
 *
 * Manages keyboard bindings using a data-driven approach with Effect.Service pattern.
 * Keybindings are stored as data structures with associated Effect actions,
 * allowing for dynamic registration, mode-based filtering, and overlay precedence.
 *
 * This service handles ALL keyboard input for the application, delegating
 * to domain-specific handler services for complex actions.
 */

import { Effect, Ref } from "effect"
import { BeadsClient } from "../core/BeadsClient.js"
import { TmuxService } from "../core/TmuxService.js"
import { BoardService } from "./BoardService.js"
import { EditorService } from "./EditorService.js"
import { createDefaultBindings } from "./keyboard/bindings.js"
import { DevServerHandlersService } from "./keyboard/DevServerHandlersService.js"
import { InputHandlersService } from "./keyboard/InputHandlersService.js"
import { KeyboardHelpersService } from "./keyboard/KeyboardHelpersService.js"
import { OrchestrateHandlersService } from "./keyboard/OrchestrateHandlersService.js"
import { PRHandlersService } from "./keyboard/PRHandlersService.js"
import { SessionHandlersService } from "./keyboard/SessionHandlersService.js"
import { TaskHandlersService } from "./keyboard/TaskHandlersService.js"
import type { Keybinding, KeyMode } from "./keyboard/types.js"
import { NavigationService } from "./NavigationService.js"
import { OverlayService } from "./OverlayService.js"
import { SettingsService } from "./SettingsService.js"
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
		// Handler services (each brings its own deps)
		KeyboardHelpersService.Default,
		SessionHandlersService.Default,
		TaskHandlersService.Default,
		PRHandlersService.Default,
		InputHandlersService.Default,
		OrchestrateHandlersService.Default,
		DevServerHandlersService.Default,
		// Core services for direct binding access
		ToastService.Default,
		OverlayService.Default,
		SettingsService.Default,
		NavigationService.Default,
		EditorService.Default,
		ViewService.Default,
		TmuxService.Default,
		BeadsClient.Default,
		BoardService.Default,
	],

	effect: Effect.gen(function* () {
		// ====================================================================
		// Inject handler services
		// ====================================================================
		const helpers = yield* KeyboardHelpersService
		const sessionHandlers = yield* SessionHandlersService
		const taskHandlers = yield* TaskHandlersService
		const prHandlers = yield* PRHandlersService
		const inputHandlers = yield* InputHandlersService
		const orchestrateHandlers = yield* OrchestrateHandlersService
		const devServerHandlers = yield* DevServerHandlersService

		// ====================================================================
		// Inject core services for direct binding access
		// ====================================================================
		const toast = yield* ToastService
		const overlay = yield* OverlayService
		const settings = yield* SettingsService
		const nav = yield* NavigationService
		const editor = yield* EditorService
		const viewService = yield* ViewService
		const tmux = yield* TmuxService
		const beadsClient = yield* BeadsClient
		const board = yield* BoardService

		// ====================================================================
		// Create default keybindings
		// ====================================================================
		const defaultBindings = createDefaultBindings({
			// Handler services
			sessionHandlers,
			taskHandlers,
			prHandlers,
			inputHandlers,
			orchestrateHandlers,
			devServerHandlers,
			helpers,
			// Core services for direct bindings
			nav,
			editor,
			overlay,
			settings,
			toast,
			viewService,
			tmux,
			beadsClient,
			board,
		})

		const keybindings = yield* Ref.make<ReadonlyArray<Keybinding>>(defaultBindings)

		// ====================================================================
		// Keybinding lookup
		// ====================================================================

		/**
		 * Find matching keybinding for key and mode
		 *
		 * Priority:
		 * 1. Exact single mode match (mode === effectiveMode)
		 * 2. Array mode match (mode includes effectiveMode)
		 * 3. Wildcard "*" match
		 */
		const findBinding = (
			key: string,
			effectiveMode: KeyMode,
		): Effect.Effect<Keybinding | undefined> =>
			Effect.gen(function* () {
				const bindings = yield* Ref.get(keybindings)

				// First try exact single mode match (highest priority)
				const exactMatch = bindings.find(
					(b) => b.key === key && !Array.isArray(b.mode) && b.mode === effectiveMode,
				)
				if (exactMatch) return exactMatch

				// Then try array mode match
				const arrayMatch = bindings.find(
					(b) => b.key === key && Array.isArray(b.mode) && b.mode.includes(effectiveMode),
				)
				if (arrayMatch) return arrayMatch

				// Finally try wildcard
				return bindings.find((b) => b.key === key && b.mode === "*")
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

					// Check for mergeChoice overlay (handles its own keys)
					const handledAsMergeChoice = yield* inputHandlers.handleMergeChoiceInput(key)
					if (handledAsMergeChoice) return

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

					// Check for settings overlay
					if (currentOverlay?._tag === "settings") {
						const handledAsSettings = yield* inputHandlers.handleSettingsInput(key)
						if (handledAsSettings) return
					}

					// Check for imagePreview overlay
					if (currentOverlay?._tag === "imagePreview") {
						const handledAsImagePreview = yield* inputHandlers.handleImagePreviewInput(key)
						if (handledAsImagePreview) return
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
			 *
			 * For array modes, matches if the binding's mode array equals the provided mode array.
			 */
			unregister: (key: string, mode: KeyMode | ReadonlyArray<KeyMode>): Effect.Effect<void> =>
				Ref.update(keybindings, (bs) =>
					bs.filter((b) => {
						if (b.key !== key) return true
						// Compare modes - handle arrays
						if (Array.isArray(b.mode) && Array.isArray(mode)) {
							return JSON.stringify(b.mode) !== JSON.stringify(mode)
						}
						return b.mode !== mode
					}),
				),

			/**
			 * Get all registered keybindings
			 */
			getBindings: (): Effect.Effect<ReadonlyArray<Keybinding>> => Ref.get(keybindings),
		}
	}),
}) {}
