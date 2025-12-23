/**
 * TaskHandlersService
 *
 * Handles task/bead management:
 * - Edit bead (e)
 * - Create bead (c)
 * - Delete bead (D)
 * - Move task between columns (h/l in action mode)
 * - Toggle VC auto-pilot (a in normal mode)
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect } from "effect"
import { BeadEditorService } from "../../core/BeadEditorService.js"
import { BeadsClient } from "../../core/BeadsClient.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import { VCService } from "../../core/VCService.js"
import { COLUMNS } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { EditorService } from "../EditorService.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

// ============================================================================
// Service Definition
// ============================================================================

export class TaskHandlersService extends Effect.Service<TaskHandlersService>()(
	"TaskHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			BoardService.Default,
			NavigationService.Default,
			EditorService.Default,
			OverlayService.Default,
			BeadsClient.Default,
			BeadEditorService.Default,
			PRWorkflow.Default,
			VCService.Default,
		],

		effect: Effect.gen(function* () {
			// Inject services at construction time
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const board = yield* BoardService
			const nav = yield* NavigationService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const beadsClient = yield* BeadsClient
			const beadEditor = yield* BeadEditorService
			const prWorkflow = yield* PRWorkflow
			const vc = yield* VCService

			// ================================================================
			// Internal Helpers
			// ================================================================

			/**
			 * Execute the actual delete bead operation (called via confirm dialog)
			 *
			 * Internal helper used by deleteBead.
			 */
			const doDeleteBead = (taskId: string, hasSession: boolean) =>
				Effect.gen(function* () {
					// If there's an active session, clean up the worktree first
					// (like space+d does, but without closing the bead since we're deleting it)
					if (hasSession) {
						yield* toast.show("info", `Cleaning up worktree for ${taskId}...`)
						yield* prWorkflow
							.cleanup({
								beadId: taskId,
								projectPath: process.cwd(),
								closeBead: false, // Don't close - we're deleting it entirely
							})
							.pipe(
								Effect.catchAll((error) => {
									// Log but continue with deletion - worktree cleanup is best-effort
									return Effect.logWarning(`Worktree cleanup failed for ${taskId}: ${error}`)
								}),
							)
					}

					yield* beadsClient.delete(taskId).pipe(
						Effect.tap(() => toast.show("success", `Deleted ${taskId}`)),
						Effect.tap(() => board.requestRefresh()),
						Effect.tap(() => nav.initialize()),
						Effect.catchAll(helpers.showErrorToast("Failed to delete")),
					)
				})

			// ================================================================
			// Task Handler Methods
			// ================================================================

			/**
			 * Edit bead action (Space+e)
			 *
			 * Opens the task in $EDITOR for editing.
			 * Refreshes the board after successful edit.
			 */
			const editBead = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					yield* beadEditor.editBead(task).pipe(
						Effect.tap(() => toast.show("success", `Updated ${task.id}`)),
						Effect.tap(() => board.requestRefresh()),
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
								yield* toast.show("error", msg)
							})
						}),
					)
				})

			/**
			 * Create bead via $EDITOR action (c key)
			 *
			 * Opens $EDITOR with a template for a new bead.
			 * After creation, jumps to the new task.
			 * If in epic drill-down mode, automatically adds the new task as a child of the epic.
			 */
			const createBead = () =>
				Effect.gen(function* () {
					yield* beadEditor.createBead().pipe(
						Effect.tap((result) =>
							Effect.gen(function* () {
								// Check if we're in epic drill-down mode
								const epicId = yield* nav.getDrillDownEpic()

								if (epicId) {
									// Add parent-child dependency to link task to epic
									yield* beadsClient.addDependency(result.id, epicId, "parent-child").pipe(
										Effect.tap(() => toast.show("success", `Created ${result.id} (added to epic)`)),
										Effect.catchAll((error) =>
											Effect.gen(function* () {
												// Log warning but don't fail - the task was still created
												yield* Effect.logWarning(
													`Failed to link ${result.id} to epic ${epicId}: ${error}`,
												)
												yield* toast.show(
													"warning",
													`Created ${result.id} (failed to link to epic)`,
												)
											}),
										),
									)
								} else {
									yield* toast.show("success", `Created ${result.id}`)
								}
							}),
						),
						Effect.tap(() => board.requestRefresh()),
						Effect.tap((result) => nav.jumpToTask(result.id)),
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
								yield* toast.show("error", msg)
							})
						}),
					)
				})

			/**
			 * Delete bead action (Space+D)
			 *
			 * Shows confirmation dialog, then permanently deletes the selected bead.
			 * If the task has an active session/worktree, cleans it up first.
			 * Reinitializes cursor position after deletion.
			 */
			const deleteBead = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					const hasSession = task.sessionState !== "idle"
					const sessionWarning = hasSession
						? "\n\nThis will also remove the worktree and session."
						: ""

					// Show confirmation dialog before deletion
					yield* overlay.push({
						_tag: "confirm",
						message: `Permanently delete bead ${task.id}?${sessionWarning}`,
						onConfirm: doDeleteBead(task.id, hasSession),
					})
				})

			/**
			 * Move task(s) to adjacent column
			 *
			 * Moves the selected task(s) left or right between kanban columns.
			 * If in select mode, moves all selected tasks.
			 * Otherwise, moves just the current task.
			 *
			 * @param direction - "left" or "right"
			 */
			const moveTasksToColumn = (direction: "left" | "right") =>
				Effect.gen(function* () {
					const columnIndex = yield* helpers.getColumnIndex()
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
					const mode = yield* editor.getMode()
					const selectedIds = mode._tag === "select" ? mode.selectedIds : []
					const task = yield* helpers.getActionTargetTask()

					const taskIdsToMove = selectedIds.length > 0 ? [...selectedIds] : task ? [task.id] : []
					const firstTaskId = taskIdsToMove[0]

					if (taskIdsToMove.length > 0) {
						yield* Effect.all(
							taskIdsToMove.map((id) => beadsClient.update(id, { status: targetStatus })),
						)
						yield* board.requestRefresh()
						if (firstTaskId) {
							yield* nav.setFollow(firstTaskId)
						}
					}
				})

			/**
			 * Toggle VC auto-pilot action (a key in normal mode)
			 *
			 * Starts or stops the VC (Virtual Coworker) auto-pilot.
			 */
			const toggleVC = () =>
				vc.toggleAutoPilot().pipe(
					Effect.tap((status) => {
						const message =
							status.status === "running" ? "VC auto-pilot started" : "VC auto-pilot stopped"
						return toast.show("success", message)
					}),
					Effect.catchAll(helpers.showErrorToast("Failed to toggle VC")),
				)

			// ================================================================
			// Public API
			// ================================================================

			return {
				editBead,
				createBead,
				deleteBead,
				moveTasksToColumn,
				toggleVC,
			}
		}),
	},
) {}
