/**
 * PR Key Handlers
 *
 * Handlers for PR workflow:
 * - Create PR (P)
 * - Merge to main (m)
 * - Cleanup worktree (d)
 */

import { Effect } from "effect"
import { formatForToast } from "../ErrorFormatter"
import type { HandlerContext } from "./types"

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

				yield* ctx.prWorkflow.mergeToMain({ beadId, projectPath: process.cwd() }).pipe(
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
		 */
		createPR: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				yield* ctx.withQueue(
					task.id,
					"create-pr",
					Effect.gen(function* () {
						yield* ctx.toast.show("info", `Creating PR for ${task.id}...`)

						yield* ctx.prWorkflow.createPR({ beadId: task.id, projectPath: process.cwd() }).pipe(
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
		 * If real conflicts are detected, blocks the merge and shows an error.
		 * The user must resolve conflicts in the worktree before retrying.
		 * Clean merges proceed directly without confirmation.
		 */
		mergeToMain: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				// Check for potential merge conflicts before proceeding
				const conflictCheck = yield* ctx.prWorkflow
					.checkMergeConflicts({
						beadId: task.id,
						projectPath: process.cwd(),
					})
					.pipe(
						Effect.catchAll(() =>
							// If check fails, assume no conflicts and proceed
							Effect.succeed({
								hasConflictRisk: false,
								conflictingFiles: [] as readonly string[],
								branchChangedFiles: 0,
								mainChangedFiles: 0,
							}),
						),
					)

				if (conflictCheck.hasConflictRisk) {
					// Block the merge - conflicts must be resolved in worktree first
					const fileList =
						conflictCheck.conflictingFiles.length > 0
							? conflictCheck.conflictingFiles.slice(0, 5).join(", ") +
								(conflictCheck.conflictingFiles.length > 5
									? ` (+${conflictCheck.conflictingFiles.length - 5} more)`
									: "")
							: "unknown files"

					yield* ctx.toast.show(
						"error",
						`Cannot merge ${task.id}: conflicts in ${fileList}. Resolve in worktree first.`,
					)
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
		 */
		cleanup: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree to delete for ${task.id}`)
					return
				}

				yield* ctx.withQueue(
					task.id,
					"cleanup",
					Effect.gen(function* () {
						yield* ctx.toast.show("info", `Cleaning up ${task.id}...`)

						yield* ctx.prWorkflow.cleanup({ beadId: task.id, projectPath: process.cwd() }).pipe(
							Effect.tap(() => ctx.toast.show("success", `Cleaned up ${task.id}`)),
							Effect.catchAll(ctx.showErrorToast("Failed to cleanup")),
						)
					}),
				)
			}),

		// Expose doMergeToMain for direct calls if needed
		doMergeToMain,
	}
}

export type PRHandlers = ReturnType<typeof createPRHandlers>
