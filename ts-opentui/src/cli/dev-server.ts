/**
 * CLI commands for dev server management
 *
 * These handlers delegate to DevServerService - they don't contain business logic,
 * just CLI argument parsing and output formatting.
 */
import { Args, Command, Options } from "@effect/cli"
import { Console, DateTime, Duration, Effect, HashMap, Option, SubscriptionRef } from "effect"
import { DevServerService, type DevServerState } from "../services/DevServerService.js"
import { ProjectService } from "../services/ProjectService.js"

const CLI_DEFAULT_SERVER_NAME = "default"

/**
 * Format uptime from a start DateTime to now
 */
const formatUptime = (startedAt: Date | undefined): Effect.Effect<string, never, never> =>
	Effect.gen(function* () {
		if (!startedAt) return "-"
		const now = yield* DateTime.now
		const start = DateTime.unsafeMake(startedAt)
		const durationMs = DateTime.distance(start, now)
		const seconds = Math.floor(Duration.toMillis(durationMs) / 1000)
		const minutes = Math.floor(seconds / 60)
		const hours = Math.floor(minutes / 60)

		if (hours > 0) return `${hours}h ${minutes % 60}m`
		if (minutes > 0) return `${minutes}m ${seconds % 60}s`
		return `${seconds}s`
	})

// ============================================================================
// CLI Arguments and Options
// ============================================================================

const issueIdArg = Args.text({ name: "issue-id" }).pipe(
	Args.withDescription("Issue ID (e.g., az-2qy)"),
)

const projectDirArg = Args.directory().pipe(
	Args.optional,
	Args.withDescription("Project directory (default: current directory)"),
)

const verboseOption = Options.boolean("verbose").pipe(
	Options.withAlias("v"),
	Options.withDescription("Enable verbose logging"),
)

const serverOption = Options.text("server").pipe(
	Options.withAlias("s"),
	Options.optional,
	Options.withDescription("Server name (default: 'default')"),
)

const jsonOption = Options.boolean("json").pipe(Options.withDescription("Output in JSON format"))

// ============================================================================
// Helper to get project path
// ============================================================================

const getProjectPath = (projectDir: Option.Option<string>) =>
	Effect.gen(function* () {
		if (Option.isSome(projectDir)) return projectDir.value
		const projectService = yield* ProjectService
		const currentPath = yield* projectService.getCurrentPath()
		return currentPath ?? process.cwd()
	})

// ============================================================================
// Command Handlers - delegate to DevServerService
// ============================================================================

const devStartHandler = (args: {
	readonly issueId: string
	readonly server: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const serverName = Option.getOrElse(args.server, () => CLI_DEFAULT_SERVER_NAME)
		const projectPath = yield* getProjectPath(args.projectDir)

		const devServerService = yield* DevServerService

		// Check current status first
		const currentStatus = yield* devServerService.getStatus(args.issueId, serverName)

		if (currentStatus.status === "running") {
			if (args.json) {
				yield* Console.log(
					JSON.stringify({
						resultStatus: "already_running",
						serverName,
						serverStatus: "running",
						port: currentStatus.port,
					}),
				)
			} else {
				yield* Console.log(`Dev server '${serverName}' is already running for ${args.issueId}`)
				if (currentStatus.port) {
					yield* Console.log(`  Port: ${currentStatus.port}`)
				}
			}
			return
		}

		// Start the server via service
		const state = yield* devServerService.start(args.issueId, projectPath, serverName)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({
					resultStatus: "started",
					issueId: args.issueId,
					serverName,
					serverStatus: state.status,
					port: state.port,
					window: state.windowName,
				}),
			)
		} else {
			yield* Console.log(`Started dev server '${serverName}' for ${args.issueId}`)
			if (state.port) yield* Console.log(`  Port: ${state.port}`)
			if (state.windowName) yield* Console.log(`  Window: ${state.windowName}`)
		}
	})

const devStopHandler = (args: {
	readonly issueId: string
	readonly server: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const serverName = Option.getOrElse(args.server, () => CLI_DEFAULT_SERVER_NAME)
		const devServerService = yield* DevServerService

		// Check current status
		const currentStatus = yield* devServerService.getStatus(args.issueId, serverName)

		if (currentStatus.status !== "running" && currentStatus.status !== "starting") {
			if (args.json) {
				yield* Console.log(
					JSON.stringify({ resultStatus: "not_running", message: "Dev server is not running" }),
				)
			} else {
				yield* Console.log(`Dev server '${serverName}' is not running for ${args.issueId}`)
			}
			return
		}

		// Stop the server via service
		yield* devServerService.stop(args.issueId, serverName)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({ resultStatus: "stopped", issueId: args.issueId, serverName }),
			)
		} else {
			yield* Console.log(`Stopped dev server '${serverName}' for ${args.issueId}`)
		}
	})

const devRestartHandler = (args: {
	readonly issueId: string
	readonly server: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const serverName = Option.getOrElse(args.server, () => CLI_DEFAULT_SERVER_NAME)
		const projectPath = yield* getProjectPath(args.projectDir)
		const devServerService = yield* DevServerService

		if (!args.json) {
			yield* Console.log(`Restarting dev server '${serverName}' for ${args.issueId}...`)
		}

		// Stop then start via service
		yield* devServerService.stop(args.issueId, serverName).pipe(Effect.ignore)
		yield* Effect.sleep("500 millis")
		const state = yield* devServerService.start(args.issueId, projectPath, serverName)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({
					resultStatus: "restarted",
					issueId: args.issueId,
					serverName,
					serverStatus: state.status,
					port: state.port,
				}),
			)
		} else {
			yield* Console.log(`Restarted dev server '${serverName}' for ${args.issueId}`)
			if (state.port) yield* Console.log(`  Port: ${state.port}`)
		}
	})

const devStatusHandler = (args: {
	readonly issueId: string
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const devServerService = yield* DevServerService

		// Get all servers for this issue
		const issueServers = yield* devServerService.getIssueServers(args.issueId)
		const serverList = Array.from(HashMap.values(issueServers))

		if (serverList.length === 0) {
			if (args.json) {
				yield* Console.log(JSON.stringify({ issueId: args.issueId, servers: [] }))
			} else {
				yield* Console.log(`No dev servers configured for ${args.issueId}`)
			}
			return
		}

		if (args.json) {
			const serversJson = yield* Effect.all(
				serverList.map((s) =>
					Effect.gen(function* () {
						const uptime = yield* formatUptime(s.startedAt)
						return {
							name: s.name,
							status: s.status,
							port: s.port,
							uptime: s.startedAt ? uptime : null,
							startedAt: s.startedAt?.toISOString(),
						}
					}),
				),
			)
			yield* Console.log(JSON.stringify({ issueId: args.issueId, servers: serversJson }, null, 2))
			return
		}

		yield* Console.log(`Dev servers for ${args.issueId}:`)
		yield* Console.log("")

		for (const server of serverList) {
			const statusIcon =
				server.status === "running"
					? "🟢"
					: server.status === "starting"
						? "🟡"
						: server.status === "error"
							? "🔴"
							: "⚪"

			yield* Console.log(`  ${statusIcon} ${server.name}`)
			yield* Console.log(`      Status: ${server.status}`)
			if (server.port) yield* Console.log(`      Port:   ${server.port}`)
			if (server.startedAt && server.status === "running") {
				const uptime = yield* formatUptime(server.startedAt)
				yield* Console.log(`      Uptime: ${uptime}`)
			}
			if (server.error) yield* Console.log(`      Error:  ${server.error}`)
		}
	})

const devListHandler = (args: { readonly verbose: boolean; readonly json: boolean }) =>
	Effect.gen(function* () {
		const devServerService = yield* DevServerService

		// Get all servers from the service's state
		const allServers = yield* SubscriptionRef.get(devServerService.servers)

		// Collect running servers across all issues
		const runningServers: Array<{ issueId: string; server: DevServerState }> = []
		for (const [issueId, issueServers] of HashMap.entries(allServers)) {
			for (const server of HashMap.values(issueServers)) {
				if (server.status === "running") {
					runningServers.push({ issueId, server })
				}
			}
		}

		if (args.json) {
			const serversJson = yield* Effect.all(
				runningServers.map(({ issueId, server }) =>
					Effect.gen(function* () {
						const uptime = yield* formatUptime(server.startedAt)
						return {
							issueId,
							name: server.name,
							status: server.status,
							port: server.port,
							uptime,
							startedAt: server.startedAt?.toISOString(),
						}
					}),
				),
			)
			yield* Console.log(JSON.stringify({ servers: serversJson }, null, 2))
			return
		}

		if (runningServers.length === 0) {
			yield* Console.log("No dev servers running.")
			return
		}

		yield* Console.log("Running dev servers:")
		yield* Console.log("")
		yield* Console.log("  ISSUE         SERVER    PORT    UPTIME")
		yield* Console.log("  ─────────────────────────────────────────")

		for (const { issueId, server } of runningServers) {
			const port = server.port?.toString() ?? "-"
			const uptime = yield* formatUptime(server.startedAt)
			yield* Console.log(
				`  ${issueId.padEnd(12)} ${server.name.padEnd(9)} ${port.padEnd(7)} ${uptime}`,
			)
		}

		yield* Console.log("")
		yield* Console.log(`${runningServers.length} server(s) running`)
	})

const devStartCommand = Command.make(
	"start",
	{
		issueId: issueIdArg,
		server: serverOption,
		projectDir: projectDirArg,
		verbose: verboseOption,
		json: jsonOption,
	},
	devStartHandler,
).pipe(Command.withDescription("Start a dev server for an issue"))

const devStopCommand = Command.make(
	"stop",
	{
		issueId: issueIdArg,
		server: serverOption,
		verbose: verboseOption,
		json: jsonOption,
	},
	devStopHandler,
).pipe(Command.withDescription("Stop a dev server for an issue"))

const devRestartCommand = Command.make(
	"restart",
	{
		issueId: issueIdArg,
		server: serverOption,
		projectDir: projectDirArg,
		verbose: verboseOption,
		json: jsonOption,
	},
	devRestartHandler,
).pipe(Command.withDescription("Restart a dev server for an issue"))

const devStatusCommand = Command.make(
	"status",
	{
		issueId: issueIdArg,
		verbose: verboseOption,
		json: jsonOption,
	},
	devStatusHandler,
).pipe(Command.withDescription("Show dev server status for an issue"))

const devListCommand = Command.make(
	"list",
	{
		verbose: verboseOption,
		json: jsonOption,
	},
	devListHandler,
).pipe(Command.withDescription("Show all running dev servers across all issues"))

export const devCommand = Command.make("dev", {}, () =>
	Console.log("Use 'az dev --help' to see available subcommands"),
).pipe(
	Command.withSubcommands([
		devStartCommand,
		devStopCommand,
		devRestartCommand,
		devStatusCommand,
		devListCommand,
	]),
	Command.withDescription("Manage dev servers for issues"),
)
