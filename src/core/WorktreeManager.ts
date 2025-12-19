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

import { Command, type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, Ref, Schedule, Schema, type Scope } from "effect"
import {
	deepMerge,
	deepMergeWithDedup,
	extractMergeableSettings,
	generateHookConfig,
} from "./hooks.js"

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

	/**
	 * Merge Claude's local settings from worktree back to main
	 *
	 * When a worktree is merged to main, this preserves permission grants
	 * (allowedTools, trustedPaths, etc.) that Claude added during the session.
	 * Excludes hook configurations which are bead-specific.
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.mergeClaudeLocalSettings({
	 *   worktreePath: "/Users/user/project-az-05y",
	 *   mainProjectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly mergeClaudeLocalSettings: (options: {
		worktreePath: string
		mainProjectPath: string
	}) => Effect.Effect<void, never, never>
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
	dependencies: [],
	effect: Effect.gen(function* () {
		// Grab platform services at layer construction
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		// Track active worktrees in memory for fast lookups
		const worktreesRef = yield* Ref.make<Map<string, Worktree>>(new Map())

		/**
		 * Copy Claude's local settings to a new worktree and inject hook configuration
		 *
		 * Claude Code stores personal permission grants in .claude/settings.local.json,
		 * which is globally gitignored and thus not copied when git creates a worktree.
		 * This function copies that file and merges in hook configuration for session
		 * state detection.
		 */
		const copyClaudeLocalSettings = (
			sourceProjectPath: string,
			targetWorktreePath: string,
			beadId: string,
		): Effect.Effect<void, never, never> =>
			Effect.gen(function* () {
				const sourceSettings = pathService.join(sourceProjectPath, ".claude", "settings.local.json")
				const targetClaudeDir = pathService.join(targetWorktreePath, ".claude")
				const targetSettings = pathService.join(targetClaudeDir, "settings.local.json")

				// Ensure target .claude directory exists (it should from git, but be safe)
				const targetDirExists = yield* fs.exists(targetClaudeDir)
				if (!targetDirExists) {
					yield* fs.makeDirectory(targetClaudeDir, { recursive: true })
				}

				// Read existing settings if they exist
				let existingSettings: Record<string, unknown> = {}
				const sourceExists = yield* fs.exists(sourceSettings)
				if (sourceExists) {
					const content = yield* fs
						.readFileString(sourceSettings)
						.pipe(Effect.catchAll(() => Effect.succeed("{}")))
					existingSettings = yield* Effect.try({
						try: () => JSON.parse(content) as Record<string, unknown>,
						catch: () => ({}) as Record<string, unknown>,
					}).pipe(Effect.catchAll(() => Effect.succeed({} as Record<string, unknown>)))
				}

				// Generate hook configuration for this bead
				const hookConfig = generateHookConfig(beadId)

				// Merge existing settings with hook configuration
				const mergedSettings = deepMerge(existingSettings, hookConfig)

				// Write merged settings to target
				yield* fs.writeFileString(targetSettings, JSON.stringify(mergedSettings, null, "\t"))
			}).pipe(
				// Don't fail worktree creation if settings copy fails - just log and continue
				Effect.catchAll((error) =>
					Effect.logWarning(`Failed to copy Claude local settings: ${error}`),
				),
			)

		/**
		 * Schema for parsing settings.local.json
		 *
		 * Uses Schema.parseJson to safely parse JSON string into a record.
		 * Falls back to empty object on parse failure.
		 */
		const SettingsJsonSchema = Schema.parseJson(
			Schema.Record({ key: Schema.String, value: Schema.Unknown }),
		)
		const decodeSettings = Schema.decodeUnknown(SettingsJsonSchema)

		/**
		 * Merge Claude's local settings from worktree back to main
		 *
		 * Preserves permission grants (allowedTools, trustedPaths) that Claude
		 * added during the session. Excludes hook configurations which are bead-specific.
		 */
		const mergeClaudeLocalSettings = (options: {
			worktreePath: string
			mainProjectPath: string
		}): Effect.Effect<void, never, never> =>
			Effect.gen(function* () {
				const { worktreePath, mainProjectPath } = options
				const worktreeSettings = pathService.join(worktreePath, ".claude", "settings.local.json")
				const mainSettings = pathService.join(mainProjectPath, ".claude", "settings.local.json")

				// Read worktree settings
				const worktreeExists = yield* fs.exists(worktreeSettings)
				if (!worktreeExists) {
					yield* Effect.logDebug("No worktree settings.local.json to merge")
					return
				}

				const worktreeContent = yield* fs
					.readFileString(worktreeSettings)
					.pipe(Effect.catchAll(() => Effect.succeed("{}")))

				// Parse with Schema - fallback to empty object on failure
				const worktreeData = yield* decodeSettings(worktreeContent).pipe(
					Effect.catchAll(() => Effect.succeed({})),
				)

				// Extract only permission-related settings (exclude hooks)
				const mergeableSettings = extractMergeableSettings(worktreeData)

				// If nothing to merge, skip
				if (Object.keys(mergeableSettings).length === 0) {
					yield* Effect.logDebug("No permission settings to merge from worktree")
					return
				}

				// Read main settings
				const mainExists = yield* fs.exists(mainSettings)
				let mainData: Record<string, unknown> = {}

				if (mainExists) {
					const mainContent = yield* fs
						.readFileString(mainSettings)
						.pipe(Effect.catchAll(() => Effect.succeed("{}")))

					mainData = yield* decodeSettings(mainContent).pipe(
						Effect.catchAll(() => Effect.succeed({})),
					)
				}

				// Merge with deduplication
				const mergedData = deepMergeWithDedup(mainData, mergeableSettings)

				// Ensure .claude directory exists
				const mainClaudeDir = pathService.join(mainProjectPath, ".claude")
				const mainDirExists = yield* fs.exists(mainClaudeDir)
				if (!mainDirExists) {
					yield* fs.makeDirectory(mainClaudeDir, { recursive: true })
				}

				// Write merged settings
				yield* fs.writeFileString(mainSettings, JSON.stringify(mergedData, null, "\t"))
				yield* Effect.log("Merged permission settings from worktree to main")
			}).pipe(
				// Don't fail the merge if settings merge fails - just log warning
				Effect.catchAll((error) =>
					Effect.logWarning(`Failed to merge Claude local settings: ${error}`),
				),
			)

		// Pure helper to parse worktree list output (uses captured pathService)
		const parseWorktreeList = (output: string, projectPath: string): Worktree[] => {
			if (!output.trim()) return []

			const entries = output.split("\n\n").filter((entry) => entry.trim())
			const worktrees: Worktree[] = []
			const normalizedProjectPath = pathService.resolve(projectPath)

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
						branch = line.slice("branch ".length).replace("refs/heads/", "")
					} else if (line.startsWith("locked")) {
						isLocked = true
					}
				}

				let beadId = branch
				if (!beadId) {
					const pathParts = path.split("/")
					const lastPart = pathParts[pathParts.length - 1]
					const match = lastPart?.match(/-([a-z]+-[a-z0-9]+)$/i)
					beadId = match?.[1] || ""
				}

				const normalizedPath = pathService.resolve(path)
				if (beadId && normalizedPath !== normalizedProjectPath) {
					worktrees.push({ path, beadId, branch, isLocked, head })
				}
			}
			return worktrees
		}

		// Pure helper to get worktree path (uses captured pathService)
		const getWorktreePath = (projectPath: string, beadId: string): string => {
			const projectName = pathService.basename(projectPath)
			const parentDir = pathService.dirname(projectPath)
			return pathService.join(parentDir, `${projectName}-${beadId}`)
		}

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

					// Copy Claude's local settings and inject hook configuration
					yield* copyClaudeLocalSettings(projectPath, worktreePath, beadId)

					// Refresh cache and look for the new worktree with retry logic.
					// Git worktree list can sometimes miss newly created worktrees due to
					// filesystem sync timing issues, especially on macOS APFS. We retry
					// a few times with short delays to handle this race condition.
					const findNewWorktree = Effect.gen(function* () {
						yield* refreshWorktrees(projectPath)
						const updated = yield* Ref.get(worktreesRef)
						const newWorktree = updated.get(beadId)

						if (!newWorktree) {
							const foundBeadIds = Array.from(updated.keys())
							return yield* Effect.fail({
								_tag: "NotFound" as const,
								foundBeadIds,
								cacheSize: updated.size,
							})
						}
						return newWorktree
					})

					// Retry up to 5 times with 100ms delay between attempts (500ms total max wait)
					const retrySchedule = Schedule.recurs(4).pipe(Schedule.addDelay(() => "100 millis"))

					const result = yield* findNewWorktree.pipe(
						Effect.retry({
							schedule: retrySchedule,
							while: (e) => e._tag === "NotFound",
						}),
						Effect.catchIf(
							(e): e is { _tag: "NotFound"; foundBeadIds: string[]; cacheSize: number } =>
								"_tag" in e && e._tag === "NotFound",
							(e) => {
								// All retries exhausted, log and fail with descriptive error
								Effect.logError("Worktree created but not found in cache after retries", {
									beadId,
									worktreePath,
									projectPath,
									foundBeadIds: e.foundBeadIds,
									cacheSize: e.cacheSize,
								})
								return Effect.fail(
									new GitError({
										message: `Worktree created but not found in list after retries. Looking for: ${beadId}, found: [${e.foundBeadIds.join(", ")}]`,
										command: `git worktree add -b ${beadId} ${worktreePath} ${base}`,
									}),
								)
							},
						),
					)

					return result
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

			mergeClaudeLocalSettings,
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
