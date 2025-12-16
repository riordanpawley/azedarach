/**
 * PRWorkflow - Effect service for automated GitHub PR creation and worktree cleanup
 *
 * Handles the complete PR lifecycle:
 * - Create draft PRs from worktree branches
 * - Check PR status
 * - Cleanup after merge (delete worktree, branches)
 *
 * Uses gh CLI for GitHub operations and git for branch management.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Data, Effect, Option } from "effect"
import {
	BeadsClient,
	type BeadsError,
	type Issue,
	type NotFoundError,
	type ParseError,
} from "./BeadsClient.js"
import { type SessionError, SessionManager } from "./SessionManager.js"
import type { TmuxError } from "./TmuxService.js"
import { GitError, type NotAGitRepoError, WorktreeManager } from "./WorktreeManager.js"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * GitHub PR information
 */
export interface PR {
	readonly number: number
	readonly url: string
	readonly title: string
	readonly state: "open" | "closed" | "merged"
	readonly draft: boolean
	readonly branch: string
}

/**
 * Options for creating a PR
 */
export interface CreatePROptions {
	readonly beadId: string
	readonly projectPath: string
	/** Override the auto-generated title */
	readonly title?: string
	/** Override the auto-generated body */
	readonly body?: string
	/** Create as draft PR (default: true) */
	readonly draft?: boolean
	/** Base branch to merge into (default: main) */
	readonly baseBranch?: string
}

/**
 * Options for cleanup
 */
export interface CleanupOptions {
	readonly beadId: string
	readonly projectPath: string
	/** Delete remote branch (default: true) */
	readonly deleteRemoteBranch?: boolean
	/** Close the bead issue (default: true) */
	readonly closeBead?: boolean
}

/**
 * Options for merging to main
 */
export interface MergeToMainOptions {
	readonly beadId: string
	readonly projectPath: string
	/** Push to origin after merge (default: true) */
	readonly pushToOrigin?: boolean
	/** Close the bead issue after successful merge (default: true) */
	readonly closeBead?: boolean
}

/**
 * Options for pushing a worktree branch
 */
export interface PushOptions {
	readonly beadId: string
	readonly projectPath: string
	/** Set upstream tracking (default: true) */
	readonly setUpstream?: boolean
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when PR operation fails
 */
export class PRError extends Data.TaggedError("PRError")<{
	readonly message: string
	readonly command?: string
	readonly beadId?: string
}> {}

/**
 * Error when gh CLI is not installed or not authenticated
 */
export class GHCLIError extends Data.TaggedError("GHCLIError")<{
	readonly message: string
}> {}

/**
 * Error when PR is not found
 */
export class PRNotFoundError extends Data.TaggedError("PRNotFoundError")<{
	readonly beadId: string
	readonly branch: string
}> {}

/**
 * Error when merge has conflicts
 */
export class MergeConflictError extends Data.TaggedError("MergeConflictError")<{
	readonly beadId: string
	readonly branch: string
	readonly message: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * PRWorkflow service interface
 */
export interface PRWorkflowService {
	/**
	 * Create a PR for a bead's worktree branch
	 *
	 * Workflow:
	 * 1. Sync beads changes
	 * 2. Commit any uncommitted changes
	 * 3. Push branch to origin
	 * 4. Create PR via gh CLI
	 * 5. Link PR URL back to bead
	 *
	 * @example
	 * ```ts
	 * const pr = yield* prWorkflow.createPR({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly createPR: (
		options: CreatePROptions,
	) => Effect.Effect<
		PR,
		PRError | GHCLIError | GitError | NotAGitRepoError | BeadsError | NotFoundError | ParseError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Get PR info for a bead's branch
	 *
	 * Returns None if no PR exists for the branch.
	 *
	 * @example
	 * ```ts
	 * const pr = yield* prWorkflow.getPR({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly getPR: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<
		Option.Option<PR>,
		PRError | GHCLIError | GitError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Cleanup after PR is merged or work is abandoned
	 *
	 * Workflow:
	 * 1. Stop any running session
	 * 2. Delete worktree directory
	 * 3. Delete remote branch (optional)
	 * 4. Delete local branch
	 * 5. Close bead issue (optional)
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.cleanup({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly cleanup: (
		options: CleanupOptions,
	) => Effect.Effect<
		void,
		PRError | GitError | NotAGitRepoError | SessionError | TmuxError | BeadsError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Check if gh CLI is installed and authenticated
	 */
	readonly checkGHCLI: () => Effect.Effect<boolean, never, CommandExecutor.CommandExecutor>

	/**
	 * Merge worktree branch to main and clean up
	 *
	 * This is for local merges without creating a PR. Use when work is complete
	 * and you want to merge directly to main without GitHub PR workflow.
	 *
	 * Workflow:
	 * 1. Stop any running session
	 * 2. Sync beads in worktree (bd sync --from-main)
	 * 3. Commit any uncommitted changes in worktree
	 * 4. Switch to main branch in main repo
	 * 5. Merge branch with --no-ff
	 * 6. Remove worktree directory
	 * 7. Delete local branch
	 * 8. Push to origin (optional)
	 * 9. Close bead issue (optional)
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.mergeToMain({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly mergeToMain: (
		options: MergeToMainOptions,
	) => Effect.Effect<
		void,
		| PRError
		| MergeConflictError
		| GitError
		| NotAGitRepoError
		| SessionError
		| TmuxError
		| BeadsError
		| NotFoundError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Push worktree branch to origin
	 *
	 * Commits any uncommitted changes and pushes to origin.
	 * Does not create a PR or merge - just pushes the branch.
	 *
	 * Workflow:
	 * 1. Stage all changes in worktree
	 * 2. Commit with WIP message if needed
	 * 3. Push branch to origin
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.push({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly push: (
		options: PushOptions,
	) => Effect.Effect<void, PRError | GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>
}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Execute a git command and return stdout
 */
const runGit = (
	args: readonly string[],
	cwd: string,
): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", ...args).pipe(Command.workingDirectory(cwd))
		return yield* Command.string(command).pipe(
			Effect.mapError(
				(error) =>
					new GitError({
						message: `git ${args.join(" ")} failed: ${error}`,
						command: `git ${args.join(" ")}`,
					}),
			),
		)
	})

/**
 * Execute a gh command and return stdout
 */
const runGH = (
	args: readonly string[],
	cwd: string,
): Effect.Effect<string, PRError | GHCLIError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("gh", ...args).pipe(Command.workingDirectory(cwd))
		return yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const errorStr = String(error)
				if (errorStr.includes("gh auth login") || errorStr.includes("not logged")) {
					return new GHCLIError({ message: "gh CLI not authenticated. Run: gh auth login" })
				}
				if (errorStr.includes("command not found") || errorStr.includes("ENOENT")) {
					return new GHCLIError({ message: "gh CLI not installed. Run: brew install gh" })
				}
				return new PRError({
					message: `gh ${args.join(" ")} failed: ${errorStr}`,
					command: `gh ${args.join(" ")}`,
				})
			}),
		)
	})

/**
 * Generate PR title from bead
 */
const generatePRTitle = (bead: Issue): string => {
	const typePrefix = bead.issue_type ? `[${bead.issue_type}] ` : ""
	return `${typePrefix}${bead.title} (${bead.id})`
}

/**
 * Generate PR body from bead
 */
const generatePRBody = (bead: Issue): string => {
	const lines: string[] = []

	lines.push(`## Summary`)
	lines.push(``)
	lines.push(`Resolves ${bead.id}: ${bead.title}`)
	lines.push(``)

	if (bead.description) {
		lines.push(`## Description`)
		lines.push(``)
		lines.push(bead.description)
		lines.push(``)
	}

	if (bead.design) {
		lines.push(`## Design Notes`)
		lines.push(``)
		lines.push(bead.design)
		lines.push(``)
	}

	lines.push(`## Test Plan`)
	lines.push(``)
	lines.push(`- [ ] Manual testing`)
	lines.push(`- [ ] Type check passes`)
	lines.push(``)
	lines.push(`---`)
	lines.push(`🤖 Generated with [Azedarach](https://github.com/riordanpawley/azedarach)`)

	return lines.join("\n")
}

/**
 * Parse gh pr view JSON output to PR type
 */
const parsePRJson = (json: string): PR => {
	const data = JSON.parse(json)
	return {
		number: data.number,
		url: data.url,
		title: data.title,
		state: data.state.toLowerCase() as "open" | "closed" | "merged",
		draft: data.isDraft ?? false,
		branch: data.headRefName,
	}
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * PRWorkflow service
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const prWorkflow = yield* PRWorkflow
 *   const pr = yield* prWorkflow.createPR({
 *     beadId: "az-123",
 *     projectPath: process.cwd()
 *   })
 *   return pr
 * }).pipe(Effect.provide(PRWorkflow.Default))
 * ```
 */
export class PRWorkflow extends Effect.Service<PRWorkflow>()("PRWorkflow", {
	dependencies: [WorktreeManager.Default, BeadsClient.Default, SessionManager.Default],
	effect: Effect.gen(function* () {
		const worktreeManager = yield* WorktreeManager
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* SessionManager

		return {
			createPR: (options: CreatePROptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, draft = true, baseBranch = "main" } = options

					// Get bead info for PR title/body
					const bead = yield* beadsClient.show(beadId)

					// Get worktree info
					const worktree = yield* worktreeManager.get({ beadId, projectPath })
					if (!worktree) {
						return yield* Effect.fail(
							new PRError({
								message: `No worktree found for ${beadId}`,
								beadId,
							}),
						)
					}

					// Sync beads changes
					yield* beadsClient.sync(worktree.path).pipe(Effect.catchAll(() => Effect.void))

					// Stage and commit any changes
					yield* runGit(["add", "-A"], worktree.path).pipe(Effect.catchAll(() => Effect.void))

					yield* runGit(["commit", "-m", `Complete ${beadId}: ${bead.title}`], worktree.path).pipe(
						Effect.catchAll(() => Effect.void),
					) // Ignore if nothing to commit

					// Push branch to origin
					yield* runGit(["push", "-u", "origin", beadId], worktree.path).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to push branch: ${e.message}`,
									command: "git push",
								}),
						),
					)

					// Generate PR title and body
					const title = options.title ?? generatePRTitle(bead)
					const body = options.body ?? generatePRBody(bead)

					// Create PR via gh CLI
					const ghArgs = ["pr", "create", "--title", title, "--body", body, "--base", baseBranch]
					if (draft) {
						ghArgs.push("--draft")
					}

					const prUrl = yield* runGH(ghArgs, worktree.path).pipe(
						Effect.map((output) => output.trim()),
					)

					// Extract PR number from URL
					const prNumberMatch = prUrl.match(/\/pull\/(\d+)/)
					const prNumber = prNumberMatch ? parseInt(prNumberMatch[1], 10) : 0

					// Link PR back to bead
					yield* beadsClient
						.update(beadId, {
							notes: `PR: ${prUrl}`,
						})
						.pipe(Effect.catchAll(() => Effect.void))

					return {
						number: prNumber,
						url: prUrl,
						title,
						state: "open" as const,
						draft,
						branch: beadId,
					}
				}),

			getPR: (options: { beadId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

					// Try to get PR info for the branch
					const result = yield* runGH(
						["pr", "view", beadId, "--json", "number,url,title,state,isDraft,headRefName"],
						projectPath,
					).pipe(
						Effect.map((output) => Option.some(parsePRJson(output))),
						Effect.catchAll(() => Effect.succeed(Option.none<PR>())),
					)

					return result
				}),

			cleanup: (options: CleanupOptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, deleteRemoteBranch = true, closeBead = true } = options

					// 1. Stop any running session (ignore errors)
					yield* sessionManager.stop(beadId).pipe(Effect.catchAll(() => Effect.void))

					// 2. Delete worktree
					yield* worktreeManager.remove({ beadId, projectPath })

					// 3. Delete remote branch (optional)
					if (deleteRemoteBranch) {
						yield* runGit(["push", "origin", "--delete", beadId], projectPath).pipe(
							Effect.catchAll(() => Effect.void), // Ignore if already deleted
						)
					}

					// 4. Delete local branch
					yield* runGit(["branch", "-D", beadId], projectPath).pipe(
						Effect.catchAll(() => Effect.void), // Ignore if already deleted
					)

					// 5. Close bead issue (optional)
					if (closeBead) {
						yield* beadsClient
							.update(beadId, { status: "closed" })
							.pipe(Effect.catchAll(() => Effect.void))
					}
				}),

			checkGHCLI: () =>
				Effect.gen(function* () {
					const command = Command.make("gh", "auth", "status")
					const exitCode = yield* Command.exitCode(command).pipe(
						Effect.catchAll(() => Effect.succeed(1)),
					)
					return exitCode === 0
				}),

			mergeToMain: (options: MergeToMainOptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, pushToOrigin = true, closeBead = true } = options

					// Get bead info for merge commit message
					const bead = yield* beadsClient.show(beadId)

					// Get worktree info
					const worktree = yield* worktreeManager.get({ beadId, projectPath })
					if (!worktree) {
						return yield* Effect.fail(
							new PRError({
								message: `No worktree found for ${beadId}`,
								beadId,
							}),
						)
					}

					// 1. Stop any running session (ignore errors)
					yield* sessionManager.stop(beadId).pipe(Effect.catchAll(() => Effect.void))

					// 2. Sync beads in worktree (bd sync --from-main)
					yield* beadsClient.sync(worktree.path).pipe(Effect.catchAll(() => Effect.void))

					// 3. Stage and commit any uncommitted changes in worktree
					yield* runGit(["add", "-A"], worktree.path).pipe(Effect.catchAll(() => Effect.void))
					yield* runGit(["commit", "-m", `Complete ${beadId}: ${bead.title}`], worktree.path).pipe(
						Effect.catchAll(() => Effect.void), // Ignore if nothing to commit
					)

					// 4. Switch to main branch in main repo
					yield* runGit(["checkout", "main"], projectPath).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to checkout main: ${e.message}`,
									command: "git checkout main",
								}),
						),
					)

					// 5. Merge branch with --no-ff
					const mergeMessage = `Merge ${beadId}: ${bead.title}`
					yield* runGit(["merge", beadId, "--no-ff", "-m", mergeMessage], projectPath).pipe(
						Effect.mapError((e) => {
							// Check if it's a merge conflict
							if (
								e.stderr?.includes("CONFLICT") ||
								e.stderr?.includes("Automatic merge failed") ||
								e.message.includes("CONFLICT") ||
								e.message.includes("Automatic merge failed")
							) {
								return new MergeConflictError({
									beadId,
									branch: beadId,
									message:
										"Merge conflicts detected. Resolve manually and run: git merge --continue",
								})
							}
							return new GitError({
								message: `Merge failed: ${e.message}`,
								command: `git merge ${beadId} --no-ff`,
								stderr: e.stderr,
							})
						}),
					)

					// 6. Remove worktree directory
					yield* worktreeManager.remove({ beadId, projectPath })

					// 7. Delete local branch
					yield* runGit(["branch", "-d", beadId], projectPath).pipe(
						Effect.catchAll(() => Effect.void), // Ignore if already deleted
					)

					// 8. Push to origin (optional)
					if (pushToOrigin) {
						yield* runGit(["push", "origin", "main"], projectPath).pipe(
							Effect.mapError(
								(e) =>
									new GitError({
										message: `Push failed: ${e.message}. Your local merge succeeded - retry push manually.`,
										command: "git push origin main",
										stderr: e.stderr,
									}),
							),
						)
					}

					// 9. Close bead issue (optional)
					if (closeBead) {
						yield* beadsClient
							.update(beadId, { status: "closed" })
							.pipe(Effect.catchAll(() => Effect.void))
					}
				}),

			push: (options: PushOptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, setUpstream = true } = options

					// Get worktree info
					const worktree = yield* worktreeManager.get({ beadId, projectPath })
					if (!worktree) {
						return yield* Effect.fail(
							new PRError({
								message: `No worktree found for ${beadId}`,
								beadId,
							}),
						)
					}

					// Stage all changes
					yield* runGit(["add", "-A"], worktree.path).pipe(Effect.catchAll(() => Effect.void))

					// Commit if there are staged changes (ignore if nothing to commit)
					yield* runGit(["commit", "-m", `WIP: ${beadId}`], worktree.path).pipe(
						Effect.catchAll(() => Effect.void),
					)

					// Push to origin
					const pushArgs = setUpstream
						? ["push", "-u", "origin", beadId]
						: ["push", "origin", beadId]

					yield* runGit(pushArgs, worktree.path).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to push branch: ${e.message}`,
									command: `git push${setUpstream ? " -u" : ""} origin ${beadId}`,
								}),
						),
					)
				}),
		}
	}),
}) {}

/**
 * Legacy layer export
 *
 * @deprecated Use PRWorkflow.Default instead
 */
export const PRWorkflowLive = PRWorkflow.Default
