/**
 * useOverlays - Hook for overlay stack management
 *
 * Wraps OverlayService atoms for convenient React usage.
 * Manages help, detail, create, and confirm overlays.
 */

import type { CommandExecutor } from "@effect/platform"
import { useAtom, useAtomValue } from "@effect-atom/atom-react"
import type { Effect } from "effect"
import { useMemo } from "react"
import { currentOverlayAtom, popOverlayAtom, pushOverlayAtom } from "../atoms.js"

// onConfirm effects require CommandExecutor (exception to no-leaking-requirements rule)
type AnyEffect = Effect.Effect<void, never, CommandExecutor.CommandExecutor>

export type OverlayType =
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
			readonly onConfirm: AnyEffect
	  }
	| {
			readonly _tag: "mergeChoice"
			readonly message: string
			readonly commitsBehind: number
			readonly onMerge: AnyEffect
			readonly onSkip: AnyEffect
	  }
	| {
			readonly _tag: "bulkCleanup"
			readonly taskIds: ReadonlyArray<string>
			readonly onWorktreeOnly: AnyEffect
			readonly onFullCleanup: AnyEffect
	  }
	| { readonly _tag: "diagnostics" }
	| { readonly _tag: "projectSelector" }
	| { readonly _tag: "diffViewer"; readonly worktreePath: string; readonly baseBranch: string }
	| { readonly _tag: "devServerMenu"; readonly beadId: string }
	| { readonly _tag: "planning" }

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

			showConfirm: (message: string, onConfirm: AnyEffect) => {
				push({ _tag: "confirm", message, onConfirm })
			},

			showImageAttach: (taskId: string) => {
				push({ _tag: "imageAttach", taskId })
			},

			showImagePreview: (taskId: string) => {
				push({ _tag: "imagePreview", taskId })
			},

			showDiagnostics: () => {
				push({ _tag: "diagnostics" })
			},

			showProjectSelector: () => {
				push({ _tag: "projectSelector" })
			},

			showDiffViewer: (worktreePath: string, baseBranch: string) => {
				push({ _tag: "diffViewer", worktreePath, baseBranch })
			},

			showDevServerMenu: (beadId: string) => {
				push({ _tag: "devServerMenu", beadId })
			},

			showPlanning: () => {
				push({ _tag: "planning" })
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
			showingMergeChoice: currentOverlay?._tag === "mergeChoice",
			showingBulkCleanup: currentOverlay?._tag === "bulkCleanup",
			showingImageAttach: currentOverlay?._tag === "imageAttach",
			showingImagePreview: currentOverlay?._tag === "imagePreview",
			showingDiagnostics: currentOverlay?._tag === "diagnostics",
			showingProjectSelector: currentOverlay?._tag === "projectSelector",
			showingDiffViewer: currentOverlay?._tag === "diffViewer",
			showingDevServerMenu: currentOverlay?._tag === "devServerMenu",
			showingPlanning: currentOverlay?._tag === "planning",
		}),
		[currentOverlay],
	)

	return {
		currentOverlay,
		...actions,
		...flags,
	}
}
