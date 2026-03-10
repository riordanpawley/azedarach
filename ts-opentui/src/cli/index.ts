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
import { AzedarachConfigSchema } from "../config/schema.js"
import { AttachmentService } from "../core/AttachmentService.js"
import { deepMerge, generateHookConfig } from "../core/hooks.js"
import { ImageAttachmentService } from "../core/ImageAttachmentService.js"
import { IssueEditorService } from "../core/IssueEditorService.js"
import { IssueTrackerClient, type Issue as TrackedIssue } from "../core/IssueTrackerClient.js"
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
import type { SpecRequirementLookupSelector } from "../core/specTypes.js"
import { TemplateService } from "../core/TemplateService.js"
import { TerminalService } from "../core/TerminalService.js"
import { TmuxService } from "../core/TmuxService.js"
import type { TmuxStatus } from "../core/TmuxSessionMonitor.js"
import { TmuxSessionMonitor } from "../core/TmuxSessionMonitor.js"
import { VCService } from "../core/VCService.js"
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
import { ProjectService } from "../services/ProjectService.js"
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

/**
 * Full CLI layer used for TUI launch and commands that still depend on
 * TUI-coupled services (for now, `az dev`).
 */
const fullCliLayer = Layer.mergeAll(
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
	AppConfig.Default,
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
const commandCliLayer = Layer.mergeAll(
	AppConfig.Default,
	ProjectService.Default,
	IssueTrackerClient.Default,
	SessionManager.Default,
	SpecService.Default,
).pipe(
	Layer.provide(Logger.replaceScoped(Logger.defaultLogger, fileLogger)),
	Layer.provideMerge(telemetryLayer),
	Layer.provideMerge(BunContext.layer),
)

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

/**
 * Config file path option
 */
const configOption = Options.file("config").pipe(
	Options.withAlias("c"),
	Options.optional,
	Options.withDescription("Path to config file (default: .azedarach.json)"),
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

// ============================================================================
// Command Handlers
// ============================================================================

/**
 * Default command - Launch TUI
 */
const defaultHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly config: Option.Option<string>
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())

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
const startHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly config: Option.Option<string>
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const issueId = yield* resolveCliIssueId(args.issueId, cwd)

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

		// Start the session using SessionManager (provided by cliLayer)
		const sessionManager = yield* SessionManager
		const session = yield* sessionManager.start({
			issueId,
			projectPath: cwd,
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

/**
 * Sync issue tracker state in current or all worktrees
 */
const syncHandler = (args: {
	readonly all: boolean
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())

		yield* Console.log("Syncing issue tracker state...")
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		// Validate issue tracker store
		yield* validateIssueTrackerStore(cwd)

		if (args.all) {
			// TODO: Sync all worktrees
			yield* Console.log("[Stub] Syncing all worktrees...")
			yield* Console.log("[Stub] Synced 3 worktrees")
		} else {
			// TODO: Sync current directory only
			yield* Console.log("[Stub] Syncing current directory...")
			yield* Console.log("[Stub] Pushed: 2, Pulled: 1")
		}
	})

type RelationshipDependencyType = "blocks" | "related" | "parent-child" | "discovered-from"
type RelationshipSpecLinkType = "implements" | "tests" | "blocks" | "relates"
type SpecRequirementLookupInput = {
	readonly reference: string
	readonly selector: SpecRequirementLookupSelector
}

const SPEC_EXTERNAL_CODE_PATTERN = /^AZ-(FR|AT)-\d{4}[A-Z]?$/i

const normalizeSpecExternalCodeForCli = (value: string): string =>
	value.trim().toUpperCase().replace(/\s+/g, "")

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

		yield* Console.log(formatIssueSummaryLine(issue))
		const detailSections = formatIssueDetailSections(issue, { linkedSpecRequirements })
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

		if (issues.length === 0) {
			yield* Console.log("No issues found.")
			return
		}

		for (const issue of issues) {
			yield* Console.log(formatIssueSummaryLine(issue))
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
	readonly priority: Option.Option<number>
	readonly description: Option.Option<string>
	readonly design: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly assignee: Option.Option<string>
	readonly estimate: Option.Option<number>
	readonly labels: Option.Option<string>
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

		const issueTrackerClient = yield* IssueTrackerClient
		const issue = yield* issueTrackerClient.create({
			title: args.title,
			type: Option.getOrUndefined(args.issueType),
			priority: Option.getOrUndefined(args.priority),
			description: Option.getOrUndefined(args.description),
			design: Option.getOrUndefined(args.design),
			acceptance: Option.getOrUndefined(args.acceptance),
			assignee: Option.getOrUndefined(args.assignee),
			estimate: Option.getOrUndefined(args.estimate),
			labels: parseLabelsOption(args.labels),
			parent: resolvedParent,
			cwd: explicitProjectDir,
		})

		if (args.json) {
			yield* Console.log(JSON.stringify(issue, null, 2))
			return
		}

		yield* Console.log(`Created issue ${issue.id}`)
		if (resolvedParent !== undefined) {
			yield* Console.log(`Parent: ${resolvedParent}`)
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
	readonly priority: Option.Option<number>
	readonly description: Option.Option<string>
	readonly design: Option.Option<string>
	readonly acceptance: Option.Option<string>
	readonly assignee: Option.Option<string>
	readonly estimate: Option.Option<number>
	readonly labels: Option.Option<string>
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

		const issueTrackerClient = yield* IssueTrackerClient
		const issue = yield* issueTrackerClient.create({
			title: args.title,
			type: Option.getOrUndefined(args.issueType),
			priority: Option.getOrUndefined(args.priority),
			description: Option.getOrUndefined(args.description),
			design: Option.getOrUndefined(args.design),
			acceptance: Option.getOrUndefined(args.acceptance),
			assignee: Option.getOrUndefined(args.assignee),
			estimate: Option.getOrUndefined(args.estimate),
			labels: parseLabelsOption(args.labels),
			parent: resolvedParent,
			cwd: explicitProjectDir,
		})

		if (args.json) {
			yield* Console.log(JSON.stringify(issue, null, 2))
			return
		}

		yield* Console.log(`Created child issue ${issue.id} under ${resolvedParent}`)
		if (args.verbose) {
			yield* Console.error(
				`status=${issue.status} priority=${issue.priority} type=${issue.issue_type}`,
			)
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
		if (args.json) {
			yield* Console.log(JSON.stringify({ id: issueId, updated: true }, null, 2))
			return
		}
		yield* Console.log(`Updated issue ${issueId}`)
		if (args.verbose) {
			yield* Console.error("Use `az issue get <issue-id>` to inspect the updated issue.")
		}
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

/**
 * List spec requirements
 */
const specReqListHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const specService = yield* SpecService
		const requirements = yield* specService.listRequirements(explicitProjectDir)

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
		}

		if (args.verbose) {
			yield* Console.error(`Listed ${requirements.length} requirement(s).`)
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
					`${issue.id} [${issue.status ?? "unknown"} ${issue.issue_type ?? "task"}] (${issue.link_type}) ${issue.title ?? ""}`.trimEnd(),
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
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
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
		const links = yield* specService.listLinks(
			{
				issueId,
				requirementId: requirementLookup?.reference,
				requirementSelector: requirementLookup?.selector,
			},
			explicitProjectDir,
		)

		if (args.json) {
			yield* Console.log(JSON.stringify(links, null, 2))
			return
		}
		if (links.length === 0) {
			yield* Console.log("No spec links found.")
			return
		}
		for (const link of links) {
			const requirementRef =
				link.requirement_external_code === null
					? link.requirement_local_id
					: `${link.requirement_local_id} (${link.requirement_external_code})`
			yield* Console.log(
				`${link.issue_id} -> ${requirementRef} [type=${link.link_type}] id=${link.requirement_id} updated_at=${link.updated_at}`,
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
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
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

		const specService = yield* SpecService
		yield* specService.addIssueLink(
			issueId,
			lookup.reference,
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
						type: linkType,
						updated: true,
					},
					null,
					2,
				),
			)
			return
		}
		yield* Console.log(`Added spec link: ${issueId} -> ${lookup.reference} (${linkType})`)
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
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
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

		const specService = yield* SpecService
		const removed = yield* specService.removeIssueLink(
			issueId,
			lookup.reference,
			linkType,
			explicitProjectDir,
			lookup.selector,
		)

		if (args.json) {
			yield* Console.log(JSON.stringify({ removed }, null, 2))
			return
		}
		yield* Console.log(`Removed ${removed} spec link(s).`)
	})

/**
 * Run spec publish immediately
 */
const specPublishRunHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const explicitProjectDir = Option.getOrUndefined(args.projectDir)
		const resolverCwd = Option.getOrElse(args.projectDir, () => process.cwd())
		yield* validateIssueTrackerStore(resolverCwd)

		const specService = yield* SpecService
		const outcome = yield* specService.publish(explicitProjectDir)
		if (args.json) {
			yield* Console.log(JSON.stringify(outcome, null, 2))
			return
		}

		yield* Console.log(
			`Publish ${outcome.status}: requirements=${outcome.total_requirements} links=${outcome.total_links}`,
		)
		for (const documentOutcome of outcome.outcomes) {
			yield* Console.log(
				`- ${documentOutcome.document_key} [${documentOutcome.status}] ${documentOutcome.message}`,
			)
		}
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
		const issueContext =
			issueId === undefined
				? ""
				: yield* PlatformCommand.string(PlatformCommand.make("az", "issue", "get", issueId)).pipe(
						Effect.map((output) => output.trim()),
						Effect.catchAll(() => Effect.succeed("")),
					)

		yield* Console.log(buildPrimeOutput(issueId, issueContext))
	})

/**
 * Valid hook event types from Claude Code
 */
const VALID_HOOK_EVENTS = [
	"user_prompt",
	"idle_prompt",
	"permission_request",
	"pretooluse",
	"stop",
	"session_end",
] as const
type HookEvent = (typeof VALID_HOOK_EVENTS)[number]

const AZ_STATUS_OPTION = "@az_status"
const AZ_WAITING_ALERTED_OPTION = "@az_waiting_alerted"
const BELL_CHAR = "\u0007"
const WAITING_WINDOW_BELL_STYLE = "fg=colour226,bg=colour237,bold"
const WAITING_WINDOW_ACTIVITY_STYLE = "fg=colour220,bg=colour237,bold"

/**
 * Type guard to check if a string is a valid hook event
 */
const isValidHookEvent = (event: string): event is HookEvent =>
	(VALID_HOOK_EVENTS as readonly string[]).includes(event)

/**
 * Map hook event to session status for tmux
 *
 * Converts detailed hook events to simple status values:
 * - busy: Claude is actively working
 * - waiting: Claude is waiting for user input
 * - idle: Session is inactive/ended
 */
const mapEventToStatus = (event: HookEvent): TmuxStatus => {
	switch (event) {
		case "user_prompt":
		case "pretooluse":
			return "busy"
		case "idle_prompt":
		case "permission_request":
		case "stop":
			return "waiting"
		case "session_end":
			return "idle"
	}
}

const setTmuxSessionOption = (
	sessionName: string,
	optionName: string,
	value: string,
	verbose: boolean,
) =>
	PlatformCommand.exitCode(
		PlatformCommand.make("tmux", "set-option", "-t", sessionName, optionName, value),
	).pipe(
		Effect.catchAll((error) =>
			Effect.logWarning(error).pipe(
				Effect.zipRight(
					verbose
						? Console.log(`Could not set tmux option ${optionName}: ${error}`).pipe(Effect.as(1))
						: Effect.succeed(1),
				),
			),
		),
	)

const getTmuxSessionOption = (sessionName: string, optionName: string) =>
	PlatformCommand.string(
		PlatformCommand.make("tmux", "show-option", "-t", sessionName, "-v", optionName),
	).pipe(
		Effect.map((value) => value.trim()),
		Effect.catchAll((error) =>
			Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
				Effect.zipRight(Effect.succeed("")),
			),
		),
	)

const ringSessionPaneBell = (sessionName: string) =>
	Effect.gen(function* () {
		const paneTty = yield* PlatformCommand.string(
			PlatformCommand.make("tmux", "display-message", "-p", "-t", sessionName, "#{pane_tty}"),
		).pipe(
			Effect.map((value) => value.trim()),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed("")),
				),
			),
		)
		if (paneTty.length === 0) {
			return false
		}

		const fs = yield* FileSystem.FileSystem
		return yield* fs.writeFileString(paneTty, BELL_CHAR).pipe(
			Effect.as(true),
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(false)),
				),
			),
		)
	})

const applyTmuxAttentionStyles = (sessionName: string, verbose: boolean) =>
	Effect.gen(function* () {
		// Keep bell monitoring + alert styles session-local so Az sessions stay readable
		// in native tmux pickers without changing the user's global theme.
		yield* setTmuxSessionOption(sessionName, "monitor-bell", "on", verbose)
		yield* setTmuxSessionOption(
			sessionName,
			"window-status-bell-style",
			WAITING_WINDOW_BELL_STYLE,
			verbose,
		)
		yield* setTmuxSessionOption(
			sessionName,
			"window-status-activity-style",
			WAITING_WINDOW_ACTIVITY_STYLE,
			verbose,
		)
	})

const applyTmuxWaitingAttentionSignal = (
	sessionName: string,
	status: TmuxStatus,
	verbose: boolean,
) =>
	Effect.gen(function* () {
		yield* applyTmuxAttentionStyles(sessionName, verbose)

		const currentFlag = yield* getTmuxSessionOption(sessionName, AZ_WAITING_ALERTED_OPTION)
		const plan = deriveWaitingAttentionPlan(status, currentFlag.length > 0 ? currentFlag : null)

		let nextFlag: "0" | "1" = plan.nextFlag
		if (plan.ringBell) {
			const bellSent = yield* ringSessionPaneBell(sessionName)
			if (!bellSent) {
				nextFlag = "0"
				if (verbose) {
					yield* Console.log(`Could not ring tmux bell for session ${sessionName}`)
				}
			}
		}

		yield* setTmuxSessionOption(sessionName, AZ_WAITING_ALERTED_OPTION, nextFlag, verbose)
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

		const status = mapEventToStatus(args.event)

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

		// Update tmux session option for the Claude session
		// The TUI can poll this with: tmux show-option -t <session> -v @az_status
		yield* setTmuxSessionOption(sessionName, AZ_STATUS_OPTION, status, args.verbose)
		yield* applyTmuxWaitingAttentionSignal(sessionName, status, args.verbose)

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
		const localConfigPath = pathService.join(absolutePath, ".azedarach.json")
		const hasLocalConfig = yield* fs
			.exists(localConfigPath)
			.pipe(
				Effect.catchAll((error) =>
					Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
						Effect.zipRight(Effect.succeed(false)),
					),
				),
			)
		if (hasLocalConfig) {
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
	},
	syncHandler,
).pipe(Command.withDescription("Sync issue tracker state in worktrees"))

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

/**
 * az issue dep add <issue-id> <depends-on-id> - Add dependency edge
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

/**
 * az issue dep - Parent command for dependency edge operations
 */
const issueDepCommand = Command.make("dep", {}, () =>
	Console.log("Usage: az issue dep add [--type <type>] <issue-id> <depends-on-id>"),
).pipe(
	Command.withDescription("Manage issue dependency edges"),
	Command.withSubcommands([issueDepAddCommand]),
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
		issueChildCommand,
		issueUpdateCommand,
		issueDepCommand,
		issueCheckCommand,
		issueDoctorCommand,
		issueCloseCommand,
		issueDeleteCommand,
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

const specReqListCommand = Command.make(
	"list",
	{
		projectDir: projectDirOption,
		verbose: verboseOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specReqListHandler,
).pipe(Command.withDescription("List spec requirements"))

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
	Console.log(
		"Usage: az spec req [list|get|create|update|delete] [--req <requirement-ref>|<requirement-ref>] [--id|--local-id|--external-code] ...",
	),
).pipe(
	Command.withDescription("Manage spec requirement records"),
	Command.withSubcommands([
		specReqListCommand,
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
		projectDir: projectDirOption,
		json: Options.boolean("json").pipe(
			Options.withAlias("j"),
			Options.withDescription("Output JSON"),
		),
	},
	specLinkRemoveHandler,
).pipe(Command.withDescription("Remove typed issue<->requirement link"))

const specLinkCommand = Command.make("link", {}, () =>
	Console.log(
		"Usage: az spec link [list|add|remove] [--issue <issue-id>|<issue-id>] [--req <requirement-ref>|<requirement-ref>] ...",
	),
).pipe(
	Command.withDescription("Manage typed issue/spec links"),
	Command.withSubcommands([specLinkListCommand, specLinkAddCommand, specLinkRemoveCommand]),
)

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
).pipe(Command.withDescription("Run one-way spec publish to Linear project documents"))

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
	Console.log("Usage: az spec publish [run|config] ..."),
).pipe(
	Command.withDescription("Spec publish operations"),
	Command.withSubcommands([specPublishRunCommand, specPublishConfigCommand]),
)

const specCommand = Command.make("spec", {}, () =>
	Console.log("Usage: az spec [req|link|publish] ..."),
).pipe(
	Command.withDescription("Spec requirement/link/publish operations"),
	Command.withSubcommands([specReqCommand, specLinkCommand, specPublishCommand]),
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
			const pluginsValue = existingConfig["plugins"]
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
		primeCommand,
		// Session management
		startCommand,
		attachCommand,
		pauseCommand,
		killCommand,
		statusCommand,
		syncCommand,
		issueCommand,
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
		primeCommand,
		startCommand,
		attachCommand,
		pauseCommand,
		killCommand,
		statusCommand,
		syncCommand,
		issueCommand,
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
	if (configPath === null) return fullCliLayer

	return Layer.mergeAll(
		fullCliLayer,
		Layer.succeed(
			AppConfigConfig,
			AppConfigConfig.make({
				configPath,
				projectPath: process.cwd(),
			}),
		),
	)
}

const buildCommandCliLayerForArgv = (argv: ReadonlyArray<string>) => {
	const configPath = parseConfigPathFromArgv(argv)
	if (configPath === null) return commandCliLayer

	return Layer.mergeAll(
		commandCliLayer,
		Layer.succeed(
			AppConfigConfig,
			AppConfigConfig.make({
				configPath,
				projectPath: process.cwd(),
			}),
		),
	)
}

/**
 * CLI runner function - returns an Effect that still needs BunContext
 */
const cliRunner = (argv: ReadonlyArray<string>) => {
	const normalizedArgv = normalizeIssueOptionOrder(normalizeCliAliases(argv))
	const mode = resolveCliExecutionMode(normalizedArgv)
	const minimumLogLevel = hasVerboseFlag(normalizedArgv) ? LogLevel.Info : LogLevel.None
	const runEffect =
		mode === "command"
			? Command.run(commandCli.pipe(Command.provide(buildCommandCliLayerForArgv(normalizedArgv))), {
					name: "Azedarach",
					version: CLI_VERSION,
				})(normalizedArgv)
			: Command.run(cli.pipe(Command.provide(buildFullCliLayerForArgv(normalizedArgv))), {
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
	cliRunner,
	deriveWaitingAttentionPlan,
	findLikelyParentChildTrackingMisses,
	formatIssueDetailSections,
	formatParentChildCheckOutput,
	formatIssueSummaryLine,
	normalizeCliAliases,
	normalizeIssueOptionOrder,
	normalizeIssueJsonFlagOrder,
	resolveCliExecutionMode,
}
