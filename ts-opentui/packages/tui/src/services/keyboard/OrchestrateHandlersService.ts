import type { CommandExecutor } from "@effect/platform"
import { Effect, Option } from "effect"
import type { OrchestrationTask } from "../EditorService.js"
import { EditorService } from "../EditorService.js"
import { OverlayService } from "../OverlayService.js"
import type { TemplateError } from "../TemplateService.js"
import { TemplateService } from "../TemplateService.js"
import { ToastService } from "../ToastService.js"
import type { TuiIssueAdapterServiceError } from "../TuiIssueAdapterService.js"
import { TuiIssueAdapterService } from "../TuiIssueAdapterService.js"
import type { TuiSessionAdapterServiceError } from "../TuiSessionAdapterService.js"
import { TuiSessionAdapterService } from "../TuiSessionAdapterService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

type OrchestrateLoadError = TuiIssueAdapterServiceError
type OrchestrateSpawnError = TuiSessionAdapterServiceError | TemplateError

export interface OrchestrateHandlersServiceApi {
	readonly enterFromDetail: () => Effect.Effect<void>
	readonly confirmSpawn: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
}

const toLoadMessage = (error: OrchestrateLoadError): string => {
	switch (error.operation) {
		case "issueShow":
			return `Failed to load task: ${error.message}`
		case "issueGetEpicWithChildren":
			return `Failed to load epic children: ${error.message}`
		case "issueUpdate":
		case "issueAddDependency":
		case "issueRemoveDependency":
			return error.message
	}
}

const toSpawnMessage = (error: OrchestrateSpawnError): string => {
	if (error._tag === "TuiSessionAdapterServiceError") {
		return error.message
	}

	return error.reason
}

const toOrchestrationStatus = (
	status: "open" | "in_progress" | "blocked" | "closed" | "tombstone" | undefined,
): OrchestrationTask["status"] | undefined => {
	switch (status) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
			return status
		case "tombstone":
		case undefined:
			return undefined
	}
}

export class OrchestrateHandlersService extends Effect.Service<OrchestrateHandlersService>()(
	"OrchestrateHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			EditorService.Default,
			OverlayService.Default,
			TuiIssueAdapterService.Default,
			TuiSessionAdapterService.Default,
			TemplateService.Default,
		],
		effect: Effect.gen(function* () {
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const editor = yield* EditorService
			const overlay = yield* OverlayService
			const issueAdapter = yield* TuiIssueAdapterService
			const sessionAdapter = yield* TuiSessionAdapterService
			const templateService = yield* TemplateService

			const enterFromDetail = () =>
				Effect.gen(function* () {
					const current = yield* overlay.current()
					if (current === undefined || current._tag !== "detail") {
						yield* toast.show("error", "No task detail open")
						return
					}

					const task = yield* issueAdapter
						.show(current.taskId)
						.pipe(
							Effect.catchTag("TuiIssueAdapterServiceError", (error) =>
								toast.show("error", toLoadMessage(error)).pipe(Effect.as(undefined)),
							),
						)
					if (task === undefined) {
						return
					}

					if (task.issue_type !== "epic") {
						yield* toast.show("error", "Only epics can be orchestrated")
						return
					}

					const epicWithChildren = yield* issueAdapter
						.getEpicWithChildren(task.id)
						.pipe(
							Effect.catchTag("TuiIssueAdapterServiceError", (error) =>
								toast.show("error", toLoadMessage(error)).pipe(Effect.as(undefined)),
							),
						)
					if (epicWithChildren === undefined) {
						return
					}

					const activeSessionIds = yield* sessionAdapter.listActive().pipe(
						Effect.map((sessions) => new Set(sessions.map((session) => session.issueId))),
						Effect.catchTag("TuiSessionAdapterServiceError", () =>
							Effect.succeed(new Set<string>()),
						),
					)

					const orchestrationTasks: OrchestrationTask[] = []
					for (const child of epicWithChildren.children) {
						const status = toOrchestrationStatus(child.status)
						if (status === undefined) {
							continue
						}
						orchestrationTasks.push({
							id: child.id,
							title: child.title ?? "(untitled)",
							status,
							hasSession: activeSessionIds.has(child.id),
						})
					}

					yield* editor.enterOrchestrate(task.id, task.title, orchestrationTasks)
					yield* overlay.pop()
				})

			const confirmSpawn = (): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const mode = yield* editor.getMode()
					if (mode._tag !== "orchestrate") {
						yield* toast.show("error", "Not in orchestrate mode")
						return
					}

					if (mode.selectedIds.length === 0) {
						yield* toast.show("error", "No tasks selected")
						return
					}

					const projectPath = yield* helpers.getProjectPath()
					const epic = yield* issueAdapter.show(mode.epicId).pipe(
						Effect.map(Option.some),
						Effect.catchTag("TuiIssueAdapterServiceError", () => Effect.succeed(Option.none())),
					)

					yield* editor.exitOrchestrate()

					const spawnResults = yield* Effect.all(
						mode.selectedIds.map((taskId) =>
							Effect.gen(function* () {
								const task = yield* issueAdapter.show(taskId).pipe(
									Effect.map(Option.some),
									Effect.catchTag("TuiIssueAdapterServiceError", () =>
										Effect.succeed(Option.none()),
									),
								)

								const initialPrompt = yield* templateService
									.tryRenderWorkerTemplate(
										{
											TASK_ID: taskId,
											TASK_TITLE: Option.match(task, {
												onNone: () => taskId,
												onSome: (resolvedTask) => resolvedTask.title,
											}),
											TASK_DESCRIPTION: Option.match(task, {
												onNone: () => undefined,
												onSome: (resolvedTask) => resolvedTask.description,
											}),
											TASK_DESIGN: Option.match(task, {
												onNone: () => undefined,
												onSome: (resolvedTask) => resolvedTask.design,
											}),
											EPIC_ID: mode.epicId,
											EPIC_TITLE: mode.epicTitle,
											EPIC_DESIGN: Option.match(epic, {
												onNone: () => undefined,
												onSome: (resolvedEpic) => resolvedEpic.design,
											}),
										},
										projectPath,
									)
									.pipe(
										Effect.map((template) =>
											Option.match(template, {
												onNone: () => `Work on bead ${taskId}`,
												onSome: (rendered) => rendered,
											}),
										),
									)

								return yield* sessionAdapter
									.start(taskId, {
										projectPath,
										initialPrompt,
									})
									.pipe(
										Effect.as(true),
										Effect.catchTags({
											TuiSessionAdapterServiceError: (error) =>
												Effect.logError(`Failed to spawn ${taskId}: ${toSpawnMessage(error)}`).pipe(
													Effect.as(false),
												),
										}),
									)
							}),
						),
						{ concurrency: "unbounded" },
					)

					const successCount = spawnResults.filter(Boolean).length
					yield* toast.show(
						successCount === mode.selectedIds.length ? "success" : "error",
						successCount === mode.selectedIds.length
							? `Spawned ${successCount} session${successCount === 1 ? "" : "s"}`
							: `Spawned ${successCount}/${mode.selectedIds.length} sessions (some failed)`,
					)
				})

			return {
				enterFromDetail,
				confirmSpawn,
			} satisfies OrchestrateHandlersServiceApi
		}),
	},
) {}
