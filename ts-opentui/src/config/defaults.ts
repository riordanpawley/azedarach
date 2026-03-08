/**
 * Default Configuration Values
 *
 * Provides sensible defaults for all configuration options.
 * These are merged with user-provided config to ensure all fields are defined.
 */

import { Command, type CommandExecutor } from "@effect/platform"
import { Effect } from "effect"
import {
	type AzedarachConfig,
	type CliTool,
	CURRENT_CONFIG_VERSION,
	type SessionRecoveryMode,
	type SupportedModel,
	type WorkflowMode,
} from "./schema.js"

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
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed("")),
					),
				),
			)
			const shell = result.split(":")[1]?.trim()
			if (shell) return shell
		} else {
			// Linux/Unix: Use passwd database via getent
			const result = yield* Command.make("sh", "-c", "getent passwd $(whoami) | cut -d: -f7").pipe(
				Command.string,
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed("")),
					),
				),
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

	/** CLI tool to use for AI sessions */
	cliTool: "claude" as const,

	/** Model configuration for AI sessions */
	model: {
		/** Default model - undefined means use CLI tool's default */
		default: undefined as SupportedModel | undefined,
		/** Chat model - defaults based on CLI tool */
		chat: undefined as SupportedModel | undefined,
		/** Claude-specific overrides */
		claude: {
			default: "claude-4.5-haiku" as SupportedModel | undefined,
			chat: undefined as SupportedModel | undefined,
		},
		/** OpenCode-specific overrides */
		opencode: {
			default: "gpt-5.3-codex-spark" as SupportedModel | undefined,
			chat: undefined as SupportedModel | undefined,
		},
		/** Codex-specific overrides */
		codex: {
			default: "gpt-5.3-codex-spark" as SupportedModel | undefined,
			chat: undefined as SupportedModel | undefined,
		},
	},

	worktree: {
		initCommands: [] satisfies string[],
		env: {} satisfies Record<string, string>,
		continueOnFailure: true,
		parallel: false,
		copyPaths: [".direnv"] satisfies string[],
	},
	git: {
		pushBranchOnCreate: true,
		remote: "origin",
		branchPrefix: "az-",
		branchSlugMaxLength: 24,
		baseBranch: "main",
		pushEnabled: true,
		fetchEnabled: true,
		showLineChanges: true,
		workflowMode: "origin" as WorkflowMode,
	},
	session: {
		command: "claude",
		shell: getLoginShellSync(),
		tmuxPrefix: "C-a",
		dangerouslySkipPermissions: false,
		backgroundTasks: [] satisfies string[],
	},
	patterns: {
		waiting: [] satisfies string[],
		done: [] satisfies string[],
		error: [] satisfies string[],
	},
	stateDetection: {
		/**
		 * PTY pattern matching is disabled by default.
		 * When disabled, PTY-driven detection and metrics are inert.
		 * Native tool signals (hooks/events via TmuxSessionMonitor) remain authoritative.
		 */
		patternMatching: false,
	},
	pr: {
		enabled: true,
		autoDraft: true,
		autoMerge: false,
		aiModel: undefined as SupportedModel | undefined,
	},
	merge: {
		// No validation by default - must be explicitly configured in .azedarach.json
		validateCommands: [] satisfies string[],
		fixCommand: "",
		maxFixAttempts: 2,
		startAiSessionOnFailure: true,
	},
	notifications: {
		bell: true,
		system: false,
	},
	issueTracker: {
		local: {
			syncEnabled: false,
			backups: {
				enabled: true,
				intervalMinutes: 60,
				writeCooldownSeconds: 300,
				maxBackups: 30,
				directory: ".azedarach/backups",
			},
		},
	},
	network: {
		autoDetect: true,
		checkIntervalSeconds: 30,
		checkHost: "github.com",
	},
	devServer: {
		portPattern: "localhost:(\\d+)|127\\.0\\.0\\.1:(\\d+)",
		servers: {
			default: {
				command: "npm run dev",
				ports: { PORT: 3000 },
			},
		},
	},
	keyboard: {
		/** Home row keys for QWERTY layout - customize for Colemak, Dvorak, etc. */
		jumpLabelChars: "asdfjkl;",
	},
	sessionRecovery: {
		/**
		 * Auto-recovery is the default - crashed sessions are automatically respawned
		 * on startup using `claude --resume` to continue the conversation.
		 * Set to "manual" to show crashed sessions in UI and recover with R key.
		 */
		mode: "auto" as SessionRecoveryMode,
		/** Delay before auto-recovery starts (ms) - gives UI time to render */
		autoRecoveryDelayMs: 2000,
		/** Retry backoff base delay (ms) for transient auto-recovery failures */
		retryBaseDelayMs: 1000,
		/** Maximum retry wait (ms) for transient auto-recovery failures */
		retryMaxDelayMs: 60000,
	},
	hooks: {
		preCompact: {
			/**
			 * PreCompact hook is enabled by default.
			 * Updates the issue with session progress before context compaction,
			 * ensuring work-in-progress survives auto-compaction.
			 */
			enabled: true,
		},
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

	/** CLI tool to use for AI sessions */
	cliTool: CliTool

	/** Model configuration */
	model: {
		default: SupportedModel | undefined
		chat: SupportedModel | undefined
		claude: {
			default: SupportedModel | undefined
			chat: SupportedModel | undefined
		}
		opencode: {
			default: SupportedModel | undefined
			chat: SupportedModel | undefined
		}
		codex: {
			default: SupportedModel | undefined
			chat: SupportedModel | undefined
		}
	}

	worktree: {
		initCommands: readonly string[]
		env: Readonly<Record<string, string>>
		continueOnFailure: boolean
		parallel: boolean
		copyPaths: readonly string[]
	}
	git: {
		pushBranchOnCreate: boolean
		remote: string
		branchPrefix: string
		branchSlugMaxLength: number
		baseBranch: string
		pushEnabled: boolean
		fetchEnabled: boolean
		showLineChanges: boolean
		workflowMode: WorkflowMode
	}
	session: {
		command: string
		shell: string
		tmuxPrefix: string
		dangerouslySkipPermissions: boolean
		backgroundTasks: readonly string[]
	}
	patterns: {
		waiting: readonly string[]
		done: readonly string[]
		error: readonly string[]
	}
	stateDetection: {
		patternMatching: boolean
	}
	pr: {
		enabled: boolean
		autoDraft: boolean
		autoMerge: boolean
		aiModel: SupportedModel | undefined
	}
	merge: {
		validateCommands: readonly string[]
		fixCommand: string
		maxFixAttempts: number
		startAiSessionOnFailure: boolean
	}
	notifications: {
		bell: boolean
		system: boolean
	}
	issueTracker:
		| {
				tracker: {
					syncEnabled: boolean
				}
		  }
		| {
				legacy: {
					syncEnabled: boolean
				}
		  }
		| {
				linear: {
					syncEnabled: boolean
					command: string
					team: string | undefined
					project: string | undefined
					webhooks: {
						enabled: boolean
						transport: "sdk" | "cli"
						url: string | undefined
						port: number
						events: readonly string[]
						secret: string | undefined
					}
					syncThrottle: {
						maxPerMinute: number
						burst: number
					}
				}
		  }
		| {
				local: {
					syncEnabled: boolean
					backups: {
						enabled: boolean
						intervalMinutes: number
						writeCooldownSeconds: number
						maxBackups: number
						directory: string
					}
				}
		  }
	network: {
		autoDetect: boolean
		checkIntervalSeconds: number
		checkHost: string
	}
	devServer: {
		portPattern: string
		servers:
			| Readonly<
					Record<
						string,
						{
							readonly command: string
							readonly cwd?: string
							readonly ports?: Readonly<Record<string, number>>
						}
					>
			  >
			| undefined
	}
	keyboard: {
		jumpLabelChars: string
	}
	sessionRecovery: {
		mode: SessionRecoveryMode
		autoRecoveryDelayMs: number
		retryBaseDelayMs: number
		retryMaxDelayMs: number
	}
	hooks: {
		preCompact: {
			enabled: boolean
		}
	}
	projects: ReadonlyArray<{
		name: string
		path: string
		issueStorePath?: string
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
const mergeIssueTrackerWithDefaults = (
	issueTracker: AzedarachConfig["issueTracker"],
): ResolvedConfig["issueTracker"] => {
	const defaultLinearWebhooks: {
		readonly enabled: boolean
		readonly transport: "sdk" | "cli"
		readonly url: string | undefined
		readonly port: number
		readonly events: readonly string[]
		readonly secret: string | undefined
	} = {
		enabled: true,
		transport: "sdk",
		url: undefined,
		port: 9000,
		events: ["Issue"],
		secret: undefined,
	}
	const defaultLinearSyncThrottle: {
		readonly maxPerMinute: number
		readonly burst: number
	} = {
		maxPerMinute: 10,
		burst: 10,
	}

	if (issueTracker !== undefined) {
		if (issueTracker.tracker !== undefined) {
			return {
				tracker: {
					syncEnabled: issueTracker.tracker.syncEnabled ?? true,
				},
			}
		}

		if (issueTracker.legacy !== undefined) {
			return {
				legacy: {
					syncEnabled: issueTracker.legacy.syncEnabled ?? true,
				},
			}
		}

		if (issueTracker.linear !== undefined) {
			const configuredWebhooks = issueTracker.linear.webhooks
			const configuredSyncThrottle = issueTracker.linear.syncThrottle
			return {
				linear: {
					syncEnabled: issueTracker.linear.syncEnabled ?? true,
					command: issueTracker.linear.command ?? "linear-cli",
					team: issueTracker.linear.team,
					project: issueTracker.linear.project,
					webhooks: {
						enabled: configuredWebhooks?.enabled ?? defaultLinearWebhooks.enabled,
						transport: configuredWebhooks?.transport ?? defaultLinearWebhooks.transport,
						url: configuredWebhooks?.url ?? defaultLinearWebhooks.url,
						port: configuredWebhooks?.port ?? defaultLinearWebhooks.port,
						events: configuredWebhooks?.events ?? defaultLinearWebhooks.events,
						secret: configuredWebhooks?.secret ?? defaultLinearWebhooks.secret,
					},
					syncThrottle: {
						maxPerMinute:
							configuredSyncThrottle?.maxPerMinute ?? defaultLinearSyncThrottle.maxPerMinute,
						burst: configuredSyncThrottle?.burst ?? defaultLinearSyncThrottle.burst,
					},
				},
			}
		}

		if (issueTracker.local !== undefined) {
			const configuredBackups = issueTracker.local.backups
			return {
				local: {
					syncEnabled: issueTracker.local.syncEnabled ?? false,
					backups: {
						enabled:
							configuredBackups?.enabled ?? DEFAULT_CONFIG.issueTracker.local.backups.enabled,
						intervalMinutes:
							configuredBackups?.intervalMinutes ??
							DEFAULT_CONFIG.issueTracker.local.backups.intervalMinutes,
						writeCooldownSeconds:
							configuredBackups?.writeCooldownSeconds ??
							DEFAULT_CONFIG.issueTracker.local.backups.writeCooldownSeconds,
						maxBackups:
							configuredBackups?.maxBackups ?? DEFAULT_CONFIG.issueTracker.local.backups.maxBackups,
						directory:
							configuredBackups?.directory ?? DEFAULT_CONFIG.issueTracker.local.backups.directory,
					},
				},
			}
		}
	}

	return DEFAULT_CONFIG.issueTracker
}

export function mergeWithDefaults(config: AzedarachConfig): ResolvedConfig {
	return {
		$schema: config.$schema ?? DEFAULT_CONFIG.$schema,
		cliTool: config.cliTool ?? DEFAULT_CONFIG.cliTool,
		model: {
			default: config.model?.default ?? DEFAULT_CONFIG.model.default,
			chat: config.model?.chat ?? DEFAULT_CONFIG.model.chat,
			claude: {
				default: config.model?.claude?.default ?? DEFAULT_CONFIG.model.claude.default,
				chat: config.model?.claude?.chat ?? DEFAULT_CONFIG.model.claude.chat,
			},
			opencode: {
				default: config.model?.opencode?.default ?? DEFAULT_CONFIG.model.opencode.default,
				chat: config.model?.opencode?.chat ?? DEFAULT_CONFIG.model.opencode.chat,
			},
			codex: {
				default: config.model?.codex?.default ?? DEFAULT_CONFIG.model.codex.default,
				chat: config.model?.codex?.chat ?? DEFAULT_CONFIG.model.codex.chat,
			},
		},
		worktree: {
			initCommands: config.worktree?.initCommands ?? DEFAULT_CONFIG.worktree.initCommands,
			env: config.worktree?.env ?? DEFAULT_CONFIG.worktree.env,
			continueOnFailure:
				config.worktree?.continueOnFailure ?? DEFAULT_CONFIG.worktree.continueOnFailure,
			parallel: config.worktree?.parallel ?? DEFAULT_CONFIG.worktree.parallel,
			copyPaths: config.worktree?.copyPaths ?? DEFAULT_CONFIG.worktree.copyPaths,
		},
		session: {
			command: config.session?.command ?? DEFAULT_CONFIG.session.command,
			shell: config.session?.shell ?? DEFAULT_CONFIG.session.shell,
			tmuxPrefix: config.session?.tmuxPrefix ?? DEFAULT_CONFIG.session.tmuxPrefix,
			dangerouslySkipPermissions:
				config.session?.dangerouslySkipPermissions ??
				DEFAULT_CONFIG.session.dangerouslySkipPermissions,
			backgroundTasks: config.session?.backgroundTasks ?? DEFAULT_CONFIG.session.backgroundTasks,
		},
		git: {
			pushBranchOnCreate: config.git?.pushBranchOnCreate ?? DEFAULT_CONFIG.git.pushBranchOnCreate,
			remote: config.git?.remote ?? DEFAULT_CONFIG.git.remote,
			branchPrefix: config.git?.branchPrefix ?? DEFAULT_CONFIG.git.branchPrefix,
			branchSlugMaxLength:
				config.git?.branchSlugMaxLength ?? DEFAULT_CONFIG.git.branchSlugMaxLength,
			baseBranch: config.git?.baseBranch ?? DEFAULT_CONFIG.git.baseBranch,
			pushEnabled: config.git?.pushEnabled ?? DEFAULT_CONFIG.git.pushEnabled,
			fetchEnabled: config.git?.fetchEnabled ?? DEFAULT_CONFIG.git.fetchEnabled,
			showLineChanges: config.git?.showLineChanges ?? DEFAULT_CONFIG.git.showLineChanges,
			workflowMode: config.git?.workflowMode ?? DEFAULT_CONFIG.git.workflowMode,
		},
		patterns: {
			waiting: config.patterns?.waiting ?? DEFAULT_CONFIG.patterns.waiting,
			done: config.patterns?.done ?? DEFAULT_CONFIG.patterns.done,
			error: config.patterns?.error ?? DEFAULT_CONFIG.patterns.error,
		},
		stateDetection: {
			patternMatching:
				config.stateDetection?.patternMatching ?? DEFAULT_CONFIG.stateDetection.patternMatching,
		},
		pr: {
			enabled: config.pr?.enabled ?? DEFAULT_CONFIG.pr.enabled,
			autoDraft: config.pr?.autoDraft ?? DEFAULT_CONFIG.pr.autoDraft,
			autoMerge: config.pr?.autoMerge ?? DEFAULT_CONFIG.pr.autoMerge,
			aiModel: config.pr?.aiModel ?? DEFAULT_CONFIG.pr.aiModel,
		},
		merge: {
			validateCommands: config.merge?.validateCommands ?? DEFAULT_CONFIG.merge.validateCommands,
			fixCommand: config.merge?.fixCommand ?? DEFAULT_CONFIG.merge.fixCommand,
			maxFixAttempts: config.merge?.maxFixAttempts ?? DEFAULT_CONFIG.merge.maxFixAttempts,
			startAiSessionOnFailure:
				config.merge?.startAiSessionOnFailure ?? DEFAULT_CONFIG.merge.startAiSessionOnFailure,
		},
		notifications: {
			bell: config.notifications?.bell ?? DEFAULT_CONFIG.notifications.bell,
			system: config.notifications?.system ?? DEFAULT_CONFIG.notifications.system,
		},
		issueTracker: mergeIssueTrackerWithDefaults(config.issueTracker),
		projects: config.projects ?? DEFAULT_CONFIG.projects,
		defaultProject: config.defaultProject ?? DEFAULT_CONFIG.defaultProject,
		network: {
			autoDetect: config.network?.autoDetect ?? DEFAULT_CONFIG.network.autoDetect,
			checkIntervalSeconds:
				config.network?.checkIntervalSeconds ?? DEFAULT_CONFIG.network.checkIntervalSeconds,
			checkHost: config.network?.checkHost ?? DEFAULT_CONFIG.network.checkHost,
		},
		devServer: {
			portPattern: config.devServer?.portPattern ?? DEFAULT_CONFIG.devServer.portPattern,
			servers: config.devServer?.servers ?? DEFAULT_CONFIG.devServer.servers,
		},
		keyboard: {
			jumpLabelChars: config.keyboard?.jumpLabelChars ?? DEFAULT_CONFIG.keyboard.jumpLabelChars,
		},
		sessionRecovery: {
			mode: config.sessionRecovery?.mode ?? DEFAULT_CONFIG.sessionRecovery.mode,
			autoRecoveryDelayMs:
				config.sessionRecovery?.autoRecoveryDelayMs ??
				DEFAULT_CONFIG.sessionRecovery.autoRecoveryDelayMs,
			retryBaseDelayMs:
				config.sessionRecovery?.retryBaseDelayMs ?? DEFAULT_CONFIG.sessionRecovery.retryBaseDelayMs,
			retryMaxDelayMs:
				config.sessionRecovery?.retryMaxDelayMs ?? DEFAULT_CONFIG.sessionRecovery.retryMaxDelayMs,
		},
		hooks: {
			preCompact: {
				enabled: config.hooks?.preCompact?.enabled ?? DEFAULT_CONFIG.hooks.preCompact.enabled,
			},
		},
	}
}
