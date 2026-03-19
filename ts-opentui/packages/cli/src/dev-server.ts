/**
 * CLI commands for dev server management
 *
 * These handlers delegate to daemon RPC only. CLI does not keep a local
 * dev-server runtime fallback anymore.
 */

import { GlobalDaemonBootstrap } from "@azedarach/daemon-control"
import type {
	DaemonDevServerListRequest,
	DaemonDevServerListResult,
	DaemonDevServerMutationResult,
	DaemonDevServerStartRequest,
	DaemonDevServerState,
	DaemonDevServerStatusRequest,
	DaemonDevServerStatusResult,
	DaemonDevServerStopRequest,
	DaemonRpcClientApi,
} from "@azedarach/shared/rpc"
import { Args, Command, Options } from "@effect/cli"
import { Console, Data, DateTime, Duration, Effect, Option } from "effect"
import { resolveCliIssueId } from "./issueIdResolver.js"

const CLI_DEFAULT_SERVER_NAME = "default"

interface CliDevServerState {
	readonly name: string
	readonly status: DaemonDevServerState["status"]
	readonly port: number | undefined
	readonly windowName: string | undefined
	readonly tmuxSession: string | undefined
	readonly worktreePath: string | undefined
	readonly startedAt: Date | undefined
	readonly error: string | undefined
}

class DevServerCommandRpcUnavailableError extends Data.TaggedError(
	"DevServerCommandRpcUnavailableError",
)<{
	readonly message: string
}> {}

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
	Args.withDescription("Issue ID (e.g., a, ab, 12, AZE-123, or shorthand suffix 123)"),
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

const jsonOption = Options.boolean("json").pipe(
	Options.withAlias("j"),
	Options.withDescription("Output in JSON format"),
)

// ============================================================================
// Helper to get project path
// ============================================================================

const getProjectPath = (projectDir: Option.Option<string>) =>
	Effect.sync(() => (Option.isSome(projectDir) ? projectDir.value : process.cwd()))

const parseStartedAt = (startedAt: string | null): Date | undefined => {
	if (startedAt === null) return undefined
	const parsed = new Date(startedAt)
	return Number.isNaN(parsed.getTime()) ? undefined : parsed
}

const toCliState = (server: {
	readonly serverName: string
	readonly status: DaemonDevServerState["status"]
	readonly port: number | null
	readonly windowName: string | null
	readonly tmuxSession: string | null
	readonly worktreePath: string | null
	readonly startedAt: string | null
	readonly error: string | null
}): CliDevServerState => ({
	name: server.serverName,
	status: server.status,
	port: server.port ?? undefined,
	windowName: server.windowName ?? undefined,
	tmuxSession: server.tmuxSession ?? undefined,
	worktreePath: server.worktreePath ?? undefined,
	startedAt: parseStartedAt(server.startedAt),
	error: server.error ?? undefined,
})

const getDaemonClient = () =>
	Effect.gen(function* () {
		const daemonBootstrap = yield* GlobalDaemonBootstrap
		const bootstrap = yield* daemonBootstrap.bootstrapDaemonRpcClient({
			autoStart: false,
			verifyReachable: true,
		})
		return bootstrap.client
	}).pipe(
		Effect.mapError(
			() =>
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC unavailable; start the daemon before using az dev",
				}),
		),
	)

const devServerStatus = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerStatusRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerStatusResult, DevServerCommandRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerStatus
		if (method === undefined) {
			return yield* Effect.fail(
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server status unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server status unavailable",
				}),
		),
	)

const devServerList = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerListRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerListResult, DevServerCommandRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerList
		if (method === undefined) {
			return yield* Effect.fail(
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server list unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server list unavailable",
				}),
		),
	)

const devServerStart = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerStartRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerMutationResult, DevServerCommandRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerStart
		if (method === undefined) {
			return yield* Effect.fail(
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server start unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server start unavailable",
				}),
		),
	)

const devServerStop = (
	daemonClient: DaemonRpcClientApi,
	request: Omit<DaemonDevServerStopRequest, "rpcProtocolVersion">,
): Effect.Effect<DaemonDevServerMutationResult, DevServerCommandRpcUnavailableError> =>
	Effect.gen(function* () {
		const method = daemonClient.devServerStop
		if (method === undefined) {
			return yield* Effect.fail(
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server stop unavailable",
				}),
			)
		}
		return yield* method(request)
	}).pipe(
		Effect.mapError(
			() =>
				new DevServerCommandRpcUnavailableError({
					message: "daemon RPC dev-server stop unavailable",
				}),
		),
	)

// ============================================================================
// Command Handlers - daemon RPC only
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
		const issueId = yield* resolveCliIssueId(args.issueId, projectPath)
		const daemonClient = yield* getDaemonClient()
		const currentStatus = yield* devServerStatus(daemonClient, {
			issueId,
			serverName,
			projectPath,
		})
		const currentState = toCliState(currentStatus.server)

		if (currentState.status === "running") {
			if (args.json) {
				yield* Console.log(
					JSON.stringify({
						resultStatus: "already_running",
						serverName,
						serverStatus: "running",
						port: currentState.port,
					}),
				)
			} else {
				yield* Console.log(`Dev server '${serverName}' is already running for ${issueId}`)
				if (currentState.port) {
					yield* Console.log(`  Port: ${currentState.port}`)
				}
			}
			return
		}

		const started = yield* devServerStart(daemonClient, {
			issueId,
			serverName,
			projectPath,
		})
		const state = toCliState(started.server)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({
					resultStatus: "started",
					issueId,
					serverName,
					serverStatus: state.status,
					port: state.port,
					window: state.windowName,
				}),
			)
		} else {
			yield* Console.log(`Started dev server '${serverName}' for ${issueId}`)
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
		const projectPath = yield* getProjectPath(Option.none())
		const issueId = yield* resolveCliIssueId(args.issueId, projectPath)
		const daemonClient = yield* getDaemonClient()
		const currentStatus = yield* devServerStatus(daemonClient, {
			issueId,
			serverName,
			projectPath,
		})
		const currentState = toCliState(currentStatus.server)

		if (currentState.status !== "running" && currentState.status !== "starting") {
			if (args.json) {
				yield* Console.log(
					JSON.stringify({ resultStatus: "not_running", message: "Dev server is not running" }),
				)
			} else {
				yield* Console.log(`Dev server '${serverName}' is not running for ${issueId}`)
			}
			return
		}

		yield* devServerStop(daemonClient, {
			issueId,
			serverName,
			projectPath,
		})

		if (args.json) {
			yield* Console.log(JSON.stringify({ resultStatus: "stopped", issueId, serverName }))
		} else {
			yield* Console.log(`Stopped dev server '${serverName}' for ${issueId}`)
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
		const issueId = yield* resolveCliIssueId(args.issueId, projectPath)
		const daemonClient = yield* getDaemonClient()

		if (!args.json) {
			yield* Console.log(`Restarting dev server '${serverName}' for ${issueId}...`)
		}

		yield* devServerStop(daemonClient, {
			issueId,
			serverName,
			projectPath,
		})
		yield* Effect.sleep("500 millis")
		const started = yield* devServerStart(daemonClient, {
			issueId,
			serverName,
			projectPath,
		})
		const state = toCliState(started.server)

		if (args.json) {
			yield* Console.log(
				JSON.stringify({
					resultStatus: "restarted",
					issueId,
					serverName,
					serverStatus: state.status,
					port: state.port,
				}),
			)
		} else {
			yield* Console.log(`Restarted dev server '${serverName}' for ${issueId}`)
			if (state.port) yield* Console.log(`  Port: ${state.port}`)
		}
	})

const devStatusHandler = (args: {
	readonly issueId: string
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const projectPath = yield* getProjectPath(Option.none())
		const issueId = yield* resolveCliIssueId(args.issueId, projectPath)
		const daemonClient = yield* getDaemonClient()
		const daemonServers = yield* devServerList(daemonClient, {
			issueId,
			projectPath,
		})
		const serverList = daemonServers.servers.map(toCliState)

		if (serverList.length === 0) {
			if (args.json) {
				yield* Console.log(JSON.stringify({ issueId, servers: [] }))
			} else {
				yield* Console.log(`No dev servers configured for ${issueId}`)
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
			yield* Console.log(JSON.stringify({ issueId, servers: serversJson }, null, 2))
			return
		}

		yield* Console.log(`Dev servers for ${issueId}:`)
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
		const projectPath = yield* getProjectPath(Option.none())
		const daemonClient = yield* getDaemonClient()
		const daemonServers = yield* devServerList(daemonClient, { projectPath })
		const runningServers = daemonServers.servers
			.map((server) => ({
				issueId: server.issueId,
				server: toCliState(server),
			}))
			.filter(({ server }) => server.status === "running")

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
