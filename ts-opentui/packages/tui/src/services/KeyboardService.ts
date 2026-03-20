import { Effect, Ref } from "effect"
import { detectTmuxCapabilities } from "../utils/tmuxCapabilities.js"
import { EditorService } from "./EditorService.js"
import { NavigationService } from "./NavigationService.js"
import { OverlayService } from "./OverlayService.js"
import { SettingsService } from "./SettingsService.js"
import { ToastService } from "./ToastService.js"
import { TmuxService } from "./TmuxService.js"
import { TuiBoardStoreService } from "./TuiBoardStoreService.js"
import { TuiIssueAdapterService } from "./TuiIssueAdapterService.js"
import { ViewService } from "./ViewService.js"
import { createDefaultBindings } from "./keyboard/bindings.js"
import { DevServerHandlersService } from "./keyboard/DevServerHandlersService.js"
import { InputHandlersService } from "./keyboard/InputHandlersService.js"
import { KeyboardHelpersService } from "./keyboard/KeyboardHelpersService.js"
import { OrchestrateHandlersService } from "./keyboard/OrchestrateHandlersService.js"
import { PRHandlersService } from "./keyboard/PRHandlersService.js"
import { SessionHandlersService } from "./keyboard/SessionHandlersService.js"
import { TaskHandlersService } from "./keyboard/TaskHandlersService.js"
import type { Keybinding, KeyMode } from "./keyboard/types.js"

export type { Keybinding, KeyMode } from "./keyboard/types.js"

export class KeyboardService extends Effect.Service<KeyboardService>()("KeyboardService", {
	dependencies: [
		KeyboardHelpersService.Default,
		SessionHandlersService.Default,
		TaskHandlersService.Default,
		PRHandlersService.Default,
		InputHandlersService.Default,
		OrchestrateHandlersService.Default,
		DevServerHandlersService.Default,
		TuiIssueAdapterService.Default,
		ToastService.Default,
		OverlayService.Default,
		SettingsService.Default,
		NavigationService.Default,
		EditorService.Default,
		ViewService.Default,
		TmuxService.Default,
		TuiBoardStoreService.Default,
	],
	effect: Effect.gen(function* () {
		const helpers = yield* KeyboardHelpersService
		const sessionHandlers = yield* SessionHandlersService
		const taskHandlers = yield* TaskHandlersService
		const prHandlers = yield* PRHandlersService
		const inputHandlers = yield* InputHandlersService
		const orchestrateHandlers = yield* OrchestrateHandlersService
		const devServerHandlers = yield* DevServerHandlersService
		const issueAdapter = yield* TuiIssueAdapterService
		const toast = yield* ToastService
		const overlay = yield* OverlayService
		const settings = yield* SettingsService
		const nav = yield* NavigationService
		const editor = yield* EditorService
		const viewService = yield* ViewService
		const tmux = yield* TmuxService
		const board = yield* TuiBoardStoreService
		const tmuxCapabilities = detectTmuxCapabilities()

		const defaultBindings = createDefaultBindings({
			sessionHandlers,
			taskHandlers,
			prHandlers,
			inputHandlers,
			orchestrateHandlers,
			devServerHandlers,
			helpers,
			issueAdapter,
			nav,
			editor,
			overlay,
			settings,
			toast,
			viewService,
			tmux,
			tmuxCapabilities,
			board,
		})

		const keybindings = yield* Ref.make<ReadonlyArray<Keybinding>>(defaultBindings)

		const findBinding = (key: string, effectiveMode: KeyMode): Effect.Effect<Keybinding | undefined> =>
			Effect.gen(function* () {
				const bindings = yield* Ref.get(keybindings)
				const exactMatch = bindings.find(
					(binding) =>
						binding.key === key &&
						!Array.isArray(binding.mode) &&
						binding.mode === effectiveMode,
				)
				if (exactMatch) return exactMatch

				const arrayMatch = bindings.find(
					(binding) =>
						binding.key === key &&
						Array.isArray(binding.mode) &&
						binding.mode.includes(effectiveMode),
				)
				if (arrayMatch) return arrayMatch

				return bindings.find((binding) => binding.key === key && binding.mode === "*")
			})

		return {
			keybindings,
			handleKey: (key: string) =>
				Effect.gen(function* () {
					const handledAsConfirm = yield* inputHandlers.handleConfirmInput(key)
					if (handledAsConfirm) return

					const handledAsMergeChoice = yield* inputHandlers.handleMergeChoiceInput(key)
					if (handledAsMergeChoice) return

					const handledAsFork = yield* inputHandlers.handleForkInput(key)
					if (handledAsFork) return

					const handledAsBulkCleanup = yield* inputHandlers.handleBulkCleanupInput(key)
					if (handledAsBulkCleanup) return

					const handledAsImageAttach = yield* inputHandlers.handleImageAttachInput(key)
					if (handledAsImageAttach) return

					const handledAsDetail = yield* inputHandlers.handleDetailOverlayInput(key)
					if (handledAsDetail) return

					const handledAsDiagnostics = yield* inputHandlers.handleDiagnosticsOverlayInput(key)
					if (handledAsDiagnostics) return

					const currentOverlay = yield* overlay.current()
					if (currentOverlay?._tag === "projectSelector") {
						const handledAsProjectSelector = yield* inputHandlers.handleProjectSelectorInput(key)
						if (handledAsProjectSelector) return
					}

					if (currentOverlay?._tag === "waitingSessionPicker") {
						const handledAsWaitingSessionPicker =
							yield* inputHandlers.handleWaitingSessionPickerInput(key)
						if (handledAsWaitingSessionPicker) return
					}

					if (currentOverlay?._tag === "settings") {
						const handledAsSettings = yield* inputHandlers.handleSettingsInput(key)
						if (handledAsSettings) return
					}

					if (currentOverlay?._tag === "imagePreview") {
						const handledAsImagePreview = yield* inputHandlers.handleImagePreviewInput(key)
						if (handledAsImagePreview) return
					}

					const effectiveMode = yield* inputHandlers.getEffectiveMode()
					if (effectiveMode === "goto-jump") {
						yield* inputHandlers.handleJumpInput(key)
						return
					}

					const handledAsText = yield* inputHandlers.handleTextInput(key)
					if (handledAsText) return

					const binding = yield* findBinding(key, effectiveMode)
					if (binding !== undefined) {
						yield* binding.action
					}
				}),
			register: (binding: Keybinding): Effect.Effect<void> =>
				Ref.update(keybindings, (bindings) => [...bindings, binding]),
			unregister: (key: string, mode: KeyMode | ReadonlyArray<KeyMode>): Effect.Effect<void> =>
				Ref.update(keybindings, (bindings) =>
					bindings.filter((binding) => {
						if (binding.key !== key) return true
						if (Array.isArray(binding.mode) && Array.isArray(mode)) {
							return JSON.stringify(binding.mode) !== JSON.stringify(mode)
						}
						return binding.mode !== mode
					}),
				),
			getBindings: (): Effect.Effect<ReadonlyArray<Keybinding>> => Ref.get(keybindings),
		}
	}),
}) {}
