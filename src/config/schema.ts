/**
 * Configuration Schema for Azedarach
 *
 * Uses @effect/schema for runtime validation, matching patterns in BeadsClient.ts.
 * Schema definitions provide both TypeScript types and runtime validation.
 *
 * ## Config Versioning
 *
 * The config file includes a `configVersion` field to track schema changes.
 * When loading, the schema automatically migrates old formats to current.
 *
 * Version history:
 * - 1: Initial versioned schema (moved pr.baseBranch → git.baseBranch)
 * - 0/undefined: Legacy unversioned configs
 */

import * as Schema from "effect/Schema"

// ============================================================================
// Version Constants
// ============================================================================

/** Current config schema version */
export const CURRENT_CONFIG_VERSION = 1

// ============================================================================
// Nested Config Schemas
// ============================================================================

/**
 * Worktree configuration - hooks for worktree lifecycle
 *
 * Controls what happens after a git worktree is created for a bead session.
 */
const WorktreeConfigSchema = Schema.Struct({
	/** Commands to run after worktree creation (e.g., "direnv allow", "bun install") */
	initCommands: Schema.optional(Schema.Array(Schema.String)),

	/** Environment variables to set when running init commands */
	env: Schema.optional(Schema.Record({ key: Schema.String, value: Schema.String })),

	/** Continue with remaining commands if one fails (default: true) */
	continueOnFailure: Schema.optional(Schema.Boolean),

	/** Run init commands in parallel instead of sequentially (default: false) */
	parallel: Schema.optional(Schema.Boolean),
})

/**
 * Session configuration - Claude session defaults
 *
 * Controls how Claude Code sessions are started in tmux.
 */
const SessionConfigSchema = Schema.Struct({
	/** The command to run Claude (default: "claude") */
	command: Schema.optional(Schema.String),

	/** Shell to use for the tmux session (default: $SHELL or "bash") */
	shell: Schema.optional(Schema.String),

	/** tmux prefix key (default: "C-a" to avoid Claude capturing C-b) */
	tmuxPrefix: Schema.optional(Schema.String),

	/** Run Claude with --dangerously-skip-permissions flag (default: false) */
	dangerouslySkipPermissions: Schema.optional(Schema.Boolean),
})

/**
 * State detection pattern overrides
 *
 * Allows customizing the patterns used to detect Claude session state.
 */
const PatternsConfigSchema = Schema.Struct({
	/** Patterns that indicate Claude is waiting for user input */
	waiting: Schema.optional(Schema.Array(Schema.String)),

	/** Patterns that indicate Claude has completed the task */
	done: Schema.optional(Schema.Array(Schema.String)),

	/** Patterns that indicate an error occurred */
	error: Schema.optional(Schema.Array(Schema.String)),
})

/**
 * Git configuration
 *
 * Controls git behavior for worktrees and branches.
 */
const GitConfigSchema = Schema.Struct({
	/**
	 * Push branches after worktree creation (default: true)
	 *
	 * When true, runs `git push -u <remote> <branch>` after creating worktrees.
	 * This makes branches non-ephemeral, enabling normal `bd sync` behavior.
	 * Set to false for local-only development without a remote.
	 */
	pushBranchOnCreate: Schema.optional(Schema.Boolean),

	/** Remote to push to (default: "origin") */
	remote: Schema.optional(Schema.String),

	/** Prefix for branch names (default: "az-") */
	branchPrefix: Schema.optional(Schema.String),

	/**
	 * Base branch for merges, diffs, and PRs (default: "main")
	 *
	 * This is the branch that worktree branches are compared against and merged into.
	 * Common values: "main", "master", "develop", "preview"
	 */
	baseBranch: Schema.optional(Schema.String),
})

/**
 * PR workflow configuration
 *
 * Controls automatic PR creation behavior.
 */
const PRConfigSchema = Schema.Struct({
	/** Create PRs as draft (default: true) */
	autoDraft: Schema.optional(Schema.Boolean),

	/** Auto-merge after CI passes (default: false) */
	autoMerge: Schema.optional(Schema.Boolean),
})

/**
 * Merge workflow configuration
 *
 * Controls post-merge validation behavior (Space+m).
 */
const MergeConfigSchema = Schema.Struct({
	/**
	 * Commands to run after merge to validate the result
	 * All commands must pass for merge to be considered successful
	 * Default: ["bun run type-check"]
	 */
	validateCommands: Schema.optional(Schema.Array(Schema.String)),

	/**
	 * Command to run when validation fails to attempt auto-fix
	 * Default: "bun run fix"
	 */
	fixCommand: Schema.optional(Schema.String),

	/**
	 * Maximum number of fix attempts before giving up
	 * Default: 2
	 */
	maxFixAttempts: Schema.optional(Schema.Number),

	/**
	 * Start a Claude session to fix issues if auto-fix fails
	 * Default: true
	 */
	startClaudeOnFailure: Schema.optional(Schema.Boolean),
})

/**
 * Notification configuration
 *
 * Controls how users are notified of session state changes.
 */
const NotificationsConfigSchema = Schema.Struct({
	/** Terminal bell on state change (default: true) */
	bell: Schema.optional(Schema.Boolean),

	/** System notifications via osascript/notify-send (default: false) */
	system: Schema.optional(Schema.Boolean),
})

/**
 * Project configuration
 *
 * Defines a project that can be managed by Azedarach.
 */
const ProjectConfigSchema = Schema.Struct({
	/** Name of the project */
	name: Schema.String,

	/** Absolute path to the project root */
	path: Schema.String,

	/** Optional path to the beads database for this project */
	beadsPath: Schema.optional(Schema.String),
})

// ============================================================================
// Legacy Schemas (for migration)
// ============================================================================

/**
 * Legacy PR config schema (v0)
 *
 * In v0 configs, baseBranch was under pr section.
 * This was moved to git section in v1.
 */
const LegacyPRConfigSchema = Schema.Struct({
	autoDraft: Schema.optional(Schema.Boolean),
	autoMerge: Schema.optional(Schema.Boolean),
	/** @deprecated Moved to git.baseBranch in v1 */
	baseBranch: Schema.optional(Schema.String),
})

// ============================================================================
// Migration System
// ============================================================================

/**
 * Raw config type from schema - used as input to migrations
 */
type RawConfig = Schema.Schema.Type<typeof RawConfigSchema>

/**
 * Current config type from schema - output of migrations
 */
type CurrentConfig = Schema.Schema.Type<typeof CurrentConfigSchema>

/**
 * A migration transforms config from one version to the next.
 *
 * Each migration is self-contained and documents what it changes.
 * This pattern makes it easy to:
 * - See exactly what changed in each version
 * - Test migrations in isolation
 * - Add new migrations without touching old code
 */
interface Migration {
	/** Version this migration produces */
	readonly toVersion: number
	/** Human-readable description of what changed */
	readonly description: string
	/** Transform function */
	readonly migrate: (config: RawConfig) => RawConfig
}

/**
 * Migration registry - add new migrations here
 *
 * Each migration handles ONE version bump.
 * Migrations are applied in sequence from the config's current version to CURRENT_CONFIG_VERSION.
 */
const migrations: readonly Migration[] = [
	{
		toVersion: 1,
		description: "Move pr.baseBranch → git.baseBranch",
		migrate: (config) => {
			const pr = config.pr
			const git = config.git

			// Extract legacy baseBranch from pr section
			const legacyBaseBranch = pr?.baseBranch
			const currentGitBaseBranch = git?.baseBranch

			// Migrate if legacy exists and current doesn't
			const migratedBaseBranch =
				legacyBaseBranch !== undefined && currentGitBaseBranch === undefined
					? legacyBaseBranch
					: currentGitBaseBranch

			// Build new pr config without legacy baseBranch field
			const newPr =
				pr !== undefined ? { autoDraft: pr.autoDraft, autoMerge: pr.autoMerge } : undefined

			return {
				...config,
				configVersion: 1,
				git: migratedBaseBranch !== undefined ? { ...git, baseBranch: migratedBaseBranch } : git,
				pr: newPr,
			}
		},
	},
	// ────────────────────────────────────────────────────────────────────────
	// Future migrations go here. Example:
	// ────────────────────────────────────────────────────────────────────────
	// {
	//   toVersion: 2,
	//   description: "Add session.timeout option",
	//   migrate: (config) => ({
	//     ...config,
	//     configVersion: 2,
	//     // New fields get defaults, existing fields pass through
	//   }),
	// },
]

/**
 * Apply all necessary migrations to bring config to current version
 *
 * Migrations are applied in sequence. A config at v0 will go through
 * all migrations (v0→v1, v1→v2, etc.) until it reaches CURRENT_CONFIG_VERSION.
 */
const applyMigrations = (config: RawConfig): CurrentConfig => {
	let current = config
	const startVersion = current.configVersion ?? 0

	for (const migration of migrations) {
		if (startVersion < migration.toVersion) {
			current = migration.migrate(current)
		}
	}

	// Ensure version is set even if no migrations were needed
	// Strip legacy fields to match CurrentConfig
	return {
		configVersion: CURRENT_CONFIG_VERSION,
		worktree: current.worktree,
		git: current.git,
		session: current.session,
		patterns: current.patterns,
		pr: current.pr
			? { autoDraft: current.pr.autoDraft, autoMerge: current.pr.autoMerge }
			: undefined,
		merge: current.merge,
		notifications: current.notifications,
		projects: current.projects,
		defaultProject: current.defaultProject,
	}
}

// ============================================================================
// Root Schema
// ============================================================================

/**
 * Raw input schema for Azedarach config
 *
 * Accepts both legacy (v0) and current (v1) formats.
 * Used as the input side of the migration transform.
 */
const RawConfigSchema = Schema.Struct({
	/** Config version - undefined/0 for legacy, 1 for current */
	configVersion: Schema.optional(Schema.Number),

	worktree: Schema.optional(WorktreeConfigSchema),
	git: Schema.optional(GitConfigSchema),
	session: Schema.optional(SessionConfigSchema),
	patterns: Schema.optional(PatternsConfigSchema),
	/** May contain legacy baseBranch field */
	pr: Schema.optional(LegacyPRConfigSchema),
	merge: Schema.optional(MergeConfigSchema),
	notifications: Schema.optional(NotificationsConfigSchema),
	projects: Schema.optional(Schema.Array(ProjectConfigSchema)),
	defaultProject: Schema.optional(Schema.String),
})

/**
 * Current config schema (v1)
 *
 * This is the canonical schema after migration.
 * Does NOT include legacy fields - they should be migrated away.
 */
const CurrentConfigSchema = Schema.Struct({
	configVersion: Schema.optional(Schema.Number),
	worktree: Schema.optional(WorktreeConfigSchema),
	git: Schema.optional(GitConfigSchema),
	session: Schema.optional(SessionConfigSchema),
	patterns: Schema.optional(PatternsConfigSchema),
	pr: Schema.optional(PRConfigSchema),
	merge: Schema.optional(MergeConfigSchema),
	notifications: Schema.optional(NotificationsConfigSchema),
	projects: Schema.optional(Schema.Array(ProjectConfigSchema)),
	defaultProject: Schema.optional(Schema.String),
})

/**
 * Root configuration schema for Azedarach with automatic migration
 *
 * The transform pipeline:
 * 1. RawConfigSchema validates basic structure (accepts legacy fields)
 * 2. applyMigrations() transforms to current version
 * 3. Result matches CurrentConfigSchema
 */
export const AzedarachConfigSchema = Schema.transform(RawConfigSchema, CurrentConfigSchema, {
	strict: true,
	decode: applyMigrations,
	encode: (current) => ({ ...current, configVersion: CURRENT_CONFIG_VERSION }),
})

// ============================================================================
// Type Exports
// ============================================================================

/** Input type for config (what users write in .azedarach.json) */
export type AzedarachConfigInput = Schema.Schema.Encoded<typeof AzedarachConfigSchema>

/** Validated config type (after schema validation) */
export type AzedarachConfig = Schema.Schema.Type<typeof AzedarachConfigSchema>

/** Worktree config section type */
export type WorktreeConfig = Schema.Schema.Type<typeof WorktreeConfigSchema>

/** Git config section type */
export type GitConfig = Schema.Schema.Type<typeof GitConfigSchema>

/** Session config section type */
export type SessionConfig = Schema.Schema.Type<typeof SessionConfigSchema>

/** Patterns config section type */
export type PatternsConfig = Schema.Schema.Type<typeof PatternsConfigSchema>

/** PR config section type */
export type PRConfig = Schema.Schema.Type<typeof PRConfigSchema>

/** Merge config section type */
export type MergeConfig = Schema.Schema.Type<typeof MergeConfigSchema>

/** Notifications config section type */
export type NotificationsConfig = Schema.Schema.Type<typeof NotificationsConfigSchema>

/** Project config section type */
export type ProjectConfig = Schema.Schema.Type<typeof ProjectConfigSchema>
