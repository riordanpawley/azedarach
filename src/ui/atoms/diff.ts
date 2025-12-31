/**
 * Diff Atoms
 *
 * Atoms for diff viewer - file list fetching and tmux popup display.
 */

import { Effect } from "effect"
import { TmuxService } from "../../core/TmuxService.js"
import { DiffService } from "../../services/DiffService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Types
// ============================================================================

export interface DiffParams {
	readonly worktreePath: string
	readonly baseBranch: string
}

export interface ShowDiffPopupParams extends DiffParams {
	readonly filePath?: string // undefined = all files
}

// ============================================================================
// Atoms
// ============================================================================

/**
 * Fetch list of changed files between base branch and HEAD
 *
 * Usage:
 *   const getChangedFiles = useAtomSet(changedFilesAtom, { mode: "promise" })
 *   const files = await getChangedFiles({ worktreePath, baseBranch })
 */
export const changedFilesAtom = appRuntime.fn(({ worktreePath, baseBranch }: DiffParams) =>
	Effect.gen(function* () {
		const diffService = yield* DiffService
		return yield* diffService.getChangedFiles(worktreePath, baseBranch)
	}).pipe(
		Effect.catchAll((error) =>
			Effect.zipRight(Effect.logError("Failed to get changed files", error), Effect.succeed([])),
		),
	),
)

/**
 * Show diff in a tmux popup with native ANSI rendering
 *
 * Opens a fullscreen tmux popup running difftastic for single files
 * or colored git diff for all files. Popup closes when user presses q.
 *
 * Usage:
 *   const showDiffPopup = useAtomSet(showDiffPopupAtom, { mode: "promise" })
 *   await showDiffPopup({ worktreePath, baseBranch, filePath: "src/foo.ts" })
 *   await showDiffPopup({ worktreePath, baseBranch }) // all files
 */
export const showDiffPopupAtom = appRuntime.fn(
	({ worktreePath, baseBranch, filePath }: ShowDiffPopupParams) =>
		Effect.gen(function* () {
			const tmux = yield* TmuxService
			const diffService = yield* DiffService

			// Get merge base for accurate comparison (where branch diverged from base)
			const mergeBase = yield* diffService.getMergeBase(worktreePath, baseBranch)

			// Build the diff command
			let command: string
			let title: string

			if (filePath) {
				// Single file: use difftastic for syntax-aware side-by-side diff
				// DFT_COLOR=always: force colors even when piped
				// GIT_EXTERNAL_DIFF: make git invoke difftastic for diff rendering
				// Note: use merge-base directly (two-dot), not ...HEAD (three-dot)
				command =
					`DFT_COLOR=always GIT_EXTERNAL_DIFF="difft --display=side-by-side" ` +
					`git diff ${mergeBase} -- "${filePath}" | less -RS`
				title = ` ${filePath} `
			} else {
				// All files: stat summary first, then difftastic side-by-side
				// Shows quick overview of what changed before detailed diff
				command =
					`git diff ${mergeBase} --stat --color=always -- ':^.beads' && echo "" && ` +
					`DFT_COLOR=always GIT_EXTERNAL_DIFF="difft --display=side-by-side" ` +
					`git diff ${mergeBase} -- ':^.beads' | less -RS`
				title = " All Changes "
			}

			yield* tmux.displayPopup({
				command,
				width: "95%",
				height: "95%",
				title,
				cwd: worktreePath,
			})
		}).pipe(
			Effect.catchAll((error) =>
				Effect.zipRight(Effect.logError("Failed to show diff popup", error), Effect.void),
			),
		),
)
