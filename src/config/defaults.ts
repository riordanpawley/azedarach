/**
 * Default Configuration Values
 *
 * Provides sensible defaults for all configuration options.
 * These are merged with user-provided config to ensure all fields are defined.
 */

import type { AzedarachConfig } from "./schema.js"

// ============================================================================
// Default Config Object
// ============================================================================

/**
 * Complete default configuration
 *
 * Every field has a default value, ensuring the resolved config is fully typed.
 */
export const DEFAULT_CONFIG = {
	worktree: {
		initCommands: [] as string[],
		env: {} as Record<string, string>,
		continueOnFailure: true,
		parallel: false,
	},
	session: {
		command: "claude",
		shell: process.env.SHELL || "bash",
		tmuxPrefix: "C-a",
	},
	patterns: {
		waiting: [] as string[],
		done: [] as string[],
		error: [] as string[],
	},
	pr: {
		autoDraft: true,
		autoMerge: false,
		baseBranch: "main",
	},
	notifications: {
		bell: true,
		system: false,
	},
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
	worktree: {
		initCommands: readonly string[]
		env: Readonly<Record<string, string>>
		continueOnFailure: boolean
		parallel: boolean
	}
	session: {
		command: string
		shell: string
		tmuxPrefix: string
	}
	patterns: {
		waiting: readonly string[]
		done: readonly string[]
		error: readonly string[]
	}
	pr: {
		autoDraft: boolean
		autoMerge: boolean
		baseBranch: string
	}
	notifications: {
		bell: boolean
		system: boolean
	}
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
		session: {
			command: config.session?.command ?? DEFAULT_CONFIG.session.command,
			shell: config.session?.shell ?? DEFAULT_CONFIG.session.shell,
			tmuxPrefix: config.session?.tmuxPrefix ?? DEFAULT_CONFIG.session.tmuxPrefix,
		},
		patterns: {
			waiting: config.patterns?.waiting ?? DEFAULT_CONFIG.patterns.waiting,
			done: config.patterns?.done ?? DEFAULT_CONFIG.patterns.done,
			error: config.patterns?.error ?? DEFAULT_CONFIG.patterns.error,
		},
		pr: {
			autoDraft: config.pr?.autoDraft ?? DEFAULT_CONFIG.pr.autoDraft,
			autoMerge: config.pr?.autoMerge ?? DEFAULT_CONFIG.pr.autoMerge,
			baseBranch: config.pr?.baseBranch ?? DEFAULT_CONFIG.pr.baseBranch,
		},
		notifications: {
			bell: config.notifications?.bell ?? DEFAULT_CONFIG.notifications.bell,
			system: config.notifications?.system ?? DEFAULT_CONFIG.notifications.system,
		},
	}
}
