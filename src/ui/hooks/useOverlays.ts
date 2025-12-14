/**
 * useOverlays - Hook for overlay stack management
 *
 * Wraps OverlayService atoms for convenient React usage.
 * Manages help, detail, create, and confirm overlays.
 */

import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import { useCallback, useMemo } from "react"
import { Effect } from "effect"
import { currentOverlayAtom, popOverlayAtom, pushOverlayAtom } from "../atoms"

export type OverlayType =
	| { readonly _tag: "help" }
	| { readonly _tag: "detail"; readonly taskId: string }
	| { readonly _tag: "create" }
	| { readonly _tag: "settings" }
	| {
			readonly _tag: "confirm"
			readonly message: string
			readonly onConfirm: Effect.Effect<void>
	  }

/**
 * Hook for managing overlay stack
 *
 * @example
 * ```tsx
 * const { currentOverlay, showHelp, showDetail, showCreate, dismiss } = useOverlays()
 *
 * // Show help overlay
 * showHelp()
 *
 * // Show detail panel for a task
 * showDetail(taskId)
 *
 * // Dismiss current overlay
 * dismiss()
 * ```
 */
export function useOverlays() {
	// currentOverlayAtom is now a derived plain value (not Result-wrapped)
	const currentOverlay = useAtomValue(currentOverlayAtom)
	const [, push] = useAtom(pushOverlayAtom, { mode: "promise" })
	const [, pop] = useAtom(popOverlayAtom, { mode: "promise" })

	// Actions (memoized)
	const actions = useMemo(
		() => ({
			showHelp: () => {
				push({ _tag: "help" }).catch(console.error)
			},

			showDetail: (taskId: string) => {
				push({ _tag: "detail", taskId }).catch(console.error)
			},

			showCreate: () => {
				push({ _tag: "create" }).catch(console.error)
			},

			showSettings: () => {
				push({ _tag: "settings" }).catch(console.error)
			},

			showConfirm: (message: string, onConfirm: Effect.Effect<void>) => {
				push({ _tag: "confirm", message, onConfirm }).catch(console.error)
			},

			dismiss: () => {
				pop().catch(console.error)
			},
		}),
		[push, pop],
	)

	// Convenience booleans for common checks (memoized)
	const flags = useMemo(
		() => ({
			showingHelp: currentOverlay?._tag === "help",
			showingDetail: currentOverlay?._tag === "detail",
			showingCreate: currentOverlay?._tag === "create",
			showingSettings: currentOverlay?._tag === "settings",
			showingConfirm: currentOverlay?._tag === "confirm",
		}),
		[currentOverlay],
	)

	return {
		currentOverlay,
		...actions,
		...flags,
	}
}
