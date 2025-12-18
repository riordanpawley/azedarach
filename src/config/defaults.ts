/**
 * Default Configuration Values
 *
 * Provides sensible defaults for all configuration options.
 * These are merged with user-provided config to ensure all fields are defined.
 */

import { execSync } from "node:child_process"
import type { AzedarachConfig } from "./schema.js"

// ============================================================================
// Login Shell Detection
// ============================================================================

/**
 * Get the user's login shell from the system
 *
 * We query the system directly rather than trusting $SHELL because:
 * - Nix develop shells often override $SHELL to bash for reproducibility
 * - direnv environments may inherit a non-login shell
 * - The login shell is what the user actually configured and expects
 *
 * Falls back to $SHELL or "bash" if detection fails.
 */
function getLoginShell(): string {
	try {
		if (process.platform === "darwin") {
			// macOS: Use Directory Services
			const result = execSync("dscl . -read /Users/$(whoami) UserShell", {
				encoding: "utf8",
				stdio: ["pipe", "pipe", "pipe"],
			})
			const shell = result.split(":")[1]?.trim()
			if (shell) return shell
		} else {
			// Linux/Unix: Use passwd database
			const result = execSync("getent passwd $(whoami) | cut -d: -f7", {
				encoding: "utf8",
				shell: "/bin/sh",
				stdio: ["pipe", "pipe", "pipe"],
			})
			const shell = result.trim()
			if (shell) return shell
		}
	} catch {
		// Detection failed, fall back to environment
	}
	return process.env.SHELL || "bash"
}

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
		shell: getLoginShell(),
		tmuxPrefix: "C-a",
		dangerouslySkipPermissions: false,
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
		dangerouslySkipPermissions: boolean
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
			autoDraft: config.pr?.autoDraft ?? DEFAULT_CONFIG.pr.autoDraft,
			autoMerge: config.pr?.autoMerge ?? DEFAULT_CONFIG.pr.autoMerge,
			baseBranch: config.pr?.baseBranch ?? DEFAULT_CONFIG.pr.baseBranch,
		},
		notifications: {
			bell: config.notifications?.bell ?? DEFAULT_CONFIG.notifications.bell,
			system: config.notifications?.system ?? DEFAULT_CONFIG.notifications.system,
		},
		projects: config.projects ?? DEFAULT_CONFIG.projects,
		defaultProject: config.defaultProject ?? DEFAULT_CONFIG.defaultProject,
	}
}
