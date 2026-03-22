import { resolveEffectiveProjectPath } from "@azedarach/shared/project-path"
import type { CommandExecutor } from "@effect/platform"
import { Effect, SubscriptionRef } from "effect"
import { COLUMNS, hasTaskSessionPresence, type TaskWithSession } from "../../types.js"
import { EditorService } from "../EditorService.js"
import { IssueEditorService } from "../IssueEditorService.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { PrWorkflowService as PRWorkflow } from "../PrWorkflowService.js"
import { ToastService } from "../ToastService.js"
import { TuiBoardStoreService } from "../TuiBoardStoreService.js"
import { TuiIssueAdapterService } from "../TuiIssueAdapterService.js"
import { TuiProjectContextService } from "../TuiProjectContextService.js"
import { TuiSessionAdapterService } from "../TuiSessionAdapterService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

type ColumnStatus = (typeof COLUMNS)[number]["status"]

export interface TaskHandlersServiceApi {
	readonly editIssue: () => Effect.Effect<void>
	readonly createIssue: () => Effect.Effect<void>
	readonly deleteIssue: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	readonly tombstoneIssue: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	readonly moveTasksToColumn: (
		direction: "left" | "right",
	) => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	readonly forkIssue: () => Effect.Effect<void>
	readonly forkFromCurrent: (taskId: string) => Effect.Effect<void>
	readonly forkWithNewEpic: (taskId: string) => Effect.Effect<void>
	readonly forkUnderParent: (taskId: string, parentEpicId: string) => Effect.Effect<void>
}

const isColumnStatus = (status: string): status is ColumnStatus =>
	COLUMNS.some((column) => column.status === status)

const getIssueEditorErrorMessage = (error: {
	readonly _tag: string
	readonly message: string
}): string => {
	switch (error._tag) {
		case "ParseMarkdownError":
			return `Invalid format: ${error.message}`
		case "EditorError":
			return `Editor error: ${error.message}`
		default:
			return error.message
	}
}

export class TaskHandlersService extends Effect.Service<TaskHandlersService>()(
	"TaskHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			TuiBoardStoreService.Default,
			NavigationService.Default,
			EditorService.Default,
			OverlayService.Default,
			TuiIssueAdapterService.Default,
			IssueEditorService.Default,
			PRWorkflow.Default,
			TuiSessionAdapterService.Default,
		],
		effect: Effect.gen(function* () {
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const board = yield* TuiBoardStoreService
			const nav = yield* NavigationService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const issueAdapter = yield* TuiIssueAdapterService
			const issueEditor = yield* IssueEditorService
			const prWorkflow = yield* PRWorkflow
			const sessionAdapter = yield* TuiSessionAdapterService
			const projectContext = yield* TuiProjectContextService

			const getActiveProjectPath = (): Effect.Effect<string | undefined> =>
				SubscriptionRef.get(board.currentProjectPath).pipe(
					Effect.map((projectPath) => projectPath ?? undefined),
				)

			const getTaskById = (taskId: string): Effect.Effect<TaskWithSession | undefined> =>
				board.getTasks().pipe(Effect.map((tasks) => tasks.find((task) => task.id === taskId)))

			const syncTaskFromBackend = (
				taskId: string,
				options?: { readonly parentEpicId?: string | null },
			): Effect.Effect<void> =>
				board.syncTaskFromBackend(taskId, options).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Failed to sync task ${taskId} after mutation`, error),
					),
					Effect.asVoid,
				)

			const deleteIssueAndCleanup = (
				taskId: string,
				hasSession: boolean,
			): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const projectPath = yield* getActiveProjectPath()
					if (hasSession) {
						const effectiveProjectPath = resolveEffectiveProjectPath(
							projectPath ?? (yield* projectContext.getCurrentPath()),
						)
						yield* toast.show("info", `Cleaning up worktree for ${taskId}...`)
						yield* prWorkflow
							.cleanup({
								issueId: taskId,
								projectPath: effectiveProjectPath,
								closeIssue: false,
							})
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Worktree cleanup failed for ${taskId}`, error),
								),
							)
					}

					yield* issueAdapter
						.delete(taskId, { projectPath })
						.pipe(
							Effect.catchTag("TuiIssueAdapterServiceError", (error) =>
								toast.show("error", `Failed to delete ${taskId}: ${error.message}`),
							),
						)
					yield* board.removeTaskFromMutation(taskId)
					yield* toast.show("success", `Deleted ${taskId}`)
					yield* nav.initialize()
				})

			const editIssue = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) {
						return
					}

					yield* issueEditor.editIssue(task).pipe(
						Effect.tap(() => toast.show("success", `Updated ${task.id}`)),
						Effect.tap(() => syncTaskFromBackend(task.id)),
						Effect.catchTags({
							ParseMarkdownError: (error) => toast.show("error", getIssueEditorErrorMessage(error)),
							EditorError: (error) => toast.show("error", getIssueEditorErrorMessage(error)),
						}),
					)
				})

			const createIssue = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					yield* issueEditor.createIssue().pipe(
						Effect.flatMap((result) =>
							Effect.gen(function* () {
								const projectPath = yield* getActiveProjectPath()
								const epicId = yield* nav.getDrillDownEpic()
								let parentEpicId: string | null | undefined

								if (epicId !== null) {
									yield* issueAdapter
										.addDependency(result.id, epicId, "parent-child", { projectPath })
										.pipe(
											Effect.tap(() => {
												parentEpicId = epicId
												return toast.show("success", `Created ${result.id} (added to epic)`)
											}),
											Effect.catchTag("TuiIssueAdapterServiceError", () =>
												toast.show("warning", `Created ${result.id} (failed to link to epic)`),
											),
										)
								} else {
									yield* toast.show("success", `Created ${result.id}`)
								}

								yield* syncTaskFromBackend(result.id, { parentEpicId })
								yield* nav.jumpToTask(result.id)
							}),
						),
						Effect.catchTags({
							ParseMarkdownError: (error) => toast.show("error", getIssueEditorErrorMessage(error)),
							EditorError: (error) => toast.show("error", getIssueEditorErrorMessage(error)),
						}),
					)
				})

			const deleteIssue = (): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) {
						return
					}

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

			const tombstoneIssue = (): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) {
						return
					}

					const hasSession = hasTaskSessionPresence(task)
					const sessionWarning = hasSession
						? "\n\nThis will stop the active session, but keep the branch and worktree."
						: "\n\nThis keeps the branch and worktree."

					const tombstoneIssueOnly = Effect.gen(function* () {
						const projectPath = yield* getActiveProjectPath()
						if (hasSession) {
							yield* toast.show("info", `Stopping session for ${task.id} before tombstoning...`)
							yield* sessionAdapter
								.stop(task.id, { projectPath })
								.pipe(
									Effect.catchTag("TuiSessionAdapterServiceError", (error) =>
										Effect.logWarning(`Failed to stop session for ${task.id}`, error),
									),
								)
						}

						yield* issueAdapter.update(
							task.id,
							{
								status: "tombstone",
							},
							{ projectPath },
						)
						yield* syncTaskFromBackend(task.id)
						yield* toast.show("success", `Tombstoned ${task.id}`)
						yield* nav.initialize()
					}).pipe(
						Effect.catchTag("TuiIssueAdapterServiceError", (error) =>
							toast.show("error", `Failed to tombstone ${task.id}: ${error.message}`),
						),
					)

					yield* overlay.push({
						_tag: "confirm",
						message: `Tombstone issue ${task.id}?${sessionWarning}`,
						onConfirm: tombstoneIssueOnly,
					})
				})

			const moveTasksToColumn = (
				direction: "left" | "right",
			): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const columnIndex = yield* helpers.getColumnIndex()
					const targetColumnIndex = direction === "left" ? columnIndex - 1 : columnIndex + 1

					if (targetColumnIndex < 0 || targetColumnIndex >= COLUMNS.length) {
						return
					}

					const targetStatus = COLUMNS[targetColumnIndex]?.status
					if (targetStatus === undefined) {
						return
					}

					const mode = yield* editor.getMode()
					const selectedIds = mode._tag === "select" ? mode.selectedIds : []
					const currentTask = yield* helpers.getActionTargetTask()
					const taskIdsToMove =
						selectedIds.length > 0 ? [...selectedIds] : currentTask ? [currentTask.id] : []
					const firstTaskId = taskIdsToMove[0]

					if (taskIdsToMove.length === 0) {
						return
					}

					for (const taskId of taskIdsToMove) {
						yield* board.applyOptimisticMove(taskId, targetStatus)
					}

					if (firstTaskId !== undefined) {
						yield* nav.setFollow(firstTaskId)
					}

					const projectPath = yield* getActiveProjectPath()
					for (const taskId of taskIdsToMove) {
						const taskBeforeMove = yield* board.findTaskById(taskId)
						if (taskBeforeMove === undefined) {
							continue
						}
						const previousStatus = isColumnStatus(taskBeforeMove.status)
							? taskBeforeMove.status
							: undefined
						if (previousStatus === undefined) {
							continue
						}

						yield* helpers.withQueue(
							taskId,
							"move",
							issueAdapter
								.update(
									taskId,
									{
										status: targetStatus,
									},
									{ projectPath },
								)
								.pipe(
									Effect.tap(() => syncTaskFromBackend(taskId)),
									Effect.catchTag("TuiIssueAdapterServiceError", (error) =>
										Effect.gen(function* () {
											yield* Effect.logError(`Failed to move ${taskId}`, error)
											yield* board.applyOptimisticMove(taskId, previousStatus)
											yield* toast.show("error", `Failed to move ${taskId}: ${error.message}`)
										}),
									),
								),
							projectPath,
						)
					}
				})

			const handleForkError = (error: { readonly message: string }): Effect.Effect<void> =>
				Effect.gen(function* () {
					yield* Effect.logError(`Fork failed: ${error.message}`, error)
					yield* toast.show("error", `Fork failed: ${error.message}`)
				})

			const forkIssue = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) {
						return
					}

					yield* overlay.push({
						_tag: "fork",
						sourceTaskId: task.id,
						sourceTaskTitle: task.title,
						parentEpicId: task.parentEpicId,
					})
				})

			const forkFromCurrent = (taskId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getTaskById(taskId)
					if (task === undefined) {
						yield* toast.show("error", "Fork failed: task not found")
						return
					}

					if (task.issue_type !== "epic") {
						yield* toast.show("info", `Converting ${task.id} to epic...`)
						const projectPath = yield* getActiveProjectPath()
						yield* issueAdapter.update(
							task.id,
							{
								type: "epic",
							},
							{ projectPath },
						)
						yield* board.patchTaskFromMutation(task.id, {
							issue_type: "epic",
							updated_at: new Date().toISOString(),
						})
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
				}).pipe(Effect.catchTag("TuiIssueAdapterServiceError", handleForkError))

			const forkWithNewEpic = (taskId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getTaskById(taskId)
					if (task === undefined) {
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
				})

			const forkUnderParent = (taskId: string, parentEpicId: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getTaskById(taskId)
					if (task === undefined) {
						yield* toast.show("error", "Fork failed: task not found")
						return
					}

					if (parentEpicId.length === 0) {
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
				})

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
			} satisfies TaskHandlersServiceApi
		}),
	},
) {}
