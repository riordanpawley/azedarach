import { AppConfig } from "@azedarach/config"
import { Command } from "@effect/platform"
import { Effect } from "effect"
import { hasTaskWorktreeContext } from "../../types.js"
import { formatForToast } from "../../utils/formatForToast.js"
import { getWorktreePath } from "../../utils/worktreePaths.js"
import { EditorService } from "../EditorService.js"
import { NavigationService } from "../NavigationService.js"
import { OverlayService } from "../OverlayService.js"
import { PrWorkflowService as PRWorkflow } from "../PrWorkflowService.js"
import { TmuxService } from "../TmuxService.js"
import { ToastService } from "../ToastService.js"
import { TuiBoardStoreService } from "../TuiBoardStoreService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

export interface PRHandlersServiceApi {
	readonly createPR: () => Effect.Effect<void>
	readonly openPR: () => Effect.Effect<void>
	readonly updateFromBase: () => Effect.Effect<void>
	readonly merge: () => Effect.Effect<void>
	readonly cleanup: () => Effect.Effect<void>
	readonly abortMerge: () => Effect.Effect<void>
	readonly showDiff: () => Effect.Effect<void>
	readonly doMerge: (
		issueId: string,
		targetBranch: string,
		projectPath: string,
	) => Effect.Effect<void>
	readonly enterMergeSelect: () => Effect.Effect<void>
	readonly confirmMergeSelect: () => Effect.Effect<void>
	readonly cancelMergeSelect: () => Effect.Effect<void>
}

type GitOperationInProgress = {
	readonly kind: "merge" | "rebase" | "cherry-pick" | "revert"
	readonly pseudoRef?: "MERGE_HEAD" | "CHERRY_PICK_HEAD" | "REVERT_HEAD"
	readonly continueArgs: readonly [string, "--continue"]
	readonly abortArgs: readonly [string, "--abort"]
}

const GIT_OPERATION_IN_PROGRESS_CHECKS: readonly GitOperationInProgress[] = [
	{
		kind: "merge",
		pseudoRef: "MERGE_HEAD",
		continueArgs: ["merge", "--continue"],
		abortArgs: ["merge", "--abort"],
	},
	{
		kind: "rebase",
		continueArgs: ["rebase", "--continue"],
		abortArgs: ["rebase", "--abort"],
	},
	{
		kind: "cherry-pick",
		pseudoRef: "CHERRY_PICK_HEAD",
		continueArgs: ["cherry-pick", "--continue"],
		abortArgs: ["cherry-pick", "--abort"],
	},
	{
		kind: "revert",
		pseudoRef: "REVERT_HEAD",
		continueArgs: ["revert", "--continue"],
		abortArgs: ["revert", "--abort"],
	},
]

const hasPseudoRef = (cwd: string, refName: "MERGE_HEAD" | "CHERRY_PICK_HEAD" | "REVERT_HEAD") =>
	Effect.gen(function* () {
		const command = Command.make("git", "rev-parse", "-q", "--verify", refName).pipe(
			Command.workingDirectory(cwd),
		)
		const exitCode = yield* Command.exitCode(command).pipe(Effect.orElseSucceed(() => 1))
		return exitCode === 0
	})

const hasRebaseInProgress = (cwd: string) =>
	Effect.gen(function* () {
		const resolveGitPath = (name: "rebase-merge" | "rebase-apply") =>
			Command.string(
				Command.make("git", "rev-parse", "--git-path", name).pipe(Command.workingDirectory(cwd)),
			).pipe(
				Effect.map((output) => output.trim()),
				Effect.orElseSucceed(() => ""),
			)

		const hasGitPath = (filePath: string) =>
			filePath.length === 0
				? Effect.succeed(false)
				: Command.exitCode(Command.make("test", "-e", filePath)).pipe(
						Effect.map((code) => code === 0),
						Effect.orElseSucceed(() => false),
					)

		const rebaseMergePath = yield* resolveGitPath("rebase-merge")
		if (yield* hasGitPath(rebaseMergePath)) return true
		const rebaseApplyPath = yield* resolveGitPath("rebase-apply")
		return yield* hasGitPath(rebaseApplyPath)
	})

const getGitOperationInProgress = (cwd: string) =>
	Effect.gen(function* () {
		for (const operation of GIT_OPERATION_IN_PROGRESS_CHECKS) {
			const present =
				operation.kind === "rebase"
					? yield* hasRebaseInProgress(cwd)
					: operation.pseudoRef
						? yield* hasPseudoRef(cwd, operation.pseudoRef)
						: false
			if (present) {
				return operation
			}
		}
		return undefined
	})

export class PRHandlersService extends Effect.Service<PRHandlersService>()("PRHandlersService", {
	dependencies: [
		KeyboardHelpersService.Default,
		ToastService.Default,
		TuiBoardStoreService.Default,
		OverlayService.Default,
		PRWorkflow.Default,
		TmuxService.Default,
		AppConfig.Default,
		EditorService.Default,
		NavigationService.Default,
	],
	effect: Effect.gen(function* () {
		const helpers = yield* KeyboardHelpersService
		const toast = yield* ToastService
		const board = yield* TuiBoardStoreService
		const overlay = yield* OverlayService
		const prWorkflow = yield* PRWorkflow
		const tmux = yield* TmuxService
		const appConfig = yield* AppConfig
		const editor = yield* EditorService
		const nav = yield* NavigationService

		const doMerge = (issueId: string, targetBranch: string, projectPath: string) =>
			helpers.withQueue(
				issueId,
				"merge",
				Effect.gen(function* () {
					yield* toast.show("info", `Merging ${issueId} into ${targetBranch}...`)
					yield* prWorkflow
						.mergeToMain({
							issueId,
							projectPath,
						})
						.pipe(
							Effect.tap(() =>
								Effect.gen(function* () {
									yield* board.patchTaskFromMutation(issueId, {
										hasMergeConflict: false,
										updated_at: new Date().toISOString(),
									})
									yield* board.refreshGitStats().pipe(Effect.catchAll(Effect.logError))
								}),
							),
							Effect.tap(() =>
								toast.show("success", `Merged ${issueId} into ${targetBranch} locally`),
							),
							Effect.catchAll(helpers.showErrorToast("Merge failed")),
						)
				}),
				projectPath,
			)

		const doCleanup = (issueId: string, projectPath: string) =>
			helpers.withQueue(
				issueId,
				"cleanup",
				Effect.gen(function* () {
					yield* toast.show("info", `Cleaning up ${issueId}...`)
					yield* prWorkflow.cleanup({ issueId, projectPath, closeIssue: true }).pipe(
						Effect.tap(() => toast.show("success", `Cleaned up ${issueId}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to cleanup")),
					)
				}),
				projectPath,
			)

		const updateFromBase = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				const isBusy = yield* helpers.checkBusy(task.id, projectPath)
				if (isBusy) {
					return
				}

				if (!hasTaskWorktreeContext(task)) {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				const gitConfig = yield* appConfig.getGitConfig()
				const effectiveBaseBranch = task.parentEpicId ?? gitConfig.baseBranch

				yield* helpers.withQueue(
					task.id,
					"update",
					Effect.gen(function* () {
						yield* toast.show("info", `Updating from ${effectiveBaseBranch}...`)
						yield* prWorkflow.updateFromBase({ issueId: task.id, projectPath }).pipe(
							Effect.tap(() => toast.show("success", `Updated from ${effectiveBaseBranch}`)),
							Effect.catchAll(helpers.showErrorToast("Update from base failed")),
						)
					}),
					projectPath,
				)
			})

		const createPR = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}

				const workflowMode = yield* appConfig.getWorkflowMode()
				if (workflowMode === "local") {
					yield* toast.show(
						"info",
						"PR creation disabled in local workflow mode (use Space+m to merge)",
					)
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				const isBusy = yield* helpers.checkBusy(task.id, projectPath)
				if (isBusy) {
					return
				}

				if (!hasTaskWorktreeContext(task)) {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				const gitConfig = yield* appConfig.getGitConfig()
				const effectiveBaseBranch = task.parentEpicId ?? gitConfig.baseBranch

				yield* toast.show("info", `Syncing with ${effectiveBaseBranch} before PR...`)
				const updateResult = yield* prWorkflow
					.updateFromBase({ issueId: task.id, projectPath })
					.pipe(
						Effect.match({
							onFailure: (error) => ({ _tag: "error" as const, error }),
							onSuccess: () => ({ _tag: "success" as const }),
						}),
					)
				if (updateResult._tag === "error") {
					yield* Effect.logWarning(
						"Update from base failed, proceeding with PR creation anyway",
						updateResult.error,
					)
				}

				yield* helpers.withQueue(
					task.id,
					"create-pr",
					Effect.gen(function* () {
						yield* toast.show("info", `Creating PR for ${task.id}...`)
						yield* prWorkflow.createPR({ issueId: task.id, projectPath }).pipe(
							Effect.tap((pr) => toast.show("success", `PR created: ${pr.url}`)),
							Effect.catchAll((error) =>
								Effect.gen(function* () {
									yield* Effect.logError("Create PR failed", error)
									yield* toast.show("error", formatForToast(error))
								}),
							),
						)
					}),
				)
			})

		const merge = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}

				const workflowMode = yield* appConfig.getWorkflowMode()
				const drilldownEpicId = yield* nav.getDrillDownEpic()
				const isEpicChild = task.parentEpicId !== undefined
				if (workflowMode === "origin" && !drilldownEpicId && !isEpicChild) {
					yield* toast.show(
						"info",
						"Direct merge disabled in origin workflow mode (use Space+P to create PR)",
					)
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				const isBusy = yield* helpers.checkBusy(task.id, projectPath)
				if (isBusy) {
					return
				}

				if (!hasTaskWorktreeContext(task)) {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				const baseOperation = yield* getGitOperationInProgress(projectPath)
				if (baseOperation !== undefined) {
					const continueCommand = `git -C ${projectPath} ${baseOperation.continueArgs.join(" ")}`
					const abortCommand = `git -C ${projectPath} ${baseOperation.abortArgs.join(" ")}`
					yield* toast.show(
						"error",
						`Cannot merge: project base branch has ${baseOperation.kind} in progress.\nContinue with '${continueCommand}' after resolving conflicts, or abort with '${abortCommand}', then retry Space+m.`,
					)
					return
				}

				const { targetBranch } = yield* prWorkflow
					.getTargetBranch(task.id, projectPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(Effect.succeed({ targetBranch: "main", isEpicChild: false })),
							),
						),
					)

				const uncommittedResult = yield* prWorkflow
					.checkUncommittedChanges({
						issueId: task.id,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({ _tag: "success" as const, result })),
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(
									Effect.succeed({
										_tag: "error" as const,
										message: formatForToast(error),
									}),
								),
							),
						),
					)

				if (uncommittedResult._tag === "error") {
					yield* toast.show(
						"info",
						`Could not check for uncommitted changes: ${uncommittedResult.message}`,
					)
				} else if (uncommittedResult.result.hasUncommittedChanges) {
					const fileCount = uncommittedResult.result.changedFiles.length
					const fileList =
						uncommittedResult.result.changedFiles.slice(0, 3).join(", ") +
						(fileCount > 3 ? ` (+${fileCount - 3} more)` : "")

					yield* overlay.push({
						_tag: "confirm",
						message: `Uncommitted changes in worktree: ${fileList}\n\nWith autostash, these may conflict after merge. Commit first?`,
						onConfirm: doMerge(task.id, targetBranch, projectPath),
					})
					return
				}

				const conflictCheckResult = yield* prWorkflow
					.checkMergeConflicts({
						issueId: task.id,
						projectPath,
					})
					.pipe(
						Effect.map((check) => ({ _tag: "success" as const, check })),
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(
									Effect.succeed({
										_tag: "error" as const,
										message: formatForToast(error),
									}),
								),
							),
						),
					)

				if (conflictCheckResult._tag === "error") {
					yield* toast.show(
						"error",
						`Cannot verify merge safety: ${conflictCheckResult.message}. Aborting.`,
					)
					return
				}

				if (conflictCheckResult.check.hasConflictRisk) {
					const fileList =
						conflictCheckResult.check.conflictingFiles.length > 0
							? conflictCheckResult.check.conflictingFiles.slice(0, 5).join(", ") +
								(conflictCheckResult.check.conflictingFiles.length > 5
									? ` (+${conflictCheckResult.check.conflictingFiles.length - 5} more)`
									: "")
							: "unknown files"
					yield* overlay.push({
						_tag: "confirm",
						message: `Conflicts detected in: ${fileList}\n\nAsk AI to resolve them?`,
						onConfirm: doMerge(task.id, targetBranch, projectPath),
					})
					return
				}

				yield* doMerge(task.id, targetBranch, projectPath)
			})

		const cleanup = () =>
			Effect.gen(function* () {
				const tasks = yield* helpers.getActionTargetTasks()
				if (tasks.length === 0) {
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				const tasksWithWorktrees = tasks.filter((task) => hasTaskWorktreeContext(task))
				if (tasksWithWorktrees.length === 0) {
					yield* toast.show("error", "No worktrees to delete")
					return
				}

				if (tasksWithWorktrees.length === 1) {
					const task = tasksWithWorktrees[0]
					if (task === undefined) {
						return
					}
					const isBusy = yield* helpers.checkBusy(task.id, projectPath)
					if (isBusy) {
						return
					}

					const windows = yield* tmux.listWindows(task.id)
					let message = `Delete worktree and branch for ${task.id}?`
					if (windows.length > 0) {
						message += `\n\nThis will terminate the tmux session with ${windows.length} window(s):`
						for (const window of windows) {
							message += `\n  • ${window}`
						}
					}
					message += "\n\nAll uncommitted changes will be lost."
					message += "\nThe issue will be closed."

					yield* overlay.push({
						_tag: "confirm",
						message,
						onConfirm: doCleanup(task.id, projectPath),
					})
					return
				}

				const taskIds = tasksWithWorktrees.map((task) => task.id)

				const onWorktreeOnly = Effect.gen(function* () {
					yield* toast.show("info", `Cleaning up ${taskIds.length} worktrees and branches...`)
					yield* Effect.all(
						tasksWithWorktrees.map((task) =>
							Effect.gen(function* () {
								const isBusy = yield* helpers.checkBusy(task.id, projectPath)
								if (isBusy) {
									return
								}
								yield* helpers.withQueue(
									task.id,
									"cleanup",
									prWorkflow
										.cleanup({ issueId: task.id, projectPath, closeIssue: false })
										.pipe(Effect.catchAll(helpers.showErrorToast(`Cleanup ${task.id}`))),
									projectPath,
								)
							}),
						),
						{ concurrency: "unbounded" },
					)

					for (const task of tasksWithWorktrees) {
						yield* board.patchTaskFromMutation(task.id, {
							sessionState: "idle",
							hasWorktree: undefined,
							hasMergeConflict: false,
							gitBehindCount: undefined,
							hasUncommittedChanges: undefined,
							gitAdditions: undefined,
							gitDeletions: undefined,
						})
					}
					yield* toast.show("success", `Cleaned up ${taskIds.length} worktrees and branches`)
				}).pipe(Effect.catchAll(Effect.logError))

				const onFullCleanup = Effect.gen(function* () {
					yield* toast.show(
						"info",
						`Full cleanup of ${taskIds.length} worktrees, branches, and tracker entries...`,
					)
					yield* Effect.all(
						tasksWithWorktrees.map((task) =>
							Effect.gen(function* () {
								const isBusy = yield* helpers.checkBusy(task.id, projectPath)
								if (isBusy) {
									return
								}
								yield* helpers.withQueue(
									task.id,
									"cleanup",
									prWorkflow
										.cleanup({ issueId: task.id, projectPath, closeIssue: true })
										.pipe(Effect.catchAll(helpers.showErrorToast(`Cleanup ${task.id}`))),
									projectPath,
								)
							}),
						),
						{ concurrency: "unbounded" },
					)

					for (const task of tasksWithWorktrees) {
						yield* board.patchTaskFromMutation(task.id, {
							status: "closed",
							sessionState: "idle",
							hasWorktree: undefined,
							hasMergeConflict: false,
							updated_at: new Date().toISOString(),
							gitBehindCount: undefined,
							hasUncommittedChanges: undefined,
							gitAdditions: undefined,
							gitDeletions: undefined,
						})
					}
					yield* toast.show(
						"success",
						`Full cleanup of ${taskIds.length} worktrees, branches, and tracker entries completed`,
					)
				}).pipe(Effect.catchAll(Effect.logError))

				yield* overlay.push({
					_tag: "bulkCleanup",
					taskIds,
					onWorktreeOnly,
					onFullCleanup,
				})
			})

		const abortMerge = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				const isBusy = yield* helpers.checkBusy(task.id, projectPath)
				if (isBusy) {
					return
				}
				if (!hasTaskWorktreeContext(task)) {
					yield* toast.show("error", `No worktree for ${task.id} - nothing to abort`)
					return
				}

				yield* helpers.withQueue(
					task.id,
					"abort-merge",
					Effect.gen(function* () {
						yield* toast.show("info", `Aborting merge for ${task.id}...`)
						yield* prWorkflow.abortMerge({ issueId: task.id, projectPath }).pipe(
							Effect.tap(() =>
								Effect.gen(function* () {
									yield* board.patchTaskFromMutation(task.id, {
										hasMergeConflict: false,
									})
									yield* board.refreshGitStats().pipe(Effect.catchAll(Effect.logError))
								}),
							),
							Effect.tap(() => toast.show("success", `Merge aborted for ${task.id}`)),
							Effect.catchAll((error) =>
								Effect.logWarning(error).pipe(
									Effect.zipRight(toast.show("error", `Abort failed: ${formatForToast(error)}`)),
								),
							),
						)
					}),
					projectPath,
				)
			})

		const showDiff = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}
				if (!hasTaskWorktreeContext(task)) {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				const { baseBranch: effectiveBaseBranch } = yield* prWorkflow
					.getEffectiveBaseBranchForIssue({
						issueId: task.id,
						projectPath,
					})
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(
									appConfig.getEffectiveBaseBranch().pipe(
										Effect.map((baseBranch) => ({
											baseBranch,
											parentEpicId: undefined,
										})),
									),
								),
							),
						),
					)

				yield* overlay.push({
					_tag: "diffViewer",
					worktreePath: getWorktreePath(projectPath, task.id),
					baseBranch: effectiveBaseBranch,
				})
			})

		const enterMergeSelect = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}
				if (!hasTaskWorktreeContext(task)) {
					yield* toast.show("error", `No worktree for ${task.id} - nothing to merge`)
					return
				}
				yield* editor.enterMergeSelect(task.id)
				yield* toast.show("info", `Select target bead to merge ${task.id} into`)
			})

		const confirmMergeSelect = () =>
			Effect.gen(function* () {
				const sourceId = yield* editor.getMergeSelectSourceId()
				if (sourceId === null) {
					yield* editor.exitToNormal()
					return
				}
				const targetId = yield* nav.getFocusedTaskId()
				if (targetId === null) {
					yield* toast.show("error", "No target bead selected")
					return
				}
				if (sourceId === targetId) {
					yield* toast.show("error", "Cannot merge bead into itself")
					return
				}

				yield* editor.exitToNormal()
				const projectPath = yield* helpers.getProjectPath()
				yield* toast.show("info", `Merging ${sourceId} into ${targetId}...`)
				yield* prWorkflow
					.mergeIssueIntoIssue({
						sourceIssueId: sourceId,
						targetIssueId: targetId,
						projectPath,
					})
					.pipe(
						Effect.tap(() =>
							board.patchTaskFromMutation(sourceId, {
								status: "closed",
								updated_at: new Date().toISOString(),
							}),
						),
						Effect.tap(() =>
							toast.show("success", `Merged ${sourceId} into ${targetId}. Source bead closed.`),
						),
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(toast.show("error", `Merge failed: ${formatForToast(error)}`)),
							),
						),
					)
			})

		const cancelMergeSelect = () =>
			Effect.gen(function* () {
				yield* editor.exitToNormal()
				yield* toast.show("info", "Merge cancelled")
			})

		const openPR = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getActionTargetTask()
				if (task === undefined) {
					return
				}
				if (!task.hasPR || !task.prNumber) {
					yield* toast.show("error", `No PR for ${task.id} - create one first (P)`)
					return
				}

				const projectPath = yield* helpers.getProjectPath()
				yield* toast.show("info", `Opening PR #${task.prNumber}...`)
				yield* Command.exitCode(
					Command.make("gh", "pr", "view", String(task.prNumber), "--web").pipe(
						Command.workingDirectory(projectPath),
					),
				).pipe(Effect.catchAll(helpers.showErrorToast("Failed to open PR")))
			})

		return {
			createPR,
			openPR,
			updateFromBase,
			merge,
			cleanup,
			abortMerge,
			showDiff,
			doMerge,
			enterMergeSelect,
			confirmMergeSelect,
			cancelMergeSelect,
		}
	}),
}) {}
