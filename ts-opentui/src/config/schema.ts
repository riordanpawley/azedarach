/**
 * Configuration Schema for Azedarach
 *
 * Uses @effect/schema for runtime validation with versioned schemas and automatic
 * migration. Old config formats are automatically upgraded to the current version.
 *
 * ## Version History
 * - Version 1: Original schema (no $schema field) - legacy format
 * - Version 2: Adds $schema field, moves pr.baseBranch → git.baseBranch
 *
 * ## Adding New Versions
 * 1. Define ConfigVNSchema with `$schema: Schema.Literal(N)`
 * 2. Add VN-1ToVNTransform migration
 * 3. Update MigratingConfigSchema union
 * 4. Update CURRENT_CONFIG_VERSION
 */

import * as Schema from "effect/Schema"

// ============================================================================
// Version Constants
// ============================================================================

/** Current config schema version */
export const CURRENT_CONFIG_VERSION = 2

// ============================================================================
// CLI Tool Configuration
// ============================================================================

/**
 * Supported CLI tools for AI coding assistance
 *
 * - claude: Claude Code (Anthropic's official CLI)
 * - opencode: OpenCode (SST's open-source alternative)
 */
export const CliToolSchema = Schema.Literal("claude", "opencode")
export type CliTool = Schema.Schema.Type<typeof CliToolSchema>

/**
 * Model configuration for AI sessions
 *
 * Allows configuring which models to use for different session types and tools.
 */
const ModelConfigSchema = Schema.Struct({
	/**
	 * Default model for regular sessions (Space+s, Space+S)
	 * If not set, uses the CLI tool's default model.
	 */
	default: Schema.optional(Schema.String),

	/**
	 * Model for chat sessions (Space+c)
	 * Typically a faster/cheaper model for quick interactions.
	 */
	chat: Schema.optional(Schema.String),

	/**
	 * Tool-specific model configuration overrides.
	 * Allows having different defaults for Claude and OpenCode at the same time.
	 */
	claude: Schema.optional(
		Schema.Struct({
			default: Schema.optional(Schema.String),
			chat: Schema.optional(Schema.String),
		}),
	),

	opencode: Schema.optional(
		Schema.Struct({
			default: Schema.optional(Schema.String),
			chat: Schema.optional(Schema.String),
		}),
	),
})

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

	/**
	 * Paths to copy from source worktree to new worktree (default: [".direnv"])
	 *
	 * When creating a worktree for a child task of an epic, these paths are copied
	 * from the epic's worktree to the child worktree. This allows sharing untracked
	 * files like node_modules, .env.local, .direnv cache, etc.
	 *
	 * Each path is relative to the worktree root. Both files and directories are supported.
	 * Missing paths are silently skipped.
	 *
	 * @example ["node_modules", ".env.local", ".direnv", "vendor"]
	 */
	copyPaths: Schema.optional(Schema.Array(Schema.String)),
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

	/** Commands to run in background tmux windows when a session starts */
	backgroundTasks: Schema.optional(Schema.Array(Schema.String)),

	/**
	 * Maximum number of concurrent sessions allowed (default: 10)
	 */
	maxSessions: Schema.optional(Schema.Number),
})

/**
 * State detection pattern overrides
 *
 * Allows customizing the patterns used to detect Claude session state.
 * Only used if stateDetection.patternMatching is enabled.
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
 * State detection configuration
 *
 * Controls how session state (busy/waiting/done) is detected.
 * By default, uses Claude Code hooks which are authoritative.
 * Pattern matching can be enabled as a fallback for older Claude versions.
 */
const StateDetectionConfigSchema = Schema.Struct({
	/**
	 * Enable regex pattern matching for state detection (default: false)
	 *
	 * When enabled, StateDetector analyzes Claude's output to detect state.
	 * This is less reliable than hooks and can produce false positives.
	 * Only enable if hooks aren't working or for debugging.
	 *
	 * Hooks (via TmuxSessionMonitor) are always active and take precedence.
	 */
	patternMatching: Schema.optional(Schema.Boolean),
})

/**
 * Workflow mode for git operations
 *
 * - 'local': Work via Space+m to merge directly to main. PRs hidden.
 * - 'origin': Work via Space+P to create pull requests. Direct merge hidden.
 */
export const WorkflowModeSchema = Schema.Literal("local", "origin")
export type WorkflowMode = Schema.Schema.Type<typeof WorkflowModeSchema>

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

	/**
	 * Enable git push operations (default: true)
	 *
	 * When false, all git push operations are silently skipped.
	 * Useful for offline mode or local-only workflows.
	 */
	pushEnabled: Schema.optional(Schema.Boolean),

	/**
	 * Enable git fetch/pull operations (default: true)
	 *
	 * When false, git fetch and pull operations are silently skipped.
	 */
	fetchEnabled: Schema.optional(Schema.Boolean),

	/**
	 * Show line change statistics (+/-) in TaskCard headers (default: false)
	 *
	 * When enabled, shows +X/-Y line stats comparing the worktree to the base branch.
	 * Adds a small amount of overhead from running `git diff --stat`.
	 */
	showLineChanges: Schema.optional(Schema.Boolean),

	/**
	 * Workflow mode: 'local' for direct merge (Space+m), 'origin' for PRs (Space+P)
	 * Default: 'origin'
	 */
	workflowMode: Schema.optional(WorkflowModeSchema),
})

/**
 * PR workflow configuration
 *
 * Controls automatic PR creation behavior.
 */
const PRConfigSchema = Schema.Struct({
	/**
	 * Enable PR creation (default: true)
	 *
	 * When false, PR creation is disabled. The action menu will show
	 * "Create PR (disabled)" and attempting it will show an info message.
	 */
	enabled: Schema.optional(Schema.Boolean),

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

const DevServerConfigSchema = Schema.Struct({
	portPattern: Schema.optional(Schema.String),

	servers: Schema.optional(
		Schema.Record({
			key: Schema.String,
			value: Schema.Struct({
				command: Schema.String,
				cwd: Schema.optional(Schema.String),
				ports: Schema.optional(Schema.Record({ key: Schema.String, value: Schema.Number })),
			}),
		}),
	),
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

// ============================================================================
// Legacy Schemas (for migration)
// ============================================================================

/**
 * Legacy PR config schema (v1/unversioned)
 *
 * In legacy configs, baseBranch was under pr section.
 * This was moved to git section in v2.
 */
const LegacyPRConfigSchema = Schema.Struct({
	enabled: Schema.optional(Schema.Boolean),
	autoDraft: Schema.optional(Schema.Boolean),
	autoMerge: Schema.optional(Schema.Boolean),
	/** @deprecated Moved to git.baseBranch in v2 */
	baseBranch: Schema.optional(Schema.String),
})

/**
 * Beads configuration
 *
 * Controls beads issue tracker behavior.
 */
const BeadsConfigSchema = Schema.Struct({
	/**
	 * Enable beads sync operations (default: true)
	 *
	 * When false, `bd sync` is silently skipped. Issues are still
	 * tracked locally but not synced to the remote repository.
	 */
	syncEnabled: Schema.optional(Schema.Boolean),
})

/**
 * Keyboard configuration
 *
 * Controls keyboard-related settings like jump label characters.
 */
const KeyboardConfigSchema = Schema.Struct({
	/**
	 * Characters to use for jump labels in goto-word mode (default: "asdfjkl;")
	 *
	 * Uses home row keys for ergonomics. Customize for your keyboard layout:
	 * - QWERTY: "asdfjkl;"
	 * - Colemak: "arstneio" or "cieahtsn"
	 * - Dvorak: "aoeuhtns"
	 *
	 * 8 characters gives 64 possible labels (8×8), which is usually enough
	 * for visible tasks across all columns.
	 */
	jumpLabelChars: Schema.optional(Schema.String),
})

/**
 * Network configuration
 *
 * Controls automatic network connectivity detection.
 */
const NetworkConfigSchema = Schema.Struct({
	/**
	 * Automatically detect network connectivity (default: true)
	 *
	 * When true, periodically checks if github.com is reachable.
	 * If unreachable, network-dependent operations are disabled.
	 */
	autoDetect: Schema.optional(Schema.Boolean),

	/**
	 * Interval in seconds between connectivity checks (default: 30)
	 */
	checkIntervalSeconds: Schema.optional(Schema.Number),

	/**
	 * Host to check for connectivity (default: "github.com")
	 */
	checkHost: Schema.optional(Schema.String),
})

/**
 * Session recovery mode
 *
 * - 'auto': Automatically recover crashed sessions on startup (default)
 * - 'manual': Show crashed sessions in UI, user triggers recovery with R key
 */
export const SessionRecoveryModeSchema = Schema.Literal("auto", "manual")
export type SessionRecoveryMode = Schema.Schema.Type<typeof SessionRecoveryModeSchema>

/**
 * Hooks configuration
 *
 * Controls which Claude Code hooks are injected into spawned worktree sessions.
 * These hooks enable session state detection and context preservation.
 */
const HooksConfigSchema = Schema.Struct({
	/**
	 * PreCompact hook configuration
	 *
	 * The PreCompact hook fires before Claude compacts its context window.
	 * When enabled, it updates the bead with session progress so context
	 * survives compaction.
	 */
	preCompact: Schema.optional(
		Schema.Struct({
			/**
			 * Enable PreCompact hook (default: true)
			 *
			 * When true, injects a hook that updates the bead with session
			 * progress before context compaction. This ensures work-in-progress
			 * is preserved even if auto-compact triggers unexpectedly.
			 */
			enabled: Schema.optional(Schema.Boolean),
		}),
	),
})

/**
 * Session recovery configuration
 *
 * Controls how sessions are recovered after computer restart or tmux crash.
 * Sessions are detected as "crashed" when they exist in persisted state but
 * not in running tmux sessions.
 */
const SessionRecoveryConfigSchema = Schema.Struct({
	/**
	 * Recovery mode (default: "auto")
	 *
	 * - "auto": Automatically respawn crashed sessions on startup
	 * - "manual": Show crashed sessions in UI, recover with R/Shift+R keys
	 */
	mode: Schema.optional(SessionRecoveryModeSchema),

	/**
	 * Delay in milliseconds before auto-recovery starts (default: 2000)
	 *
	 * Gives time for UI to render so user sees what's being recovered.
	 * Only applies when mode is "auto".
	 */
	autoRecoveryDelayMs: Schema.optional(Schema.Number),
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
		toVersion: 2,
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
				$schema: 2,
				git: migratedBaseBranch !== undefined ? { ...git, baseBranch: migratedBaseBranch } : git,
				pr: newPr,
			}
		},
	},
	// ────────────────────────────────────────────────────────────────────────
	// Future migrations go here. Example:
	// ────────────────────────────────────────────────────────────────────────
	// {
	//   toVersion: 3,
	//   description: "Add session.timeout option",
	//   migrate: (config) => ({
	//     ...config,
	//     $schema: 3,
	//     // New fields get defaults, existing fields pass through
	//   }),
	// },
]

/**
 * Apply all necessary migrations to bring config to current version
 *
 * Migrations are applied in sequence. A config at v1 will go through
 * all migrations (v1→v2, v2→v3, etc.) until it reaches CURRENT_CONFIG_VERSION.
 */
const applyMigrations = (config: RawConfig): CurrentConfig => {
	let current = config
	const startVersion = current.$schema ?? 1

	for (const migration of migrations) {
		if (startVersion < migration.toVersion) {
			current = migration.migrate(current)
		}
	}

	// Ensure version is set even if no migrations were needed
	// Strip legacy fields to match CurrentConfig
	return {
		$schema: CURRENT_CONFIG_VERSION,
		cliTool: current.cliTool,
		model: current.model,
		worktree: current.worktree,
		git: current.git,
		session: current.session,
		patterns: current.patterns,
		stateDetection: current.stateDetection,
		pr: current.pr
			? {
					enabled: current.pr.enabled,
					autoDraft: current.pr.autoDraft,
					autoMerge: current.pr.autoMerge,
				}
			: undefined,
		merge: current.merge,
		devServer: current.devServer,
		notifications: current.notifications,
		beads: current.beads,
		network: current.network,
		keyboard: current.keyboard,
		sessionRecovery: current.sessionRecovery,
		hooks: current.hooks,
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
 * Accepts both legacy (v1/unversioned) and current (v2) formats.
 * Used as the input side of the migration transform.
 */
const RawConfigSchema = Schema.Struct({
	/** Config version - undefined/1 for legacy, 2+ for current */
	$schema: Schema.optional(Schema.Number),

	/**
	 * CLI tool to use for AI sessions (default: "claude")
	 *
	 * Applies to NEW sessions only - existing sessions are not affected.
	 * - "claude": Claude Code (Anthropic's official CLI)
	 * - "opencode": OpenCode (SST's open-source alternative)
	 */
	cliTool: Schema.optional(CliToolSchema),

	/**
	 * Model configuration for AI sessions
	 *
	 * Allows setting default and chat models.
	 * Model format depends on the CLI tool selected.
	 */
	model: Schema.optional(ModelConfigSchema),

	worktree: Schema.optional(WorktreeConfigSchema),
	git: Schema.optional(GitConfigSchema),
	session: Schema.optional(SessionConfigSchema),
	patterns: Schema.optional(PatternsConfigSchema),
	stateDetection: Schema.optional(StateDetectionConfigSchema),
	/** May contain legacy baseBranch field */
	pr: Schema.optional(LegacyPRConfigSchema),
	merge: Schema.optional(MergeConfigSchema),
	devServer: Schema.optional(DevServerConfigSchema),
	notifications: Schema.optional(NotificationsConfigSchema),

	/** Beads issue tracker configuration */
	beads: Schema.optional(BeadsConfigSchema),

	/** Network connectivity configuration */
	network: Schema.optional(NetworkConfigSchema),

	/** Keyboard configuration */
	keyboard: Schema.optional(KeyboardConfigSchema),

	/** Session recovery configuration */
	sessionRecovery: Schema.optional(SessionRecoveryConfigSchema),

	/** Hooks configuration for spawned sessions */
	hooks: Schema.optional(HooksConfigSchema),

	projects: Schema.optional(Schema.Array(ProjectConfigSchema)),
	defaultProject: Schema.optional(Schema.String),
})

/**
 * Current config schema (v2)
 *
 * This is the canonical schema after migration.
 * Does NOT include legacy fields - they should be migrated away.
 */
const CurrentConfigSchema = Schema.Struct({
	$schema: Schema.optional(Schema.Number),
	cliTool: Schema.optional(CliToolSchema),
	model: Schema.optional(ModelConfigSchema),
	worktree: Schema.optional(WorktreeConfigSchema),
	git: Schema.optional(GitConfigSchema),
	session: Schema.optional(SessionConfigSchema),
	patterns: Schema.optional(PatternsConfigSchema),
	stateDetection: Schema.optional(StateDetectionConfigSchema),
	pr: Schema.optional(PRConfigSchema),
	merge: Schema.optional(MergeConfigSchema),
	devServer: Schema.optional(DevServerConfigSchema),
	notifications: Schema.optional(NotificationsConfigSchema),
	beads: Schema.optional(BeadsConfigSchema),
	network: Schema.optional(NetworkConfigSchema),
	keyboard: Schema.optional(KeyboardConfigSchema),
	sessionRecovery: Schema.optional(SessionRecoveryConfigSchema),
	hooks: Schema.optional(HooksConfigSchema),
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
	encode: (current) => ({ ...current, $schema: CURRENT_CONFIG_VERSION }),
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

/** State detection config section type */
export type StateDetectionConfig = Schema.Schema.Type<typeof StateDetectionConfigSchema>

/** PR config section type */
export type PRConfig = Schema.Schema.Type<typeof PRConfigSchema>

/** Merge config section type */
export type MergeConfig = Schema.Schema.Type<typeof MergeConfigSchema>

/** Notifications config section type */
export type NotificationsConfig = Schema.Schema.Type<typeof NotificationsConfigSchema>

/** Beads config section type */
export type BeadsConfig = Schema.Schema.Type<typeof BeadsConfigSchema>

/** Network config section type */
export type NetworkConfig = Schema.Schema.Type<typeof NetworkConfigSchema>

/** Project config section type */
export type ProjectConfig = Schema.Schema.Type<typeof ProjectConfigSchema>

/** Dev server config section type */
export type DevServerConfig = Schema.Schema.Type<typeof DevServerConfigSchema>

/** Model config section type */
export type ModelConfig = Schema.Schema.Type<typeof ModelConfigSchema>

/** Keyboard config section type */
export type KeyboardConfig = Schema.Schema.Type<typeof KeyboardConfigSchema>

/** Session recovery config section type */
export type SessionRecoveryConfig = Schema.Schema.Type<typeof SessionRecoveryConfigSchema>

/** Hooks config section type */
export type HooksConfig = Schema.Schema.Type<typeof HooksConfigSchema>
