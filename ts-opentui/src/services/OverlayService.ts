// src/services/OverlayService.ts

import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, SubscriptionRef } from "effect"
import { ImageAttachmentService } from "../core/ImageAttachmentService.js"
import { emptyArray } from "../lib/empty.js"

// onConfirm effects require CommandExecutor (exception to no-leaking-requirements rule)
type AnyEffect = Effect.Effect<void, never, CommandExecutor.CommandExecutor>

/**
 * Scroll command for detail panel scrolling.
 * Each emission triggers a scroll action.
 */
export interface ScrollCommand {
	readonly type: "line" | "halfPage"
	readonly amount: number // positive = down, negative = up
	readonly timestamp: number // unique per command
}

export type Overlay =
	| { readonly _tag: "help" }
	| { readonly _tag: "detail"; readonly taskId: string }
	| { readonly _tag: "create" }
	| { readonly _tag: "claudeCreate" }
	| { readonly _tag: "settings" }
	| { readonly _tag: "imageAttach"; readonly taskId: string }
	| { readonly _tag: "imagePreview"; readonly taskId: string }
	| { readonly _tag: "confirm"; readonly message: string; readonly onConfirm: AnyEffect }
	| {
			readonly _tag: "gitPull"
			readonly commitsBehind: number
			readonly baseBranch: string
			readonly remote: string
			readonly onConfirm: AnyEffect
	  }
	| {
			readonly _tag: "mergeChoice"
			readonly message: string
			readonly commitsBehind: number
			readonly baseBranch: string
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

export class OverlayService extends Effect.Service<OverlayService>()("OverlayService", {
	dependencies: [ImageAttachmentService.Default],
	effect: Effect.gen(function* () {
		const imageAttachment = yield* ImageAttachmentService

		// SubscriptionRef for reactive overlay stack
		const stack = yield* SubscriptionRef.make<ReadonlyArray<Overlay>>(emptyArray())

		// Scroll command stream for detail panel - each emission triggers a scroll
		const scrollCommand = yield* SubscriptionRef.make<ScrollCommand | null>(null)

		return {
			// Expose SubscriptionRef for atom subscription
			stack,

			// Scroll command ref for detail panel
			scrollCommand,

			/**
			 * Emit a scroll command for the detail panel
			 */
			scroll: (type: "line" | "halfPage", amount: number) =>
				SubscriptionRef.set(scrollCommand, {
					type,
					amount,
					timestamp: Date.now(),
				}),

			push: (overlay: Overlay) =>
				Effect.gen(function* () {
					yield* SubscriptionRef.update(stack, (s) => [...s, Data.struct(overlay)])

					// Load attachments when opening detail overlay
					// Errors are logged but don't prevent overlay from opening
					if (overlay._tag === "detail") {
						yield* imageAttachment
							.loadForTask(overlay.taskId)
							.pipe(Effect.catchAll(Effect.logError))
					}

					// Initialize overlay state when opening imageAttach overlay
					if (overlay._tag === "imageAttach") {
						yield* imageAttachment.openOverlay(overlay.taskId)
					}
				}),

			pop: () =>
				Effect.gen(function* () {
					const popped = yield* SubscriptionRef.modify(stack, (s) => {
						if (s.length === 0) return [undefined, s]
						return [s[s.length - 1], s.slice(0, -1)]
					})

					// Clear attachments when closing detail overlay
					if (popped?._tag === "detail") {
						yield* imageAttachment.clearCurrent()
					}

					// Close overlay state when closing imageAttach overlay
					if (popped?._tag === "imageAttach") {
						yield* imageAttachment.closeOverlay()
					}

					return popped
				}),

			clear: () => SubscriptionRef.set(stack, emptyArray()),

			current: () =>
				SubscriptionRef.get(stack).pipe(
					Effect.map((s) => (s.length > 0 ? s[s.length - 1] : undefined)),
				),

			isOpen: () => SubscriptionRef.get(stack).pipe(Effect.map((s) => s.length > 0)),
		}
	}),
}) {}
