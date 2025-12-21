/**
 * Diff Atoms
 *
 * Atoms for fetching git diff data via DiffService.
 */

import { Effect } from "effect"
import { DiffService } from "../../services/DiffService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Types
// ============================================================================

export interface DiffParams {
	readonly worktreePath: string
	readonly baseBranch: string
}

export interface FileDiffParams extends DiffParams {
	readonly filePath: string
}

// ============================================================================
// Atoms
// ============================================================================

/**
 * Fetch list of changed files between base branch and HEAD
 *
 * Usage:
 *   const [, getChangedFiles] = useAtom(changedFilesAtom, { mode: "promise" })
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
 * Fetch diff for a specific file
 *
 * Usage:
 *   const [, getFileDiff] = useAtom(fileDiffAtom, { mode: "promise" })
 *   const diff = await getFileDiff({ worktreePath, baseBranch, filePath })
 */
export const fileDiffAtom = appRuntime.fn(
	({ worktreePath, baseBranch, filePath }: FileDiffParams) =>
		Effect.gen(function* () {
			const diffService = yield* DiffService
			return yield* diffService.getFileDiff(worktreePath, baseBranch, filePath)
		}).pipe(
			Effect.catchAll((error) =>
				Effect.zipRight(
					Effect.logError("Failed to get file diff", error),
					Effect.succeed("Error loading diff"),
				),
			),
		),
)

/**
 * Fetch full diff (all files)
 *
 * Usage:
 *   const [, getFullDiff] = useAtom(fullDiffAtom, { mode: "promise" })
 *   const diff = await getFullDiff({ worktreePath, baseBranch })
 */
export const fullDiffAtom = appRuntime.fn(({ worktreePath, baseBranch }: DiffParams) =>
	Effect.gen(function* () {
		const diffService = yield* DiffService
		return yield* diffService.getFullDiff(worktreePath, baseBranch)
	}).pipe(
		Effect.catchAll((error) =>
			Effect.zipRight(
				Effect.logError("Failed to get full diff", error),
				Effect.succeed("Error loading diff"),
			),
		),
	),
)
