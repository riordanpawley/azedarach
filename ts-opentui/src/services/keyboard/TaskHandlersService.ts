/**
 * TaskHandlersService
 *
 * Handles task management:
 * - Edit issue (e)
 * - Create issue (c)
 * - Delete issue (D)
 * - Move task between columns (h/l in action mode)
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect } from "effect"
import { IssueEditorService } from "../../core/IssueEditorService.js"
import { IssueTrackerClient } from "../../core/IssueTrackerClient.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import { COLUMNS } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { EditorService } from "../EditorService.js"
import { formatForToast } from "../ErrorFormatter.js"
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
			IssueTrackerClient.Default,
			IssueEditorService.Default,
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
			const issueTrackerClient = yield* IssueTrackerClient
			const issueEditor = yield* IssueEditorService
			const prWorkflow = yield* PRWorkflow
			const mutationQueue = yield* MutationQueue

			const getTaskById = (taskId: string) =>
				Effect.gen(function* () {
					const tasks = yield* board.getTasks()
					return tasks.find((task) => task.id === taskId)
				})

			const syncTaskFromBackend = (
				taskId: string,
				options?: { readonly parentEpicId?: string | null },
			) =>
				board.syncTaskFromBackend(taskId, options).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(
							`Failed to sync task ${taskId} after mutation: ${formatForToast(error)}`,
						),
					),
					Effect.asVoid,
				)

			const isColumnStatus = (status: string): status is (typeof COLUMNS)[number]["status"] =>
				COLUMNS.some((column) => column.status === status)

			const handleForkError = (error: unknown) =>
				Effect.gen(function* () {
					const formatted = formatForToast(error)
					yield* Effect.logError(`Fork failed: ${formatted}`, { error })
					yield* toast.show("error", `Fork failed: ${formatted}`)
				})

			const deleteIssueAndCleanup = (taskId: string, hasSession: boolean) =>
				Effect.gen(function* () {
					if (hasSession) {
						yield* toast.show("info", `Cleaning up worktree for ${taskId}...`)
						yield* prWorkflow
							.cleanup({
								issueId: taskId,
								projectPath: process.cwd(),
								closeIssue: false,
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
						rollback: syncTaskFromBackend(taskId),
					}
					yield* board.removeTaskFromMutation(taskId)
					yield* mutationQueue.add(deleteMutation)
					yield* toast.show("success", `Deleted ${taskId}`)
					// Await mutation processing - tracker commands are fast (~50ms)
					// MutationQueue handles rollback and error toasts on failure
					yield* mutationQueue.process(taskId)
					yield* nav.initialize()
				})

			const editIssue = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					yield* issueEditor.editIssue(task).pipe(
						Effect.tap(() => toast.show("success", `Updated ${task.id}`)),
						Effect.tap(() => syncTaskFromBackend(task.id)),
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
								yield* Effect.logError(`Edit issue: ${msg}`, { error })
								yield* toast.show("error", msg)
							})
						}),
					)
				})

			const createIssue = () =>
				Effect.gen(function* () {
					yield* issueEditor.createIssue().pipe(
						Effect.flatMap((result) =>
							Effect.gen(function* () {
								const epicId = yield* nav.getDrillDownEpic()
								let parentEpicId: string | null | undefined = undefined

								if (epicId) {
									yield* issueTrackerClient.addDependency(result.id, epicId, "parent-child").pipe(
										Effect.tap(() => {
											parentEpicId = epicId
											return toast.show("success", `Created ${result.id} (added to epic)`)
										}),
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

								yield* syncTaskFromBackend(result.id, { parentEpicId })
								yield* nav.jumpToTask(result.id)
							}),
						),
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
								yield* Effect.logError(`Create issue: ${msg}`, { error })
								yield* toast.show("error", msg)
							})
						}),
					)
				})

			const deleteIssue = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					const hasSession = task.sessionState !== "idle"
					const sessionWarning = hasSession
						? "\n\nThis will also remove the worktree and session."
						: ""

					yield* overlay.push({
						_tag: "confirm",
						message: `Permanently delete issue ${task.id}?${sessionWarning}`,
						onConfirm: deleteIssueAndCleanup(task.id, hasSession),
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
						const previousStatusByTaskId = new Map<string, (typeof COLUMNS)[number]["status"]>()
						for (const id of taskIdsToMove) {
							const task = yield* board.findTaskById(id)
							if (task && isColumnStatus(task.status)) {
								previousStatusByTaskId.set(id, task.status)
							}
						}

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
							const previousStatus = previousStatusByTaskId.get(id)
							if (!previousStatus) continue

							const moveMutation: Mutation = {
								_tag: "Move",
								id,
								status: targetStatus,
								rollback: board.applyOptimisticMove(id, previousStatus),
							}
							yield* mutationQueue.add(moveMutation)
							yield* mutationQueue.process(id)
						}
					}
				})

			const forkIssue = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					yield* overlay.push({
						_tag: "fork",
						sourceTaskId: task.id,
						sourceTaskTitle: task.title,
						parentEpicId: task.parentEpicId,
					})
				})

			const forkFromCurrent = (taskId: string) =>
				Effect.gen(function* () {
					const task = yield* getTaskById(taskId)
					if (!task) {
						yield* toast.show("error", "Fork failed: task not found")
						return
					}

					if (task.issue_type !== "epic") {
						yield* toast.show("info", `Converting ${task.id} to epic...`)
						yield* issueTrackerClient.update(task.id, { type: "epic" })
						yield* board.patchTaskFromMutation(task.id, {
							issue_type: "epic",
							updated_at: new Date().toISOString(),
						})
					}

					yield* overlay.push({
						_tag: "create",
						title: "Create Forked Task",
						initial: { type: "task", priority: task.priority },
						context: { _tag: "forkChild", parentEpicId: task.id, sourceTaskId: task.id },
					})
				}).pipe(Effect.catchAll(handleForkError))

			const forkWithNewEpic = (taskId: string) =>
				Effect.gen(function* () {
					const task = yield* getTaskById(taskId)
					if (!task) {
						yield* toast.show("error", "Fork failed: task not found")
						return
					}

					yield* overlay.push({
						_tag: "create",
						title: "Create Parent Epic",
						initial: { type: "epic", priority: task.priority },
						lockType: true,
						context: { _tag: "forkEpic", sourceTaskId: task.id },
					})
				}).pipe(Effect.catchAll(handleForkError))

			const forkUnderParent = (taskId: string, parentEpicId: string) =>
				Effect.gen(function* () {
					const task = yield* getTaskById(taskId)
					if (!task) {
						yield* toast.show("error", "Fork failed: task not found")
						return
					}

					if (!parentEpicId) {
						yield* toast.show("warning", "No parent epic to fork under")
						return
					}

					yield* overlay.push({
						_tag: "create",
						title: "Create Forked Task",
						initial: { type: "task", priority: task.priority },
						context: { _tag: "forkChild", parentEpicId, sourceTaskId: task.id },
					})
				}).pipe(Effect.catchAll(handleForkError))

			return {
				editIssue,
				createIssue,
				deleteIssue,
				moveTasksToColumn,
				forkIssue,
				forkFromCurrent,
				forkWithNewEpic,
				forkUnderParent,
			}
		}),
	},
) {}
