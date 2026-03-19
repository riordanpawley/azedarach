/**
 * PR Workflow Atoms
 *
 * Handles PR creation, merge, and cleanup operations.
 */

import { Effect } from "effect"
import { PRWorkflow, ProjectService } from "../utils/runtimeServices.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// PR Workflow Atoms
// ============================================================================

/**
 * Create a PR for a bead's worktree branch
 *
 * Usage: const createPR = useAtomSet(createPRAtom, { mode: "promise" })
 *        const pr = await createPR(issueId)
 */
export const createPRAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		const projectService = yield* ProjectService

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		return yield* prWorkflow.createPR({
			issueId,
			projectPath,
		})
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Cleanup worktree and branches after PR merge or abandonment
 *
 * Usage: const cleanup = useAtomSet(cleanupAtom, { mode: "promise" })
 *        await cleanup(issueId)
 */
export const cleanupAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		const projectService = yield* ProjectService

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		yield* prWorkflow.cleanup({ issueId, projectPath, closeIssue: true })
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Merge worktree branch to main and clean up
 *
 * Merges the worktree branch to main locally without creating a PR.
 * Ideal for completed work that doesn't need review.
 *
 * Usage: const mergeToMain = useAtomSet(mergeToMainAtom, { mode: "promise" })
 *        await mergeToMain(issueId)
 */
export const mergeToMainAtom = appRuntime.fn((issueId: string) =>
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		const projectService = yield* ProjectService

		// Get current project path (or cwd if no project selected)
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()

		yield* prWorkflow.mergeToMain({
			issueId,
			projectPath,
		})
	}).pipe(Effect.tapError(Effect.logError)),
)

/**
 * Check if gh CLI is available and authenticated
 *
 * Usage: const ghAvailable = useAtomValue(ghCLIAvailableAtom)
 */
export const ghCLIAvailableAtom = appRuntime.atom(
	Effect.gen(function* () {
		const prWorkflow = yield* PRWorkflow
		return yield* prWorkflow.checkGHCLI()
	}),
	{ initialValue: false },
)
