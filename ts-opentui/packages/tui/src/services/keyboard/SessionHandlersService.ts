import { AppConfig } from "@azedarach/config"
import {
	getIssueSessionName,
	issueIdsEqualForLookup,
	parseIssueSessionName,
} from "@azedarach/shared/session-names"
import type { CommandExecutor } from "@effect/platform"
import { FileSystem } from "@effect/platform"
import { Effect } from "effect"
import { hasTaskSessionPresence } from "../../types.js"
import { formatForToast } from "../../utils/formatForToast.js"
import { getWorktreePath } from "../../utils/worktreePaths.js"
import { AttachmentService } from "../AttachmentService.js"
import { ImageAttachmentService } from "../ImageAttachmentService.js"
import { OverlayService } from "../OverlayService.js"
import { PrWorkflowService as PRWorkflow } from "../PrWorkflowService.js"
import { TmuxService } from "../TmuxService.js"
import { ToastService } from "../ToastService.js"
import { TuiBoardStoreService } from "../TuiBoardStoreService.js"
import { TuiSessionAdapterService } from "../TuiSessionAdapterService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"
import { buildStartWorkPrompt } from "./SessionPrompt.js"

const AZEDARACH_PRIME_MODE_ENV = "AZEDARACH_PRIME_MODE"
const QUESTION_FIRST_PRIME_MODE = "question-first"
const HELIX_WINDOW_NAME = "hx"

export interface SessionHandlersServiceApi {
	readonly startSession: () => Effect.Effect<void>
	readonly startSessionWithPrompt: () => Effect.Effect<void>
	readonly startSessionQuestionFirst: () => Effect.Effect<void>
	readonly startSessionDangerous: () => Effect.Effect<void>
	readonly attachExternal: () => Effect.Effect<void>
	readonly attachInline: () => Effect.Effect<void>
	readonly pauseSession: () => Effect.Effect<void>
	readonly resumeSession: () => Effect.Effect<void>
	readonly stopSession: () => Effect.Effect<void>
	readonly startHelixSession: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor>
	readonly recoverCrashedSession: () => Effect.Effect<void>
	readonly recoverAllCrashedSessions: () => Effect.Effect<void>
}

const isSafeImagePath = (imagePath: string): boolean => {
	const trimmed = imagePath.trim()
	if (trimmed.length === 0) return false
	if (trimmed.includes("\u0000") || trimmed.includes("\r") || trimmed.includes("\n")) return false
	return trimmed.startsWith("/") || /^[A-Za-z]:[\\/]/.test(trimmed)
}

export class SessionHandlersService extends Effect.Service<SessionHandlersService>()(
	"SessionHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			AttachmentService.Default,
			ImageAttachmentService.Default,
			TmuxService.Default,
			AppConfig.Default,
			PRWorkflow.Default,
			OverlayService.Default,
			TuiBoardStoreService.Default,
			TuiSessionAdapterService.Default,
		],
		effect: Effect.gen(function* () {
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const attachment = yield* AttachmentService
			const imageAttachment = yield* ImageAttachmentService
			const fs = yield* FileSystem.FileSystem
			const tmux = yield* TmuxService
			const appConfig = yield* AppConfig
			const prWorkflow = yield* PRWorkflow
			const overlay = yield* OverlayService
			const boardService = yield* TuiBoardStoreService
			const sessionAdapter = yield* TuiSessionAdapterService
			const gitConfig = yield* appConfig.getGitConfig()

			const findAiSession = (issueId: string, projectPath: string) =>
				Effect.gen(function* () {
					const snapshots = yield* sessionAdapter
						.listActive({ projectPath })
						.pipe(Effect.catchAll(() => Effect.succeed([])))
					for (const snapshot of snapshots) {
						if (issueIdsEqualForLookup(snapshot.issueId, issueId)) {
							return snapshot.tmuxSessionName
						}
					}

					const canonicalSessionName = getIssueSessionName(issueId, projectPath)
					const hasCanonicalSession = yield* tmux.hasSession(canonicalSessionName)
					if (hasCanonicalSession) {
						return canonicalSessionName
					}

					const sessions = yield* tmux.listSessions()
					for (const session of sessions) {
						const parsed = parseIssueSessionName(session.name, projectPath)
						if (parsed?.type === "issue" && issueIdsEqualForLookup(parsed.issueId, issueId)) {
							return session.name
						}
					}

					return null
				})

			const validateSafeExistingImagePath = (
				taskId: string,
				candidatePath: string,
				source: "worktree" | "project",
			) =>
				Effect.gen(function* () {
					if (!isSafeImagePath(candidatePath)) {
						yield* Effect.logWarning(
							`Skipping unsafe ${source} attachment path for ${taskId}: ${JSON.stringify(candidatePath)}`,
						)
						return null
					}

					const exists = yield* fs.exists(candidatePath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						yield* Effect.logWarning(
							`Skipping missing ${source} attachment path for ${taskId}: ${candidatePath}`,
						)
						return null
					}

					return candidatePath
				})

			const resolveAttachmentPathWithFallback = (
				taskId: string,
				attachmentId: string,
				worktreePath: string,
			) =>
				Effect.gen(function* () {
					const worktreeCandidate = yield* imageAttachment
						.getPathForProjectRoot(taskId, attachmentId, worktreePath)
						.pipe(Effect.orElseSucceed(() => ""))
					if (worktreeCandidate.length > 0) {
						const validatedWorktreePath = yield* validateSafeExistingImagePath(
							taskId,
							worktreeCandidate,
							"worktree",
						)
						if (validatedWorktreePath !== null) {
							return validatedWorktreePath
						}
					}

					const projectCandidate = yield* imageAttachment
						.getPath(taskId, attachmentId)
						.pipe(Effect.orElseSucceed(() => ""))
					if (projectCandidate.length === 0) {
						return null
					}
					return yield* validateSafeExistingImagePath(taskId, projectCandidate, "project")
				})

			const resolveSessionImagePaths = (taskId: string, projectPath: string) =>
				Effect.gen(function* () {
					const worktreePath = getWorktreePath(projectPath, taskId)
					const attachments = yield* imageAttachment
						.list(taskId)
						.pipe(Effect.catchAll(() => Effect.succeed([])))
					const resolved = yield* Effect.forEach(attachments, (currentAttachment) =>
						resolveAttachmentPathWithFallback(taskId, currentAttachment.id, worktreePath),
					)
					return resolved.filter((value): value is string => value !== null)
				})

			const runStart = (options: {
				readonly issueId: string
				readonly projectPath: string
				readonly successMessage: string
				readonly initialPrompt?: string
				readonly imagePaths?: ReadonlyArray<string>
				readonly sessionEnv?: Readonly<Record<string, string>>
				readonly dangerouslySkipPermissions?: boolean
			}) =>
				helpers.withQueue(
					options.issueId,
					"start",
					sessionAdapter
						.start(options.issueId, {
							projectPath: options.projectPath,
							initialPrompt: options.initialPrompt,
							imagePaths: options.imagePaths,
							sessionEnv: options.sessionEnv,
							dangerouslySkipPermissions: options.dangerouslySkipPermissions,
						})
						.pipe(
							Effect.tap(() => toast.show("success", options.successMessage)),
							Effect.tap(() => boardService.refresh()),
							Effect.catchAll(helpers.showErrorToast("Failed to start")),
						),
					options.projectPath,
				)

			const doAttach = (issueId: string, projectPath: string) =>
				Effect.gen(function* () {
					const sessionName = yield* findAiSession(issueId, projectPath)
					if (sessionName === null) {
						yield* toast.show("error", `No session for ${issueId} - press Space+s to start`)
						return
					}
					yield* attachment.attachExternal(sessionName)
					yield* toast.show("info", "Switched! Ctrl-a Ctrl-a to return")
				}).pipe(
					Effect.catchAll((error) =>
						Effect.gen(function* () {
							yield* Effect.logError("Attach external failed", error)
							yield* toast.show("error", formatForToast(error))
						}),
					),
				)

			const startSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					const projectPath = yield* helpers.getProjectPath()
					if (yield* helpers.checkBusy(task.id, projectPath)) return
					if (hasTaskSessionPresence(task)) {
						const stateLabel =
							task.sessionState !== "idle"
								? task.sessionState
								: task.hasTmuxSession
									? "tmux-present"
									: "idle"
						yield* toast.show("error", `Cannot start: task is ${stateLabel}`)
						return
					}
					yield* runStart({
						issueId: task.id,
						projectPath,
						successMessage: `Started session for ${task.id}`,
					})
				})

			const startSessionWithPrompt = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					const projectPath = yield* helpers.getProjectPath()
					if (yield* helpers.checkBusy(task.id, projectPath)) return
					if (hasTaskSessionPresence(task)) {
						const stateLabel =
							task.sessionState !== "idle"
								? task.sessionState
								: task.hasTmuxSession
									? "tmux-present"
									: "idle"
						yield* toast.show("error", `Cannot start: task is ${stateLabel}`)
						return
					}

					const cliTool = yield* appConfig.getCliTool()
					const imagePaths =
						cliTool === "codex" ? yield* resolveSessionImagePaths(task.id, projectPath) : undefined
					const initialPrompt = buildStartWorkPrompt({
						taskId: task.id,
						issueType: task.issue_type,
						title: task.title,
					})

					yield* runStart({
						issueId: task.id,
						projectPath,
						initialPrompt,
						imagePaths,
						successMessage: task.hasWorktree
							? `Resumed session for ${task.id} on existing worktree`
							: `Started session for ${task.id} with prompt`,
					})
				})

			const startSessionQuestionFirst = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					const projectPath = yield* helpers.getProjectPath()
					if (yield* helpers.checkBusy(task.id, projectPath)) return
					if (hasTaskSessionPresence(task)) {
						const stateLabel =
							task.sessionState !== "idle"
								? task.sessionState
								: task.hasTmuxSession
									? "tmux-present"
									: "idle"
						yield* toast.show("error", `Cannot start: task is ${stateLabel}`)
						return
					}

					const cliTool = yield* appConfig.getCliTool()
					const imagePaths =
						cliTool === "codex" ? yield* resolveSessionImagePaths(task.id, projectPath) : undefined
					const initialPrompt = buildStartWorkPrompt({
						taskId: task.id,
						issueType: task.issue_type,
						title: task.title,
					})

					yield* runStart({
						issueId: task.id,
						projectPath,
						initialPrompt,
						imagePaths,
						sessionEnv: {
							[AZEDARACH_PRIME_MODE_ENV]: QUESTION_FIRST_PRIME_MODE,
						},
						successMessage: task.hasWorktree
							? `Resumed session for ${task.id} with question-first primer`
							: `Started session for ${task.id} with question-first primer`,
					})
				})

			const startSessionDangerous = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					const projectPath = yield* helpers.getProjectPath()
					if (yield* helpers.checkBusy(task.id, projectPath)) return
					if (hasTaskSessionPresence(task)) {
						const stateLabel =
							task.sessionState !== "idle"
								? task.sessionState
								: task.hasTmuxSession
									? "tmux-present"
									: "idle"
						yield* toast.show("error", `Cannot start: task is ${stateLabel}`)
						return
					}

					const cliTool = yield* appConfig.getCliTool()
					const imagePaths =
						cliTool === "codex" ? yield* resolveSessionImagePaths(task.id, projectPath) : undefined
					const initialPrompt = buildStartWorkPrompt({
						taskId: task.id,
						issueType: task.issue_type,
						title: task.title,
					})

					yield* runStart({
						issueId: task.id,
						projectPath,
						initialPrompt,
						imagePaths,
						dangerouslySkipPermissions: true,
						successMessage: task.hasWorktree
							? `Resumed session for ${task.id} (skip-permissions)`
							: `Started session for ${task.id} (skip-permissions)`,
					})
				})

			const attachExternal = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return

					const projectPath = yield* helpers.getProjectPath()
					const branchStatus = yield* prWorkflow
						.checkBranchBehindBase({ issueId: task.id, projectPath })
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(error).pipe(
									Effect.zipRight(
										Effect.succeed({
											behind: 0,
											ahead: 0,
											baseBranch: task.parentEpicId ?? gitConfig.baseBranch,
										}),
									),
								),
							),
						)

					if (branchStatus.behind === 0) {
						yield* doAttach(task.id, projectPath)
						return
					}

					if (task.hasMergeConflict) {
						yield* toast.show("info", "Merge in progress - attaching directly")
						yield* doAttach(task.id, projectPath)
						return
					}

					const baseBranch = branchStatus.baseBranch
					yield* overlay.push({
						_tag: "mergeChoice",
						message: `Merge ${baseBranch} into your branch before attaching?`,
						commitsBehind: branchStatus.behind,
						baseBranch,
						onMerge: Effect.gen(function* () {
							yield* toast.show("info", `Merging ${baseBranch} into branch...`)
							yield* prWorkflow.mergeBaseIntoBranch({ issueId: task.id, projectPath }).pipe(
								Effect.tap(() => toast.show("success", "Merged! Attaching...")),
								Effect.tap(() => boardService.refresh()),
								Effect.tap(() => doAttach(task.id, projectPath)),
								Effect.catchAll((error) =>
									Effect.gen(function* () {
										yield* Effect.logError("Merge before attach failed", error)
										yield* toast.show("error", formatForToast(error))
									}),
								),
							)
						}),
						onSkip: doAttach(task.id, projectPath),
					})
				})

			const attachInline = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					yield* attachment
						.attachInline(task.id)
						.pipe(Effect.catchAll(helpers.showErrorToast("Failed to attach")))
				})

			const pauseSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					if (task.sessionState !== "busy") {
						yield* toast.show("error", `Cannot pause: task is ${task.sessionState}`)
						return
					}
					const projectPath = yield* helpers.getProjectPath()
					yield* sessionAdapter.pause(task.id, { projectPath }).pipe(
						Effect.tap(() => toast.show("success", `Paused session for ${task.id}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to pause")),
					)
				})

			const resumeSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					if (task.sessionState !== "paused") {
						yield* toast.show("error", `Cannot resume: task is ${task.sessionState}`)
						return
					}
					const projectPath = yield* helpers.getProjectPath()
					yield* sessionAdapter.resume(task.id, { projectPath }).pipe(
						Effect.tap(() => toast.show("success", `Resumed session for ${task.id}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to resume")),
					)
				})

			const stopSession = () =>
				Effect.gen(function* () {
					const tasks = yield* helpers.getActionTargetTasks()
					if (tasks.length === 0) return

					const tasksWithSessions = tasks.filter((task) => hasTaskSessionPresence(task))
					if (tasksWithSessions.length === 0) {
						yield* toast.show("error", "No sessions to stop")
						return
					}

					yield* Effect.all(
						tasksWithSessions.map((task) =>
							Effect.gen(function* () {
								const projectPath = yield* helpers.getProjectPath()
								const isBusy = yield* helpers.checkBusy(task.id, projectPath)
								if (isBusy) return

								yield* helpers.withQueue(
									task.id,
									"stop",
									sessionAdapter.stop(task.id, { projectPath }).pipe(
										Effect.tap(() =>
											tasksWithSessions.length === 1
												? toast.show("success", `Stopped session for ${task.id}`)
												: Effect.void,
										),
										Effect.catchAll(helpers.showErrorToast(`Failed to stop ${task.id}`)),
									),
									projectPath,
								)
							}),
						),
						{ concurrency: "unbounded" },
					)

					if (tasksWithSessions.length > 1) {
						yield* toast.show("success", `Stopped ${tasksWithSessions.length} sessions`)
					}
				})

			const startHelixSession: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor> =
				() =>
					Effect.gen(function* () {
						const task = yield* helpers.getActionTargetTask()
						if (task === undefined) return
						const projectPath = yield* helpers.getProjectPath()

						let sessionName = yield* findAiSession(task.id, projectPath)
						let worktreePath = getWorktreePath(projectPath, task.id)
						if (sessionName === null) {
							yield* toast.show("info", `Starting session for ${task.id}...`)
							const started = yield* sessionAdapter.start(task.id, { projectPath }).pipe(
								Effect.map((session) => session),
								Effect.catchAll((error) =>
									helpers.showErrorToast("Failed to start session")(error).pipe(Effect.as(null)),
								),
							)
							if (started === null) return
							sessionName = started.tmuxSessionName
							if (started.worktreePath !== null) {
								worktreePath = started.worktreePath
							}
						}

						const sessionConfig = yield* appConfig.getSessionConfig()
						const shell = sessionConfig.shell
						const hasHelixWindow = yield* tmux.hasWindow(sessionName, HELIX_WINDOW_NAME)
						if (hasHelixWindow) {
							yield* tmux.selectWindow(sessionName, HELIX_WINDOW_NAME)
						} else {
							yield* tmux.newWindow(sessionName, HELIX_WINDOW_NAME, {
								command: `${shell} -i -c 'hx .; exec ${shell}'`,
								cwd: worktreePath,
							})
						}

						yield* toast.show("success", `Helix ready for ${task.id} - press Space+a to attach`)
					}).pipe(Effect.catchAll((error) => helpers.showErrorToast("Failed to open Helix")(error)))

			const recoverCrashedSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (task === undefined) return
					if (task.sessionState !== "crashed") {
						yield* toast.show("error", `Cannot recover: task is ${task.sessionState}`)
						return
					}

					yield* toast.show("info", `Recovering session for ${task.id}...`)
					const projectPath = yield* helpers.getProjectPath()
					yield* sessionAdapter.recover(task.id, { projectPath }).pipe(
						Effect.tap(() => toast.show("success", `Recovered session for ${task.id}`)),
						Effect.tap(() => boardService.refresh()),
						Effect.catchAll(helpers.showErrorToast("Failed to recover")),
					)
				})

			const recoverAllCrashedSessions = () =>
				Effect.gen(function* () {
					const allTasks = yield* boardService.getTasks()
					const crashedTasks = allTasks.filter((task) => task.sessionState === "crashed")
					if (crashedTasks.length === 0) {
						yield* toast.show("info", "No crashed sessions to recover")
						return
					}

					yield* toast.show("info", `Recovering ${crashedTasks.length} crashed session(s)...`)
					const projectPath = yield* helpers.getProjectPath()
					let recovered = 0
					for (const task of crashedTasks) {
						yield* sessionAdapter.recover(task.id, { projectPath }).pipe(
							Effect.tap(() => {
								recovered += 1
							}),
							Effect.catchAll((error) =>
								Effect.log(`Failed to recover ${task.id}: ${formatForToast(error)}`),
							),
						)
					}

					yield* boardService.refresh()
					yield* toast.show("success", `Recovered ${recovered}/${crashedTasks.length} sessions`)
				}).pipe(Effect.catchAll(helpers.showErrorToast("Failed to recover crashed sessions")))

			return {
				startSession,
				startSessionWithPrompt,
				startSessionQuestionFirst,
				startSessionDangerous,
				attachExternal,
				attachInline,
				pauseSession,
				resumeSession,
				stopSession,
				startHelixSession,
				recoverCrashedSession,
				recoverAllCrashedSessions,
			}
		}),
	},
) {}
