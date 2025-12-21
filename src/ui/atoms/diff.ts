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

			// Get merge base for accurate comparison
			const mergeBase = yield* diffService.getMergeBase(worktreePath, baseBranch)

			// Build the diff command
			let diffCommand: string
			let title: string

			if (filePath) {
				// Single file: use difftastic for syntax-aware diff
				// GIT_EXTERNAL_DIFF makes git invoke difftastic for each file
				diffCommand = `GIT_EXTERNAL_DIFF="difft --display=side-by-side" git diff ${mergeBase}...HEAD -- "${filePath}"`
				title = ` ${filePath} `
			} else {
				// All files: use regular git diff with colors (faster than difftastic on many files)
				diffCommand = `git diff --color=always ${mergeBase}...HEAD -- ':!.beads'`
				title = " All Changes "
			}

			// Wrap in less for scrolling and search
			// -R: interpret ANSI colors
			// -S: don't wrap long lines (horizontal scroll instead)
			// +Gg: start at top (less sometimes starts at bottom with piped input)
			const command = `bash -c '${diffCommand} | less -RS +Gg'`

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
