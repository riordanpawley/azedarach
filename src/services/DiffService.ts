/**
 * DiffService - Git diff operations with difftastic integration
 *
 * Provides git diff functionality for the custom diff viewer TUI.
 * Executes git commands and difftastic for syntax-aware diffs.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect } from "effect"

// ============================================================================
// Types
// ============================================================================

/**
 * Changed file status
 */
export type FileStatus = "added" | "modified" | "deleted" | "renamed"

/**
 * Represents a file changed in the current branch vs base
 */
export interface ChangedFile {
	readonly path: string
	readonly status: FileStatus
	readonly oldPath?: string // for renames
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Git command execution error
 */
export class GitError extends Data.TaggedError("GitError")<{
	readonly message: string
	readonly command: string
	readonly stderr?: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

export class DiffService extends Effect.Service<DiffService>()("DiffService", {
	effect: Effect.gen(function* () {
		/**
		 * Get merge base commit between HEAD and base branch
		 */
		const getMergeBase = (
			worktreePath: string,
			baseBranch: string,
		): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const command = Command.make("git", "merge-base", baseBranch, "HEAD").pipe(
					Command.workingDirectory(worktreePath),
				)

				const mergeBase = yield* Command.string(command).pipe(
					Effect.mapError((error) => {
						const stderr = "stderr" in error ? String(error.stderr) : String(error)
						return new GitError({
							message: `Failed to get merge base: ${stderr}`,
							command: `git merge-base ${baseBranch} HEAD`,
							stderr,
						})
					}),
				)

				return mergeBase.trim()
			})

		/**
		 * Parse git diff --name-status output into ChangedFile array
		 */
		const parseNameStatus = (output: string): ChangedFile[] => {
			const lines = output.trim().split("\n").filter(Boolean)

			return lines.map((line) => {
				const parts = line.split("\t")
				const status = parts[0]!
				const path = parts[1]!

				// Handle renames (format: "R100\toldpath\tnewpath")
				if (status.startsWith("R")) {
					const oldPath = path
					const newPath = parts[2]!
					return {
						path: newPath,
						status: "renamed" as const,
						oldPath,
					}
				}

				// Map status codes to FileStatus
				const statusMap: Record<string, FileStatus> = {
					A: "added",
					M: "modified",
					D: "deleted",
				}

				return {
					path,
					status: statusMap[status] ?? "modified",
				}
			})
		}

		/**
		 * Get list of changed files vs base branch
		 *
		 * Uses merge-base to show changes since branch diverged from base.
		 * Includes all files (including .beads/) for file picker display.
		 */
		const getChangedFiles = (
			worktreePath: string,
			baseBranch: string,
		): Effect.Effect<ChangedFile[], GitError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const mergeBase = yield* getMergeBase(worktreePath, baseBranch)

				// Compare merge-base to HEAD (all commits since branch diverged)
				// Without HEAD, git diff compares to working tree which may be empty
				// Includes .beads/ - file picker shows all changes, "all diff" view filters
				const command = Command.make("git", "diff", "--name-status", mergeBase, "HEAD").pipe(
					Command.workingDirectory(worktreePath),
				)

				const output = yield* Command.string(command).pipe(
					Effect.mapError((error) => {
						const stderr = "stderr" in error ? String(error.stderr) : String(error)
						return new GitError({
							message: `Failed to get changed files: ${stderr}`,
							command: `git diff --name-status ${mergeBase} HEAD`,
							stderr,
						})
					}),
				)

				if (!output.trim()) {
					return []
				}

				return parseNameStatus(output)
			})

		return {
			getMergeBase,
			getChangedFiles,
		}
	}),
}) {}
