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

import type { CommandExecutor } from "@effect/platform"
import { Data, Effect, SubscriptionRef } from "effect"
import { IssueEditorService } from "../../core/IssueEditorService.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import { DaemonRpcClient } from "../../rpc/DaemonRpcClient.js"
import { COLUMNS, hasTaskSessionPresence } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { EditorService } from "../EditorService.js"
import { formatForToast } from "../ErrorFormatter.js"
import { type Mutation, MutationQueue } from "../MutationQueue.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

class MissingDaemonIssueRpcError extends Data.TaggedError("MissingDaemonIssueRpcError")<{
	readonly method: "issueUpdate"
}> {}

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
			const issueEditor = yield* IssueEditorService
			const prWorkflow = yield* PRWorkflow
			const mutationQueue = yield* MutationQueue
			const daemonRpcClient = yield* DaemonRpcClient

			const getActiveProjectPath = (): Effect.Effect<string | undefined> =>
				SubscriptionRef.get(board.currentProjectPath).pipe(
					Effect.map((projectPath) => projectPath ?? undefined),
				)

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

			const stopSessionWithPreferredRuntime = (
				issueId: string,
				projectPath?: string,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					if (daemonRpcClient.sessionStop === undefined) {
						return yield* Effect.fail(new Error("Daemon sessionStop RPC is unavailable"))
					}
					const effectiveProjectPath =
						projectPath ?? (yield* getActiveProjectPath()) ?? process.cwd()
					yield* daemonRpcClient.sessionStop({
						issueId,
						projectPath: effectiveProjectPath,
					})
				}).pipe(Effect.asVoid)

			const issueUpdateWithPreferredRuntime = (params: {
				readonly issueId: string
				readonly fields: {
					readonly title?: string
					readonly description?: string
					readonly status?: "open" | "in_progress" | "blocked" | "closed" | "tombstone"
					readonly priority?: number
					readonly assignee?: string
					readonly design?: string
					readonly notes?: string
					readonly acceptance?: string
					readonly estimate?: number
					readonly parent?: string
					readonly addDependency?: string
					readonly removeDependency?: string
					readonly dependencyType?: string
				}
				readonly cwd?: string
			}): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const issueUpdate = daemonRpcClient.issueUpdate
					if (issueUpdate === undefined) {
						return yield* Effect.fail(new MissingDaemonIssueRpcError({ method: "issueUpdate" }))
					}
					yield* issueUpdate(params)
				}).pipe(Effect.asVoid)

			const isMissingDaemonIssueRpcError = (error: unknown): error is MissingDaemonIssueRpcError =>
				typeof error === "object" &&
				error !== null &&
				"_tag" in error &&
				error._tag === "MissingDaemonIssueRpcError"

			const isColumnStatus = (status: string): status is (typeof COLUMNS)[number]["status"] =>
				COLUMNS.some((column) => column.status === status)

			const handleForkError = (error: unknown) =>
				Effect.gen(function* () {
					const formatted = formatForToast(error)
					yield* Effect.logError(`Fork failed: ${formatted}`, { error })
					yield* toast.show("error", `Fork failed: ${formatted}`)
				})

			const formatIssueEditorError = (action: "edit" | "create", error: unknown): string => {
				if (typeof error === "object" && error !== null && "_tag" in error) {
					const tag = error._tag
					if (
						(tag === "ParseMarkdownError" || tag === "EditorError") &&
						"message" in error &&
						typeof error.message === "string"
					) {
						return tag === "ParseMarkdownError"
							? `Invalid format: ${error.message}`
							: `Editor error: ${error.message}`
					}
				}
				return `Failed to ${action}: ${String(error)}`
			}

			const deleteIssueAndCleanup = (taskId: string, hasSession: boolean) =>
				Effect.gen(function* () {
					const projectPath = yield* getActiveProjectPath()
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
						cwd: projectPath,
						rollback: syncTaskFromBackend(taskId),
					}
					yield* board.removeTaskFromMutation(taskId)
					yield* mutationQueue.enqueue(deleteMutation)
					yield* toast.show("success", `Deleted ${taskId}`)
					yield* nav.initialize()
				}).pipe(
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							const message = `Failed to delete ${taskId}: ${formatForToast(error)}`
							yield* Effect.logError(message, { error })
							yield* toast.show("error", message)
						}),
					),
				)

			const editIssue = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					yield* issueEditor.editIssue(task).pipe(
						Effect.tap(() => toast.show("success", `Updated ${task.id}`)),
						Effect.tap(() => syncTaskFromBackend(task.id)),
						Effect.catchAll((error) => {
							const msg = formatIssueEditorError("edit", error)
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
								const projectPath = yield* getActiveProjectPath()
								const epicId = yield* nav.getDrillDownEpic()
								let parentEpicId: string | null | undefined

								if (epicId) {
									yield* issueUpdateWithPreferredRuntime({
										issueId: result.id,
										fields: { parent: epicId },
										cwd: projectPath,
									}).pipe(
										Effect.tap(() => {
											parentEpicId = epicId
											return toast.show("success", `Created ${result.id} (added to epic)`)
										}),
										Effect.catchAll((error) =>
											isMissingDaemonIssueRpcError(error)
												? Effect.fail(error)
												: Effect.gen(function* () {
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
							const msg = formatIssueEditorError("create", error)
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

					const hasSession = hasTaskSessionPresence(task)
					const sessionWarning = hasSession
						? "\n\nThis will also remove the worktree and session."
						: ""

					yield* overlay.push({
						_tag: "confirm",
						message: `Permanently delete issue ${task.id}?${sessionWarning}`,
						onConfirm: deleteIssueAndCleanup(task.id, hasSession),
					})
				})

			const tombstoneIssue = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					const hasSession = hasTaskSessionPresence(task)
					const sessionWarning = hasSession
						? "\n\nThis will stop the active session, but keep the branch and worktree."
						: "\n\nThis keeps the branch and worktree."

					const tombstoneIssueOnly = Effect.gen(function* () {
						const projectPath = yield* getActiveProjectPath()
						if (hasSession) {
							yield* toast.show("info", `Stopping session for ${task.id} before tombstoning...`)
							yield* stopSessionWithPreferredRuntime(task.id, projectPath).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Failed to stop session for ${task.id}: ${error}`),
								),
							)
						}

						const updateMutation: Mutation = {
							_tag: "Update",
							id: task.id,
							fields: { status: "tombstone" },
							cwd: projectPath,
							rollback: syncTaskFromBackend(task.id),
						}
						yield* board.removeTaskFromMutation(task.id)
						yield* mutationQueue.enqueue(updateMutation)
						yield* toast.show("success", `Tombstoned ${task.id}`)
						yield* nav.initialize()
					}).pipe(
						Effect.catchAll((error) =>
							Effect.gen(function* () {
								const message = `Failed to tombstone ${task.id}: ${formatForToast(error)}`
								yield* Effect.logError(message, { error })
								yield* toast.show("error", message)
							}),
						),
					)

					yield* overlay.push({
						_tag: "confirm",
						message: `Tombstone issue ${task.id}?${sessionWarning}`,
						onConfirm: tombstoneIssueOnly,
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
						const projectPath = yield* getActiveProjectPath()
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
								cwd: projectPath,
								rollback: board.applyOptimisticMove(id, previousStatus),
							}
							yield* mutationQueue.enqueue(moveMutation)
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
						yield* toast.show(
							"error",
							`Fork failed: ${task.id} must already be an epic (daemon RPC does not support type conversion here)`,
						)
						return
					}

					yield* overlay.push({
						_tag: "create",
						title: "Create Forked Task",
						initial: {
							type: "task",
							priority: task.priority,
							implementations: task.implementations,
						},
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
						initial: {
							type: "epic",
							priority: task.priority,
							implementations: task.implementations,
						},
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
						initial: {
							type: "task",
							priority: task.priority,
							implementations: task.implementations,
						},
						context: { _tag: "forkChild", parentEpicId, sourceTaskId: task.id },
					})
				}).pipe(Effect.catchAll(handleForkError))

			return {
				editIssue,
				createIssue,
				deleteIssue,
				tombstoneIssue,
				moveTasksToColumn,
				forkIssue,
				forkFromCurrent,
				forkWithNewEpic,
				forkUnderParent,
			}
		}),
	},
) {}
