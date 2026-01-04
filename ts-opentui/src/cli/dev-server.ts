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

const beadIdArg = Args.text({ name: "bead-id" }).pipe(
	Args.withDescription("Beads issue ID (e.g., az-2qy)"),
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
	readonly beadId: string
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
		const currentStatus = yield* devServerService.getStatus(args.beadId, serverName)

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
				yield* Console.log(`Dev server '${serverName}' is already running for ${args.beadId}`)
				if (currentStatus.port) {
					yield* Console.log(`  Port: ${currentStatus.port}`)
				}
			}
			return
		}

		// Start the server via service
		const state = yield* devServerService.start(args.beadId, projectPath, serverName)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({
					resultStatus: "started",
					beadId: args.beadId,
					serverName,
					serverStatus: state.status,
					port: state.port,
					window: state.windowName,
				}),
			)
		} else {
			yield* Console.log(`Started dev server '${serverName}' for ${args.beadId}`)
			if (state.port) yield* Console.log(`  Port: ${state.port}`)
			if (state.windowName) yield* Console.log(`  Window: ${state.windowName}`)
		}
	})

const devStopHandler = (args: {
	readonly beadId: string
	readonly server: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const serverName = Option.getOrElse(args.server, () => CLI_DEFAULT_SERVER_NAME)
		const devServerService = yield* DevServerService

		// Check current status
		const currentStatus = yield* devServerService.getStatus(args.beadId, serverName)

		if (currentStatus.status !== "running" && currentStatus.status !== "starting") {
			if (args.json) {
				yield* Console.log(
					JSON.stringify({ resultStatus: "not_running", message: "Dev server is not running" }),
				)
			} else {
				yield* Console.log(`Dev server '${serverName}' is not running for ${args.beadId}`)
			}
			return
		}

		// Stop the server via service
		yield* devServerService.stop(args.beadId, serverName)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({ resultStatus: "stopped", beadId: args.beadId, serverName }),
			)
		} else {
			yield* Console.log(`Stopped dev server '${serverName}' for ${args.beadId}`)
		}
	})

const devRestartHandler = (args: {
	readonly beadId: string
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
			yield* Console.log(`Restarting dev server '${serverName}' for ${args.beadId}...`)
		}

		// Stop then start via service
		yield* devServerService.stop(args.beadId, serverName).pipe(Effect.ignore)
		yield* Effect.sleep("500 millis")
		const state = yield* devServerService.start(args.beadId, projectPath, serverName)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({
					resultStatus: "restarted",
					beadId: args.beadId,
					serverName,
					serverStatus: state.status,
					port: state.port,
				}),
			)
		} else {
			yield* Console.log(`Restarted dev server '${serverName}' for ${args.beadId}`)
			if (state.port) yield* Console.log(`  Port: ${state.port}`)
		}
	})

const devStatusHandler = (args: {
	readonly beadId: string
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const devServerService = yield* DevServerService

		// Get all servers for this bead
		const beadServers = yield* devServerService.getBeadServers(args.beadId)
		const serverList = Array.from(HashMap.values(beadServers))

		if (serverList.length === 0) {
			if (args.json) {
				yield* Console.log(JSON.stringify({ beadId: args.beadId, servers: [] }))
			} else {
				yield* Console.log(`No dev servers configured for ${args.beadId}`)
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
			yield* Console.log(JSON.stringify({ beadId: args.beadId, servers: serversJson }, null, 2))
			return
		}

		yield* Console.log(`Dev servers for ${args.beadId}:`)
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

		// Collect running servers across all beads
		const runningServers: Array<{ beadId: string; server: DevServerState }> = []
		for (const [beadId, beadServers] of HashMap.entries(allServers)) {
			for (const server of HashMap.values(beadServers)) {
				if (server.status === "running") {
					runningServers.push({ beadId, server })
				}
			}
		}

		if (args.json) {
			const serversJson = yield* Effect.all(
				runningServers.map(({ beadId, server }) =>
					Effect.gen(function* () {
						const uptime = yield* formatUptime(server.startedAt)
						return {
							beadId,
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
		yield* Console.log("  BEAD         SERVER    PORT    UPTIME")
		yield* Console.log("  ─────────────────────────────────────────")

		for (const { beadId, server } of runningServers) {
			const port = server.port?.toString() ?? "-"
			const uptime = yield* formatUptime(server.startedAt)
			yield* Console.log(
				`  ${beadId.padEnd(12)} ${server.name.padEnd(9)} ${port.padEnd(7)} ${uptime}`,
			)
		}

		yield* Console.log("")
		yield* Console.log(`${runningServers.length} server(s) running`)
	})

const devStartCommand = Command.make(
	"start",
	{
		beadId: beadIdArg,
		server: serverOption,
		projectDir: projectDirArg,
		verbose: verboseOption,
		json: jsonOption,
	},
	devStartHandler,
).pipe(Command.withDescription("Start a dev server for a bead"))

const devStopCommand = Command.make(
	"stop",
	{
		beadId: beadIdArg,
		server: serverOption,
		verbose: verboseOption,
		json: jsonOption,
	},
	devStopHandler,
).pipe(Command.withDescription("Stop a dev server for a bead"))

const devRestartCommand = Command.make(
	"restart",
	{
		beadId: beadIdArg,
		server: serverOption,
		projectDir: projectDirArg,
		verbose: verboseOption,
		json: jsonOption,
	},
	devRestartHandler,
).pipe(Command.withDescription("Restart a dev server for a bead"))

const devStatusCommand = Command.make(
	"status",
	{
		beadId: beadIdArg,
		verbose: verboseOption,
		json: jsonOption,
	},
	devStatusHandler,
).pipe(Command.withDescription("Show dev server status for a bead"))

const devListCommand = Command.make(
	"list",
	{
		verbose: verboseOption,
		json: jsonOption,
	},
	devListHandler,
).pipe(Command.withDescription("Show all running dev servers across all beads"))

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
	Command.withDescription("Manage dev servers for beads"),
)
