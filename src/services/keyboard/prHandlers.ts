/**
 * PR Key Handlers
 *
 * Handlers for PR workflow:
 * - Create PR (P)
 * - Merge to main (m)
 * - Cleanup worktree (d)
 */

import { Effect } from "effect"
import { formatForToast } from "../ErrorFormatter.js"
import type { HandlerContext } from "./types.js"

// ============================================================================
// PR Handler Factory
// ============================================================================

/**
 * Create all PR-related action handlers
 *
 * These handlers manage the pull request workflow: creating PRs,
 * merging branches to main, and cleaning up worktrees.
 */
export const createPRHandlers = (ctx: HandlerContext) => {
	/**
	 * Execute the actual merge operation (called directly or via confirm)
	 *
	 * Queued to prevent race conditions with other operations on the same task.
	 * Internal helper used by mergeToMain.
	 */
	const doMergeToMain = (beadId: string) =>
		ctx.withQueue(
			beadId,
			"merge",
			Effect.gen(function* () {
				yield* ctx.toast.show("info", `Merging ${beadId} to main...`)

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* ctx.getProjectPath()

				yield* ctx.prWorkflow.mergeToMain({ beadId, projectPath }).pipe(
					Effect.tap(() => ctx.board.refresh()),
					Effect.tap(() => ctx.toast.show("success", `Merged ${beadId} to main`)),
					Effect.catchAll((error: unknown) => {
						const formatted = formatForToast(error)
						return ctx.toast.show("error", `Merge failed: ${formatted}`)
					}),
				)
			}),
		)

	return {
		/**
		 * Create PR action (Space+P)
		 *
		 * Creates a GitHub PR for the current task's worktree branch.
		 * Requires an active session with a worktree.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		createPR: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* ctx.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				yield* ctx.withQueue(
					task.id,
					"create-pr",
					Effect.gen(function* () {
						yield* ctx.toast.show("info", `Creating PR for ${task.id}...`)

						// Get current project path (from ProjectService or cwd fallback)
						const projectPath = yield* ctx.getProjectPath()

						yield* ctx.prWorkflow.createPR({ beadId: task.id, projectPath }).pipe(
							Effect.tap((pr) => ctx.toast.show("success", `PR created: ${pr.url}`)),
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
									yield* ctx.toast.show("error", msg)
								})
							}),
						)
					}),
				)
			}),

		/**
		 * Merge worktree to main action (Space+m)
		 *
		 * Performs a safe merge check using git merge-tree (in-memory 3-way merge).
		 * - Clean merges proceed directly without confirmation
		 * - If conflicts detected, offers to ask Claude to resolve them
		 * - Claude resolution merges main into worktree, then prompts Claude
		 * - User retries Space+m after Claude resolves
		 * Blocked if task already has an operation in progress.
		 */
		mergeToMain: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* ctx.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* ctx.getProjectPath()

				// Check for potential merge conflicts before proceeding
				// If check fails, we must NOT proceed - failing to check doesn't mean it's safe
				const conflictCheckResult = yield* ctx.prWorkflow
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
					yield* ctx.toast.show(
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

					yield* ctx.overlay.push({
						_tag: "confirm",
						message,
						onConfirm: doMergeToMain(task.id),
					})
				} else {
					// No conflicts detected, proceed directly
					yield* doMergeToMain(task.id)
				}
			}),

		/**
		 * Cleanup worktree action (Space+d)
		 *
		 * Deletes the worktree and branch for the current task.
		 * Requires an active session with a worktree.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		cleanup: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* ctx.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree to delete for ${task.id}`)
					return
				}

				// Get current project path (from ProjectService or cwd fallback)
				const projectPath = yield* ctx.getProjectPath()

				yield* ctx.withQueue(
					task.id,
					"cleanup",
					Effect.gen(function* () {
						yield* ctx.toast.show("info", `Cleaning up ${task.id}...`)

						yield* ctx.prWorkflow.cleanup({ beadId: task.id, projectPath }).pipe(
							Effect.tap(() => ctx.toast.show("success", `Cleaned up ${task.id}`)),
							Effect.catchAll(ctx.showErrorToast("Failed to cleanup")),
						)
					}),
				)
			}),

		// Expose doMergeToMain for direct calls if needed
		doMergeToMain,

		/**
		 * Abort merge action (Space+M)
		 *
		 * Aborts an in-progress merge in the worktree. Use this when a merge
		 * conflict resolution is stuck or you want to cancel the merge.
		 * Queued to prevent race conditions with other operations on the same task.
		 * Blocked if task already has an operation in progress.
		 */
		abortMerge: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				// Check if task has an operation in progress
				const isBusy = yield* ctx.checkBusy(task.id)
				if (isBusy) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree for ${task.id} - nothing to abort`)
					return
				}

				yield* ctx.withQueue(
					task.id,
					"abort-merge",
					Effect.gen(function* () {
						yield* ctx.toast.show("info", `Aborting merge for ${task.id}...`)

						yield* ctx.prWorkflow.abortMerge({ beadId: task.id, projectPath: process.cwd() }).pipe(
							Effect.tap(() => ctx.board.refresh()),
							Effect.tap(() => ctx.toast.show("success", `Merge aborted for ${task.id}`)),
							Effect.catchAll((error: unknown) => {
								const formatted = formatForToast(error)
								return ctx.toast.show("error", `Abort failed: ${formatted}`)
							}),
						)
					}),
				)
			}),
	}
}

export type PRHandlers = ReturnType<typeof createPRHandlers>
