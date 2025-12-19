/**
 * Overlay and Toast Atoms
 *
 * Handles overlay stack management and toast notifications.
 */

import type { CommandExecutor } from "@effect/platform"
import { Atom, Result } from "@effect-atom/atom"
import { Effect } from "effect"
import { OverlayService } from "../../services/OverlayService.js"
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
			| { readonly _tag: "projectSelector" },
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
 * Detail scroll command atom - subscribes to scroll commands for the detail panel
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
