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

/**
 * Difftastic execution error
 */
export class DifftasticError extends Data.TaggedError("DifftasticError")<{
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
		 * Excludes .beads/ directory from results.
		 */
		const getChangedFiles = (
			worktreePath: string,
			baseBranch: string,
		): Effect.Effect<ChangedFile[], GitError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const mergeBase = yield* getMergeBase(worktreePath, baseBranch)

				const command = Command.make(
					"git",
					"diff",
					"--name-status",
					`${mergeBase}...HEAD`,
					"--",
					":!.beads",
				).pipe(Command.workingDirectory(worktreePath))

				const output = yield* Command.string(command).pipe(
					Effect.mapError((error) => {
						const stderr = "stderr" in error ? String(error.stderr) : String(error)
						return new GitError({
							message: `Failed to get changed files: ${stderr}`,
							command: `git diff --name-status ${mergeBase}...HEAD -- ':!.beads'`,
							stderr,
						})
					}),
				)

				if (!output.trim()) {
					return []
				}

				return parseNameStatus(output)
			})

		/**
		 * Get difftastic output for a single file
		 *
		 * Returns raw ANSI output as string for display.
		 */
		const getFileDiff = (
			worktreePath: string,
			baseBranch: string,
			filePath: string,
		): Effect.Effect<string, GitError | DifftasticError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const mergeBase = yield* getMergeBase(worktreePath, baseBranch)

				const command = Command.make("git", "diff", `${mergeBase}...HEAD`, "--", filePath).pipe(
					Command.workingDirectory(worktreePath),
					Command.env({
						DFT_COLOR: "always",
						GIT_EXTERNAL_DIFF: "difft --display=side-by-side",
					}),
				)

				const output = yield* Command.string(command).pipe(
					Effect.mapError((error) => {
						const stderr = "stderr" in error ? String(error.stderr) : String(error)
						return new DifftasticError({
							message: `Failed to get diff for file: ${stderr}`,
							command: `git diff ${mergeBase}...HEAD -- ${filePath}`,
							stderr,
						})
					}),
				)

				return output
			})

		/**
		 * Get difftastic output for all files
		 *
		 * Excludes .beads/ directory. Returns raw ANSI output as string.
		 */
		const getFullDiff = (
			worktreePath: string,
			baseBranch: string,
		): Effect.Effect<string, GitError | DifftasticError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const mergeBase = yield* getMergeBase(worktreePath, baseBranch)

				const command = Command.make("git", "diff", `${mergeBase}...HEAD`, "--", ":!.beads").pipe(
					Command.workingDirectory(worktreePath),
					Command.env({
						DFT_COLOR: "always",
						GIT_EXTERNAL_DIFF: "difft --display=side-by-side",
					}),
				)

				const output = yield* Command.string(command).pipe(
					Effect.mapError((error) => {
						const stderr = "stderr" in error ? String(error.stderr) : String(error)
						return new DifftasticError({
							message: `Failed to get full diff: ${stderr}`,
							command: `git diff ${mergeBase}...HEAD -- ':!.beads'`,
							stderr,
						})
					}),
				)

				return output
			})

		return {
			getChangedFiles,
			getFileDiff,
			getFullDiff,
		}
	}),
}) {}
