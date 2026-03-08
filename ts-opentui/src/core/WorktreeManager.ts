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
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import {
	createStaleLockRecoveryHint,
	extractGitRecoveryHint,
	type GitRecoveryHint,
} from "./gitRecovery.js"
import {
	deepMerge,
	deepMergeWithDedup,
	extractMergeableSettings,
	generateHookConfig,
	generateWorktreeSkill,
	type HookConfigOptions,
} from "./hooks.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Worktree information from git
 */
export interface Worktree {
	readonly path: string
	readonly issueId: string
	readonly branch: string
	readonly isLocked: boolean
	readonly head: string
}

/**
 * Options for creating a worktree
 */
export interface CreateWorktreeOptions {
	readonly issueId: string
	/**
	 * Human-friendly issue title used for branch name generation.
	 *
	 * When provided, branch names are derived from this title instead of the
	 * internal issue ID. The first generated branch for an issue is persisted
	 * and reused for future operations.
	 */
	readonly issueTitle?: string
	/**
	 * Maximum length for the title-derived slug portion of generated branch names.
	 *
	 * Applies only to the `<slug>` segment in `<author>/<issue-id>/<slug>`.
	 * Existing branch mappings are not rewritten.
	 */
	readonly branchSlugMaxLength?: number
	readonly baseBranch?: string
	readonly projectPath: string
	/**
	 * Source worktree to copy untracked files from.
	 *
	 * When creating a child task of an epic, this should be the epic's worktree path.
	 * If not provided, falls back to projectPath.
	 */
	readonly sourceWorktreePath?: string
	/**
	 * Paths to copy from source worktree to new worktree.
	 *
	 * Each path is relative to the worktree root. Both files and directories are supported.
	 * Missing paths are silently skipped.
	 *
	 * @example ["node_modules", ".env.local", ".direnv"]
	 */
	readonly copyPaths?: readonly string[]
	/**
	 * Whether to enable the PreCompact hook for context preservation.
	 *
	 * When true (default), injects a hook that reminds Claude to update tracker
	 * before context compaction. This ensures work-in-progress is preserved.
	 *
	 * @default true
	 */
	readonly preCompactEnabled?: boolean
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
	readonly recovery?: GitRecoveryHint
}> {}

/**
 * Error when a worktree is not found
 */
export class WorktreeNotFoundError extends Data.TaggedError("WorktreeNotFoundError")<{
	readonly issueId: string
	readonly path: string
}> {}

/**
 * Error when worktree already exists (for non-idempotent operations)
 */
export class WorktreeExistsError extends Data.TaggedError("WorktreeExistsError")<{
	readonly issueId: string
	readonly path: string
}> {}

/**
 * Error when issue-derived worktree or branch names collide with another worktree
 */
export class WorktreeNameClashError extends Data.TaggedError("WorktreeNameClashError")<{
	readonly issueId: string
	readonly conflictKind: "path" | "branch"
	readonly requestedWorktreePath: string
	readonly requestedBranch?: string
	readonly conflictingIssueId: string
	readonly conflictingWorktreePath: string
	readonly conflictingBranch: string
	readonly baseBranch: string
	readonly commitsAheadOfBase?: number
	readonly uncommittedFileCount: number
}> {}

class WorktreeCacheMissAfterCreateError extends Data.TaggedError(
	"WorktreeCacheMissAfterCreateError",
)<{
	readonly foundIssueIds: readonly string[]
	readonly cacheSize: number
}> {}

/**
 * Error when project is not a git repository
 */
export class NotAGitRepoError extends Data.TaggedError("NotAGitRepoError")<{
	readonly path: string
}> {}

export type WorktreeCreateError = GitError | NotAGitRepoError | WorktreeNameClashError

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
	 * Creates/reuses a stable mapped branch name derived from issue title.
	 * Falls back to issueId-derived mapping when title is unavailable.
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.create({
	 *   issueId: "az-05y",
	 *   baseBranch: "main",
	 *   projectPath: "/Users/user/project"
	 * })
	 * ```
	 */
	readonly create: (
		options: CreateWorktreeOptions,
	) => Effect.Effect<Worktree, WorktreeCreateError, CommandExecutor.CommandExecutor>

	/**
	 * Remove a worktree by bead ID
	 *
	 * Cleans up the worktree directory and removes git metadata.
	 * Safe to call even if worktree doesn't exist (becomes a no-op).
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.remove({ issueId: "az-05y", projectPath: "/Users/user/project" })
	 * ```
	 */
	readonly remove: (options: {
		issueId: string
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
	 * WorktreeManager.exists({ issueId: "az-05y", projectPath: "/Users/user/project" })
	 * ```
	 */
	readonly exists: (options: {
		issueId: string
		projectPath: string
	}) => Effect.Effect<boolean, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor>

	/**
	 * Get worktree info for a specific bead
	 *
	 * Returns None if worktree doesn't exist.
	 *
	 * @example
	 * ```ts
	 * WorktreeManager.get({ issueId: "az-05y", projectPath: "/Users/user/project" })
	 * ```
	 */
	readonly get: (options: {
		issueId: string
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

		const result = yield* Command.string(command).pipe(
			Effect.mapError((error) => {
				const stderr = "stderr" in error ? String(error.stderr) : String(error)
				return new GitError({
					message: `git command failed: ${stderr}`,
					command: `git ${args.join(" ")}`,
					stderr,
					recovery: extractGitRecoveryHint(stderr),
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
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(false)),
				),
			),
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
 * Check if a branch exists (locally)
 */
const branchExists = (
	branchName: string,
	projectPath: string,
): Effect.Effect<boolean, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const command = Command.make("git", "rev-parse", "--verify", `refs/heads/${branchName}`).pipe(
			Command.workingDirectory(projectPath),
		)

		return yield* Command.exitCode(command).pipe(
			Effect.map((code) => code === 0),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(false)),
				),
			),
		)
	})

/**
 * Check if a branch exists locally or on origin.
 */
const branchExistsAnywhere = (
	branchName: string,
	projectPath: string,
): Effect.Effect<boolean, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const localExists = yield* branchExists(branchName, projectPath)
		if (localExists) {
			return true
		}

		const command = Command.make(
			"git",
			"rev-parse",
			"--verify",
			`refs/remotes/origin/${branchName}`,
		).pipe(Command.workingDirectory(projectPath))

		return yield* Command.exitCode(command).pipe(
			Effect.map((code) => code === 0),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(false)),
				),
			),
		)
	})

const DEFAULT_BRANCH_SLUG_MAX_LENGTH = 24
const MIN_BRANCH_SLUG_MAX_LENGTH = 4

export const normalizeBranchSlugMaxLength = (value?: number): number => {
	if (typeof value !== "number" || !Number.isFinite(value)) {
		return DEFAULT_BRANCH_SLUG_MAX_LENGTH
	}

	const normalized = Math.floor(value)
	return normalized >= MIN_BRANCH_SLUG_MAX_LENGTH ? normalized : DEFAULT_BRANCH_SLUG_MAX_LENGTH
}

export const slugifyIssueTitleForBranch = (title: string, maxLength: number): string => {
	const base = title
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")

	if (base.length === 0) {
		return "task"
	}

	return base.slice(0, maxLength).replace(/-+$/g, "") || "task"
}

const sanitizeBranchAuthor = (value: string): string =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "")
		.trim()

export const sanitizeIssueIdForBranchSegment = (value: string): string => {
	const normalized = value
		.toLowerCase()
		.trim()
		.replace(/[^a-z0-9._-]+/g, "-")
		.replace(/^-+|-+$/g, "")

	return normalized.length > 0 ? normalized : "issue"
}

export const composeIssueBranchName = (author: string, issueId: string, slug: string): string =>
	`${author}/${sanitizeIssueIdForBranchSegment(issueId)}/${slug}`

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
 *     issueId: "az-05y",
 *     baseBranch: "main",
 *     projectPath: "/Users/user/project"
 *   })
 *   return worktree
 * }).pipe(Effect.provide(WorktreeManager.Default))
 * ```
 */
export class WorktreeManager extends Effect.Service<WorktreeManager>()("WorktreeManager", {
	dependencies: [DiagnosticsService.Default],
	effect: Effect.gen(function* () {
		// Grab platform services at layer construction
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const diagnostics = yield* DiagnosticsService

		// Track active worktrees in memory for fast lookups
		// Now supports multiple projects: projectPath -> (issueId -> Worktree)
		const worktreesRef = yield* Ref.make<Map<string, Map<string, Worktree>>>(new Map())

		// TTL cache for worktree refresh - avoid repeated git worktree list calls
		// Supports multiple projects for fast project switching
		const WORKTREE_CACHE_TTL_MS = 2000
		const BRANCH_NAME_MAP_RELATIVE_PATH = ".azedarach/branch-name-map.json"
		// Map from projectPath to timestamp
		const cacheTimestampRef = yield* Ref.make<Map<string, number>>(new Map())

		const getBranchAuthor = (
			projectPath: string,
		): Effect.Effect<string, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const configuredName = yield* runGit(["config", "user.name"], projectPath).pipe(
					Effect.map((value) => value.trim()),
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed("")),
						),
					),
				)
				const configuredAuthor = sanitizeBranchAuthor(configuredName)
				if (configuredAuthor.length > 0) {
					return configuredAuthor
				}

				const envAuthor = sanitizeBranchAuthor(process.env.USER ?? "")
				if (envAuthor.length > 0) {
					return envAuthor
				}

				return "author"
			})

		const getBranchNameMapPath = (projectPath: string): string =>
			pathService.join(projectPath, BRANCH_NAME_MAP_RELATIVE_PATH)

		const readBranchNameMap = (
			projectPath: string,
		): Effect.Effect<Record<string, string>, never, never> =>
			Effect.gen(function* () {
				const mapPath = getBranchNameMapPath(projectPath)
				const exists = yield* fs.exists(mapPath)
				if (!exists) {
					return {}
				}

				const raw = yield* fs
					.readFileString(mapPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("{}")),
							),
						),
					)
				const parsed = yield* Effect.try({
					try: () => JSON.parse(raw) as unknown,
					catch: () => ({}),
				})

				if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
					return {}
				}

				const entries = Object.entries(parsed)
				const map: Record<string, string> = {}
				for (const [key, value] of entries) {
					if (typeof value === "string" && value.length > 0) {
						map[key] = value
					}
				}

				return map
			}).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed({})),
					),
				),
			)

		const writeBranchNameMap = (
			projectPath: string,
			map: Record<string, string>,
		): Effect.Effect<void, never, never> =>
			Effect.gen(function* () {
				const mapPath = getBranchNameMapPath(projectPath)
				const mapDir = pathService.dirname(mapPath)
				yield* fs.makeDirectory(mapDir, { recursive: true }).pipe(Effect.ignore)
				yield* fs
					.writeFileString(mapPath, JSON.stringify(map, null, "\t"))
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.void),
							),
						),
					)
			})

		const getOrCreateBranchName = (options: {
			issueId: string
			issueTitle?: string
			branchSlugMaxLength?: number
			projectPath: string
		}): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const { issueId, issueTitle, branchSlugMaxLength, projectPath } = options
				const branchMap = yield* readBranchNameMap(projectPath)

				const existing = branchMap[issueId]
				if (existing) {
					return existing
				}

				const slugMaxLength = normalizeBranchSlugMaxLength(branchSlugMaxLength)
				const author = yield* getBranchAuthor(projectPath)
				const baseSlug = slugifyIssueTitleForBranch(issueTitle ?? issueId, slugMaxLength)
				const reserved = new Set(Object.values(branchMap))

				for (let attempt = 0; attempt < 1000; attempt++) {
					const suffix = attempt === 0 ? "" : `-${attempt + 1}`
					const maxBaseLength = Math.max(1, slugMaxLength - suffix.length)
					const trimmedBase = baseSlug.slice(0, maxBaseLength).replace(/-+$/g, "") || "task"
					const slugWithSuffix = `${trimmedBase}${suffix}`
					const candidate = composeIssueBranchName(author, issueId, slugWithSuffix)

					if (reserved.has(candidate)) {
						continue
					}

					const existsInGit = yield* branchExistsAnywhere(candidate, projectPath)
					if (existsInGit) {
						continue
					}

					const nextMap = { ...branchMap, [issueId]: candidate }
					yield* writeBranchNameMap(projectPath, nextMap)
					return candidate
				}

				return yield* Effect.fail(
					new GitError({
						message: `Failed to allocate unique branch name for ${issueId}`,
						command: "git rev-parse --verify",
					}),
				)
			})

		/**
		 * Copy untracked files/directories to a new worktree
		 *
		 * Copies paths specified in the copyPaths config from the source worktree
		 * to the target worktree. This allows sharing untracked files like:
		 * - .direnv (Nix flake evaluation cache)
		 * - node_modules (dependencies)
		 * - .env.local (environment config)
		 * - vendor (Go/PHP dependencies)
		 *
		 * Missing paths are silently skipped. Errors are logged but don't fail
		 * worktree creation.
		 */
		const copyUntrackedFiles = (
			sourceWorktreePath: string,
			targetWorktreePath: string,
			copyPaths: readonly string[],
		): Effect.Effect<void, never, never> =>
			Effect.gen(function* () {
				if (copyPaths.length === 0) {
					yield* Effect.logDebug("No paths configured to copy")
					return
				}

				yield* Effect.logDebug(
					`Copying untracked files from ${sourceWorktreePath}: ${copyPaths.join(", ")}`,
				)

				// Copy each path, logging success/failure individually
				yield* Effect.forEach(
					copyPaths,
					(relativePath) =>
						Effect.gen(function* () {
							const sourcePath = pathService.join(sourceWorktreePath, relativePath)
							const targetPath = pathService.join(targetWorktreePath, relativePath)

							// Check if source exists
							const sourceExists = yield* fs.exists(sourcePath)
							if (!sourceExists) {
								yield* Effect.logDebug(`Skipping ${relativePath}: source does not exist`)
								return
							}

							// Ensure parent directory exists
							const targetParent = pathService.dirname(targetPath)
							yield* fs.makeDirectory(targetParent, { recursive: true }).pipe(Effect.ignore)

							// Copy file or directory
							yield* fs.copy(sourcePath, targetPath)
							yield* Effect.log(`Copied ${relativePath} to worktree`)
						}).pipe(
							// Don't fail on individual path copy errors
							Effect.catchAll((error) =>
								Effect.logWarning(`Failed to copy ${relativePath}: ${error}`),
							),
						),
					{ concurrency: "unbounded" },
				)
			}).pipe(
				// Don't fail worktree creation if copy fails
				Effect.catchAll((error) => Effect.logWarning(`Failed to copy untracked files: ${error}`)),
			)

		const injectIssueIdIntoEnvLocal = (
			targetWorktreePath: string,
			issueId: string,
		): Effect.Effect<void, never, never> =>
			Effect.gen(function* () {
				const envLocalPath = pathService.join(targetWorktreePath, ".env.local")
				const existingContent = yield* fs
					.exists(envLocalPath)
					.pipe(
						Effect.flatMap((exists) =>
							exists ? fs.readFileString(envLocalPath) : Effect.succeed(""),
						),
					)
				const normalizedIssueId = issueId.trim()
				if (normalizedIssueId.length === 0) {
					return
				}

				const issueLine = `AZEDARACH_ISSUE_ID=${normalizedIssueId}`
				const lines = existingContent.length > 0 ? existingContent.split(/\r?\n/) : []
				let replaced = false
				const updatedLines = lines.map((line) => {
					if (!replaced && line.startsWith("AZEDARACH_ISSUE_ID=")) {
						replaced = true
						return issueLine
					}
					return line
				})
				if (!replaced) {
					updatedLines.push(issueLine)
				}

				const compacted = updatedLines.filter(
					(line, index, all) => !(index === all.length - 1 && line.length === 0),
				)
				const nextContent = `${compacted.join("\n")}\n`
				yield* fs.writeFileString(envLocalPath, nextContent)
				yield* Effect.logDebug(`Injected AZEDARACH_ISSUE_ID into ${envLocalPath}`)
			}).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Failed to inject AZEDARACH_ISSUE_ID into .env.local: ${error}`),
				),
			)

		/**
		 * Copy Claude's local settings to a new worktree and inject hook configuration
		 *
		 * Claude Code stores personal permission grants in .claude/settings.local.json,
		 * which is globally gitignored and thus not copied when git creates a worktree.
		 * This function copies that file and merges in hook configuration for session
		 * state detection.
		 *
		 * @param sourceProjectPath - Path to the source project
		 * @param targetWorktreePath - Path to the target worktree
		 * @param issueId - Bead ID for the session
		 * @param hookOptions - Optional hook configuration options
		 */
		const copyClaudeLocalSettings = (
			sourceProjectPath: string,
			targetWorktreePath: string,
			issueId: string,
			hookOptions: HookConfigOptions = {},
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
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed("{}")),
								),
							),
						)
					existingSettings = yield* Effect.try({
						try: () => JSON.parse(content) as Record<string, unknown>,
						catch: () => ({}) as Record<string, unknown>,
					}).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(error).pipe(
								Effect.zipRight(Effect.succeed({} as Record<string, unknown>)),
							),
						),
					)
				}

				// Generate hook configuration for this bead
				const hookConfig = generateHookConfig(issueId, hookOptions)

				// Merge existing settings with hook configuration
				const mergedSettings = deepMerge(existingSettings, hookConfig)

				// Write merged settings to target
				yield* fs.writeFileString(targetSettings, JSON.stringify(mergedSettings, null, "\t"))

				// Inject worktree-specific skill with bead ID context
				const localSkillsDir = pathService.join(targetClaudeDir, "skills", "local")
				yield* fs.makeDirectory(localSkillsDir, { recursive: true })
				const skillPath = pathService.join(localSkillsDir, "worktree-context.skill.md")
				yield* fs.writeFileString(skillPath, generateWorktreeSkill(issueId))
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
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("{}")),
							),
						),
					)

				// Parse with Schema - fallback to empty object on failure
				const worktreeData = yield* decodeSettings(worktreeContent).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed({})),
						),
					),
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
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed("{}")),
								),
							),
						)

					mainData = yield* decodeSettings(mainContent).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed({})),
							),
						),
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
			const projectName = pathService.basename(normalizedProjectPath)
			const projectNamePrefix = `${projectName}-`

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

				const normalizedPath = pathService.resolve(path)
				const lastPart = pathService.basename(normalizedPath)
				let issueId = ""
				if (lastPart.startsWith(projectNamePrefix) && lastPart.length > projectNamePrefix.length) {
					issueId = lastPart.slice(projectNamePrefix.length)
				} else if (branch) {
					issueId = branch
				} else {
					const match = lastPart.match(/-([A-Za-z0-9]+[.-][A-Za-z0-9._-]+)$/)
					issueId = match?.[1] ?? ""
				}

				if (issueId && normalizedPath !== normalizedProjectPath) {
					worktrees.push({ path, issueId, branch, isLocked, head })
				}
			}
			return worktrees
		}

		// Pure helper to get worktree path (uses captured pathService)
		const getWorktreePath = (projectPath: string, issueId: string): string => {
			const projectName = pathService.basename(projectPath)
			const parentDir = pathService.dirname(projectPath)
			return pathService.join(parentDir, `${projectName}-${issueId}`)
		}

		const caseInsensitivePathLookup = process.platform === "darwin" || process.platform === "win32"

		const normalizePathForLookup = (rawPath: string): string => {
			const normalized = pathService.normalize(pathService.resolve(rawPath))
			return caseInsensitivePathLookup ? normalized.toLowerCase() : normalized
		}

		const pathsEqualForLookup = (leftPath: string, rightPath: string): boolean =>
			normalizePathForLookup(leftPath) === normalizePathForLookup(rightPath)

		const countUncommittedFiles = (
			worktreePath: string,
		): Effect.Effect<number, never, CommandExecutor.CommandExecutor> =>
			runGit(["status", "--porcelain"], worktreePath).pipe(
				Effect.map(
					(output) =>
						output
							.split("\n")
							.map((line) => line.trim())
							.filter((line) => line.length > 0).length,
				),
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(0)),
					),
				),
			)

		const countCommitsAheadOfBase = (
			worktreePath: string,
			baseBranch: string,
		): Effect.Effect<number | undefined, never, CommandExecutor.CommandExecutor> =>
			runGit(["rev-list", "--count", `${baseBranch}..HEAD`], worktreePath).pipe(
				Effect.map((output) => {
					const parsed = Number.parseInt(output.trim(), 10)
					return Number.isFinite(parsed) ? parsed : undefined
				}),
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(undefined)),
					),
				),
			)

		const buildWorktreeNameClashError = (options: {
			issueId: string
			conflictKind: "path" | "branch"
			requestedWorktreePath: string
			requestedBranch?: string
			conflictingWorktree: Worktree
			baseBranch: string
		}): Effect.Effect<WorktreeNameClashError, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const {
					issueId,
					conflictKind,
					requestedWorktreePath,
					requestedBranch,
					conflictingWorktree,
					baseBranch,
				} = options
				const commitsAheadOfBase = yield* countCommitsAheadOfBase(
					conflictingWorktree.path,
					baseBranch,
				)
				const uncommittedFileCount = yield* countUncommittedFiles(conflictingWorktree.path)

				return new WorktreeNameClashError({
					issueId,
					conflictKind,
					requestedWorktreePath,
					requestedBranch,
					conflictingIssueId: conflictingWorktree.issueId,
					conflictingWorktreePath: conflictingWorktree.path,
					conflictingBranch: conflictingWorktree.branch,
					baseBranch,
					commitsAheadOfBase,
					uncommittedFileCount,
				})
			})

		// Helper to refresh worktrees cache (with TTL to avoid repeated git calls)
		// Now supports multiple projects - each project has its own cache entry
		const refreshWorktrees = (
			projectPath: string,
		): Effect.Effect<void, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor> =>
			diagnostics.measure(
				{
					source: "WorktreeManager",
					name: "refreshWorktrees",
					thresholdMs: 200,
					details: projectPath,
				},
				Effect.gen(function* () {
					// Check if cache is still valid for this project
					const timestamps = yield* Ref.get(cacheTimestampRef)
					const now = Date.now()
					const cachedTimestamp = timestamps.get(projectPath)

					if (cachedTimestamp && now - cachedTimestamp < WORKTREE_CACHE_TTL_MS) {
						// Cache hit - skip git call
						return
					}

					const isRepo = yield* isGitRepo(projectPath)
					if (!isRepo) {
						return yield* Effect.fail(new NotAGitRepoError({ path: projectPath }))
					}

					const output = yield* runGit(["worktree", "list", "--porcelain"], projectPath)
					const worktrees = parseWorktreeList(output, projectPath)

					const newMap = new Map<string, Worktree>()
					for (const wt of worktrees) {
						newMap.set(wt.issueId, wt)
					}

					// Update cache for this project (preserves other projects)
					yield* Ref.update(worktreesRef, (cache) => {
						const newCache = new Map(cache)
						newCache.set(projectPath, newMap)
						return newCache
					})
					yield* Ref.update(cacheTimestampRef, (cache) => {
						const newCache = new Map(cache)
						newCache.set(projectPath, now)
						return newCache
					})
				}).pipe(Effect.withSpan("worktree.refresh")),
			)

		// Force refresh worktrees (bypass TTL cache for this project only)
		const forceRefreshWorktrees = (
			projectPath: string,
		): Effect.Effect<void, GitError | NotAGitRepoError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				// Invalidate cache for this project only
				yield* Ref.update(cacheTimestampRef, (cache) => {
					const newCache = new Map(cache)
					newCache.delete(projectPath)
					return newCache
				})
				yield* refreshWorktrees(projectPath)
			})

		return {
			create: (options: CreateWorktreeOptions) =>
				diagnostics.measure(
					{
						source: "WorktreeManager",
						name: "create",
						thresholdMs: 500,
						details: `issueId=${options.issueId}`,
					},
					Effect.gen(function* () {
						const {
							issueId,
							issueTitle,
							branchSlugMaxLength,
							baseBranch,
							projectPath,
							sourceWorktreePath,
							copyPaths,
							preCompactEnabled,
						} = options

						// Determine effective source for copying untracked files
						// If sourceWorktreePath is provided (e.g., epic worktree), use that
						// Otherwise fall back to the main project path
						const effectiveSourcePath = sourceWorktreePath ?? projectPath

						// Check if git repo
						const isRepo = yield* isGitRepo(projectPath)
						if (!isRepo) {
							return yield* Effect.fail(new NotAGitRepoError({ path: projectPath }))
						}

						// Get expected worktree path
						const worktreePath = getWorktreePath(projectPath, issueId)

						// Refresh cache and check if already exists
						yield* refreshWorktrees(projectPath)
						const allWorktrees = yield* Ref.get(worktreesRef)
						const projectWorktrees = allWorktrees.get(projectPath) ?? new Map()
						const existingWorktree = projectWorktrees.get(issueId)

						if (existingWorktree) {
							// Idempotent: worktree already exists
							return existingWorktree
						}

						const resolveComparisonBaseBranch = (() => {
							let cachedBaseBranch: string | undefined
							return (): Effect.Effect<string, GitError, CommandExecutor.CommandExecutor> =>
								Effect.gen(function* () {
									if (cachedBaseBranch) {
										return cachedBaseBranch
									}

									const resolvedBaseBranch = baseBranch || (yield* getCurrentBranch(projectPath))
									cachedBaseBranch = resolvedBaseBranch
									return resolvedBaseBranch
								})
						})()

						const worktreesBeforeCreate = Array.from(projectWorktrees.values())

						const conflictingByPath = worktreesBeforeCreate.find(
							(worktree) =>
								pathsEqualForLookup(worktree.path, worktreePath) && worktree.issueId !== issueId,
						)

						if (conflictingByPath) {
							const comparisonBaseBranch = yield* resolveComparisonBaseBranch()
							const clashError = yield* buildWorktreeNameClashError({
								issueId,
								conflictKind: "path",
								requestedWorktreePath: worktreePath,
								conflictingWorktree: conflictingByPath,
								baseBranch: comparisonBaseBranch,
							})
							return yield* Effect.fail(clashError)
						}

						const conflictingByCaseVariantIssueId = !caseInsensitivePathLookup
							? undefined
							: worktreesBeforeCreate.find(
									(worktree) =>
										worktree.issueId !== issueId &&
										worktree.issueId.toLowerCase() === issueId.toLowerCase(),
								)

						if (conflictingByCaseVariantIssueId) {
							const comparisonBaseBranch = yield* resolveComparisonBaseBranch()
							const clashError = yield* buildWorktreeNameClashError({
								issueId,
								conflictKind: "path",
								requestedWorktreePath: worktreePath,
								conflictingWorktree: conflictingByCaseVariantIssueId,
								baseBranch: comparisonBaseBranch,
							})
							return yield* Effect.fail(clashError)
						}

						const stalePathExists = yield* fs
							.exists(worktreePath)
							.pipe(Effect.catchAll(() => Effect.succeed(false)))
						if (stalePathExists) {
							const comparisonBaseBranch = yield* resolveComparisonBaseBranch()
							const clashError = yield* buildWorktreeNameClashError({
								issueId,
								conflictKind: "path",
								requestedWorktreePath: worktreePath,
								conflictingWorktree: {
									path: worktreePath,
									issueId,
									branch: "",
									isLocked: false,
									head: "",
								},
								baseBranch: comparisonBaseBranch,
							})
							return yield* Effect.fail(clashError)
						}

						const branchName = yield* getOrCreateBranchName({
							issueId,
							issueTitle,
							branchSlugMaxLength,
							projectPath,
						})

						const conflictingByBranch = Array.from(projectWorktrees.values()).find(
							(worktree) => worktree.branch === branchName && worktree.issueId !== issueId,
						)

						if (conflictingByBranch) {
							const comparisonBaseBranch = yield* resolveComparisonBaseBranch()
							const clashError = yield* buildWorktreeNameClashError({
								issueId,
								conflictKind: "branch",
								requestedWorktreePath: worktreePath,
								requestedBranch: branchName,
								conflictingWorktree: conflictingByBranch,
								baseBranch: comparisonBaseBranch,
							})
							return yield* Effect.fail(clashError)
						}

						// Check if branch already exists (e.g., from a previously deleted worktree)
						const hasBranch = yield* branchExists(branchName, projectPath)

						if (hasBranch) {
							// Branch exists - create worktree using the existing branch
							// git worktree add <path> <branch-name>
							yield* Effect.logInfo(`Branch ${branchName} already exists, reusing it for worktree`)
							yield* runGit(["worktree", "add", worktreePath, branchName], projectPath)
						} else {
							// Branch doesn't exist - create new branch and worktree
							// git worktree add -b <branch-name> <path> <start-point>
							const base = baseBranch || (yield* getCurrentBranch(projectPath))
							yield* runGit(["worktree", "add", "-b", branchName, worktreePath, base], projectPath)
						}

						// Copy Claude's local settings and inject hook configuration
						// Use effectiveSourcePath so child tasks inherit settings from epic worktree
						yield* copyClaudeLocalSettings(effectiveSourcePath, worktreePath, issueId, {
							preCompactEnabled,
							projectPath,
						})

						// Copy configured untracked files from source to new worktree
						// Default copyPaths includes [".direnv"] for Nix flake cache
						// When copyPaths is provided, it overrides the default (caller should include .direnv if needed)
						const effectiveCopyPaths = copyPaths ?? [".direnv"]
						yield* copyUntrackedFiles(effectiveSourcePath, worktreePath, effectiveCopyPaths)
						yield* injectIssueIdIntoEnvLocal(worktreePath, issueId)

						// Refresh cache and look for the new worktree with retry logic.
						// Git worktree list can sometimes miss newly created worktrees due to
						// filesystem sync timing issues, especially on macOS APFS. We retry
						// a few times with short delays to handle this race condition.
						const findNewWorktree = Effect.gen(function* () {
							yield* forceRefreshWorktrees(projectPath)
							const allUpdated = yield* Ref.get(worktreesRef)
							const projectUpdated = allUpdated.get(projectPath) ?? new Map()
							const newWorktree = projectUpdated.get(issueId)

							if (!newWorktree) {
								const foundIssueIds = Array.from(projectUpdated.keys())
								return yield* Effect.fail(
									new WorktreeCacheMissAfterCreateError({
										foundIssueIds,
										cacheSize: projectUpdated.size,
									}),
								)
							}
							return newWorktree
						})

						// Retry up to 5 times with 100ms delay between attempts (500ms total max wait)
						const retrySchedule = Schedule.recurs(4).pipe(Schedule.addDelay(() => "100 millis"))

						const result = yield* findNewWorktree.pipe(
							Effect.retry({
								schedule: retrySchedule,
								while: (e) => e._tag === "WorktreeCacheMissAfterCreateError",
							}),
							Effect.catchTag("WorktreeCacheMissAfterCreateError", (e) =>
								Effect.gen(function* () {
									yield* Effect.logError("Worktree created but not found in cache after retries", {
										issueId,
										worktreePath,
										projectPath,
										foundIssueIds: e.foundIssueIds,
										cacheSize: e.cacheSize,
									})

									const allAfterRetries = yield* Ref.get(worktreesRef)
									const projectAfterRetries =
										allAfterRetries.get(projectPath) ?? new Map<string, Worktree>()
									const worktreesAfterRetries = Array.from(projectAfterRetries.values())

									const conflictByPath = worktreesAfterRetries.find(
										(worktree) =>
											worktree.issueId !== issueId &&
											pathsEqualForLookup(worktree.path, worktreePath),
									)

									const conflictByCaseVariantIssueId =
										conflictByPath || !caseInsensitivePathLookup
											? undefined
											: worktreesAfterRetries.find(
													(worktree) =>
														worktree.issueId !== issueId &&
														worktree.issueId.toLowerCase() === issueId.toLowerCase(),
												)

									const conflictingWorktree = conflictByPath ?? conflictByCaseVariantIssueId
									if (conflictingWorktree) {
										const comparisonBaseBranch = yield* resolveComparisonBaseBranch().pipe(
											Effect.catchAll((error) =>
												Effect.logWarning(error).pipe(
													Effect.zipRight(Effect.succeed(baseBranch ?? "main")),
												),
											),
										)
										const clashError = yield* buildWorktreeNameClashError({
											issueId,
											conflictKind: "path",
											requestedWorktreePath: worktreePath,
											requestedBranch: branchName,
											conflictingWorktree,
											baseBranch: comparisonBaseBranch,
										})
										return yield* Effect.fail(clashError)
									}

									const stalePathExists = yield* fs
										.exists(worktreePath)
										.pipe(Effect.catchAll(() => Effect.succeed(false)))
									if (stalePathExists) {
										const comparisonBaseBranch = yield* resolveComparisonBaseBranch().pipe(
											Effect.catchAll(() => Effect.succeed(baseBranch ?? "main")),
										)
										const clashError = yield* buildWorktreeNameClashError({
											issueId,
											conflictKind: "path",
											requestedWorktreePath: worktreePath,
											requestedBranch: branchName,
											conflictingWorktree: {
												path: worktreePath,
												issueId,
												branch: branchName,
												isLocked: false,
												head: "",
											},
											baseBranch: comparisonBaseBranch,
										})
										return yield* Effect.fail(clashError)
									}

									return yield* Effect.fail(
										new GitError({
											message: `Worktree created but not found in list after retries. Looking for: ${issueId}, found: [${e.foundIssueIds.join(", ")}]`,
											command: `git worktree add ${worktreePath}`,
										}),
									)
								}),
							),
						)

						return result
					}).pipe(Effect.withSpan("worktree.create")),
				),

			remove: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options

					// Force refresh cache (mutation operation needs fresh data)
					yield* forceRefreshWorktrees(projectPath)
					const allWorktrees = yield* Ref.get(worktreesRef)
					const projectWorktrees = allWorktrees.get(projectPath) ?? new Map()
					const worktree = projectWorktrees.get(issueId)

					if (!worktree) {
						const expectedWorktreePath = getWorktreePath(projectPath, issueId)
						const stalePathExists = yield* fs
							.exists(expectedWorktreePath)
							.pipe(Effect.catchAll(() => Effect.succeed(false)))
						if (!stalePathExists) {
							// Safe no-op if doesn't exist
							return
						}

						yield* Effect.logWarning("Removing stale derived worktree directory", {
							issueId,
							projectPath,
							worktreePath: expectedWorktreePath,
						})

						yield* fs.remove(expectedWorktreePath, { recursive: true }).pipe(
							Effect.mapError(
								(error) =>
									new GitError({
										message: `Failed to remove stale worktree path: ${String(error)}`,
										command: `rm -rf ${expectedWorktreePath}`,
									}),
							),
						)

						yield* forceRefreshWorktrees(projectPath)
						return
					}

					// Remove worktree
					yield* runGit(["worktree", "remove", worktree.path, "--force"], projectPath)

					// Force refresh cache after removal
					yield* forceRefreshWorktrees(projectPath)
				}),

			list: (projectPath: string) =>
				Effect.gen(function* () {
					yield* refreshWorktrees(projectPath)
					const allWorktrees = yield* Ref.get(worktreesRef)
					const projectWorktrees = allWorktrees.get(projectPath) ?? new Map()
					return Array.from(projectWorktrees.values())
				}),

			exists: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					yield* refreshWorktrees(projectPath)
					const allWorktrees = yield* Ref.get(worktreesRef)
					const projectWorktrees = allWorktrees.get(projectPath) ?? new Map()
					return projectWorktrees.has(issueId)
				}),

			get: (options: { issueId: string; projectPath: string }) =>
				Effect.gen(function* () {
					const { issueId, projectPath } = options
					yield* refreshWorktrees(projectPath)
					const allWorktrees = yield* Ref.get(worktreesRef)
					const projectWorktrees = allWorktrees.get(projectPath) ?? new Map()
					return projectWorktrees.get(issueId) || null
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
	WorktreeCreateError,
	WorktreeManager | CommandExecutor.CommandExecutor
> => Effect.flatMap(WorktreeManager, (manager) => manager.create(options))

/**
 * Remove a worktree by bead ID
 */
export const remove = (options: {
	issueId: string
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
	issueId: string
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
	issueId: string
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
 *     issueId: "az-05y",
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
	WorktreeCreateError,
	Scope.Scope | WorktreeManager | CommandExecutor.CommandExecutor
> =>
	Effect.acquireRelease(create(options), (worktree) =>
		remove({ issueId: worktree.issueId, projectPath: options.projectPath }).pipe(
			Effect.tapError((error) =>
				Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
			),
			Effect.orElseSucceed(() => undefined),
		),
	)
