/**
 * CLI Definition for Azedarach
 *
 * Uses @effect/cli for type-safe command parsing and validation.
 * Provides commands for managing Claude Code sessions via TUI and direct control.
 */

import * as path from "node:path"
import { Args, Command, Options } from "@effect/cli"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, SubscriptionRef } from "effect"
import { AppConfigConfig } from "../config/AppConfig.js"
import { SessionManager } from "../core/SessionManager.js"
import { ProjectService } from "../services/ProjectService.js"

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
 * Validate that beads database exists in the project
 */
const validateBeadsDatabase = (projectDir: string) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const path = yield* Path.Path
		const beadsDir = path.join(projectDir, ".beads")

		const exists = yield* fs.exists(beadsDir)
		if (!exists) {
			return yield* Effect.fail(
				new Error(
					`No .beads directory found in ${projectDir}. Run 'bd init' to initialize beads tracking.`,
				),
			)
		}

		yield* Console.log(`Using beads database: ${beadsDir}`)
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

		// Validate beads database
		yield* validateBeadsDatabase(cwd)

		// Launch TUI
		const { launchTUI } = yield* Effect.promise(() => import("../ui/launch.js"))
		yield* Effect.promise(() => launchTUI())
	})

/**
 * Start a new Claude session for a beads issue
 */
const startHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly config: Option.Option<string>
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const configPath = Option.getOrUndefined(args.config)

		yield* Console.log(`Starting Claude session for issue: ${args.issueId}`)
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
			if (configPath) {
				yield* Console.log(`Using config: ${configPath}`)
			}
		}

		// Validate beads database
		yield* validateBeadsDatabase(cwd)

		// Create the combined layer with config and platform dependencies
		const appConfigLayer = AppConfigConfig.Default(cwd, configPath)
		const fullLayer = Layer.provideMerge(
			SessionManager.Default,
			Layer.merge(appConfigLayer, BunContext.layer),
		)

		// Start the session using SessionManager
		const session = yield* Effect.gen(function* () {
			const sessionManager = yield* SessionManager
			return yield* sessionManager.start({
				beadId: args.issueId,
				projectPath: cwd,
			})
		}).pipe(Effect.provide(fullLayer))

		yield* Console.log(`Session started successfully!`)
		yield* Console.log(`  Worktree: ${session.worktreePath}`)
		yield* Console.log(`  tmux session: ${session.tmuxSessionName}`)
		yield* Console.log(``)
		yield* Console.log(`To attach: az attach ${args.issueId}`)
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

		yield* Console.log(`Attaching to session for issue: ${args.issueId}`)
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		// Validate beads database
		yield* validateBeadsDatabase(cwd)

		// TODO: Implement session attachment
		yield* Console.log("[Stub] Checking if session exists...")
		yield* Console.log("[Stub] Attaching to tmux session...")
		yield* Console.log(`[Stub] Run: tmux attach-session -t az-${args.issueId}`)
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

		yield* Console.log(`Pausing session for issue: ${args.issueId}`)
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		// Validate beads database
		yield* validateBeadsDatabase(cwd)

		// TODO: Implement session pause
		yield* Console.log("[Stub] Sending Ctrl+C to session...")
		yield* Console.log("[Stub] Session paused. Use 'az attach' to resume.")
	})

/**
 * Show status of all sessions
 */
const statusHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())

		yield* Console.log("Session Status")
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		// Validate beads database
		yield* validateBeadsDatabase(cwd)

		// TODO: Implement status display
		yield* Console.log("[Stub] Active sessions:")
		yield* Console.log("  az-2qy (CLI parsing) - WAITING")
		yield* Console.log("  az-05y (BeadsClient) - DONE")
		yield* Console.log("  az-1a3 (TUI setup)   - ERROR")
	})

/**
 * Sync beads database in current or all worktrees
 */
const syncHandler = (args: {
	readonly all: boolean
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())

		yield* Console.log("Syncing beads database...")
		yield* Console.log(`Project: ${cwd}`)

		if (args.verbose) {
			yield* Console.log("Verbose mode enabled")
		}

		// Validate beads database
		yield* validateBeadsDatabase(cwd)

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

/**
 * Valid hook event types from Claude Code
 */
const VALID_HOOK_EVENTS = ["idle_prompt", "stop", "session_end"] as const
type HookEvent = (typeof VALID_HOOK_EVENTS)[number]

/**
 * Handle hook notifications from Claude Code sessions
 *
 * This command is called by Claude Code hooks configured in worktree's
 * .claude/settings.local.json. It writes a notification file that the
 * HookReceiver service in the main Azedarach process watches.
 */
const notifyHandler = (args: {
	readonly event: string
	readonly beadId: string
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem

		// Validate event type
		if (!VALID_HOOK_EVENTS.includes(args.event as HookEvent)) {
			yield* Console.error(`Invalid event type: ${args.event}`)
			yield* Console.error(`Valid events: ${VALID_HOOK_EVENTS.join(", ")}`)
			return yield* Effect.fail(new Error(`Invalid event: ${args.event}`))
		}

		if (args.verbose) {
			yield* Console.log(`Received hook event: ${args.event} for ${args.beadId}`)
		}

		// Write notification file for HookReceiver to pick up
		const notifyPath = `/tmp/azedarach-notify-${args.beadId}.json`
		const payload = JSON.stringify({
			event: args.event,
			beadId: args.beadId,
			timestamp: Date.now(),
		})

		yield* fs.writeFileString(notifyPath, payload)

		if (args.verbose) {
			yield* Console.log(`Wrote notification to: ${notifyPath}`)
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

		// Validate .beads directory exists
		const beadsPath = pathService.join(absolutePath, ".beads")
		const beadsExists = yield* fs.exists(beadsPath)
		if (!beadsExists) {
			return yield* Effect.fail(
				new Error(
					`No .beads directory found in ${absolutePath}. Run 'bd init' to initialize beads tracking.`,
				),
			)
		}

		// Derive name from directory if not provided
		const projectName = Option.getOrElse(args.name, () => path.basename(absolutePath))

		if (args.verbose) {
			yield* Console.log(`Adding project: ${projectName}`)
			yield* Console.log(`  Path: ${absolutePath}`)
			yield* Console.log(`  Beads: ${beadsPath}`)
		}

		// Create ProjectService layer and add project
		const fullLayer = Layer.provide(ProjectService.Default, BunContext.layer)

		yield* Effect.gen(function* () {
			const projectService = yield* ProjectService
			yield* projectService.addProject({
				name: projectName,
				path: absolutePath,
				beadsPath,
			})
		}).pipe(Effect.provide(fullLayer))

		yield* Console.log(`Project '${projectName}' added successfully.`)
	})

/**
 * List all registered projects
 */
const projectListHandler = (args: { readonly verbose: boolean }) =>
	Effect.gen(function* () {
		const fullLayer = Layer.provide(ProjectService.Default, BunContext.layer)

		const result = yield* Effect.gen(function* () {
			const projectService = yield* ProjectService
			const projects = yield* projectService.getProjects()
			const currentProject = yield* SubscriptionRef.get(projectService.currentProject)

			return { projects, currentProject }
		}).pipe(Effect.provide(fullLayer))

		if (result.projects.length === 0) {
			yield* Console.log("No projects registered.")
			yield* Console.log("Use 'az project add <path>' to register a project.")
			return
		}

		yield* Console.log("Registered projects:")
		yield* Console.log("")

		for (const project of result.projects) {
			const isCurrent = result.currentProject?.name === project.name
			const marker = isCurrent ? "* " : "  "
			yield* Console.log(`${marker}${project.name}`)
			yield* Console.log(`    Path: ${project.path}`)
			if (project.beadsPath && args.verbose) {
				yield* Console.log(`    Beads: ${project.beadsPath}`)
			}
			if (isCurrent) {
				yield* Console.log(`    (current)`)
			}
			yield* Console.log("")
		}

		if (!result.currentProject) {
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

		const fullLayer = Layer.provide(ProjectService.Default, BunContext.layer)

		yield* Effect.gen(function* () {
			const projectService = yield* ProjectService
			yield* projectService.removeProject(args.name)
		}).pipe(Effect.provide(fullLayer))

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

		const fullLayer = Layer.provide(ProjectService.Default, BunContext.layer)

		yield* Effect.gen(function* () {
			const projectService = yield* ProjectService
			yield* projectService.switchProject(args.name)
			yield* projectService.setDefaultProject(args.name)
		}).pipe(Effect.provide(fullLayer))

		yield* Console.log(`Switched to project '${args.name}' and set as default.`)
	})

// ============================================================================
// Command Definitions
// ============================================================================

/**
 * Issue ID argument for commands that operate on a specific issue
 */
const issueIdArg = Args.text({ name: "issue-id" }).pipe(
	Args.withDescription("Beads issue ID (e.g., az-2qy)"),
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
).pipe(Command.withDescription("Start a new Claude Code session for a beads issue"))

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
 * az sync - Sync beads database
 */
const syncCommand = Command.make(
	"sync",
	{
		all: Options.boolean("all").pipe(
			Options.withDescription("Sync all worktrees (not just current)"),
		),
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	syncHandler,
).pipe(Command.withDescription("Sync beads database in worktrees"))

/**
 * Event argument for notify command
 */
const eventArg = Args.text({ name: "event" }).pipe(
	Args.withDescription("Hook event type: idle_prompt, stop, session_end"),
)

/**
 * Bead ID argument for notify command
 */
const beadIdArg = Args.text({ name: "bead-id" }).pipe(
	Args.withDescription("Bead ID for the session (e.g., az-123)"),
)

/**
 * az notify <event> <bead-id> - Handle Claude Code hook notifications
 *
 * Called by Claude Code hooks to notify Azedarach of session state changes.
 * Writes a notification file that HookReceiver watches.
 */
const notifyCommand = Command.make(
	"notify",
	{
		event: eventArg,
		beadId: beadIdArg,
		verbose: verboseOption,
	},
	notifyHandler,
).pipe(Command.withDescription("Handle Claude Code hook notifications (internal use)"))

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
		startCommand,
		attachCommand,
		pauseCommand,
		statusCommand,
		syncCommand,
		notifyCommand,
		projectCommand,
	]),
)

// ============================================================================
// CLI Runner
// ============================================================================

/**
 * CLI runner function
 *
 * Takes raw process.argv (including binary and script path) and returns
 * an Effect that executes the appropriate command.
 */
const cliRunner = Command.run(cli, {
	name: "Azedarach",
	version: "0.1.0",
})

/**
 * Full CLI Effect - ready to be executed with process.argv
 *
 * Usage in entry point:
 * ```ts
 * Effect.suspend(() => cli(process.argv)).pipe(BunRuntime.runMain)
 * ```
 */
export { cli }

/**
 * Run the CLI with the provided arguments (full process.argv)
 */
export const run = (argv: ReadonlyArray<string>) =>
	cliRunner(argv).pipe(Effect.provide(BunContext.layer))
