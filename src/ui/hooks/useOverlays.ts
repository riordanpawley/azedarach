/**
 * useOverlays - Hook for overlay stack management
 *
 * Wraps OverlayService atoms for convenient React usage.
 * Manages help, detail, create, and confirm overlays.
 */

import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import type { Effect } from "effect"
import { useMemo } from "react"
import { currentOverlayAtom, popOverlayAtom, pushOverlayAtom } from "../atoms"

export type OverlayType =
	| { readonly _tag: "help" }
	| { readonly _tag: "detail"; readonly taskId: string }
	| { readonly _tag: "create" }
	| { readonly _tag: "claudeCreate" }
	| { readonly _tag: "settings" }
	| { readonly _tag: "imageAttach"; readonly taskId: string }
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

	// Actions (memoized) - errors are logged in Effect layer
	const actions = useMemo(
		() => ({
			showHelp: () => {
				push({ _tag: "help" })
			},

			showDetail: (taskId: string) => {
				push({ _tag: "detail", taskId })
			},

			showCreate: () => {
				push({ _tag: "create" })
			},

			showClaudeCreate: () => {
				push({ _tag: "claudeCreate" })
			},

			showSettings: () => {
				push({ _tag: "settings" })
			},

			showConfirm: (message: string, onConfirm: Effect.Effect<void>) => {
				push({ _tag: "confirm", message, onConfirm })
			},

			showImageAttach: (taskId: string) => {
				push({ _tag: "imageAttach", taskId })
			},

			dismiss: () => {
				pop()
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
			showingClaudeCreate: currentOverlay?._tag === "claudeCreate",
			showingSettings: currentOverlay?._tag === "settings",
			showingConfirm: currentOverlay?._tag === "confirm",
			showingImageAttach: currentOverlay?._tag === "imageAttach",
		}),
		[currentOverlay],
	)

	return {
		currentOverlay,
		...actions,
		...flags,
	}
}
