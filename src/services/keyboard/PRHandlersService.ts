/**
 * PRHandlersService
 *
 * Handles PR workflow:
 * - Create PR (P)
 * - Update from base (u)
 * - Merge to main (m)
 * - Abort merge (M)
 * - Cleanup worktree (d)
 * - Show diff (f)
 *
 * Converted from factory pattern to Effect.Service layer.
 */

import { Effect } from "effect"
import { AppConfig } from "../../config/AppConfig.js"
import { MergeConflictError, PRWorkflow } from "../../core/PRWorkflow.js"
import { getWorktreePath } from "../../core/paths.js"
import { TmuxService } from "../../core/TmuxService.js"
import { BoardService } from "../BoardService.js"
import { formatForToast } from "../ErrorFormatter.js"
import { OverlayService } from "../OverlayService.js"
import { ToastService } from "../ToastService.js"
import { KeyboardHelpersService } from "./KeyboardHelpersService.js"

// ============================================================================
// Service Definition
// ============================================================================

export class PRHandlersService extends Effect.Service<PRHandlersService>()("PRHandlersService", {
	dependencies: [
		KeyboardHelpersService.Default,
		ToastService.Default,
		BoardService.Default,
		OverlayService.Default,
		PRWorkflow.Default,
		TmuxService.Default,
		AppConfig.Default,
	],

	effect: Effect.gen(function* () {
		// Inject services at construction time
		const helpers = yield* KeyboardHelpersService
		const toast = yield* ToastService
		const board = yield* BoardService
		const overlay = yield* OverlayService
		const prWorkflow = yield* PRWorkflow
		const tmux = yield* TmuxService
		const appConfig = yield* AppConfig
		const gitConfig = yield* appConfig.getGitConfig()

		// ================================================================
		// Internal Helpers
		// ================================================================

		/**
		 * Execute the actual merge operation (called directly or via confirm)
		 *
		 * Queued to prevent race conditions with other operations on the same task.
		 * Internal helper used by mergeToMain.
		 */
		const doMergeToMain = (beadId: string) =>
			helpers.withQueue(
				beadId,
				"merge",
				Effect.gen(function* () {
					yield* toast.show("info", `Merging ${beadId} to main...`)

					// Get current project path (from ProjectService or cwd fallback)
					const projectPath = yield* helpers.getProjectPath()

					yield* prWorkflow.mergeToMain({ beadId, projectPath }).pipe(
						Effect.tap(() => board.refresh()),
						Effect.tap(() => toast.show("success", `Merged ${beadId} to main`)),
						Effect.catchAll(helpers.showErrorToast("Merge failed")),
					)
				}),
			)

		/**
		 * Execute the actual cleanup operation (called via confirm dialog)
		 *
		 * Queued to prevent race conditions with other operations on the same task.
		 * Internal helper used by cleanup.
		 */
		const doCleanup = (beadId: string, projectPath: string) =>
			helpers.withQueue(
				beadId,
				"cleanup",
				Effect.gen(function* () {
					yield* toast.show("info", `Cleaning up ${beadId}...`)

					yield* prWorkflow.cleanup({ beadId, projectPath }).pipe(
						Effect.tap(() => toast.show("success", `Cleaned up ${beadId}`)),
						Effect.catchAll(helpers.showErrorToast("Failed to cleanup")),
					)
				}),
			)

		// ================================================================
		// PR Handler Methods
		// ================================================================

		/**
		 * Update from base action (Space+u)
		 *
		 * Updates the worktree branch with latest changes from main.
		 * Useful for syncing before creating a PR or resolving conflicts.
		 * Requires an active session with a worktree.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		const updateFromBase = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* helpers.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				yield* helpers.withQueue(
					task.id,
					"update",
					Effect.gen(function* () {
						yield* toast.show("info", `Updating from ${gitConfig.baseBranch}...`)

						// Get current project path (from ProjectService or cwd fallback)
						const projectPath = yield* helpers.getProjectPath()

						yield* prWorkflow.updateFromBase({ beadId: task.id, projectPath }).pipe(
							Effect.tap(() => toast.show("success", `Updated from ${gitConfig.baseBranch}`)),
							Effect.catchAll(helpers.showErrorToast("Update from base failed")),
						)
					}),
				)
			})

		/**
		 * Create PR action (Space+P)
		 *
		 * Creates a GitHub PR for the current task's worktree branch.
		 * First updates from main to ensure the branch is synced and resolve any conflicts.
		 * Requires an active session with a worktree.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		const createPR = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* helpers.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* helpers.getProjectPath()

				// Update from base first to resolve any conflicts
				yield* toast.show("info", `Syncing with ${gitConfig.baseBranch} before PR...`)
				const updateResult = yield* prWorkflow
					.updateFromBase({ beadId: task.id, projectPath })
					.pipe(
						Effect.match({
							onFailure: (error) => {
								// MergeConflictError means Claude is resolving - don't proceed
								if (
									error &&
									typeof error === "object" &&
									"_tag" in error &&
									error._tag === "MergeConflictError"
								) {
									return { _tag: "conflict" as const, error }
								}
								// Other errors - log but continue
								return { _tag: "error" as const, error }
							},
							onSuccess: () => ({ _tag: "success" as const }),
						}),
					)

				if (updateResult._tag === "conflict") {
					yield* toast.show("info", "Resolving conflicts - retry PR after Claude finishes")
					return
				}

				if (updateResult._tag === "error") {
					yield* Effect.logWarning("Update from base failed, proceeding with PR creation anyway", {
						error: updateResult.error,
					})
				}

				yield* helpers.withQueue(
					task.id,
					"create-pr",
					Effect.gen(function* () {
						yield* toast.show("info", `Creating PR for ${task.id}...`)

						yield* prWorkflow.createPR({ beadId: task.id, projectPath }).pipe(
							Effect.tap((pr) => toast.show("success", `PR created: ${pr.url}`)),
							Effect.catchAll((error) => {
								const msg =
									error &&
									typeof error === "object" &&
									"_tag" in error &&
									error._tag === "GHCLIError"
										? String((error as { message: string }).message)
										: `Failed to create PR: ${error}`
								return Effect.gen(function* () {
									yield* Effect.logError(`Create PR: ${msg}`, { error })
									yield* toast.show("error", msg)
								})
							}),
						)
					}),
				)
			})

		/**
		 * Merge worktree to main action (Space+m)
		 *
		 * Performs safety checks before merge:
		 * 1. Check for uncommitted changes (autostash can cause hard-to-recover conflicts)
		 * 2. Check for merge conflicts using git merge-tree (in-memory 3-way merge)
		 *
		 * - Uncommitted changes: shows confirmation dialog warning about autostash risks
		 * - Clean merges proceed directly without confirmation
		 * - If conflicts detected, offers to ask Claude to resolve them
		 * - Claude resolution merges main into worktree, then prompts Claude
		 * - User retries Space+m after Claude resolves
		 * Blocked if task already has an operation in progress.
		 */
		const mergeToMain = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* helpers.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* helpers.getProjectPath()

				// Check for uncommitted changes in worktree BEFORE merge
				// With merge.autostash=true, uncommitted changes get stashed before merge
				// and auto-popped after. If stash pop conflicts with merged content,
				// you get a stash conflict that's hard to recover from.
				const uncommittedResult = yield* prWorkflow
					.checkUncommittedChanges({
						beadId: task.id,
						projectPath,
					})
					.pipe(
						Effect.map((result) => ({ _tag: "success" as const, result })),
						Effect.catchAll((error) =>
							Effect.succeed({
								_tag: "error" as const,
								message: formatForToast(error),
							}),
						),
					)

				// If uncommitted check failed, we can still proceed (warn but don't block)
				if (uncommittedResult._tag === "error") {
					yield* toast.show(
						"info",
						`Could not check for uncommitted changes: ${uncommittedResult.message}`,
					)
				} else if (uncommittedResult.result.hasUncommittedChanges) {
					// Show confirmation dialog - uncommitted changes detected
					const fileCount = uncommittedResult.result.changedFiles.length
					const fileList =
						uncommittedResult.result.changedFiles.slice(0, 3).join(", ") +
						(fileCount > 3 ? ` (+${fileCount - 3} more)` : "")

					const message = `Uncommitted changes in worktree: ${fileList}\n\nWith autostash, these may conflict after merge. Commit first?`

					yield* overlay.push({
						_tag: "confirm",
						message,
						onConfirm: doMergeToMain(task.id),
					})
					return
				}

				// Check for potential merge conflicts before proceeding
				// If check fails, we must NOT proceed - failing to check doesn't mean it's safe
				const conflictCheckResult = yield* prWorkflow
					.checkMergeConflicts({
						beadId: task.id,
						projectPath,
					})
					.pipe(
						Effect.map((check) => ({ _tag: "success" as const, check })),
						Effect.catchAll((error) =>
							Effect.succeed({
								_tag: "error" as const,
								message: formatForToast(error),
							}),
						),
					)

				// If conflict check failed, block the merge
				if (conflictCheckResult._tag === "error") {
					yield* toast.show(
						"error",
						`Cannot verify merge safety: ${conflictCheckResult.message}. Aborting.`,
					)
					return
				}

				const conflictCheck = conflictCheckResult.check

				if (conflictCheck.hasConflictRisk) {
					// Offer to ask Claude to resolve the conflicts
					const fileList =
						conflictCheck.conflictingFiles.length > 0
							? conflictCheck.conflictingFiles.slice(0, 5).join(", ") +
								(conflictCheck.conflictingFiles.length > 5
									? ` (+${conflictCheck.conflictingFiles.length - 5} more)`
									: "")
							: "unknown files"

					const message = `Conflicts detected in: ${fileList}\n\nAsk Claude to resolve them?`

					yield* overlay.push({
						_tag: "confirm",
						message,
						onConfirm: doMergeToMain(task.id),
					})
				} else {
					// No conflicts detected, proceed directly
					yield* doMergeToMain(task.id)
				}
			})

		/**
		 * Cleanup worktree action (Space+d)
		 *
		 * Shows confirmation dialog, then deletes the worktree and branch for
		 * the current task. Requires an active session with a worktree.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		const cleanup = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* helpers.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree to delete for ${task.id}`)
					return
				}

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* helpers.getProjectPath()

				// Show confirmation dialog before cleanup
				yield* overlay.push({
					_tag: "confirm",
					message: `Delete worktree and branch for ${task.id}?\n\nThis will remove the session and all uncommitted changes.`,
					onConfirm: doCleanup(task.id, projectPath),
				})
			})

		/**
		 * Abort merge action (Space+M)
		 *
		 * Aborts an in-progress merge in the worktree. Use this when a merge
		 * conflict resolution is stuck or you want to cancel the merge.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		const abortMerge = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* helpers.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - nothing to abort`)
					return
				}

				yield* helpers.withQueue(
					task.id,
					"abort-merge",
					Effect.gen(function* () {
						yield* toast.show("info", `Aborting merge for ${task.id}...`)

						// Get project path from helpers
						const abortProjectPath = yield* helpers.getProjectPath()
						yield* prWorkflow.abortMerge({ beadId: task.id, projectPath: abortProjectPath }).pipe(
							Effect.tap(() => board.refresh()),
							Effect.tap(() => toast.show("success", `Merge aborted for ${task.id}`)),
							Effect.catchAll((error: unknown) => {
								const formatted = formatForToast(error)
								return toast.show("error", `Abort failed: ${formatted}`)
							}),
						)
					}),
				)
			})

		/**
		 * Show diff action (Space+f)
		 *
		 * Opens a tmux popup showing the git diff between the bead's branch and base branch.
		 * Base branch is configurable via pr.baseBranch in .azedarach.json (default: "main").
		 * Useful for code review before merging. Respects user's git diff configuration
		 * (difftastic, delta, etc.) with DFT_COLOR=always for difftastic support.
		 * Requires an active session with a worktree.
		 */
		const showDiff = () =>
			Effect.gen(function* () {
				const task = yield* helpers.getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* helpers.getProjectPath()

				// Compute worktree path using centralized function
				const worktreePath = getWorktreePath(projectPath, task.id)

				// Get base branch from config (default: "main")
				const baseBranch = gitConfig.baseBranch

				// Build git diff command that works in the worktree
				// - Uses $(git merge-base <baseBranch> HEAD) to compare from branch point
				// - Compares to working tree (not HEAD) to include uncommitted changes
				// - Shows only changes made on THIS branch (not changes base made after we branched)
				// - DFT_COLOR=always ensures difftastic colors in tmux popup
				// - --color=always ensures colors for delta/git built-in diff
				// - pipes to less with -R for raw ANSI codes, -S for horizontal scroll
				// - ':!.beads' excludes beads database changes (infrastructure noise)
				const diffCommand = `cd "${worktreePath}" && MERGE_BASE=$(git merge-base ${baseBranch} HEAD) && git diff $MERGE_BASE --stat --color=always -- ':!.beads' && echo "" && DFT_COLOR=always git diff $MERGE_BASE --color=always -- ':!.beads' | less -RS`

				yield* tmux
					.displayPopup({
						command: diffCommand,
						width: "95%",
						height: "95%",
						title: ` Diff: ${task.id} changes since ${baseBranch} (q to quit) `,
						cwd: worktreePath,
					})
					.pipe(Effect.catchAll(helpers.showErrorToast("Failed to show diff")))
			})

		// ================================================================
		// Public API
		// ================================================================

		return {
			createPR,
			updateFromBase,
			mergeToMain,
			cleanup,
			abortMerge,
			showDiff,
			// Expose doMergeToMain for direct calls if needed
			doMergeToMain,
		}
	}),
}) {}
