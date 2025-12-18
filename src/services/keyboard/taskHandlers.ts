/**
 * Task Key Handlers
 *
 * Handlers for task/bead management:
 * - Edit bead (e)
 * - Create bead (c)
 * - Delete bead (D)
 * - Move task between columns (h/l in action mode)
 * - Toggle VC auto-pilot (a in normal mode)
 */

import { Effect } from "effect"
import { COLUMNS } from "../../ui/types.js"
import type { HandlerContext } from "./types.js"

// ============================================================================
// Task Handler Factory
// ============================================================================

/**
 * Create all task-related action handlers
 *
 * These handlers manage beads: creating, editing, deleting, and moving
 * tasks between kanban columns.
 */
export const createTaskHandlers = (ctx: HandlerContext) => ({
	/**
	 * Edit bead action (Space+e)
	 *
	 * Opens the task in $EDITOR for editing.
	 * Refreshes the board after successful edit.
	 */
	editBead: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			yield* ctx.beadEditor.editBead(task).pipe(
				Effect.tap(() => ctx.toast.show("success", `Updated ${task.id}`)),
				Effect.tap(() => ctx.board.refresh()),
				Effect.catchAll((error) => {
					const msg =
						error && typeof error === "object" && "_tag" in error
							? error._tag === "ParseMarkdownError"
								? `Invalid format: ${(error as { message: string }).message}`
								: error._tag === "EditorError"
									? `Editor error: ${(error as { message: string }).message}`
									: `Failed to edit: ${error}`
							: `Failed to edit: ${error}`
					return Effect.gen(function* () {
						yield* Effect.logError(`Edit bead: ${msg}`, { error })
						yield* ctx.toast.show("error", msg)
					})
				}),
			)
		}),

	/**
	 * Create bead via $EDITOR action (c key)
	 *
	 * Opens $EDITOR with a template for a new bead.
	 * After creation, jumps to the new task.
	 */
	createBead: () =>
		Effect.gen(function* () {
			yield* ctx.beadEditor.createBead().pipe(
				Effect.tap(() => ctx.board.refresh()),
				Effect.tap((result) => ctx.nav.jumpToTask(result.id)),
				Effect.tap((result) => ctx.toast.show("success", `Created ${result.id}`)),
				Effect.catchAll((error) => {
					const msg =
						error && typeof error === "object" && "_tag" in error
							? error._tag === "ParseMarkdownError"
								? `Invalid format: ${(error as { message: string }).message}`
								: error._tag === "EditorError"
									? `Editor error: ${(error as { message: string }).message}`
									: `Failed to create: ${error}`
							: `Failed to create: ${error}`
					return Effect.gen(function* () {
						yield* Effect.logError(`Create bead: ${msg}`, { error })
						yield* ctx.toast.show("error", msg)
					})
				}),
			)
		}),

	/**
	 * Delete bead action (Space+D)
	 *
	 * Permanently deletes the selected bead.
	 * If the task has an active session/worktree, cleans it up first.
	 * Reinitializes cursor position after deletion.
	 */
	deleteBead: () =>
		Effect.gen(function* () {
			const task = yield* ctx.getSelectedTask()
			if (!task) return

			// If there's an active session, clean up the worktree first
			// (like space+d does, but without closing the bead since we're deleting it)
			if (task.sessionState !== "idle") {
				yield* ctx.toast.show("info", `Cleaning up worktree for ${task.id}...`)
				yield* ctx.prWorkflow
					.cleanup({
						beadId: task.id,
						projectPath: process.cwd(),
						closeBead: false, // Don't close - we're deleting it entirely
					})
					.pipe(
						Effect.catchAll((error) => {
							// Log but continue with deletion - worktree cleanup is best-effort
							return Effect.logWarning(`Worktree cleanup failed for ${task.id}: ${error}`)
						}),
					)
			}

			yield* ctx.beadsClient.delete(task.id).pipe(
				Effect.tap(() => ctx.toast.show("success", `Deleted ${task.id}`)),
				Effect.tap(() => ctx.board.refresh()),
				// Move cursor to a valid task after deletion
				Effect.tap(() => ctx.nav.initialize()),
				Effect.catchAll(ctx.showErrorToast("Failed to delete")),
			)
		}),

	/**
	 * Move task(s) to adjacent column
	 *
	 * Moves the selected task(s) left or right between kanban columns.
	 * If in select mode, moves all selected tasks.
	 * Otherwise, moves just the current task.
	 *
	 * @param direction - "left" or "right"
	 */
	moveTasksToColumn: (direction: "left" | "right") =>
		Effect.gen(function* () {
			const columnIndex = yield* ctx.getColumnIndex()
			const targetColIdx = direction === "left" ? columnIndex - 1 : columnIndex + 1

			// Bounds check
			if (targetColIdx < 0 || targetColIdx >= COLUMNS.length) {
				return
			}

			const targetStatus = COLUMNS[targetColIdx]?.status
			if (!targetStatus) {
				return
			}

			// Get selected IDs or current task
			const mode = yield* ctx.editor.getMode()
			const selectedIds = mode._tag === "select" ? mode.selectedIds : []
			const task = yield* ctx.getSelectedTask()

			const taskIdsToMove = selectedIds.length > 0 ? [...selectedIds] : task ? [task.id] : []
			const firstTaskId = taskIdsToMove[0]

			if (taskIdsToMove.length > 0) {
				yield* Effect.all(
					taskIdsToMove.map((id) => ctx.beadsClient.update(id, { status: targetStatus })),
				)
				// Refresh board to reflect the move
				yield* ctx.board.refresh()
				// Follow the first task
				if (firstTaskId) {
					yield* ctx.nav.setFollow(firstTaskId)
				}
			}
		}),

	/**
	 * Toggle VC auto-pilot action (a key in normal mode)
	 *
	 * Starts or stops the VC (Virtual Coworker) auto-pilot.
	 */
	toggleVC: () =>
		ctx.vc.toggleAutoPilot().pipe(
			Effect.tap((status) => {
				const message =
					status.status === "running" ? "VC auto-pilot started" : "VC auto-pilot stopped"
				return ctx.toast.show("success", message)
			}),
			Effect.catchAll(ctx.showErrorToast("Failed to toggle VC")),
		),
})

export type TaskHandlers = ReturnType<typeof createTaskHandlers>
