/**
 * CLI Definition for Azedarach
 *
 * Uses @effect/cli for type-safe command parsing and validation.
 * Provides commands for managing Claude Code sessions via TUI and direct control.
 */

import { Args, Command, Options } from "@effect/cli"
import { FileSystem, Path, Command as PlatformCommand } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, SubscriptionRef } from "effect"
import { AppConfigConfig } from "../config/AppConfig.js"
import { ClaudeSessionManager } from "../core/ClaudeSessionManager.js"
import { deepMerge, generateHookConfig } from "../core/hooks.js"
import { getBeadSessionName } from "../core/paths.js"
import type { TmuxStatus } from "../core/TmuxSessionMonitor.js"
import { ProjectService } from "../services/ProjectService.js"
import { launchTUI } from "../ui/launch.js"
import { devCommand } from "./dev-server.js"

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

		// Claim the bead with session assignee
		const claimCommand = PlatformCommand.make(
			"bd",
			"update",
			args.issueId,
			"--status=in_progress",
			`--assignee=${session.tmuxSessionName}`,
		)
		yield* PlatformCommand.exitCode(claimCommand).pipe(
			Effect.tap(() => {
				if (args.verbose) {
					return Console.log(
						`Claimed bead ${args.issueId} with assignee ${session.tmuxSessionName}`,
					)
				}
				return Effect.void
			}),
			Effect.catchAll((e) => {
				// Non-fatal: log warning but continue
				return Console.log(`Warning: Could not claim bead: ${e}`)
			}),
		)

		yield* Console.log(`Session started successfully!`)
		yield* Console.log(`  Worktree: ${session.worktreePath}`)
		yield* Console.log(`  tmux session: ${session.tmuxSessionName}`)
		yield* Console.log(`  Bead claimed: ${args.issueId} (assignee: ${session.tmuxSessionName})`)
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

		if (args.verbose) {
			yield* Console.log(`Attaching to session for issue: ${args.issueId}`)
			yield* Console.log(`Project: ${cwd}`)
		}

		// Check if session exists
		const sessionName = getBeadSessionName(args.issueId)
		const command = PlatformCommand.make("tmux", "has-session", "-t", sessionName)
		const exitCode = yield* PlatformCommand.exitCode(command).pipe(
			Effect.catchAll(() => Effect.succeed(1)),
		)

		if (exitCode !== 0) {
			yield* Console.error(`No session found for ${args.issueId}`)
			yield* Console.log(`Start a new session with: az start ${args.issueId}`)
			return yield* Effect.fail(new Error(`Session not found: ${args.issueId}`))
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
 * Kill a running Claude session
 */
const killHandler = (args: {
	readonly issueId: string
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())

		yield* Console.log(`Killing session for issue: ${args.issueId}`)

		// Check if session exists
		const sessionName = getBeadSessionName(args.issueId)
		const checkCommand = PlatformCommand.make("tmux", "has-session", "-t", sessionName)
		const exitCode = yield* PlatformCommand.exitCode(checkCommand).pipe(
			Effect.catchAll(() => Effect.succeed(1)),
		)

		if (exitCode !== 0) {
			yield* Console.log(`No session found for ${args.issueId}`)
			return
		}

		// Kill the tmux session
		const killCommand = PlatformCommand.make("tmux", "kill-session", "-t", sessionName)
		yield* PlatformCommand.exitCode(killCommand).pipe(
			Effect.catchAll((e) => {
				return Console.error(`Failed to kill session: ${e}`).pipe(Effect.as(1))
			}),
		)

		yield* Console.log(`Session ${args.issueId} killed.`)

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
			Effect.catchAll(() => Effect.succeed("")),
		)

		if (!output.trim()) {
			yield* Console.log("No active sessions.")
			return
		}

		const lines = output.trim().split("\n")
		let sessionCount = 0

		for (const line of lines) {
			const [name, _created, attached, status] = line.split("|")
			// Only show sessions that look like bead IDs (contain a dash, short format)
			if (name && name.includes("-") && name.length < 20) {
				sessionCount++
				const statusDisplay = status || "unknown"
				const attachedDisplay = attached === "attached" ? " (attached)" : ""
				yield* Console.log(`  ${name} - ${statusDisplay.toUpperCase()}${attachedDisplay}`)

				if (args.verbose) {
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
						Effect.catchAll(() => Effect.succeed("")),
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

		yield* Console.log(`Running quality gates for: ${args.issueId}`)

		// Find the worktree path for this task
		const sessionName = getBeadSessionName(args.issueId)
		const wtCommand = PlatformCommand.make(
			"tmux",
			"display-message",
			"-t",
			sessionName,
			"-p",
			"#{pane_current_path}",
		)

		let worktreePath = yield* PlatformCommand.string(wtCommand).pipe(
			Effect.map((s) => s.trim()),
			Effect.catchAll(() => Effect.succeed("")),
		)

		// If no active session, try to find worktree by convention
		if (!worktreePath) {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const parentDir = pathService.dirname(cwd)
			const projectName = pathService.basename(cwd)
			const expectedPath = pathService.join(parentDir, `${projectName}-${args.issueId}`)

			const exists = yield* fs.exists(expectedPath)
			if (exists) {
				worktreePath = expectedPath
			} else {
				yield* Console.error(`Could not find worktree for ${args.issueId}`)
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
			Effect.catchAll((e) => Effect.succeed({ passed: false, output: String(e) })),
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
			Effect.catchAll((e) => Effect.succeed({ passed: false, output: String(e) })),
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
					return Effect.succeed({ passed: true, output: "No test script" })
				}
				return Effect.succeed({ passed: false, output })
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
					return Effect.succeed({ passed: true, output: "No build script" })
				}
				return Effect.succeed({ passed: false, output })
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

const findAiSessionByBeadId = (beadId: string) =>
	Effect.gen(function* () {
		yield* Console.log(`[DEBUG] findAiSessionByBeadId: beadId=${beadId}`)

		const sessionName = getBeadSessionName(beadId)
		yield* Console.log(`[DEBUG] Checking for session: ${sessionName}`)
		const command = PlatformCommand.make("tmux", "has-session", "-t", sessionName)
		const exitCode = yield* PlatformCommand.exitCode(command).pipe(
			Effect.catchAll(() => Effect.succeed(1)),
		)

		if (exitCode === 0) {
			yield* Console.log(`[DEBUG] Found session: ${sessionName}`)
			return sessionName
		}

		yield* Console.log(`[DEBUG] No session found for beadId=${beadId}`)
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

		// Find the session by beadId (handles both new and legacy naming formats)
		const sessionName = yield* findAiSessionByBeadId(args.beadId)
		if (!sessionName) {
			if (args.verbose) {
				yield* Console.log(`No session found for ${args.beadId}`)
			}
			return
		}

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
			Effect.catchAll((error) =>
				// Session may not exist (e.g., during startup) - log but don't fail
				args.verbose
					? Console.log(`Could not set tmux status: ${error}`).pipe(Effect.as(1))
					: Effect.succeed(1),
			),
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
 * az gate <issue-id> - Run quality gates for a task
 */
const gateCommand = Command.make(
	"gate",
	{
		issueId: issueIdArg,
		projectDir: projectDirArg,
		verbose: verboseOption,
		fix: Options.boolean("fix").pipe(Options.withDescription("Auto-fix lint issues")),
	},
	gateHandler,
).pipe(Command.withDescription("Run quality gates (type-check, lint, test, build) for a task"))

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

// ============================================================================
// OpenCode Commands
// ============================================================================

/**
 * Default opencode.json configuration
 */
const DEFAULT_OPENCODE_CONFIG = {
	$schema: "https://opencode.ai/config.json",
	instructions: ["CLAUDE.md"],
	plugins: ["opencode-beads", "opencode-skills"],
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
			"bd *": "allow",
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
 * - Generates SKILL.md wrappers from .claude/skills if present
 * - Installs azedarach plugin globally if not present
 */
const opencodeInitHandler = (args: {
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly skipSkills: boolean
}) =>
	Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const opencodeJsonPath = pathService.join(cwd, "opencode.json")
		const claudeSkillsDir = pathService.join(cwd, ".claude", "skills")
		const globalPluginDir = pathService.join(
			process.env.HOME ?? "~",
			".config",
			"opencode",
			"plugin",
		)
		const globalPluginPath = pathService.join(globalPluginDir, "azedarach.js")

		yield* Console.log("🚀 Initializing OpenCode support...")
		yield* Console.log("")

		// Step 1: Create/update opencode.json
		let config = { ...DEFAULT_OPENCODE_CONFIG }
		const configExists = yield* fs.exists(opencodeJsonPath)
		if (configExists) {
			const existingContent = yield* fs.readFileString(opencodeJsonPath)
			const existingConfig = yield* Effect.try({
				try: () => JSON.parse(existingContent),
				catch: () => ({}),
			})

			// Merge plugins - existingConfig.plugins could be undefined or an array
			const existingPlugins = Array.isArray(existingConfig.plugins)
				? (existingConfig.plugins as string[])
				: []
			const newPlugins = [...new Set([...existingPlugins, "opencode-beads", "opencode-skills"])]
			config = { ...existingConfig, ...config, plugins: newPlugins }

			yield* Console.log("✓ Updated existing opencode.json")
		} else {
			yield* Console.log("✓ Created opencode.json")
		}

		yield* fs.writeFileString(opencodeJsonPath, JSON.stringify(config, null, 2))

		if (args.verbose) {
			yield* Console.log(`  Plugins: ${config.plugins.join(", ")}`)
		}

		// Step 2: Check/install global azedarach plugin
		const globalPluginExists = yield* fs.exists(globalPluginPath)
		if (!globalPluginExists) {
			yield* Console.log("")
			yield* Console.log("⚠ Global azedarach plugin not found")
			yield* Console.log(`  Install with: mkdir -p ${globalPluginDir}`)
			yield* Console.log(`  Then copy azedarach.js from an existing project's .opencode/plugin/`)
		} else {
			yield* Console.log("✓ Global azedarach plugin found")
		}

		// Step 3: Generate skill wrappers if .claude/skills exists
		if (!args.skipSkills) {
			const claudeSkillsExist = yield* fs.exists(claudeSkillsDir)
			if (claudeSkillsExist) {
				yield* Console.log("")
				yield* Console.log("📚 Generating skill wrappers...")

				// Find the generator script
				const scriptPath = pathService.join(
					pathService.dirname(pathService.dirname(import.meta.dirname ?? "")),
					"scripts",
					"generate-opencode-skills.sh",
				)

				const scriptExists = yield* fs.exists(scriptPath)
				if (scriptExists) {
					// Run the generator script
					const command = PlatformCommand.make("bash", scriptPath, cwd)
					const output = yield* PlatformCommand.string(command).pipe(
						Effect.catchAll((e) => Effect.succeed(`Error: ${e}`)),
					)

					// Count generated skills
					const generatedCount = (output.match(/Generated:/g) ?? []).length
					yield* Console.log(`✓ Generated ${generatedCount} skill wrappers`)

					if (args.verbose) {
						yield* Console.log(output)
					}
				} else {
					yield* Console.log("⚠ Skill generator script not found")
					yield* Console.log(`  Expected at: ${scriptPath}`)
					yield* Console.log("  Run manually: generate-opencode-skills.sh <project-dir>")
				}
			} else if (args.verbose) {
				yield* Console.log("")
				yield* Console.log("ℹ No .claude/skills directory found, skipping skill generation")
			}
		}

		// Summary
		yield* Console.log("")
		yield* Console.log("✅ OpenCode setup complete!")
		yield* Console.log("")
		yield* Console.log("Next steps:")
		yield* Console.log("  1. Install opencode-beads: npm install -g opencode-beads")
		yield* Console.log("  2. Install opencode-skills: npm install -g opencode-skills")
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
		skipSkills: Options.boolean("skip-skills").pipe(
			Options.withDescription("Skip generating skill wrappers"),
		),
	},
	opencodeInitHandler,
).pipe(Command.withDescription("Initialize OpenCode support in a project"))

/**
 * az opencode - Parent command for OpenCode integration
 */
const opencodeCommand = Command.make("opencode", {}, () =>
	Console.log("Usage: az opencode init [project-dir]"),
).pipe(
	Command.withDescription("OpenCode integration commands"),
	Command.withSubcommands([opencodeInitCommand]),
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
		killCommand,
		statusCommand,
		syncCommand,
		gateCommand,
		devCommand,
		// Internal/advanced commands
		notifyCommand,
		hooksCommand,
		projectCommand,
		opencodeCommand,
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
