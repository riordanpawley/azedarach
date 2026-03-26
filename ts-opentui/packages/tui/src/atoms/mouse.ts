/**
 * Mouse Interaction Atoms
 *
 * Encapsulates mouse-triggered behavior in Effect services so React handlers
 * can remain thin event adapters.
 */

import { Effect } from "effect"
import { EditorService } from "../../../../src/services/EditorService.js"
import { KeyboardService } from "../../../../src/services/KeyboardService.js"
import { NavigationService } from "../../../../src/services/NavigationService.js"
import { OverlayService } from "../../../../src/services/OverlayService.js"
import type { TaskWithSession } from "../types.js"
import { appRuntime } from "./runtime.js"

type MouseButtonType = "left" | "right"

export interface TaskMouseInteractionParams {
	taskId: string
	button: MouseButtonType
	/** Selected task ID from UI snapshot at click time. */
	selectedTaskId: string | undefined
}

export interface ColumnPagerInteractionParams {
	delta: -1 | 1
	columnIndex: number
	taskIndex: number
	tasksByColumn: readonly (readonly TaskWithSession[])[]
}

export const findNextNonEmptyColumnIndex = (
	tasksByColumn: readonly (readonly TaskWithSession[])[],
	columnIndex: number,
	delta: -1 | 1,
): number | undefined => {
	for (
		let candidateIndex = columnIndex + delta;
		candidateIndex >= 0 && candidateIndex < tasksByColumn.length;
		candidateIndex += delta
	) {
		if ((tasksByColumn[candidateIndex]?.length ?? 0) > 0) {
			return candidateIndex
		}
	}

	return undefined
}

const openActionPalette = Effect.gen(function* () {
	const editor = yield* EditorService
	const keyboard = yield* KeyboardService
	const mode = yield* editor.getMode()

	// Match previous behavior: preserve select mode; normalize other modes first.
	if (mode._tag !== "normal" && mode._tag !== "select") {
		yield* keyboard.handleKey("escape")
	}
	yield* keyboard.handleKey("space")
})

const openDetailFromMouse = Effect.gen(function* () {
	const editor = yield* EditorService
	const keyboard = yield* KeyboardService
	const mode = yield* editor.getMode()

	if (mode._tag !== "normal") {
		yield* keyboard.handleKey("escape")
	}
	yield* keyboard.handleKey("return")
})

/**
 * Handle task-card mouse interaction with mobile-first semantics:
 * - left tap on unselected task: focus + action palette
 * - left tap on selected task: open detail
 * - right click: focus + action palette
 */
export const handleTaskMouseInteractionAtom = appRuntime.fn((params: TaskMouseInteractionParams) =>
	Effect.gen(function* () {
		const overlay = yield* OverlayService
		const currentOverlay = yield* overlay.current()
		if (currentOverlay) return

		const nav = yield* NavigationService
		yield* nav.jumpToTask(params.taskId)

		if (params.button === "right") {
			yield* openActionPalette
			return
		}

		if (params.selectedTaskId === params.taskId) {
			yield* openDetailFromMouse
			return
		}

		yield* openActionPalette
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Handle small-screen column pager clicks.
 */
export const handleColumnPagerMouseInteractionAtom = appRuntime.fn(
	(params: ColumnPagerInteractionParams) =>
		Effect.gen(function* () {
			const overlay = yield* OverlayService
			const currentOverlay = yield* overlay.current()
			if (currentOverlay) return

			const nextColumn = findNextNonEmptyColumnIndex(
				params.tasksByColumn,
				params.columnIndex,
				params.delta,
			)
			if (nextColumn === undefined) return

			const nextColumnTasks = params.tasksByColumn[nextColumn] ?? []
			const nextTask = Math.min(params.taskIndex, Math.max(0, nextColumnTasks.length - 1))

			const nav = yield* NavigationService
			yield* nav.jumpTo(nextColumn, nextTask)
		}).pipe(Effect.catchAll(Effect.logError)),
)
