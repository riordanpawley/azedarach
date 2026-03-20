import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, SubscriptionRef } from "effect"
import { ImageAttachmentService } from "../utils/legacyRuntimeServices.js"

type AnyEffect = Effect.Effect<void, never, CommandExecutor.CommandExecutor>
const emptyArray = <A>(): ReadonlyArray<A> => []

export interface ScrollCommand {
	readonly target: "detail" | "diagnostics"
	readonly type: "line" | "halfPage"
	readonly amount: number
	readonly timestamp: number
}

export type Overlay =
	| { readonly _tag: "help" }
	| { readonly _tag: "detail"; readonly taskId: string }
	| {
			readonly _tag: "create"
			readonly title?: string
			readonly initial?: {
				readonly title?: string
				readonly type?: string
				readonly priority?: number
				readonly implementations?: readonly string[]
			}
			readonly lockType?: boolean
			readonly context?:
				| {
						readonly _tag: "forkChild"
						readonly parentEpicId: string
						readonly sourceTaskId: string
				  }
				| { readonly _tag: "forkEpic"; readonly sourceTaskId: string }
	  }
	| { readonly _tag: "aiCreate" }
	| { readonly _tag: "settings" }
	| { readonly _tag: "imageAttach"; readonly taskId: string }
	| { readonly _tag: "imagePreview"; readonly taskId: string }
	| {
			readonly _tag: "fork"
			readonly sourceTaskId: string
			readonly sourceTaskTitle: string
			readonly parentEpicId?: string
			readonly blockedReason?: string
	  }
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
	| { readonly _tag: "waitingSessionPicker" }
	| { readonly _tag: "diffViewer"; readonly worktreePath: string; readonly baseBranch: string }
	| { readonly _tag: "devServerMenu"; readonly issueId: string }
	| { readonly _tag: "planning" }

export const shouldResetScrollCommandOnPush = (overlay: Overlay): boolean =>
	overlay._tag === "detail" || overlay._tag === "diagnostics"

export class OverlayService extends Effect.Service<OverlayService>()("OverlayService", {
	dependencies: [ImageAttachmentService.Default],
	effect: Effect.gen(function* () {
		const imageAttachment = yield* ImageAttachmentService
		const stack = yield* SubscriptionRef.make<ReadonlyArray<Overlay>>(emptyArray())
		const scrollCommand = yield* SubscriptionRef.make<ScrollCommand | null>(null)

		return {
			stack,
			scrollCommand,
			scroll: (
				type: "line" | "halfPage",
				amount: number,
				target: "detail" | "diagnostics" = "detail",
			) =>
				SubscriptionRef.set(scrollCommand, {
					target,
					type,
					amount,
					timestamp: Date.now(),
				}),
			push: (overlay: Overlay) =>
				Effect.gen(function* () {
					if (shouldResetScrollCommandOnPush(overlay)) {
						yield* SubscriptionRef.set(scrollCommand, null)
					}
					yield* SubscriptionRef.update(stack, (currentStack) => [
						...currentStack,
						Data.struct(overlay),
					])

					if (overlay._tag === "detail") {
						yield* imageAttachment
							.loadForTask(overlay.taskId)
							.pipe(Effect.catchAll(Effect.logError))
					}
					if (overlay._tag === "imageAttach") {
						yield* imageAttachment.openOverlay(overlay.taskId)
					}
				}),
			pop: () =>
				Effect.gen(function* () {
					const popped = yield* SubscriptionRef.modify(stack, (currentStack) => {
						if (currentStack.length === 0) return [undefined, currentStack] as const
						return [currentStack[currentStack.length - 1], currentStack.slice(0, -1)] as const
					})

					if (popped?._tag === "detail") {
						yield* imageAttachment.clearCurrent()
					}
					if (popped?._tag === "imageAttach") {
						yield* imageAttachment.closeOverlay()
					}
					return popped
				}),
			clear: () => SubscriptionRef.set(stack, emptyArray()),
			current: () =>
				SubscriptionRef.get(stack).pipe(
					Effect.map((currentStack) =>
						currentStack.length > 0 ? currentStack[currentStack.length - 1] : undefined,
					),
				),
			isOpen: () =>
				SubscriptionRef.get(stack).pipe(Effect.map((currentStack) => currentStack.length > 0)),
		}
	}),
}) {}
