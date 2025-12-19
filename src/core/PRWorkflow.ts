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
import { Data, Duration, Effect, Option, Schema } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { OfflineService } from "../services/OfflineService.js"
import {
	BeadsClient,
	type BeadsError,
	type Issue,
	type NotFoundError,
	type ParseError,
} from "./BeadsClient.js"
import { ClaudeSessionManager, type SessionError } from "./ClaudeSessionManager.js"
import { FileLockManager } from "./FileLockManager.js"
import { type TmuxError, TmuxService } from "./TmuxService.js"
import { GitError, type NotAGitRepoError, WorktreeManager } from "./WorktreeManager.js"

// ============================================================================
// Beads Sync Locking
// ============================================================================

/**
 * Lock path for beads sync operations.
 * Using a fixed path ensures all processes use the same lock.
 */
const BEADS_SYNC_LOCK_PATH = "/tmp/azedarach-beads-sync.lock"

/**
 * Timeout for acquiring the beads sync lock.
 * Should be long enough to allow slow syncs to complete.
 */
const BEADS_SYNC_LOCK_TIMEOUT = Duration.seconds(60)

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
 * Options for updating worktree from base branch
 */
export interface UpdateFromBaseOptions {
	readonly beadId: string
	readonly projectPath: string
	/** Base branch to merge from (default: main) */
	readonly baseBranch?: string
}

/**
 * Result of fetching PR comments
 */
export interface PRComment {
	readonly author: string
	readonly body: string
	readonly createdAt: string
	readonly path?: string // For review comments on specific files
	readonly line?: number // For review comments on specific lines
}

// ============================================================================
// GitHub API Response Schemas
// ============================================================================

/**
 * Schema for GitHub PR comment author
 */
const GHAuthorSchema = Schema.Struct({
	login: Schema.optional(Schema.String),
})

/**
 * Schema for GitHub PR issue comment
 */
const GHCommentSchema = Schema.Struct({
	author: Schema.optional(GHAuthorSchema),
	body: Schema.optional(Schema.String),
	createdAt: Schema.optional(Schema.String),
})

/**
 * Schema for GitHub PR review
 */
const GHReviewSchema = Schema.Struct({
	author: Schema.optional(GHAuthorSchema),
	body: Schema.optional(Schema.String),
	submittedAt: Schema.optional(Schema.String),
})

/**
 * Schema for GitHub PR comments API response
 */
const GHPRCommentsResponseSchema = Schema.Struct({
	comments: Schema.optional(Schema.Array(GHCommentSchema)),
	reviews: Schema.optional(Schema.Array(GHReviewSchema)),
})

/**
 * Options for getting PR comments
 */
export interface GetPRCommentsOptions {
	readonly beadId: string
	readonly projectPath: string
}

/**
 * Result of merge conflict check using git merge-tree
 */
export interface MergeConflictCheck {
	/** Whether actual merge conflicts exist (line-level, not just file overlap) */
	readonly hasConflictRisk: boolean
	/** Files with actual merge conflicts */
	readonly conflictingFiles: readonly string[]
	/** Total files changed in the branch (informational) */
	readonly branchChangedFiles: number
	/** Total files changed in main since divergence (informational) */
	readonly mainChangedFiles: number
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

/**
 * Error when type-check fails after merge
 * This indicates the merged code has type errors that need fixing
 */
export class TypeCheckError extends Data.TaggedError("TypeCheckError")<{
	readonly beadId: string
	readonly message: string
	readonly output: string
}> {}

/**
 * Error when operation is blocked by offline mode
 * Contains a descriptive message explaining why the operation was skipped
 */
export class OfflineError extends Data.TaggedError("OfflineError")<{
	readonly operation: string
	readonly reason: "config" | "offline" | "both"
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
		| PRError
		| GHCLIError
		| GitError
		| NotAGitRepoError
		| BeadsError
		| NotFoundError
		| ParseError
		| OfflineError,
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
		| TypeCheckError
		| GitError
		| NotAGitRepoError
		| SessionError
		| TmuxError
		| BeadsError
		| NotFoundError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Check for actual merge conflicts without touching index or worktree
	 *
	 * Uses git merge-tree to perform a real 3-way merge in memory:
	 * - Detects actual line-level conflicts, not just file overlap
	 * - Handles rename detection and directory/file conflicts
	 * - Returns exit code 0 for clean merge, 1 for conflicts
	 *
	 * This is safe to call at any time - it never modifies any files.
	 *
	 * @example
	 * ```ts
	 * const check = yield* prWorkflow.checkMergeConflicts({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * if (check.hasConflictRisk) {
	 *   // Real conflicts exist - must resolve before merge
	 *   console.log("Conflicts in:", check.conflictingFiles)
	 * }
	 * ```
	 */
	readonly checkMergeConflicts: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<MergeConflictCheck, PRError | GitError, CommandExecutor.CommandExecutor>

	/**
	 * Abort an in-progress merge in the worktree
	 *
	 * Use this when a merge conflict resolution is stuck or you want to cancel
	 * the merge operation. Runs `git merge --abort` in the worktree.
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.abortMerge({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly abortMerge: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<void, PRError | GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>

	/**
	 * Check for uncommitted changes in the worktree
	 *
	 * Detects modified, added, deleted, or untracked files using `git status --porcelain`.
	 * Used to warn users before merge operations when autostash is enabled,
	 * since autostash conflicts can be hard to recover from.
	 *
	 * @example
	 * ```ts
	 * const result = yield* prWorkflow.checkUncommittedChanges({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * if (result.hasUncommittedChanges) {
	 *   // Show warning or block operation
	 * }
	 * ```
	 */
	readonly checkUncommittedChanges: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<
		{ hasUncommittedChanges: boolean; changedFiles: readonly string[] },
		PRError | GitError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Update worktree from base branch (typically main)
	 *
	 * Merges the base branch into the worktree, resolving conflicts with Claude if needed.
	 * This is the inverse of mergeToMain - it brings main INTO the worktree.
	 *
	 * Workflow:
	 * 1. Fetch latest from origin
	 * 2. Check for conflicts using git merge-tree
	 * 3. If conflicts: start merge, have Claude resolve
	 * 4. If no conflicts: fast-forward merge
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.updateFromBase({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly updateFromBase: (
		options: UpdateFromBaseOptions,
	) => Effect.Effect<
		void,
		PRError | MergeConflictError | GitError | NotAGitRepoError | BeadsError | NotFoundError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Get PR comments for a bead's branch
	 *
	 * Fetches all comments (issue comments + review comments) from the PR.
	 * Returns empty array if no PR exists or no comments.
	 *
	 * @example
	 * ```ts
	 * const comments = yield* prWorkflow.getPRComments({
	 *   beadId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * if (comments.length > 0) {
	 *   // Inject into Claude context
	 * }
	 * ```
	 */
	readonly getPRComments: (
		options: GetPRCommentsOptions,
	) => Effect.Effect<readonly PRComment[], PRError | GHCLIError, CommandExecutor.CommandExecutor>
}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Run a shell command and return success/failure with output
 *
 * Used for post-merge validation and fix commands.
 */
const runShellCommand = (
	commandStr: string,
	cwd: string,
): Effect.Effect<{ success: boolean; output: string }, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		// Split command into program and args
		const parts = commandStr.split(" ")
		const [program, ...args] = parts
		if (!program) {
			return { success: false, output: "Empty command" }
		}

		const command = Command.make(program, ...args).pipe(Command.workingDirectory(cwd))
		const result = yield* Effect.all({
			exitCode: Command.exitCode(command).pipe(Effect.catchAll(() => Effect.succeed(1))),
			output: Command.string(command).pipe(Effect.catchAll((e) => Effect.succeed(String(e)))),
		})
		return {
			success: result.exitCode === 0,
			output: result.output,
		}
	})

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
			Effect.mapError((error) => {
				// Extract stderr from platform error (like BeadsClient does)
				// This is critical for conflict detection which checks stderr for "CONFLICT"
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new GitError({
					message: `git ${args.join(" ")} failed: ${stderr}`,
					command: `git ${args.join(" ")}`,
					stderr,
				})
			}),
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
	dependencies: [
		WorktreeManager.Default,
		BeadsClient.Default,
		ClaudeSessionManager.Default,
		TmuxService.Default,
		FileLockManager.Default,
		AppConfig.Default,
		OfflineService.Default,
	],
	effect: Effect.gen(function* () {
		const worktreeManager = yield* WorktreeManager
		const beadsClient = yield* BeadsClient
		const sessionManager = yield* ClaudeSessionManager
		const tmuxService = yield* TmuxService
		const fileLockManager = yield* FileLockManager
		const appConfig = yield* AppConfig
		const offlineService = yield* OfflineService
		const mergeConfig = appConfig.getMergeConfig()
		const gitConfig = appConfig.getGitConfig()
		const baseBranch = gitConfig.baseBranch

		/**
		 * Execute an effect with exclusive beads sync lock.
		 * Uses Effect.acquireUseRelease for guaranteed cleanup.
		 * Fails gracefully if lock cannot be acquired.
		 */
		const withSyncLock = <A, E, R>(effect: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> =>
			Effect.acquireUseRelease(
				// Acquire: get the lock (null if failed)
				fileLockManager
					.acquireLock({
						path: BEADS_SYNC_LOCK_PATH,
						type: "exclusive",
						timeout: BEADS_SYNC_LOCK_TIMEOUT,
					})
					.pipe(Effect.option),
				// Use: run the effect
				() => effect,
				// Release: release the lock if acquired
				(lockOption) =>
					Option.isSome(lockOption) ? fileLockManager.releaseLock(lockOption.value) : Effect.void,
			)

		return {
			createPR: (options: CreatePROptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, draft = true, baseBranch = "main" } = options

					// Check if PR creation is enabled (config + network)
					const prStatus = yield* offlineService.isPREnabled()
					if (!prStatus.enabled) {
						return yield* Effect.fail(
							new OfflineError({
								operation: "PR creation",
								reason: prStatus.reason,
							}),
						)
					}

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

					// Sync beads changes (with lock to prevent races)
					yield* withSyncLock(
						beadsClient.sync(worktree.path).pipe(Effect.catchAll(() => Effect.void)),
					)

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
					// First try ClaudeSessionManager.stop (handles beads sync from worktree)
					yield* sessionManager.stop(beadId).pipe(Effect.catchAll(() => Effect.void))
					// Also directly kill tmux session in case it wasn't tracked in memory
					yield* tmuxService.killSession(beadId).pipe(Effect.catchAll(() => Effect.void))

					// 2. Delete worktree
					yield* worktreeManager.remove({ beadId, projectPath })

					// 3. Delete remote branch (optional, only if online)
					if (deleteRemoteBranch) {
						const pushStatus = yield* offlineService.isGitPushEnabled()
						if (pushStatus.enabled) {
							yield* runGit(["push", "origin", "--delete", beadId], projectPath).pipe(
								Effect.catchAll(() => Effect.void), // Ignore if already deleted
							)
						}
						// Silently skip if offline - remote branch can be cleaned up later
					}

					// 4. Delete local branch
					yield* runGit(["branch", "-D", beadId], projectPath).pipe(
						Effect.catchAll(() => Effect.void), // Ignore if already deleted
					)

					// 5. Close bead issue (optional) and sync to persist the change
					if (closeBead) {
						yield* beadsClient
							.update(beadId, { status: "closed" })
							.pipe(Effect.catchAll(() => Effect.void))

						// Sync the closed status to JSONL and commit it
						// This fixes az-o5m9: merged tasks being left in in_progress status
						yield* withSyncLock(
							beadsClient.sync(projectPath).pipe(Effect.catchAll(() => Effect.void)),
						)
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

					// === STRATEGY: Exclude JSONL from git merge, handle beads separately ===
					// The .beads/issues.jsonl file causes most merge conflicts because:
					// 1. It's line-delimited JSON where line order can change
					// 2. Different beads on same line numbers = text conflict
					// 3. The bd merge driver (if configured) has known bugs with 3-way merge
					//
					// Solution: Use git merge with -X ours for .beads/ paths, then reconcile
					// beads separately using bd sync which handles JSONL semantically.

					// 1. Stop any running session first (before we modify git state)
					yield* sessionManager
						.stop(beadId)
						.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to stop session: ${e}`)))
					yield* tmuxService
						.killSession(beadId)
						.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to kill tmux session: ${e}`)))

					// 2. Stage and commit any uncommitted changes in worktree
					yield* runGit(["add", "-A"], worktree.path).pipe(
						Effect.catchAll((e) => Effect.logWarning(`Failed to stage changes: ${e.message}`)),
					)
					yield* runGit(["commit", "-m", `Complete ${beadId}: ${bead.title}`], worktree.path).pipe(
						Effect.catchAll(() => Effect.void), // Ignore if nothing to commit
					)

					// 3. Check for non-beads conflicts using merge-tree (safe, in-memory)
					// We only care about conflicts in actual code files, not .beads/
					const mergeTreeResult = yield* Effect.gen(function* () {
						const command = Command.make(
							"git",
							"merge-tree",
							"--write-tree",
							"--name-only",
							baseBranch,
							beadId,
						).pipe(Command.workingDirectory(projectPath))

						const exitCode = yield* Command.exitCode(command).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`merge-tree command failed: ${e}`).pipe(Effect.map(() => 2)),
							),
						)

						if (exitCode === 0) {
							return { hasConflicts: false, conflictingFiles: [] as string[] }
						}

						// Get conflicting files
						const output = yield* runGit(
							["merge-tree", "--write-tree", "--name-only", "--no-messages", baseBranch, beadId],
							projectPath,
						).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to get conflicting files: ${e.message}`).pipe(
									Effect.map(() => ""),
								),
							),
						)

						const lines = output.trim().split("\n")
						const conflictingFiles = lines
							.slice(1)
							.filter((f) => f.length > 0)
							// Filter OUT .beads/ files - we handle those separately
							.filter((f) => !f.startsWith(".beads/"))

						return {
							hasConflicts: conflictingFiles.length > 0,
							conflictingFiles,
						}
					})

					// 4. If there are real code conflicts (not .beads/), ask Claude to resolve
					if (mergeTreeResult.hasConflicts) {
						const fileList = mergeTreeResult.conflictingFiles.join(", ")

						// Start merge in worktree so Claude can resolve
						yield* runGit(
							["merge", baseBranch, "-m", `Merge ${baseBranch} into ${beadId}`],
							worktree.path,
						).pipe(Effect.catchAll(() => Effect.void)) // Will fail with conflicts, that's expected

						const resolvePrompt = `There are merge conflicts in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution.`

						const sessionStarted = yield* sessionManager
							.start({
								beadId,
								projectPath,
								initialPrompt: resolvePrompt,
							})
							.pipe(
								Effect.map(() => true),
								Effect.catchAll((e) =>
									Effect.logError(
										`Failed to start Claude session for conflict resolution: ${e}`,
									).pipe(Effect.map(() => false)),
								),
							)

						const message = sessionStarted
							? `Code conflicts detected in: ${fileList}. Started Claude session to resolve. Retry merge after resolution.`
							: `Code conflicts detected in: ${fileList}. Failed to start Claude - resolve manually in worktree, then retry merge.`

						return yield* Effect.fail(
							new MergeConflictError({
								beadId,
								branch: beadId,
								message,
							}),
						)
					}

					// 5. No code conflicts - safe to merge
					// Switch to base branch in main repo
					yield* runGit(["checkout", baseBranch], projectPath).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to checkout ${baseBranch}: ${e.message}`,
									command: `git checkout ${baseBranch}`,
								}),
						),
					)

					// 6. Merge branch with strategy to favor 'ours' for .beads/ conflicts
					// This ensures .beads/issues.jsonl from main is preserved during merge
					const mergeMessage = `Merge ${beadId}: ${bead.title}`
					yield* runGit(
						["merge", beadId, "--no-ff", "-m", mergeMessage, "-X", "ours"],
						projectPath,
					).pipe(
						Effect.mapError((e) => {
							// If merge still fails, report conflict or error
							if (e.stderr?.includes("CONFLICT") || e.message.includes("CONFLICT")) {
								return new MergeConflictError({
									beadId,
									branch: beadId,
									message: `Merge conflict. Resolve manually: git checkout main && git merge ${beadId}`,
								})
							}
							return new GitError({
								message: `Merge failed: ${e.message}`,
								command: `git merge ${beadId} --no-ff`,
								stderr: e.stderr,
							})
						}),
					)

					// 7. Sync beads AFTER merge to reconcile any bead changes from branch
					// This imports beads from the branch that might have been excluded by -X ours
					yield* withSyncLock(
						Effect.gen(function* () {
							// Import beads from the merged JSONL
							yield* beadsClient
								.syncImportOnly(projectPath)
								.pipe(
									Effect.catchAll((e) =>
										Effect.logWarning(`Failed to import beads after merge: ${e}`),
									),
								)

							// Recover any tombstoned issues
							yield* beadsClient
								.recoverTombstones(projectPath)
								.pipe(
									Effect.catchAll((e) =>
										Effect.logWarning(`Failed to recover tombstoned beads: ${e}`),
									),
								)

							// Full sync to commit any bead changes
							yield* beadsClient
								.sync(projectPath)
								.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to sync beads: ${e}`)))
						}),
					)

					// 7.5. Run post-merge validation (configurable via .azedarach.json)
					// Only runs if merge.validateCommands is configured
					if (mergeConfig.validateCommands.length > 0) {
						yield* Effect.gen(function* () {
							const { validateCommands, fixCommand, maxFixAttempts, startClaudeOnFailure } =
								mergeConfig

							/**
							 * Run all validation commands and return first failure
							 */
							const runValidation = (): Effect.Effect<
								{ success: boolean; output: string; failedCommand?: string },
								never,
								CommandExecutor.CommandExecutor
							> =>
								Effect.gen(function* () {
									for (const cmd of validateCommands) {
										yield* Effect.log(`Running: ${cmd}`)
										const result = yield* runShellCommand(cmd, projectPath)
										if (!result.success) {
											return { success: false, output: result.output, failedCommand: cmd }
										}
									}
									return { success: true, output: "" }
								})

							// Initial validation
							let lastResult = yield* runValidation()
							if (lastResult.success) {
								yield* Effect.log("Post-merge validation passed")
								return
							}

							// Try fix attempts if fixCommand is configured
							if (fixCommand) {
								for (let attempt = 1; attempt <= maxFixAttempts; attempt++) {
									yield* Effect.log(
										`Validation failed, running fix (attempt ${attempt}/${maxFixAttempts}): ${fixCommand}`,
									)
									yield* runShellCommand(fixCommand, projectPath)

									lastResult = yield* runValidation()
									if (lastResult.success) {
										yield* Effect.log(`Validation passed after fix attempt ${attempt}`)

										// Commit the fixes
										yield* runGit(["add", "-A"], projectPath).pipe(
											Effect.catchAll(() => Effect.void),
										)
										yield* runGit(
											["commit", "-m", `fix: auto-fix after merging ${beadId}`],
											projectPath,
										).pipe(Effect.catchAll(() => Effect.void))

										return
									}
								}
							}

							// Still failing after all fix attempts
							yield* Effect.log("Validation still failing after auto-fix attempts")

							// Commit any partial fixes
							yield* runGit(["add", "-A"], projectPath).pipe(Effect.catchAll(() => Effect.void))
							yield* runGit(
								["commit", "-m", `wip: partial fix after merging ${beadId}`],
								projectPath,
							).pipe(Effect.catchAll(() => Effect.void))

							// Start Claude session if configured
							if (startClaudeOnFailure) {
								const failedCmd = lastResult.failedCommand ?? validateCommands[0] ?? "validation"
								const fixPrompt = `Post-merge validation failed. Please fix the errors:\n\nFailed command: ${failedCmd}\n\n${lastResult.output}\n\nRun the validation commands after fixing to verify.`

								yield* sessionManager
									.start({
										beadId,
										projectPath,
										initialPrompt: fixPrompt,
									})
									.pipe(
										Effect.catchAll((e) =>
											Effect.logWarning(`Failed to start Claude session for fixes: ${e}`),
										),
									)
							}

							return yield* Effect.fail(
								new TypeCheckError({
									beadId,
									message: `Post-merge validation failed. ${startClaudeOnFailure ? "Claude session started to fix. " : ""}Retry merge after fixing.`,
									output: lastResult.output,
								}),
							)
						})
					}

					// 8. Merge Claude's local settings from worktree to main
					// This preserves permission grants (allowedTools, trustedPaths) that Claude
					// added during the session. Must happen BEFORE worktree deletion.
					yield* worktreeManager.mergeClaudeLocalSettings({
						worktreePath: worktree.path,
						mainProjectPath: projectPath,
					})

					// 9. Remove worktree directory
					yield* worktreeManager.remove({ beadId, projectPath })

					// 10. Delete local branch
					yield* runGit(["branch", "-d", beadId], projectPath).pipe(
						Effect.catchAll(() => Effect.void),
					)

					// 11. Close bead issue
					if (closeBead) {
						yield* beadsClient
							.update(beadId, { status: "closed" })
							.pipe(
								Effect.catchAll((e) => Effect.logWarning(`Failed to close bead ${beadId}: ${e}`)),
							)

						yield* withSyncLock(
							beadsClient
								.sync(projectPath)
								.pipe(
									Effect.catchAll((e) => Effect.logWarning(`Failed to sync closed status: ${e}`)),
								),
						)
					}

					// 12. Push to origin (if enabled and online)
					if (pushToOrigin) {
						const pushStatus = yield* offlineService.isGitPushEnabled()
						if (pushStatus.enabled) {
							yield* runGit(["push", "origin", baseBranch], projectPath).pipe(
								Effect.mapError(
									(e) =>
										new GitError({
											message: `Push failed: ${e.message}. Your local merge succeeded - retry push manually.`,
											command: `git push origin ${baseBranch}`,
											stderr: e.stderr,
										}),
								),
							)
						}
						// Silently skip if offline/disabled - merge already succeeded locally
					}
				}),

			checkMergeConflicts: (options: { beadId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

					// Use git merge-tree to perform an actual 3-way merge in memory
					// This detects real line-level conflicts, not just file overlap
					// Exit code 0 = clean merge, 1 = conflicts, other = error
					const mergeTreeCommand = Command.make(
						"git",
						"merge-tree",
						"--write-tree",
						baseBranch,
						beadId,
					).pipe(Command.workingDirectory(projectPath))

					const exitCode = yield* Command.exitCode(mergeTreeCommand).pipe(
						Effect.catchAll(() => Effect.succeed(2)), // Treat errors as unknown
					)

					// Exit code 1 means conflicts detected
					const hasConflictRisk = exitCode === 1

					// If conflicts exist, get the conflicting files from merge-tree output
					let conflictingFiles: readonly string[] = []
					if (hasConflictRisk) {
						// Run merge-tree again to get the conflicted file list
						// Use --no-messages to suppress "Auto-merging" messages and get clean file list
						const output = yield* runGit(
							["merge-tree", "--write-tree", "--name-only", "--no-messages", baseBranch, beadId],
							projectPath,
						).pipe(Effect.catchAll(() => Effect.succeed("")))

						// Parse output - conflicting files appear after the tree hash line
						const lines = output.trim().split("\n")
						// Skip first line (tree hash) and filter non-empty lines
						conflictingFiles = lines.slice(1).filter((f) => f.length > 0)
					}

					// Get file change counts for informational purposes
					const mergeBase = yield* runGit(["merge-base", baseBranch, beadId], projectPath).pipe(
						Effect.map((output) => output.trim()),
						Effect.catchAll(() => Effect.succeed("")),
					)

					let branchChangedFiles = 0
					let baseChangedFiles = 0

					if (mergeBase) {
						const branchOutput = yield* runGit(
							["diff", "--name-only", `${mergeBase}..${beadId}`],
							projectPath,
						).pipe(
							Effect.map(
								(output) =>
									output
										.trim()
										.split("\n")
										.filter((f) => f.length > 0).length,
							),
							Effect.catchAll(() => Effect.succeed(0)),
						)
						branchChangedFiles = branchOutput

						const baseOutput = yield* runGit(
							["diff", "--name-only", `${mergeBase}..${baseBranch}`],
							projectPath,
						).pipe(
							Effect.map(
								(output) =>
									output
										.trim()
										.split("\n")
										.filter((f) => f.length > 0).length,
							),
							Effect.catchAll(() => Effect.succeed(0)),
						)
						baseChangedFiles = baseOutput
					}

					return {
						hasConflictRisk,
						conflictingFiles,
						branchChangedFiles,
						mainChangedFiles: baseChangedFiles,
					} satisfies MergeConflictCheck
				}),

			abortMerge: (options: { beadId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

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

					// Run git merge --abort in the worktree
					yield* runGit(["merge", "--abort"], worktree.path).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to abort merge: ${e.message}`,
									command: "git merge --abort",
									stderr: e.stderr,
								}),
						),
					)
				}),

			checkUncommittedChanges: (options: { beadId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

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

					// Run git status --porcelain to get changed files
					// This is faster than git status and easier to parse
					// Format: XY filename (where X=index status, Y=worktree status)
					const output = yield* runGit(["status", "--porcelain"], worktree.path).pipe(
						Effect.catchAll(() => Effect.succeed("")),
					)

					// Parse output - each non-empty line is a changed file
					const changedFiles = output
						.trim()
						.split("\n")
						.filter((line) => line.length > 0)
						.map((line) => line.slice(3)) // Remove "XY " prefix to get filename

					return {
						hasUncommittedChanges: changedFiles.length > 0,
						changedFiles,
					}
				}),

			updateFromBase: (options: UpdateFromBaseOptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath, baseBranch = "main" } = options

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

					// === Step 1: Update local base branch to match origin ===
					// Fetch latest from origin
					yield* runGit(["fetch", "origin", baseBranch], projectPath).pipe(
						Effect.catchAll((e) => Effect.logWarning(`Failed to fetch: ${e.message}`)),
					)

					// Fast-forward local base branch to origin (done in main project, not worktree)
					// This updates the local branch without checking it out
					yield* runGit(["fetch", "origin", `${baseBranch}:${baseBranch}`], projectPath).pipe(
						Effect.catchAll((e) =>
							Effect.logWarning(`Failed to fast-forward local ${baseBranch}: ${e.message}`),
						),
					)

					// === Step 2: Check for conflicts using git merge-tree (in-memory, safe) ===
					const mergeTreeResult = yield* Effect.gen(function* () {
						const command = Command.make(
							"git",
							"merge-tree",
							"--write-tree",
							"--name-only",
							baseBranch,
							beadId,
						).pipe(Command.workingDirectory(worktree.path))

						const exitCode = yield* Command.exitCode(command).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`merge-tree command failed: ${e}`).pipe(Effect.map(() => 2)),
							),
						)

						if (exitCode === 0) {
							return { hasConflicts: false, conflictingFiles: [] as string[] }
						}

						// Get conflicting files
						const output = yield* runGit(
							["merge-tree", "--write-tree", "--name-only", "--no-messages", baseBranch, beadId],
							worktree.path,
						).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to get conflicting files: ${e.message}`).pipe(
									Effect.map(() => ""),
								),
							),
						)

						const lines = output.trim().split("\n")
						const conflictingFiles = lines
							.slice(1)
							.filter((f) => f.length > 0)
							// Filter OUT .beads/ files - we handle those separately
							.filter((f) => !f.startsWith(".beads/"))

						return {
							hasConflicts: conflictingFiles.length > 0,
							conflictingFiles,
						}
					})

					// === Step 3: Handle conflicts or merge ===
					if (mergeTreeResult.hasConflicts) {
						const fileList = mergeTreeResult.conflictingFiles.join(", ")

						// Start merge in worktree (will result in conflict state)
						yield* runGit(
							["merge", baseBranch, "-m", `Merge ${baseBranch} into ${beadId}`],
							worktree.path,
						).pipe(Effect.catchAll(() => Effect.void)) // Will fail with conflicts, expected

						const resolvePrompt = `There are merge conflicts with ${baseBranch} in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution. After resolving, the branch will be up to date with ${baseBranch}.`

						const sessionStarted = yield* sessionManager
							.start({
								beadId,
								projectPath,
								initialPrompt: resolvePrompt,
							})
							.pipe(
								Effect.map(() => true),
								Effect.catchAll((e) =>
									Effect.logError(
										`Failed to start Claude session for conflict resolution: ${e}`,
									).pipe(Effect.map(() => false)),
								),
							)

						const message = sessionStarted
							? `Conflicts detected in: ${fileList}. Started Claude session to resolve.`
							: `Conflicts detected in: ${fileList}. Failed to start Claude - resolve manually.`

						return yield* Effect.fail(
							new MergeConflictError({
								beadId,
								branch: beadId,
								message,
							}),
						)
					}

					// No conflicts - safe to merge local base branch (fast-forward if possible)
					yield* runGit(
						["merge", baseBranch, "-m", `Merge ${baseBranch} into ${beadId}`],
						worktree.path,
					).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Merge failed: ${e.message}`,
									command: `git merge ${baseBranch}`,
									stderr: e.stderr,
								}),
						),
					)

					// Sync beads after merge to pick up any bead changes from main
					yield* withSyncLock(
						beadsClient.sync(worktree.path).pipe(Effect.catchAll(() => Effect.void)),
					)
				}),

			getPRComments: (options: GetPRCommentsOptions) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

					// First check if a PR exists for this branch
					const prExists = yield* runGH(
						["pr", "view", beadId, "--json", "number"],
						projectPath,
					).pipe(
						Effect.map(() => true),
						Effect.catchAll(() => Effect.succeed(false)),
					)

					if (!prExists) {
						return [] as readonly PRComment[]
					}

					// Fetch PR comments (both issue comments and review comments)
					const commentsJson = yield* runGH(
						["pr", "view", beadId, "--json", "comments,reviews"],
						projectPath,
					).pipe(Effect.catchAll(() => Effect.succeed("{}")))

					// Parse JSON using Effect.try
					const parsed = yield* Effect.try({
						try: () => JSON.parse(commentsJson) as unknown,
						catch: () => new PRError({ message: "Failed to parse PR comments JSON" }),
					}).pipe(Effect.catchAll(() => Effect.succeed({} as unknown)))

					// Decode using Schema
					const data = yield* Schema.decodeUnknown(GHPRCommentsResponseSchema)(parsed).pipe(
						Effect.catchAll(() => Effect.succeed({ comments: [], reviews: [] })),
					)

					const comments: PRComment[] = []

					// Parse issue comments
					for (const c of data.comments ?? []) {
						comments.push({
							author: c.author?.login ?? "unknown",
							body: c.body ?? "",
							createdAt: c.createdAt ?? "",
						})
					}

					// Parse review comments (which include file/line info)
					for (const review of data.reviews ?? []) {
						// Review body (general review comment)
						if (review.body?.trim()) {
							comments.push({
								author: review.author?.login ?? "unknown",
								body: review.body,
								createdAt: review.submittedAt ?? "",
							})
						}
					}

					return comments as readonly PRComment[]
				}),

			/**
			 * Check if a worktree branch is behind main
			 *
			 * Uses git rev-list to count commits between HEAD and main.
			 * Returns { behind, ahead } so caller can show informative message.
			 */
			checkBranchBehindMain: (options: { beadId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

					// Get worktree info
					const worktree = yield* worktreeManager.get({ beadId, projectPath })
					if (!worktree) {
						// No worktree = not behind (task has no session)
						return { behind: 0, ahead: 0 }
					}

					// Count commits branch is behind main
					// HEAD..main = commits in main that are not in HEAD (how many we're behind)
					const behindOutput = yield* runGit(
						["rev-list", "--count", "HEAD..main"],
						worktree.path,
					).pipe(
						Effect.map((output) => Number.parseInt(output.trim(), 10)),
						Effect.catchAll(() => Effect.succeed(0)),
					)

					// Count commits branch is ahead of main
					// main..HEAD = commits in HEAD that are not in main (how many we're ahead)
					const aheadOutput = yield* runGit(
						["rev-list", "--count", "main..HEAD"],
						worktree.path,
					).pipe(
						Effect.map((output) => Number.parseInt(output.trim(), 10)),
						Effect.catchAll(() => Effect.succeed(0)),
					)

					return { behind: behindOutput, ahead: aheadOutput }
				}),

			/**
			 * Merge main into a worktree branch
			 *
			 * Auto-stashes uncommitted changes, merges main, pops stash.
			 * If conflicts, spawns Claude session to resolve them.
			 *
			 * @returns Effect that succeeds if merge was clean, fails with MergeConflictError if conflicts.
			 */
			mergeMainIntoBranch: (options: { beadId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { beadId, projectPath } = options

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

					// 1. Check for uncommitted changes and stash them
					const statusOutput = yield* runGit(["status", "--porcelain"], worktree.path).pipe(
						Effect.catchAll(() => Effect.succeed("")),
					)
					const hasUncommitted = statusOutput.trim().length > 0
					let stashed = false

					if (hasUncommitted) {
						// Stash with message so we can identify it
						const stashResult = yield* runGit(
							["stash", "push", "-m", "azedarach-merge-stash"],
							worktree.path,
						).pipe(
							Effect.map(() => true),
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to stash changes: ${e.message}`).pipe(Effect.as(false)),
							),
						)
						stashed = stashResult
					}

					// 2. Check for conflicts using merge-tree (safe, in-memory check)
					const mergeTreeResult = yield* Effect.gen(function* () {
						const command = Command.make("git", "merge-tree", "--write-tree", "main", "HEAD").pipe(
							Command.workingDirectory(worktree.path),
						)

						const exitCode = yield* Command.exitCode(command).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`merge-tree command failed: ${e}`).pipe(Effect.map(() => 2)),
							),
						)

						if (exitCode === 0) {
							return { hasConflicts: false, conflictingFiles: [] as string[] }
						}

						// Get conflicting files
						const output = yield* runGit(
							["merge-tree", "--write-tree", "--name-only", "--no-messages", "main", "HEAD"],
							worktree.path,
						).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to get conflicting files: ${e.message}`).pipe(
									Effect.map(() => ""),
								),
							),
						)

						const lines = output.trim().split("\n")
						const conflictingFiles = lines
							.slice(1)
							.filter((f) => f.length > 0)
							// Filter OUT .beads/ files - we handle those separately via bd sync
							.filter((f) => !f.startsWith(".beads/"))

						return {
							hasConflicts: conflictingFiles.length > 0,
							conflictingFiles,
						}
					})

					// 3. If conflicts, start merge and spawn Claude
					if (mergeTreeResult.hasConflicts) {
						const fileList = mergeTreeResult.conflictingFiles.join(", ")

						// Start merge in worktree so conflict markers are created
						yield* runGit(["merge", "main", "-m", `Merge main into ${beadId}`], worktree.path).pipe(
							Effect.catchAll(() => Effect.void), // Will fail with conflicts, that's expected
						)

						const resolvePrompt = `There are merge conflicts in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution.`

						const sessionStarted = yield* sessionManager
							.start({
								beadId,
								projectPath,
								initialPrompt: resolvePrompt,
							})
							.pipe(
								Effect.map(() => true),
								Effect.catchAll((e) =>
									Effect.logError(
										`Failed to start Claude session for conflict resolution: ${e}`,
									).pipe(Effect.map(() => false)),
								),
							)

						const message = sessionStarted
							? `Merge conflicts detected in: ${fileList}. Started Claude session to resolve. Retry attach after resolution.`
							: `Merge conflicts detected in: ${fileList}. Failed to start Claude - resolve manually, then retry.`

						return yield* Effect.fail(
							new MergeConflictError({
								beadId,
								branch: beadId,
								message,
							}),
						)
					}

					// 4. No conflicts - do the merge
					yield* runGit(["merge", "main", "--no-edit"], worktree.path).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to merge main: ${e.message}`,
									command: "git merge main",
									stderr: e.stderr,
								}),
						),
					)

					// 5. Pop stash if we stashed
					if (stashed) {
						yield* runGit(["stash", "pop"], worktree.path).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to pop stash: ${e.message}`).pipe(Effect.asVoid),
							),
						)
					}

					yield* Effect.log(`Successfully merged main into ${beadId}`)
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
