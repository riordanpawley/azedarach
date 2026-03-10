/**
 * Configuration Schema for Azedarach
 *
 * Uses @effect/schema for runtime validation with versioned schemas and automatic
 * migration. Old config formats are automatically upgraded to the current version.
 *
 * ## Version History
 * - Version 1: Original schema (no version field) - legacy format
 * - Version 2: Adds numeric schema-version field (legacy `$schema`), moves pr.baseBranch → git.baseBranch
 * - Version 3: Adds top-level issueTracker selector + backend-specific config blocks
 * - Version 4: Nests backend config under top-level issueTracker object
 * - Version 5: Renames merge.startClaudeOnFailure → merge.startAiSessionOnFailure
 * - Version 6: Normalizes git.pr/git.merge aliases to canonical workflow config
 * - Version 7: Adds spec.enabled feature gating for optional spec workflows
 *
 * ## Adding New Versions
 * 1. Define ConfigVNSchema with numeric schema-version field
 * 2. Add VN-1ToVNTransform migration
 * 3. Update MigratingConfigSchema union
 * 4. Update CURRENT_CONFIG_VERSION
 */

import { Effect } from "effect"
import * as JSONSchema from "effect/JSONSchema"
import * as ParseResult from "effect/ParseResult"
import * as Schema from "effect/Schema"

// ============================================================================
// Version Constants
// ============================================================================

/** Current config schema version */
export const CURRENT_CONFIG_VERSION = 7
/** Relative schema URI used in `.azedarach.json` for JSON-LSP tooling */
export const AZEDARACH_CONFIG_JSON_SCHEMA_URI = "./.azedarach.schema.json"

// ============================================================================
// CLI Tool Configuration
// ============================================================================

/**
 * Supported CLI tools for AI coding assistance
 *
 * - claude: Claude Code (Anthropic's official CLI)
 * - opencode: OpenCode (SST's open-source alternative)
 * - codex: Codex CLI (OpenAI)
 */
export const CliToolSchema = Schema.Literal("claude", "opencode", "codex")
export type CliTool = Schema.Schema.Type<typeof CliToolSchema>

/**
 * Supported issue tracker backend selectors (legacy / internal).
 *
 * - tracker: legacy tracker backend
 * - legacy: rust tracker backend
 * - linear: Linear backend through linear-cli
 * - local: local-first sqlite backend (no external tracker required)
 */
export const IssueTrackerSchema = Schema.Literal("tracker", "legacy", "linear", "local")
export type IssueTracker = Schema.Schema.Type<typeof IssueTrackerSchema>

/**
 * Model configuration for AI sessions
 *
 * Allows configuring which models to use for different session types and tools.
 */
const SupportedModelSchema = Schema.Literal(
	"gpt-5.3-codex-spark",
	"gpt-5.3-codex",
	"gpt-5.4",
	"gpt-5-mini",
	"claude-4.5-haiku",
	"claude-4.5-sonnet",
)
export type SupportedModel = Schema.Schema.Type<typeof SupportedModelSchema>

const ModelConfigSchema = Schema.Struct({
	/**
	 * Default model for regular sessions (Space+s, Space+S)
	 * If not set, uses the CLI tool's default model.
	 */
	default: Schema.optional(SupportedModelSchema),

	/**
	 * Model for lightweight assistant interactions.
	 * Typically a faster/cheaper model for quick interactions.
	 */
	chat: Schema.optional(SupportedModelSchema),

	/**
	 * Tool-specific model configuration overrides.
	 * Allows having different defaults for each CLI tool at the same time.
	 */
	claude: Schema.optional(
		Schema.Struct({
			default: Schema.optional(SupportedModelSchema),
			chat: Schema.optional(SupportedModelSchema),
		}),
	),

	opencode: Schema.optional(
		Schema.Struct({
			default: Schema.optional(SupportedModelSchema),
			chat: Schema.optional(SupportedModelSchema),
		}),
	),

	codex: Schema.optional(
		Schema.Struct({
			default: Schema.optional(SupportedModelSchema),
			chat: Schema.optional(SupportedModelSchema),
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
 * Native tool signals (hooks/events) remain authoritative when available.
 * Pattern matching may be enabled to activate PTY-based detection/metrics.
 */
const StateDetectionConfigSchema = Schema.Struct({
	/**
	 * Enable regex pattern matching for state detection (default: false)
	 *
	 * When enabled, StateDetector analyzes terminal output to detect state.
	 * When disabled, PTY pattern-based monitoring is inert.
	 * This can produce false positives and should be treated as best-effort fallback.
	 *
	 * Native tool signals (via TmuxSessionMonitor) still take precedence.
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
 * Git-scoped PR workflow alias.
 *
 * Supported for config organization, then normalized to canonical workflow fields.
 */
const GitScopedPRConfigSchema = Schema.Struct({
	enabled: Schema.optional(Schema.Boolean),
	autoDraft: Schema.optional(Schema.Boolean),
	autoMerge: Schema.optional(Schema.Boolean),
	/** @deprecated Moved to top-level pr.aiModel in v6 */
	aiModel: Schema.optional(SupportedModelSchema),
	/** @deprecated Moved to git.baseBranch in v2 */
	baseBranch: Schema.optional(Schema.String),
})

/**
 * Git-scoped merge workflow alias.
 *
 * Supported for config organization, then normalized to canonical workflow fields.
 */
const GitScopedMergeConfigSchema = Schema.Struct({
	validateCommands: Schema.optional(Schema.Array(Schema.String)),
	fixCommand: Schema.optional(Schema.String),
	maxFixAttempts: Schema.optional(Schema.Number),
	startAiSessionOnFailure: Schema.optional(Schema.Boolean),
	/** @deprecated Renamed to startAiSessionOnFailure in v5 */
	startClaudeOnFailure: Schema.optional(Schema.Boolean),
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
	 * This makes branches non-ephemeral, enabling normal `tracker sync` behavior.
	 * Set to false for local-only development without a remote.
	 */
	pushBranchOnCreate: Schema.optional(Schema.Boolean),

	/** Remote to push to (default: "origin") */
	remote: Schema.optional(Schema.String),

	/** Prefix for branch names (default: "az-") */
	branchPrefix: Schema.optional(Schema.String),

	/**
	 * Maximum length for title-derived branch slug segment (default: 24)
	 *
	 * Applies to the `<slug>` part of `<author>/<issue-id>/<slug>`.
	 * The author prefix is derived from `git config user.name`.
	 */
	branchSlugMaxLength: Schema.optional(Schema.Number),

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

	/**
	 * Optional git-scoped PR workflow alias.
	 *
	 * This is normalized to canonical workflow config at load time.
	 */
	pr: Schema.optional(GitScopedPRConfigSchema),

	/**
	 * Optional git-scoped merge workflow alias.
	 *
	 * This is normalized to canonical workflow config at load time.
	 */
	merge: Schema.optional(GitScopedMergeConfigSchema),
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

	/**
	 * Optional model override for AI invoked during PR creation.
	 * When set, this model is used for PR-specific prompts instead of the main session default.
	 */
	aiModel: Schema.optional(SupportedModelSchema),
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
	 * Start an AI session to fix issues if auto-fix fails
	 * Default: true
	 */
	startAiSessionOnFailure: Schema.optional(Schema.Boolean),
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
	aiModel: Schema.optional(SupportedModelSchema),
	/** @deprecated Moved to git.baseBranch in v2 */
	baseBranch: Schema.optional(Schema.String),
})

/**
 * Legacy merge config schema (v4 and earlier)
 *
 * v5 renamed `startClaudeOnFailure` → `startAiSessionOnFailure`.
 */
const LegacyMergeConfigSchema = Schema.Struct({
	validateCommands: Schema.optional(Schema.Array(Schema.String)),
	fixCommand: Schema.optional(Schema.String),
	maxFixAttempts: Schema.optional(Schema.Number),
	startAiSessionOnFailure: Schema.optional(Schema.Boolean),
	/** @deprecated Renamed to startAiSessionOnFailure in v5 */
	startClaudeOnFailure: Schema.optional(Schema.Boolean),
})

/**
 * IssueTracker (tracker) backend configuration
 *
 * Controls legacy tracker issue tracker behavior.
 */
const LegacyBdConfigSchema = Schema.Struct({
	/**
	 * Enable tracker sync operations (default: true)
	 *
	 * When false, `tracker sync` is silently skipped. Issues are still
	 * tracked locally but not synced to the remote repository.
	 */
	syncEnabled: Schema.optional(Schema.Boolean),
})

/**
 * IssueTracker Rust (legacy) backend configuration
 *
 * Controls rust tracker issue tracker behavior.
 */
const LegacyBrConfigSchema = Schema.Struct({
	/**
	 * Enable tracker sync operations (default: true)
	 *
	 * When false, `tracker sync`/`legacy sync` is silently skipped.
	 */
	syncEnabled: Schema.optional(Schema.Boolean),
})

/**
 * Local backend configuration
 *
 * Runs Azedarach as a standalone issue tracker using local SQLite storage.
 */
const LocalBackupsConfigSchema = Schema.Struct({
	/**
	 * Enable automatic SQLite backups for the local issue store (default: true)
	 */
	enabled: Schema.optional(Schema.Boolean),

	/**
	 * Minimum age (minutes) before stale-on-open triggers a backup (default: 60)
	 */
	intervalMinutes: Schema.optional(Schema.Number),

	/**
	 * Cooldown between write-triggered backups (seconds, default: 300)
	 */
	writeCooldownSeconds: Schema.optional(Schema.Number),

	/**
	 * Max number of backup snapshots to keep (default: 30)
	 */
	maxBackups: Schema.optional(Schema.Number),

	/**
	 * Backup directory path.
	 *
	 * Relative paths are resolved against the project root.
	 * Default: ".azedarach/backups"
	 */
	directory: Schema.optional(Schema.String),
})

const LocalConfigSchema = Schema.Struct({
	/**
	 * Enable external sync queue processing when a sync target is configured.
	 *
	 * Local-only mode does not require external sync, so default is false.
	 */
	syncEnabled: Schema.optional(Schema.Boolean),

	/**
	 * Local SQLite backup policy and retention.
	 */
	backups: Schema.optional(LocalBackupsConfigSchema),
})

/**
 * Linear webhook listener configuration
 *
 * Controls how Azedarach consumes Linear webhook events for board refreshes.
 */
const LinearWebhookConfigSchema = Schema.Struct({
	/**
	 * Enable webhook-driven board refresh for linear backend (default: true)
	 */
	enabled: Schema.optional(Schema.Boolean),

	/**
	 * Webhook transport implementation (default: "sdk")
	 *
	 * - sdk: @linear/sdk webhook runtime + registration
	 * - cli: linear-cli `webhooks listen` fallback path
	 */
	transport: Schema.optional(Schema.Literal("sdk", "cli")),

	/**
	 * Optional public HTTPS base URL used when registering the webhook listener.
	 *
	 * When omitted, runtime attempts automatic URL resolution
	 * (env `LINEAR_WEBHOOK_PUBLIC_URL`, then local tunnel integration).
	 *
	 * Example: https://my-tunnel.ngrok.io
	 */
	url: Schema.optional(Schema.String),

	/**
	 * Local port for webhook delivery listener (default: 9000)
	 */
	port: Schema.optional(Schema.Number),

	/**
	 * Resource types to subscribe to (default: ["Issue"])
	 */
	events: Schema.optional(Schema.Array(Schema.String)),

	/**
	 * Optional webhook signing secret for verification
	 */
	secret: Schema.optional(Schema.String),
})

/**
 * Linear sync throttle policy
 *
 * Applies to all Linear SDK sync calls to avoid exceeding API limits while
 * still allowing short bursts.
 */
const LinearSyncThrottleConfigSchema = Schema.Struct({
	/**
	 * Sustained request rate (per minute) for Linear sync calls.
	 */
	maxPerMinute: Schema.optional(Schema.Number),

	/**
	 * Token bucket burst capacity.
	 */
	burst: Schema.optional(Schema.Number),
})

/**
 * Linear backend configuration
 *
 * Controls linear-cli integration.
 */
const LinearConfigSchema = Schema.Struct({
	/**
	 * Enable linear sync operations (default: true)
	 *
	 * For linear this controls whether sync-like operations are enabled.
	 */
	syncEnabled: Schema.optional(Schema.Boolean),

	/**
	 * Command used to invoke linear CLI (default: "linear-cli")
	 */
	command: Schema.optional(Schema.String),

	/**
	 * Default Linear team key or ID (required for create operations)
	 */
	team: Schema.optional(Schema.String),

	/**
	 * Optional default Linear project name or ID
	 */
	project: Schema.optional(Schema.String),

	/**
	 * Webhook listener configuration for event-driven refresh
	 */
	webhooks: Schema.optional(LinearWebhookConfigSchema),

	/**
	 * Sync throttle configuration for Linear API calls.
	 */
	syncThrottle: Schema.optional(LinearSyncThrottleConfigSchema),
})

/**
 * Canonical issue tracker configuration (v4+)
 *
 * Exactly one backend block must be set.
 */
const IssueTrackerConfigSchema = Schema.Struct({
	tracker: Schema.optional(LegacyBdConfigSchema),
	legacy: Schema.optional(LegacyBrConfigSchema),
	linear: Schema.optional(LinearConfigSchema),
	local: Schema.optional(LocalConfigSchema),
}).pipe(
	Schema.filter((value) => {
		const configuredCount =
			(value.tracker !== undefined ? 1 : 0) +
			(value.legacy !== undefined ? 1 : 0) +
			(value.linear !== undefined ? 1 : 0) +
			(value.local !== undefined ? 1 : 0)
		return configuredCount === 1
	}),
)

/**
 * Legacy tracker schema used for migration.
 *
 * v1/v2 had backend selection nested under `tracker.issueTracker`.
 */
const LegacyLegacyBdConfigSchema = Schema.Struct({
	syncEnabled: Schema.optional(Schema.Boolean),
	issueTracker: Schema.optional(IssueTrackerSchema),
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
 * Spec feature configuration
 *
 * Controls whether spec workflows are enabled for the project.
 */
const SpecConfigSchema = Schema.Struct({
	/**
	 * Enable az spec commands, guidance, and UI affordances (default: false)
	 */
	enabled: Schema.optional(Schema.Boolean),
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

	/**
	 * Base retry delay in milliseconds for transient auto-recovery failures (default: 1000)
	 *
	 * Retries use exponential backoff with jitter.
	 */
	retryBaseDelayMs: Schema.optional(Schema.Number),

	/**
	 * Maximum retry wait in milliseconds for transient auto-recovery failures (default: 60000)
	 *
	 * Auto-recovery keeps retrying transient failures indefinitely, but each wait
	 * duration is capped at this maximum.
	 */
	retryMaxDelayMs: Schema.optional(Schema.Number),
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

	/** Optional path to the tracker database for this project */
	issueStorePath: Schema.optional(Schema.String),
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
				pr !== undefined
					? {
							enabled: pr.enabled,
							autoDraft: pr.autoDraft,
							autoMerge: pr.autoMerge,
							aiModel: pr.aiModel,
						}
					: undefined

			return {
				...config,
				$schema: 2,
				git: migratedBaseBranch !== undefined ? { ...git, baseBranch: migratedBaseBranch } : git,
				pr: newPr,
			}
		},
	},
	{
		toVersion: 3,
		description: "Move backend selection to top-level issueTracker + backend blocks",
		migrate: (config) => {
			type BackendKey = "tracker" | "legacy" | "linear" | "local"

			const trackerToBackend = (tracker: IssueTracker): BackendKey => {
				switch (tracker) {
					case "tracker":
						return "tracker"
					case "legacy":
						return "legacy"
					case "linear":
						return "linear"
					case "local":
						return "local"
				}
			}

			const backendToTracker = (backend: BackendKey): IssueTracker => {
				switch (backend) {
					case "tracker":
						return "tracker"
					case "legacy":
						return "legacy"
					case "linear":
						return "linear"
					case "local":
						return "local"
				}
			}

			const explicitTracker: IssueTracker | undefined =
				config.issueTracker === "tracker" ||
				config.issueTracker === "legacy" ||
				config.issueTracker === "linear" ||
				config.issueTracker === "local"
					? config.issueTracker
					: undefined

			const nestedIssueTracker =
				config.issueTracker !== undefined &&
				typeof config.issueTracker === "object" &&
				config.issueTracker !== null
					? config.issueTracker
					: undefined

			const bdConfig =
				config.tracker ??
				(nestedIssueTracker?.tracker !== undefined
					? { syncEnabled: nestedIssueTracker.tracker.syncEnabled }
					: undefined)
			const brConfig =
				config.legacy ??
				(nestedIssueTracker?.legacy !== undefined
					? { syncEnabled: nestedIssueTracker.legacy.syncEnabled }
					: undefined)
			const linearConfig =
				config.linear ??
				(nestedIssueTracker?.linear !== undefined
					? {
							syncEnabled: nestedIssueTracker.linear.syncEnabled,
							command: nestedIssueTracker.linear.command,
							team: nestedIssueTracker.linear.team,
							project: nestedIssueTracker.linear.project,
							webhooks: nestedIssueTracker.linear.webhooks,
							syncThrottle: nestedIssueTracker.linear.syncThrottle,
						}
					: undefined)
			const localConfig =
				config.local ??
				(nestedIssueTracker?.local !== undefined
					? {
							syncEnabled: nestedIssueTracker.local.syncEnabled,
							backups: nestedIssueTracker.local.backups,
						}
					: undefined)

			const configuredBackends: BackendKey[] = []
			if (bdConfig !== undefined) configuredBackends.push("tracker")
			if (brConfig !== undefined) configuredBackends.push("legacy")
			if (linearConfig !== undefined) configuredBackends.push("linear")
			if (localConfig !== undefined) configuredBackends.push("local")

			if (configuredBackends.length > 1) {
				throw new Error(
					"Invalid config: only one issue backend block is allowed (tracker, legacy, linear, or local)",
				)
			}

			const inferredTracker =
				configuredBackends.length === 1 ? backendToTracker(configuredBackends[0]!) : undefined
			const legacyTracker = config.tracker?.issueTracker

			if (
				explicitTracker !== undefined &&
				inferredTracker !== undefined &&
				explicitTracker !== inferredTracker
			) {
				throw new Error(
					`Invalid config: issueTracker='${explicitTracker}' does not match backend block '${configuredBackends[0]}'`,
				)
			}

			const tracker: IssueTracker = explicitTracker ?? legacyTracker ?? inferredTracker ?? "local"
			const selectedBackend = trackerToBackend(tracker)

			const legacySyncEnabled = config.tracker?.syncEnabled
			const syncEnabledDefault = legacySyncEnabled ?? true

			return {
				...config,
				$schema: 3,
				issueTracker: tracker,
				tracker:
					selectedBackend === "tracker"
						? {
								syncEnabled: bdConfig?.syncEnabled ?? syncEnabledDefault,
							}
						: undefined,
				legacy:
					selectedBackend === "legacy"
						? {
								syncEnabled: brConfig?.syncEnabled ?? syncEnabledDefault,
							}
						: undefined,
				linear:
					selectedBackend === "linear"
						? {
								syncEnabled: linearConfig?.syncEnabled ?? syncEnabledDefault,
								command: linearConfig?.command,
								team: linearConfig?.team,
								project: linearConfig?.project,
								webhooks: linearConfig?.webhooks,
								syncThrottle: linearConfig?.syncThrottle,
							}
						: undefined,
				local:
					selectedBackend === "local"
						? {
								syncEnabled: localConfig?.syncEnabled ?? false,
								backups: localConfig?.backups,
							}
						: undefined,
			}
		},
	},
	{
		toVersion: 4,
		description: "Nest backend config under top-level issueTracker object",
		migrate: (config) => {
			const explicitTracker: IssueTracker | undefined =
				config.issueTracker === "tracker" ||
				config.issueTracker === "legacy" ||
				config.issueTracker === "linear" ||
				config.issueTracker === "local"
					? config.issueTracker
					: undefined

			const issueTrackerObject =
				config.issueTracker !== undefined &&
				typeof config.issueTracker === "object" &&
				config.issueTracker !== null
					? config.issueTracker
					: undefined

			if (issueTrackerObject !== undefined) {
				return {
					...config,
					$schema: 4,
					issueTracker: issueTrackerObject,
					tracker: undefined,
					legacy: undefined,
					linear: undefined,
					local: undefined,
				}
			}

			const inferredTracker: IssueTracker =
				explicitTracker ??
				(config.tracker !== undefined
					? "tracker"
					: config.legacy !== undefined
						? "legacy"
						: config.linear !== undefined
							? "linear"
							: config.local !== undefined
								? "local"
								: "local")

			const legacySyncEnabled = config.tracker?.syncEnabled
			const syncEnabledDefault = legacySyncEnabled ?? true

			const nestedIssueTracker =
				inferredTracker === "tracker"
					? {
							tracker: {
								syncEnabled: config.tracker?.syncEnabled ?? syncEnabledDefault,
							},
						}
					: inferredTracker === "legacy"
						? {
								legacy: {
									syncEnabled: config.legacy?.syncEnabled ?? syncEnabledDefault,
								},
							}
						: inferredTracker === "linear"
							? {
									linear: {
										syncEnabled: config.linear?.syncEnabled ?? syncEnabledDefault,
										command: config.linear?.command,
										team: config.linear?.team,
										project: config.linear?.project,
										webhooks: config.linear?.webhooks,
										syncThrottle: config.linear?.syncThrottle,
									},
								}
							: {
									local: {
										syncEnabled: config.local?.syncEnabled ?? false,
										backups: config.local?.backups,
									},
								}

			return {
				...config,
				$schema: 4,
				issueTracker: nestedIssueTracker,
				tracker: undefined,
				legacy: undefined,
				linear: undefined,
				local: undefined,
			}
		},
	},
	{
		toVersion: 5,
		description: "Rename merge.startClaudeOnFailure → merge.startAiSessionOnFailure",
		migrate: (config) => {
			const merge = config.merge
			const migratedMerge =
				merge === undefined
					? undefined
					: {
							validateCommands: merge.validateCommands,
							fixCommand: merge.fixCommand,
							maxFixAttempts: merge.maxFixAttempts,
							startAiSessionOnFailure: merge.startAiSessionOnFailure ?? merge.startClaudeOnFailure,
						}

			return {
				...config,
				$schema: 5,
				merge: migratedMerge,
			}
		},
	},
	{
		toVersion: 6,
		description: "Normalize git.pr/git.merge aliases to canonical workflow fields",
		migrate: (config) => {
			const git = config.git
			const scopedPr = git?.pr
			const scopedMerge = git?.merge

			const migratedPr =
				config.pr !== undefined || scopedPr !== undefined
					? {
							enabled: config.pr?.enabled ?? scopedPr?.enabled,
							autoDraft: config.pr?.autoDraft ?? scopedPr?.autoDraft,
							autoMerge: config.pr?.autoMerge ?? scopedPr?.autoMerge,
							aiModel: config.pr?.aiModel ?? scopedPr?.aiModel,
							baseBranch: scopedPr?.baseBranch,
						}
					: undefined

			const migratedMerge =
				config.merge ??
				(scopedMerge === undefined
					? undefined
					: {
							validateCommands: scopedMerge.validateCommands,
							fixCommand: scopedMerge.fixCommand,
							maxFixAttempts: scopedMerge.maxFixAttempts,
							startAiSessionOnFailure:
								scopedMerge.startAiSessionOnFailure ?? scopedMerge.startClaudeOnFailure,
						})

			const migratedBaseBranch =
				git?.baseBranch === undefined ? migratedPr?.baseBranch : git.baseBranch

			const migratedGit =
				git === undefined
					? undefined
					: {
							pushBranchOnCreate: git.pushBranchOnCreate,
							remote: git.remote,
							branchPrefix: git.branchPrefix,
							branchSlugMaxLength: git.branchSlugMaxLength,
							baseBranch: migratedBaseBranch,
							pushEnabled: git.pushEnabled,
							fetchEnabled: git.fetchEnabled,
							showLineChanges: git.showLineChanges,
							workflowMode: git.workflowMode,
						}

			return {
				...config,
				$schema: 6,
				git: migratedGit,
				pr: migratedPr,
				merge: migratedMerge,
			}
		},
	},
	{
		toVersion: 7,
		description: "Add spec.enabled feature gating for optional spec workflows",
		migrate: (config) => ({
			...config,
			$schema: 7,
			spec: config.spec,
		}),
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
	const startVersion =
		current.$version ?? (typeof current.$schema === "number" ? current.$schema : undefined) ?? 1

	for (const migration of migrations) {
		if (startVersion < migration.toVersion) {
			current = migration.migrate(current)
		}
	}

	const issueTrackerConfig =
		current.issueTracker !== undefined &&
		typeof current.issueTracker === "object" &&
		current.issueTracker !== null
			? current.issueTracker
			: undefined

	if (current.issueTracker !== undefined && issueTrackerConfig === undefined) {
		throw new Error(
			"Invalid config: issueTracker must be an object with exactly one backend block (tracker, legacy, linear, or local)",
		)
	}

	const gitConfig =
		current.git === undefined
			? undefined
			: {
					pushBranchOnCreate: current.git.pushBranchOnCreate,
					remote: current.git.remote,
					branchPrefix: current.git.branchPrefix,
					branchSlugMaxLength: current.git.branchSlugMaxLength,
					baseBranch: current.git.baseBranch,
					pushEnabled: current.git.pushEnabled,
					fetchEnabled: current.git.fetchEnabled,
					showLineChanges: current.git.showLineChanges,
					workflowMode: current.git.workflowMode,
				}

	const scopedPr = current.git?.pr
	const scopedMerge = current.git?.merge
	const prSource =
		current.pr !== undefined || scopedPr !== undefined
			? {
					enabled: current.pr?.enabled ?? scopedPr?.enabled,
					autoDraft: current.pr?.autoDraft ?? scopedPr?.autoDraft,
					autoMerge: current.pr?.autoMerge ?? scopedPr?.autoMerge,
					aiModel: current.pr?.aiModel ?? scopedPr?.aiModel,
				}
			: undefined
	const mergeSource =
		current.merge !== undefined || scopedMerge !== undefined
			? {
					validateCommands: current.merge?.validateCommands ?? scopedMerge?.validateCommands,
					fixCommand: current.merge?.fixCommand ?? scopedMerge?.fixCommand,
					maxFixAttempts: current.merge?.maxFixAttempts ?? scopedMerge?.maxFixAttempts,
					startAiSessionOnFailure:
						current.merge?.startAiSessionOnFailure ??
						scopedMerge?.startAiSessionOnFailure ??
						current.merge?.startClaudeOnFailure ??
						scopedMerge?.startClaudeOnFailure,
				}
			: undefined

	// Ensure version is set even if no migrations were needed
	// Strip legacy fields to match CurrentConfig
	return {
		$schema: CURRENT_CONFIG_VERSION,
		cliTool: current.cliTool,
		issueTracker: issueTrackerConfig,
		model: current.model,
		worktree: current.worktree,
		git: gitConfig,
		session: current.session,
		patterns: current.patterns,
		stateDetection: current.stateDetection,
		pr: prSource,
		merge: mergeSource,
		devServer: current.devServer,
		notifications: current.notifications,
		network: current.network,
		keyboard: current.keyboard,
		sessionRecovery: current.sessionRecovery,
		hooks: current.hooks,
		spec: current.spec,
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
 * Accepts both legacy and current formats.
 * Used as the input side of the migration transform.
 */
const RawConfigSchema = Schema.Struct({
	/** Config schema metadata URI for editors, or legacy numeric version in older files */
	$schema: Schema.optional(Schema.Union(Schema.String, Schema.Number)),
	/** Canonical config version used for migration sequencing */
	$version: Schema.optional(Schema.Number),

	/**
	 * CLI tool to use for AI sessions (default: "claude")
	 *
	 * Applies to NEW sessions only - existing sessions are not affected.
	 * - "claude": Claude Code (Anthropic's official CLI)
	 * - "opencode": OpenCode (SST's open-source alternative)
	 * - "codex": Codex CLI (OpenAI)
	 */
	cliTool: Schema.optional(CliToolSchema),
	issueTracker: Schema.optional(Schema.Union(IssueTrackerSchema, IssueTrackerConfigSchema)),

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
	merge: Schema.optional(LegacyMergeConfigSchema),
	devServer: Schema.optional(DevServerConfigSchema),
	notifications: Schema.optional(NotificationsConfigSchema),

	/** Legacy tracker config (supports nested issueTracker for migration) */
	tracker: Schema.optional(LegacyLegacyBdConfigSchema),
	/** IssueTracker rust backend config */
	legacy: Schema.optional(LegacyBrConfigSchema),
	/** Linear backend config */
	linear: Schema.optional(LinearConfigSchema),
	/** Local backend config */
	local: Schema.optional(LocalConfigSchema),

	/** Network connectivity configuration */
	network: Schema.optional(NetworkConfigSchema),

	/** Keyboard configuration */
	keyboard: Schema.optional(KeyboardConfigSchema),

	/** Session recovery configuration */
	sessionRecovery: Schema.optional(SessionRecoveryConfigSchema),

	/** Hooks configuration for spawned sessions */
	hooks: Schema.optional(HooksConfigSchema),

	/** Spec workflow feature gating */
	spec: Schema.optional(SpecConfigSchema),

	projects: Schema.optional(Schema.Array(ProjectConfigSchema)),
	defaultProject: Schema.optional(Schema.String),
})

/**
 * Current config schema (v7)
 *
 * This is the canonical schema after migration.
 * Does NOT include legacy fields - they should be migrated away.
 */
const CurrentConfigSchema = Schema.Struct({
	$schema: Schema.optional(Schema.Number),
	cliTool: Schema.optional(CliToolSchema),
	issueTracker: Schema.optional(IssueTrackerConfigSchema),
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
	network: Schema.optional(NetworkConfigSchema),
	keyboard: Schema.optional(KeyboardConfigSchema),
	sessionRecovery: Schema.optional(SessionRecoveryConfigSchema),
	hooks: Schema.optional(HooksConfigSchema),
	spec: Schema.optional(SpecConfigSchema),
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
export const AzedarachConfigSchema = Schema.transformOrFail(RawConfigSchema, CurrentConfigSchema, {
	strict: true,
	decode: (rawConfig, _options, ast, rawInput) =>
		Effect.try({
			try: () => applyMigrations(rawConfig),
			catch: (error) =>
				new ParseResult.Type(
					ast,
					rawInput,
					error instanceof Error ? error.message : `Config migration failed: ${String(error)}`,
				),
		}),
	encode: (current) =>
		Effect.succeed({
			...current,
			$schema: AZEDARACH_CONFIG_JSON_SCHEMA_URI,
			$version: CURRENT_CONFIG_VERSION,
			// Persist canonical v7 layout: top-level `pr`/`merge`, git aliases stripped.
			git: current.git,
			pr: current.pr,
			merge: current.merge,
		}),
})

export const AzedarachConfigJsonSchema = JSONSchema.make(AzedarachConfigSchema, {
	target: "jsonSchema2020-12",
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

/** IssueTracker config section type */
export type LegacyBdConfig = Schema.Schema.Type<typeof LegacyBdConfigSchema>

/** IssueTracker rust config section type */
export type LegacyBrConfig = Schema.Schema.Type<typeof LegacyBrConfigSchema>

/** Linear config section type */
export type LinearConfig = Schema.Schema.Type<typeof LinearConfigSchema>

/** Issue tracker config section type */
export type IssueTrackerConfig = Schema.Schema.Type<typeof IssueTrackerConfigSchema>

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

/** Spec config section type */
export type SpecConfig = Schema.Schema.Type<typeof SpecConfigSchema>
