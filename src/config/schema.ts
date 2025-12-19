/**
 * Configuration Schema for Azedarach
 *
 * Uses @effect/schema for runtime validation, matching patterns in BeadsClient.ts.
 * Schema definitions provide both TypeScript types and runtime validation.
 */

import * as Schema from "effect/Schema"

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

	/** Base branch for PRs (default: "main") */
	baseBranch: Schema.optional(Schema.String),
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
// Root Schema
// ============================================================================

/**
 * Root configuration schema for Azedarach
 *
 * All sections are optional - missing sections use defaults.
 */
export const AzedarachConfigSchema = Schema.Struct({
	/** Worktree lifecycle configuration */
	worktree: Schema.optional(WorktreeConfigSchema),

	/** Git behavior configuration */
	git: Schema.optional(GitConfigSchema),

	/** Claude session configuration */
	session: Schema.optional(SessionConfigSchema),

	/** State detection pattern overrides */
	patterns: Schema.optional(PatternsConfigSchema),

	/** PR workflow configuration */
	pr: Schema.optional(PRConfigSchema),

	/** Merge workflow configuration */
	merge: Schema.optional(MergeConfigSchema),

	/** Notification configuration */
	notifications: Schema.optional(NotificationsConfigSchema),

	/** Project configurations */
	projects: Schema.optional(Schema.Array(ProjectConfigSchema)),

	/** Default project name to use */
	defaultProject: Schema.optional(Schema.String),
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
