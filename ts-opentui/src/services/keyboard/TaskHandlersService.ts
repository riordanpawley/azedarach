/**
 * TaskHandlersService
 *
 * Handles task/bead management:
 * - Edit bead (e)
 * - Create bead (c)
 * - Delete bead (D)
 * - Move task between columns (h/l in action mode)
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect } from "effect"
import { BeadEditorService } from "../../core/BeadEditorService.js"
import { BeadsClient } from "../../core/BeadsClient.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import { COLUMNS } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { EditorService } from "../EditorService.js"
import { type Mutation, MutationQueue } from "../MutationQueue.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

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
			MutationQueue.Default,
		],

		effect: Effect.gen(function* () {
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const board = yield* BoardService
			const nav = yield* NavigationService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const beadsClient = yield* BeadsClient
			const beadEditor = yield* BeadEditorService
			const prWorkflow = yield* PRWorkflow
			const mutationQueue = yield* MutationQueue

			const doDeleteBead = (taskId: string, hasSession: boolean) =>
				Effect.gen(function* () {
					if (hasSession) {
						yield* toast.show("info", `Cleaning up worktree for ${taskId}...`)
						yield* prWorkflow
							.cleanup({
								beadId: taskId,
								projectPath: process.cwd(),
								closeBead: false,
							})
							.pipe(
								Effect.catchAll((error) => {
									return Effect.logWarning(`Worktree cleanup failed for ${taskId}: ${error}`)
								}),
							)
					}

					const deleteMutation: Mutation = {
						_tag: "Delete",
						id: taskId,
						rollback: board.requestRefresh(),
					}
					yield* mutationQueue.add(deleteMutation)
					yield* toast.show("success", `Deleted ${taskId}`)
					// Await mutation processing - bd commands are fast (~50ms)
					// MutationQueue handles rollback and error toasts on failure
					yield* mutationQueue.process(taskId)
					yield* board.requestRefresh()
					yield* nav.initialize()
				})

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

			const createBead = () =>
				Effect.gen(function* () {
					yield* beadEditor.createBead().pipe(
						Effect.tap((result) =>
							Effect.gen(function* () {
								const epicId = yield* nav.getDrillDownEpic()

								if (epicId) {
									yield* beadsClient.addDependency(result.id, epicId, "parent-child").pipe(
										Effect.tap(() => toast.show("success", `Created ${result.id} (added to epic)`)),
										Effect.catchAll((error) =>
											Effect.gen(function* () {
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

			const deleteBead = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					const hasSession = task.sessionState !== "idle"
					const sessionWarning = hasSession
						? "\n\nThis will also remove the worktree and session."
						: ""

					yield* overlay.push({
						_tag: "confirm",
						message: `Permanently delete bead ${task.id}?${sessionWarning}`,
						onConfirm: doDeleteBead(task.id, hasSession),
					})
				})

			const moveTasksToColumn = (direction: "left" | "right") =>
				Effect.gen(function* () {
					const columnIndex = yield* helpers.getColumnIndex()
					const targetColIdx = direction === "left" ? columnIndex - 1 : columnIndex + 1

					if (targetColIdx < 0 || targetColIdx >= COLUMNS.length) {
						return
					}

					const targetStatus = COLUMNS[targetColIdx]?.status
					if (!targetStatus) {
						return
					}

					const mode = yield* editor.getMode()
					const selectedIds = mode._tag === "select" ? mode.selectedIds : []
					const currentTask = yield* helpers.getActionTargetTask()

					const taskIdsToMove =
						selectedIds.length > 0 ? [...selectedIds] : currentTask ? [currentTask.id] : []
					const firstTaskId = taskIdsToMove[0]

					if (taskIdsToMove.length > 0) {
						// Apply optimistic updates IMMEDIATELY to in-memory state
						// This gives instant visual feedback before any async work
						for (const id of taskIdsToMove) {
							yield* board.applyOptimisticMove(id, targetStatus)
						}

						// Follow the task to its new column
						if (firstTaskId) {
							yield* nav.setFollow(firstTaskId)
						}

						// Add mutations to queue and process
						for (const id of taskIdsToMove) {
							const task = yield* board.findTaskById(id)
							if (!task) continue

							const moveMutation: Mutation = {
								_tag: "Move",
								id,
								status: targetStatus,
								rollback: Effect.gen(function* () {
									yield* board.requestRefresh()
								}),
							}
							yield* mutationQueue.add(moveMutation)
							yield* mutationQueue.process(id)
						}

						// Refresh to sync with backend after mutations complete
						yield* board.requestRefresh()
					}
				})

			return {
				editBead,
				createBead,
				deleteBead,
				moveTasksToColumn,
			}
		}),
	},
) {}
