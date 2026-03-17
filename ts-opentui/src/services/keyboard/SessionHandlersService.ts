/**
 * SessionHandlersService
 *
 * Handles Claude session lifecycle:
 * - Start session (s) / Start with prompt (S)
 * - Attach external (a) / Attach inline (A)
 * - Pause (p) / Resume (r)
 * - Stop (x)
 * - Start Helix editor (H)
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { type CommandExecutor, FileSystem } from "@effect/platform"
import { Effect, Option } from "effect"
import { AppConfig } from "../../config/index.js"
import { AttachmentService } from "../../core/AttachmentService.js"
import { ImageAttachmentService } from "../../core/ImageAttachmentService.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import {
	getIssueSessionName,
	getWorktreePath,
	issueIdsEqualForLookup,
	parseIssueSessionName,
	WINDOW_NAMES,
} from "../../core/paths.js"
import { SessionManager } from "../../core/SessionManager.js"
import { TmuxService } from "../../core/TmuxService.js"
import { WorktreeManager, type WorktreeNameClashError } from "../../core/WorktreeManager.js"
import { WorktreeSessionService } from "../../core/WorktreeSessionService.js"
import { DaemonRpcClient } from "../../rpc/DaemonRpcClient.js"
import { hasTaskSessionPresence } from "../../ui/types.js"
import { BoardService } from "../BoardService.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"
import { buildStartWorkPrompt } from "./SessionPrompt.js"

const AZEDARACH_PRIME_MODE_ENV = "AZEDARACH_PRIME_MODE"
const QUESTION_FIRST_PRIME_MODE = "question-first"

// ============================================================================
// Service Definition
// ============================================================================

export class SessionHandlersService extends Effect.Service<SessionHandlersService>()(
	"SessionHandlersService",
	{
		dependencies: [
			KeyboardHelpersService.Default,
			ToastService.Default,
			SessionManager.Default,
			AttachmentService.Default,
			ImageAttachmentService.Default,
			TmuxService.Default,
			WorktreeSessionService.Default,
			WorktreeManager.Default,
			AppConfig.Default,
			PRWorkflow.Default,
			OverlayService.Default,
			BoardService.Default,
		],

		effect: Effect.gen(function* () {
			const helpers = yield* KeyboardHelpersService
			const toast = yield* ToastService
			const sessionManager = yield* SessionManager
			const attachment = yield* AttachmentService
			const imageAttachment = yield* ImageAttachmentService
			const fs = yield* FileSystem.FileSystem
			const tmux = yield* TmuxService
			const worktreeSession = yield* WorktreeSessionService
			const worktreeManager = yield* WorktreeManager
			const appConfig = yield* AppConfig
			const prWorkflow = yield* PRWorkflow
			const overlay = yield* OverlayService
			const boardService = yield* BoardService
			const daemonRpcClient = yield* Effect.serviceOption(DaemonRpcClient)
			const gitConfig = yield* appConfig.getGitConfig()

			const startSessionWithPreferredRuntime = (options: {
				readonly issueId: string
				readonly projectPath: string
				readonly initialPrompt?: string
				readonly imagePaths?: ReadonlyArray<string>
				readonly sessionEnv?: Readonly<Record<string, string>>
				readonly dangerouslySkipPermissions?: boolean
			}): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					if (Option.isSome(daemonRpcClient) && daemonRpcClient.value.sessionStart !== undefined) {
						// Current daemon RPC start does not support prompt/image/skip-permissions arguments yet.
						if (
							options.initialPrompt === undefined &&
							options.imagePaths === undefined &&
							options.sessionEnv === undefined &&
							options.dangerouslySkipPermissions !== true
						) {
							yield* daemonRpcClient.value.sessionStart({
								issueId: options.issueId,
								projectPath: options.projectPath,
							})
							return
						}
					}

					yield* sessionManager.start(options)
				}).pipe(Effect.asVoid)

			const stopSessionWithPreferredRuntime = (
				issueId: string,
				projectPath?: string,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					if (Option.isSome(daemonRpcClient) && daemonRpcClient.value.sessionStop !== undefined) {
						const effectiveProjectPath = projectPath ?? (yield* helpers.getProjectPath())
						yield* daemonRpcClient.value.sessionStop({
							issueId,
							projectPath: effectiveProjectPath,
						})
						return
					}
					yield* sessionManager.stop(issueId)
				}).pipe(Effect.asVoid)

			const pauseSessionWithPreferredRuntime = (
				issueId: string,
				projectPath?: string,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					if (Option.isSome(daemonRpcClient) && daemonRpcClient.value.sessionPause !== undefined) {
						const effectiveProjectPath = projectPath ?? (yield* helpers.getProjectPath())
						yield* daemonRpcClient.value.sessionPause({
							issueId,
							projectPath: effectiveProjectPath,
						})
						return
					}
					yield* sessionManager.pause(issueId)
				}).pipe(Effect.asVoid)

			const resumeSessionWithPreferredRuntime = (
				issueId: string,
				projectPath?: string,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					if (Option.isSome(daemonRpcClient) && daemonRpcClient.value.sessionResume !== undefined) {
						const effectiveProjectPath = projectPath ?? (yield* helpers.getProjectPath())
						yield* daemonRpcClient.value.sessionResume({
							issueId,
							projectPath: effectiveProjectPath,
						})
						return
					}
					yield* sessionManager.resume(issueId)
				}).pipe(Effect.asVoid)

			const recoverSessionWithPreferredRuntime = (
				issueId: string,
				projectPath?: string,
			): Effect.Effect<void, unknown, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					if (
						Option.isSome(daemonRpcClient) &&
						daemonRpcClient.value.sessionRecover !== undefined
					) {
						const effectiveProjectPath = projectPath ?? (yield* helpers.getProjectPath())
						yield* daemonRpcClient.value.sessionRecover({
							issueId,
							projectPath: effectiveProjectPath,
						})
						return
					}
					yield* sessionManager.recoverSession(issueId)
				}).pipe(Effect.asVoid)

			const buildWorktreeClashMessage = (error: WorktreeNameClashError): string => {
				const aheadRisk =
					error.commitsAheadOfBase === undefined
						? `Could not determine whether the duplicate worktree has commits not in ${error.baseBranch}.`
						: error.commitsAheadOfBase > 0
							? `${error.commitsAheadOfBase} commit(s) in the duplicate worktree are not in ${error.baseBranch}.`
							: `No extra commits detected in the duplicate worktree versus ${error.baseBranch}.`
				const dirtyRisk =
					error.uncommittedFileCount > 0
						? `${error.uncommittedFileCount} uncommitted file(s) detected in the duplicate worktree.`
						: "No uncommitted changes detected in the duplicate worktree."
				const requestedBranchLine = error.requestedBranch
					? `Requested branch: ${error.requestedBranch}\n`
					: ""
				const conflictReason =
					error.conflictKind === "branch"
						? "Derived branch name collision detected."
						: "Derived worktree path collision detected."

				return `Worktree name clash for ${error.issueId}.

${conflictReason}
Conflicting issue: ${error.conflictingIssueId}
Conflicting worktree: ${error.conflictingWorktreePath}
Conflicting branch: ${error.conflictingBranch || "(unknown)"}
Requested worktree: ${error.requestedWorktreePath}
${requestedBranchLine}
Before deleting the duplicate worktree:
- ${aheadRisk}
- ${dirtyRisk}

Delete the duplicate worktree and retry?`
			}

			const promptWorktreeClashResolution = (options: {
				error: WorktreeNameClashError
				projectPath: string
				retry: Effect.Effect<void, never, CommandExecutor.CommandExecutor>
			}) =>
				Effect.gen(function* () {
					const { error, projectPath, retry } = options

					yield* overlay.push({
						_tag: "confirm",
						message: buildWorktreeClashMessage(error),
						onConfirm: Effect.gen(function* () {
							yield* toast.show(
								"info",
								`Removing duplicate worktree for ${error.conflictingIssueId}...`,
							)

							const removed = yield* worktreeManager
								.remove({
									issueId: error.conflictingIssueId,
									projectPath,
								})
								.pipe(
									Effect.map(() => true),
									Effect.catchAll((removeError) =>
										Effect.logWarning(removeError).pipe(
											Effect.zipRight(
												helpers
													.showErrorToast("Failed to remove duplicate worktree")(removeError)
													.pipe(Effect.as(false)),
											),
										),
									),
								)

							if (!removed) {
								return
							}

							yield* boardService
								.refresh()
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								)
							yield* retry
						}),
					})
				})

			const isSafeImagePath = (imagePath: string): boolean => {
				const trimmed = imagePath.trim()
				if (trimmed.length === 0) return false
				if (trimmed.includes("\u0000") || trimmed.includes("\r") || trimmed.includes("\n")) {
					return false
				}
				return trimmed.startsWith("/") || /^[A-Za-z]:[\\/]/.test(trimmed)
			}

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

					const exists = yield* fs
						.exists(candidatePath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)

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
						.pipe(
							Effect.tapError((error) =>
								Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
							),
							Effect.orElseSucceed(() => ""),
						)
					if (worktreeCandidate.length > 0) {
						const validatedWorktreePath = yield* validateSafeExistingImagePath(
							taskId,
							worktreeCandidate,
							"worktree",
						)
						if (validatedWorktreePath) {
							return validatedWorktreePath
						}

						yield* Effect.logWarning(
							`Falling back to project-root attachment materialization for ${taskId}/${attachmentId}`,
						)
					}

					const projectCandidate = yield* imageAttachment.getPath(taskId, attachmentId).pipe(
						Effect.tapError((error) =>
							Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
						),
						Effect.orElseSucceed(() => ""),
					)
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
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed([] as const)),
								),
							),
						)
					const resolved = yield* Effect.forEach(attachments, (attachment) =>
						resolveAttachmentPathWithFallback(taskId, attachment.id, worktreePath),
					)

					return resolved.filter((path): path is string => path !== null)
				})

			const isWorktreeNameClashError = (error: unknown): error is WorktreeNameClashError =>
				typeof error === "object" &&
				error !== null &&
				"_tag" in error &&
				error._tag === "WorktreeNameClashError"

			const runStartWithClashRecovery = <A, E>(options: {
				issueId: string
				projectPath: string
				successMessage: string
				startEffect: Effect.Effect<A, E, CommandExecutor.CommandExecutor>
			}) => {
				const attemptStart = (): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
					options.startEffect.pipe(
						Effect.tap(() => toast.show("success", options.successMessage)),
						Effect.asVoid,
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(
									isWorktreeNameClashError(error)
										? promptWorktreeClashResolution({
												error,
												projectPath: options.projectPath,
												retry: helpers.withQueue(options.issueId, "start", attemptStart()),
											})
										: helpers.showErrorToast("Failed to start")(error),
								),
							),
						),
					)

				return helpers.withQueue(options.issueId, "start", attemptStart())
			}

			// ================================================================
			// Session Handler Methods
			// ================================================================

			/**
			 * Start session action (Space+s)
			 *
			 * Starts a Claude session for the currently selected task.
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const startSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					const projectPath = yield* helpers.getProjectPath()

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id, projectPath)
					if (isBusy) return

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

					yield* runStartWithClashRecovery({
						issueId: task.id,
						projectPath,
						successMessage: `Started session for ${task.id}`,
						startEffect: startSessionWithPreferredRuntime({ issueId: task.id, projectPath }),
					})
				})

			/**
			 * Start session with initial prompt (Space+S)
			 *
			 * Starts the agent with a short task-specific prompt that tells it to run
			 * `az prime` first and then continue working without waiting.
			 * Codex still receives attached images as native CLI image arguments.
			 *
			 * Existing worktrees are still surfaced through the success message so users
			 * can distinguish fresh starts from resumed sessions.
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const startSessionWithPrompt = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Get current project path (from ProjectService or cwd fallback)
					const projectPath = yield* helpers.getProjectPath()

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id, projectPath)
					if (isBusy) return

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
					// Space+S uses a short task prompt that tells the agent to run
					// `az prime` first; only Codex still needs native image arguments
					// because they are out-of-band from primer text.
					const imagePaths =
						cliTool === "codex" ? yield* resolveSessionImagePaths(task.id, projectPath) : undefined
					const initialPrompt = buildStartWorkPrompt({
						taskId: task.id,
						issueType: task.issue_type,
						title: task.title,
					})

					yield* runStartWithClashRecovery({
						issueId: task.id,
						projectPath,
						successMessage: task.hasWorktree
							? `Resumed session for ${task.id} on existing worktree`
							: `Started session for ${task.id} with prompt`,
						startEffect: startSessionWithPreferredRuntime({
							issueId: task.id,
							projectPath,
							initialPrompt,
							imagePaths,
						}),
					})
				})

			/**
			 * Start session with question-first primer mode (Space+Q)
			 *
			 * Starts the agent with the same initial prompt as Space+S and also injects
			 * `AZEDARACH_PRIME_MODE=question-first` so `az prime` emits question-first
			 * guidance before implementation.
			 */
			const startSessionQuestionFirst = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					const projectPath = yield* helpers.getProjectPath()
					const isBusy = yield* helpers.checkBusy(task.id, projectPath)
					if (isBusy) return

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

					yield* runStartWithClashRecovery({
						issueId: task.id,
						projectPath,
						successMessage: task.hasWorktree
							? `Resumed session for ${task.id} with question-first primer`
							: `Started session for ${task.id} with question-first primer`,
						startEffect: startSessionWithPreferredRuntime({
							issueId: task.id,
							projectPath,
							initialPrompt,
							imagePaths,
							sessionEnv: {
								[AZEDARACH_PRIME_MODE_ENV]: QUESTION_FIRST_PRIME_MODE,
							},
						}),
					})
				})

			/**
			 * Start session with prompt and --dangerously-skip-permissions (Space+!)
			 *
			 * Starts the agent with the same short task-specific prompt as Space+S and
			 * also passes --dangerously-skip-permissions.
			 * This allows the agent to run without permission prompts and should be used
			 * only for trusted tasks.
			 *
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const startSessionDangerous = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Get current project path
					const projectPath = yield* helpers.getProjectPath()

					// Check if task has an operation in progress
					const isBusy = yield* helpers.checkBusy(task.id, projectPath)
					if (isBusy) return

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

					yield* runStartWithClashRecovery({
						issueId: task.id,
						projectPath,
						successMessage: task.hasWorktree
							? `Resumed session for ${task.id} (skip-permissions)`
							: `Started session for ${task.id} (skip-permissions)`,
						startEffect: startSessionWithPreferredRuntime({
							issueId: task.id,
							projectPath,
							initialPrompt,
							imagePaths,
							dangerouslySkipPermissions: true,
						}),
					})
				})

			const findAiSession = (issueId: string, projectPath?: string) =>
				Effect.gen(function* () {
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

			const doAttach = (issueId: string) =>
				Effect.gen(function* () {
					const projectPath = yield* helpers
						.getProjectPath()
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(undefined)),
								),
							),
						)
					const sessionName = yield* findAiSession(issueId, projectPath)
					if (!sessionName) {
						yield* toast.show("error", `No session for ${issueId} - press Space+s to start`)
						return
					}
					yield* attachment.attachExternal(sessionName)
					yield* toast.show("info", "Switched! Ctrl-a Ctrl-a to return")
				}).pipe(
					Effect.catchAll((error) => {
						const errorObj = error && typeof error === "object" ? error : {}
						const msg =
							"_tag" in errorObj
								? String("message" in errorObj ? errorObj.message : error)
								: String(error)
						return Effect.gen(function* () {
							yield* Effect.logError(`Attach external: ${msg}`, { error })
							yield* toast.show("error", msg)
						})
					}),
				)

			/**
			 * Attach to session externally (Space+a)
			 *
			 * Switches to the tmux session in a new terminal window.
			 * After successful attach, checks for PR comments and injects them into the session.
			 * The user can return with Ctrl-a Ctrl-a.
			 *
			 * If the worktree branch is behind main, shows a choice dialog:
			 * - Merge & Attach: merges main into branch, then attaches
			 * - Skip & Attach: attaches without merging
			 * - Cancel: returns to board
			 *
			 * If merge has conflicts, spawns Claude to resolve them.
			 */
			const attachExternal = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					// Get current project path
					const projectPath = yield* helpers.getProjectPath()

					// Check if branch is behind its base branch (epic branch for children, main for others)
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

					// If not behind, just attach directly
					if (branchStatus.behind === 0) {
						yield* doAttach(task.id)
						return
					}

					// If merge already in progress (MERGE_HEAD exists), skip merge choice
					// and attach directly - user is likely resuming conflict resolution
					if (task.hasMergeConflict) {
						yield* toast.show("info", "Merge in progress - attaching directly")
						yield* doAttach(task.id)
						return
					}

					// Branch is behind - show merge choice dialog
					const baseBranch = branchStatus.baseBranch
					const message = `Merge ${baseBranch} into your branch before attaching?`

					// Define the merge action (merge base branch, then attach)
					const onMerge = Effect.gen(function* () {
						yield* toast.show("info", `Merging ${baseBranch} into branch...`)
						yield* prWorkflow.mergeBaseIntoBranch({ issueId: task.id, projectPath }).pipe(
							Effect.tap(() => toast.show("success", "Merged! Attaching...")),
							Effect.tap(() => boardService.refresh()),
							Effect.tap(() => doAttach(task.id)),
							Effect.catchAll((error) => {
								// MergeConflictError means Claude was started to resolve
								const errorObj = error && typeof error === "object" ? error : {}
								const msg =
									"_tag" in errorObj
										? errorObj._tag === "MergeConflictError"
											? String("message" in errorObj ? errorObj.message : "Conflicts detected")
											: String("message" in errorObj ? errorObj.message : error)
										: String(error)
								return Effect.gen(function* () {
									yield* Effect.logError(`Merge failed: ${msg}`, { error })
									yield* toast.show("error", msg)
								})
							}),
						)
					})

					// Define the skip action (attach directly without merging)
					const onSkip = doAttach(task.id)

					// Show the merge choice dialog
					yield* overlay.push({
						_tag: "mergeChoice",
						message,
						commitsBehind: branchStatus.behind,
						baseBranch,
						onMerge,
						onSkip,
					})
				})

			/**
			 * Attach to session inline (Space+A)
			 *
			 * Embeds the tmux session output in the current TUI.
			 */
			const attachInline = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					yield* attachment
						.attachInline(task.id)
						.pipe(Effect.catchAll(helpers.showErrorToast("Failed to attach")))
				})

			/**
			 * Pause session action (Space+p)
			 *
			 * Pauses an active Claude session. Only valid when session is busy.
			 */
			const pauseSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					if (task.sessionState !== "busy") {
						yield* toast.show("error", `Cannot pause: task is ${task.sessionState}`)
						return
					}

					const projectPath = yield* helpers.getProjectPath()

					yield* pauseSessionWithPreferredRuntime(task.id, projectPath).pipe(
						Effect.tap(() => toast.show("success", `Paused session for ${task.id}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to pause")),
					)
				})

			/**
			 * Resume session action (Space+r)
			 *
			 * Resumes a paused Claude session. Only valid when session is paused.
			 */
			const resumeSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					if (task.sessionState !== "paused") {
						yield* toast.show("error", `Cannot resume: task is ${task.sessionState}`)
						return
					}

					const projectPath = yield* helpers.getProjectPath()

					yield* resumeSessionWithPreferredRuntime(task.id, projectPath).pipe(
						Effect.tap(() => toast.show("success", `Resumed session for ${task.id}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to resume")),
					)
				})

			/**
			 * Stop session action (Space+x)
			 *
			 * Stops running Claude session(s) and cleans up resources.
			 * Supports bulk operations when multiple tasks are selected.
			 * Queued to prevent race conditions with other operations on the same task.
			 * Blocked if task already has an operation in progress.
			 */
			const stopSession = () =>
				Effect.gen(function* () {
					const tasks = yield* helpers.getActionTargetTasks()
					if (tasks.length === 0) return

					// Filter to tasks with active or recoverable tmux sessions.
					const tasksWithSessions = tasks.filter((task) => hasTaskSessionPresence(task))

					if (tasksWithSessions.length === 0) {
						yield* toast.show("error", "No sessions to stop")
						return
					}

					// Stop all sessions in parallel
					yield* Effect.all(
						tasksWithSessions.map((task) =>
							Effect.gen(function* () {
								const projectPath = yield* helpers.getProjectPath()

								// Check if task has an operation in progress
								const isBusy = yield* helpers.checkBusy(task.id, projectPath)
								if (isBusy) return

								yield* helpers.withQueue(
									task.id,
									"stop",
									stopSessionWithPreferredRuntime(task.id, projectPath).pipe(
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

			/**
			 * Start Helix editor in a tmux window (Space+H)
			 *
			 * Opens Helix editor in a dedicated "hx" window within the bead's tmux session.
			 * If no session exists, creates the worktree and session first.
			 *
			 * Unlike Claude sessions, this is always available - works for both idle
			 * and running tasks. For idle tasks, creates the worktree/session without
			 * starting Claude.
			 */
			const startHelixSession: () => Effect.Effect<void, never, CommandExecutor.CommandExecutor> =
				() =>
					Effect.gen(function* () {
						const task = yield* helpers.getActionTargetTask()
						if (!task) return

						const projectPath = yield* helpers.getProjectPath()
						const sessionConfig = yield* appConfig.getSessionConfig()
						const worktreeConfig = yield* appConfig.getWorktreeConfig()
						const gitConfig = yield* appConfig.getGitConfig()
						const shell = sessionConfig.shell
						const existingSessionName = yield* findAiSession(task.id, projectPath)
						const sessionName = existingSessionName ?? getIssueSessionName(task.id, projectPath)

						// Check if session already exists
						const hasSession = yield* tmux.hasSession(sessionName)

						if (!hasSession) {
							// No session - create worktree and session first
							yield* toast.show("info", `Creating worktree for ${task.id}...`)

							// Create worktree (idempotent - returns existing if present)
							const worktree = yield* worktreeManager
								.create({
									issueId: task.id,
									issueTitle: task.title,
									branchSlugMaxLength: gitConfig.branchSlugMaxLength,
									projectPath,
								})
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(error).pipe(
											Effect.zipRight(
												isWorktreeNameClashError(error)
													? promptWorktreeClashResolution({
															error,
															projectPath,
															retry: helpers.withQueue(task.id, "start", startHelixSession()),
														}).pipe(Effect.as(null))
													: helpers
															.showErrorToast("Failed to create worktree")(error)
															.pipe(Effect.as(null)),
											),
										),
									),
								)

							if (!worktree) return

							// Create tmux session with init commands (same as SessionManager)
							yield* worktreeSession
								.getOrCreateSession(task.id, {
									worktreePath: worktree.path,
									projectPath,
									initCommands: worktreeConfig.initCommands,
									tmuxPrefix: sessionConfig.tmuxPrefix,
								})
								.pipe(Effect.catchAll(helpers.showErrorToast("Failed to create session")))
						}

						// Get worktree path for the helix command
						const worktreePath = getWorktreePath(projectPath, task.id)

						// Create or switch to the "hx" window with Helix running
						// Uses interactive shell wrapper so direnv loads and Helix has proper env
						const helixCommand = `${shell} -i -c 'hx .; exec ${shell}'`

						yield* worktreeSession
							.ensureWindow(sessionName, WINDOW_NAMES.HX, {
								command: helixCommand,
								cwd: worktreePath,
							})
							.pipe(
								Effect.tap(() =>
									toast.show("success", `Helix ready for ${task.id} - press Space+a to attach`),
								),
								Effect.catchAll(helpers.showErrorToast("Failed to open Helix")),
							)
					})

			/**
			 * Recover crashed session (r in normal mode)
			 *
			 * Recovers a crashed session for the selected task.
			 * Only valid when session state is "crashed".
			 * Uses `claude --resume` to continue the conversation.
			 */
			const recoverCrashedSession = () =>
				Effect.gen(function* () {
					const task = yield* helpers.getActionTargetTask()
					if (!task) return

					if (task.sessionState !== "crashed") {
						yield* toast.show("error", `Cannot recover: task is ${task.sessionState}`)
						return
					}

					yield* toast.show("info", `Recovering session for ${task.id}...`)
					const projectPath = yield* helpers.getProjectPath()

					yield* recoverSessionWithPreferredRuntime(task.id, projectPath).pipe(
						Effect.tap(() => toast.show("success", `Recovered session for ${task.id}`)),
						Effect.tap(() => boardService.refresh()),
						Effect.catchAll(helpers.showErrorToast("Failed to recover")),
					)
				})

			/**
			 * Recover all crashed sessions (Shift+R in normal mode)
			 *
			 * Recovers all crashed sessions.
			 */
			const recoverAllCrashedSessions = () =>
				Effect.gen(function* () {
					const allTasks = yield* boardService.getTasks()
					const crashedTasks = allTasks.filter((t) => t.sessionState === "crashed")

					if (crashedTasks.length === 0) {
						yield* toast.show("info", "No crashed sessions to recover")
						return
					}

					yield* toast.show("info", `Recovering ${crashedTasks.length} crashed session(s)...`)

					let recovered = 0
					const projectPath = yield* helpers.getProjectPath()
					for (const task of crashedTasks) {
						yield* recoverSessionWithPreferredRuntime(task.id, projectPath).pipe(
							Effect.tap(() => {
								recovered++
							}),
							Effect.catchAll((error) =>
								Effect.log(`Failed to recover ${task.id}: ${String(error)}`),
							),
						)
					}

					yield* boardService.refresh()
					yield* toast.show("success", `Recovered ${recovered}/${crashedTasks.length} sessions`)
				})

			// ================================================================
			// Public API
			// ================================================================

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
