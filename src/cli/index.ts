/**
 * CLI Definition for Azedarach
 *
 * Uses @effect/cli for type-safe command parsing and validation.
 * Provides commands for managing Claude Code sessions via TUI and direct control.
 */

import { Args, Command, Options } from "@effect/cli"
import { Command as PlatformCommand, FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, SubscriptionRef } from "effect"
import { AppConfigConfig } from "../config/AppConfig.js"
import { ClaudeSessionManager } from "../core/ClaudeSessionManager.js"
import { deepMerge, generateHookConfig } from "../core/hooks.js"
import type { TmuxStatus } from "../core/TmuxSessionMonitor.js"
import { ProjectService } from "../services/ProjectService.js"
import { launchTUI } from "../ui/launch.js"

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
			ClaudeSessionManager.Default,
			Layer.merge(appConfigLayer, BunContext.layer),
		)

		// Start the session using ClaudeSessionManager
		const session = yield* Effect.gen(function* () {
			const sessionManager = yield* ClaudeSessionManager
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
const VALID_HOOK_EVENTS = [
	"user_prompt",
	"idle_prompt",
	"permission_request",
	"pretooluse",
	"stop",
	"session_end",
] as const
type HookEvent = (typeof VALID_HOOK_EVENTS)[number]

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
	readonly beadId: string
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		// Validate event type using type guard
		if (!isValidHookEvent(args.event)) {
			yield* Console.error(`Invalid event type: ${args.event}`)
			yield* Console.error(`Valid events: ${VALID_HOOK_EVENTS.join(", ")}`)
			return yield* Effect.fail(new Error(`Invalid event: ${args.event}`))
		}

		const status = mapEventToStatus(args.event)
		const sessionName = `claude-${args.beadId}`

		if (args.verbose) {
			yield* Console.log(`Hook: ${args.event} for ${args.beadId} → status: ${status}`)
		}

		// Update tmux session option for the Claude session
		// The TUI can poll this with: tmux show-option -t <session> -v @az_status
		const tmuxCommand = PlatformCommand.make(
			"tmux",
			"set-option",
			"-t",
			sessionName,
			"@az_status",
			status,
		)

		yield* PlatformCommand.exitCode(tmuxCommand).pipe(
			Effect.catchAll((error) => {
				// Session may not exist (e.g., during startup) - log but don't fail
				if (args.verbose) {
					yield* Console.log(`Could not set tmux status: ${error}`)
				}
				return Effect.succeed(1)
			}),
		)

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
	readonly beadId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
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
				.pipe(Effect.catchAll(() => Effect.succeed("{}")))
			existingSettings = yield* Effect.try({
				try: () => JSON.parse(content),
				catch: () => ({}),
			}).pipe(Effect.catchAll(() => Effect.succeed({})))

			if (args.verbose) {
				yield* Console.log(`Read existing settings from: ${settingsPath}`)
			}
		}

		// Generate and merge hook configuration
		const hookConfig = generateHookConfig(args.beadId)
		const mergedSettings = deepMerge(existingSettings, hookConfig)

		// Write merged settings
		yield* fs.writeFileString(settingsPath, JSON.stringify(mergedSettings, null, "\t"))

		yield* Console.log(`✓ Installed hooks for bead ${args.beadId}`)
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
		const projectName = Option.getOrElse(args.name, () => pathService.basename(absolutePath))

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
 * Sets tmux session option that TmuxSessionMonitor polls.
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
 * az hooks install <bead-id> - Install session state hooks
 *
 * Installs Azedarach hooks into .claude/settings.local.json for session state detection.
 * This is automatically done when creating worktrees, but can be run manually.
 */
const hooksInstallCommand = Command.make(
	"install",
	{
		beadId: beadIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
	},
	hooksInstallHandler,
).pipe(Command.withDescription("Install session state hooks into .claude/settings.local.json"))

/**
 * az hooks - Parent command for hook management
 */
const hooksCommand = Command.make("hooks", {}, () =>
	Console.log("Usage: az hooks install <bead-id>"),
).pipe(
	Command.withDescription("Manage Claude Code hooks for session state detection"),
	Command.withSubcommands([hooksInstallCommand]),
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
		// Session management
		startCommand,
		attachCommand,
		pauseCommand,
		statusCommand,
		syncCommand,
		// Internal/advanced commands
		notifyCommand,
		hooksCommand,
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
