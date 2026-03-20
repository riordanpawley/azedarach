import type { CommandExecutor } from "@effect/platform"
import { Effect } from "effect"
import type { TaskWithSession } from "../../types.js"
import { buildTaskQueueKey } from "../../utils/queueKey.js"
import {
	type CommandCancelledError,
	CommandQueueService,
	type CommandTimeoutError,
} from "../CommandQueueService.js"
import { EditorService } from "../EditorService.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { TuiBoardStoreService } from "../TuiBoardStoreService.js"
import { TuiProjectContextService } from "../TuiProjectContextService.js"

type QueueFailure = CommandTimeoutError | CommandCancelledError

type MessageError = Readonly<{
	readonly message: string
}>

export interface KeyboardHelpersServiceApi {
	readonly getSelectedTask: () => Effect.Effect<TaskWithSession | undefined>
	readonly getActionTargetTask: () => Effect.Effect<TaskWithSession | undefined>
	readonly getActionTargetTasks: () => Effect.Effect<ReadonlyArray<TaskWithSession>>
	readonly getColumnIndex: () => Effect.Effect<number>
	readonly getProjectPath: () => Effect.Effect<string>
	readonly showErrorToast: <E extends MessageError>(
		prefix: string,
	) => (error: E) => Effect.Effect<void>
	readonly openCurrentDetail: () => Effect.Effect<void>
	readonly toggleCurrentSelection: () => Effect.Effect<void>
	readonly withQueue: <A, E>(
		taskId: string,
		label: string,
		operation: Effect.Effect<A, E, CommandExecutor.CommandExecutor>,
		projectPathOverride?: string,
	) => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	readonly checkBusy: (taskId: string, projectPathOverride?: string) => Effect.Effect<boolean>
	readonly isAnyBusy: () => Effect.Effect<boolean, never, never>
	readonly getRunningOperationLabels: () => Effect.Effect<readonly string[], never, never>
}

const showQueueFailureToast = (
	toast: ToastService,
	label: string,
	error: QueueFailure,
): Effect.Effect<void> =>
	Effect.gen(function* () {
		yield* Effect.logWarning(error)
		yield* toast.show("error", `${label} timed out or was cancelled: ${error._tag}`)
	})

export class KeyboardHelpersService extends Effect.Service<KeyboardHelpersService>()(
	"KeyboardHelpersService",
	{
		dependencies: [
			ToastService.Default,
			NavigationService.Default,
			TuiBoardStoreService.Default,
			EditorService.Default,
			OverlayService.Default,
			CommandQueueService.Default,
			TuiProjectContextService.Default,
		],
		effect: Effect.gen(function* () {
			const toast = yield* ToastService
			const navigation = yield* NavigationService
			const board = yield* TuiBoardStoreService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const commandQueue = yield* CommandQueueService
			const projectContext = yield* TuiProjectContextService

			const getSelectedTask = (): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					const taskId = yield* navigation.getFocusedTaskId()
					if (taskId === null) {
						return undefined
					}

					const tasks = yield* board.getTasks()
					return tasks.find((task) => task.id === taskId)
				})

			const getActionTargetTask = (): Effect.Effect<TaskWithSession | undefined> =>
				Effect.gen(function* () {
					const targetId = yield* editor.getActionTargetTaskId()
					if (targetId !== null) {
						const tasks = yield* board.getTasks()
						return tasks.find((task) => task.id === targetId)
					}

					return yield* getSelectedTask()
				})

			const getActionTargetTasks = (): Effect.Effect<ReadonlyArray<TaskWithSession>> =>
				Effect.gen(function* () {
					const selectedIds = yield* editor.getSelectedIds()
					if (selectedIds.length > 0) {
						const tasks = yield* board.getTasks()
						return selectedIds.flatMap((selectedId) => {
							const task = tasks.find((candidate) => candidate.id === selectedId)
							return task === undefined ? [] : [task]
						})
					}

					const task = yield* getActionTargetTask()
					return task === undefined ? [] : [task]
				})

			const getColumnIndex = (): Effect.Effect<number> =>
				Effect.gen(function* () {
					const position = yield* navigation.getPosition()
					return position.columnIndex
				})

			const getProjectPath = (): Effect.Effect<string> =>
				projectContext
					.getCurrentPath()
					.pipe(Effect.map((projectPath) => projectPath ?? process.cwd()))

			const showErrorToast =
				<E extends MessageError>(prefix: string) =>
				(error: E): Effect.Effect<void> =>
					Effect.gen(function* () {
						yield* Effect.logError(`${prefix}: ${error.message}`, { error })
						yield* toast.show("error", `${prefix}: ${error.message}`)
					})

			const openCurrentDetail = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getSelectedTask()
					if (task !== undefined) {
						yield* overlay.push({ _tag: "detail", taskId: task.id })
					}
				})

			const toggleCurrentSelection = (): Effect.Effect<void> =>
				Effect.gen(function* () {
					const task = yield* getSelectedTask()
					if (task !== undefined) {
						yield* editor.toggleSelection(task.id)
					}
				})

			const withQueue = <A, E>(
				taskId: string,
				label: string,
				operation: Effect.Effect<A, E, CommandExecutor.CommandExecutor>,
				projectPathOverride?: string,
			): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const projectPath = projectPathOverride ?? (yield* getProjectPath())
					yield* commandQueue.enqueue({
						taskId,
						queueKey: buildTaskQueueKey(taskId, projectPath),
						label,
						effect: Effect.asVoid(operation),
					})
				}).pipe(
					Effect.catchTags({
						CommandTimeoutError: (error) => showQueueFailureToast(toast, label, error),
						CommandCancelledError: (error) => showQueueFailureToast(toast, label, error),
					}),
				)

			const checkBusy = (taskId: string, projectPathOverride?: string): Effect.Effect<boolean> =>
				Effect.gen(function* () {
					const projectPath = projectPathOverride ?? (yield* getProjectPath())
					const queueInfo = yield* commandQueue.getTaskQueueInfo(taskId, projectPath)

					if (queueInfo.runningLabel === null) {
						return false
					}

					const recovered = yield* commandQueue.recoverStaleRunning(taskId)
					if (recovered) {
						yield* toast.show(
							"warning",
							`${taskId} had a stale ${queueInfo.runningLabel} operation; recovered. Retry your action.`,
						)
						return false
					}

					const refreshedQueueInfo = yield* commandQueue.getTaskQueueInfo(taskId, projectPath)
					if (refreshedQueueInfo.runningLabel === null) {
						return false
					}

					yield* toast.show(
						"error",
						`${taskId} is busy (${refreshedQueueInfo.runningLabel} in progress)`,
					)
					return true
				})

			return {
				getSelectedTask,
				getActionTargetTask,
				getActionTargetTasks,
				getColumnIndex,
				getProjectPath,
				showErrorToast,
				openCurrentDetail,
				toggleCurrentSelection,
				withQueue,
				checkBusy,
				isAnyBusy: () => commandQueue.isAnyBusy(),
				getRunningOperationLabels: () => commandQueue.getRunningOperationLabels(),
			} satisfies KeyboardHelpersServiceApi
		}),
	},
) {}
