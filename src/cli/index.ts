/**
 * CLI Definition for Azedarach
 *
 * Uses @effect/cli for type-safe command parsing and validation.
 * Provides commands for managing Claude Code sessions via TUI and direct control.
 */

import { Args, Command, Options } from "@effect/cli"
import { Effect, Console, Option, Layer } from "effect"
import { BunContext, BunRuntime } from "@effect/platform-bun"
import * as FileSystem from "@effect/platform/FileSystem"
import * as Path from "@effect/platform/Path"
import { BeadsClientLiveWithPlatform } from "../core/BeadsClient.js"
import { AppConfigLiveWithPlatform } from "../config/index.js"
import { SessionManagerLive, SessionManager } from "../core/SessionManager.js"

// ============================================================================
// Shared Options
// ============================================================================

/**
 * Verbose logging flag
 */
const verboseOption = Options.boolean("verbose").pipe(
  Options.withAlias("v"),
  Options.withDescription("Enable verbose logging")
)

/**
 * Config file path option
 */
const configOption = Options.file("config").pipe(
  Options.withAlias("c"),
  Options.optional,
  Options.withDescription("Path to config file (default: .azedarach.json)")
)

/**
 * Project directory argument
 */
const projectDirArg = Args.directory().pipe(
  Args.optional,
  Args.withDescription("Project directory (default: current directory)")
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
          `No .beads directory found in ${projectDir}. Run 'bd init' to initialize beads tracking.`
        )
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

    // Create the combined layer with config
    const appConfigLayer = AppConfigLiveWithPlatform(cwd, configPath)
    const fullLayer = Layer.provideMerge(SessionManagerLive, appConfigLayer)

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
    yield* Console.log(
      `[Stub] Run: tmux attach-session -t az-${args.issueId}`
    )
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

// ============================================================================
// Command Definitions
// ============================================================================

/**
 * Issue ID argument for commands that operate on a specific issue
 */
const issueIdArg = Args.text({ name: "issue-id" }).pipe(
  Args.withDescription("Beads issue ID (e.g., az-2qy)")
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
  startHandler
).pipe(
  Command.withDescription(
    "Start a new Claude Code session for a beads issue"
  )
)

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
  attachHandler
).pipe(
  Command.withDescription("Attach to an existing Claude Code session")
)

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
  pauseHandler
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
  statusHandler
).pipe(
  Command.withDescription("Show status of all Claude Code sessions")
)

/**
 * az sync - Sync beads database
 */
const syncCommand = Command.make(
  "sync",
  {
    all: Options.boolean("all").pipe(
      Options.withDescription("Sync all worktrees (not just current)")
    ),
    projectDir: projectDirArg,
    verbose: verboseOption,
  },
  syncHandler
).pipe(Command.withDescription("Sync beads database in worktrees"))

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
  defaultHandler
).pipe(
  Command.withDescription(
    "Azedarach - TUI Kanban board for orchestrating parallel Claude Code sessions"
  )
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
  ])
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
  cliRunner(argv).pipe(
    Effect.provide(BeadsClientLiveWithPlatform),
    Effect.provide(BunContext.layer)
  )
