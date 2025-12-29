/**
 * Overlay and Toast Atoms
 *
 * Handles overlay stack management and toast notifications.
 */

import type { CommandExecutor } from "@effect/platform"
import { Atom, Result } from "@effect-atom/atom"
import { Effect } from "effect"
import { OverlayService } from "../../services/OverlayService.js"
import { SettingsService } from "../../services/SettingsService.js"
import { ToastService } from "../../services/ToastService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Toast Atoms
// ============================================================================

/**
 * Toast notifications atom - subscribes to ToastService toasts changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const toasts = useAtomValue(toastsAtom)
 */
export const toastsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const toast = yield* ToastService
		return toast.toasts
	}),
)

/**
 * Show toast atom - display a toast notification
 *
 * Usage: const [, showToast] = useAtom(showToastAtom, { mode: "promise" })
 *        await showToast({ type: "success", message: "Task completed!" })
 */
export const showToastAtom = appRuntime.fn(
	({ type, message }: { type: "success" | "error" | "info" | "warning"; message: string }) =>
		Effect.gen(function* () {
			const toast = yield* ToastService
			yield* toast.show(type, message)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Dismiss toast atom - remove a toast by ID
 *
 * Usage: const [, dismissToast] = useAtom(dismissToastAtom, { mode: "promise" })
 *        await dismissToast(toastId)
 */
export const dismissToastAtom = appRuntime.fn((toastId: string) =>
	Effect.gen(function* () {
		const toast = yield* ToastService
		yield* toast.dismiss(toastId)
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Overlay Atoms
// ============================================================================

/**
 * Overlay stack atom - subscribes to OverlayService stack changes
 *
 * Uses appRuntime.subscriptionRef() for automatic reactive updates.
 *
 * Usage: const overlays = useAtomValue(overlaysAtom)
 */
export const overlaysAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const overlay = yield* OverlayService
		return overlay.stack
	}),
)

/**
 * Current overlay atom - the top of the overlay stack
 *
 * Derived from overlaysAtom for automatic reactivity.
 *
 * Usage: const currentOverlay = useAtomValue(currentOverlayAtom)
 */
export const currentOverlayAtom = Atom.readable((get) => {
	const overlaysResult = get(overlaysAtom)
	if (!Result.isSuccess(overlaysResult)) return undefined
	const overlays = overlaysResult.value
	return overlays.length > 0 ? overlays[overlays.length - 1] : undefined
})

/**
 * Push overlay atom - add overlay to stack
 *
 * Usage: const [, pushOverlay] = useAtom(pushOverlayAtom, { mode: "promise" })
 *        await pushOverlay({ _tag: "help" })
 */
export const pushOverlayAtom = appRuntime.fn(
	(
		overlay:
			| { readonly _tag: "help" }
			| { readonly _tag: "detail"; readonly taskId: string }
			| { readonly _tag: "create" }
			| { readonly _tag: "claudeCreate" }
			| { readonly _tag: "settings" }
			| { readonly _tag: "imageAttach"; readonly taskId: string }
			| { readonly _tag: "imagePreview"; readonly taskId: string }
			| {
					readonly _tag: "confirm"
					readonly message: string
					// Exception: CommandExecutor is the only allowed leaked requirement
					readonly onConfirm: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
			  }
			| { readonly _tag: "diagnostics" }
			| { readonly _tag: "projectSelector" }
			| { readonly _tag: "diffViewer"; readonly worktreePath: string; readonly baseBranch: string }
			| {
					readonly _tag: "devServerMenu"
					readonly beadId: string
			  }
			| { readonly _tag: "planning" },
	) =>
		Effect.gen(function* () {
			const overlayService = yield* OverlayService
			// OverlayService.push() now handles attachment loading for detail/imageAttach overlays
			yield* overlayService.push(overlay)
		}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pop overlay atom - remove top overlay from stack
 *
 * Usage: const [, popOverlay] = useAtom(popOverlayAtom, { mode: "promise" })
 *        await popOverlay()
 */
export const popOverlayAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const overlayService = yield* OverlayService
		// OverlayService.pop() now handles clearing attachments for detail/imageAttach overlays
		yield* overlayService.pop()
	}).pipe(Effect.catchAll(Effect.logError)),
)

// ============================================================================
// Scroll Atoms
// ============================================================================

/**
 * Detail scroll command atom - subscribes to scroll commands for detail panel
 *
 * Each emission triggers a scroll action in the DetailPanel component.
 * The timestamp ensures each command is unique and triggers useEffect.
 *
 * Usage: const scrollCommand = useAtomValue(detailScrollAtom)
 */
export const detailScrollAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const overlay = yield* OverlayService
		return overlay.scrollCommand
	}),
)

// ============================================================================
// Settings Overlay Atoms
// ============================================================================

/**
 * Settings state atom - subscribes to SettingsService state
 *
 * Provides focus index and isOpen state for the settings overlay.
 *
 * Usage: const settingsState = useAtomValue(settingsStateAtom)
 */
export const settingsStateAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const settings = yield* SettingsService
		return settings.state
	}),
)

/**
 * Open settings atom - open the settings overlay
 *
 * Usage: const [, openSettings] = useAtom(openSettingsAtom, { mode: "promise" })
 *        await openSettings()
 */
export const openSettingsAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const settings = yield* SettingsService
		yield* settings.open()
	}),
)

/**
 * Close settings atom - close the settings overlay
 *
 * Usage: const [, closeSettings] = useAtom(closeSettingsAtom, { mode: "promise" })
 *        await closeSettings()
 */
export const closeSettingsAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const settings = yield* SettingsService
		yield* settings.close()
	}),
)

/**
 * Move up in settings atom - move focus to previous setting
 *
 * Usage: const [, moveUpSettings] = useAtom(moveUpSettingsAtom, { mode: "promise" })
 *        await moveUpSettings()
 */
export const moveUpSettingsAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const settings = yield* SettingsService
		yield* settings.moveUp()
	}),
)

/**
 * Move down in settings atom - move focus to next setting
 *
 * Usage: const [, moveDownSettings] = useAtom(moveDownSettingsAtom, { mode: "promise" })
 *        await moveDownSettings()
 */
export const moveDownSettingsAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const settings = yield* SettingsService
		yield* settings.moveDown()
	}),
)

/**
 * Toggle current setting atom - toggle the value of the currently focused setting
 *
 * Usage: const [, toggleCurrentSetting] = useAtom(toggleCurrentSettingAtom, { mode: "promise" })
 *        await toggleCurrentSetting()
 */
export const toggleCurrentSettingAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const settings = yield* SettingsService
		yield* settings.toggleCurrent()
	}),
)

/**
 * Open settings in editor atom - open .azedarach.json in $EDITOR
 *
 * Returns configPath and backupContent for post-edit validation.
 *
 * Usage: const [, openEditor] = useAtom(openSettingsEditorAtom, { mode: "promise" })
 *        const result = await openEditor()
 */
export const openSettingsEditorAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const settings = yield* SettingsService
		return yield* settings.openInEditor()
	}),
)

/**
 * Validate settings after edit atom - validate config after external editor closes
 *
 * Rolls back to backup if validation fails.
 *
 * Usage: const [, validateAfterEdit] = useAtom(validateSettingsAfterEditAtom, { mode: "promise" })
 *        const result = await validateAfterEdit({ configPath, backupContent })
 */
export const validateSettingsAfterEditAtom = appRuntime.fn(
	({
		configPath,
		backupContent,
	}: {
		readonly configPath: string
		readonly backupContent: string
	}) =>
		Effect.gen(function* () {
			const settings = yield* SettingsService
			return yield* settings.validateAfterEdit(configPath, backupContent)
		}),
)
