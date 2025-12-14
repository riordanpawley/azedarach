/**
 * WorktreeManager - Effect service for git worktree lifecycle management
 *
 * Manages isolated git worktrees for parallel Claude sessions. Each bead gets its own
 * worktree in a sibling directory following the convention: ../ProjectName-<bead-id>/
 *
 * Key features:
 * - Idempotent create operations (safe to call multiple times)
 * - acquireRelease for cleanup guarantees
 * - Tracks active worktrees in Ref for state management
 * - Handles epic vs task worktree sharing logic
 * - Parses git worktree list --porcelain output
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Context, Data, Effect, Layer, Ref, type Scope } from "effect"
import { getWorktreePath } from "./paths.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Worktree information from git
 */
export interface Worktree {
	readonly path: string
	readonly beadId: string
	readonly branch: string
	readonly isLocked: boolean
	readonly head: string
}

/**
 * Options for creating a worktree
 */
export interface CreateWorktreeOptions {
	readonly beadId: string
	readonly baseBranch?: string
	readonly projectPath: string
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Generic git command execution error
 */
export class GitError extends Data.TaggedError("GitError")<{
	readonly message: string
	readonly command: string
	readonly stderr?: string
}> {}

/**
 * Error when a worktree is not found
 */
export class WorktreeNotFoundError extends Data.TaggedError("WorktreeNotFoundError")<{
	readonly beadId: string
	readonly path: string
}> {}

/**
 * Error when worktree already exists (for non-idempotent operations)
 */
export class WorktreeExistsError extends Data.TaggedError("WorktreeExistsError")<{
	readonly beadId: string
	readonly path: string
}> {}

/**
 * Error when project is not a git repository
 */
export class NotAGitRepoError extends Data.TaggedError("NotAGitRepoError")<{
	readonly path: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * WorktreeManager service interface
 *
 * Provides typed access to git worktree operations with Effect error handling.
 * All operations require CommandExecutor in their context.
 */
export interface WorktreeManagerService {
	/**
	 * Create a new worktree for a bead
	 *
	 * Idempotent: if worktree already exists at expected path, returns existing worktree info.
	 * Creates a new branch named after the beadId from baseBranch (defaults to current branch).
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.create({
	 *   beadId: "az-05y",
	 *   baseBranch: "main",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly create: (
		options: CreateWorktreeOptions,
	) => Effect.Effect<Worktree, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>

	/**
	 * Remove a worktree by bead ID
	 *
	 * Cleans up the worktree directory and removes git metadata.
	 * Safe to call even if worktree doesn't exist (becomes a no-op).
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.remove({ beadId: "az-05y", projectPath: "/Users/user/project" })
	 * ```
	 */
	readonly remove: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<void, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>

	/**
	 * List all worktrees for the current repository
	 *
	 * Parses git worktree list --porcelain output to structured data.
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.list("/Users/user/project")
	 * ```
	 */
	readonly list: (
		projectPath: string,
	) => Effect.Effect<Worktree[], GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>

	/**
	 * Check if a worktree exists for a bead
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.exists({ beadId: "az-05y", projectPath: "/Users/user/project" })
	 * ```
	 */
	readonly exists: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<boolean, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>

	/**
	 * Get worktree info for a specific bead
	 *
	 * Returns None if worktree doesn't exist.
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.get({ beadId: "az-05y", projectPath: "/Users/user/project" })
	 * ```
	 */
	readonly get: (options: {
		beadId: string
		projectPath: string
	}) => Effect.Effect<Worktree | null, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>
}


// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Execute a git command and return stdout as string
 */
const runGit = (
	args: readonly string[],
	cwd: string,
): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", ...args).pipe(Command.workingDirectory(cwd))

		const result = yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new GitError({
					message: `git command failed: ${stderr}`,
					command: `git ${args.join(" ")}`,
					stderr,
				})
			}),
		)

		return result
	})

/**
 * Check if a path is a git repository
 */
const isGitRepo = (
	projectPath: string,
): Effect.Effect<boolean, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", "rev-parse", "--git-dir").pipe(
			Command.workingDirectory(projectPath),
		)

		return yield* Command.exitCode(command).pipe(
			Effect.map((code) => code === 0),
			Effect.catchAll(() => Effect.succeed(false)),
		)
	})

/**
 * Get current branch name
 */
const getCurrentBranch = (
	projectPath: string,
): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const output = yield* runGit(["rev-parse", "--abbrev-ref", "HEAD"], projectPath)
		return output.trim()
	})

/**
 * Parse git worktree list --porcelain output
 *
 * Format:
 * worktree /path/to/worktree
 * HEAD <sha>
 * branch refs/heads/branch-name
 * [locked <reason>]
 * [prunable <reason>]
 *
 * Entries are separated by blank lines.
 */
const parseWorktreeList = (output: string, projectPath: string): Worktree[] => {
	if (!output.trim()) {
		return []
	}

	const entries = output.split("\n\n").filter((entry) => entry.trim())
	const worktrees: Worktree[] = []

	for (const entry of entries) {
		const lines = entry.split("\n")
		let path = ""
		let head = ""
		let branch = ""
		let isLocked = false

		for (const line of lines) {
			if (line.startsWith("worktree ")) {
				path = line.slice("worktree ".length)
			} else if (line.startsWith("HEAD ")) {
				head = line.slice("HEAD ".length)
			} else if (line.startsWith("branch ")) {
				const branchRef = line.slice("branch ".length)
				branch = branchRef.replace("refs/heads/", "")
			} else if (line.startsWith("locked")) {
				isLocked = true
			}
		}

		// Extract beadId from path
		// Path format: /parent/dir/ProjectName-beadId
		const pathParts = path.split("/")
		const lastPart = pathParts[pathParts.length - 1]
		const match = lastPart?.match(/-(az-[a-z0-9]+)$/)
		const beadId = match?.[1] || ""

		// Only include worktrees that match our naming convention and aren't the main worktree
		if (beadId && path !== projectPath) {
			worktrees.push({
				path,
				beadId,
				branch,
				isLocked,
				head,
			})
		}
	}

	return worktrees
}

// ============================================================================
// Service Implementation
// ============================================================================

/**
 * WorktreeManager service
 *
 * Creates a service implementation with stateful tracking via Ref.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const manager = yield* WorktreeManager
 *   const worktree = yield* manager.create({
 *     beadId: "az-05y",
 *     baseBranch: "main",
 *     projectPath: "/Users/user/project"
 *   })
 *   return worktree
 * }).pipe(Effect.provide(WorktreeManager.Default))
 * ```
 */
export class WorktreeManager extends Effect.Service<WorktreeManager>()("WorktreeManager", {
	dependencies: [BunContext.layer],
	effect: Effect.gen(function* () {
		// Track active worktrees in memory for fast lookups
		const worktreesRef = yield* Ref.make<Map<string, Worktree>>(new Map())

		// Helper to refresh worktrees cache
		const refreshWorktrees = (
			projectPath: string,
		): Effect.Effect<void, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const isRepo = yield* isGitRepo(projectPath)
				if (!isRepo) {
					return yield* Effect.fail(new NotAGitRepoError({ path: projectPath }))
				}

				const output = yield* runGit(["worktree", "list", "--porcelain"], projectPath)
				const worktrees = parseWorktreeList(output, projectPath)

				const newMap = new Map<string, Worktree>()
				for (const wt of worktrees) {
					newMap.set(wt.beadId, wt)
				}

				yield* Ref.set(worktreesRef, newMap)
			})

		return {
		create: (options: CreateWorktreeOptions) =>
			Effect.gen(function* () {
				const { beadId, baseBranch, projectPath } = options

				// Check if git repo
				const isRepo = yield* isGitRepo(projectPath)
				if (!isRepo) {
					return yield* Effect.fail(new NotAGitRepoError({ path: projectPath }))
				}

				// Get expected worktree path
				const worktreePath = getWorktreePath(projectPath, beadId)

				// Refresh cache and check if already exists
				yield* refreshWorktrees(projectPath)
				const existing = yield* Ref.get(worktreesRef)
				const existingWorktree = existing.get(beadId)

				if (existingWorktree) {
					// Idempotent: worktree already exists
					return existingWorktree
				}

				// Determine base branch
				const base = baseBranch || (yield* getCurrentBranch(projectPath))

				// Create new branch and worktree
				// git worktree add -b <branch-name> <path> <start-point>
				yield* runGit(["worktree", "add", "-b", beadId, worktreePath, base], projectPath)

				// Refresh cache to get the new worktree info
				yield* refreshWorktrees(projectPath)
				const updated = yield* Ref.get(worktreesRef)
				const newWorktree = updated.get(beadId)

				if (!newWorktree) {
					// This shouldn't happen, but handle it gracefully
					return yield* Effect.fail(
						new GitError({
							message: "Worktree created but not found in list",
							command: `git worktree add -b ${beadId} ${worktreePath} ${base}`,
						}),
					)
				}

				return newWorktree
			}),

		remove: (options: { beadId: string; projectPath: string }) =>
			Effect.gen(function* () {
				const { beadId, projectPath } = options

				// Refresh cache
				yield* refreshWorktrees(projectPath)
				const worktrees = yield* Ref.get(worktreesRef)
				const worktree = worktrees.get(beadId)

				if (!worktree) {
					// Safe no-op if doesn't exist
					return
				}

				// Remove worktree
				yield* runGit(["worktree", "remove", worktree.path, "--force"], projectPath)

				// Refresh cache
				yield* refreshWorktrees(projectPath)
			}),

		list: (projectPath: string) =>
			Effect.gen(function* () {
				yield* refreshWorktrees(projectPath)
				const worktrees = yield* Ref.get(worktreesRef)
				return Array.from(worktrees.values())
			}),

		exists: (options: { beadId: string; projectPath: string }) =>
			Effect.gen(function* () {
				const { beadId, projectPath } = options
				yield* refreshWorktrees(projectPath)
				const worktrees = yield* Ref.get(worktreesRef)
				return worktrees.has(beadId)
			}),

		get: (options: { beadId: string; projectPath: string }) =>
			Effect.gen(function* () {
				const { beadId, projectPath } = options
				yield* refreshWorktrees(projectPath)
				const worktrees = yield* Ref.get(worktreesRef)
				return worktrees.get(beadId) || null
			}),
		}
	}),
}) {}

/**
 * Complete WorktreeManager layer with all platform dependencies (legacy alias)
 *
 * @deprecated Use WorktreeManager.Default instead
 */
export const WorktreeManagerLiveWithPlatform = WorktreeManager.Default

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Create a worktree for a bead
 */
export const create = (
	options: CreateWorktreeOptions,
): Effect.Effect<
	Worktree,
	GitError | NotAGitRepoError,
	WorktreeManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(WorktreeManager, (manager) => manager.create(options))

/**
 * Remove a worktree by bead ID
 */
export const remove = (options: {
	beadId: string
	projectPath: string
}): Effect.Effect<
	void,
	GitError | NotAGitRepoError,
	WorktreeManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(WorktreeManager, (manager) => manager.remove(options))

/**
 * List all worktrees
 */
export const list = (
	projectPath: string,
): Effect.Effect<
	Worktree[],
	GitError | NotAGitRepoError,
	WorktreeManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(WorktreeManager, (manager) => manager.list(projectPath))

/**
 * Check if a worktree exists
 */
export const exists = (options: {
	beadId: string
	projectPath: string
}): Effect.Effect<
	boolean,
	GitError | NotAGitRepoError,
	WorktreeManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(WorktreeManager, (manager) => manager.exists(options))

/**
 * Get worktree info for a bead
 */
export const get = (options: {
	beadId: string
	projectPath: string
}): Effect.Effect<
	Worktree | null,
	GitError | NotAGitRepoError,
	WorktreeManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(WorktreeManager, (manager) => manager.get(options))

/**
 * Create a worktree with acquireRelease for cleanup guarantees
 *
 * Automatically removes the worktree when the scope is closed.
 *
 * @example
 * ```ts
 * Effect.gen(function* () {
 *   const worktree = yield* acquireWorktree({
 *     beadId: "az-05y",
 *     baseBranch: "main",
 *     projectPath: "/Users/user/project"
 *   })
 *
 *   // Do work with worktree...
 *   // Worktree automatically removed when scope closes
 * }).pipe(Effect.scoped)
 * ```
 */
export const acquireWorktree = (
	options: CreateWorktreeOptions,
): Effect.Effect<
	Worktree,
	GitError | NotAGitRepoError,
	Scope.Scope | WorktreeManager | CommandExecutor.CommandExecutor
> =>
	Effect.acquireRelease(create(options), (worktree) =>
		remove({ beadId: worktree.beadId, projectPath: options.projectPath }).pipe(
			Effect.orElseSucceed(() => undefined),
		),
	)
