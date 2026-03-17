/**
 * CLI Definition for Azedarach
 *
 * Uses @effect/cli for type-safe command parsing and validation.
 * Provides commands for managing Claude Code sessions via TUI and direct control.
 *
 * ARCHITECTURE NOTE:
 * - CLI handlers use `yield* ServiceName` to get dependencies
 * - Layer is provided ONCE at the top level in `run()`
 * - Handlers should NEVER use `Effect.provide` internally
 */

import { Args, Command, Options } from "@effect/cli"
import { Otlp } from "@effect/opentelemetry"
import {
	FetchHttpClient,
	FileSystem,
	Path,
	Command as PlatformCommand,
	PlatformLogger,
} from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import {
	Console,
	Data,
	DateTime,
	Duration,
	Effect,
	Layer,
	Logger,
	LogLevel,
	Option,
	Schema,
	SubscriptionRef,
} from "effect"
// biome-ignore lint/correctness/useImportExtensions: <stupid biome>
import packageJson from "../../package.json" with { type: "json" }
import { AppConfig, AppConfigConfig } from "../config/AppConfig.js"
import {
	type AzedarachConfig,
	AzedarachConfigJsonSchema,
	AzedarachConfigSchema,
} from "../config/schema.js"
import { AttachmentService } from "../core/AttachmentService.js"
import { BackendDaemonControlService } from "../core/BackendDaemonControlService.js"
import { BackendSyncDaemonService } from "../core/BackendSyncDaemonService.js"
import {
	resolveDaemonIntervalMsFromEnv,
	resolveDaemonOperationsPolicy,
} from "../core/DaemonOperationsPolicy.js"
import { deepMerge, generateHookConfig } from "../core/hooks.js"
import { ImageAttachmentService } from "../core/ImageAttachmentService.js"
import { IssueEditorService } from "../core/IssueEditorService.js"
import {
	type ImplementationRecord,
	type ImplementationRegistry,
	IssueTrackerClient,
	resolveConfiguredIssueBackend,
	type Issue as TrackedIssue,
} from "../core/IssueTrackerClient.js"
import { PlanningService } from "../core/PlanningService.js"
import { PRWorkflow } from "../core/PRWorkflow.js"
import { PTYMonitor } from "../core/PTYMonitor.js"
import {
	getIssueSessionName,
	issueIdsEqualForLookup,
	parseIssueSessionName,
} from "../core/paths.js"
import { SessionManager } from "../core/SessionManager.js"
import { SpecService } from "../core/SpecService.js"
import type { SpecLinkFulfillmentStatus, SpecRequirementLookupSelector } from "../core/specTypes.js"
import { getProjectStoragePaths } from "../core/storagePaths.js"
import { TemplateService } from "../core/TemplateService.js"
import { TerminalService } from "../core/TerminalService.js"
import { TmuxService } from "../core/TmuxService.js"
import type { TmuxStatus } from "../core/TmuxSessionMonitor.js"
import { TmuxSessionMonitor } from "../core/TmuxSessionMonitor.js"
import { VCService } from "../core/VCService.js"
import type { DaemonRpcClientApi } from "../rpc/DaemonRpcClient.js"
import type {
	DaemonEventStreamEntry,
	DaemonEventStreamResult,
	DaemonSessionMutationResult,
	DaemonSessionSnapshotResult,
} from "../rpc/DaemonRpcSchemas.js"
import { BoardService } from "../services/BoardService.js"
import { ClockService } from "../services/ClockService.js"
import { CommandQueueService } from "../services/CommandQueueService.js"
import { DevServerService } from "../services/DevServerService.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { DiffService } from "../services/DiffService.js"
import { EditorService } from "../services/EditorService.js"
import { KeyboardService } from "../services/KeyboardService.js"
import { MutationQueue } from "../services/MutationQueue.js"
import { NavigationService } from "../services/NavigationService.js"
import { NetworkService } from "../services/NetworkService.js"
import { OfflineService } from "../services/OfflineService.js"
import { OverlayService } from "../services/OverlayService.js"
import { ProjectService, resolveConfigBasePath } from "../services/ProjectService.js"
import { ProjectStateService } from "../services/ProjectStateService.js"
import { SessionService } from "../services/SessionService.js"
import { SettingsService } from "../services/SettingsService.js"
import { ToastService } from "../services/ToastService.js"
import { ViewService } from "../services/ViewService.js"
import { launchTUI } from "../ui/launch.js"
import {
	hasVerboseFlag,
	normalizeCliAliases,
	normalizeIssueJsonFlagOrder,
	normalizeIssueOptionOrder,
	parseConfigPathFromArgv,
	resolveCliExecutionMode,
} from "./argv-normalization.js"
import {
	bootstrapDaemonRpcClient,
	formatDaemonRpcClientFailure,
	isRetryableRpcClientError,
} from "./daemonClientBootstrap.js"
import { devCommand } from "./dev-server.js"
import { resolveCliIssueId } from "./issueIdResolver.js"
import { OPENCODE_AZ_PLUGIN_FILENAME, OPENCODE_AZ_PLUGIN_SOURCE } from "./opencodePluginSource.js"
import {
	buildPrimeOutput,
	compactSingleLineText,
	deriveWaitingAttentionPlan,
	formatIssueDetailSections,
	formatIssueSummaryLine,
	formatSpecRequirementReference,
} from "./output-formatting.js"
import { ensureProjectAzedarachGitignore } from "./projectGitignore.js"
import {
	applyNotifyStatusToTmux,
	isValidHookEvent,
	mapHookEventToTmuxStatus,
	VALID_HOOK_EVENTS,
} from "./tmux-notify.js"

// ============================================================================
// CLI Layers
// ============================================================================

/**
 * File logger used by both TUI and command-mode invocations.
 */
const fileLogger = Logger.logfmtLogger.pipe(PlatformLogger.toFile("az-cli.log", { flag: "a" }))

const telemetryLayer =
	process.env.AZ_OTEL === "1"
		? Otlp.layerJson({
				baseUrl: "http://localhost:4318",
				metricsExportInterval: Duration.seconds(10),
				resource: {
					serviceName: "azedarach",
				},
			}).pipe(Layer.provide(FetchHttpClient.layer))
		: Layer.empty

const CLI_VERSION = packageJson.version

const buildAppConfigLayer = (configPath: string | null) => {
	if (configPath === null) {
		return AppConfig.Default
	}

	return AppConfig.Default.pipe(
		Layer.provide(
			Layer.succeed(
				AppConfigConfig,
				AppConfigConfig.make({
					configPath,
					projectPath: process.cwd(),
				}),
			),
		),
	)
}

/**
 * Full CLI layer used for TUI launch and commands that still depend on
 * TUI-coupled services (for now, `az dev`).
 */
const createFullCliLayer = (configPath: string | null) =>
	Layer.mergeAll(
		BackendDaemonControlService.Default,
		BackendSyncDaemonService.Default,
		MutationQueue.Default,
		SessionService.Default,
		AttachmentService.Default,
		OverlayService.Default,
		ImageAttachmentService.Default,
		BoardService.Default,
		ClockService.Default,
		TmuxService.Default,
		IssueEditorService.Default,
		PRWorkflow.Default,
		TerminalService.Default,
		EditorService.Default,
		KeyboardService.Default,
		ToastService.Default,
		NavigationService.Default,
		SessionManager.Default,
		IssueTrackerClient.Default,
		buildAppConfigLayer(configPath),
		VCService.Default,
		ViewService.Default,
		TmuxSessionMonitor.Default,
		CommandQueueService.Default,
		PTYMonitor.Default,
		DiagnosticsService.Default,
		ProjectService.Default,
		ProjectStateService.Default,
		SettingsService.Default,
		TemplateService.Default,
		NetworkService.Default,
		OfflineService.Default,
		DevServerService.Default,
		DiffService.Default,
		PlanningService.Default,
		SpecService.Default,
	).pipe(
		Layer.provide(Logger.replaceScoped(Logger.defaultLogger, fileLogger)),
		Layer.provideMerge(telemetryLayer),
		Layer.provideMerge(BunContext.layer),
	)

/**
 * Lean command layer for non-TUI CLI commands.
 * Intentionally excludes board/navigation/overlay/view services so command
 * invocations don't start board refresh polling or webhook listeners.
 */
const createCommandCliLayer = (configPath: string | null) =>
	Layer.mergeAll(
		buildAppConfigLayer(configPath),
		ProjectService.Default,
		IssueTrackerClient.Default,
		SessionManager.Default,
		SpecService.Default,
	).pipe(
		Layer.provide(Logger.replaceScoped(Logger.defaultLogger, fileLogger)),
		Layer.provideMerge(telemetryLayer),
		Layer.provideMerge(BunContext.layer),
	)

const fullCliLayer = createFullCliLayer(null)
const commandCliLayer = createCommandCliLayer(null)

// ============================================================================
// Shared Options
// ============================================================================

/**
 * Verbose logging flag
 */
const verboseOption = Options.boolean("verbose").pipe(
	Options.withAlias("v"),
	Options.withDescription("Enable verbose logging"),
)

const noDaemonOption = Options.boolean("no-daemon").pipe(
	Options.withDescription("Disable automatic daemon startup for this command"),
)

/**
 * Config file path option
 */
const configOption = Options.file("config").pipe(
	Options.withAlias("c"),
	Options.optional,
	Options.withDescription("Path to config file (default: .azedarach/config.json)"),
)

/**
 * Project directory option for commands that need explicit cwd without
 * conflicting with trailing options like --json.
 */
const projectDirOption = Options.directory("project-dir").pipe(
	Options.optional,
	Options.withDescription("Project directory (default: current directory)"),
)

/**
 * Project directory argument
 */
const projectDirArg = Args.directory().pipe(
	Args.optional,
	Args.withDescription("Project directory (default: current directory)"),
)

// ============================================================================
// Validation Helpers
// ============================================================================

/**
 * Validate that the issue tracker store exists when backend requires .azedarach.
 */
const validateIssueTrackerStore = (projectDir: string) =>
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const resolvedConfig = yield* SubscriptionRef.get(appConfig.config)
		if ("linear" in resolvedConfig.issueTracker || "local" in resolvedConfig.issueTracker) {
			return
		}

		const fs = yield* FileSystem.FileSystem
		const path = yield* Path.Path
		const issueStoreDir = path.join(projectDir, ".azedarach")

		const exists = yield* fs.exists(issueStoreDir)
		if (!exists) {
			return yield* Effect.fail(
				new Error(
					`No .azedarach directory found in ${projectDir}. Initialize issue tracking for this project, then retry your \`az issue\` command.`,
				),
			)
		}
	})

/**
 * Validate that spec workflows are enabled for the project before using az spec surfaces.
 */
const ensureSpecEnabled = (projectDir: string) =>
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const specConfig = yield* appConfig.getSpecConfigForProjectPath(projectDir)
		if (specConfig.enabled) {
			return
		}

		return yield* Effect.fail(
			new Error(
				"Spec workflows are disabled for this project. Run `az config set spec.enabled true` or set `spec.enabled` to true in `.azedarach.json` to use `az spec` and spec-aware guidance.",
			),
		)
	})

const configJsonSchemaString = `${JSON.stringify(AzedarachConfigJsonSchema, null, 2)}\n`

const resolveWritableConfigPath = (explicitProjectDir: string | undefined) =>
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const loadedConfigPath = yield* SubscriptionRef.get(appConfig.loadedConfigPath)
		if (loadedConfigPath !== null && !loadedConfigPath.endsWith("package.json")) {
			return loadedConfigPath
		}

		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const projectService = yield* ProjectService
		const projectPath =
			explicitProjectDir ?? (yield* projectService.getCurrentPath()) ?? process.cwd()
		const cwdPath = explicitProjectDir ?? process.cwd()
		const cwdConfigPath = pathService.join(cwdPath, ".azedarach.json")
		const cwdHasConfig = yield* fs
			.exists(cwdConfigPath)
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(false)),
					),
				),
			)
		const configBasePath = resolveConfigBasePath({
			cwdPath,
			projectPath,
			pathOps: pathService,
			cwdHasConfig,
		})
		return pathService.join(configBasePath, ".azedarach.json")
	})

const loadWritableConfig = (configPath: string) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const exists = yield* fs
			.exists(configPath)
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(false)),
					),
				),
			)
		if (!exists) {
			return yield* Schema.decodeUnknown(AzedarachConfigSchema)({}).pipe(
				Effect.mapError(
					(error) => new Error(`Failed to create default config snapshot: ${String(error)}`),
				),
			)
		}

		const content = yield* fs
			.readFileString(configPath)
			.pipe(
				Effect.mapError(
					(error) => new Error(`Failed to read config file ${configPath}: ${String(error)}`),
				),
			)

		return yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(content).pipe(
			Effect.mapError(
				(error) => new Error(`Config parse/validation failed for ${configPath}: ${String(error)}`),
			),
		)
	})

const saveWritableConfig = (configPath: string, config: AzedarachConfig) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const json = yield* Schema.encode(Schema.parseJson(AzedarachConfigSchema))(config).pipe(
			Effect.mapError((error) => new Error(`Failed to encode config: ${String(error)}`)),
		)
		yield* fs
			.writeFileString(configPath, json)
			.pipe(
				Effect.mapError(
					(error) => new Error(`Failed to write config file ${configPath}: ${String(error)}`),
				),
			)
		const schemaPath = pathService.join(pathService.dirname(configPath), ".azedarach.schema.json")
		yield* fs
			.writeFileString(schemaPath, configJsonSchemaString)
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(
						`Failed to write config JSON schema at ${schemaPath}: ${String(error)}`,
					),
				),
			)
	})

const parseBooleanConfigValue = (value: string): boolean | undefined => {
	const normalized = value.trim().toLowerCase()
	switch (normalized) {
		case "true":
		case "1":
		case "yes":
		case "on":
			return true
		case "false":
		case "0":
		case "no":
		case "off":
			return false
		default:
			return undefined
	}
}

const setConfigValue = (
	config: AzedarachConfig,
	key: string,
	value: string,
): Effect.Effect<
	{ readonly nextConfig: AzedarachConfig; readonly renderedValue: string },
	Error
> => {
	if (key === "spec.enabled") {
		const parsed = parseBooleanConfigValue(value)
		if (parsed === undefined) {
			return Effect.fail(
				new Error(
					`Invalid boolean value '${value}' for spec.enabled. Use true/false, on/off, yes/no, or 1/0.`,
				),
			)
		}
		return Effect.succeed({
			nextConfig: {
				...config,
				spec: {
					...config.spec,
					enabled: parsed,
				},
			},
			renderedValue: String(parsed),
		})
	}

	return Effect.fail(new Error(`Unsupported config key '${key}'. Supported keys: spec.enabled`))
}

// ============================================================================
// Command Handlers
// ============================================================================

/**
 * Default command - Launch TUI
 */
const defaultHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly noDaemon: boolean
	readonly config: Option.Option<string>
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureDaemonAutoStartForCliCommand({
			command: "tui-default",
			projectPath: cwd,
			noDaemonFlag: args.noDaemon,
			verbose: args.verbose,
		})

		if (args.verbose) {
			yield* Console.log("Azedarach - TUI Kanban for Claude orchestration")
			yield* Console.log(`Project: ${cwd}`)
			yield* Console.log("Verbose mode enabled")
		}

		if (Option.isSome(args.config)) {
			yield* Console.log(`Using config: ${args.config.value}`)
		}

		// Validate issue tracker store
		yield* validateIssueTrackerStore(cwd)

		// Launch TUI
		yield* Effect.promise(() => launchTUI())
	})

/**
 * Start a new Claude session for an issue
 */
type StartSessionRuntimeMode = "daemon-rpc" | "session-manager-fallback"

const mapDaemonSessionMutationToCliSession = (
	result: DaemonSessionMutationResult,
): {
	readonly worktreePath: string
	readonly tmuxSessionName: string
} => ({
	worktreePath: result.session.worktreePath,
	tmuxSessionName: result.session.tmuxSessionName,
})

export const resolveStartSessionRuntimeMode = (params: {
	readonly noDaemonFlag: boolean
	readonly env: Readonly<Record<string, string | undefined>>
}): {
	readonly mode: StartSessionRuntimeMode
	readonly decision:
		| "enabled-by-default"
		| "disabled-by-cli-flag"
		| "disabled-by-env"
		| "enabled-by-env"
		| "ignored-invalid-env"
} => {
	const policy = resolveDaemonOperationsPolicy({
		command: "tui-default",
		noDaemonFlag: params.noDaemonFlag,
		env: params.env,
	})
	return {
		mode: policy.autoDaemonize ? "daemon-rpc" : "session-manager-fallback",
		decision: policy.decision,
	}
}

const startHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly noDaemon: boolean
	readonly config: Option.Option<string>
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)
		const sessionRuntime = resolveStartSessionRuntimeMode({
			noDaemonFlag: args.noDaemon,
			env: process.env,
		})

		yield* Console.log(`Starting Claude session for issue: ${issueId}`)
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
			if (Option.isSome(args.config)) {
				yield* Console.log(`Using config: ${args.config.value}`)
			}
		}

		// Validate issue tracker store
		yield* validateIssueTrackerStore(cwd)

		const session =
			sessionRuntime.mode === "daemon-rpc"
				? yield* Effect.gen(function* () {
						const bootstrap = yield* bootstrapDaemonRpcClient({
							autoStart: true,
						})
						if (bootstrap.client.sessionStart === undefined) {
							return yield* Effect.fail(
								new Error(
									"Connected daemon does not support sessionStart RPC yet. Update daemon/runtime or rerun with --no-daemon.",
								),
							)
						}
						const mutation = yield* bootstrap.client
							.sessionStart({
								issueId,
								projectPath: cwd,
							})
							.pipe(
								Effect.mapError((error) =>
									formatDaemonRpcClientFailure({
										operation: "sessionStart",
										socketUrl: bootstrap.socketUrl,
										error,
									}),
								),
							)
						return mapDaemonSessionMutationToCliSession(mutation)
					})
				: yield* Effect.gen(function* () {
						if (args.verbose) {
							yield* Console.log(
								`Session start daemon RPC disabled (${sessionRuntime.decision}); using direct runtime fallback.`,
							)
						}
						const sessionManager = yield* SessionManager
						return yield* sessionManager.start({
							issueId,
							projectPath: cwd,
						})
					})

		// Claim the issue with session assignee
		const issueTrackerClient = yield* IssueTrackerClient
		yield* issueTrackerClient
			.update(
				issueId,
				{
					status: "in_progress",
					assignee: session.tmuxSessionName,
				},
				cwd,
			)
			.pipe(
				Effect.tap(() => {
					if (args.verbose) {
						return Console.log(`Claimed issue ${issueId} with assignee ${session.tmuxSessionName}`)
					}
					return Effect.void
				}),
				Effect.catchAll((e) => {
					// Non-fatal: log warning but continue
					return Effect.logWarning(e).pipe(
						Effect.zipRight(Console.log(`Warning: Could not claim issue: ${e}`)),
					)
				}),
			)

		yield* Console.log(`Session started successfully!`)
		yield* Console.log(`  Worktree: ${session.worktreePath}`)
		yield* Console.log(`  tmux session: ${session.tmuxSessionName}`)
		yield* Console.log(`  Issue claimed: ${issueId} (assignee: ${session.tmuxSessionName})`)
		yield* Console.log(``)
		yield* Console.log(`To attach: az attach ${issueId}`)
		yield* Console.log(`Or directly: tmux attach-session -t ${session.tmuxSessionName}`)
	})

/**
 * Attach to an existing Claude session
 */
const attachHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)

		if (args.verbose) {
			yield* Console.log(`Attaching to session for issue: ${issueId}`)
			yield* Console.log(`Project: ${cwd}`)
		}

		const sessionName = yield* findSessionByIssueId(issueId)
		if (!sessionName) {
			yield* Console.error(`No session found for ${issueId}`)
			yield* Console.log(`Start a new session with: az start ${issueId}`)
			return yield* Effect.fail(new Error(`Session not found: ${issueId}`))
		}

		// Attach to tmux session (this replaces current process)
		const attachCommand = PlatformCommand.make("tmux", "attach-session", "-t", sessionName)
		yield* PlatformCommand.exitCode(attachCommand)
	})

/**
 * Pause a running Claude session
 */
const pauseHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)

		yield* Console.log(`Pausing session for issue: ${issueId}`)
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		// Validate issue tracker store
		yield* validateIssueTrackerStore(cwd)

		// TODO: Implement session pause
		yield* Console.log("[Stub] Sending Ctrl+C to session...")
		yield* Console.log("[Stub] Session paused. Use 'az attach' to resume.")
	})

/**
 * Kill a running Claude session
 */
const killHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)

		yield* Console.log(`Killing session for issue: ${issueId}`)

		const sessionName = yield* findSessionByIssueId(issueId)
		if (!sessionName) {
			yield* Console.log(`No session found for ${issueId}`)
			return
		}

		// Kill the tmux session
		const killCommand = PlatformCommand.make("tmux", "kill-session", "-t", sessionName)
		yield* PlatformCommand.exitCode(killCommand).pipe(
			Effect.catchAll((e) => {
				return Effect.logError(e).pipe(
					Effect.zipRight(Console.error(`Failed to kill session: ${e}`).pipe(Effect.as(1))),
				)
			}),
		)

		yield* Console.log(`Session ${issueId} killed.`)

		if (args.verbose) {
			yield* Console.log(`Project: ${cwd}`)
			yield* Console.log("Note: Worktree was not removed. Use git worktree remove if needed.")
		}
	})

/**
 * Show status of all sessions
 */
const statusHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		yield* Console.log("Session Status")
		yield* Console.log("")

		// List tmux sessions that match our naming pattern
		const listCommand = PlatformCommand.make(
			"tmux",
			"list-sessions",
			"-F",
			"#{session_name}|#{session_created}|#{?session_attached,attached,detached}|#{@az_status}",
		)

		const output = yield* PlatformCommand.string(listCommand).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed("")),
				),
			),
		)

		if (!output.trim()) {
			yield* Console.log("No active sessions.")
			return
		}

		const lines = output.trim().split("\n")
		let sessionCount = 0

		for (const line of lines) {
			const [name, _created, attached, status] = line.split("|")
			if (!name) {
				continue
			}

			const parsed = parseIssueSessionName(name, process.cwd())
			if (parsed?.type === "issue") {
				sessionCount++
				const statusDisplay = status || "unknown"
				const attachedDisplay = attached === "attached" ? " (attached)" : ""
				yield* Console.log(`  ${parsed.issueId} - ${statusDisplay.toUpperCase()}${attachedDisplay}`)

				if (args.verbose) {
					if (name !== parsed.issueId) {
						yield* Console.log(`    Session: ${name}`)
					}

					// Get worktree path if available
					const wtCommand = PlatformCommand.make(
						"tmux",
						"display-message",
						"-t",
						name,
						"-p",
						"#{pane_current_path}",
					)
					const wtPath = yield* PlatformCommand.string(wtCommand).pipe(
						Effect.map((s) => s.trim()),
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed("")),
							),
						),
					)
					if (wtPath) {
						yield* Console.log(`    Path: ${wtPath}`)
					}
				}
			}
		}

		if (sessionCount === 0) {
			yield* Console.log("No active sessions.")
		} else {
			yield* Console.log("")
			yield* Console.log(`${sessionCount} session(s) active`)
		}
	})

export const parseGitWorktreeListPaths = (porcelainOutput: string): readonly string[] => {
	const uniquePaths = new Set<string>()
	for (const rawLine of porcelainOutput.split("\n")) {
		if (!rawLine.startsWith("worktree ")) continue
		const path = rawLine.slice("worktree ".length).trim()
		if (path.length === 0) continue
		uniquePaths.add(path)
	}
	return [...uniquePaths]
}

const listSyncTargetPaths = (cwd: string, includeAllWorktrees: boolean) =>
	Effect.gen(function* () {
		if (!includeAllWorktrees) {
			return [cwd] as const
		}

		const output = yield* PlatformCommand.string(
			PlatformCommand.make("git", "-C", cwd, "worktree", "list", "--porcelain"),
		).pipe(Effect.mapError((error) => new Error(`Failed to list git worktrees: ${String(error)}`)))
		const parsedPaths = parseGitWorktreeListPaths(output)
		return parsedPaths.length > 0 ? parsedPaths : [cwd]
	})

const hasStringMessage = (value: unknown): value is { readonly message: string } =>
	typeof value === "object" &&
	value !== null &&
	"message" in value &&
	typeof value.message === "string"

const getSyncFailureMessage = (error: unknown): string => {
	if (error instanceof Error) {
		return error.message
	}
	if (hasStringMessage(error)) {
		return error.message
	}
	return String(error)
}

const ensureDaemonAutoStartForCliCommand = (params: {
	readonly command: "tui-default" | "sync"
	readonly projectPath: string
	readonly noDaemonFlag: boolean
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const policy = resolveDaemonOperationsPolicy({
			command: params.command,
			noDaemonFlag: params.noDaemonFlag,
			env: process.env,
		})
		if (!policy.autoDaemonize) {
			if (params.verbose) {
				yield* Console.log(`Auto-daemonize disabled (${policy.decision}).`)
			}
			return
		}

		const daemonizeEffect = Effect.gen(function* () {
			const { intervalMs, warning } = resolveDaemonIntervalMsFromEnv(process.env)
			if (warning !== undefined) {
				yield* Console.error(`Warning: ${warning}`)
			}

			const bootstrap = yield* bootstrapDaemonRpcClient({
				autoStart: true,
			})
			const currentStatus = yield* bootstrap.client.status().pipe(
				Effect.mapError((error) =>
					formatDaemonRpcClientFailure({
						operation: "status",
						socketUrl: bootstrap.socketUrl,
						error,
					}),
				),
			)
			const alreadyRunningForPath =
				currentStatus.sync.state === "running" &&
				currentStatus.sync.projectPath === params.projectPath
			if (alreadyRunningForPath) {
				if (params.verbose) {
					yield* Console.log(
						`Auto-daemonize: reusing running daemon for ${params.projectPath} (state=${currentStatus.sync.state}).`,
					)
				}
				return
			}

			yield* bootstrap.client
				.restart({
					projectPath: params.projectPath,
					...(intervalMs === undefined ? {} : { intervalMs }),
				})
				.pipe(
					Effect.mapError((error) =>
						formatDaemonRpcClientFailure({
							operation: "restart",
							socketUrl: bootstrap.socketUrl,
							error,
						}),
					),
				)
			if (params.verbose) {
				yield* Console.log(`Auto-daemonize: daemon ready for ${params.projectPath}.`)
			}
		})

		yield* Effect.fork(
			daemonizeEffect.pipe(
				Effect.catchAll((error) =>
					Console.error(`Warning: auto-daemonize failed (${error.message}); continuing startup.`),
				),
			),
		)
		if (params.verbose) {
			yield* Console.log("Auto-daemonize: daemon preparation running in background.")
		}
	})

const syncLinearAfterIssueMutation = (params: {
	readonly issueTrackerClient: IssueTrackerClient
	readonly explicitProjectDir: string | undefined
	readonly resolverCwd: string
	readonly commandLabel: string
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const projectPath = params.explicitProjectDir ?? params.resolverCwd
		const syncConfig = yield* appConfig
			.getIssueTrackerSyncConfigForProjectPath(projectPath)
			.pipe(
				Effect.mapError(
					(error) =>
						new Error(
							`Failed to load issue tracker sync config for post-mutation sync (${projectPath}): ${error.message}`,
						),
				),
			)

		const backend = resolveConfiguredIssueBackend(syncConfig.issueTracker)
		if (backend !== "linear" || !syncConfig.syncEnabled) {
			return
		}

		const syncResult = yield* params.issueTrackerClient
			.sync(params.explicitProjectDir, { hydrateRemote: false })
			.pipe(
				Effect.mapError(
					(error) =>
						new Error(
							`Post-mutation linear sync failed after ${params.commandLabel}: ${getSyncFailureMessage(error)}`,
						),
				),
			)

		if (params.verbose) {
			yield* Console.error(
				`post_sync pushed=${syncResult.pushed} pulled=${syncResult.pulled} backend=${backend}`,
			)
		}
	})

/**
 * Sync issue tracker state in current or all worktrees
 */
const syncHandler = (args: {
	readonly all: boolean
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly noDaemon: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueTrackerClient = yield* IssueTrackerClient
		yield* ensureDaemonAutoStartForCliCommand({
			command: "sync",
			projectPath: cwd,
			noDaemonFlag: args.noDaemon,
			verbose: args.verbose,
		})

		yield* Console.log("Syncing issue tracker state...")
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		const targetPaths = yield* listSyncTargetPaths(cwd, args.all)
		if (args.all) {
			yield* Console.log(`Targets: ${targetPaths.length} worktree(s)`)
		}

		let totalPushed = 0
		let totalPulled = 0
		let syncedCount = 0
		const failures: Array<{ readonly path: string; readonly message: string }> = []

		for (const targetPath of targetPaths) {
			if (args.verbose || args.all) {
				yield* Console.log(`Syncing: ${targetPath}`)
			}

			const result = yield* validateIssueTrackerStore(targetPath).pipe(
				Effect.zipRight(issueTrackerClient.sync(targetPath)),
				Effect.either,
			)
			if (result._tag === "Left") {
				const message = getSyncFailureMessage(result.left)
				failures.push({ path: targetPath, message })
				yield* Console.error(`  Failed: ${message}`)
				continue
			}

			syncedCount += 1
			totalPushed += result.right.pushed
			totalPulled += result.right.pulled
			yield* Console.log(`  Pushed: ${result.right.pushed}, Pulled: ${result.right.pulled}`)
		}

		yield* Console.log("")
		yield* Console.log(
			`Sync summary: targets=${targetPaths.length}, succeeded=${syncedCount}, failed=${failures.length}, pushed=${totalPushed}, pulled=${totalPulled}`,
		)

		if (failures.length > 0) {
			for (const failure of failures) {
				yield* Console.error(`  ${failure.path}: ${failure.message}`)
			}
			return yield* Effect.fail(
				new Error(
					`Sync failed for ${failures.length} target(s). Successful targets: ${syncedCount}/${targetPaths.length}.`,
				),
			)
		}
	})

const daemonSyncHandler = (args: {
	readonly intervalMs: Option.Option<number>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const intervalMs = Option.getOrUndefined(args.intervalMs)

		yield* validateIssueTrackerStore(cwd)
		const bootstrap = yield* bootstrapDaemonRpcClient({
			autoStart: daemonCommandShouldAutoStart("sync"),
		})
		const status = yield* bootstrap.client
			.restart({
				projectPath: cwd,
				...(intervalMs === undefined ? {} : { intervalMs }),
			})
			.pipe(
				Effect.mapError((error) =>
					formatDaemonRpcClientFailure({
						operation: "restart",
						socketUrl: bootstrap.socketUrl,
						error,
					}),
				),
			)

		yield* Console.log(
			`Headless backend sync daemon started for ${cwd}${intervalMs === undefined ? "" : ` (interval=${intervalMs}ms)`}`,
		)
		yield* Console.log(
			formatDaemonControlStatusLine({
				mode: "restart",
				status,
			}),
		)
		if (args.verbose) {
			yield* Console.log(JSON.stringify(status, null, 2))
		}
	})

const formatDaemonControlStatusLine = (params: {
	readonly mode: "status" | "stop" | "restart" | "health"
	readonly status: {
		readonly runtime: {
			readonly runtimePhase: string
			readonly lifecycleGeneration: number
			readonly revision: number
		}
		readonly sync: {
			readonly state: string
			readonly generation: number
			readonly projectPath: string | null
			readonly intervalMs: number | null
		}
	}
}): string =>
	`daemon ${params.mode}: sync=${params.status.sync.state} runtime=${params.status.runtime.runtimePhase} generation=${params.status.sync.generation} projectPath=${params.status.sync.projectPath ?? "<none>"} intervalMs=${params.status.sync.intervalMs ?? "<none>"} revision=${params.status.runtime.revision} lifecycleGeneration=${params.status.runtime.lifecycleGeneration}`

export type DaemonSessionSnapshotSummary = {
	readonly capturedAtMs: number
	readonly totalSessions: number
	readonly stateCounts: Readonly<Record<string, number>>
}

const summarizeDaemonSessionSnapshot = (
	snapshot: DaemonSessionSnapshotResult,
): DaemonSessionSnapshotSummary => {
	const stateCounts = snapshot.sessions.reduce<Record<string, number>>((counts, session) => {
		counts[session.state] = (counts[session.state] ?? 0) + 1
		return counts
	}, {})
	return {
		capturedAtMs: snapshot.capturedAtMs,
		totalSessions: snapshot.sessions.length,
		stateCounts,
	}
}

const formatDaemonSessionSnapshotSummaryLine = (summary: DaemonSessionSnapshotSummary): string => {
	const counts = Object.entries(summary.stateCounts)
		.sort((left, right) => left[0].localeCompare(right[0]))
		.map(([state, count]) => `${state}=${count}`)
		.join(" ")
	return `daemon sessions: total=${summary.totalSessions} capturedAtMs=${summary.capturedAtMs}${counts.length === 0 ? "" : ` ${counts}`}`
}

export const getDaemonSessionSnapshotSummary = (params: {
	readonly client: Pick<DaemonRpcClientApi, "sessionSnapshot">
	readonly socketUrl: string
	readonly projectPath: string | undefined
}): Effect.Effect<Option.Option<DaemonSessionSnapshotSummary>, Error> => {
	if (params.client.sessionSnapshot === undefined) {
		return Effect.succeed(Option.none())
	}
	return params.client
		.sessionSnapshot(
			params.projectPath === undefined ? undefined : { projectPath: params.projectPath },
		)
		.pipe(
			Effect.map((snapshot) => Option.some(summarizeDaemonSessionSnapshot(snapshot))),
			Effect.mapError((error) =>
				formatDaemonRpcClientFailure({
					operation: "sessionSnapshot",
					socketUrl: params.socketUrl,
					error,
				}),
			),
		)
}

export const daemonCommandShouldAutoStart = (
	command: "sync" | "status" | "health" | "stop" | "restart" | "logs",
): boolean => command !== "stop"

const DAEMON_CONTROL_RPC_TIMEOUT = Duration.seconds(5)
const DAEMON_CONTROL_RPC_TIMEOUT_MS = 5000

class DaemonControlTimeoutError extends Data.TaggedError("DaemonControlTimeoutError")<{
	readonly operation: "status" | "health" | "stop" | "restart"
	readonly timeoutMs: number
}> {}

const withDaemonControlTimeout = <A, E, R>(
	operation: "status" | "health" | "stop" | "restart",
	effect: Effect.Effect<A, E, R>,
): Effect.Effect<A, E | DaemonControlTimeoutError, R> =>
	effect.pipe(
		Effect.disconnect,
		Effect.timeoutFail({
			duration: DAEMON_CONTROL_RPC_TIMEOUT,
			onTimeout: () =>
				new DaemonControlTimeoutError({
					operation,
					timeoutMs: DAEMON_CONTROL_RPC_TIMEOUT_MS,
				}),
		}),
	)

const formatDaemonEventStreamEntryLine = (entry: DaemonEventStreamEntry): string => {
	switch (entry.event._tag) {
		case "DaemonEventStreamSessionSnapshotEvent":
			return `cursor=${entry.cursor} session_snapshot sessions=${entry.event.sessions.length} capturedAtMs=${entry.event.capturedAtMs}`
		case "DaemonEventStreamRuntimeSnapshotEvent":
			return `cursor=${entry.cursor} runtime_snapshot phase=${entry.event.runtime.runtimePhase} revision=${entry.event.runtime.revision}`
	}
}

const formatDaemonEventStreamBatchSummaryLine = (batch: DaemonEventStreamResult): string =>
	`daemon stream batch: events=${batch.events.length} nextCursor=${batch.nextCursor} polledAtMs=${batch.polledAtMs}`

export const consumeDaemonStatusStreamBatches = (params: {
	readonly client: Pick<DaemonRpcClientApi, "eventStream">
	readonly socketUrl: string
	readonly clientId: string
	readonly projectPath: string | undefined
	readonly initialCursor: number | undefined
	readonly batchSize: number
	readonly waitMs: number
	readonly watch: boolean
	readonly maxBatches: number | undefined
	readonly reconnectDelayMs: number
	readonly onBatch: (batch: DaemonEventStreamResult) => Effect.Effect<void>
}): Effect.Effect<number | undefined, Error> =>
	Effect.gen(function* () {
		if (params.client.eventStream === undefined) {
			return yield* Effect.fail(
				new Error(
					"Connected daemon does not support eventStream RPC yet. Update daemon/runtime and rerun `az daemon status --watch`.",
				),
			)
		}

		let cursor = params.initialCursor
		let processedBatches = 0

		while (params.maxBatches === undefined || processedBatches < params.maxBatches) {
			const attempt = yield* params.client
				.eventStream({
					clientId: params.clientId,
					projectPath: params.projectPath,
					cursor,
					batchSize: params.batchSize,
					waitMs: params.waitMs,
				})
				.pipe(Effect.either)

			if (attempt._tag === "Left") {
				if (params.watch && isRetryableRpcClientError(attempt.left)) {
					yield* Console.log(
						`daemon stream reconnecting from cursor=${cursor ?? "<start>"} in ${params.reconnectDelayMs}ms (${attempt.left.message})`,
					)
					yield* Effect.sleep(Duration.millis(params.reconnectDelayMs))
					continue
				}
				return yield* Effect.fail(
					formatDaemonRpcClientFailure({
						operation: "eventStream",
						socketUrl: params.socketUrl,
						error: attempt.left,
					}),
				)
			}

			cursor = attempt.right.nextCursor
			processedBatches += 1
			yield* params.onBatch(attempt.right)

			if (!params.watch) {
				break
			}
		}

		return cursor
	})

const daemonStatusHandler = (args: {
	readonly verbose: boolean
	readonly watch: boolean
	readonly cursor: Option.Option<number>
	readonly streamBatchSize: Option.Option<number>
	readonly streamWaitMs: Option.Option<number>
	readonly streamBatches: Option.Option<number>
}) =>
	Effect.gen(function* () {
		const bootstrap = yield* bootstrapDaemonRpcClient({
			autoStart: daemonCommandShouldAutoStart("status"),
		})
		const status = yield* bootstrap.client.status().pipe(
			Effect.mapError((error) =>
				formatDaemonRpcClientFailure({
					operation: "status",
					socketUrl: bootstrap.socketUrl,
					error,
				}),
			),
			(effect) => withDaemonControlTimeout("status", effect),
		)
		yield* Console.log(
			formatDaemonControlStatusLine({
				mode: "status",
				status,
			}),
		)
		const snapshotSummary = yield* getDaemonSessionSnapshotSummary({
			client: bootstrap.client,
			socketUrl: bootstrap.socketUrl,
			projectPath: status.sync.projectPath ?? undefined,
		}).pipe(
			Effect.catchAll((error) =>
				Console.log(`daemon session snapshot unavailable: ${error.message}`).pipe(
					Effect.as(Option.none<DaemonSessionSnapshotSummary>()),
				),
			),
		)
		if (Option.isSome(snapshotSummary)) {
			yield* Console.log(formatDaemonSessionSnapshotSummaryLine(snapshotSummary.value))
		}

		if (args.watch) {
			const batchSize = Option.getOrElse(args.streamBatchSize, () => 32)
			const waitMs = Option.getOrElse(args.streamWaitMs, () => 2500)
			const maxBatches = Option.getOrUndefined(args.streamBatches)
			const startCursorLabel = Option.match(args.cursor, {
				onNone: () => "<start>",
				onSome: (cursor) => String(cursor),
			})
			yield* Console.log(
				`daemon status watch: streaming event batches from cursor=${startCursorLabel} batchSize=${batchSize} waitMs=${waitMs}`,
			)
			const finalCursor = yield* consumeDaemonStatusStreamBatches({
				client: bootstrap.client,
				socketUrl: bootstrap.socketUrl,
				clientId: `az-cli:daemon-status:${process.pid}`,
				projectPath: status.sync.projectPath ?? undefined,
				initialCursor: Option.getOrUndefined(args.cursor),
				batchSize,
				waitMs,
				watch: true,
				maxBatches,
				reconnectDelayMs: 1000,
				onBatch: (batch) =>
					Effect.gen(function* () {
						yield* Console.log(formatDaemonEventStreamBatchSummaryLine(batch))
						for (const entry of batch.events) {
							yield* Console.log(`  ${formatDaemonEventStreamEntryLine(entry)}`)
						}
					}),
			})
			yield* Console.log(`daemon status watch ended at cursor=${finalCursor ?? "<start>"}`)
		}

		if (args.verbose) {
			yield* Console.log(JSON.stringify(status, null, 2))
		}
	})

const daemonHealthHandler = (args: { readonly verbose: boolean }) =>
	Effect.gen(function* () {
		const bootstrap = yield* bootstrapDaemonRpcClient({
			autoStart: daemonCommandShouldAutoStart("health"),
		})
		const health = yield* bootstrap.client.health().pipe(
			Effect.mapError((error) =>
				formatDaemonRpcClientFailure({
					operation: "health",
					socketUrl: bootstrap.socketUrl,
					error,
				}),
			),
			(effect) => withDaemonControlTimeout("health", effect),
		)
		yield* Console.log(`daemon health: ${health.state} (${health.reason})`)
		yield* Console.log(
			formatDaemonControlStatusLine({
				mode: "health",
				status: health.status,
			}),
		)
		if (args.verbose) {
			yield* Console.log(JSON.stringify(health, null, 2))
		}
		if (health.state !== "healthy") {
			yield* Console.log("Suggested diagnostics:")
			yield* Console.log("- az daemon status")
			yield* Console.log("- az daemon logs --lines 100")
			yield* Console.log("- az daemon restart --project-dir <path>")
		}
	})

const daemonStopHandler = (args: { readonly verbose: boolean }) =>
	Effect.gen(function* () {
		const bootstrap = yield* bootstrapDaemonRpcClient({
			autoStart: daemonCommandShouldAutoStart("stop"),
		})
		const status = yield* bootstrap.client.stop().pipe(
			Effect.mapError((error) =>
				formatDaemonRpcClientFailure({
					operation: "stop",
					socketUrl: bootstrap.socketUrl,
					error,
				}),
			),
			(effect) => withDaemonControlTimeout("stop", effect),
		)
		yield* Console.log("Headless backend sync daemon stopped.")
		yield* Console.log(
			formatDaemonControlStatusLine({
				mode: "stop",
				status,
			}),
		)
		if (args.verbose) {
			yield* Console.log(JSON.stringify(status, null, 2))
		}
	})

const daemonRestartHandler = (args: {
	readonly intervalMs: Option.Option<number>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		if (args.verbose) {
			yield* Console.log("daemon_restart: bootstrap begin")
		}
		const bootstrap = yield* bootstrapDaemonRpcClient({
			autoStart: daemonCommandShouldAutoStart("restart"),
			onAttachAttempt: args.verbose
				? (observation) => {
						console.error(
							`daemon_restart: attach attempt=${observation.attempt} delayMs=${observation.delayMs} remainingMs=${observation.timeoutRemainingMs} socket=${observation.socketUrl ?? "<none>"}`,
						)
					}
				: undefined,
		})
		if (args.verbose) {
			yield* Console.log(
				`daemon_restart: bootstrap ready startedDaemon=${bootstrap.startedDaemon} attempts=${bootstrap.attachAttemptCount} socket=${bootstrap.socketUrl}`,
			)
			yield* Console.log("daemon_restart: dispatch restart RPC")
		}
		const status = yield* bootstrap.client
			.restart({
				projectPath: Option.getOrUndefined(args.projectDir),
				intervalMs: Option.getOrUndefined(args.intervalMs),
			})
			.pipe(
				Effect.mapError((error) =>
					formatDaemonRpcClientFailure({
						operation: "restart",
						socketUrl: bootstrap.socketUrl,
						error,
					}),
				),
				(effect) => withDaemonControlTimeout("restart", effect),
			)
		if (args.verbose) {
			yield* Console.log("daemon_restart: restart RPC response received")
		}
		yield* Console.log("Headless backend sync daemon restarted.")
		yield* Console.log(
			formatDaemonControlStatusLine({
				mode: "restart",
				status,
			}),
		)
		if (args.verbose) {
			yield* Console.log(JSON.stringify(status, null, 2))
		}
	})

const daemonLogsHandler = (args: {
	readonly lines: Option.Option<number>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrUndefined(args.projectDir)
		const lineLimit = Option.getOrElse(args.lines, () => 100)
		const bootstrap = yield* bootstrapDaemonRpcClient({
			autoStart: daemonCommandShouldAutoStart("logs"),
		})
		const logResult = yield* bootstrap.client
			.logs({
				projectPath: cwd,
				lines: lineLimit,
			})
			.pipe(
				Effect.mapError((error) =>
					formatDaemonRpcClientFailure({
						operation: "logs",
						socketUrl: bootstrap.socketUrl,
						error,
					}),
				),
			)
		if (logResult.lines.length === 0) {
			yield* Console.log(`No daemon log lines available in ${logResult.logPath}.`)
			return
		}

		yield* Console.log(
			`Showing ${logResult.lines.length} of ${logResult.totalLines} daemon log line(s) from ${logResult.logPath} (tail=${lineLimit})`,
		)
		for (const line of logResult.lines) {
			yield* Console.log(line)
		}
		if (args.verbose) {
			yield* Console.log("Diagnostics:")
			yield* Console.log("- az daemon status")
			yield* Console.log("- az daemon health")
			yield* Console.log("- az daemon logs --lines 200")
		}
	})

type RelationshipDependencyType = "blocks" | "related" | "parent-child" | "discovered-from"
type RelationshipSpecLinkType = "implements" | "tests" | "blocks" | "relates"
type SpecRequirementLookupInput = {
	readonly reference: string
	readonly selector: SpecRequirementLookupSelector
}

const SPEC_EXTERNAL_CODE_PATTERN = /^AZ-(FR|AT)-\d{4}[A-Z]?$/i
const SPEC_IMPLEMENTATION_PATTERN = /^[a-z][a-z0-9-]{0,63}$/
const DEFAULT_SPEC_IMPLEMENTATION = "default"

const normalizeSpecExternalCodeForCli = (value: string): string =>
	value.trim().toUpperCase().replace(/\s+/g, "")

const normalizeSpecImplementationForCli = (value: string): string => value.trim().toLowerCase()

const parseSpecImplementationForCli = (value: string): Effect.Effect<string, Error> => {
	const normalized = normalizeSpecImplementationForCli(value)
	if (!SPEC_IMPLEMENTATION_PATTERN.test(normalized)) {
		return Effect.fail(
			new Error(
				`Invalid implementation '${value}'. Expected lowercase letters, digits, and hyphens, starting with a letter.`,
			),
		)
	}
	return Effect.succeed(normalized)
}

const parseSpecImplementationsForCli = (
	values: readonly string[],
): Effect.Effect<readonly string[], Error> =>
	Effect.gen(function* () {
		if (values.length === 0) {
			return [DEFAULT_SPEC_IMPLEMENTATION] as const
		}

		const parsed = yield* Effect.all(values.map((value) => parseSpecImplementationForCli(value)))
		return [...new Set(parsed)].sort((left, right) => left.localeCompare(right))
	})

const parseOptionalImplementationListForCli = (
	values: readonly string[],
): Effect.Effect<readonly string[] | undefined, Error> =>
	values.length === 0 ? Effect.succeed(undefined) : parseSpecImplementationsForCli(values)

const resolveParityImplementationForCli = (
	value: Option.Option<string>,
	registry: ImplementationRegistry,
): Effect.Effect<string, Error> =>
	Option.match(value, {
		onNone: () => Effect.succeed(registry.default_implementation),
		onSome: (implementation) => parseSpecImplementationForCli(implementation),
	})

const formatImplementationSummaryLine = (implementation: ImplementationRecord): string => {
	const flags = [
		implementation.is_default ? "default" : undefined,
		implementation.is_builtin ? "builtin" : undefined,
	].filter((flag): flag is string => flag !== undefined)
	const description =
		implementation.description === undefined
			? ""
			: ` - ${compactSingleLineText(implementation.description)}`
	const directory =
		implementation.directory === undefined ? "" : ` (dir=${implementation.directory})`
	return `${implementation.name}${flags.length === 0 ? "" : ` [${flags.join(",")}]`}${directory}${description}`
}

const logImplementationDetails = (
	implementation: ImplementationRecord,
	registry: ImplementationRegistry,
): Effect.Effect<void> =>
	Effect.gen(function* () {
		yield* Console.log(`Implementation: ${implementation.name}`)
		yield* Console.log(`Default: ${implementation.is_default ? "yes" : "no"}`)
		yield* Console.log(`Built-in: ${implementation.is_builtin ? "yes" : "no"}`)
		yield* Console.log(
			`Implicit default allowed: ${registry.implicit_default_allowed ? "yes" : "no"}`,
		)
		if (implementation.description !== undefined) {
			yield* Console.log(`Description: ${implementation.description}`)
		}
		if (implementation.directory !== undefined) {
			yield* Console.log(`Directory: ${implementation.directory}`)
		}
		yield* Console.log(`Created: ${implementation.created_at}`)
		yield* Console.log(`Updated: ${implementation.updated_at}`)
	})

const withIssueEditorDefaultImplementation = (
	config: AzedarachConfig,
	defaultImplementation: string | undefined,
): AzedarachConfig =>
	defaultImplementation === undefined
		? {
				...config,
				issueEditor: undefined,
			}
		: {
				...config,
				issueEditor: {
					...(config.issueEditor ?? {}),
					defaultImplementation,
				},
			}

const inferSpecRequirementKindFromExternalCodeForCli = (
	externalCode: string,
): "functional" | "acceptance" | "other" => {
	if (externalCode.startsWith("AZ-FR-")) {
		return "functional"
	}
	if (externalCode.startsWith("AZ-AT-")) {
		return "acceptance"
	}
	return "other"
}

const resolveSpecRequirementLookupInput = (args: {
	readonly reference: Option.Option<string>
	readonly id: Option.Option<string>
	readonly localId: Option.Option<string>
	readonly externalCode: Option.Option<string>
}): Effect.Effect<SpecRequirementLookupInput, Error> =>
	Effect.gen(function* () {
		const byId = Option.getOrUndefined(args.id)?.trim()
		const byLocalId = Option.getOrUndefined(args.localId)?.trim()
		const byExternalCode = Option.getOrUndefined(args.externalCode)?.trim()
		const positionalRef = Option.getOrUndefined(args.reference)?.trim()

		const explicitSelectors = [
			byId === undefined || byId.length === 0
				? undefined
				: ({ selector: "id", reference: byId } as const),
			byLocalId === undefined || byLocalId.length === 0
				? undefined
				: ({ selector: "local_id", reference: byLocalId } as const),
			byExternalCode === undefined || byExternalCode.length === 0
				? undefined
				: ({
						selector: "external_code",
						reference: normalizeSpecExternalCodeForCli(byExternalCode),
					} as const),
		].filter((value) => value !== undefined)

		if (explicitSelectors.length > 1) {
			return yield* Effect.fail(
				new Error("Use only one selector flag: --id, --local-id, or --external-code."),
			)
		}

		if (explicitSelectors.length === 1) {
			if (positionalRef !== undefined && positionalRef.length > 0) {
				return yield* Effect.fail(
					new Error(
						"Provide either a positional requirement reference OR one selector flag (--id/--local-id/--external-code), not both.",
					),
				)
			}
			const selected = explicitSelectors[0]
			if (selected === undefined) {
				return yield* Effect.fail(new Error("Invalid selector input"))
			}
			return selected
		}

		if (positionalRef === undefined || positionalRef.length === 0) {
			return yield* Effect.fail(
				new Error(
					"Missing spec requirement reference. Provide a positional ref or use one selector flag (--id/--local-id/--external-code).",
				),
			)
		}

		return {
			selector: "auto",
			reference: positionalRef,
		}
	})

const resolveOptionalAliasedTextInput = (args: {
	readonly positional: Option.Option<string>
	readonly optionValue: Option.Option<string>
	readonly positionalName: string
	readonly optionName: string
}): Effect.Effect<Option.Option<string>, Error> =>
	Effect.gen(function* () {
		const positional = Option.getOrUndefined(args.positional)?.trim()
		const optionValue = Option.getOrUndefined(args.optionValue)?.trim()

		const hasPositional = positional !== undefined && positional.length > 0
		const hasOptionValue = optionValue !== undefined && optionValue.length > 0

		if (!hasPositional && !hasOptionValue) {
			return Option.none<string>()
		}
		if (!hasPositional && hasOptionValue) {
			if (optionValue === undefined) {
				return Option.none<string>()
			}
			return Option.some(optionValue)
		}
		if (hasPositional && !hasOptionValue) {
			if (positional === undefined) {
				return Option.none<string>()
			}
			return Option.some(positional)
		}

		if (positional?.toLowerCase() !== optionValue?.toLowerCase()) {
			return yield* Effect.fail(
				new Error(
					`Conflicting values for ${args.positionalName} and ${args.optionName}. Provide one source or matching values.`,
				),
			)
		}

		if (positional === undefined) {
			return Option.none<string>()
		}
		return Option.some(positional)
	})

const resolveRequiredAliasedTextInput = (args: {
	readonly positional: Option.Option<string>
	readonly optionValue: Option.Option<string>
	readonly positionalName: string
	readonly optionName: string
}): Effect.Effect<string, Error> =>
	Effect.gen(function* () {
		const merged = yield* resolveOptionalAliasedTextInput(args)
		const value = Option.getOrUndefined(merged)
		if (value === undefined || value.length === 0) {
			return yield* Effect.fail(
				new Error(
					`Missing ${args.positionalName}. Provide ${args.optionName} or positional input.`,
				),
			)
		}
		return value
	})

const parseRelationshipDependencyType = (
	value: string | undefined,
): RelationshipDependencyType | undefined => {
	if (value === undefined) {
		return undefined
	}

	const normalized = value.trim().toLowerCase()
	switch (normalized) {
		case "blocks":
		case "related":
		case "parent-child":
		case "discovered-from":
			return normalized
		default:
			return undefined
	}
}

const parseRelationshipSpecLinkType = (
	value: string | undefined,
): RelationshipSpecLinkType | undefined => {
	if (value === undefined) {
		return undefined
	}

	const normalized = value.trim().toLowerCase()
	switch (normalized) {
		case "implements":
		case "tests":
		case "blocks":
		case "relates":
			return normalized
		default:
			return undefined
	}
}

const parseSpecLinkFulfillmentStatus = (
	value: string | undefined,
): SpecLinkFulfillmentStatus | undefined => {
	if (value === undefined) {
		return undefined
	}
	const normalized = value.trim().toLowerCase()
	switch (normalized) {
		case "planned":
		case "partial":
		case "complete":
		case "verified":
			return normalized
		default:
			return undefined
	}
}

const parseSpecLinkFulfillmentPercent = (
	value: Option.Option<number>,
): Effect.Effect<number | null | undefined, Error> =>
	Effect.gen(function* () {
		if (Option.isNone(value)) {
			return undefined
		}
		const rounded = Math.round(value.value)
		if (!Number.isFinite(rounded) || rounded < 0 || rounded > 100) {
			return yield* Effect.fail(
				new Error("Invalid --fulfillment-percent. Expected an integer 0-100."),
			)
		}
		return rounded
	})

const parseSpecLinkEvidenceNote = (value: Option.Option<string>): string | null | undefined => {
	if (Option.isNone(value)) {
		return undefined
	}
	const trimmed = value.value.trim()
	return trimmed.length > 0 ? trimmed : null
}

const DEFAULT_ISSUE_GET_SYNC_MAX_WAIT_MS = 250
const DEFAULT_ISSUE_GET_WAIT_FLAG_MAX_WAIT_MS = 60_000

const resolveIssueGetSyncWaitMs = (args: {
	readonly wait: boolean
	readonly maxWaitMs: Option.Option<number>
}): number => {
	if (Option.isSome(args.maxWaitMs)) {
		return Math.max(0, Math.floor(args.maxWaitMs.value))
	}
	if (args.wait) {
		const fromEnv = Number.parseInt(process.env.AZEDARACH_ISSUE_GET_WAIT_MAX_MS ?? "", 10)
		if (Number.isFinite(fromEnv) && fromEnv > 0) {
			return Math.floor(fromEnv)
		}
		return DEFAULT_ISSUE_GET_WAIT_FLAG_MAX_WAIT_MS
	}
	return DEFAULT_ISSUE_GET_SYNC_MAX_WAIT_MS
}

/**
 * Show issue details
 */
const issueGetHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
	readonly wait: boolean
	readonly maxWaitMs: Option.Option<number>
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, resolverCwd)

		if (args.verbose) {
			yield* Console.error(`Loading issue: ${issueId}`)
			yield* Console.error(`Project: ${explicitProjectDir ?? resolverCwd}`)
		}

		yield* validateIssueTrackerStore(resolverCwd)

		const issueTrackerClient = yield* IssueTrackerClient
		const maxSyncWaitMs = resolveIssueGetSyncWaitMs({
			wait: args.wait,
			maxWaitMs: args.maxWaitMs,
		})
		const issue = yield* issueTrackerClient
			.show(issueId, explicitProjectDir, { maxSyncWaitMs })
			.pipe(
				Effect.catchTag("NotFoundError", () =>
					Effect.fail(new Error(`Issue not found internally nor externally: ${issueId}`)),
				),
			)
		const specService = yield* SpecService
		const linkedSpecRequirements = yield* specService
			.listIssueRequirements(issue.id, explicitProjectDir ?? resolverCwd)
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(
						`Unable to load linked spec requirements for ${issue.id}: ${error.message}`,
					).pipe(Effect.zipRight(Effect.succeed<readonly never[]>([]))),
				),
			)
		const registry = yield* issueTrackerClient.getImplementationRegistry(explicitProjectDir)
		const showImplementations = registry.implementations.length > 1

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						...issue,
						linked_spec_requirements: linkedSpecRequirements,
					},
					null,
					2,
				),
			)
			return
		}

		yield* Console.log(formatIssueSummaryLine(issue, { showImplementations }))
		const detailSections = formatIssueDetailSections(issue, {
			linkedSpecRequirements,
			showImplementations,
		})
		if (detailSections.length > 0) {
			yield* Console.log("")
			yield* Console.log(detailSections.join("\n\n"))
		}

		if (args.verbose) {
			if (issue.assignee && issue.assignee.trim().length > 0) {
				yield* Console.error(`assignee=${issue.assignee}`)
			}
			if (issue.labels && issue.labels.length > 0) {
				yield* Console.error(`labels=${issue.labels.join(",")}`)
			}
		}
	})

/**
 * List issues
 */
const issueListHandler = (args: {
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly issueType: Option.Option<string>
	readonly parent: Option.Option<string>
	readonly implementations: readonly string[]
	readonly limit: Option.Option<number>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const requestedLimit = Option.getOrUndefined(args.limit)
		if (requestedLimit !== undefined && requestedLimit <= 0) {
			return yield* Effect.fail(new Error("--limit must be a positive integer"))
		}

		const filters = {
			status: Option.getOrUndefined(args.status),
			priority: Option.getOrUndefined(args.priority),
			type: Option.getOrUndefined(args.issueType),
			parent: Option.getOrUndefined(args.parent),
			implementations: yield* parseOptionalImplementationListForCli(args.implementations),
		}
		const hasFilters = Object.values(filters).some((value) => value !== undefined)

		const issueTrackerClient = yield* IssueTrackerClient
		const issues = yield* issueTrackerClient.list(
			hasFilters ? filters : undefined,
			explicitProjectDir,
			{
				limit: requestedLimit === undefined ? undefined : Math.floor(requestedLimit),
				sortBy: "updated_at",
				sortDirection: "desc",
			},
		)

		if (args.json) {
			yield* Console.log(JSON.stringify(issues, null, 2))
			return
		}
		const registry = yield* issueTrackerClient.getImplementationRegistry(explicitProjectDir)
		const showImplementations = registry.implementations.length > 1

		if (issues.length === 0) {
			yield* Console.log("No issues found.")
			return
		}

		for (const issue of issues) {
			yield* Console.log(formatIssueSummaryLine(issue, { showImplementations }))
		}

		if (args.verbose) {
			yield* Console.error(`Listed ${issues.length} issue(s) sorted by updated_at desc.`)
		}
	})

/**
 * Create a new issue
 */
const issueCreateHandler = (args: {
	readonly title: string
	readonly issueType: Option.Option<string>
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly description: Option.Option<string>
	readonly design: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly assignee: Option.Option<string>
	readonly estimate: Option.Option<number>
	readonly labels: Option.Option<string>
	readonly implementations: readonly string[]
	readonly parent: Option.Option<string>
	readonly deferred: boolean
	readonly noDefaultParent: boolean
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)
		const parentContext = yield* Option.match(args.parent, {
			onSome: (parentIssueId) =>
				resolveCliIssueId(parentIssueId, resolverCwd).pipe(
					Effect.map((issueId) =>
						Option.some<ActiveParentContext>({
							issueId,
							source: "explicit-arg",
						}),
					),
				),
			onNone: () =>
				args.deferred || args.noDefaultParent
					? Effect.succeed(Option.none<ActiveParentContext>())
					: resolveActiveParentContext(resolverCwd),
		})
		const resolvedParent = Option.match(parentContext, {
			onNone: () => undefined,
			onSome: (value) => value.issueId,
		})
		const status = yield* parseIssueCreateStatusOption(args.status)

		const issueTrackerClient = yield* IssueTrackerClient
		const issue = yield* issueTrackerClient.create({
			title: args.title,
			type: Option.getOrUndefined(args.issueType),
			status,
			priority: Option.getOrUndefined(args.priority),
			description: Option.getOrUndefined(args.description),
			design: Option.getOrUndefined(args.design),
			acceptance: Option.getOrUndefined(args.acceptance),
			assignee: Option.getOrUndefined(args.assignee),
			estimate: Option.getOrUndefined(args.estimate),
			labels: parseLabelsOption(args.labels),
			implementations: yield* parseOptionalImplementationListForCli(args.implementations),
			parent: resolvedParent,
			cwd: explicitProjectDir,
		})
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue create",
			verbose: args.verbose,
		})

		if (args.json) {
			yield* Console.log(JSON.stringify(issue, null, 2))
			return
		}

		yield* Console.log(`Created issue ${issue.id}`)
		if (resolvedParent !== undefined) {
			yield* Console.log(`Parent: ${resolvedParent}`)
		}
		if (
			issue.implementations.length > 1 ||
			issue.implementations.some((implementation) => implementation !== "default")
		) {
			yield* Console.log(`Implementations: ${issue.implementations.join(", ")}`)
		}
		if (args.verbose) {
			yield* Console.error(
				`status=${issue.status} priority=${issue.priority} type=${issue.issue_type}`,
			)
			const sourceDescription = Option.match(parentContext, {
				onNone: () => "none",
				onSome: (context) => context.source,
			})
			yield* Console.error(`parent_source=${sourceDescription}`)
		}
	})

const issueChildHandler = (args: {
	readonly title: string
	readonly issueType: Option.Option<string>
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly description: Option.Option<string>
	readonly design: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly assignee: Option.Option<string>
	readonly estimate: Option.Option<number>
	readonly labels: Option.Option<string>
	readonly implementations: readonly string[]
	readonly parent: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const parentContext = yield* Option.match(args.parent, {
			onSome: (parentIssueId) =>
				resolveCliIssueId(parentIssueId, resolverCwd).pipe(
					Effect.map((issueId) =>
						Option.some<ActiveParentContext>({
							issueId,
							source: "explicit-arg",
						}),
					),
				),
			onNone: () => resolveActiveParentContext(resolverCwd),
		})
		if (Option.isNone(parentContext)) {
			return yield* Effect.fail(
				new Error(
					"No active parent context found. Provide --parent <issue-id> or set AZEDARACH_PARENT_ISSUE_ID/AZEDARACH_ISSUE_ID.",
				),
			)
		}
		const resolvedParent = parentContext.value.issueId
		const status = yield* parseIssueCreateStatusOption(args.status)

		const issueTrackerClient = yield* IssueTrackerClient
		const issue = yield* issueTrackerClient.create({
			title: args.title,
			type: Option.getOrUndefined(args.issueType),
			status,
			priority: Option.getOrUndefined(args.priority),
			description: Option.getOrUndefined(args.description),
			design: Option.getOrUndefined(args.design),
			acceptance: Option.getOrUndefined(args.acceptance),
			assignee: Option.getOrUndefined(args.assignee),
			estimate: Option.getOrUndefined(args.estimate),
			labels: parseLabelsOption(args.labels),
			implementations: yield* parseOptionalImplementationListForCli(args.implementations),
			parent: resolvedParent,
			cwd: explicitProjectDir,
		})
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue child",
			verbose: args.verbose,
		})

		if (args.json) {
			yield* Console.log(JSON.stringify(issue, null, 2))
			return
		}

		yield* Console.log(`Created child issue ${issue.id} under ${resolvedParent}`)
		if (
			issue.implementations.length > 1 ||
			issue.implementations.some((implementation) => implementation !== "default")
		) {
			yield* Console.log(`Implementations: ${issue.implementations.join(", ")}`)
		}
		if (args.verbose) {
			yield* Console.error(
				`status=${issue.status} priority=${issue.priority} type=${issue.issue_type}`,
			)
		}
	})

const issueBulkCreateHandler = (args: {
	readonly input: string
	readonly deferred: boolean
	readonly noDefaultParent: boolean
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const defaultParentContext =
			args.deferred || args.noDefaultParent
				? Option.none<ActiveParentContext>()
				: yield* resolveActiveParentContext(resolverCwd)
		const defaultParent = Option.match(defaultParentContext, {
			onNone: () => undefined,
			onSome: (context) => context.issueId,
		})

		const inputContent = yield* readIssueBulkCreateInput(args.input)
		const entries = yield* decodeIssueBulkCreatePayload(inputContent).pipe(
			Effect.mapError(
				(error) =>
					new Error(
						`Bulk create JSON parse/validation failed: ${formatIssueBulkCreateError(error)}`,
					),
			),
		)

		const issueTrackerClient = yield* IssueTrackerClient
		const results = yield* Effect.forEach(entries, (entry, index) =>
			Effect.gen(function* () {
				const requestedTitle = getIssueBulkCreateRequestedTitle(entry)
				return yield* Schema.decodeUnknown(IssueBulkCreateEntrySchema)(entry).pipe(
					Effect.flatMap((decodedEntry) =>
						Effect.gen(function* () {
							const resolvedParent =
								decodedEntry.parent === undefined
									? defaultParent
									: yield* resolveCliIssueId(decodedEntry.parent, resolverCwd)
							const issue = yield* issueTrackerClient.create({
								title: decodedEntry.title,
								type: decodedEntry.type,
								priority: decodedEntry.priority,
								description: decodedEntry.description,
								design: decodedEntry.design,
								acceptance: decodedEntry.acceptance,
								assignee: decodedEntry.assignee,
								estimate: decodedEntry.estimate,
								labels: decodedEntry.labels === undefined ? undefined : [...decodedEntry.labels],
								implementations: yield* parseOptionalImplementationListForCli(
									decodedEntry.implementations ?? [],
								),
								parent: resolvedParent,
								cwd: explicitProjectDir,
							})

							return {
								index,
								requestedTitle: decodedEntry.title,
								issueId: issue.id,
								created: true,
							} satisfies IssueBulkCreateResult
						}),
					),
					Effect.catchAll((error) =>
						Effect.succeed<IssueBulkCreateResult>({
							index,
							requestedTitle,
							created: false,
							error: formatIssueBulkCreateError(error),
						}),
					),
				)
			}),
		)

		const summary = summarizeIssueBulkCreateResults(results)
		if (summary.createdCount > 0) {
			yield* syncLinearAfterIssueMutation({
				issueTrackerClient,
				explicitProjectDir,
				resolverCwd,
				commandLabel: "issue bulk-create",
				verbose: args.verbose,
			})
		}
		if (args.json) {
			yield* Console.log(JSON.stringify(summary, null, 2))
			return
		}

		yield* Console.log(
			`Bulk create finished: ${summary.createdCount} succeeded, ${summary.failedCount} failed.`,
		)
		for (const result of summary.results) {
			const itemLabel =
				result.requestedTitle === undefined
					? `item ${result.index + 1}`
					: `"${result.requestedTitle}"`
			if (result.created) {
				yield* Console.log(`- ${itemLabel}: created ${result.issueId ?? "<unknown>"}`)
				continue
			}
			yield* Console.log(`- ${itemLabel}: failed (${result.error ?? "unknown error"})`)
		}
		if (args.verbose) {
			const sourceDescription = Option.match(defaultParentContext, {
				onNone: () => "none",
				onSome: (context) => context.source,
			})
			yield* Console.error(`default_parent_source=${sourceDescription}`)
		}
	})

/**
 * Update issue fields
 */
const issueUpdateHandler = (args: {
	readonly issueId: string
	readonly status: Option.Option<string>
	readonly notes: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly title: Option.Option<string>
	readonly issueType: Option.Option<string>
	readonly description: Option.Option<string>
	readonly design: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly assignee: Option.Option<string>
	readonly estimate: Option.Option<number>
	readonly labels: Option.Option<string>
	readonly implementations: readonly string[]
	readonly parent: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const labels = Option.match(args.labels, {
			onNone: () => undefined,
			onSome: (value) =>
				value
					.split(",")
					.map((label) => label.trim())
					.filter((label) => label.length > 0),
		})

		const resolvedParent = yield* Option.match(args.parent, {
			onNone: () => Effect.succeed<string | undefined>(undefined),
			onSome: (parentIssueId) => resolveCliIssueId(parentIssueId, resolverCwd),
		})

		const fields = {
			status: Option.getOrUndefined(args.status),
			notes: Option.getOrUndefined(args.notes),
			priority: Option.getOrUndefined(args.priority),
			title: Option.getOrUndefined(args.title),
			type: Option.getOrUndefined(args.issueType),
			description: Option.getOrUndefined(args.description),
			design: Option.getOrUndefined(args.design),
			acceptance: Option.getOrUndefined(args.acceptance),
			assignee: Option.getOrUndefined(args.assignee),
			estimate: Option.getOrUndefined(args.estimate),
			labels,
			implementations: yield* parseOptionalImplementationListForCli(args.implementations),
			parent: resolvedParent,
		}

		const hasChanges = Object.values(fields).some((value) => value !== undefined)
		if (!hasChanges) {
			return yield* Effect.fail(
				new Error(
					"No fields provided. Use at least one --status/--design/--description/... option.",
				),
			)
		}

		const issueTrackerClient = yield* IssueTrackerClient
		yield* issueTrackerClient.update(issueId, fields, explicitProjectDir)
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue update",
			verbose: args.verbose,
		})
		if (args.json) {
			yield* Console.log(JSON.stringify({ id: issueId, updated: true }, null, 2))
			return
		}
		yield* Console.log(`Updated issue ${issueId}`)
		if (args.verbose) {
			yield* Console.error("Use `az issue get <issue-id>` to inspect the updated issue.")
		}
	})

const issueBulkUpdateHandler = (args: {
	readonly input: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const inputContent = yield* readIssueBulkUpdateInput(args.input)
		const updates = yield* decodeIssueBulkUpdatePayload(inputContent).pipe(
			Effect.mapError(
				(error) =>
					new Error(
						`Bulk update JSON parse/validation failed: ${formatIssueBulkUpdateError(error)}`,
					),
			),
		)

		const issueTrackerClient = yield* IssueTrackerClient
		const results = yield* Effect.forEach(updates, (entry, index) =>
			Effect.gen(function* () {
				const fields = mapIssueBulkUpdateFields(entry)
				if (!hasIssueBulkUpdateChanges(fields)) {
					return {
						index,
						requestedId: entry.id,
						issueId: entry.id,
						updated: false,
						error: "No fields provided for bulk update item.",
					} satisfies IssueBulkUpdateResult
				}

				return yield* resolveCliIssueId(entry.id, resolverCwd).pipe(
					Effect.flatMap((issueId) =>
						issueTrackerClient.update(issueId, fields, explicitProjectDir).pipe(
							Effect.as<IssueBulkUpdateResult>({
								index,
								requestedId: entry.id,
								issueId,
								updated: true,
							}),
						),
					),
					Effect.catchAll((error) =>
						Effect.succeed<IssueBulkUpdateResult>({
							index,
							requestedId: entry.id,
							issueId: entry.id,
							updated: false,
							error: formatIssueBulkUpdateError(error),
						}),
					),
				)
			}),
		)

		const summary = summarizeIssueBulkUpdateResults(results)
		if (summary.updatedCount > 0) {
			yield* syncLinearAfterIssueMutation({
				issueTrackerClient,
				explicitProjectDir,
				resolverCwd,
				commandLabel: "issue bulk-update",
				verbose: args.verbose,
			})
		}
		if (args.json) {
			yield* Console.log(JSON.stringify(summary, null, 2))
			return
		}

		yield* Console.log(
			`Bulk update finished: ${summary.updatedCount} succeeded, ${summary.failedCount} failed.`,
		)
		if (summary.failedCount > 0 || args.verbose) {
			for (const result of summary.results) {
				const issueLabel =
					result.issueId === result.requestedId
						? result.issueId
						: `${result.requestedId} -> ${result.issueId}`
				if (result.updated) {
					yield* Console.log(`- ${issueLabel}: updated`)
					continue
				}
				yield* Console.log(`- ${issueLabel}: failed (${result.error ?? "unknown error"})`)
			}
		}
	})

const implListHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const appConfig = yield* AppConfig
		const issueTrackerClient = yield* IssueTrackerClient
		const registry = yield* issueTrackerClient.getImplementationRegistry(explicitProjectDir)
		const issueEditorConfig = yield* appConfig.getIssueEditorConfig()

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						...registry,
						tui_issue_editor_default_implementation:
							issueEditorConfig.defaultImplementation ?? null,
					},
					null,
					2,
				),
			)
			return
		}

		for (const implementation of registry.implementations) {
			yield* Console.log(formatImplementationSummaryLine(implementation))
		}
		if (args.verbose) {
			yield* Console.error(`default=${registry.default_implementation}`)
			yield* Console.error(`implicit_default_allowed=${registry.implicit_default_allowed}`)
			yield* Console.error(
				`tui_issue_editor_default=${issueEditorConfig.defaultImplementation ?? "<unset>"}`,
			)
		}
	})

const implGetHandler = (args: {
	readonly implementation: string
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const implementationName = yield* parseSpecImplementationForCli(args.implementation)
		const issueTrackerClient = yield* IssueTrackerClient
		const registry = yield* issueTrackerClient.getImplementationRegistry(explicitProjectDir)
		const implementation = registry.implementations.find(
			(entry) => entry.name === implementationName,
		)
		if (implementation === undefined) {
			return yield* Effect.fail(new Error(`Implementation not found: ${implementationName}`))
		}

		if (args.json) {
			yield* Console.log(JSON.stringify(implementation, null, 2))
			return
		}

		yield* logImplementationDetails(implementation, registry)
	})

const implAddHandler = (args: {
	readonly implementation: string
	readonly description: Option.Option<string>
	readonly directory: Option.Option<string>
	readonly setDefault: boolean
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const implementationName = yield* parseSpecImplementationForCli(args.implementation)
		const issueTrackerClient = yield* IssueTrackerClient
		const implementation = yield* issueTrackerClient.createImplementation({
			name: implementationName,
			description: Option.getOrUndefined(args.description),
			directory: Option.getOrUndefined(args.directory),
			setDefault: args.setDefault,
			cwd: explicitProjectDir,
		})

		if (args.json) {
			yield* Console.log(JSON.stringify(implementation, null, 2))
			return
		}

		yield* Console.log(`Added implementation ${implementation.name}`)
		if (implementation.is_default) {
			yield* Console.log(`Default: ${implementation.name}`)
		}
	})

const implUpdateHandler = (args: {
	readonly implementation: string
	readonly rename: Option.Option<string>
	readonly description: Option.Option<string>
	readonly directory: Option.Option<string>
	readonly setDefault: boolean
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const implementationName = yield* parseSpecImplementationForCli(args.implementation)
		const nextName = yield* Option.match(args.rename, {
			onNone: () => Effect.succeed<string | undefined>(undefined),
			onSome: (value) => parseSpecImplementationForCli(value).pipe(Effect.map((parsed) => parsed)),
		})
		const description = Option.match(args.description, {
			onNone: () => undefined,
			onSome: (value) => value,
		})
		const directory = Option.match(args.directory, {
			onNone: () => undefined,
			onSome: (value) => value,
		})
		if (
			nextName === undefined &&
			description === undefined &&
			directory === undefined &&
			!args.setDefault
		) {
			return yield* Effect.fail(
				new Error("No changes provided. Use --rename, --description, --dir, or --default."),
			)
		}

		const issueTrackerClient = yield* IssueTrackerClient
		const implementation = yield* issueTrackerClient.updateImplementation(
			implementationName,
			{
				name: nextName,
				description,
				directory,
				setDefault: args.setDefault ? true : undefined,
			},
			explicitProjectDir,
		)

		if (args.json) {
			yield* Console.log(JSON.stringify(implementation, null, 2))
			return
		}

		yield* Console.log(`Updated implementation ${implementation.name}`)
		if (implementation.is_default) {
			yield* Console.log(`Default: ${implementation.name}`)
		}
	})

const implDeleteHandler = (args: {
	readonly implementation: string
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const implementationName = yield* parseSpecImplementationForCli(args.implementation)
		const issueTrackerClient = yield* IssueTrackerClient
		const deleted = yield* issueTrackerClient.deleteImplementation(
			implementationName,
			explicitProjectDir,
		)
		if (!deleted) {
			return yield* Effect.fail(new Error(`Implementation not found: ${implementationName}`))
		}

		if (args.json) {
			yield* Console.log(JSON.stringify({ name: implementationName, deleted: true }, null, 2))
			return
		}

		yield* Console.log(`Deleted implementation ${implementationName}`)
	})

const implSetDefaultHandler = (args: {
	readonly implementation: string
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const implementationName = yield* parseSpecImplementationForCli(args.implementation)
		const issueTrackerClient = yield* IssueTrackerClient
		const registry = yield* issueTrackerClient.setDefaultImplementation(
			implementationName,
			explicitProjectDir,
		)

		if (args.json) {
			yield* Console.log(JSON.stringify(registry, null, 2))
			return
		}

		yield* Console.log(`Default implementation: ${registry.default_implementation}`)
	})

const implSetEditorDefaultHandler = (args: {
	readonly implementation: string
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const implementationName = yield* parseSpecImplementationForCli(args.implementation)
		const issueTrackerClient = yield* IssueTrackerClient
		const registry = yield* issueTrackerClient.getImplementationRegistry(explicitProjectDir)
		const implementationExists = registry.implementations.some(
			(entry) => entry.name === implementationName,
		)
		if (!implementationExists) {
			return yield* Effect.fail(new Error(`Implementation not found: ${implementationName}`))
		}

		const configPath = yield* resolveWritableConfigPath(explicitProjectDir)
		const currentConfig = yield* loadWritableConfig(configPath)
		const nextConfig = withIssueEditorDefaultImplementation(currentConfig, implementationName)
		yield* saveWritableConfig(configPath, nextConfig)

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						configPath,
						defaultImplementation: implementationName,
					},
					null,
					2,
				),
			)
			return
		}

		yield* Console.log(`TUI issue editor default implementation: ${implementationName}`)
		yield* Console.log(`Config: ${configPath}`)
	})

const implClearEditorDefaultHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const configPath = yield* resolveWritableConfigPath(explicitProjectDir)
		const currentConfig = yield* loadWritableConfig(configPath)
		const nextConfig = withIssueEditorDefaultImplementation(currentConfig, undefined)
		yield* saveWritableConfig(configPath, nextConfig)

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						configPath,
						defaultImplementation: null,
					},
					null,
					2,
				),
			)
			return
		}

		yield* Console.log("Cleared TUI issue editor default implementation")
		yield* Console.log(`Config: ${configPath}`)
	})

/**
 * Add an issue dependency edge
 */
const issueDepAddHandler = (args: {
	readonly issueId: string
	readonly dependsOnId: string
	readonly dependencyType: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, resolverCwd)
		const dependsOnId = yield* resolveCliIssueId(args.dependsOnId, resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const dependencyType = yield* Option.match(args.dependencyType, {
			onNone: () => Effect.succeed<RelationshipDependencyType>("blocks"),
			onSome: (value) => {
				const parsed = parseRelationshipDependencyType(value)
				if (parsed === undefined) {
					return Effect.fail(
						new Error(
							`Invalid dependency type '${value}'. Expected one of: blocks, related, parent-child, discovered-from.`,
						),
					)
				}
				return Effect.succeed(parsed)
			},
		})

		const issueTrackerClient = yield* IssueTrackerClient
		yield* issueTrackerClient.addDependency(
			issueId,
			dependsOnId,
			dependencyType,
			explicitProjectDir,
		)
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue dep add",
			verbose: args.verbose,
		})

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						issueId,
						dependsOnId,
						type: dependencyType,
						updated: true,
					},
					null,
					2,
				),
			)
			return
		}

		yield* Console.log(`Added ${dependencyType} dependency: ${issueId} -> ${dependsOnId}`)
		if (args.verbose) {
			yield* Console.error("Use `az issue get <issue-id>` to inspect dependencies.")
		}
	})

/**
 * Remove an issue dependency edge
 */
const issueDepRemoveHandler = (args: {
	readonly issueId: string
	readonly dependsOnId: string
	readonly dependencyType: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, resolverCwd)
		const dependsOnId = yield* resolveCliIssueId(args.dependsOnId, resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const dependencyType = yield* Option.match(args.dependencyType, {
			onNone: () => Effect.succeed<RelationshipDependencyType | undefined>(undefined),
			onSome: (value) => {
				const parsed = parseRelationshipDependencyType(value)
				if (parsed === undefined) {
					return Effect.fail(
						new Error(
							`Invalid dependency type '${value}'. Expected one of: blocks, related, parent-child, discovered-from.`,
						),
					)
				}
				return Effect.succeed(parsed)
			},
		})

		const issueTrackerClient = yield* IssueTrackerClient
		yield* issueTrackerClient.removeDependency(
			issueId,
			dependsOnId,
			dependencyType,
			explicitProjectDir,
		)
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue dep remove",
			verbose: args.verbose,
		})

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						issueId,
						dependsOnId,
						type: dependencyType ?? null,
						updated: true,
					},
					null,
					2,
				),
			)
			return
		}

		if (dependencyType === undefined) {
			yield* Console.log(`Removed dependency edge(s): ${issueId} -> ${dependsOnId}`)
		} else {
			yield* Console.log(`Removed ${dependencyType} dependency: ${issueId} -> ${dependsOnId}`)
		}
		if (args.verbose) {
			yield* Console.error("Use `az issue get <issue-id>` to inspect dependencies.")
		}
	})

/**
 * Close an issue
 */
const issueCloseHandler = (args: {
	readonly issueId: string
	readonly reason: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const issueTrackerClient = yield* IssueTrackerClient
		const issue = yield* issueTrackerClient.show(issueId, explicitProjectDir)
		const childIds = Array.from(
			new Set(
				(issue.dependents ?? []).flatMap((dependent) => {
					const childId = dependent.id.trim()
					if (dependent.dependency_type !== "parent-child" || childId.length === 0) {
						return []
					}
					return [childId]
				}),
			),
		)
		const openChildren: TrackedIssue[] = []
		for (const childId of childIds) {
			const child = yield* issueTrackerClient
				.show(childId, explicitProjectDir)
				.pipe(Effect.catchAll(() => Effect.succeed<TrackedIssue | undefined>(undefined)))
			if (child !== undefined && isOpenChildForCloseGuard(child)) {
				openChildren.push(child)
			}
		}
		if (openChildren.length > 0) {
			return yield* Effect.fail(
				new Error(
					formatCloseGuardMessage(issueId, openChildren, Option.getOrUndefined(args.reason)),
				),
			)
		}

		yield* issueTrackerClient.close(issueId, Option.getOrUndefined(args.reason), explicitProjectDir)
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue close",
			verbose: args.verbose,
		})
		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						id: issueId,
						closed: true,
						reason: Option.getOrUndefined(args.reason),
					},
					null,
					2,
				),
			)
			return
		}
		yield* Console.log(`Closed issue ${issueId}`)
		if (args.verbose && Option.isSome(args.reason)) {
			yield* Console.error(`reason=${args.reason.value}`)
		}
	})

interface ParentChildTrackingMiss {
	readonly issue: TrackedIssue
	readonly reason: string
	readonly inspectCommand: string
	readonly remediateCommand: string
}

const hasParentChildDependency = (issue: TrackedIssue): boolean =>
	(issue.dependencies ?? []).some((dependency) => dependency.dependency_type === "parent-child")

const findDependencyToIssue = (issue: TrackedIssue, targetIssueId: string) =>
	(issue.dependencies ?? []).find((dependency) =>
		issueIdsEqualForLookup(dependency.id, targetIssueId),
	)

const issueMentionsParentId = (issue: TrackedIssue, parentIssueId: string): boolean => {
	const needle = parentIssueId.trim().toLowerCase()
	if (needle.length === 0) {
		return false
	}

	const haystack = [
		issue.title,
		issue.description ?? "",
		issue.design ?? "",
		issue.acceptance ?? "",
		issue.notes ?? "",
	]
		.join(" ")
		.toLowerCase()
	return haystack.includes(needle)
}

const buildParentChildTrackingMiss = (
	issue: TrackedIssue,
	parentIssueId: string,
	reason: string,
): ParentChildTrackingMiss => ({
	issue,
	reason,
	inspectCommand: `az issue get ${issue.id}`,
	remediateCommand: `az issue update ${issue.id} --parent ${parentIssueId}`,
})

const findLikelyParentChildTrackingMisses = (
	parentIssueId: string,
	issues: ReadonlyArray<TrackedIssue>,
): ReadonlyArray<ParentChildTrackingMiss> => {
	const misses: ParentChildTrackingMiss[] = []
	for (const issue of issues) {
		if (issueIdsEqualForLookup(issue.id, parentIssueId)) {
			continue
		}
		if (!isOpenChildForCloseGuard(issue)) {
			continue
		}

		const dependencyToParent = findDependencyToIssue(issue, parentIssueId)
		if (dependencyToParent !== undefined) {
			if (dependencyToParent.dependency_type !== "parent-child") {
				misses.push(
					buildParentChildTrackingMiss(
						issue,
						parentIssueId,
						`Dependency to ${parentIssueId} is typed '${dependencyToParent.dependency_type}' instead of 'parent-child'.`,
					),
				)
			}
			continue
		}

		if (hasParentChildDependency(issue)) {
			continue
		}

		if (issueMentionsParentId(issue, parentIssueId)) {
			misses.push(
				buildParentChildTrackingMiss(
					issue,
					parentIssueId,
					`Issue text references ${parentIssueId} but no parent-child link exists.`,
				),
			)
		}
	}
	return misses
}

const formatParentChildCheckOutput = (
	parentIssueId: string,
	misses: ReadonlyArray<ParentChildTrackingMiss>,
): string => {
	if (misses.length === 0) {
		return `No likely parent-child tracking misses found for ${parentIssueId}.`
	}

	const lines: string[] = [
		`Parent-child tracking check for ${parentIssueId}: ${misses.length} likely miss(es) found.`,
		"Suggested remediation commands:",
	]
	for (const miss of misses) {
		lines.push(
			`- ${miss.issue.id}: ${compactSingleLineText(miss.issue.title)} [status=${miss.issue.status}]`,
		)
		lines.push(`  reason: ${miss.reason}`)
		lines.push(`  inspect: ${miss.inspectCommand}`)
		lines.push(`  remediate: ${miss.remediateCommand}`)
	}
	return lines.join("\n")
}

const issueCheckHandler = (args: {
	readonly issueId: Option.Option<string>
	readonly limit: Option.Option<number>
	readonly includeClosed: boolean
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const parentContext = yield* Option.match(args.issueId, {
			onSome: (parentIssueId) =>
				resolveCliIssueId(parentIssueId, resolverCwd).pipe(
					Effect.map((issueId) =>
						Option.some<ActiveParentContext>({
							issueId,
							source: "explicit-arg",
						}),
					),
				),
			onNone: () => resolveActiveParentContext(resolverCwd),
		})

		if (Option.isNone(parentContext)) {
			return yield* Effect.fail(
				new Error(
					"No active parent context found. Provide [issue-id] or set AZEDARACH_PARENT_ISSUE_ID/AZEDARACH_ISSUE_ID.",
				),
			)
		}

		const parentIssueId = parentContext.value.issueId
		const issueTrackerClient = yield* IssueTrackerClient
		const parentIssue = yield* issueTrackerClient.show(parentIssueId, explicitProjectDir)

		const issues = yield* issueTrackerClient.list(undefined, explicitProjectDir, {
			limit: Option.getOrElse(args.limit, () => 200),
			includeClosed: args.includeClosed,
			sortBy: "updated_at",
			sortDirection: "desc",
		})
		const misses = findLikelyParentChildTrackingMisses(parentIssueId, issues)

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						parent_issue_id: parentIssueId,
						parent_issue_title: parentIssue.title,
						checked_count: issues.length,
						miss_count: misses.length,
						misses: misses.map((miss) => ({
							issue_id: miss.issue.id,
							title: miss.issue.title,
							status: miss.issue.status,
							reason: miss.reason,
							inspect_command: miss.inspectCommand,
							remediate_command: miss.remediateCommand,
						})),
					},
					null,
					2,
				),
			)
			return
		}

		yield* Console.log(formatParentChildCheckOutput(parentIssueId, misses))
		if (args.verbose) {
			yield* Console.error(
				`checked=${issues.length} misses=${misses.length} parent=${parentIssueId}`,
			)
		}
	})

/**
 * Delete an issue
 */
const issueDeleteHandler = (args: {
	readonly issueId: string
	readonly force: boolean
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		if (!args.force) {
			return yield* Effect.fail(
				new Error("Refusing to delete without --force. This operation is irreversible."),
			)
		}

		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const issueTrackerClient = yield* IssueTrackerClient
		yield* issueTrackerClient.delete(issueId, explicitProjectDir)
		yield* syncLinearAfterIssueMutation({
			issueTrackerClient,
			explicitProjectDir,
			resolverCwd,
			commandLabel: "issue delete",
			verbose: false,
		})
		if (args.json) {
			yield* Console.log(JSON.stringify({ id: issueId, deleted: true }, null, 2))
			return
		}
		yield* Console.log(`Deleted issue ${issueId}`)
	})

const formatSpecRequirementSummaryLine = (requirement: {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly kind: string
	readonly status: string
	readonly priority: number
	readonly updated_at: string
}): string =>
	`${formatSpecRequirementReference(requirement)}: ${compactSingleLineText(requirement.title)} [id=${requirement.id} kind=${requirement.kind} status=${requirement.status} priority=${requirement.priority} updated_at=${requirement.updated_at}]`

type SpecRequirementViewMode = "compact" | "verbose"

const parseSpecRequirementKindOption = (
	value: Option.Option<string>,
): Effect.Effect<"functional" | "acceptance" | "other" | undefined, Error> =>
	Option.match(value, {
		onNone: () => Effect.succeed(undefined),
		onSome: (raw) => {
			const normalized = raw.trim().toLowerCase()
			if (normalized === "functional") return Effect.succeed("functional")
			if (normalized === "acceptance") return Effect.succeed("acceptance")
			if (normalized === "other") return Effect.succeed("other")
			return Effect.fail(
				new Error(`Invalid kind '${raw}'. Expected: functional, acceptance, other.`),
			)
		},
	})

const parseSpecRequirementViewMode = (
	value: Option.Option<string>,
): Effect.Effect<SpecRequirementViewMode, Error> =>
	Option.match(value, {
		onNone: () => Effect.succeed("compact"),
		onSome: (raw) => {
			const normalized = raw.trim().toLowerCase()
			if (normalized === "compact") return Effect.succeed("compact")
			if (normalized === "verbose") return Effect.succeed("verbose")
			return Effect.fail(new Error(`Invalid view '${raw}'. Expected: compact, verbose.`))
		},
	})

/**
 * List spec requirements
 */
const specReqListHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly query: Option.Option<string>
	readonly kind: Option.Option<string>
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly view: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)
		const parsedKind = yield* parseSpecRequirementKindOption(args.kind)
		const viewMode = yield* parseSpecRequirementViewMode(args.view)
		const query = Option.getOrUndefined(args.query)?.trim()
		const status = Option.getOrUndefined(args.status)?.trim()

		const specService = yield* SpecService
		const requirements = yield* specService.listRequirements(explicitProjectDir, {
			query,
			kind: parsedKind,
			status,
			priority: Option.getOrUndefined(args.priority),
		})

		if (args.json) {
			yield* Console.log(JSON.stringify(requirements, null, 2))
			return
		}

		if (requirements.length === 0) {
			yield* Console.log("No spec requirements found.")
			return
		}

		for (const requirement of requirements) {
			yield* Console.log(formatSpecRequirementSummaryLine(requirement))
			if (viewMode === "verbose") {
				yield* Console.log(`Body:\n${requirement.body}`)
				yield* Console.log("")
			}
		}

		if (args.verbose) {
			yield* Console.error(`Listed ${requirements.length} requirement(s).`)
		}
	})

const specReqSearchHandler = (args: {
	readonly query: Option.Option<string>
	readonly queryOption: Option.Option<string>
	readonly kind: Option.Option<string>
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly view: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const query = yield* resolveRequiredAliasedTextInput({
			positional: args.query,
			optionValue: args.queryOption,
			positionalName: "query",
			optionName: "--query",
		})
		return yield* specReqListHandler({
			projectDir: args.projectDir,
			query: Option.some(query),
			kind: args.kind,
			status: args.status,
			priority: args.priority,
			view: args.view,
			verbose: args.verbose,
			json: args.json,
		})
	})

const specReadHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly view: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)
		const viewMode = yield* parseSpecRequirementViewMode(args.view)

		const specService = yield* SpecService
		const [requirements, links, coverage, publishConfig, lastOutcome] = yield* Effect.all([
			specService.listRequirements(explicitProjectDir),
			specService.listLinks(undefined, explicitProjectDir),
			specService.getCoverageReport(explicitProjectDir),
			specService.getPublishConfig(explicitProjectDir),
			specService.getLastPublishOutcome(explicitProjectDir),
		])

		const payload = {
			summary: {
				requirement_count: requirements.length,
				link_count: links.length,
				unlinked_requirement_count: coverage.unlinked_requirement_ids.length,
				fully_implemented_requirement_count: coverage.fully_implemented_requirement_ids.length,
				partially_implemented_requirement_count:
					coverage.partially_implemented_requirement_ids.length,
				integrity_gap_count: coverage.integrity_gaps.length,
			},
			requirements,
			links,
			coverage,
			publish_config: publishConfig,
			last_publish_outcome: lastOutcome,
		}

		if (args.json) {
			yield* Console.log(JSON.stringify(payload, null, 2))
			return
		}

		yield* Console.log(
			`Spec summary: requirements=${requirements.length} links=${links.length} fully_implemented=${coverage.fully_implemented_requirement_ids.length} partial=${coverage.partially_implemented_requirement_ids.length} unlinked=${coverage.unlinked_requirement_ids.length} gaps=${coverage.integrity_gaps.length}`,
		)
		yield* Console.log("")

		for (const kind of ["functional", "acceptance", "other"] as const) {
			const subset = requirements.filter((requirement) => requirement.kind === kind)
			if (subset.length === 0) continue
			yield* Console.log(`${kind.toUpperCase()} (${subset.length})`)
			for (const requirement of subset) {
				yield* Console.log(`- ${formatSpecRequirementSummaryLine(requirement)}`)
				if (viewMode === "verbose") {
					yield* Console.log(`  ${compactSingleLineText(requirement.body)}`)
				}
			}
			yield* Console.log("")
		}

		if (links.length > 0) {
			yield* Console.log(`LINKS (${links.length})`)
			for (const link of links) {
				const requirementRef =
					link.requirement_external_code === null
						? link.requirement_local_id
						: `${link.requirement_local_id} (${link.requirement_external_code})`
				yield* Console.log(
					`- ${link.issue_id} -> ${requirementRef} [${link.link_type} fulfillment=${link.fulfillment_status}${link.fulfillment_percent === null ? "" : `:${link.fulfillment_percent}%`}]`,
				)
				if (viewMode === "verbose" && link.evidence_note !== null) {
					yield* Console.log(`  note: ${compactSingleLineText(link.evidence_note)}`)
				}
			}
			yield* Console.log("")
		}

		yield* Console.log(
			`Publish config: enabled=${publishConfig.enabled} debounce_ms=${publishConfig.debounce_ms} target_project=${publishConfig.target_project ?? "<unset>"}`,
		)
		if (lastOutcome !== undefined) {
			yield* Console.log(
				`Last publish: status=${lastOutcome.status} finished_at=${DateTime.formatIso(lastOutcome.finished_at)}`,
			)
		}
	})

const specLintHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly strict: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const specService = yield* SpecService
		const lintResult = yield* specService.lint(explicitProjectDir)

		if (args.json) {
			yield* Console.log(JSON.stringify(lintResult, null, 2))
		} else {
			yield* Console.log(
				`Lint ${lintResult.ok ? "ok" : "issues"}: requirements=${lintResult.requirement_count} linked=${lintResult.linked_requirement_count} unlinked=${lintResult.unlinked_requirement_count} gaps=${lintResult.integrity_gap_count}`,
			)
			for (const gap of lintResult.report.integrity_gaps) {
				yield* Console.log(`- [${gap.kind}] ${gap.message}`)
			}
		}

		if (args.strict && !lintResult.ok) {
			return yield* Effect.fail(new Error("Spec lint failed in strict mode."))
		}
	})

const parseSpecSyncTarget = (target: Option.Option<string>) =>
	Effect.gen(function* () {
		const normalized = Option.match(target, {
			onNone: () => "md",
			onSome: (value) => value.trim().toLowerCase(),
		})
		if (normalized === "md" || normalized === "markdown") return "md" as const
		if (normalized === "linear") return "linear" as const
		if (normalized === "all") return "all" as const
		return yield* Effect.fail(
			new Error(`Invalid sync target '${normalized}'. Expected one of: md, linear, all.`),
		)
	})

const specSyncHandler = (args: {
	readonly target: Option.Option<string>
	readonly outDir: Option.Option<string>
	readonly check: boolean
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)
		const target = yield* parseSpecSyncTarget(args.target)

		const specService = yield* SpecService
		if (args.check && target !== "md") {
			return yield* Effect.fail(
				new Error("--check is supported only for --target md (or default target)."),
			)
		}

		if (target === "linear") {
			const outcome = yield* specService.publish(explicitProjectDir)
			if (args.json) {
				yield* Console.log(JSON.stringify(outcome, null, 2))
				return
			}
			yield* Console.log(
				`Spec sync linear ${outcome.status}: requirements=${outcome.total_requirements} links=${outcome.total_links}`,
			)
			for (const documentOutcome of outcome.outcomes) {
				yield* Console.log(
					`- ${documentOutcome.document_key} [${documentOutcome.status}] ${documentOutcome.message}`,
				)
			}
			return
		}

		const syncResult = yield* specService.syncMarkdown(
			{
				outDir: Option.getOrUndefined(args.outDir),
				check: args.check,
			},
			explicitProjectDir,
		)
		if (target === "md") {
			if (args.json) {
				yield* Console.log(JSON.stringify(syncResult, null, 2))
			} else {
				yield* Console.log(
					`Spec sync md ${syncResult.check ? "check" : "write"}: changed=${syncResult.changed_documents}/${syncResult.total_documents} out_dir=${syncResult.out_dir}`,
				)
				for (const document of syncResult.documents) {
					yield* Console.log(`- ${document.key}: ${document.status} ${document.path}`)
				}
			}

			if (syncResult.check && !syncResult.ok) {
				return yield* Effect.fail(new Error("Spec markdown snapshots are out of sync."))
			}
			return
		}

		const outcome = yield* specService.publish(explicitProjectDir)
		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						target: "all",
						markdown: syncResult,
						linear: outcome,
					},
					null,
					2,
				),
			)
		} else {
			yield* Console.log(
				`Spec sync md write: changed=${syncResult.changed_documents}/${syncResult.total_documents} out_dir=${syncResult.out_dir}`,
			)
			for (const document of syncResult.documents) {
				yield* Console.log(`- ${document.key}: ${document.status} ${document.path}`)
			}
			yield* Console.log(
				`Spec sync linear ${outcome.status}: requirements=${outcome.total_requirements} links=${outcome.total_links}`,
			)
			for (const documentOutcome of outcome.outcomes) {
				yield* Console.log(
					`- ${documentOutcome.document_key} [${documentOutcome.status}] ${documentOutcome.message}`,
				)
			}
		}
	})

/**
 * Get spec requirement details
 */
const specReqGetHandler = (args: {
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const lookup = yield* resolveSpecRequirementLookupInput({
			reference: mergedRequirementRef,
			id: args.requirementId,
			localId: args.requirementLocalId,
			externalCode: args.requirementExternalCode,
		})

		const specService = yield* SpecService
		const requirement = yield* specService.getRequirement(
			lookup.reference,
			explicitProjectDir,
			lookup.selector,
		)
		if (requirement === undefined) {
			return yield* Effect.fail(new Error(`Spec requirement not found: ${lookup.reference}`))
		}
		const linkedIssues = yield* specService.listRequirementIssues(
			lookup.reference,
			explicitProjectDir,
			lookup.selector,
		)

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						...requirement,
						linked_issues: linkedIssues,
					},
					null,
					2,
				),
			)
			return
		}

		yield* Console.log(formatSpecRequirementSummaryLine(requirement))
		yield* Console.log("")
		yield* Console.log(`Body:\n${requirement.body}`)
		if (linkedIssues.length > 0) {
			yield* Console.log("")
			yield* Console.log("Linked Issues:")
			for (const issue of linkedIssues) {
				yield* Console.log(
					`${issue.id} [${issue.status ?? "unknown"} ${issue.issue_type ?? "task"}] (${issue.link_type} fulfillment=${issue.fulfillment_status}${issue.fulfillment_percent === null ? "" : `:${issue.fulfillment_percent}%`}) ${issue.title ?? ""}`.trimEnd(),
				)
			}
		}

		if (args.verbose) {
			yield* Console.error(`linkedIssues=${linkedIssues.length}`)
		}
	})

/**
 * Create spec requirement
 */
const specReqCreateHandler = (args: {
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly localId: Option.Option<string>
	readonly externalCode: Option.Option<string>
	readonly title: string
	readonly body: string
	readonly kind: Option.Option<string>
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const positionalRef = Option.getOrUndefined(mergedRequirementRef)?.trim()
		const optionLocalId = Option.getOrUndefined(args.localId)?.trim()
		const optionExternalCodeRaw = Option.getOrUndefined(args.externalCode)?.trim()
		const optionExternalCode =
			optionExternalCodeRaw === undefined || optionExternalCodeRaw.length === 0
				? undefined
				: normalizeSpecExternalCodeForCli(optionExternalCodeRaw)

		if (optionExternalCode !== undefined && !SPEC_EXTERNAL_CODE_PATTERN.test(optionExternalCode)) {
			return yield* Effect.fail(
				new Error(
					`Invalid external code '${optionExternalCodeRaw}'. Expected AZ-FR-####[a-z]? or AZ-AT-####[a-z]?.`,
				),
			)
		}

		let localId = optionLocalId
		let externalCode = optionExternalCode
		if (positionalRef !== undefined && positionalRef.length > 0) {
			if (optionLocalId !== undefined || optionExternalCode !== undefined) {
				return yield* Effect.fail(
					new Error(
						"Provide either positional requirement ref OR --local-id/--external-code options, not both.",
					),
				)
			}
			const normalizedPositionalExternal = normalizeSpecExternalCodeForCli(positionalRef)
			if (SPEC_EXTERNAL_CODE_PATTERN.test(normalizedPositionalExternal)) {
				externalCode = normalizedPositionalExternal
			} else {
				localId = positionalRef
			}
		}
		if (
			(localId === undefined || localId.length === 0) &&
			(externalCode === undefined || externalCode.length === 0)
		) {
			return yield* Effect.fail(
				new Error(
					"Missing requirement identifier. Provide a positional ref, --local-id, or --external-code.",
				),
			)
		}

		const kind = Option.match(args.kind, {
			onNone: () =>
				externalCode === undefined
					? undefined
					: inferSpecRequirementKindFromExternalCodeForCli(externalCode),
			onSome: (value): "functional" | "acceptance" | "other" | undefined => {
				const normalized = value.trim().toLowerCase()
				if (normalized === "functional") return "functional"
				if (normalized === "acceptance") return "acceptance"
				if (normalized === "other") return "other"
				return undefined
			},
		})
		if (Option.isSome(args.kind) && kind === undefined) {
			return yield* Effect.fail(
				new Error(`Invalid kind '${args.kind.value}'. Expected: functional, acceptance, other.`),
			)
		}

		const specService = yield* SpecService
		const created = yield* specService.createRequirement(
			{
				local_id: localId,
				external_code: externalCode,
				title: args.title,
				body: args.body,
				kind,
				status: Option.getOrUndefined(args.status),
				priority: Option.getOrUndefined(args.priority),
			},
			explicitProjectDir,
		)

		if (args.json) {
			yield* Console.log(JSON.stringify(created, null, 2))
			return
		}
		yield* Console.log(`Created spec requirement ${formatSpecRequirementReference(created)}`)
	})

/**
 * Update spec requirement
 */
const specReqUpdateHandler = (args: {
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly title: Option.Option<string>
	readonly body: Option.Option<string>
	readonly kind: Option.Option<string>
	readonly status: Option.Option<string>
	readonly priority: Option.Option<number>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const lookup = yield* resolveSpecRequirementLookupInput({
			reference: mergedRequirementRef,
			id: args.requirementId,
			localId: args.requirementLocalId,
			externalCode: args.requirementExternalCode,
		})

		const parsedKind = Option.match(args.kind, {
			onNone: () => undefined,
			onSome: (value): "functional" | "acceptance" | "other" | undefined => {
				const normalized = value.trim().toLowerCase()
				if (normalized === "functional") return "functional"
				if (normalized === "acceptance") return "acceptance"
				if (normalized === "other") return "other"
				return undefined
			},
		})
		if (Option.isSome(args.kind) && parsedKind === undefined) {
			return yield* Effect.fail(
				new Error(`Invalid kind '${args.kind.value}'. Expected: functional, acceptance, other.`),
			)
		}

		const fields = {
			title: Option.getOrUndefined(args.title),
			body: Option.getOrUndefined(args.body),
			kind: parsedKind,
			status: Option.getOrUndefined(args.status),
			priority: Option.getOrUndefined(args.priority),
		}
		const hasChanges = Object.values(fields).some((value) => value !== undefined)
		if (!hasChanges) {
			return yield* Effect.fail(
				new Error(
					"No fields provided. Use at least one --title/--body/--kind/--status/--priority.",
				),
			)
		}

		const specService = yield* SpecService
		const updated = yield* specService.updateRequirement(
			lookup.reference,
			fields,
			explicitProjectDir,
			lookup.selector,
		)
		if (!updated) {
			return yield* Effect.fail(new Error(`Spec requirement not found: ${lookup.reference}`))
		}

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						reference: lookup.reference,
						selector: lookup.selector,
						updated: true,
					},
					null,
					2,
				),
			)
			return
		}
		yield* Console.log(`Updated spec requirement ${lookup.reference}`)
	})

/**
 * Delete spec requirement
 */
const specReqDeleteHandler = (args: {
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const lookup = yield* resolveSpecRequirementLookupInput({
			reference: mergedRequirementRef,
			id: args.requirementId,
			localId: args.requirementLocalId,
			externalCode: args.requirementExternalCode,
		})

		const specService = yield* SpecService
		const deleted = yield* specService.deleteRequirement(
			lookup.reference,
			explicitProjectDir,
			lookup.selector,
		)
		if (!deleted) {
			return yield* Effect.fail(new Error(`Spec requirement not found: ${lookup.reference}`))
		}

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						reference: lookup.reference,
						selector: lookup.selector,
						deleted: true,
					},
					null,
					2,
				),
			)
			return
		}
		yield* Console.log(`Deleted spec requirement ${lookup.reference}`)
	})

/**
 * List spec links
 */
const specLinkListHandler = (args: {
	readonly issueId: Option.Option<string>
	readonly requirementRef: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly implementations: readonly string[]
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const issueId = yield* Option.match(args.issueId, {
			onNone: () => Effect.succeed<string | undefined>(undefined),
			onSome: (value) =>
				resolveCliIssueId(value, resolverCwd).pipe(Effect.map((resolved) => resolved)),
		})
		const requirementLookup = yield* Option.match(args.requirementRef, {
			onNone: () =>
				Option.isNone(args.requirementId) &&
				Option.isNone(args.requirementLocalId) &&
				Option.isNone(args.requirementExternalCode)
					? Effect.succeed<SpecRequirementLookupInput | undefined>(undefined)
					: resolveSpecRequirementLookupInput({
							reference: args.requirementRef,
							id: args.requirementId,
							localId: args.requirementLocalId,
							externalCode: args.requirementExternalCode,
						}).pipe(Effect.map((lookup) => lookup)),
			onSome: () =>
				resolveSpecRequirementLookupInput({
					reference: args.requirementRef,
					id: args.requirementId,
					localId: args.requirementLocalId,
					externalCode: args.requirementExternalCode,
				}).pipe(Effect.map((lookup) => lookup)),
		})
		const specService = yield* SpecService
		const hasImplementationFilter = args.implementations.length > 0
		const implementationFilter = hasImplementationFilter
			? yield* parseSpecImplementationsForCli(args.implementations)
			: undefined
		const links = yield* specService.listLinks(
			{
				issueId,
				requirementId: requirementLookup?.reference,
				requirementSelector: requirementLookup?.selector,
			},
			explicitProjectDir,
		)
		const filteredLinks =
			implementationFilter === undefined
				? links
				: links.filter((link) =>
						implementationFilter.some((implementation) =>
							link.implementations.includes(implementation),
						),
					)

		if (args.json) {
			yield* Console.log(JSON.stringify(filteredLinks, null, 2))
			return
		}
		if (filteredLinks.length === 0) {
			yield* Console.log("No spec links found.")
			return
		}
		for (const link of filteredLinks) {
			const requirementRef =
				link.requirement_external_code === null
					? link.requirement_local_id
					: `${link.requirement_local_id} (${link.requirement_external_code})`
			yield* Console.log(
				`${link.issue_id} -> ${requirementRef} [type=${link.link_type} fulfillment=${link.fulfillment_status}${link.fulfillment_percent === null ? "" : `:${link.fulfillment_percent}%`}] impl=${link.implementations.join(",")} id=${link.requirement_id} updated_at=${link.updated_at}${link.evidence_note === null ? "" : ` note=${link.evidence_note}`}`,
			)
		}
	})

/**
 * Add spec link
 */
const specLinkAddHandler = (args: {
	readonly issueId: Option.Option<string>
	readonly issueIdOption: Option.Option<string>
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly linkType: Option.Option<string>
	readonly implementations: readonly string[]
	readonly fulfillmentStatus: Option.Option<string>
	readonly fulfillmentPercent: Option.Option<number>
	readonly evidenceNote: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const mergedIssueId = yield* resolveRequiredAliasedTextInput({
			positional: args.issueId,
			optionValue: args.issueIdOption,
			positionalName: "issue-id",
			optionName: "--issue",
		})
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const issueId = yield* resolveCliIssueId(mergedIssueId, resolverCwd)
		const lookup = yield* resolveSpecRequirementLookupInput({
			reference: mergedRequirementRef,
			id: args.requirementId,
			localId: args.requirementLocalId,
			externalCode: args.requirementExternalCode,
		})
		const linkType = yield* Option.match(args.linkType, {
			onNone: () => Effect.succeed<RelationshipSpecLinkType>("relates"),
			onSome: (value) => {
				const parsed = parseRelationshipSpecLinkType(value)
				if (parsed === undefined) {
					return Effect.fail(
						new Error(
							`Invalid link type '${value}'. Expected one of: implements, tests, blocks, relates.`,
						),
					)
				}
				return Effect.succeed(parsed)
			},
		})
		const implementations = yield* parseOptionalImplementationListForCli(args.implementations)
		const fulfillmentStatus = yield* Option.match(args.fulfillmentStatus, {
			onNone: () => Effect.succeed<SpecLinkFulfillmentStatus>("planned"),
			onSome: (value) => {
				const parsed = parseSpecLinkFulfillmentStatus(value)
				if (parsed === undefined) {
					return Effect.fail(
						new Error(
							`Invalid fulfillment status '${value}'. Expected one of: planned, partial, complete, verified.`,
						),
					)
				}
				return Effect.succeed(parsed)
			},
		})
		const fulfillmentPercent = yield* parseSpecLinkFulfillmentPercent(args.fulfillmentPercent)
		const evidenceNote = parseSpecLinkEvidenceNote(args.evidenceNote)
		const specService = yield* SpecService
		yield* specService.addIssueLink(
			issueId,
			lookup.reference,
			linkType,
			explicitProjectDir,
			lookup.selector,
			implementations,
			{
				status: fulfillmentStatus,
				percent: fulfillmentPercent,
				evidenceNote,
			},
		)
		const matchingLinks = yield* specService.listLinks(
			{
				issueId,
				requirementId: lookup.reference,
				requirementSelector: lookup.selector,
			},
			explicitProjectDir,
		)
		const matchingLink = matchingLinks.find((link) => link.link_type === linkType)
		const effectiveImplementations = matchingLink?.implementations ?? implementations ?? []

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						issueId,
						requirement: lookup,
						type: linkType,
						fulfillment_status: fulfillmentStatus,
						fulfillment_percent: fulfillmentPercent ?? null,
						evidence_note: evidenceNote ?? null,
						implementations: effectiveImplementations,
						updated: true,
					},
					null,
					2,
				),
			)
			return
		}
		yield* Console.log(
			`Added spec link: ${issueId} -> ${lookup.reference} (${linkType}, fulfillment=${fulfillmentStatus}${fulfillmentPercent === undefined || fulfillmentPercent === null ? "" : `:${fulfillmentPercent}%`})`,
		)
		if (effectiveImplementations.length > 0) {
			yield* Console.log(`Implementations: ${effectiveImplementations.join(",")}`)
		}
	})

/**
 * Remove spec link
 */
const specLinkRemoveHandler = (args: {
	readonly issueId: Option.Option<string>
	readonly issueIdOption: Option.Option<string>
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly linkType: Option.Option<string>
	readonly implementations: readonly string[]
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const mergedIssueId = yield* resolveRequiredAliasedTextInput({
			positional: args.issueId,
			optionValue: args.issueIdOption,
			positionalName: "issue-id",
			optionName: "--issue",
		})
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const issueId = yield* resolveCliIssueId(mergedIssueId, resolverCwd)
		const lookup = yield* resolveSpecRequirementLookupInput({
			reference: mergedRequirementRef,
			id: args.requirementId,
			localId: args.requirementLocalId,
			externalCode: args.requirementExternalCode,
		})
		const linkType = Option.match(args.linkType, {
			onNone: () => undefined,
			onSome: (value) => parseRelationshipSpecLinkType(value),
		})
		if (Option.isSome(args.linkType) && linkType === undefined) {
			return yield* Effect.fail(
				new Error(
					`Invalid link type '${args.linkType.value}'. Expected one of: implements, tests, blocks, relates.`,
				),
			)
		}
		const implementations = yield* parseOptionalImplementationListForCli(args.implementations)
		const specService = yield* SpecService
		const removed = yield* specService.removeIssueLink(
			issueId,
			lookup.reference,
			linkType,
			explicitProjectDir,
			lookup.selector,
			implementations,
		)

		if (args.json) {
			yield* Console.log(JSON.stringify({ removed }, null, 2))
			return
		}
		yield* Console.log(`Removed ${removed} spec link(s).`)
	})

const specParityHandler = (args: {
	readonly implementation: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const issueTrackerClient = yield* IssueTrackerClient
		const registry = yield* issueTrackerClient.getImplementationRegistry(explicitProjectDir)
		const implementation = yield* resolveParityImplementationForCli(args.implementation, registry)
		const specService = yield* SpecService
		const report = yield* specService.getParityReport(implementation, explicitProjectDir)

		if (args.json) {
			yield* Console.log(JSON.stringify(report, null, 2))
			return
		}

		yield* Console.log(`implementation=${report.implementation}`)
		yield* Console.log(`total=${report.total_requirements}`)
		yield* Console.log(`implemented=${report.implemented_requirement_ids.length}`)
		yield* Console.log(`partial=${report.partially_implemented_requirement_ids.length}`)
		yield* Console.log(`tested=${report.tested_requirement_ids.length}`)
		yield* Console.log(`uncovered=${report.uncovered_requirement_ids.length}`)
		yield* Console.log(`related_only=${report.related_only_requirement_ids.length}`)
	})

/**
 * Update spec link fulfillment metadata
 */
const specLinkUpdateHandler = (args: {
	readonly issueId: Option.Option<string>
	readonly issueIdOption: Option.Option<string>
	readonly requirementRef: Option.Option<string>
	readonly requirementRefOption: Option.Option<string>
	readonly requirementId: Option.Option<string>
	readonly requirementLocalId: Option.Option<string>
	readonly requirementExternalCode: Option.Option<string>
	readonly linkType: Option.Option<string>
	readonly fulfillmentStatus: Option.Option<string>
	readonly fulfillmentPercent: Option.Option<number>
	readonly evidenceNote: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const mergedIssueId = yield* resolveRequiredAliasedTextInput({
			positional: args.issueId,
			optionValue: args.issueIdOption,
			positionalName: "issue-id",
			optionName: "--issue",
		})
		const mergedRequirementRef = yield* resolveOptionalAliasedTextInput({
			positional: args.requirementRef,
			optionValue: args.requirementRefOption,
			positionalName: "requirement-ref",
			optionName: "--req",
		})
		const issueId = yield* resolveCliIssueId(mergedIssueId, resolverCwd)
		const lookup = yield* resolveSpecRequirementLookupInput({
			reference: mergedRequirementRef,
			id: args.requirementId,
			localId: args.requirementLocalId,
			externalCode: args.requirementExternalCode,
		})
		const linkType = Option.match(args.linkType, {
			onNone: () => undefined,
			onSome: (value) => parseRelationshipSpecLinkType(value),
		})
		if (Option.isSome(args.linkType) && linkType === undefined) {
			return yield* Effect.fail(
				new Error(
					`Invalid link type '${args.linkType.value}'. Expected one of: implements, tests, blocks, relates.`,
				),
			)
		}

		const fulfillmentStatus = Option.match(args.fulfillmentStatus, {
			onNone: () => undefined,
			onSome: (value) => parseSpecLinkFulfillmentStatus(value),
		})
		if (Option.isSome(args.fulfillmentStatus) && fulfillmentStatus === undefined) {
			return yield* Effect.fail(
				new Error(
					`Invalid fulfillment status '${args.fulfillmentStatus.value}'. Expected one of: planned, partial, complete, verified.`,
				),
			)
		}
		const fulfillmentPercent = yield* parseSpecLinkFulfillmentPercent(args.fulfillmentPercent)
		const evidenceNote = parseSpecLinkEvidenceNote(args.evidenceNote)
		if (
			fulfillmentStatus === undefined &&
			fulfillmentPercent === undefined &&
			evidenceNote === undefined
		) {
			return yield* Effect.fail(
				new Error(
					"No fields provided. Use at least one --fulfillment-status/--fulfillment-percent/--evidence-note.",
				),
			)
		}

		const specService = yield* SpecService
		const updated = yield* specService.updateIssueLink(
			issueId,
			lookup.reference,
			{
				status: fulfillmentStatus,
				percent: fulfillmentPercent,
				evidenceNote,
			},
			linkType,
			explicitProjectDir,
			lookup.selector,
		)

		if (args.json) {
			yield* Console.log(
				JSON.stringify(
					{
						issueId,
						requirement: lookup,
						type: linkType ?? null,
						fulfillment_status: fulfillmentStatus ?? null,
						fulfillment_percent: fulfillmentPercent ?? null,
						evidence_note: evidenceNote ?? null,
						updated,
					},
					null,
					2,
				),
			)
			return
		}
		yield* Console.log(`Updated ${updated} spec link(s).`)
	})

/**
 * Run spec publish immediately
 */
const specPublishRunHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		if (!args.json) {
			yield* Console.log("Deprecated: use `az spec sync --target linear`.")
		}
		return yield* specSyncHandler({
			target: Option.some("linear"),
			outDir: Option.none(),
			check: false,
			projectDir: args.projectDir,
			json: args.json,
		})
	})

/**
 * Get spec publish config
 */
const specPublishConfigGetHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const specService = yield* SpecService
		const config = yield* specService.getPublishConfig(explicitProjectDir)
		const lastOutcome = yield* specService.getLastPublishOutcome(explicitProjectDir)
		const payload = {
			config,
			last_outcome: lastOutcome,
		}

		if (args.json) {
			yield* Console.log(JSON.stringify(payload, null, 2))
			return
		}

		yield* Console.log(`enabled=${config.enabled}`)
		yield* Console.log(`debounce_ms=${config.debounce_ms}`)
		yield* Console.log(`target_project=${config.target_project ?? "<unset>"}`)
		yield* Console.log(
			`documents=overview:"${config.documents.overview}", requirements:"${config.documents.requirements}", acceptance:"${config.documents.acceptance}", change_log:"${config.documents.change_log}"`,
		)
		if (lastOutcome) {
			yield* Console.log(
				`last_outcome=${lastOutcome.status} finished_at=${DateTime.formatIso(lastOutcome.finished_at)} requirements=${lastOutcome.total_requirements} links=${lastOutcome.total_links}`,
			)
		}
	})

/**
 * Set spec publish config
 */
const specPublishConfigSetHandler = (args: {
	readonly enabled: Option.Option<boolean>
	readonly debounceMs: Option.Option<number>
	readonly project: Option.Option<string>
	readonly overview: Option.Option<string>
	readonly requirements: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly changeLog: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* ensureSpecEnabled(resolverCwd)
		yield* validateIssueTrackerStore(resolverCwd)

		const specService = yield* SpecService
		const current = yield* specService.getPublishConfig(explicitProjectDir)

		const nextConfig = {
			enabled: Option.getOrElse(args.enabled, () => current.enabled),
			debounce_ms: Option.getOrElse(args.debounceMs, () => current.debounce_ms),
			target_project: Option.getOrElse(args.project, () => current.target_project),
			documents: {
				overview: Option.getOrElse(args.overview, () => current.documents.overview),
				requirements: Option.getOrElse(args.requirements, () => current.documents.requirements),
				acceptance: Option.getOrElse(args.acceptance, () => current.documents.acceptance),
				change_log: Option.getOrElse(args.changeLog, () => current.documents.change_log),
			},
		}
		if (nextConfig.debounce_ms < 0) {
			return yield* Effect.fail(new Error("--debounce-ms must be >= 0"))
		}

		yield* specService.setPublishConfig(nextConfig, explicitProjectDir)

		if (args.json) {
			yield* Console.log(JSON.stringify(nextConfig, null, 2))
			return
		}
		yield* Console.log("Updated spec publish config.")
	})

const specPublishConfigHandler = (args: {
	readonly action: Option.Option<string>
	readonly enabled: Option.Option<boolean>
	readonly debounceMs: Option.Option<number>
	readonly project: Option.Option<string>
	readonly overview: Option.Option<string>
	readonly requirements: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly changeLog: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const hasSetOptions =
			Option.getOrElse(args.enabled, () => false) ||
			Option.isSome(args.debounceMs) ||
			Option.isSome(args.project) ||
			Option.isSome(args.overview) ||
			Option.isSome(args.requirements) ||
			Option.isSome(args.acceptance) ||
			Option.isSome(args.changeLog)

		const requestedAction = Option.match(args.action, {
			onNone: () => (hasSetOptions ? "set" : "get"),
			onSome: (value) => value.toLowerCase(),
		})

		if (requestedAction !== "get" && requestedAction !== "set") {
			return yield* Effect.fail(
				new Error(
					`Invalid config action '${requestedAction}'. Use 'get' or 'set' (for example: az spec publish config get).`,
				),
			)
		}

		if (requestedAction === "get") {
			if (hasSetOptions) {
				return yield* Effect.fail(
					new Error(
						"Config update flags cannot be used with 'get'. Use `az spec publish config` or `az spec publish config set ...`.",
					),
				)
			}
			return yield* specPublishConfigGetHandler({
				projectDir: args.projectDir,
				json: args.json,
			})
		}

		return yield* specPublishConfigSetHandler({
			enabled: args.enabled,
			debounceMs: args.debounceMs,
			project: args.project,
			overview: args.overview,
			requirements: args.requirements,
			acceptance: args.acceptance,
			changeLog: args.changeLog,
			projectDir: args.projectDir,
			json: args.json,
		})
	})

const configSetHandler = (args: {
	readonly key: string
	readonly value: string
	readonly projectDir: Option.Option<string>
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const configPath = yield* resolveWritableConfigPath(explicitProjectDir)
		const currentConfig = yield* loadWritableConfig(configPath)
		const { nextConfig, renderedValue } = yield* setConfigValue(currentConfig, args.key, args.value)

		yield* saveWritableConfig(configPath, nextConfig)

		yield* Console.log(`Updated ${configPath}: ${args.key}=${renderedValue}`)
		if (args.key === "spec.enabled") {
			yield* Console.log(
				renderedValue === "true"
					? "Spec workflows are enabled."
					: "Spec workflows are disabled. `az prime` will stop mentioning spec and `az spec` commands will fail until re-enabled.",
			)
		}
	})

const configUsageHandler = (usage: string) => Console.log(usage)

/**
 * Run quality gates for a task's worktree
 */
const gateHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly fix: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)

		yield* Console.log(`Running quality gates for: ${issueId}`)

		// Find the worktree path for this task
		const sessionName = yield* findSessionByIssueId(issueId)
		let worktreePath = ""

		if (sessionName) {
			const wtCommand = PlatformCommand.make(
				"tmux",
				"display-message",
				"-t",
				sessionName,
				"-p",
				"#{pane_current_path}",
			)

			worktreePath = yield* PlatformCommand.string(wtCommand).pipe(
				Effect.map((s) => s.trim()),
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed("")),
					),
				),
			)
		}

		// If no active session, try to find worktree by convention
		if (!worktreePath) {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const parentDir = pathService.dirname(cwd)
			const projectName = pathService.basename(cwd)
			const expectedPath = pathService.join(parentDir, `${projectName}-${issueId}`)

			const exists = yield* fs.exists(expectedPath)
			if (exists) {
				worktreePath = expectedPath
			} else {
				yield* Console.error(`Could not find worktree for ${issueId}`)
				yield* Console.log(`Checked: ${expectedPath}`)
				yield* Console.log("Try running from within the worktree directory.")
				return yield* Effect.fail(new Error("Worktree not found"))
			}
		}

		yield* Console.log(`Worktree: ${worktreePath}`)
		yield* Console.log("")

		// Track results
		const results: { gate: string; passed: boolean; output: string }[] = []

		// Type-check
		yield* Console.log("▶ Type-check...")
		const typeCheckCmd = PlatformCommand.make("bun", "run", "type-check").pipe(
			PlatformCommand.workingDirectory(worktreePath),
		)
		const typeCheckResult = yield* PlatformCommand.string(typeCheckCmd).pipe(
			Effect.map((output) => ({ passed: true, output })),
			Effect.catchAll((e) =>
				Effect.logWarning(e).pipe(
					Effect.zipRight(Effect.succeed({ passed: false, output: String(e) })),
				),
			),
		)
		results.push({ gate: "type-check", ...typeCheckResult })
		yield* Console.log(typeCheckResult.passed ? "  ✓ Passed" : "  ✗ Failed")

		// Lint (with optional fix)
		const lintCmd = args.fix ? "fix" : "lint"
		yield* Console.log(`▶ Lint${args.fix ? " (with fix)" : ""}...`)
		const lintCommand = PlatformCommand.make("bun", "run", lintCmd).pipe(
			PlatformCommand.workingDirectory(worktreePath),
		)
		const lintResult = yield* PlatformCommand.string(lintCommand).pipe(
			Effect.map((output) => ({ passed: true, output })),
			Effect.catchAll((e) =>
				Effect.logWarning(e).pipe(
					Effect.zipRight(Effect.succeed({ passed: false, output: String(e) })),
				),
			),
		)
		results.push({ gate: "lint", ...lintResult })
		yield* Console.log(lintResult.passed ? "  ✓ Passed" : "  ✗ Failed (advisory)")

		// Test (if available)
		yield* Console.log("▶ Tests...")
		const testCommand = PlatformCommand.make("bun", "run", "test").pipe(
			PlatformCommand.workingDirectory(worktreePath),
		)
		const testResult = yield* PlatformCommand.string(testCommand).pipe(
			Effect.map((output) => ({ passed: true, output })),
			Effect.catchAll((e) => {
				const output = String(e)
				// "test" script not found is not a failure
				if (output.includes("not found") || output.includes("missing script")) {
					return Effect.logWarning(e).pipe(
						Effect.zipRight(Effect.succeed({ passed: true, output: "No test script" })),
					)
				}
				return Effect.logWarning(`Recovering after caught error: ${String(e)}`).pipe(
					Effect.zipRight(Effect.succeed({ passed: false, output })),
				)
			}),
		)
		results.push({ gate: "test", ...testResult })
		yield* Console.log(testResult.passed ? "  ✓ Passed" : "  ✗ Failed")

		// Build (if available)
		yield* Console.log("▶ Build...")
		const buildCommand = PlatformCommand.make("bun", "run", "build").pipe(
			PlatformCommand.workingDirectory(worktreePath),
		)
		const buildResult = yield* PlatformCommand.string(buildCommand).pipe(
			Effect.map((output) => ({ passed: true, output })),
			Effect.catchAll((e) => {
				const output = String(e)
				if (output.includes("not found") || output.includes("missing script")) {
					return Effect.logWarning(e).pipe(
						Effect.zipRight(Effect.succeed({ passed: true, output: "No build script" })),
					)
				}
				return Effect.logWarning(`Recovering after caught error: ${String(e)}`).pipe(
					Effect.zipRight(Effect.succeed({ passed: false, output })),
				)
			}),
		)
		results.push({ gate: "build", ...buildResult })
		yield* Console.log(buildResult.passed ? "  ✓ Passed" : "  ✗ Failed")

		// Summary
		yield* Console.log("")
		const passed = results.filter((r) => r.passed).length
		const total = results.length
		const allPassed = results.every((r) => r.passed)

		if (allPassed) {
			yield* Console.log(`✅ All gates passed (${passed}/${total})`)
		} else {
			yield* Console.log(`❌ Some gates failed (${passed}/${total})`)

			if (args.verbose) {
				yield* Console.log("")
				yield* Console.log("Failed gate details:")
				for (const r of results.filter((r) => !r.passed)) {
					yield* Console.log(`\n--- ${r.gate} ---`)
					yield* Console.log(r.output.slice(0, 500))
				}
			}
		}

		// Return exit code based on critical gates
		if (!typeCheckResult.passed) {
			return yield* Effect.fail(new Error("Type-check failed"))
		}
	})

/**
 * az prime - Print session primer for AI agents
 */
const normalizePrimeIssueId = (raw: string | undefined): string | undefined => {
	const trimmed = (raw ?? "").trim()
	if (trimmed.length === 0) {
		return undefined
	}
	return /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(trimmed) ? trimmed : undefined
}

const BRANCH_ISSUE_ID_PATTERN = /([A-Za-z][A-Za-z0-9]*-[0-9]+)/

const normalizeBranchIssueId = (candidate: string): string => {
	const [prefix, ...suffixParts] = candidate.split("-")
	const suffix = suffixParts.join("-")
	return `${prefix.toUpperCase()}-${suffix}`
}

const extractIssueIdFromBranchName = (branchName: string): string | undefined => {
	const normalizedBranchName = branchName.trim()
	if (normalizedBranchName.length === 0 || normalizedBranchName === "HEAD") {
		return undefined
	}

	const match = BRANCH_ISSUE_ID_PATTERN.exec(normalizedBranchName)
	if (!match || match[1] === undefined) {
		return undefined
	}

	return normalizeBranchIssueId(match[1])
}

type ParentContextSource =
	| "explicit-arg"
	| "explicit-parent-env"
	| "session-issue-env"
	| "branch-name"

interface ActiveParentContext {
	readonly issueId: string
	readonly source: ParentContextSource
}

const resolveActiveParentContext = (resolverCwd: string) =>
	Effect.gen(function* () {
		const explicitParentFromEnv = normalizePrimeIssueId(process.env.AZEDARACH_PARENT_ISSUE_ID)
		if (explicitParentFromEnv !== undefined) {
			const issueId = yield* resolveCliIssueId(explicitParentFromEnv, resolverCwd)
			return Option.some<ActiveParentContext>({
				issueId,
				source: "explicit-parent-env",
			})
		}

		const issueIdFromSessionEnv = normalizePrimeIssueId(process.env.AZEDARACH_ISSUE_ID)
		if (issueIdFromSessionEnv !== undefined) {
			const issueId = yield* resolveCliIssueId(issueIdFromSessionEnv, resolverCwd)
			return Option.some<ActiveParentContext>({
				issueId,
				source: "session-issue-env",
			})
		}

		const currentBranch = yield* PlatformCommand.string(
			PlatformCommand.make("git", "branch", "--show-current"),
		).pipe(
			Effect.map((output) => output.trim()),
			Effect.catchAll(() => Effect.succeed("")),
		)
		const issueIdFromBranch = extractIssueIdFromBranchName(currentBranch)
		if (issueIdFromBranch === undefined) {
			return Option.none<ActiveParentContext>()
		}

		const issueId = yield* resolveCliIssueId(issueIdFromBranch, resolverCwd)
		return Option.some<ActiveParentContext>({
			issueId,
			source: "branch-name",
		})
	})

const parseLabelsOption = (labels: Option.Option<string>): string[] | undefined =>
	Option.match(labels, {
		onNone: () => undefined,
		onSome: (value) =>
			value
				.split(",")
				.map((label) => label.trim())
				.filter((label) => label.length > 0),
	})

const parseIssueCreateStatusOption = (
	status: Option.Option<string>,
): Effect.Effect<"open" | "in_progress" | "blocked" | "closed" | undefined, Error> =>
	Option.match(status, {
		onNone: () => Effect.succeed(undefined),
		onSome: (value) => {
			switch (value) {
				case "open":
				case "in_progress":
				case "blocked":
				case "closed":
					return Effect.succeed(value)
				default:
					return Effect.fail(
						new Error(
							`Invalid --status value '${value}'. Expected one of: open, in_progress, blocked, closed.`,
						),
					)
			}
		},
	})

const IssueBulkCreateEntrySchema = Schema.Struct({
	title: Schema.String,
	type: Schema.String.pipe(Schema.optional),
	priority: Schema.Number.pipe(Schema.optional),
	description: Schema.String.pipe(Schema.optional),
	design: Schema.String.pipe(Schema.optional),
	acceptance: Schema.String.pipe(Schema.optional),
	assignee: Schema.String.pipe(Schema.optional),
	estimate: Schema.Number.pipe(Schema.optional),
	labels: Schema.Array(Schema.String).pipe(Schema.optional),
	implementations: Schema.Array(Schema.String).pipe(Schema.optional),
	parent: Schema.String.pipe(Schema.optional),
})

const IssueBulkCreatePayloadSchema = Schema.Union(
	Schema.Array(Schema.Unknown),
	Schema.Struct({
		issues: Schema.Array(Schema.Unknown),
	}),
)

type IssueBulkCreatePayload = Schema.Schema.Type<typeof IssueBulkCreatePayloadSchema>

interface IssueBulkCreateResult {
	readonly index: number
	readonly requestedTitle?: string
	readonly issueId?: string
	readonly created: boolean
	readonly error?: string
}

interface IssueBulkCreateSummary {
	readonly requestCount: number
	readonly createdCount: number
	readonly failedCount: number
	readonly results: readonly IssueBulkCreateResult[]
}

interface IssueBulkUpdateFields {
	readonly status?: string
	readonly notes?: string
	readonly priority?: number
	readonly title?: string
	readonly type?: string
	readonly description?: string
	readonly design?: string
	readonly acceptance?: string
	readonly assignee?: string
	readonly estimate?: number
	readonly labels?: string[]
	readonly implementations?: readonly string[]
	readonly parent?: string
}

const IssueBulkUpdateEntrySchema = Schema.Struct({
	id: Schema.String,
	status: Schema.String.pipe(Schema.optional),
	notes: Schema.String.pipe(Schema.optional),
	priority: Schema.Number.pipe(Schema.optional),
	title: Schema.String.pipe(Schema.optional),
	type: Schema.String.pipe(Schema.optional),
	description: Schema.String.pipe(Schema.optional),
	design: Schema.String.pipe(Schema.optional),
	acceptance: Schema.String.pipe(Schema.optional),
	assignee: Schema.String.pipe(Schema.optional),
	estimate: Schema.Number.pipe(Schema.optional),
	labels: Schema.Array(Schema.String).pipe(Schema.optional),
	implementations: Schema.Array(Schema.String).pipe(Schema.optional),
	parent: Schema.String.pipe(Schema.optional),
})

const IssueBulkUpdatePayloadSchema = Schema.Union(
	Schema.Array(IssueBulkUpdateEntrySchema),
	Schema.Struct({
		updates: Schema.Array(IssueBulkUpdateEntrySchema),
	}),
)

type IssueBulkUpdateEntry = Schema.Schema.Type<typeof IssueBulkUpdateEntrySchema>
type IssueBulkUpdatePayload = Schema.Schema.Type<typeof IssueBulkUpdatePayloadSchema>

interface IssueBulkUpdateResult {
	readonly index: number
	readonly requestedId: string
	readonly issueId: string
	readonly updated: boolean
	readonly error?: string
}

interface IssueBulkUpdateSummary {
	readonly requestCount: number
	readonly updatedCount: number
	readonly failedCount: number
	readonly results: readonly IssueBulkUpdateResult[]
}

const decodeIssueBulkCreatePayload = (content: string) =>
	Schema.decode(Schema.parseJson(IssueBulkCreatePayloadSchema))(content).pipe(
		Effect.flatMap((payload: IssueBulkCreatePayload) => {
			const entries = isIssueBulkCreateEntryArray(payload) ? payload : payload.issues
			return entries.length > 0
				? Effect.succeed(entries)
				: Effect.fail(new Error("Bulk create input must contain at least one issue item."))
		}),
	)

const isIssueBulkCreateEntryArray = (
	payload: IssueBulkCreatePayload,
): payload is readonly unknown[] => Array.isArray(payload)

const getIssueBulkCreateRequestedTitle = (entry: unknown): string | undefined => {
	if (typeof entry !== "object" || entry === null || Array.isArray(entry) || !("title" in entry)) {
		return undefined
	}

	const title = entry.title
	return typeof title === "string" && title.trim().length > 0 ? title : undefined
}

const decodeIssueBulkUpdatePayload = (content: string) =>
	Schema.decode(Schema.parseJson(IssueBulkUpdatePayloadSchema))(content).pipe(
		Effect.flatMap((payload: IssueBulkUpdatePayload) => {
			if (isIssueBulkUpdateEntryArray(payload)) {
				return payload.length > 0
					? Effect.succeed(payload)
					: Effect.fail(new Error("Bulk update input must contain at least one update item."))
			}

			return payload.updates.length > 0
				? Effect.succeed(payload.updates)
				: Effect.fail(new Error("Bulk update input must contain at least one update item."))
		}),
	)

const isIssueBulkUpdateEntryArray = (
	payload: IssueBulkUpdatePayload,
): payload is readonly IssueBulkUpdateEntry[] => Array.isArray(payload)

const mapIssueBulkUpdateFields = (entry: IssueBulkUpdateEntry): IssueBulkUpdateFields => ({
	status: entry.status,
	notes: entry.notes,
	priority: entry.priority,
	title: entry.title,
	type: entry.type,
	description: entry.description,
	design: entry.design,
	acceptance: entry.acceptance,
	assignee: entry.assignee,
	estimate: entry.estimate,
	labels: entry.labels === undefined ? undefined : [...entry.labels],
	implementations:
		entry.implementations !== undefined && entry.implementations.length > 0
			? [...entry.implementations]
			: undefined,
	parent: entry.parent,
})

const hasIssueBulkUpdateChanges = (fields: IssueBulkUpdateFields): boolean =>
	fields.status !== undefined ||
	fields.notes !== undefined ||
	fields.priority !== undefined ||
	fields.title !== undefined ||
	fields.type !== undefined ||
	fields.description !== undefined ||
	fields.design !== undefined ||
	fields.acceptance !== undefined ||
	fields.assignee !== undefined ||
	fields.estimate !== undefined ||
	fields.labels !== undefined ||
	fields.implementations !== undefined ||
	fields.parent !== undefined

const summarizeIssueBulkUpdateResults = (
	results: readonly IssueBulkUpdateResult[],
): IssueBulkUpdateSummary => {
	const updatedCount = results.filter((result) => result.updated).length
	return {
		requestCount: results.length,
		updatedCount,
		failedCount: results.length - updatedCount,
		results,
	}
}

const summarizeIssueBulkCreateResults = (
	results: readonly IssueBulkCreateResult[],
): IssueBulkCreateSummary => {
	const createdCount = results.filter((result) => result.created).length
	return {
		requestCount: results.length,
		createdCount,
		failedCount: results.length - createdCount,
		results,
	}
}

const formatIssueBulkCreateError = (error: unknown): string =>
	error instanceof Error ? error.message : String(error)

const formatIssueBulkUpdateError = (error: unknown): string =>
	error instanceof Error ? error.message : String(error)

const readIssueBulkInput = (inputPath: string, mode: "create" | "update") =>
	Effect.gen(function* () {
		if (inputPath === "-") {
			return yield* Effect.tryPromise({
				try: () => Bun.file("/dev/stdin").text(),
				catch: (error) =>
					new Error(
						`Failed to read bulk ${mode} JSON from stdin: ${
							mode === "create"
								? formatIssueBulkCreateError(error)
								: formatIssueBulkUpdateError(error)
						}`,
					),
			})
		}

		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const resolvedInputPath = pathService.resolve(inputPath)
		return yield* fs
			.readFileString(resolvedInputPath)
			.pipe(
				Effect.mapError(
					(error) =>
						new Error(
							`Failed to read bulk ${mode} JSON from ${resolvedInputPath}: ${String(error)}`,
						),
				),
			)
	})

const readIssueBulkCreateInput = (inputPath: string) => readIssueBulkInput(inputPath, "create")

const readIssueBulkUpdateInput = (inputPath: string) => readIssueBulkInput(inputPath, "update")

const isOpenChildForCloseGuard = (issue: TrackedIssue): boolean =>
	issue.status !== "closed" && issue.status !== "tombstone"

const formatCloseGuardMessage = (
	parentIssueId: string,
	openChildren: ReadonlyArray<TrackedIssue>,
	requestedReason: string | undefined,
): string => {
	const childIds = openChildren.map((child) => child.id)
	const nextReason =
		requestedReason === undefined || requestedReason.trim().length === 0
			? "Parent has no open children"
			: requestedReason
	return [
		`Refusing to close ${parentIssueId}: ${childIds.length} child issue(s) are still open (${childIds.join(", ")}).`,
		"Close or reparent children first, then retry:",
		`  az issue get ${parentIssueId}`,
		...childIds.map((childId) => `  az issue get ${childId}`),
		...childIds.map(
			(childId) => `  az issue close ${childId} --reason "Parent ${parentIssueId} is closing"`,
		),
		`  az issue close ${parentIssueId} --reason "${nextReason.replaceAll('"', '\\"')}"`,
	].join("\n")
}

const primeHandler = (_args: { readonly verbose: boolean }) =>
	Effect.gen(function* () {
		const issueId = normalizePrimeIssueId(process.env.AZEDARACH_ISSUE_ID)
		const appConfig = yield* AppConfig
		const specConfig = yield* appConfig.getSpecConfig()
		const implementationContext = yield* IssueTrackerClient.pipe(
			Effect.flatMap((issueTrackerClient) => issueTrackerClient.getImplementationRegistry()),
			Effect.map((registry) => ({
				implementations: registry.implementations.map((implementation) => ({
					name: implementation.name,
					description: implementation.description,
					directory: implementation.directory,
					is_default: implementation.is_default,
					is_builtin: implementation.is_builtin,
				})),
			})),
			Effect.catchAll(() => Effect.succeed(undefined)),
		)
		const showImplementations =
			implementationContext !== undefined && implementationContext.implementations.length > 1
		const issueContext =
			issueId === undefined
				? undefined
				: yield* IssueTrackerClient.pipe(
						Effect.flatMap((issueTrackerClient) => issueTrackerClient.show(issueId)),
						Effect.flatMap((issue) =>
							(specConfig.enabled
								? SpecService.pipe(
										Effect.flatMap((specService) =>
											specService.listIssueRequirements(issue.id, process.cwd()),
										),
										Effect.catchAll(() => Effect.succeed([])),
									)
								: Effect.succeed([])
							).pipe(
								Effect.map((linkedSpecRequirements) => ({
									issue,
									linkedSpecRequirements,
									showImplementations,
								})),
							),
						),
						Effect.catchAll(() => Effect.succeed(undefined)),
					)

		yield* Console.log(
			buildPrimeOutput(issueId, issueContext, implementationContext, specConfig.enabled),
		)
	})

const listTmuxSessionNames = Effect.gen(function* () {
	const listCommand = PlatformCommand.make("tmux", "list-sessions", "-F", "#{session_name}")
	const output = yield* PlatformCommand.string(listCommand).pipe(
		Effect.catchAll((error) =>
			Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
				Effect.zipRight(Effect.succeed("")),
			),
		),
	)

	return output
		.split("\n")
		.map((line) => line.trim())
		.filter((line) => line.length > 0)
})

const findSessionByIssueId = (issueId: string, projectPath: string = process.cwd()) =>
	Effect.gen(function* () {
		const canonicalSessionName = getIssueSessionName(issueId, projectPath)
		const checkCommand = PlatformCommand.make("tmux", "has-session", "-t", canonicalSessionName)
		const canonicalExitCode = yield* PlatformCommand.exitCode(checkCommand).pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(1)),
				),
			),
		)

		if (canonicalExitCode === 0) {
			return canonicalSessionName
		}

		const sessionNames = yield* listTmuxSessionNames
		for (const sessionName of sessionNames) {
			const parsed = parseIssueSessionName(sessionName, projectPath)
			if (parsed?.type === "issue" && issueIdsEqualForLookup(parsed.issueId, issueId)) {
				return sessionName
			}
		}

		return null
	})

const findAiSessionByIssueId = (issueId: string) =>
	Effect.gen(function* () {
		yield* Console.log(`[DEBUG] findAiSessionByIssueId: issueId=${issueId}`)

		const sessionName = yield* findSessionByIssueId(issueId)
		if (sessionName) {
			yield* Console.log(`[DEBUG] Found session: ${sessionName}`)
			return sessionName
		}

		yield* Console.log(`[DEBUG] No session found for issueId=${issueId}`)
		return null
	})

/**
 * Handle hook notifications from Claude Code sessions
 *
 * This command is called by Claude Code hooks configured in worktree's
 * .claude/settings.local.json. It updates a tmux session option that the
 * azedarach TUI can poll to detect session state.
 *
 * Uses tmux session option `@az_status` on the Claude session.
 * This is more reliable than file-based IPC with no race conditions.
 */
const notifyHandler = (args: {
	readonly event: string
	readonly issueId: string
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const projectPath = (yield* projectService.getCurrentPath()) ?? process.cwd()
		const issueId = yield* resolveCliIssueId(args.issueId, projectPath)

		// Validate event type using type guard
		if (!isValidHookEvent(args.event)) {
			yield* Console.error(`Invalid event type: ${args.event}`)
			yield* Console.error(`Valid events: ${VALID_HOOK_EVENTS.join(", ")}`)
			return yield* Effect.fail(new Error(`Invalid event: ${args.event}`))
		}

		const status = mapHookEventToTmuxStatus(args.event)

		// Find the session by issue ID (handles both new and legacy naming formats)
		const sessionName = yield* findAiSessionByIssueId(issueId)
		if (!sessionName) {
			if (args.verbose) {
				yield* Console.log(`No session found for ${issueId}`)
			}
			return
		}

		if (args.verbose) {
			yield* Console.log(`Hook: ${args.event} for ${issueId} → status: ${status}`)
		}

		yield* applyNotifyStatusToTmux(sessionName, status, args.verbose)

		if (args.verbose) {
			yield* Console.log(`Set @az_status=${status} on session ${sessionName}`)
		}
	})

/**
 * Install Azedarach hooks into the current project's .claude/settings.local.json
 *
 * This command is useful for:
 * - Setting up hooks in a non-worktree project
 * - Manually adding hooks to an existing settings.local.json
 * - Debugging hook configuration
 */
const hooksInstallHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)
		const claudeDir = pathService.join(cwd, ".claude")
		const settingsPath = pathService.join(claudeDir, "settings.local.json")

		// Ensure .claude directory exists
		const claudeDirExists = yield* fs.exists(claudeDir)
		if (!claudeDirExists) {
			yield* fs.makeDirectory(claudeDir, { recursive: true })
			if (args.verbose) {
				yield* Console.log(`Created .claude directory: ${claudeDir}`)
			}
		}

		// Read existing settings if they exist
		let existingSettings: Record<string, unknown> = {}
		const settingsExist = yield* fs.exists(settingsPath)
		if (settingsExist) {
			const content = yield* fs
				.readFileString(settingsPath)
				.pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed("{}")),
						),
					),
				)
			existingSettings = yield* Schema.decode(
				Schema.parseJson(Schema.Record({ key: Schema.String, value: Schema.Unknown })),
			)(content).pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed({})),
					),
				),
			)

			if (args.verbose) {
				yield* Console.log(`Read existing settings from: ${settingsPath}`)
			}
		}

		// Generate and merge hook configuration
		const hookConfig = generateHookConfig(issueId, { projectPath: process.cwd() })
		const mergedSettings = deepMerge(existingSettings, hookConfig)

		// Write merged settings
		yield* fs.writeFileString(settingsPath, JSON.stringify(mergedSettings, null, "\t"))

		yield* Console.log(`✓ Installed hooks for issue ${issueId}`)
		yield* Console.log(`  File: ${settingsPath}`)
		yield* Console.log(`  Events: pretooluse, permission_request, idle_prompt, stop, session_end`)

		if (args.verbose) {
			yield* Console.log("\nHook configuration:")
			yield* Console.log(JSON.stringify(hookConfig.hooks, null, 2))
		}
	})

/**
 * Add a new project to the registry
 */
const projectAddHandler = (args: {
	readonly path: string
	readonly name: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		// Resolve absolute path
		const absolutePath = pathService.resolve(args.path)

		// Validate path exists
		const exists = yield* fs.exists(absolutePath)
		if (!exists) {
			return yield* Effect.fail(new Error(`Path does not exist: ${absolutePath}`))
		}

		let tracker: "tracker" | "legacy" | "linear" | "local" = "local"
		const storagePaths = getProjectStoragePaths(absolutePath, pathService)
		const configPathCandidates = [
			storagePaths.canonicalConfigPath,
			storagePaths.legacyConfigPath,
		] as const
		let localConfigPath: string | null = null
		for (const candidatePath of configPathCandidates) {
			const existsForCandidate = yield* fs
				.exists(candidatePath)
				.pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(false)),
						),
					),
				)
			if (existsForCandidate) {
				localConfigPath = candidatePath
				break
			}
		}
		if (localConfigPath !== null) {
			const localConfigRaw = yield* fs
				.readFileString(localConfigPath)
				.pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed("")),
						),
					),
				)
			const decodedConfig = yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(
				localConfigRaw,
			).pipe(Effect.option)
			if (Option.isSome(decodedConfig)) {
				const issueTrackerConfig = decodedConfig.value.issueTracker
				if (issueTrackerConfig?.tracker !== undefined) tracker = "tracker"
				else if (issueTrackerConfig?.legacy !== undefined) tracker = "legacy"
				else if (issueTrackerConfig?.linear !== undefined) tracker = "linear"
				else tracker = "local"
			}
		}

		const issueStorePath = pathService.join(absolutePath, ".azedarach")
		if (tracker === "tracker" || tracker === "legacy") {
			const issueStoreExists = yield* fs.exists(issueStorePath)
			if (!issueStoreExists) {
				return yield* Effect.fail(
					new Error(
						`No .azedarach directory found in ${absolutePath}. Initialize issue tracking for this project, then retry with \`az issue\`.`,
					),
				)
			}
		}

		// Derive name from directory if not provided
		const projectName = Option.getOrElse(args.name, () => pathService.basename(absolutePath))

		if (args.verbose) {
			yield* Console.log(`Adding project: ${projectName}`)
			yield* Console.log(`  Path: ${absolutePath}`)
			yield* Console.log(`  Tracker: ${tracker}`)
			if (tracker === "tracker" || tracker === "legacy") {
				yield* Console.log(`  IssueTracker: ${issueStorePath}`)
			}
		}

		yield* ensureProjectAzedarachGitignore({
			projectPath: absolutePath,
			pathService,
			fs,
			verbose: args.verbose,
		})

		// Add project via ProjectService (provided by cliLayer)
		const projectService = yield* ProjectService
		yield* projectService.addProject({
			name: projectName,
			path: absolutePath,
			issueStorePath: tracker === "tracker" || tracker === "legacy" ? issueStorePath : undefined,
		})

		yield* Console.log(`Project '${projectName}' added successfully.`)
	})

/**
 * List all registered projects
 */
const projectListHandler = (args: { readonly verbose: boolean }) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const projects = yield* projectService.getProjects()
		const currentProject = yield* SubscriptionRef.get(projectService.currentProject)

		if (projects.length === 0) {
			yield* Console.log("No projects registered.")
			yield* Console.log("Use 'az project add <path>' to register a project.")
			return
		}

		yield* Console.log("Registered projects:")
		yield* Console.log("")

		for (const project of projects) {
			const isCurrent = currentProject?.name === project.name
			const marker = isCurrent ? "* " : "  "
			yield* Console.log(`${marker}${project.name}`)
			yield* Console.log(`    Path: ${project.path}`)
			if (project.issueStorePath && args.verbose) {
				yield* Console.log(`    IssueTracker: ${project.issueStorePath}`)
			}
			if (isCurrent) {
				yield* Console.log(`    (current)`)
			}
			yield* Console.log("")
		}

		if (!currentProject) {
			yield* Console.log("No current project selected.")
		}
	})

/**
 * Remove a project from the registry
 */
const projectRemoveHandler = (args: { readonly name: string; readonly verbose: boolean }) =>
	Effect.gen(function* () {
		if (args.verbose) {
			yield* Console.log(`Removing project: ${args.name}`)
		}

		const projectService = yield* ProjectService
		yield* projectService.removeProject(args.name)

		yield* Console.log(`Project '${args.name}' removed successfully.`)
	})

/**
 * Switch to a different project and set it as the default
 */
const projectSwitchHandler = (args: { readonly name: string; readonly verbose: boolean }) =>
	Effect.gen(function* () {
		if (args.verbose) {
			yield* Console.log(`Switching to project: ${args.name}`)
		}

		const projectService = yield* ProjectService
		yield* projectService.switchProject(args.name)
		yield* projectService.setDefaultProject(args.name)

		yield* Console.log(`Switched to project '${args.name}' and set as default.`)
	})

// ============================================================================
// Command Definitions
// ============================================================================

/**
 * Issue ID argument for commands that operate on a specific issue
 */
const issueIdArg = Args.text({ name: "issue-id" }).pipe(
	Args.withDescription("Issue ID (e.g., a, ab, 12, AZE-123, or shorthand suffix 123)"),
)

const dependsOnIssueIdArg = Args.text({ name: "depends-on-id" }).pipe(
	Args.withDescription("Issue ID this issue depends on (e.g., AZE-123 or shorthand suffix 123)"),
)

/**
 * az start <issue-id> - Start a new Claude session
 */
const startCommand = Command.make(
	"start",
	{
		issueId: issueIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
		noDaemon: noDaemonOption,
		config: configOption,
	},
	startHandler,
).pipe(Command.withDescription("Start a new Claude Code session for an issue"))

/**
 * az attach <issue-id> - Attach to existing session
 */
const attachCommand = Command.make(
	"attach",
	{
		issueId: issueIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	attachHandler,
).pipe(Command.withDescription("Attach to an existing Claude Code session"))

/**
 * az pause <issue-id> - Pause a running session
 */
const pauseCommand = Command.make(
	"pause",
	{
		issueId: issueIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	pauseHandler,
).pipe(Command.withDescription("Pause a running Claude Code session"))

/**
 * az kill <issue-id> - Kill a running session
 */
const killCommand = Command.make(
	"kill",
	{
		issueId: issueIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	killHandler,
).pipe(Command.withDescription("Kill a running Claude Code session"))

/**
 * az status - Show status of all sessions
 */
const statusCommand = Command.make(
	"status",
	{
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	statusHandler,
).pipe(Command.withDescription("Show status of all Claude Code sessions"))

/**
 * az sync - Sync issue tracker state
 */
const syncCommand = Command.make(
	"sync",
	{
		all: Options.boolean("all").pipe(
			Options.withAlias("a"),
			Options.withDescription("Sync all worktrees (not just current)"),
		),
		projectDir: projectDirArg,
		verbose: verboseOption,
		noDaemon: noDaemonOption,
	},
	syncHandler,
).pipe(Command.withDescription("Sync issue tracker state in worktrees"))

const daemonSyncCommand = Command.make(
	"sync",
	{
		intervalMs: Options.integer("interval-ms").pipe(
			Options.withAlias("i"),
			Options.optional,
			Options.withDescription("Sync loop interval in milliseconds"),
		),
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	daemonSyncHandler,
).pipe(Command.withDescription("Run headless backend sync daemon loop"))

const daemonStatusCommand = Command.make(
	"status",
	{
		watch: Options.boolean("watch").pipe(
			Options.withAlias("w"),
			Options.withDescription("Continuously consume daemon event stream updates"),
		),
		cursor: Options.integer("cursor").pipe(
			Options.optional,
			Options.withDescription("Start stream consumption from this cursor"),
		),
		streamBatchSize: Options.integer("stream-batch-size").pipe(
			Options.optional,
			Options.withDescription("Maximum daemon event stream entries per poll"),
		),
		streamWaitMs: Options.integer("stream-wait-ms").pipe(
			Options.optional,
			Options.withDescription("Long-poll wait duration for stream requests (milliseconds)"),
		),
		streamBatches: Options.integer("stream-batches").pipe(
			Options.optional,
			Options.withDescription("Stop watch mode after consuming N successful stream batches"),
		),
		verbose: verboseOption,
	},
	daemonStatusHandler,
).pipe(Command.withDescription("Show daemon runtime and sync status"))

const daemonHealthCommand = Command.make(
	"health",
	{
		verbose: verboseOption,
	},
	daemonHealthHandler,
).pipe(Command.withDescription("Show aggregated daemon health"))

const daemonStopCommand = Command.make(
	"stop",
	{
		verbose: verboseOption,
	},
	daemonStopHandler,
).pipe(Command.withDescription("Stop headless backend sync daemon runtime"))

const daemonRestartCommand = Command.make(
	"restart",
	{
		intervalMs: Options.integer("interval-ms").pipe(
			Options.withAlias("i"),
			Options.optional,
			Options.withDescription("Sync loop interval in milliseconds"),
		),
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	daemonRestartHandler,
).pipe(Command.withDescription("Restart headless backend sync daemon runtime"))

const daemonLogsCommand = Command.make(
	"logs",
	{
		lines: Options.integer("lines").pipe(
			Options.optional,
			Options.withDescription("Number of trailing log lines to show (default: 100)"),
		),
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	daemonLogsHandler,
).pipe(Command.withDescription("Show daemon operation logs"))

const daemonCommand = Command.make("daemon", {}, () =>
	Console.log(
		"Usage: az daemon <sync|status|health|stop|restart|logs> [--interval-ms <ms>] [--project-dir <path>]",
	),
).pipe(
	Command.withDescription("Headless backend daemon commands"),
	Command.withSubcommands([
		daemonSyncCommand,
		daemonStatusCommand,
		daemonHealthCommand,
		daemonStopCommand,
		daemonRestartCommand,
		daemonLogsCommand,
	]),
)

/**
 * az gate <issue-id> - Run quality gates for a task
 */
const gateCommand = Command.make(
	"gate",
	{
		issueId: issueIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
		fix: Options.boolean("fix").pipe(
			Options.withAlias("f"),
			Options.withDescription("Auto-fix lint issues"),
		),
	},
	gateHandler,
).pipe(Command.withDescription("Run quality gates (type-check, lint, test, build) for a task"))

/**
 * az prime - Print agent primer
 */
const primeCommand = Command.make(
	"prime",
	{
		verbose: verboseOption,
	},
	primeHandler,
).pipe(Command.withDescription("Print session primer for AI agents using az issue as task tracker"))

const issueTitleArg = Args.text({ name: "title" }).pipe(Args.withDescription("Issue title"))

const issueImplementationOption = Options.text("impl").pipe(
	Options.repeated,
	Options.withAlias("I"),
	Options.withDescription("Implementation assignment (repeatable)"),
)

/**
 * az issue list - List issues
 */
const issueListCommand = Command.make(
	"list",
	{
		status: Options.text("status").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Filter by status"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Filter by priority (1-5)"),
		),
		issueType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Filter by issue type"),
		),
		parent: Options.text("parent").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Filter by parent issue ID"),
		),
		implementations: issueImplementationOption,
		limit: Options.integer("limit").pipe(
			Options.withAlias("l"),
			Options.optional,
			Options.withDescription("Maximum number of issues to return"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output raw JSON"),
		),
	},
	issueListHandler,
).pipe(Command.withDescription("List issues sorted by most recently updated"))

/**
 * az issue get <issue-id> - Show issue details
 */
const issueGetCommand = Command.make(
	"get",
	{
		issueId: issueIdArg,
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output raw JSON"),
		),
		wait: Options.boolean("wait").pipe(
			Options.withAlias("w"),
			Options.withDescription("Wait for external sync before returning"),
		),
		maxWaitMs: Options.integer("max-wait-ms").pipe(
			Options.withAlias("m"),
			Options.optional,
			Options.withDescription("Maximum sync wait time in milliseconds"),
		),
	},
	issueGetHandler,
).pipe(Command.withDescription("Show full issue details"))

/**
 * az issue create <title> - Create issue
 */
const issueCreateCommand = Command.make(
	"create",
	{
		title: issueTitleArg,
		issueType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Issue type (task, bug, epic, chore)"),
		),
		status: Options.text("status").pipe(
			Options.optional,
			Options.withDescription("Initial issue status (open, in_progress, blocked, closed)"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Priority (1-5)"),
		),
		description: Options.text("description").pipe(
			Options.optional,
			Options.withAlias("d"),
			Options.withDescription("Issue description"),
		),
		design: Options.text("design").pipe(
			Options.withAlias("D"),
			Options.optional,
			Options.withDescription("Design/implementation notes"),
		),
		acceptance: Options.text("acceptance").pipe(
			Options.withAlias("a"),
			Options.optional,
			Options.withDescription("Acceptance criteria"),
		),
		assignee: Options.text("assignee").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Assignee value"),
		),
		estimate: Options.integer("estimate").pipe(
			Options.withAlias("e"),
			Options.optional,
			Options.withDescription("Estimate points/minutes (backend-specific)"),
		),
		labels: Options.text("labels").pipe(
			Options.withAlias("l"),
			Options.optional,
			Options.withDescription("Comma-separated labels"),
		),
		implementations: issueImplementationOption,
		deferred: Options.boolean("deferred").pipe(
			Options.withDescription("Create issue without inheriting active parent context"),
		),
		noDefaultParent: Options.boolean("no-default-parent").pipe(
			Options.withDescription("Deprecated alias for --deferred"),
		),
		parent: Options.text("parent").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Parent epic issue ID"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output raw JSON"),
		),
	},
	issueCreateHandler,
).pipe(Command.withDescription("Create a new issue"))

const issueChildCommand = Command.make(
	"child",
	{
		title: issueTitleArg,
		issueType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Issue type (task, bug, epic, chore)"),
		),
		status: Options.text("status").pipe(
			Options.optional,
			Options.withDescription("Initial issue status (open, in_progress, blocked, closed)"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Priority (1-5)"),
		),
		description: Options.text("description").pipe(
			Options.optional,
			Options.withAlias("d"),
			Options.withDescription("Issue description"),
		),
		design: Options.text("design").pipe(
			Options.withAlias("D"),
			Options.optional,
			Options.withDescription("Design/implementation notes"),
		),
		acceptance: Options.text("acceptance").pipe(
			Options.withAlias("a"),
			Options.optional,
			Options.withDescription("Acceptance criteria"),
		),
		assignee: Options.text("assignee").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Assignee value"),
		),
		estimate: Options.integer("estimate").pipe(
			Options.withAlias("e"),
			Options.optional,
			Options.withDescription("Estimate points/minutes (backend-specific)"),
		),
		labels: Options.text("labels").pipe(
			Options.withAlias("l"),
			Options.optional,
			Options.withDescription("Comma-separated labels"),
		),
		implementations: issueImplementationOption,
		parent: Options.text("parent").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Parent issue ID (defaults to active parent context)"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output raw JSON"),
		),
	},
	issueChildHandler,
).pipe(Command.withDescription("Create a child issue under active parent context"))

/**
 * az issue update <issue-id> - Update issue fields
 */
const issueUpdateCommand = Command.make(
	"update",
	{
		issueId: issueIdArg,
		status: Options.text("status").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Issue status"),
		),
		notes: Options.text("notes").pipe(
			Options.withAlias("n"),
			Options.optional,
			Options.withDescription("Issue notes"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Priority (1-5)"),
		),
		title: Options.text("title").pipe(
			Options.withAlias("T"),
			Options.optional,
			Options.withDescription("New title"),
		),
		issueType: Options.text("type").pipe(
			Options.withAlias("y"),
			Options.optional,
			Options.withDescription("Issue type"),
		),
		description: Options.text("description").pipe(
			Options.optional,
			Options.withAlias("d"),
			Options.withDescription("Issue description"),
		),
		design: Options.text("design").pipe(
			Options.withAlias("D"),
			Options.optional,
			Options.withDescription("Design/implementation notes"),
		),
		acceptance: Options.text("acceptance").pipe(
			Options.withAlias("a"),
			Options.optional,
			Options.withDescription("Acceptance criteria"),
		),
		assignee: Options.text("assignee").pipe(
			Options.withAlias("A"),
			Options.optional,
			Options.withDescription("Assignee value"),
		),
		estimate: Options.integer("estimate").pipe(
			Options.withAlias("e"),
			Options.optional,
			Options.withDescription("Estimate points/minutes (backend-specific)"),
		),
		labels: Options.text("labels").pipe(
			Options.withAlias("L"),
			Options.optional,
			Options.withDescription("Comma-separated labels (replaces labels)"),
		),
		implementations: issueImplementationOption,
		parent: Options.text("parent").pipe(
			Options.withAlias("P"),
			Options.optional,
			Options.withDescription("Parent epic issue ID"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON confirmation"),
		),
	},
	issueUpdateHandler,
).pipe(Command.withDescription("Update issue fields"))

const issueBulkUpdateCommand = Command.make(
	"bulk-update",
	{
		input: Options.text("input").pipe(
			Options.withAlias("i"),
			Options.withDescription("Path to bulk-update JSON payload, or - to read from stdin"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON summary"),
		),
	},
	issueBulkUpdateHandler,
).pipe(Command.withDescription("Update multiple issues from a JSON payload"))

const issueBulkCreateCommand = Command.make(
	"bulk-create",
	{
		input: Options.text("input").pipe(
			Options.withAlias("i"),
			Options.withDescription("Path to bulk-create JSON payload, or - to read from stdin"),
		),
		deferred: Options.boolean("deferred").pipe(
			Options.withDescription("Create issues without inheriting active parent context"),
		),
		noDefaultParent: Options.boolean("no-default-parent").pipe(
			Options.withDescription("Deprecated alias for --deferred"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON summary"),
		),
	},
	issueBulkCreateHandler,
).pipe(Command.withDescription("Create multiple issues from a JSON payload"))

/**
 * az issue dep add|remove <issue-id> <depends-on-id> - Manage dependency edge
 */
const issueDepAddCommand = Command.make(
	"add",
	{
		issueId: issueIdArg,
		dependsOnId: dependsOnIssueIdArg,
		dependencyType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription(
				"Dependency type (blocks, related, parent-child, discovered-from). Default: blocks",
			),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON confirmation"),
		),
	},
	issueDepAddHandler,
).pipe(Command.withDescription("Add a dependency edge between issues"))

const issueDepRemoveCommand = Command.make(
	"remove",
	{
		issueId: issueIdArg,
		dependsOnId: dependsOnIssueIdArg,
		dependencyType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription(
				"Optional dependency type filter (blocks, related, parent-child, discovered-from)",
			),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON confirmation"),
		),
	},
	issueDepRemoveHandler,
).pipe(Command.withDescription("Remove dependency edge(s) between issues"))

/**
 * az issue dep - Parent command for dependency edge operations
 */
const issueDepCommand = Command.make("dep", {}, () =>
	Console.log("Usage: az issue dep add|remove [--type <type>] <issue-id> <depends-on-id>"),
).pipe(
	Command.withDescription("Manage issue dependency edges"),
	Command.withSubcommands([issueDepAddCommand, issueDepRemoveCommand]),
)

/**
 * az issue close <issue-id> - Close issue
 */
const issueCloseCommand = Command.make(
	"close",
	{
		issueId: issueIdArg,
		reason: Options.text("reason").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Close reason"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON confirmation"),
		),
	},
	issueCloseHandler,
).pipe(Command.withDescription("Close an issue"))

/**
 * az issue delete <issue-id> --force - Delete issue
 */
const issueDeleteCommand = Command.make(
	"delete",
	{
		issueId: issueIdArg,
		force: Options.boolean("force").pipe(
			Options.withAlias("f"),
			Options.withDescription("Required: confirms irreversible deletion"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON confirmation"),
		),
	},
	issueDeleteHandler,
).pipe(Command.withDescription("Delete an issue (requires --force)"))

const issueCheckIssueIdArg = Args.text({ name: "issue-id" }).pipe(
	Args.optional,
	Args.withDescription("Parent issue ID (defaults to active parent context)"),
)

const issueCheckCommand = Command.make(
	"check",
	{
		issueId: issueCheckIssueIdArg,
		limit: Options.integer("limit").pipe(
			Options.optional,
			Options.withDescription("Maximum issues to scan (default: 200)"),
		),
		includeClosed: Options.boolean("include-closed").pipe(
			Options.withDescription("Include closed/tombstone issues in scan"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON diagnostics"),
		),
	},
	issueCheckHandler,
).pipe(Command.withDescription("Check for likely parent-child tracking misses"))

const issueDoctorCommand = Command.make(
	"doctor",
	{
		issueId: issueCheckIssueIdArg,
		limit: Options.integer("limit").pipe(
			Options.optional,
			Options.withDescription("Maximum issues to scan (default: 200)"),
		),
		includeClosed: Options.boolean("include-closed").pipe(
			Options.withDescription("Include closed/tombstone issues in scan"),
		),
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON diagnostics"),
		),
	},
	issueCheckHandler,
).pipe(Command.withDescription("Alias of `az issue check`"))

/**
 * az issue - Parent command for issue operations
 */
const issueCommand = Command.make("issue", {}, () =>
	Console.log("Use 'az issue --help' to see available issue commands"),
).pipe(
	Command.withDescription("Issue operations (alias: i)"),
	Command.withSubcommands([
		issueListCommand,
		issueGetCommand,
		issueCreateCommand,
		issueBulkCreateCommand,
		issueChildCommand,
		issueUpdateCommand,
		issueBulkUpdateCommand,
		issueDepCommand,
		issueCheckCommand,
		issueDoctorCommand,
		issueCloseCommand,
		issueDeleteCommand,
	]),
)

const implementationArg = Args.text({ name: "implementation" }).pipe(
	Args.withDescription("Implementation name (for example default or ts-opentui)"),
)

const implListCommand = Command.make(
	"list",
	{
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implListHandler,
).pipe(Command.withDescription("List registered implementations"))

const implGetCommand = Command.make(
	"get",
	{
		implementation: implementationArg,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implGetHandler,
).pipe(Command.withDescription("Show implementation details"))

const implAddCommand = Command.make(
	"add",
	{
		implementation: implementationArg,
		description: Options.text("description").pipe(
			Options.withAlias("d"),
			Options.optional,
			Options.withDescription("Optional implementation description"),
		),
		directory: Options.text("dir").pipe(
			Options.optional,
			Options.withDescription("Optional implementation directory metadata"),
		),
		setDefault: Options.boolean("default").pipe(
			Options.withDescription("Set the new implementation as the registry default"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implAddHandler,
).pipe(Command.withDescription("Add a named implementation"))

const implUpdateCommand = Command.make(
	"update",
	{
		implementation: implementationArg,
		rename: Options.text("rename").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Rename the implementation"),
		),
		description: Options.text("description").pipe(
			Options.withAlias("d"),
			Options.optional,
			Options.withDescription("Update the implementation description (pass empty string to clear)"),
		),
		directory: Options.text("dir").pipe(
			Options.optional,
			Options.withDescription(
				"Update implementation directory metadata (pass empty string to clear)",
			),
		),
		setDefault: Options.boolean("default").pipe(
			Options.withDescription("Set this implementation as the registry default"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implUpdateHandler,
).pipe(Command.withDescription("Update implementation metadata"))

const implDeleteCommand = Command.make(
	"delete",
	{
		implementation: implementationArg,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implDeleteHandler,
).pipe(Command.withDescription("Delete a named implementation"))

const implSetDefaultCommand = Command.make(
	"set-default",
	{
		implementation: implementationArg,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implSetDefaultHandler,
).pipe(Command.withDescription("Set the registry default implementation"))

const implSetEditorDefaultCommand = Command.make(
	"set-editor-default",
	{
		implementation: implementationArg,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implSetEditorDefaultHandler,
).pipe(Command.withDescription("Set the TUI issue editor default implementation"))

const implClearEditorDefaultCommand = Command.make(
	"clear-editor-default",
	{
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	implClearEditorDefaultHandler,
).pipe(Command.withDescription("Clear the TUI issue editor default implementation"))

const implCommand = Command.make("impl", {}, () =>
	Console.log(
		"Usage: az impl [list|get|add|update|delete|set-default|set-editor-default|clear-editor-default] ...",
	),
).pipe(
	Command.withDescription("Manage implementation registry metadata"),
	Command.withSubcommands([
		implListCommand,
		implGetCommand,
		implAddCommand,
		implUpdateCommand,
		implDeleteCommand,
		implSetDefaultCommand,
		implSetEditorDefaultCommand,
		implClearEditorDefaultCommand,
	]),
)

const requirementRefArg = Args.text({ name: "requirement-ref" }).pipe(
	Args.optional,
	Args.withDescription(
		"Spec requirement reference (auto-resolved by local_id, id, then external_code)",
	),
)

const requirementRefOption = Options.text("req").pipe(
	Options.withAlias("r"),
	Options.optional,
	Options.withDescription(
		"Spec requirement reference (alias for positional requirement-ref; auto selector)",
	),
)

const specLinkIssueIdArg = Args.text({ name: "issue-id" }).pipe(
	Args.optional,
	Args.withDescription("Issue ID (optional when --issue is provided)"),
)

const specLinkIssueIdOption = Options.text("issue").pipe(
	Options.withAlias("i"),
	Options.optional,
	Options.withDescription("Issue ID (alias for positional issue-id)"),
)

const requirementByIdOption = Options.text("id").pipe(
	Options.withAlias("I"),
	Options.optional,
	Options.withDescription("Lookup by internal opaque requirement id"),
)

const requirementByLocalIdOption = Options.text("local-id").pipe(
	Options.withAlias("l"),
	Options.withAlias("L"),
	Options.optional,
	Options.withDescription("Lookup by requirement local_id"),
)

const requirementByExternalCodeOption = Options.text("external-code").pipe(
	Options.withAlias("e"),
	Options.withAlias("E"),
	Options.optional,
	Options.withDescription("Lookup by requirement external code (for example AZ-FR-4201)"),
)

const specImplementationOption = Options.text("impl").pipe(
	Options.repeated,
	Options.withDescription("Implementation scope (repeatable). Default: default"),
)

const optionalSpecImplementationOption = Options.text("impl").pipe(
	Options.optional,
	Options.withDescription("Implementation scope. Default: default"),
)

const specUsageHandler = (usage: string) =>
	Effect.gen(function* () {
		yield* ensureSpecEnabled(process.cwd())
		yield* Console.log(usage)
	})

const specRequirementViewOption = Options.text("view").pipe(
	Options.optional,
	Options.withDescription("Display mode: compact|verbose"),
)

const specReqListCommand = Command.make(
	"list",
	{
		query: Options.text("query").pipe(
			Options.withAlias("q"),
			Options.optional,
			Options.withDescription("Filter by query against local_id/external_code/title/body"),
		),
		kind: Options.text("kind").pipe(
			Options.withAlias("k"),
			Options.optional,
			Options.withDescription("Filter by kind (functional|acceptance|other)"),
		),
		status: Options.text("status").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Filter by requirement status"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Filter by requirement priority"),
		),
		view: specRequirementViewOption,
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReqListHandler,
).pipe(Command.withDescription("List spec requirements"))

const specReqSearchCommand = Command.make(
	"search",
	{
		queryOption: Options.text("query").pipe(
			Options.withAlias("q"),
			Options.optional,
			Options.withDescription("Search query text (preferred over positional input)"),
		),
		kind: Options.text("kind").pipe(
			Options.withAlias("k"),
			Options.optional,
			Options.withDescription("Filter by kind (functional|acceptance|other)"),
		),
		status: Options.text("status").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Filter by requirement status"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Filter by requirement priority"),
		),
		view: specRequirementViewOption,
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
		query: Args.text({ name: "query" }).pipe(
			Args.optional,
			Args.withDescription("Search query text"),
		),
	},
	specReqSearchHandler,
).pipe(Command.withDescription("Search spec requirements"))

const specReqGetCommand = Command.make(
	"get",
	{
		requirementRef: requirementRefArg,
		requirementRefOption,
		requirementId: requirementByIdOption,
		requirementLocalId: requirementByLocalIdOption,
		requirementExternalCode: requirementByExternalCodeOption,
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReqGetHandler,
).pipe(Command.withDescription("Show spec requirement details"))

const specReqCreateCommand = Command.make(
	"create",
	{
		requirementRef: requirementRefArg,
		requirementRefOption,
		localId: requirementByLocalIdOption,
		externalCode: requirementByExternalCodeOption,
		title: Options.text("title").pipe(
			Options.withAlias("t"),
			Options.withDescription("Requirement title"),
		),
		body: Options.text("body").pipe(
			Options.withAlias("b"),
			Options.withDescription("Requirement body markdown"),
		),
		kind: Options.text("kind").pipe(
			Options.withAlias("k"),
			Options.optional,
			Options.withDescription("Requirement kind (functional|acceptance|other)"),
		),
		status: Options.text("status").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Requirement status"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Requirement priority"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReqCreateHandler,
).pipe(Command.withDescription("Create a spec requirement"))

const specReqUpdateCommand = Command.make(
	"update",
	{
		requirementRef: requirementRefArg,
		requirementRefOption,
		requirementId: requirementByIdOption,
		requirementLocalId: requirementByLocalIdOption,
		requirementExternalCode: requirementByExternalCodeOption,
		title: Options.text("title").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Requirement title"),
		),
		body: Options.text("body").pipe(
			Options.withAlias("b"),
			Options.optional,
			Options.withDescription("Requirement body markdown"),
		),
		kind: Options.text("kind").pipe(
			Options.withAlias("k"),
			Options.optional,
			Options.withDescription("Requirement kind (functional|acceptance|other)"),
		),
		status: Options.text("status").pipe(
			Options.withAlias("s"),
			Options.optional,
			Options.withDescription("Requirement status"),
		),
		priority: Options.integer("priority").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Requirement priority"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReqUpdateHandler,
).pipe(Command.withDescription("Update a spec requirement"))

const specReqDeleteCommand = Command.make(
	"delete",
	{
		requirementRef: requirementRefArg,
		requirementRefOption,
		requirementId: requirementByIdOption,
		requirementLocalId: requirementByLocalIdOption,
		requirementExternalCode: requirementByExternalCodeOption,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReqDeleteHandler,
).pipe(Command.withDescription("Delete a spec requirement"))

const specReqCommand = Command.make("req", {}, () =>
	specUsageHandler(
		"Usage: az spec req [list|search|get|create|update|delete] [--req <requirement-ref>|<requirement-ref>] [--id|--local-id|--external-code] ...",
	),
).pipe(
	Command.withDescription("Manage spec requirement records"),
	Command.withSubcommands([
		specReqListCommand,
		specReqSearchCommand,
		specReqGetCommand,
		specReqCreateCommand,
		specReqUpdateCommand,
		specReqDeleteCommand,
	]),
)

const specLinkListCommand = Command.make(
	"list",
	{
		issueId: Options.text("issue").pipe(
			Options.withAlias("i"),
			Options.optional,
			Options.withDescription("Filter by issue ID"),
		),
		requirementRef: Options.text("req").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Filter by requirement reference (auto selector)"),
		),
		requirementId: Options.text("req-id").pipe(
			Options.withAlias("I"),
			Options.optional,
			Options.withDescription("Filter by requirement internal id"),
		),
		requirementLocalId: Options.text("req-local-id").pipe(
			Options.withAlias("L"),
			Options.optional,
			Options.withDescription("Filter by requirement local_id"),
		),
		requirementExternalCode: Options.text("req-external-code").pipe(
			Options.withAlias("E"),
			Options.optional,
			Options.withDescription("Filter by requirement external code"),
		),
		implementations: specImplementationOption,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specLinkListHandler,
).pipe(Command.withDescription("List typed issue<->requirement links"))

const specLinkAddCommand = Command.make(
	"add",
	{
		issueId: specLinkIssueIdArg,
		issueIdOption: specLinkIssueIdOption,
		requirementRef: requirementRefArg,
		requirementRefOption,
		requirementId: requirementByIdOption,
		requirementLocalId: requirementByLocalIdOption,
		requirementExternalCode: requirementByExternalCodeOption,
		linkType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Link type (implements|tests|blocks|relates). Default: relates"),
		),
		implementations: specImplementationOption,
		fulfillmentStatus: Options.text("fulfillment-status").pipe(
			Options.withAlias("f"),
			Options.optional,
			Options.withDescription("Fulfillment status (planned|partial|complete|verified)"),
		),
		fulfillmentPercent: Options.integer("fulfillment-percent").pipe(
			Options.withAlias("F"),
			Options.optional,
			Options.withDescription("Optional fulfillment percentage (0-100)"),
		),
		evidenceNote: Options.text("evidence-note").pipe(
			Options.withAlias("N"),
			Options.optional,
			Options.withDescription("Optional fulfillment evidence note"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specLinkAddHandler,
).pipe(Command.withDescription("Add typed issue<->requirement link"))

const specLinkRemoveCommand = Command.make(
	"remove",
	{
		issueId: specLinkIssueIdArg,
		issueIdOption: specLinkIssueIdOption,
		requirementRef: requirementRefArg,
		requirementRefOption,
		requirementId: requirementByIdOption,
		requirementLocalId: requirementByLocalIdOption,
		requirementExternalCode: requirementByExternalCodeOption,
		linkType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Optional link type filter"),
		),
		implementations: specImplementationOption,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specLinkRemoveHandler,
).pipe(Command.withDescription("Remove typed issue<->requirement link"))

const specLinkUpdateCommand = Command.make(
	"update",
	{
		issueId: specLinkIssueIdArg,
		issueIdOption: specLinkIssueIdOption,
		requirementRef: requirementRefArg,
		requirementRefOption,
		requirementId: requirementByIdOption,
		requirementLocalId: requirementByLocalIdOption,
		requirementExternalCode: requirementByExternalCodeOption,
		linkType: Options.text("type").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Optional link type filter"),
		),
		fulfillmentStatus: Options.text("fulfillment-status").pipe(
			Options.withAlias("f"),
			Options.optional,
			Options.withDescription("Set fulfillment status (planned|partial|complete|verified)"),
		),
		fulfillmentPercent: Options.integer("fulfillment-percent").pipe(
			Options.withAlias("F"),
			Options.optional,
			Options.withDescription("Set fulfillment percentage (0-100)"),
		),
		evidenceNote: Options.text("evidence-note").pipe(
			Options.withAlias("N"),
			Options.optional,
			Options.withDescription("Set fulfillment evidence note"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specLinkUpdateHandler,
).pipe(Command.withDescription("Update typed issue<->requirement link fulfillment metadata"))

const specLinkCommand = Command.make("link", {}, () =>
	specUsageHandler(
		"Usage: az spec link [list|add|update|remove] [--issue <issue-id>|<issue-id>] [--req <requirement-ref>|<requirement-ref>] ...",
	),
).pipe(
	Command.withDescription("Manage typed issue/spec links"),
	Command.withSubcommands([
		specLinkListCommand,
		specLinkAddCommand,
		specLinkUpdateCommand,
		specLinkRemoveCommand,
	]),
)

const specParityCommand = Command.make(
	"parity",
	{
		implementation: optionalSpecImplementationOption,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specParityHandler,
).pipe(Command.withDescription("Report spec parity for a selected implementation"))

const specPublishRunCommand = Command.make(
	"run",
	{
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specPublishRunHandler,
).pipe(Command.withDescription("Deprecated alias for `az spec sync --target linear`"))

const specPublishConfigCommand = Command.make(
	"config",
	{
		enabled: Options.boolean("enabled").pipe(
			Options.withAlias("e"),
			Options.optional,
			Options.withDescription("Enable or disable auto-publish"),
		),
		debounceMs: Options.integer("debounce-ms").pipe(
			Options.withAlias("d"),
			Options.optional,
			Options.withDescription("Auto-publish debounce window in milliseconds"),
		),
		project: Options.text("project").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Target Linear project reference"),
		),
		overview: Options.text("doc-overview").pipe(
			Options.withAlias("o"),
			Options.optional,
			Options.withDescription("Spec Overview document title"),
		),
		requirements: Options.text("doc-requirements").pipe(
			Options.withAlias("r"),
			Options.optional,
			Options.withDescription("Requirements Index document title"),
		),
		acceptance: Options.text("doc-acceptance").pipe(
			Options.withAlias("a"),
			Options.optional,
			Options.withDescription("Acceptance Index document title"),
		),
		changeLog: Options.text("doc-change-log").pipe(
			Options.withAlias("c"),
			Options.optional,
			Options.withDescription("Change Log document title"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
		action: Args.text({ name: "action" }).pipe(
			Args.optional,
			Args.withDescription("Optional action: get | set"),
		),
	},
	specPublishConfigHandler,
).pipe(Command.withDescription("Inspect or update spec publish configuration"))

const specPublishCommand = Command.make("publish", {}, () =>
	specUsageHandler("Usage: az spec publish [run|config] ..."),
).pipe(
	Command.withDescription("Spec publish operations (deprecated export alias; use `az spec sync`)"),
	Command.withSubcommands([specPublishRunCommand, specPublishConfigCommand]),
)

const specReadCommand = Command.make(
	"read",
	{
		view: specRequirementViewOption,
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReadHandler,
).pipe(Command.withDescription("Show full spec in terminal-friendly form"))

const specLintCommand = Command.make(
	"lint",
	{
		strict: Options.boolean("strict").pipe(
			Options.withAlias("s"),
			Options.withDescription("Fail with non-zero exit when issues are found"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specLintHandler,
).pipe(Command.withDescription("Validate spec coverage and integrity"))

const specSyncCommand = Command.make(
	"sync",
	{
		target: Options.text("target").pipe(
			Options.withAlias("t"),
			Options.optional,
			Options.withDescription("Sync target: md|linear|all (default: md)"),
		),
		outDir: Options.text("out-dir").pipe(
			Options.withAlias("o"),
			Options.optional,
			Options.withDescription("Output directory for markdown snapshots (default: docs/spec)"),
		),
		check: Options.boolean("check").pipe(
			Options.withAlias("c"),
			Options.withDescription("Check for drift without writing files"),
		),
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specSyncHandler,
).pipe(Command.withDescription("Sync spec exports to markdown and/or Linear"))

const specCommand = Command.make("spec", {}, () =>
	specUsageHandler("Usage: az spec [req|link|parity|read|lint|sync|publish] ..."),
).pipe(
	Command.withDescription(
		"Spec requirement/link/read/lint/sync operations (`publish` remains as deprecated alias)",
	),
	Command.withSubcommands([
		specReqCommand,
		specLinkCommand,
		specLinkCommand,
		specParityCommand,
		specReadCommand,
		specLintCommand,
		specSyncCommand,
		specPublishCommand,
	]),
)

/**
 * Event argument for notify command
 */
const eventArg = Args.text({ name: "event" }).pipe(
	Args.withDescription("Hook event type: idle_prompt, stop, session_end"),
)

/**
 * Issue ID argument for notify/hook commands
 */
const issueIdArgForHooks = Args.text({ name: "issue-id" }).pipe(
	Args.withDescription("Issue ID for the session (e.g., a, 12, or AZE-123)"),
)

/**
 * az notify <event> <issue-id> - Handle Claude Code hook notifications
 *
 * Called by Claude Code hooks to notify Azedarach of session state changes.
 * Sets tmux session option that TmuxSessionMonitor polls.
 */
const notifyCommand = Command.make(
	"notify",
	{
		event: eventArg,
		issueId: issueIdArgForHooks,
		verbose: verboseOption,
	},
	notifyHandler,
).pipe(Command.withDescription("Handle Claude Code hook notifications (internal use)"))

/**
 * az hooks install <issue-id> - Install session state hooks
 *
 * Installs Azedarach hooks into .claude/settings.local.json for session state detection.
 * This is automatically done when creating worktrees, but can be run manually.
 */
const hooksInstallCommand = Command.make(
	"install",
	{
		issueId: issueIdArgForHooks,
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	hooksInstallHandler,
).pipe(Command.withDescription("Install session state hooks into .claude/settings.local.json"))

/**
 * az hooks - Parent command for hook management
 */
const hooksCommand = Command.make("hooks", {}, () =>
	Console.log("Usage: az hooks install <issue-id>"),
).pipe(
	Command.withDescription("Manage Claude Code hooks for session state detection"),
	Command.withSubcommands([hooksInstallCommand]),
)

// ============================================================================
// OpenCode Commands
// ============================================================================

/**
 * Default opencode.json configuration
 */
const DEFAULT_OPENCODE_CONFIG = {
	$schema: "https://opencode.ai/config.json",
	instructions: ["CLAUDE.md"],
	plugins: ["opencode-tracker"],
	theme: "tokyonight",
	permission: {
		bash: {
			"rg *": "allow",
			"fd *": "allow",
			"ls *": "allow",
			"git status": "allow",
			"git diff *": "allow",
			"git log *": "allow",
			"git branch *": "allow",
			"git add *": "allow",
			"git commit *": "allow",
			"tracker *": "allow",
			"tmux *": "allow",
		},
	},
	mcp: {
		"effect-docs": {
			type: "local",
			command: ["npx", "-y", "effect-mcp@latest"],
			enabled: true,
		},
	},
}

/**
 * Initialize OpenCode support in a project
 *
 * - Creates/updates opencode.json with recommended plugins
 * - Checks for globally installed opencode-az plugin
 */
const opencodeInitHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const opencodeJsonPath = pathService.join(cwd, "opencode.json")
		const configHome =
			process.env.XDG_CONFIG_HOME ??
			(process.env.HOME
				? pathService.join(process.env.HOME, ".config")
				: pathService.join(cwd, ".config"))
		const globalPluginDir = pathService.join(configHome, "opencode", "plugins")
		const globalPluginPath = pathService.join(globalPluginDir, OPENCODE_AZ_PLUGIN_FILENAME)

		yield* Console.log("🚀 Initializing OpenCode support...")
		yield* Console.log("")

		// Step 1: Create/update opencode.json
		let config = { ...DEFAULT_OPENCODE_CONFIG }
		const configExists = yield* fs.exists(opencodeJsonPath)
		if (configExists) {
			const existingContent = yield* fs.readFileString(opencodeJsonPath)
			const existingConfig: Readonly<Record<string, unknown>> = yield* Schema.decode(
				Schema.parseJson(Schema.Record({ key: Schema.String, value: Schema.Unknown })),
			)(existingContent).pipe(Effect.catchAll(() => Effect.succeed({})))

			// Merge plugins - existingConfig.plugins could be undefined or an array
			const pluginsValue = existingConfig.plugins
			const existingPlugins = Array.isArray(pluginsValue)
				? pluginsValue.filter((plugin): plugin is string => typeof plugin === "string")
				: []
			const newPlugins = [...new Set([...existingPlugins, "opencode-tracker"])]
			config = { ...existingConfig, ...config, plugins: newPlugins }

			yield* Console.log("✓ Updated existing opencode.json")
		} else {
			yield* Console.log("✓ Created opencode.json")
		}

		yield* fs.writeFileString(opencodeJsonPath, JSON.stringify(config, null, 2))

		if (args.verbose) {
			yield* Console.log(`  Plugins: ${config.plugins.join(", ")}`)
		}

		// Step 2: Check/install global opencode-az plugin
		const globalPluginExists = yield* fs.exists(globalPluginPath)
		if (!globalPluginExists) {
			yield* Console.log("")
			yield* Console.log("! Global opencode-az plugin not found")
			yield* Console.log("  Install with: az opencode plugin install")
		} else {
			yield* Console.log("✓ Global opencode-az plugin found")
		}

		// Summary
		yield* Console.log("")
		yield* Console.log("✅ OpenCode setup complete!")
		yield* Console.log("")
		yield* Console.log("Next steps:")
		yield* Console.log("  1. Install AZ plugin: az opencode plugin install")
		yield* Console.log("  2. Install opencode-tracker: npm install -g opencode-tracker")
		yield* Console.log("  3. Run: opencode")
	})

/**
 * az opencode init - Initialize OpenCode support
 */
const opencodeInitCommand = Command.make(
	"init",
	{
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	opencodeInitHandler,
).pipe(Command.withDescription("Initialize OpenCode support in a project"))

const opencodePluginInstallHandler = (args: {
	readonly globalDir: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const cwd = process.cwd()
		const defaultConfigHome =
			process.env.XDG_CONFIG_HOME ??
			(process.env.HOME
				? pathService.join(process.env.HOME, ".config")
				: pathService.join(cwd, ".config"))
		const defaultGlobalDir = pathService.join(defaultConfigHome, "opencode", "plugins")
		const globalDir = Option.getOrElse(args.globalDir, () => defaultGlobalDir)
		const globalPluginPath = pathService.join(globalDir, OPENCODE_AZ_PLUGIN_FILENAME)
		const legacyGlobalPluginPath = pathService.join(globalDir, "opencode-linear-cli.js")

		yield* fs.makeDirectory(globalDir, { recursive: true })

		const existingGlobalPlugin = yield* fs.exists(globalPluginPath)
		const needsGlobalWrite = existingGlobalPlugin
			? (yield* fs.readFileString(globalPluginPath)) !== OPENCODE_AZ_PLUGIN_SOURCE
			: true

		if (needsGlobalWrite) {
			yield* fs.writeFileString(globalPluginPath, OPENCODE_AZ_PLUGIN_SOURCE)
			yield* Console.log(`✓ Installed global plugin: ${globalPluginPath}`)
		} else {
			yield* Console.log(`✓ Global plugin already up to date: ${globalPluginPath}`)
		}

		if (yield* fs.exists(legacyGlobalPluginPath)) {
			yield* fs.remove(legacyGlobalPluginPath, { force: true }).pipe(Effect.ignore)
			yield* Console.log(`✓ Removed legacy global plugin: ${legacyGlobalPluginPath}`)
		}

		if (Option.isSome(args.projectDir)) {
			const projectPluginsDir = pathService.join(args.projectDir.value, ".opencode", "plugins")
			const projectPluginPath = pathService.join(projectPluginsDir, OPENCODE_AZ_PLUGIN_FILENAME)
			const legacyProjectPluginPath = pathService.join(projectPluginsDir, "opencode-linear-cli.js")

			yield* fs.makeDirectory(projectPluginsDir, { recursive: true })

			const existingProjectPlugin = yield* fs.exists(projectPluginPath)
			const needsProjectWrite = existingProjectPlugin
				? (yield* fs.readFileString(projectPluginPath)) !== OPENCODE_AZ_PLUGIN_SOURCE
				: true

			if (needsProjectWrite) {
				yield* fs.writeFileString(projectPluginPath, OPENCODE_AZ_PLUGIN_SOURCE)
				yield* Console.log(`✓ Installed project plugin: ${projectPluginPath}`)
			} else {
				yield* Console.log(`✓ Project plugin already up to date: ${projectPluginPath}`)
			}

			if (yield* fs.exists(legacyProjectPluginPath)) {
				yield* fs.remove(legacyProjectPluginPath, { force: true }).pipe(Effect.ignore)
				yield* Console.log(`✓ Removed legacy project plugin: ${legacyProjectPluginPath}`)
			}
		} else if (args.verbose) {
			yield* Console.log("i No --project-dir provided; only global plugin installed")
		}
	})

const opencodePluginInstallCommand = Command.make(
	"install",
	{
		globalDir: Options.directory("global-dir").pipe(
			Options.withAlias("g"),
			Options.optional,
			Options.withDescription("Global plugin directory (default: ~/.config/opencode/plugins)"),
		),
		projectDir: Options.directory("project-dir").pipe(
			Options.withAlias("p"),
			Options.optional,
			Options.withDescription("Optional project root to install .opencode/plugins/opencode-az.js"),
		),
		verbose: verboseOption,
	},
	opencodePluginInstallHandler,
).pipe(Command.withDescription("Install opencode-az plugin from embedded az CLI source"))

const opencodePluginCommand = Command.make("plugin", {}, () =>
	Console.log("Usage: az opencode plugin install [--global-dir <dir>] [--project-dir <dir>]"),
).pipe(
	Command.withDescription("Manage opencode-az plugin installation"),
	Command.withSubcommands([opencodePluginInstallCommand]),
)

/**
 * az opencode - Parent command for OpenCode integration
 */
const opencodeCommand = Command.make("opencode", {}, () =>
	Console.log("Usage: az opencode <init|plugin>"),
).pipe(
	Command.withDescription("OpenCode integration commands"),
	Command.withSubcommands([opencodeInitCommand, opencodePluginCommand]),
)

/**
 * Project path argument for project add command
 */
const projectPathArg = Args.text({ name: "path" }).pipe(
	Args.withDescription("Path to the project directory"),
)

/**
 * Project name argument for project commands
 */
const projectNameArg = Args.text({ name: "name" }).pipe(Args.withDescription("Project name"))

/**
 * Optional project name option for project add
 */
const projectNameOption = Options.text("name").pipe(
	Options.withAlias("n"),
	Options.optional,
	Options.withDescription("Project name (defaults to directory name)"),
)

/**
 * az project add <path> [--name <name>] - Register a new project
 */
const projectAddCommand = Command.make(
	"add",
	{
		path: projectPathArg,
		name: projectNameOption,
		verbose: verboseOption,
	},
	projectAddHandler,
).pipe(Command.withDescription("Register a new project"))

/**
 * az project list - Show all registered projects
 */
const projectListCommand = Command.make(
	"list",
	{
		verbose: verboseOption,
	},
	projectListHandler,
).pipe(Command.withDescription("Show all registered projects"))

/**
 * az project remove <name> - Unregister a project
 */
const projectRemoveCommand = Command.make(
	"remove",
	{
		name: projectNameArg,
		verbose: verboseOption,
	},
	projectRemoveHandler,
).pipe(Command.withDescription("Unregister a project"))

/**
 * az project switch <name> - Switch to a project and set as default
 */
const projectSwitchCommand = Command.make(
	"switch",
	{
		name: projectNameArg,
		verbose: verboseOption,
	},
	projectSwitchHandler,
).pipe(Command.withDescription("Switch to a project and set as default"))

/**
 * az project - Parent command for project management
 */
const projectCommand = Command.make("project", {}, () =>
	Console.log("Use 'az project --help' to see available subcommands"),
).pipe(
	Command.withSubcommands([
		projectAddCommand,
		projectListCommand,
		projectRemoveCommand,
		projectSwitchCommand,
	]),
	Command.withDescription("Manage multiple projects"),
)

const configKeyArg = Args.text({ name: "key" }).pipe(
	Args.withDescription("Config key (currently: spec.enabled)"),
)

const configValueArg = Args.text({ name: "value" }).pipe(Args.withDescription("Config value"))

const configSetCommand = Command.make(
	"set",
	{
		key: configKeyArg,
		value: configValueArg,
		projectDir: projectDirArg,
	},
	configSetHandler,
).pipe(
	Command.withDescription(
		"Set a supported project config key (for example: az config set spec.enabled false)",
	),
)

const configCommand = Command.make("config", {}, () =>
	configUsageHandler("Usage: az config set spec.enabled <true|false>"),
).pipe(
	Command.withDescription(
		"Inspect or update project config (for example, disable spec with 'az config set spec.enabled false')",
	),
	Command.withSubcommands([configSetCommand]),
)

// ============================================================================
// Top-level Shortcut Commands
// ============================================================================

/**
 * az add <path> - Top-level shortcut for az project add
 *
 * This allows users to run `az add /path/to/project` instead of
 * `az project add /path/to/project` for convenience.
 */
const addCommand = Command.make(
	"add",
	{
		path: projectPathArg,
		name: projectNameOption,
		verbose: verboseOption,
	},
	projectAddHandler,
).pipe(Command.withDescription("Register a new project (shortcut for 'az project add')"))

/**
 * az list - Top-level shortcut for az project list
 *
 * This allows users to run `az list` instead of `az project list` for convenience.
 */
const listCommand = Command.make(
	"list",
	{
		verbose: verboseOption,
	},
	projectListHandler,
).pipe(Command.withDescription("Show all registered projects (shortcut for 'az project list')"))

/**
 * Main CLI - combines all commands
 *
 * The parent command has its own handler that runs when `az` is called
 * without a subcommand. Subcommands (start, attach, etc.) have their own handlers.
 */
const az = Command.make(
	"az",
	{
		projectDir: projectDirArg,
		verbose: verboseOption,
		noDaemon: noDaemonOption,
		config: configOption,
	},
	defaultHandler,
).pipe(
	Command.withDescription(
		"Azedarach - TUI Kanban board for orchestrating parallel Claude Code sessions",
	),
)

/**
 * Full CLI with subcommands attached
 */
const cli = az.pipe(
	Command.withSubcommands([
		// Top-level shortcuts (most commonly used)
		addCommand,
		listCommand,
		configCommand,
		primeCommand,
		// Session management
		startCommand,
		attachCommand,
		pauseCommand,
		killCommand,
		statusCommand,
		syncCommand,
		daemonCommand,
		issueCommand,
		implCommand,
		specCommand,
		gateCommand,
		devCommand,
		// Internal/advanced commands
		notifyCommand,
		hooksCommand,
		projectCommand,
		opencodeCommand,
	]),
)

const devCommandPlaceholder = Command.make("dev", {}, () =>
	Console.error("Use `az dev --help` for dev server command usage."),
).pipe(Command.withDescription("Manage dev servers for issues"))

/**
 * Command-only CLI tree used for non-TUI subcommands.
 * Uses a lightweight `dev` placeholder because DevServerService is still coupled
 * to navigation/overlay services in the full runtime.
 */
const commandCli = az.pipe(
	Command.withSubcommands([
		addCommand,
		listCommand,
		configCommand,
		primeCommand,
		startCommand,
		attachCommand,
		pauseCommand,
		killCommand,
		statusCommand,
		syncCommand,
		daemonCommand,
		issueCommand,
		implCommand,
		specCommand,
		gateCommand,
		devCommandPlaceholder,
		notifyCommand,
		hooksCommand,
		projectCommand,
		opencodeCommand,
	]),
)

// ============================================================================
// CLI Runner
// ============================================================================
const buildFullCliLayerForArgv = (argv: ReadonlyArray<string>) => {
	const configPath = parseConfigPathFromArgv(argv)
	return createFullCliLayer(configPath)
}

const buildCommandCliLayerForArgv = (argv: ReadonlyArray<string>) => {
	const configPath = parseConfigPathFromArgv(argv)
	return createCommandCliLayer(configPath)
}

/**
 * CLI runner function - returns an Effect that still needs BunContext
 */
const cliRunner = (argv: ReadonlyArray<string>) => {
	const normalizedArgv = normalizeIssueOptionOrder(normalizeCliAliases(argv))
	const mode = resolveCliExecutionMode(normalizedArgv)
	const minimumLogLevel = hasVerboseFlag(normalizedArgv) ? LogLevel.Info : LogLevel.None
	const runEffect =
		mode === "dev-command"
			? Command.run(cli.pipe(Command.provide(buildFullCliLayerForArgv(normalizedArgv))), {
					name: "Azedarach",
					version: CLI_VERSION,
				})(normalizedArgv)
			: Command.run(commandCli.pipe(Command.provide(buildCommandCliLayerForArgv(normalizedArgv))), {
					name: "Azedarach",
					version: CLI_VERSION,
				})(normalizedArgv)
	return runEffect.pipe(
		Effect.provide(Logger.replaceScoped(Logger.defaultLogger, fileLogger)),
		Effect.provide(Logger.minimumLogLevel(minimumLogLevel)),
	)
}

export { cli }
export { commandCli }

/**
 * Export the layer for ManagedRuntime usage
 */
const cliLayer = fullCliLayer
export { cliLayer, commandCliLayer }

/**
 * Export the raw runner for ManagedRuntime pattern
 */
export {
	buildPrimeOutput,
	buildCommandCliLayerForArgv,
	cliRunner,
	decodeIssueBulkCreatePayload,
	decodeIssueBulkUpdatePayload,
	deriveWaitingAttentionPlan,
	findLikelyParentChildTrackingMisses,
	formatIssueDetailSections,
	formatParentChildCheckOutput,
	formatIssueSummaryLine,
	normalizeCliAliases,
	normalizeIssueOptionOrder,
	normalizeIssueJsonFlagOrder,
	resolveCliExecutionMode,
	summarizeIssueBulkCreateResults,
	summarizeIssueBulkUpdateResults,
}
