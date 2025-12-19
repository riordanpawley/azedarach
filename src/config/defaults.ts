/**
 * Default Configuration Values
 *
 * Provides sensible defaults for all configuration options.
 * These are merged with user-provided config to ensure all fields are defined.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Effect } from "effect"
import { type AzedarachConfig, CURRENT_CONFIG_VERSION } from "./schema.js"

// ============================================================================
// Login Shell Detection
// ============================================================================

/**
 * Get the user's login shell from the system (Effect-based)
 *
 * We query the system directly rather than trusting $SHELL because:
 * - Nix develop shells often override $SHELL to bash for reproducibility
 * - direnv environments may inherit a non-login shell
 * - The login shell is what the user actually configured and expects
 *
 * Falls back to $SHELL or "bash" if detection fails.
 */
export const getLoginShell = (): Effect.Effect<string, never, CommandExecutor.CommandExecutor> =>
	Effect.gen(function* () {
		const fallback = process.env.SHELL || "bash"

		if (process.platform === "darwin") {
			// macOS: Use Directory Services
			const result = yield* Command.make(
				"dscl",
				".",
				"-read",
				`/Users/${process.env.USER}`,
				"UserShell",
			).pipe(
				Command.string,
				Effect.catchAll(() => Effect.succeed("")),
			)
			const shell = result.split(":")[1]?.trim()
			if (shell) return shell
		} else {
			// Linux/Unix: Use passwd database via getent
			const result = yield* Command.make("sh", "-c", "getent passwd $(whoami) | cut -d: -f7").pipe(
				Command.string,
				Effect.catchAll(() => Effect.succeed("")),
			)
			const shell = result.trim()
			if (shell) return shell
		}

		return fallback
	})

/**
 * Get the user's login shell synchronously (simple fallback)
 *
 * This returns $SHELL or "bash" - use getLoginShell() for accurate detection.
 */
const getLoginShellSync = (): string => process.env.SHELL || "bash"

// ============================================================================
// Default Config Object
// ============================================================================

/**
 * Complete default configuration
 *
 * Every field has a default value, ensuring the resolved config is fully typed.
 *
 * Note: session.shell uses a synchronous fallback. For accurate shell detection,
 * use getLoginShell() Effect and override the default when creating AppConfig.
 */
export const DEFAULT_CONFIG = {
	/** Current config version - used for automatic migrations */
	$schema: CURRENT_CONFIG_VERSION,
	worktree: {
		initCommands: [] satisfies string[],
		env: {} satisfies Record<string, string>,
		continueOnFailure: true,
		parallel: false,
	},
	git: {
		pushBranchOnCreate: true,
		remote: "origin",
		branchPrefix: "az-",
		pushEnabled: true,
		fetchEnabled: true,
		baseBranch: "main",
	},
	session: {
		command: "claude",
		shell: getLoginShellSync(),
		tmuxPrefix: "C-a",
		dangerouslySkipPermissions: false,
	},
	patterns: {
		waiting: [] satisfies string[],
		done: [] satisfies string[],
		error: [] satisfies string[],
	},
	pr: {
		enabled: true,
		autoDraft: true,
		autoMerge: false,
	},
	merge: {
		// No validation by default - must be explicitly configured in .azedarach.json
		validateCommands: [] satisfies string[],
		fixCommand: "",
		maxFixAttempts: 2,
		startClaudeOnFailure: true,
	},
	notifications: {
		bell: true,
		system: false,
	},
	beads: {
		syncEnabled: true,
	},
	network: {
		autoDetect: true,
		checkIntervalSeconds: 30,
		checkHost: "github.com",
	},
	devServer: {
		command: undefined,
		ports: {
			web: { default: 3000, aliases: ["PORT"] },
		},
		portPattern: "localhost:(\\d+)|127\\.0\\.0\\.1:(\\d+)",
		cwd: ".",
	},
	projects: [],
	defaultProject: undefined,
} as const

// ============================================================================
// Resolved Config Type
// ============================================================================

/**
 * Fully resolved config type with all defaults applied
 *
 * Unlike AzedarachConfig (which has optional fields), ResolvedConfig
 * has all fields defined after merging with defaults.
 */
export interface ResolvedConfig {
	/** Config schema version */
	$schema: number
	worktree: {
		initCommands: readonly string[]
		env: Readonly<Record<string, string>>
		continueOnFailure: boolean
		parallel: boolean
	}
	git: {
		pushBranchOnCreate: boolean
		remote: string
		branchPrefix: string
		pushEnabled: boolean
		fetchEnabled: boolean
		baseBranch: string
	}
	session: {
		command: string
		shell: string
		tmuxPrefix: string
		dangerouslySkipPermissions: boolean
	}
	patterns: {
		waiting: readonly string[]
		done: readonly string[]
		error: readonly string[]
	}
	pr: {
		enabled: boolean
		autoDraft: boolean
		autoMerge: boolean
	}
	merge: {
		validateCommands: readonly string[]
		fixCommand: string
		maxFixAttempts: number
		startClaudeOnFailure: boolean
	}
	notifications: {
		bell: boolean
		system: boolean
	}
	beads: {
		syncEnabled: boolean
	}
	network: {
		autoDetect: boolean
		checkIntervalSeconds: number
		checkHost: string
	}
	devServer: {
		command: string | undefined
		ports: Readonly<Record<string, { default: number; aliases: readonly string[] }>>
		portPattern: string
		cwd: string
	}
	projects: ReadonlyArray<{
		name: string
		path: string
		beadsPath?: string
	}>
	defaultProject: string | undefined
}

// ============================================================================
// Merge Helper
// ============================================================================

/**
 * Deep merge user config with defaults
 *
 * User-provided values override defaults. Missing sections get full defaults.
 *
 * @param config - User-provided configuration (may have undefined fields)
 * @returns Fully resolved configuration with all fields defined
 */
export function mergeWithDefaults(config: AzedarachConfig): ResolvedConfig {
	return {
		worktree: {
			initCommands: config.worktree?.initCommands ?? DEFAULT_CONFIG.worktree.initCommands,
			env: config.worktree?.env ?? DEFAULT_CONFIG.worktree.env,
			continueOnFailure:
				config.worktree?.continueOnFailure ?? DEFAULT_CONFIG.worktree.continueOnFailure,
			parallel: config.worktree?.parallel ?? DEFAULT_CONFIG.worktree.parallel,
		},
		git: {
			pushBranchOnCreate: config.git?.pushBranchOnCreate ?? DEFAULT_CONFIG.git.pushBranchOnCreate,
			remote: config.git?.remote ?? DEFAULT_CONFIG.git.remote,
			branchPrefix: config.git?.branchPrefix ?? DEFAULT_CONFIG.git.branchPrefix,
			pushEnabled: config.git?.pushEnabled ?? DEFAULT_CONFIG.git.pushEnabled,
			fetchEnabled: config.git?.fetchEnabled ?? DEFAULT_CONFIG.git.fetchEnabled,
			baseBranch: config.git?.baseBranch ?? DEFAULT_CONFIG.git.baseBranch,
		},
		session: {
			command: config.session?.command ?? DEFAULT_CONFIG.session.command,
			shell: config.session?.shell ?? DEFAULT_CONFIG.session.shell,
			tmuxPrefix: config.session?.tmuxPrefix ?? DEFAULT_CONFIG.session.tmuxPrefix,
			dangerouslySkipPermissions:
				config.session?.dangerouslySkipPermissions ??
				DEFAULT_CONFIG.session.dangerouslySkipPermissions,
		},
		patterns: {
			waiting: config.patterns?.waiting ?? DEFAULT_CONFIG.patterns.waiting,
			done: config.patterns?.done ?? DEFAULT_CONFIG.patterns.done,
			error: config.patterns?.error ?? DEFAULT_CONFIG.patterns.error,
		},
		pr: {
			enabled: config.pr?.enabled ?? DEFAULT_CONFIG.pr.enabled,
			autoDraft: config.pr?.autoDraft ?? DEFAULT_CONFIG.pr.autoDraft,
			autoMerge: config.pr?.autoMerge ?? DEFAULT_CONFIG.pr.autoMerge,
		},
		merge: {
			validateCommands: config.merge?.validateCommands ?? DEFAULT_CONFIG.merge.validateCommands,
			fixCommand: config.merge?.fixCommand ?? DEFAULT_CONFIG.merge.fixCommand,
			maxFixAttempts: config.merge?.maxFixAttempts ?? DEFAULT_CONFIG.merge.maxFixAttempts,
			startClaudeOnFailure:
				config.merge?.startClaudeOnFailure ?? DEFAULT_CONFIG.merge.startClaudeOnFailure,
		},
		notifications: {
			bell: config.notifications?.bell ?? DEFAULT_CONFIG.notifications.bell,
			system: config.notifications?.system ?? DEFAULT_CONFIG.notifications.system,
		},
		beads: {
			syncEnabled: config.beads?.syncEnabled ?? DEFAULT_CONFIG.beads.syncEnabled,
		},
		network: {
			autoDetect: config.network?.autoDetect ?? DEFAULT_CONFIG.network.autoDetect,
			checkIntervalSeconds:
				config.network?.checkIntervalSeconds ?? DEFAULT_CONFIG.network.checkIntervalSeconds,
			checkHost: config.network?.checkHost ?? DEFAULT_CONFIG.network.checkHost,
		},
		devServer: {
			command: config.devServer?.command ?? DEFAULT_CONFIG.devServer.command,
			ports: config.devServer?.ports ?? DEFAULT_CONFIG.devServer.ports,
			portPattern: config.devServer?.portPattern ?? DEFAULT_CONFIG.devServer.portPattern,
			cwd: config.devServer?.cwd ?? DEFAULT_CONFIG.devServer.cwd,
		},
		projects: config.projects ?? DEFAULT_CONFIG.projects,
		defaultProject: config.defaultProject ?? DEFAULT_CONFIG.defaultProject,
		$schema: config.$schema ?? DEFAULT_CONFIG.$schema,
	}
}
