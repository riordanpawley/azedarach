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
		 * Checks for potential merge conflicts before merging. If conflicts are
		 * likely (files modified in both branches), shows a confirmation dialog.
		 * Otherwise proceeds directly with the merge.
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
					// Show confirmation dialog with conflict warning
					const fileList =
						conflictCheck.conflictingFiles.length > 0
							? `\n\nConflicting files:\n${conflictCheck.conflictingFiles.slice(0, 5).join("\n")}${
									conflictCheck.conflictingFiles.length > 5
										? `\n... and ${conflictCheck.conflictingFiles.length - 5} more`
										: ""
								}`
							: ""

					const message = `Merge ${task.id} may have conflicts.${fileList}\n\nMerge will be attempted in the worktree first - main won't be affected if conflicts occur.\n\nProceed?`

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

		/**
		 * Show git diff action (Space+g)
		 *
		 * Opens a tmux popup showing the diff between the branch and main.
		 * Uses `git diff main..{branch}` with color, piped to less for scrolling.
		 * Requires an active session with a worktree.
		 */
		showDiff: () =>
			Effect.gen(function* () {
				const task = yield* ctx.getSelectedTask()
				if (!task) return

				if (task.sessionState === "idle") {
					yield* ctx.toast.show("error", `No worktree for ${task.id} - start a session first`)
					return
				}

				// Get worktree path from PRWorkflow (via worktreeManager)
				const projectPath = process.cwd()

				// Show diff in tmux popup using less for scrolling
				// git diff main..{branch} shows what the branch adds relative to main
				// --color=always ensures ANSI colors, -R in less interprets them
				yield* ctx.tmux
					.displayPopup({
						command: `cd "${projectPath}" && git diff --color=always main..${task.id} | less -R`,
						width: "95%",
						height: "95%",
						title: ` Diff: main..${task.id} (q to quit, /search, n/N next/prev) `,
					})
					.pipe(Effect.catchAll(Effect.logError))
			}),
	}
}

export type PRHandlers = ReturnType<typeof createPRHandlers>
