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
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { OfflineService } from "../services/OfflineService.js"
import { getToolDefinition } from "./CliToolRegistry.js"
import { FileLockManager } from "./FileLockManager.js"
import { createStaleLockRecoveryHint, extractGitRecoveryHint } from "./gitRecovery.js"
import { ImageAttachmentService } from "./ImageAttachmentService.js"
import {
	type Issue,
	IssueTrackerClient,
	type IssueTrackerError,
	type NotFoundError,
	type ParseError,
	type SyncRequiredError,
} from "./IssueTrackerClient.js"
import { getIssueSessionName, WINDOW_NAMES } from "./paths.js"
import { type SessionError, SessionManager } from "./SessionManager.js"
import { type TmuxError, TmuxService } from "./TmuxService.js"
import { GitError, type NotAGitRepoError, WorktreeManager } from "./WorktreeManager.js"
import { WorktreeSessionService } from "./WorktreeSessionService.js"

// ============================================================================
// IssueTracker Sync Locking
// ============================================================================

/**
 * Lock path for tracker sync operations.
 * Using a fixed path ensures all processes use the same lock.
 */
const ISSUE_TRACKER_SYNC_LOCK_PATH = "/tmp/azedarach-tracker-sync.lock"

/**
 * Timeout for acquiring the tracker sync lock.
 * Should be long enough to allow slow syncs to complete.
 */
const ISSUE_TRACKER_SYNC_LOCK_TIMEOUT = Duration.seconds(60)

/**
 * Timeout for merge push to origin.
 * Prevents Space+m from looking stuck when push blocks on network/auth.
 */
const MERGE_PUSH_TIMEOUT_SECONDS = 30
const MERGE_PUSH_TIMEOUT = Duration.seconds(MERGE_PUSH_TIMEOUT_SECONDS)

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
	readonly issueId: string
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
	readonly issueId: string
	readonly projectPath: string
	/** Delete remote branch (default: true) */
	readonly deleteRemoteBranch?: boolean
	/** Close the bead issue (default: true) */
	readonly closeIssue?: boolean
}

/**
 * Options for merging to main
 */
export interface MergeToMainOptions {
	readonly issueId: string
	readonly projectPath: string
	/** Push to origin after merge (default: true) */
	readonly pushToOrigin?: boolean
	/** Close the bead issue after successful merge (default: false) */
	readonly closeIssue?: boolean
	/**
	 * Keep the worktree and branch after merge (default: true)
	 *
	 * When true (default): merge to main, keep worktree and session running for iteration
	 * When false: full cleanup (stop session, delete worktree, delete branch)
	 *
	 * Typical workflow: Space+m to merge (keep iterating), Space+d to cleanup when done
	 */
	readonly keepWorktree?: boolean
	/** Optional progress callback for UX updates during merge flow. */
	readonly onProgress?: (stage: MergeToMainProgressStage) => Effect.Effect<void, never, never>
	/**
	 * Optional callback for async push status after local merge succeeds.
	 * Called only when push is enabled and started.
	 */
	readonly onDeferredPushStatus?: (
		status: MergeToMainDeferredPushStatus,
	) => Effect.Effect<void, never, never>
}

export type MergeToMainDeferredPushStatus =
	| { readonly _tag: "started"; readonly branch: string }
	| { readonly _tag: "succeeded"; readonly branch: string }
	| { readonly _tag: "failed"; readonly branch: string; readonly error: GitError }

/**
 * Progress stages emitted during mergeToMain.
 */
export type MergeToMainProgressStage =
	| "prepare"
	| "commit"
	| "check-conflicts"
	| "merge"
	| "validate"
	| "push"

/**
 * Options for updating worktree from base branch
 */
export interface UpdateFromBaseOptions {
	readonly issueId: string
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
	readonly issueId: string
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
	readonly issueId?: string
}> {}

/**
 * Error when a target repository already has an active git operation in progress.
 */
export class GitOperationInProgressError extends Data.TaggedError("GitOperationInProgressError")<{
	readonly issueId: string
	readonly operation: "merge" | "rebase" | "cherry-pick" | "revert"
	readonly contextLabel: string
	readonly continueCommand: string
	readonly abortCommand: string
	readonly message: string
}> {}

/**
 * Error when gh CLI is not installed or not authenticated
 */
export class GHCLIError extends Data.TaggedError("GHCLIError")<{
	readonly message: string
}> {}

/**
 * Error when attempting to create a PR but one already exists for the branch.
 */
export class PRAlreadyExistsError extends Data.TaggedError("PRAlreadyExistsError")<{
	readonly message: string
	readonly issueId?: string
	readonly branch?: string
	readonly baseBranch?: string
}> {}

/**
 * Error when branch protection prevents push or PR creation workflow steps.
 */
export class PRBranchProtectionError extends Data.TaggedError("PRBranchProtectionError")<{
	readonly message: string
	readonly operation: "push" | "pr-create"
	readonly issueId?: string
	readonly branch?: string
	readonly baseBranch?: string
}> {}

/**
 * Error when PR is not found
 */
export class PRNotFoundError extends Data.TaggedError("PRNotFoundError")<{
	readonly issueId: string
	readonly branch: string
}> {}

/**
 * Error when merge has conflicts
 */
export class MergeConflictError extends Data.TaggedError("MergeConflictError")<{
	readonly issueId: string
	readonly branch: string
	readonly message: string
}> {}

/**
 * Error when type-check fails after merge
 * This indicates the merged code has type errors that need fixing
 */
export class TypeCheckError extends Data.TaggedError("TypeCheckError")<{
	readonly issueId: string
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
	 * 1. Sync tracker changes
	 * 2. Commit any uncommitted changes
	 * 3. Push branch to origin
	 * 4. Create PR via gh CLI
	 * 5. Link PR URL back to bead
	 *
	 * @example
	 * ```ts
	 * const pr = yield* prWorkflow.createPR({
	 *   issueId: "az-05y",
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
		| PRAlreadyExistsError
		| PRBranchProtectionError
		| GitError
		| NotAGitRepoError
		| IssueTrackerError
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
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly getPR: (options: {
		issueId: string
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
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly cleanup: (
		options: CleanupOptions,
	) => Effect.Effect<
		void,
		PRError | GitError | NotAGitRepoError | SessionError | TmuxError | IssueTrackerError,
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
	 * 2. Sync tracker in worktree (tracker sync --from-main)
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
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly mergeToMain: (
		options: MergeToMainOptions,
	) => Effect.Effect<
		void,
		| PRError
		| GitOperationInProgressError
		| MergeConflictError
		| TypeCheckError
		| GitError
		| NotAGitRepoError
		| SessionError
		| TmuxError
		| IssueTrackerError
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
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * if (check.hasConflictRisk) {
	 *   // Real conflicts exist - must resolve before merge
	 *   console.log("Conflicts in:", check.conflictingFiles)
	 * }
	 * ```
	 */
	readonly checkMergeConflicts: (options: {
		issueId: string
		projectPath: string
	}) => Effect.Effect<
		MergeConflictCheck,
		PRError | GitOperationInProgressError | GitError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Abort an in-progress merge in the worktree
	 *
	 * Use this when a merge conflict resolution is stuck or you want to cancel
	 * the merge operation. Runs `git merge --abort` in the worktree.
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.abortMerge({
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly abortMerge: (options: {
		issueId: string
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
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * if (result.hasUncommittedChanges) {
	 *   // Show warning or block operation
	 * }
	 * ```
	 */
	readonly checkUncommittedChanges: (options: {
		issueId: string
		projectPath: string
	}) => Effect.Effect<
		{ hasUncommittedChanges: boolean; changedFiles: readonly string[] },
		PRError | GitError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Update worktree from base branch (typically main)
	 *
	 * Merges the base branch into the worktree, resolving conflicts with AI assistance if needed.
	 * This is the inverse of mergeToMain - it brings main INTO the worktree.
	 *
	 * Workflow:
	 * 1. Fetch latest from origin
	 * 2. Check for conflicts using git merge-tree
	 * 3. If conflicts: start merge, have AI resolve
	 * 4. If no conflicts: fast-forward merge
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.updateFromBase({
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly updateFromBase: (
		options: UpdateFromBaseOptions,
	) => Effect.Effect<
		void,
		| PRError
		| GitOperationInProgressError
		| MergeConflictError
		| GitError
		| NotAGitRepoError
		| IssueTrackerError
		| NotFoundError,
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
	 *   issueId: "az-05y",
	 *   projectPath: "/Users/user/project"
	 * })
	 * if (comments.length > 0) {
	 *   // Inject into AI session context
	 * }
	 * ```
	 */
	readonly getPRComments: (
		options: GetPRCommentsOptions,
	) => Effect.Effect<readonly PRComment[], PRError | GHCLIError, CommandExecutor.CommandExecutor>

	/**
	 * Get the effective base branch for a bead
	 *
	 * Returns the parent epic's branch if the bead is a child of an epic,
	 * otherwise returns the standard base branch (main or origin/main depending on workflowMode).
	 *
	 * This enables epic-consolidated PRs where child tasks target the epic branch
	 * instead of main, allowing all child work to be merged into the epic first,
	 * then a single epic PR goes to main.
	 *
	 * @example
	 * ```ts
	 * const baseBranch = yield* prWorkflow.getEffectiveBaseBranchForIssue({
	 *   issueId: "az-task",
	 *   projectPath: "/Users/user/project"
	 * })
	 * // Returns "az-epic" if az-task is child of az-epic
	 * // Returns "main" or "origin/main" otherwise
	 * ```
	 */
	readonly getEffectiveBaseBranchForIssue: (options: {
		issueId: string
		projectPath: string
	}) => Effect.Effect<
		{ baseBranch: string; parentEpic: Issue | undefined },
		IssueTrackerError | NotFoundError | ParseError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Merge one bead's branch into another bead's branch
	 *
	 * This allows consolidating work from bead A into bead B without going through main.
	 * Useful when you realize exploratory work in A belongs with other work in B.
	 *
	 * Workflow:
	 * 1. Validate source bead has commits
	 * 2. Ensure target bead has a branch/worktree (create if needed)
	 * 3. Fetch source branch
	 * 4. Check for conflicts using git merge-tree
	 * 5. If conflicts: start merge in target worktree, spawn AI to resolve
	 * 6. If clean: merge source into target
	 * 7. Sync tracker
	 * 8. Close source bead
	 *
	 * @example
	 * ```ts
	 * yield* prWorkflow.mergeIssueIntoIssue({
	 *   sourceIssueId: "az-05y",
	 *   targetIssueId: "az-06z",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly mergeIssueIntoIssue: (options: {
		sourceIssueId: string
		targetIssueId: string
		projectPath: string
	}) => Effect.Effect<
		void,
		| PRError
		| MergeConflictError
		| GitError
		| NotAGitRepoError
		| IssueTrackerError
		| NotFoundError
		| TmuxError,
		CommandExecutor.CommandExecutor
	>

	/**
	 * Get the target branch for a bead's merge/PR operations.
	 *
	 * For epic children: returns the parent epic's branch
	 * For standalone tasks/epics: returns the configured base branch (usually "main")
	 *
	 * @example
	 * ```ts
	 * const { targetBranch, isEpicChild } = yield* prWorkflow.getTargetBranch(issueId, projectPath)
	 * // targetBranch: "main" or "az-epic-123"
	 * // isEpicChild: true if merging to parent epic
	 * ```
	 */
	readonly getTargetBranch: (
		issueId: string,
		projectPath: string,
	) => Effect.Effect<
		{ targetBranch: string; isEpicChild: boolean },
		IssueTrackerError | NotFoundError | ParseError,
		CommandExecutor.CommandExecutor
	>
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
		const trimmedCommand = commandStr.trim()
		if (!trimmedCommand) {
			return { success: false, output: "Empty command" }
		}

		// Run through a shell so config commands can use syntax like:
		// - "cd ts-opentui && bun run type-check"
		// - pipes, redirects, env var assignments, etc.
		const shellCommand = Command.make("sh", "-lc", trimmedCommand).pipe(
			Command.workingDirectory(cwd),
		)

		return yield* Command.string(shellCommand).pipe(
			Effect.map((output) => ({ success: true, output })),
			Effect.catchAll((error) => {
				const fallback = String(error)
				const output = getCommandErrorOutput(error)

				return Effect.succeed({
					success: false,
					output: output.length > 0 ? output : fallback,
				})
			}),
		)
	})

const getCommandErrorField = (
	error: unknown,
	fieldName: "stdout" | "stderr",
): string | undefined => {
	if (typeof error !== "object" || error === null || !(fieldName in error)) {
		return undefined
	}
	const value = Reflect.get(error, fieldName)
	if (typeof value === "string") {
		return value
	}
	if (value === undefined || value === null) {
		return undefined
	}
	return String(value)
}

const getCommandErrorOutput = (error: unknown): string => {
	const stderr = getCommandErrorField(error, "stderr")
	const stdout = getCommandErrorField(error, "stdout")
	return [stderr, stdout]
		.filter((part): part is string => part !== undefined && part.length > 0)
		.join("\n")
		.trim()
}

const hasStagedChanges = (
	cwd: string,
): Effect.Effect<boolean, GitError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", "diff", "--cached", "--quiet").pipe(
			Command.workingDirectory(cwd),
		)
		const exitCode = yield* Command.exitCode(command).pipe(
			Effect.mapError(
				(error) =>
					new GitError({
						message: `git diff --cached --quiet failed: ${String(error)}`,
						command: "git diff --cached --quiet",
					}),
			),
		)
		if (exitCode === 0) return false
		if (exitCode === 1) return true
		return yield* Effect.fail(
			new GitError({
				message: `git diff --cached --quiet returned unexpected exit code: ${exitCode}`,
				command: "git diff --cached --quiet",
			}),
		)
	})

/**
 * Execute a git command and return stdout
 */
const runGit = (
	args: readonly string[],
	cwd: string,
): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const indexLockPath = yield* Command.string(
			Command.make("git", "rev-parse", "--git-path", "index.lock").pipe(
				Command.workingDirectory(cwd),
			),
		).pipe(
			Effect.map((output) => output.trim()),
			Effect.catchAll(() => Effect.succeed(undefined)),
		)
		if (indexLockPath) {
			const lockExists = yield* Command.exitCode(Command.make("test", "-e", indexLockPath)).pipe(
				Effect.map((code) => code === 0),
				Effect.catchAll(() => Effect.succeed(false)),
			)
			if (lockExists) {
				return yield* Effect.fail(
					new GitError({
						message: `git command blocked by existing lock file: ${indexLockPath}`,
						command: `git ${args.join(" ")}`,
						stderr: `lock file exists: ${indexLockPath}`,
						recovery: createStaleLockRecoveryHint(indexLockPath),
					}),
				)
			}
		}

		const command = Command.make("git", ...args).pipe(Command.workingDirectory(cwd))
		return yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = getCommandErrorOutput(error) || String(error)
				return new GitError({
					message: `git ${args.join(" ")} failed: ${stderr}`,
					command: `git ${args.join(" ")}`,
					stderr,
					recovery: extractGitRecoveryHint(stderr),
				})
			}),
		)
	})

/**
 * Supported in-progress git operation descriptors.
 */
type GitOperationInProgress = {
	readonly kind: "merge" | "rebase" | "cherry-pick" | "revert"
	readonly pseudoRef: "MERGE_HEAD" | "REBASE_HEAD" | "CHERRY_PICK_HEAD" | "REVERT_HEAD"
	readonly continueArgs: readonly [string, "--continue"]
	readonly abortArgs: readonly [string, "--abort"]
}

const GIT_OPERATION_IN_PROGRESS_CHECKS: readonly GitOperationInProgress[] = [
	{
		kind: "merge",
		pseudoRef: "MERGE_HEAD",
		continueArgs: ["merge", "--continue"],
		abortArgs: ["merge", "--abort"],
	},
	{
		kind: "rebase",
		pseudoRef: "REBASE_HEAD",
		continueArgs: ["rebase", "--continue"],
		abortArgs: ["rebase", "--abort"],
	},
	{
		kind: "cherry-pick",
		pseudoRef: "CHERRY_PICK_HEAD",
		continueArgs: ["cherry-pick", "--continue"],
		abortArgs: ["cherry-pick", "--abort"],
	},
	{
		kind: "revert",
		pseudoRef: "REVERT_HEAD",
		continueArgs: ["revert", "--continue"],
		abortArgs: ["revert", "--abort"],
	},
]

/**
 * Check whether a git pseudo-ref exists in the target repository.
 */
const hasPseudoRef = (
	cwd: string,
	refName: GitOperationInProgress["pseudoRef"],
): Effect.Effect<boolean, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", "rev-parse", "-q", "--verify", refName).pipe(
			Command.workingDirectory(cwd),
		)
		const exitCode = yield* Command.exitCode(command).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(1)),
				),
			),
		)
		return exitCode === 0
	})

/**
 * Detect the first in-progress git operation in the target repository.
 */
const getGitOperationInProgress = (
	cwd: string,
): Effect.Effect<GitOperationInProgress | undefined, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		for (const operation of GIT_OPERATION_IN_PROGRESS_CHECKS) {
			const present = yield* hasPseudoRef(cwd, operation.pseudoRef)
			if (present) return operation
		}
		return undefined
	})

/**
 * Fail fast when target repository is already in an active git operation state.
 *
 * This prevents ambiguous failures later in merge flow and gives clear recovery guidance.
 */
const ensureNoGitOperationInProgress = (options: {
	cwd: string
	issueId: string
	contextLabel: string
}): Effect.Effect<void, GitOperationInProgressError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const { cwd, issueId, contextLabel } = options
		const operation = yield* getGitOperationInProgress(cwd)
		if (!operation) return

		const continueCommand = `git -C ${cwd} ${operation.continueArgs.join(" ")}`
		const abortCommand = `git -C ${cwd} ${operation.abortArgs.join(" ")}`

		return yield* Effect.fail(
			new GitOperationInProgressError({
				issueId,
				operation: operation.kind,
				contextLabel,
				continueCommand,
				abortCommand,
				message: `Git ${operation.kind} in progress in ${contextLabel}. Continue with '${continueCommand}' after resolving conflicts, or abort with '${abortCommand}', then retry the action.`,
			}),
		)
	})

/**
 * Execute a gh command and return stdout
 */
const runGH = (
	args: readonly string[],
	cwd: string,
): Effect.Effect<
	string,
	PRError | GHCLIError | PRAlreadyExistsError | PRBranchProtectionError,
	CommandExecutor.CommandExecutor
> =>
	Effect.gen(function* () {
		const command = Command.make("gh", ...args).pipe(Command.workingDirectory(cwd))
		return yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const errorStr = String(error)
				const isPRCreate = args[0] === "pr" && args[1] === "create"

				if (errorStr.includes("gh auth login") || errorStr.includes("not logged")) {
					return new GHCLIError({ message: "gh CLI not authenticated. Run: gh auth login" })
				}
				if (errorStr.includes("command not found") || errorStr.includes("ENOENT")) {
					return new GHCLIError({ message: "gh CLI not installed. Run: brew install gh" })
				}
				if (isPRCreate && errorStr.includes("already exists")) {
					return new PRAlreadyExistsError({
						message: "A pull request already exists for this branch",
					})
				}
				if (
					isPRCreate &&
					(errorStr.includes("protected branch") || errorStr.includes("branch protection"))
				) {
					return new PRBranchProtectionError({
						operation: "pr-create",
						message: "Branch protection prevented PR creation",
					})
				}
				return new PRError({
					message: `gh ${args.join(" ")} failed: ${errorStr}`,
					command: `gh ${args.join(" ")}`,
				})
			}),
		)
	})

const buildPRCreateAIPrompt = (
	issueId: string,
	projectPath: string,
): Effect.Effect<string, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const promptOutput = yield* Command.string(
			Command.make("env", `AZEDARACH_ISSUE_ID=${issueId}`, "az", "prompt", "pr", "create").pipe(
				Command.workingDirectory(projectPath),
			),
		).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Failed to generate PR AI prompt via az: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed("")),
				),
			),
		)

		const prompt = promptOutput.trim()
		if (prompt.length > 0) {
			return prompt
		}

		return `work on issue ${issueId}: create a high-quality pull request now.

Start by running \`az prime\`.
Then run \`az prompt pr create\` and follow that guidance.
Create or update the PR with improved title/body/checklist based on the current branch diff.`
	})

/**
 * Generate PR title from bead
 */
const generateIssuePRTitle = (issue: Issue): string => {
	const typePrefix = issue.issue_type ? `[${issue.issue_type}] ` : ""
	return `${typePrefix}${issue.title} (${issue.id})`
}

/**
 * Generate PR body from bead
 */
interface PRDraftContext {
	readonly baseBranch: string
	readonly commitSubjects: readonly string[]
	readonly changedFiles: readonly string[]
}

const toNonEmptyLines = (input: string): readonly string[] =>
	input
		.split("\n")
		.map((line) => line.trim())
		.filter((line) => line.length > 0)

const limitWithOverflow = (
	items: readonly string[],
	limit: number,
): { readonly visible: readonly string[]; readonly overflowCount: number } => ({
	visible: items.slice(0, limit),
	overflowCount: Math.max(0, items.length - limit),
})

const generateIssuePRBody = (issue: Issue, draftContext?: PRDraftContext): string => {
	const lines: string[] = []

	lines.push(`## Summary`)
	lines.push(``)
	lines.push(`Resolves ${issue.id}: ${issue.title}`)
	if (draftContext) {
		lines.push(`Base branch: \`${draftContext.baseBranch}\``)
	}
	lines.push(``)

	if (issue.description) {
		lines.push(`## Description`)
		lines.push(``)
		lines.push(issue.description)
		lines.push(``)
	}

	if (issue.design) {
		lines.push(`## Design Notes`)
		lines.push(``)
		lines.push(issue.design)
		lines.push(``)
	}

	if (draftContext && draftContext.commitSubjects.length > 0) {
		const { visible, overflowCount } = limitWithOverflow(draftContext.commitSubjects, 8)
		lines.push(`## What Changed`)
		lines.push(``)
		for (const subject of visible) {
			lines.push(`- ${subject}`)
		}
		if (overflowCount > 0) {
			lines.push(`- ...and ${overflowCount} more commit${overflowCount === 1 ? "" : "s"}`)
		}
		lines.push(``)
	}

	if (draftContext && draftContext.changedFiles.length > 0) {
		const { visible, overflowCount } = limitWithOverflow(draftContext.changedFiles, 20)
		lines.push(`## Changed Files`)
		lines.push(``)
		for (const file of visible) {
			lines.push(`- \`${file}\``)
		}
		if (overflowCount > 0) {
			lines.push(`- ...and ${overflowCount} more file${overflowCount === 1 ? "" : "s"}`)
		}
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
	const data = Schema.decodeUnknownSync(
		Schema.parseJson(
			Schema.Struct({
				number: Schema.Number,
				url: Schema.String,
				title: Schema.String,
				state: Schema.Literal("OPEN", "CLOSED", "MERGED"),
				isDraft: Schema.optional(Schema.Boolean),
				headRefName: Schema.String,
			}),
		),
	)(json)
	const prState = (state: "OPEN" | "CLOSED" | "MERGED"): PR["state"] => {
		switch (state) {
			case "OPEN":
				return "open"
			case "CLOSED":
				return "closed"
			case "MERGED":
				return "merged"
		}
	}
	return {
		number: data.number,
		url: data.url,
		title: data.title,
		state: prState(data.state),
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
 *     issueId: "az-123",
 *     projectPath: process.cwd()
 *   })
 *   return pr
 * }).pipe(Effect.provide(PRWorkflow.Default))
 * ```
 */
export class PRWorkflow extends Effect.Service<PRWorkflow>()("PRWorkflow", {
	dependencies: [
		WorktreeManager.Default,
		IssueTrackerClient.Default,
		SessionManager.Default,
		TmuxService.Default,
		WorktreeSessionService.Default,
		FileLockManager.Default,
		AppConfig.Default,
		OfflineService.Default,
		ImageAttachmentService.Default,
		DiagnosticsService.Default,
	],
	scoped: Effect.gen(function* () {
		const serviceScope = yield* Effect.scope
		const worktreeManager = yield* WorktreeManager
		const issueTrackerClient = yield* IssueTrackerClient
		const sessionManager = yield* SessionManager
		const tmuxService = yield* TmuxService
		const worktreeSession = yield* WorktreeSessionService
		const fileLockManager = yield* FileLockManager
		const appConfig = yield* AppConfig
		const offlineService = yield* OfflineService
		const imageAttachmentService = yield* ImageAttachmentService
		const diagnostics = yield* DiagnosticsService
		const getMergeConfig = () => appConfig.getMergeConfig()
		const getGitConfig = () => appConfig.getGitConfig()

		const getIssueBranchName = (
			issueId: string,
			projectPath: string,
		): Effect.Effect<string, never, CommandExecutor.CommandExecutor> =>
			worktreeManager.get({ issueId, projectPath }).pipe(
				Effect.map((worktree) => worktree?.branch || issueId),
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(issueId)),
					),
				),
			)

		/**
		 * Execute an effect with exclusive tracker sync lock.
		 * Uses Effect.acquireUseRelease for guaranteed cleanup.
		 * Fails gracefully if lock cannot be acquired.
		 */
		const withSyncLock = <A, E, R>(effect: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> =>
			Effect.acquireUseRelease(
				// Acquire: get the lock (null if failed)
				fileLockManager
					.acquireLock({
						path: ISSUE_TRACKER_SYNC_LOCK_PATH,
						type: "exclusive",
						timeout: ISSUE_TRACKER_SYNC_LOCK_TIMEOUT,
					})
					.pipe(Effect.option),
				// Use: run the effect
				() => effect,
				// Release: release the lock if acquired
				(lockOption) =>
					Option.isSome(lockOption) ? fileLockManager.releaseLock(lockOption.value) : Effect.void,
			)

		/**
		 * Internal helper to get effective base branch for a bead.
		 * Uses parent epic branch if child of epic, otherwise uses configured base branch.
		 */
		const getIssueBaseBranch = (
			issueId: string,
			projectPath: string,
			explicitBaseBranch?: string,
		): Effect.Effect<
			{ baseBranch: string; parentEpic: Issue | undefined },
			IssueTrackerError | NotFoundError | ParseError | SyncRequiredError,
			CommandExecutor.CommandExecutor
		> =>
			Effect.gen(function* () {
				if (explicitBaseBranch) {
					return { baseBranch: explicitBaseBranch, parentEpic: undefined }
				}
				const gitConfig = yield* getGitConfig()
				const parentEpic = yield* issueTrackerClient.getParentEpic(issueId)
				if (parentEpic) {
					// Child of epic: target the epic branch (mapped branch name when available)
					const baseBranch = yield* getIssueBranchName(parentEpic.id, projectPath)
					return { baseBranch, parentEpic }
				}
				// No parent epic: use the standard base branch
				return { baseBranch: gitConfig.baseBranch, parentEpic: undefined }
			})

		/**
		 * Resolve which repository context owns the effective base branch operation state.
		 *
		 * Standalone tasks use the project base-branch context.
		 * Epic children use the parent epic worktree when available.
		 */
		const getBaseOperationContext = (
			projectPath: string,
			parentEpic: Issue | undefined,
		): Effect.Effect<
			{ cwd: string; contextLabel: string },
			GitError | NotAGitRepoError,
			CommandExecutor.CommandExecutor
		> =>
			Effect.gen(function* () {
				if (!parentEpic) {
					return { cwd: projectPath, contextLabel: "project base branch" }
				}

				const epicWorktree = yield* worktreeManager.get({
					issueId: parentEpic.id,
					projectPath,
				})

				if (epicWorktree) {
					return {
						cwd: epicWorktree.path,
						contextLabel: `epic ${parentEpic.id} worktree`,
					}
				}

				return {
					cwd: projectPath,
					contextLabel: `epic ${parentEpic.id} branch context`,
				}
			})

		return {
			createPR: (options: CreatePROptions) =>
				diagnostics.measure(
					{
						source: "PRWorkflow",
						name: "createPR",
						thresholdMs: 1000,
						details: `issueId=${options.issueId}`,
					},
					Effect.gen(function* () {
						const { issueId, projectPath, draft = true, baseBranch: explicitBaseBranch } = options

						// Determine effective base branch (epic branch for children, main otherwise)
						const { baseBranch, parentEpic } = yield* getIssueBaseBranch(
							issueId,
							projectPath,
							explicitBaseBranch,
						)

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

						// Get issue info for PR title/body
						const issue = yield* issueTrackerClient.show(issueId)

						// Log context for debugging
						if (parentEpic) {
							yield* Effect.log(`Creating PR for ${issueId} targeting epic branch ${parentEpic.id}`)
						}

						// Get worktree info
						const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
						if (!worktree) {
							return yield* Effect.fail(
								new PRError({
									message: `No worktree found for ${issueId}`,
									issueId,
								}),
							)
						}
						const issueBranch = worktree.branch

						// Invoke AI at PR-create time:
						// 1. Reuse existing issue code window when available
						// 2. Otherwise start a new issue session with injected PR-create prompt
						yield* Effect.gen(function* () {
							const prPrompt = yield* buildPRCreateAIPrompt(issueId, projectPath)
							const sessionName = getIssueSessionName(issueId, projectPath)
							const codeTarget = `${sessionName}:${WINDOW_NAMES.CODE}`
							const prWindowTarget = "pr-create"
							const cliTool = yield* appConfig.getCliTool()
							const modelConfig = yield* appConfig.getModelConfig()
							const prConfig = yield* appConfig.getPRConfig()
							const sessionConfig = yield* appConfig.getSessionConfig()
							const toolDef = getToolDefinition(cliTool)
							const toolDefaultModel = modelConfig[cliTool].default ?? modelConfig.default
							const prAgentModel = prConfig.aiModel ?? toolDefaultModel
							const preferDedicatedAgentWindow =
								prConfig.aiModel !== undefined && prConfig.aiModel !== toolDefaultModel
							const prAgentCommand = toolDef.buildCommand({
								initialPrompt: prPrompt,
								issueId,
								model: prAgentModel,
								dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
							})

							const hasSession = yield* tmuxService.hasSession(sessionName)
							if (hasSession) {
								const hasCodeWindow = yield* tmuxService.hasWindow(sessionName, WINDOW_NAMES.CODE)
								if (hasCodeWindow && !preferDedicatedAgentWindow) {
									yield* tmuxService.sendLiteralCommand(codeTarget, prPrompt)
									yield* Effect.log(`Queued PR-create AI prompt in existing session ${codeTarget}`)
									return
								}

								yield* worktreeSession.ensureWindow(sessionName, prWindowTarget, {
									cwd: worktree.path,
									command: prAgentCommand,
								})
								yield* Effect.log(
									`Started PR-create AI agent in ${sessionName}:${prWindowTarget}${prAgentModel ? ` (model=${prAgentModel})` : ""}`,
								)
								return
							}

							yield* sessionManager
								.start({
									issueId,
									projectPath,
									initialPrompt: prPrompt,
									model: prAgentModel,
								})
								.pipe(Effect.asVoid)

							yield* Effect.log(
								`Started session for PR-create AI prompt (${issueId})${prAgentModel ? ` with model=${prAgentModel}` : ""}`,
							)
						}).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(
									`Failed to invoke AI at PR creation for ${issueId}; continuing PR workflow: ${String(error)}`,
								).pipe(Effect.asVoid),
							),
						)

						// Sync tracker changes (with lock to prevent races)
						yield* withSyncLock(
							issueTrackerClient
								.sync(worktree.path)
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								),
						)

						// Stage and commit any changes
						yield* runGit(["add", "-A"], worktree.path)
						const shouldCommit = yield* hasStagedChanges(worktree.path)
						if (shouldCommit) {
							yield* runGit(["commit", "-m", `Complete ${issueId}: ${issue.title}`], worktree.path)
						}

						// Push branch to origin
						yield* runGit(["push", "-u", "origin", issueBranch], worktree.path).pipe(
							Effect.mapError((e) => {
								const stderr = e.stderr ?? e.message
								if (stderr.includes("protected branch") || stderr.includes("branch protection")) {
									return new PRBranchProtectionError({
										operation: "push",
										issueId,
										branch: issueBranch,
										baseBranch,
										message: `Push blocked by branch protection for ${issueBranch}`,
									})
								}
								return new GitError({
									message: `Failed to push branch: ${e.message}`,
									command: "git push",
								})
							}),
						)

						// Build richer PR draft context from branch history and diff
						const draftContext = yield* Effect.gen(function* () {
							const commitRange = `${baseBranch}..HEAD`
							const diffRange = `${baseBranch}...HEAD`

							const commitSubjectsOutput = yield* runGit(
								["log", "--format=%s", commitRange],
								worktree.path,
							).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(
										`Failed to collect commit subjects for PR draft: ${error}`,
									).pipe(Effect.zipRight(Effect.succeed(""))),
								),
							)

							const changedFilesOutput = yield* runGit(
								["diff", "--name-only", diffRange],
								worktree.path,
							).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Failed to collect changed files for PR draft: ${error}`).pipe(
										Effect.zipRight(Effect.succeed("")),
									),
								),
							)

							const commitSubjects = toNonEmptyLines(commitSubjectsOutput)
							const changedFiles = toNonEmptyLines(changedFilesOutput)

							return {
								baseBranch,
								commitSubjects,
								changedFiles,
							} satisfies PRDraftContext
						})

						// Generate PR title and body
						const title = options.title ?? generateIssuePRTitle(issue)
						const body = options.body ?? generateIssuePRBody(issue, draftContext)

						// Create PR via gh CLI
						const ghArgs = ["pr", "create", "--title", title, "--body", body, "--base", baseBranch]
						if (draft) {
							ghArgs.push("--draft")
						}

						const prUrl = yield* runGH(ghArgs, worktree.path).pipe(
							Effect.map((output) => output.trim()),
							Effect.mapError((error) => {
								switch (error._tag) {
									case "PRAlreadyExistsError":
										return new PRAlreadyExistsError({
											message: error.message,
											issueId,
											branch: issueBranch,
											baseBranch,
										})
									case "PRBranchProtectionError":
										return new PRBranchProtectionError({
											operation: error.operation,
											message: error.message,
											issueId,
											branch: issueBranch,
											baseBranch,
										})
									default:
										return error
								}
							}),
						)

						// Extract PR number from URL
						const prNumberMatch = prUrl.match(/\/pull\/(\d+)/)
						const prNumber = prNumberMatch ? parseInt(prNumberMatch[1], 10) : 0

						// Link PR back to bead
						yield* issueTrackerClient
							.update(issueId, {
								notes: `PR: ${prUrl}`,
							})
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)

						return {
							number: prNumber,
							url: prUrl,
							title,
							state: "open" as const,
							draft,
							branch: issueBranch,
						}
					}).pipe(Effect.withSpan("pr.create")),
				),

			getPR: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					const gitConfig = yield* getGitConfig()
					const _baseBranch = gitConfig.baseBranch

					// Try to get PR info for the branch
					const issueBranch = yield* getIssueBranchName(issueId, projectPath)
					const result = yield* runGH(
						["pr", "view", issueBranch, "--json", "number,url,title,state,isDraft,headRefName"],
						projectPath,
					).pipe(
						Effect.map((output) => Option.some(parsePRJson(output))),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(Option.none<PR>())),
							),
						),
					)

					return result
				}),

			cleanup: (options: CleanupOptions) =>
				diagnostics.measure(
					{
						source: "PRWorkflow",
						name: "cleanup",
						thresholdMs: 800,
						details: `issueId=${options.issueId}`,
					},
					Effect.gen(function* () {
						const { issueId, projectPath, deleteRemoteBranch = true, closeIssue = true } = options
						const issueBranch = yield* getIssueBranchName(issueId, projectPath)

						// 1. Stop any running session (ignore errors)
						// First try SessionManager.stop (handles tracker sync from worktree)
						yield* sessionManager
							.stop(issueId)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)
						// Also directly kill tmux session in case it wasn't tracked in memory
						yield* tmuxService
							.killSession(issueId)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)

						// 2. Delete worktree
						yield* worktreeManager.remove({ issueId: issueId, projectPath })

						// 3. Delete remote branch (optional, only if online)
						if (deleteRemoteBranch) {
							const pushStatus = yield* offlineService.isGitPushEnabled()
							if (pushStatus.enabled) {
								yield* runGit(["push", "origin", "--delete", issueBranch], projectPath).pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									), // Ignore if already deleted
								)
							}
							// Silently skip if offline - remote branch can be cleaned up later
						}

						// 4. Delete local branch
						yield* runGit(["branch", "-D", issueBranch], projectPath).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.void),
								),
							), // Ignore if already deleted
						)

						// 5. Close bead issue (optional) and sync to persist the change
						if (closeIssue) {
							yield* issueTrackerClient
								.update(issueId, { status: "closed" })
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								)

							// Clean up images for the closed issue (temporary debugging images)
							// This prevents unbounded growth of local attachment storage
							yield* imageAttachmentService
								.cleanupImagesForIssue(issueId)
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								)

							// Sync the closed status to JSONL and commit it
							// This fixes az-o5m9: merged tasks being left in in_progress status
							yield* withSyncLock(
								issueTrackerClient
									.sync(projectPath)
									.pipe(
										Effect.catchAll((error) =>
											Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
												Effect.zipRight(Effect.void),
											),
										),
									),
							)
						}
					}).pipe(Effect.withSpan("pr.cleanup")),
				),

			checkGHCLI: () =>
				Effect.gen(function* () {
					const command = Command.make("gh", "auth", "status")
					const exitCode = yield* Command.exitCode(command).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(1)),
							),
						),
					)
					return exitCode === 0
				}),

			mergeToMain: (options: MergeToMainOptions) =>
				diagnostics.measure(
					{
						source: "PRWorkflow",
						name: "mergeToMain",
						thresholdMs: 1000,
						details: `issueId=${options.issueId}`,
					},
					Effect.gen(function* () {
						const gitConfig = yield* getGitConfig()
						const {
							issueId,
							projectPath,
							pushToOrigin = gitConfig.pushEnabled,
							closeIssue = false,
							keepWorktree = true,
							onProgress,
							onDeferredPushStatus,
						} = options
						const reportProgress = (stage: MergeToMainProgressStage) =>
							onProgress ? onProgress(stage) : Effect.void
						const reportDeferredPush = (status: MergeToMainDeferredPushStatus) =>
							onDeferredPushStatus ? onDeferredPushStatus(status) : Effect.void
						yield* reportProgress("prepare")

						// Determine effective base branch (epic branch for children, main for epics/standalone)
						const { baseBranch, parentEpic } = yield* getIssueBaseBranch(issueId, projectPath)

						// Log context for debugging
						if (parentEpic) {
							yield* Effect.log(`Merging ${issueId} into epic branch ${parentEpic.id} (not main)`)
						}

						// If merging into a parent epic, we need to merge IN the epic's worktree
						// because git won't let us checkout a branch that's in use by another worktree.
						// If epic has no worktree, create one first.
						let mergeDir = projectPath
						if (parentEpic) {
							let epicWorktree = yield* worktreeManager.get({
								issueId: parentEpic.id,
								projectPath,
							})
							if (!epicWorktree) {
								yield* Effect.log(`Creating worktree for epic ${parentEpic.id} to receive merge`)
								epicWorktree = yield* worktreeManager.create({
									issueId: parentEpic.id,
									issueTitle: parentEpic.title,
									branchSlugMaxLength: gitConfig.branchSlugMaxLength,
									projectPath,
								})
							}
							if (!epicWorktree) {
								return yield* Effect.die(`Failed to resolve worktree for epic ${parentEpic.id}`)
							}
							mergeDir = epicWorktree.path
							yield* Effect.log(`Merging ${issueId} in epic worktree ${mergeDir}`)
						}

						// Fail fast if the merge target repository already has an in-progress git operation.
						// Continuing would produce opaque git errors and non-deterministic outcomes.
						yield* ensureNoGitOperationInProgress({
							cwd: mergeDir,
							issueId,
							contextLabel: parentEpic ? `epic ${parentEpic.id} worktree` : "project base branch",
						})

						// Get issue info for merge commit message
						const issue = yield* issueTrackerClient.show(issueId)

						// Get worktree info
						const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
						if (!worktree) {
							return yield* Effect.fail(
								new PRError({
									message: `No worktree found for ${issueId}`,
									issueId,
								}),
							)
						}
						const issueBranch = worktree.branch

						// Fail fast if source worktree already has an in-progress git operation.
						// This commonly occurs when a prior conflict-resolution merge was started
						// but not completed/aborted.
						yield* ensureNoGitOperationInProgress({
							cwd: worktree.path,
							issueId,
							contextLabel: `source worktree ${issueId}`,
						})

						// 1. Stop any running session first (only if doing full cleanup)
						// When keepWorktree=true, we want to keep iterating in the same session
						if (!keepWorktree) {
							yield* sessionManager
								.stop(issueId)
								.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to stop session: ${e}`)))
							yield* tmuxService
								.killSession(issueId)
								.pipe(
									Effect.catchAll((e) => Effect.logWarning(`Failed to kill tmux session: ${e}`)),
								)
						}

						// 2. Stage and commit any uncommitted changes in worktree
						yield* reportProgress("commit")
						yield* runGit(["add", "-A"], worktree.path)
						const shouldCommit = yield* hasStagedChanges(worktree.path)
						if (shouldCommit) {
							yield* runGit(["commit", "-m", `Complete ${issueId}: ${issue.title}`], worktree.path)
						}

						// 3. Check for non-tracker conflicts using merge-tree (safe, in-memory)
						// We only care about conflicts in actual code files, not .azedarach/
						yield* reportProgress("check-conflicts")
						const mergeTreeResult = yield* Effect.gen(function* () {
							const command = Command.make(
								"git",
								"merge-tree",
								"--write-tree",
								"--name-only",
								baseBranch,
								issueBranch,
							).pipe(Command.workingDirectory(mergeDir))

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
								[
									"merge-tree",
									"--write-tree",
									"--name-only",
									"--no-messages",
									baseBranch,
									issueBranch,
								],
								mergeDir,
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
								// Filter OUT .azedarach/ files - we handle those separately
								.filter((f) => !f.startsWith(".azedarach/"))

							return {
								hasConflicts: conflictingFiles.length > 0,
								conflictingFiles,
							}
						})

						// 4. If there are real code conflicts (not .azedarach/), ask AI to resolve
						yield* reportProgress("merge")
						if (mergeTreeResult.hasConflicts) {
							const fileList = mergeTreeResult.conflictingFiles.join(", ")

							// Start merge in worktree so AI can resolve
							yield* runGit(
								["merge", baseBranch, "-m", `Merge ${baseBranch} into ${issueBranch}`],
								worktree.path,
							).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							) // Will fail with conflicts, that's expected

							const resolvePrompt = `There are merge conflicts in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution.`

							const windowName = "merge"
							const sessionName = getIssueSessionName(issueId, projectPath)

							const cliTool = yield* appConfig.getCliTool()
							const sessionConfig = yield* appConfig.getSessionConfig()
							const modelConfig = yield* appConfig.getModelConfig()
							const toolDef = getToolDefinition(cliTool)
							const toolModelConfig = modelConfig[cliTool]
							const effectiveModel = toolModelConfig.default ?? modelConfig.default

							const command = toolDef.buildCommand({
								initialPrompt: resolvePrompt,
								issueId,
								model: effectiveModel,
								dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
							})

							yield* worktreeSession.ensureWindow(sessionName, windowName, {
								cwd: worktree.path,
								command,
							})

							const message = `Code conflicts detected in: ${fileList}. Started AI session in '${windowName}' window to resolve. Retry merge after resolution.`

							return yield* Effect.fail(
								new MergeConflictError({
									issueId,
									branch: issueBranch,
									message,
								}),
							)
						}

						// 5. No code conflicts - safe to merge
						// If merging in main project, checkout base branch first
						// If merging in epic's worktree, branch is already checked out
						if (mergeDir === projectPath) {
							yield* runGit(["checkout", baseBranch], projectPath).pipe(
								Effect.mapError(
									(e) =>
										new GitError({
											message: `Failed to checkout ${baseBranch}: ${e.message}`,
											command: `git checkout ${baseBranch}`,
										}),
								),
							)
						}

						// 6. Merge branch with strategy to favor 'ours' for .azedarach/ conflicts
						// This ensures .azedarach/issues.jsonl from base branch is preserved during merge
						const mergeMessage = `Merge ${issueId}: ${issue.title}`
						yield* runGit(
							["merge", issueBranch, "--no-ff", "-m", mergeMessage, "-X", "ours"],
							mergeDir,
						).pipe(
							Effect.mapError((e) => {
								// If merge still fails, report conflict or error
								if (e.stderr?.includes("CONFLICT") || e.message.includes("CONFLICT")) {
									return new MergeConflictError({
										issueId,
										branch: issueBranch,
										message: `Merge conflict. Resolve manually: git checkout ${baseBranch} && git merge ${issueBranch}`,
									})
								}
								return new GitError({
									message: `Merge failed: ${e.message}`,
									command: `git merge ${issueBranch} --no-ff`,
									stderr: e.stderr,
								})
							}),
						)

						// 7. Sync tracker AFTER merge to reconcile any bead changes from branch
						// This imports tracker from the branch that might have been excluded by -X ours
						yield* withSyncLock(
							Effect.gen(function* () {
								// Import tracker from the merged JSONL
								yield* issueTrackerClient
									.syncImportOnly(mergeDir)
									.pipe(
										Effect.catchAll((e) =>
											Effect.logWarning(`Failed to import tracker after merge: ${e}`),
										),
									)

								// Recover any tombstoned issues
								yield* issueTrackerClient
									.recoverTombstones(mergeDir)
									.pipe(
										Effect.catchAll((e) =>
											Effect.logWarning(`Failed to recover tombstoned tracker: ${e}`),
										),
									)

								// Full sync to commit any bead changes
								yield* issueTrackerClient
									.sync(mergeDir)
									.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to sync tracker: ${e}`)))
							}),
						)

						// 7.5. Run post-merge validation (configurable via project config)
						// Only runs if merge.validateCommands is configured
						const mergeConfig = yield* getMergeConfig()
						if (mergeConfig.validateCommands.length > 0) {
							yield* reportProgress("validate")
							yield* Effect.gen(function* () {
								const { validateCommands, fixCommand, maxFixAttempts, startAiSessionOnFailure } =
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
											const result = yield* runShellCommand(cmd, mergeDir)
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
										yield* runShellCommand(fixCommand, mergeDir)

										lastResult = yield* runValidation()
										if (lastResult.success) {
											yield* Effect.log(`Validation passed after fix attempt ${attempt}`)

											// Commit the fixes
											yield* runGit(["add", "-A"], mergeDir)
											const shouldCommitFix = yield* hasStagedChanges(mergeDir)
											if (shouldCommitFix) {
												yield* runGit(
													["commit", "-m", `fix: auto-fix after merging ${issueId}`],
													mergeDir,
												)
											}

											return
										}
									}
								}

								// Still failing after all fix attempts
								yield* Effect.log("Validation still failing after auto-fix attempts")

								// Commit any partial fixes
								yield* runGit(["add", "-A"], mergeDir)
								const shouldCommitPartialFix = yield* hasStagedChanges(mergeDir)
								if (shouldCommitPartialFix) {
									yield* runGit(
										["commit", "-m", `wip: partial fix after merging ${issueId}`],
										mergeDir,
									)
								}

								// Start an AI session if configured
								if (startAiSessionOnFailure) {
									const failedCmd = lastResult.failedCommand ?? validateCommands[0] ?? "validation"
									const fixPrompt = `Post-merge validation failed. Please fix the errors:\n\nFailed command: ${failedCmd}\n\n${lastResult.output}\n\nRun the validation commands after fixing to verify.`

									yield* sessionManager
										.start({
											issueId,
											projectPath,
											initialPrompt: fixPrompt,
										})
										.pipe(
											Effect.catchAll((e) =>
												Effect.logWarning(`Failed to start AI session for fixes: ${e}`),
											),
										)
								}

								return yield* Effect.fail(
									new TypeCheckError({
										issueId,
										message: `Post-merge validation failed. ${startAiSessionOnFailure ? "AI session started to fix. " : ""}Retry merge after fixing.`,
										output: lastResult.output,
									}),
								)
							})
						}

						// 8-10. Cleanup worktree and branch (only if not keeping worktree)
						if (!keepWorktree) {
							// 8. Merge Claude's local settings from worktree to main
							// This preserves permission grants (allowedTools, trustedPaths) that Claude
							// added during the session. Must happen BEFORE worktree deletion.
							yield* worktreeManager.mergeClaudeLocalSettings({
								worktreePath: worktree.path,
								mainProjectPath: projectPath,
							})

							// 9. Remove worktree directory
							yield* worktreeManager.remove({ issueId: issueId, projectPath })

							// 10. Delete local branch
							yield* runGit(["branch", "-d", issueBranch], projectPath).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)
						}

						// 11. Close bead issue (and children if epic merging to main)
						if (closeIssue) {
							yield* issueTrackerClient
								.update(issueId, { status: "closed" })
								.pipe(
									Effect.catchAll((e) =>
										Effect.logWarning(`Failed to close bead ${issueId}: ${e}`),
									),
								)

							// Clean up images for the closed bead (temporary debugging images)
							yield* imageAttachmentService
								.cleanupImagesForIssue(issueId)
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								)

							// If this is an epic being merged to main (not to another epic branch),
							// also close all its child tasks
							if (issue.issue_type === "epic" && !parentEpic) {
								const children = yield* issueTrackerClient
									.getEpicChildren(issueId)
									.pipe(
										Effect.catchAll((error) =>
											Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
												Effect.zipRight(Effect.succeed([])),
											),
										),
									)

								for (const child of children) {
									if (child.status !== "closed") {
										yield* issueTrackerClient.update(child.id, { status: "closed" }).pipe(
											Effect.tap(() =>
												Effect.log(`Closed child task ${child.id} as part of epic merge`),
											),
											Effect.catchAll((e) =>
												Effect.logWarning(`Failed to close child ${child.id}: ${e}`),
											),
										)
										// Clean up images for each closed child
										yield* imageAttachmentService
											.cleanupImagesForIssue(child.id)
											.pipe(
												Effect.catchAll((error) =>
													Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
														Effect.zipRight(Effect.void),
													),
												),
											)
									}
								}

								if (children.length > 0) {
									yield* Effect.log(`Closed ${children.length} child task(s) for epic ${issueId}`)
								}
							}

							yield* withSyncLock(
								issueTrackerClient
									.sync(mergeDir)
									.pipe(
										Effect.catchAll((e) => Effect.logWarning(`Failed to sync closed status: ${e}`)),
									),
							)
						}

						// 12. Push to origin (if enabled and online)
						if (pushToOrigin) {
							const pushStatus = yield* offlineService.isGitPushEnabled()
							if (pushStatus.enabled) {
								yield* reportProgress("push")
								const pushCommand = `git push origin ${baseBranch}`
								const runDeferredPush = Effect.gen(function* () {
									yield* reportDeferredPush({ _tag: "started", branch: baseBranch })

									const pushResult = yield* Effect.raceFirst(
										runGit(["push", "origin", baseBranch], mergeDir).pipe(
											Effect.match({
												onFailure: (error) => ({
													_tag: "failed" as const,
													branch: baseBranch,
													error: new GitError({
														message: `Push failed: ${error.message}. Your local merge succeeded - retry push manually.`,
														command: pushCommand,
														stderr: error.stderr,
													}),
												}),
												onSuccess: () => ({
													_tag: "succeeded" as const,
													branch: baseBranch,
												}),
											}),
										),
										Effect.sleep(MERGE_PUSH_TIMEOUT).pipe(
											Effect.as({ _tag: "timed-out" as const, branch: baseBranch }),
										),
									)

									if (pushResult._tag === "failed") {
										yield* Effect.logWarning(
											`Deferred push failed for ${issueId} -> ${baseBranch}: ${pushResult.error.message}`,
										)
										yield* reportDeferredPush(pushResult)
										return
									}

									if (pushResult._tag === "timed-out") {
										const timeoutError = new GitError({
											message: `Push timed out after ${MERGE_PUSH_TIMEOUT_SECONDS}s. Your local merge succeeded - retry push manually.`,
											command: pushCommand,
										})
										yield* Effect.logWarning(
											`Deferred push timed out for ${issueId} -> ${baseBranch}`,
										)
										yield* reportDeferredPush({
											_tag: "failed",
											branch: pushResult.branch,
											error: timeoutError,
										})
										return
									}

									yield* reportDeferredPush({
										_tag: "succeeded",
										branch: baseBranch,
									})
								}).pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(
											`Deferred push reporting failed for ${issueId} -> ${baseBranch}: ${String(error)}`,
										),
									),
								)

								yield* Effect.forkIn(runDeferredPush, serviceScope)
							}
							// Silently skip if offline/disabled - merge already succeeded locally
						}
					}).pipe(Effect.withSpan("pr.mergeToMain")),
				),

			checkMergeConflicts: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					const { baseBranch: effectiveBaseBranch } = yield* getIssueBaseBranch(
						issueId,
						projectPath,
					)
					const issueBranch = yield* getIssueBranchName(issueId, projectPath)

					// Fail fast if repository already has an in-progress git operation.
					// Conflict probing is not reliable while operation state is unresolved.
					yield* ensureNoGitOperationInProgress({
						cwd: projectPath,
						issueId,
						contextLabel: "project base branch",
					})

					// Use git merge-tree to perform an actual 3-way merge in memory
					// This detects real line-level conflicts, not just file overlap
					// Exit code 0 = clean merge, 1 = conflicts, other = error
					const mergeTreeCommand = Command.make(
						"git",
						"merge-tree",
						"--write-tree",
						effectiveBaseBranch,
						issueBranch,
					).pipe(Command.workingDirectory(projectPath))

					const exitCode = yield* Command.exitCode(mergeTreeCommand).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(2)),
							),
						), // Treat errors as unknown
					)

					// Exit code 1 means conflicts detected
					const hasConflictRisk = exitCode === 1

					// If conflicts exist, get the conflicting files from merge-tree output
					let conflictingFiles: readonly string[] = []
					if (hasConflictRisk) {
						// Run merge-tree again to get the conflicted file list
						// Use --no-messages to suppress "Auto-merging" messages and get clean file list
						const output = yield* runGit(
							[
								"merge-tree",
								"--write-tree",
								"--name-only",
								"--no-messages",
								effectiveBaseBranch,
								issueBranch,
							],
							projectPath,
						).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed("")),
								),
							),
						)

						// Parse output - conflicting files appear after the tree hash line
						const lines = output.trim().split("\n")
						// Skip first line (tree hash) and filter non-empty lines
						conflictingFiles = lines.slice(1).filter((f) => f.length > 0)
					}

					// Get file change counts for informational purposes
					const mergeBase = yield* runGit(
						["merge-base", effectiveBaseBranch, issueBranch],
						projectPath,
					).pipe(
						Effect.map((output) => output.trim()),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("")),
							),
						),
					)

					let branchChangedFiles = 0
					let baseChangedFiles = 0

					if (mergeBase) {
						const branchOutput = yield* runGit(
							["diff", "--name-only", `${mergeBase}..${issueBranch}`],
							projectPath,
						).pipe(
							Effect.map(
								(output) =>
									output
										.trim()
										.split("\n")
										.filter((f) => f.length > 0).length,
							),
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(0)),
								),
							),
						)
						branchChangedFiles = branchOutput

						const baseOutput = yield* runGit(
							["diff", "--name-only", `${mergeBase}..${effectiveBaseBranch}`],
							projectPath,
						).pipe(
							Effect.map(
								(output) =>
									output
										.trim()
										.split("\n")
										.filter((f) => f.length > 0).length,
							),
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(0)),
								),
							),
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

			abortMerge: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options

					// Get worktree info
					const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
					if (!worktree) {
						return yield* Effect.fail(
							new PRError({
								message: `No worktree found for ${issueId}`,
								issueId,
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

			checkUncommittedChanges: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options

					// Get worktree info
					const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
					if (!worktree) {
						return yield* Effect.fail(
							new PRError({
								message: `No worktree found for ${issueId}`,
								issueId,
							}),
						)
					}

					// Run git status --porcelain to get changed files
					// This is faster than git status and easier to parse
					// Format: XY filename (where X=index status, Y=worktree status)
					const output = yield* runGit(["status", "--porcelain"], worktree.path).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("")),
							),
						),
					)

					// Parse output - each non-empty line is a changed file
					const allChangedFiles = output
						.trim()
						.split("\n")
						.filter((line) => line.length > 0)
						.map((line) => line.slice(3)) // Remove "XY " prefix to get filename

					// Filter out .azedarach/ files - these are expected to change during normal
					// operation and are handled separately via `tracker sync`. We only want to
					// warn about actual code changes that could cause autostash conflicts.
					const changedFiles = allChangedFiles.filter((file) => !file.startsWith(".azedarach/"))

					return {
						hasUncommittedChanges: changedFiles.length > 0,
						changedFiles,
					}
				}),

			updateFromBase: (options: UpdateFromBaseOptions) =>
				diagnostics.measure(
					{
						source: "PRWorkflow",
						name: "updateFromBase",
						thresholdMs: 1000,
						details: `issueId=${options.issueId}`,
					},
					Effect.gen(function* () {
						const gitConfig = yield* getGitConfig()
						const { issueId, projectPath, baseBranch: explicitBaseBranch } = options

						// Determine effective base branch (epic branch for children, main for epics/standalone)
						const { baseBranch, parentEpic } = yield* getIssueBaseBranch(
							issueId,
							projectPath,
							explicitBaseBranch,
						)

						// Get worktree info
						const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
						if (!worktree) {
							return yield* Effect.fail(
								new PRError({
									message: `No worktree found for ${issueId}`,
									issueId,
								}),
							)
						}
						const issueBranch = worktree.branch

						// Fail fast if the effective base context is already in an active git operation.
						// Continuing from an unresolved base state can produce misleading merge outcomes.
						const baseOperationContext = yield* getBaseOperationContext(projectPath, parentEpic)
						yield* ensureNoGitOperationInProgress({
							cwd: baseOperationContext.cwd,
							issueId,
							contextLabel: baseOperationContext.contextLabel,
						})

						// === Step 1: Update local base branch to match origin ===
						if (gitConfig.fetchEnabled) {
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
						}

						// === Step 2: Check for conflicts using git merge-tree (in-memory, safe) ===
						const mergeTreeResult = yield* Effect.gen(function* () {
							const command = Command.make(
								"git",
								"merge-tree",
								"--write-tree",
								"--name-only",
								baseBranch,
								issueBranch,
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
								[
									"merge-tree",
									"--write-tree",
									"--name-only",
									"--no-messages",
									baseBranch,
									issueBranch,
								],
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
								// Filter OUT .azedarach/ files - we handle those separately
								.filter((f) => !f.startsWith(".azedarach/"))

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
								["merge", baseBranch, "-m", `Merge ${baseBranch} into ${issueBranch}`],
								worktree.path,
							).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							) // Will fail with conflicts, expected

							const resolvePrompt = `There are merge conflicts with ${baseBranch} in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution. After resolving, the branch will be up to date with ${baseBranch}.`

							const windowName = "merge"
							const sessionName = getIssueSessionName(issueId, projectPath)

							const cliTool = yield* appConfig.getCliTool()
							const sessionConfig = yield* appConfig.getSessionConfig()
							const modelConfig = yield* appConfig.getModelConfig()
							const toolDef = getToolDefinition(cliTool)
							const toolModelConfig = modelConfig[cliTool]
							const effectiveModel = toolModelConfig.default ?? modelConfig.default

							const command = toolDef.buildCommand({
								initialPrompt: resolvePrompt,
								issueId,
								model: effectiveModel,
								dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
							})

							yield* worktreeSession.ensureWindow(sessionName, windowName, {
								cwd: worktree.path,
								command,
							})

							const message = `Conflicts detected in: ${fileList}. Started AI session in '${windowName}' window to resolve. Retry update after resolution.`

							return yield* Effect.fail(
								new MergeConflictError({
									issueId,
									branch: issueBranch,
									message,
								}),
							)
						}

						// No conflicts - safe to merge local base branch (fast-forward if possible)
						yield* runGit(
							["merge", baseBranch, "-m", `Merge ${baseBranch} into ${issueBranch}`],
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

						// Sync tracker after merge to pick up any bead changes from main
						yield* withSyncLock(
							issueTrackerClient
								.sync(worktree.path)
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								),
						)
					}).pipe(Effect.withSpan("pr.updateFromBase")),
				),

			getPRComments: (options: GetPRCommentsOptions) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					const issueBranch = yield* getIssueBranchName(issueId, projectPath)

					// First check if a PR exists for this branch
					const prExists = yield* runGH(
						["pr", "view", issueBranch, "--json", "number"],
						projectPath,
					).pipe(
						Effect.map(() => true),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					)

					if (!prExists) {
						return [] as readonly PRComment[]
					}

					// Fetch PR comments (both issue comments and review comments)
					const commentsJson = yield* runGH(
						["pr", "view", issueBranch, "--json", "comments,reviews"],
						projectPath,
					).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("{}")),
							),
						),
					)

					const data = yield* Schema.decode(Schema.parseJson(GHPRCommentsResponseSchema))(
						commentsJson,
					).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(Effect.succeed({ comments: [], reviews: [] })),
							),
						),
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
			 * Check if a worktree branch is behind its base branch
			 *
			 * Uses git rev-list to count commits between HEAD and the base branch.
			 * For epic children, the base branch is the parent epic's branch.
			 * Returns { behind, ahead, baseBranch } so caller can show informative message.
			 */
			checkBranchBehindBase: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options

					// Get effective base branch (parent epic for children, main for others)
					const { baseBranch } = yield* getIssueBaseBranch(issueId, projectPath)

					// Get worktree info
					const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
					if (!worktree) {
						// No worktree = not behind (task has no session)
						return { behind: 0, ahead: 0, baseBranch }
					}

					// Count commits branch is behind base branch
					// HEAD..<baseBranch> = commits in base branch that are not in HEAD (how many we're behind)
					const behindOutput = yield* runGit(
						["rev-list", "--count", `HEAD..${baseBranch}`],
						worktree.path,
					).pipe(
						Effect.map((output) => Number.parseInt(output.trim(), 10)),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(0)),
							),
						),
					)

					// Count commits branch is ahead of base branch
					// <baseBranch>..HEAD = commits in HEAD that are not in base branch (how many we're ahead)
					const aheadOutput = yield* runGit(
						["rev-list", "--count", `${baseBranch}..HEAD`],
						worktree.path,
					).pipe(
						Effect.map((output) => Number.parseInt(output.trim(), 10)),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(0)),
							),
						),
					)

					return { behind: behindOutput, ahead: aheadOutput, baseBranch }
				}),

			/**
			 * Merge base branch into a worktree branch
			 *
			 * Auto-stashes uncommitted changes, merges base branch, pops stash.
			 * For epic children, merges the parent epic's branch instead of main.
			 * If conflicts, spawns an AI session to resolve them.
			 *
			 * @returns Effect that succeeds if merge was clean, fails with MergeConflictError if conflicts.
			 */
			mergeBaseIntoBranch: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					const gitConfig = yield* getGitConfig()

					// Get effective base branch (parent epic for children, main for others)
					const { baseBranch, parentEpic } = yield* getIssueBaseBranch(issueId, projectPath)

					// Get worktree info
					const worktree = yield* worktreeManager.get({ issueId: issueId, projectPath })
					if (!worktree) {
						return yield* Effect.fail(
							new PRError({
								message: `No worktree found for ${issueId}`,
								issueId,
							}),
						)
					}

					// Fail fast if the effective base context is already in an active git operation.
					// This keeps attach-time sync from mutating around unresolved base conflicts.
					const baseOperationContext = yield* getBaseOperationContext(projectPath, parentEpic)
					yield* ensureNoGitOperationInProgress({
						cwd: baseOperationContext.cwd,
						issueId,
						contextLabel: baseOperationContext.contextLabel,
					})

					if (gitConfig.fetchEnabled) {
						yield* runGit(["fetch", "origin", baseBranch], projectPath).pipe(
							Effect.catchAll((e) => Effect.logWarning(`Failed to fetch: ${e.message}`)),
						)
						yield* runGit(["fetch", "origin", `${baseBranch}:${baseBranch}`], projectPath).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to update local ${baseBranch}: ${e.message}`),
							),
						)
					}

					const statusOutput = yield* runGit(["status", "--porcelain"], worktree.path).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("")),
							),
						),
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
						const command = Command.make(
							"git",
							"merge-tree",
							"--write-tree",
							baseBranch,
							"HEAD",
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
							["merge-tree", "--write-tree", "--name-only", "--no-messages", baseBranch, "HEAD"],
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
							// Filter OUT .azedarach/ files - we handle those separately via tracker sync
							.filter((f) => !f.startsWith(".azedarach/"))

						return {
							hasConflicts: conflictingFiles.length > 0,
							conflictingFiles,
						}
					})

					// 3. If conflicts, start merge and spawn AI
					if (mergeTreeResult.hasConflicts) {
						const fileList = mergeTreeResult.conflictingFiles.join(", ")

						// Start merge in worktree so conflict markers are created
						yield* runGit(
							["merge", baseBranch, "-m", `Merge ${baseBranch} into ${issueId}`],
							worktree.path,
						).pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.void),
								),
							), // Will fail with conflicts, that's expected
						)

						const resolvePrompt = `There are merge conflicts in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution.`

						const windowName = "merge"
						const sessionName = getIssueSessionName(issueId, projectPath)

						const cliTool = yield* appConfig.getCliTool()
						const sessionConfig = yield* appConfig.getSessionConfig()
						const modelConfig = yield* appConfig.getModelConfig()
						const toolDef = getToolDefinition(cliTool)
						const toolModelConfig = modelConfig[cliTool]
						const effectiveModel = toolModelConfig.default ?? modelConfig.default

						const command = toolDef.buildCommand({
							initialPrompt: resolvePrompt,
							issueId,
							model: effectiveModel,
							dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
						})

						yield* worktreeSession.ensureWindow(sessionName, windowName, {
							cwd: worktree.path,
							command,
						})

						const message = `Merge conflicts detected in: ${fileList}. Started AI session in '${windowName}' window to resolve. Retry attach after resolution.`

						return yield* Effect.fail(
							new MergeConflictError({
								issueId,
								branch: issueId,
								message,
							}),
						)
					}

					// 4. No conflicts - do the merge
					yield* runGit(["merge", baseBranch, "--no-edit"], worktree.path).pipe(
						Effect.mapError(
							(e) =>
								new GitError({
									message: `Failed to merge ${baseBranch}: ${e.message}`,
									command: `git merge ${baseBranch}`,
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

					yield* Effect.log(`Successfully merged ${baseBranch} into ${issueId}`)
				}),

			getEffectiveBaseBranchForIssue: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					const gitConfig = yield* getGitConfig()
					const workflowMode = yield* appConfig.getWorkflowMode()

					// Check if this bead has a parent epic
					const parentEpic = yield* issueTrackerClient.getParentEpic(issueId)

					if (parentEpic) {
						// Child of epic: target the epic branch
						// In origin mode, we still use the epic branch directly (not origin/epic)
						// because the epic branch is a feature branch, not the base branch
						const baseBranch = yield* getIssueBranchName(parentEpic.id, projectPath)
						return { baseBranch, parentEpic }
					}

					// No parent epic: use the standard base branch
					const baseBranch =
						workflowMode === "origin" ? `origin/${gitConfig.baseBranch}` : gitConfig.baseBranch

					return { baseBranch, parentEpic: undefined }
				}),

			mergeIssueIntoIssue: (options: {
				sourceIssueId: string
				targetIssueId: string
				projectPath: string
			}) =>
				diagnostics.measure(
					{
						source: "PRWorkflow",
						name: "mergeIssueIntoIssue",
						thresholdMs: 1000,
						details: `source=${options.sourceIssueId} target=${options.targetIssueId}`,
					},
					Effect.gen(function* () {
						const { sourceIssueId, targetIssueId, projectPath } = options

						// Validate source and target are different
						if (sourceIssueId === targetIssueId) {
							return yield* Effect.fail(
								new PRError({
									message: "Cannot merge bead into itself",
									issueId: sourceIssueId,
								}),
							)
						}

						// Get source bead info
						const sourceIssue = yield* issueTrackerClient.show(sourceIssueId)

						// Validate target bead exists (will throw NotFoundError if not)
						const targetIssue = yield* issueTrackerClient.show(targetIssueId)
						const gitConfig = yield* getGitConfig()

						// Check if source has a worktree/branch
						const sourceWorktree = yield* worktreeManager.get({
							issueId: sourceIssueId,
							projectPath,
						})
						if (!sourceWorktree) {
							return yield* Effect.fail(
								new PRError({
									message: `No worktree found for source bead ${sourceIssueId}`,
									issueId: sourceIssueId,
								}),
							)
						}

						// Commit any uncommitted changes in source worktree
						yield* runGit(["add", "-A"], sourceWorktree.path)
						const shouldCommitSource = yield* hasStagedChanges(sourceWorktree.path)
						if (shouldCommitSource) {
							yield* runGit(
								["commit", "-m", `Work in progress: ${sourceIssueId}: ${sourceIssue.title}`],
								sourceWorktree.path,
							)
						}

						// Ensure target has a worktree (create if needed)
						let targetWorktree = yield* worktreeManager.get({
							issueId: targetIssueId,
							projectPath,
						})
						if (!targetWorktree) {
							yield* Effect.log(`Creating worktree for target bead ${targetIssueId}`)
							targetWorktree = yield* worktreeManager.create({
								issueId: targetIssueId,
								issueTitle: targetIssue.title,
								branchSlugMaxLength: gitConfig.branchSlugMaxLength,
								projectPath,
							})
						}
						if (!targetWorktree) {
							return yield* Effect.die(
								`Failed to resolve worktree for target bead ${targetIssueId}`,
							)
						}
						const sourceBranch = sourceWorktree.branch

						// Fetch source branch into target worktree
						// We use the project path since that's where the git repo is
						yield* runGit(["fetch", ".", sourceBranch], targetWorktree.path).pipe(
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to fetch source branch: ${e.message}`).pipe(
									Effect.map(() => undefined),
								),
							),
						)

						// Check for conflicts using git merge-tree (in-memory, safe)
						const mergeTreeResult = yield* Effect.gen(function* () {
							const command = Command.make(
								"git",
								"merge-tree",
								"--write-tree",
								"--name-only",
								"HEAD",
								sourceBranch,
							).pipe(Command.workingDirectory(targetWorktree.path))

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
								[
									"merge-tree",
									"--write-tree",
									"--name-only",
									"--no-messages",
									"HEAD",
									sourceBranch,
								],
								targetWorktree.path,
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
								// Filter OUT .azedarach/ files - we handle those separately
								.filter((f) => !f.startsWith(".azedarach/"))

							return {
								hasConflicts: conflictingFiles.length > 0,
								conflictingFiles,
							}
						})

						// If there are conflicts, ask AI to resolve
						if (mergeTreeResult.hasConflicts) {
							const fileList = mergeTreeResult.conflictingFiles.join(", ")

							// Start merge in target worktree so AI can resolve
							yield* runGit(
								["merge", sourceBranch, "-m", `Merge ${sourceIssueId} into ${targetIssueId}`],
								targetWorktree.path,
							).pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							) // Will fail with conflicts, expected

							const resolvePrompt = `There are merge conflicts when merging ${sourceIssueId} into ${targetIssueId} in: ${fileList}. Please resolve these conflicts, then stage and commit the resolution.`

							// Start AI session in a new "merge" window within the target's session
							const windowName = "merge"
							const sessionName = getIssueSessionName(targetIssueId, projectPath)

							const cliTool = yield* appConfig.getCliTool()
							const sessionConfig = yield* appConfig.getSessionConfig()
							const modelConfig = yield* appConfig.getModelConfig()
							const toolDef = getToolDefinition(cliTool)
							const toolModelConfig = modelConfig[cliTool]
							const effectiveModel = toolModelConfig.default ?? modelConfig.default

							const command = toolDef.buildCommand({
								initialPrompt: resolvePrompt,
								issueId: targetIssueId,
								model: effectiveModel,
								dangerouslySkipPermissions: sessionConfig.dangerouslySkipPermissions,
							})

							yield* worktreeSession.ensureWindow(sessionName, windowName, {
								cwd: targetWorktree.path,
								command,
							})

							const message = `Conflicts detected in: ${fileList}. Started AI session in '${windowName}' window of ${targetIssueId} to resolve. Retry merge after resolution.`

							return yield* Effect.fail(
								new MergeConflictError({
									issueId: sourceIssueId,
									branch: sourceBranch,
									message,
								}),
							)
						}

						// No conflicts - do the merge
						const mergeMessage = `Merge ${sourceIssueId}: ${sourceIssue.title}`
						yield* runGit(
							["merge", sourceBranch, "--no-ff", "-m", mergeMessage, "-X", "ours"],
							targetWorktree.path,
						).pipe(
							Effect.mapError((e) => {
								if (e.stderr?.includes("CONFLICT") || e.message.includes("CONFLICT")) {
									return new MergeConflictError({
										issueId: sourceIssueId,
										branch: sourceBranch,
										message: `Merge conflict. Resolve manually in ${targetIssueId} worktree`,
									})
								}
								return new GitError({
									message: `Merge failed: ${e.message}`,
									command: `git merge ${sourceBranch} --no-ff`,
									stderr: e.stderr,
								})
							}),
						)

						// Sync tracker after merge
						yield* withSyncLock(
							Effect.gen(function* () {
								yield* issueTrackerClient
									.syncImportOnly(targetWorktree.path)
									.pipe(
										Effect.catchAll((e) =>
											Effect.logWarning(`Failed to import tracker after merge: ${e}`),
										),
									)

								yield* issueTrackerClient
									.sync(targetWorktree.path)
									.pipe(Effect.catchAll((e) => Effect.logWarning(`Failed to sync tracker: ${e}`)))
							}),
						)

						// Close source bead
						yield* issueTrackerClient.update(sourceIssueId, { status: "closed" }).pipe(
							Effect.tap(() =>
								Effect.log(
									`Closed source bead ${sourceIssueId} after merging into ${targetIssueId}`,
								),
							),
							Effect.catchAll((e) =>
								Effect.logWarning(`Failed to close source bead ${sourceIssueId}: ${e}`),
							),
						)

						// Clean up images for the closed source bead
						yield* imageAttachmentService
							.cleanupImagesForIssue(sourceIssueId)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.void),
									),
								),
							)

						// Sync the closed status
						yield* withSyncLock(
							issueTrackerClient
								.sync(projectPath)
								.pipe(
									Effect.catchAll((error) =>
										Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
											Effect.zipRight(Effect.void),
										),
									),
								),
						)

						yield* Effect.log(
							`Successfully merged ${sourceIssueId} into ${targetIssueId}. Source bead closed.`,
						)
					}).pipe(Effect.withSpan("pr.mergeIssueIntoIssue")),
				),

			getTargetBranch: (issueId: string, projectPath: string) =>
				Effect.gen(function* () {
					const { baseBranch, parentEpic } = yield* getIssueBaseBranch(issueId, projectPath)
					return {
						targetBranch: baseBranch,
						isEpicChild: parentEpic !== undefined,
					}
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
