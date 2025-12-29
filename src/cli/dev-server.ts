import { Args, Command, Options } from "@effect/cli"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Console, Effect, Layer, Option, Schema } from "effect"
import { AppConfigConfig } from "../config/AppConfig.js"
import {
	getBeadSessionName,
	getWorktreePath,
	parseSessionName,
	WINDOW_NAMES,
} from "../core/paths.js"
import { TmuxService } from "../core/TmuxService.js"

const DevServerMetadataSchema = Schema.Struct({
	beadId: Schema.String,
	serverName: Schema.String,
	status: Schema.Literal("idle", "starting", "running", "error"),
	port: Schema.optional(Schema.Number),
	paneId: Schema.optional(Schema.String),
	worktreePath: Schema.optional(Schema.String),
	projectPath: Schema.optional(Schema.String),
	startedAt: Schema.optional(Schema.String),
	error: Schema.optional(Schema.String),
	beadPorts: Schema.optional(Schema.Record({ key: Schema.String, value: Schema.Number })),
})
type DevServerMetadata = Schema.Schema.Type<typeof DevServerMetadataSchema>

const TMUX_OPT_DEV_METADATA = "@az-devserver-meta"
const CLI_DEFAULT_SERVER_NAME = "default"

const formatUptime = (startedAt: string | undefined): string => {
	if (!startedAt) return "-"
	const start = new Date(startedAt).getTime()
	const now = Date.now()
	const diffMs = now - start
	const seconds = Math.floor(diffMs / 1000)
	const minutes = Math.floor(seconds / 60)
	const hours = Math.floor(minutes / 60)

	if (hours > 0) return `${hours}h ${minutes % 60}m`
	if (minutes > 0) return `${minutes}m ${seconds % 60}s`
	return `${seconds}s`
}

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

const devStartHandler = (args: {
	readonly beadId: string
	readonly server: Option.Option<string>
	readonly projectDir: Option.Option<string>
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const cwd = Option.getOrElse(args.projectDir, () => process.cwd())
		const serverName = Option.getOrElse(args.server, () => CLI_DEFAULT_SERVER_NAME)

		const appConfigLayer = AppConfigConfig.Default(cwd, undefined)
		const tmuxLayer = TmuxService.Default
		const fullLayer = Layer.merge(appConfigLayer, Layer.merge(tmuxLayer, BunContext.layer))

		const result = yield* Effect.gen(function* () {
			const tmux = yield* TmuxService
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const configModule = yield* Effect.promise(() => import("../config/index.js"))
			const appConfig = yield* configModule.AppConfig

			const worktreePath = getWorktreePath(cwd, args.beadId)

			const worktreeExists = yield* fs
				.exists(worktreePath)
				.pipe(Effect.catchAll(() => Effect.succeed(false)))
			if (!worktreeExists) {
				return yield* Effect.fail(
					new Error(
						`No worktree found for ${args.beadId}. Start a Claude session first with: az start ${args.beadId}`,
					),
				)
			}

			const devServerConfig = yield* appConfig.getDevServerConfig()
			const serverConfig = devServerConfig.servers?.[serverName]

			if (!serverConfig) {
				return yield* Effect.fail(
					new Error(
						`No server configuration found for '${serverName}'. Define it in .azedarach.json under devServer.servers.`,
					),
				)
			}

			const sessionName = getBeadSessionName(args.beadId)
			const targetWindow = `${sessionName}:${WINDOW_NAMES.DEV}`
			const serverCwd = serverConfig.cwd
				? pathService.join(worktreePath, serverConfig.cwd)
				: worktreePath

			const hasSession = yield* tmux.hasSession(sessionName)
			if (!hasSession) {
				return yield* Effect.fail(
					new Error(
						`No tmux session for ${args.beadId}. Start a Claude session first with: az start ${args.beadId}`,
					),
				)
			}

			const existingMetaJson = yield* tmux.getUserOption(sessionName, TMUX_OPT_DEV_METADATA)
			if (Option.isSome(existingMetaJson)) {
				const existingMeta = yield* Schema.decodeUnknown(Schema.parseJson(DevServerMetadataSchema))(
					existingMetaJson.value,
				).pipe(Effect.option)
				if (Option.isSome(existingMeta) && existingMeta.value.status === "running") {
					if (args.json) {
						return {
							resultStatus: "already_running" as const,
							serverName: existingMeta.value.serverName,
							serverStatus: existingMeta.value.status,
							port: existingMeta.value.port,
						}
					}
					yield* Console.log(`Dev server '${serverName}' is already running for ${args.beadId}`)
					if (existingMeta.value.port) {
						yield* Console.log(`  Port: ${existingMeta.value.port}`)
					}
					return { resultStatus: "already_running" as const }
				}
			}

			const ports = serverConfig.ports ?? { PORT: 3000 }
			const envStr = Object.entries(ports)
				.map(([k, v]) => `${k}=${v}`)
				.join(" ")

			const hasWindow = yield* tmux.hasWindow(sessionName, WINDOW_NAMES.DEV)
			let paneId: string | undefined

			if (hasWindow) {
				const panes = yield* tmux.listPanes(targetWindow)
				paneId = panes[0]?.id
				if (paneId) {
					yield* tmux.sendKeys(paneId, `${envStr} ${serverConfig.command}`)
				}
			} else {
				yield* tmux.newWindow(sessionName, WINDOW_NAMES.DEV, {
					cwd: serverCwd,
					command: `${envStr} ${serverConfig.command}`,
				})
				const panes = yield* tmux.listPanes(targetWindow)
				paneId = panes[0]?.id
			}

			const primaryPort = Object.values(ports)[0] ?? 3000
			const metadata: DevServerMetadata = {
				beadId: args.beadId,
				serverName,
				status: "running",
				port: primaryPort,
				paneId,
				worktreePath,
				projectPath: cwd,
				startedAt: new Date().toISOString(),
				beadPorts: ports,
			}

			const metadataJson = yield* Schema.encode(Schema.parseJson(DevServerMetadataSchema))(
				metadata,
			).pipe(Effect.catchAll(() => Effect.succeed("{}")))
			yield* tmux
				.setUserOption(sessionName, TMUX_OPT_DEV_METADATA, metadataJson)
				.pipe(Effect.ignore)

			if (args.json) {
				return {
					resultStatus: "started" as const,
					beadId: args.beadId,
					serverName,
					serverStatus: "running" as const,
					port: primaryPort,
					window: targetWindow,
				}
			}

			yield* Console.log(`Started dev server '${serverName}' for ${args.beadId}`)
			yield* Console.log(`  Port: ${primaryPort}`)
			yield* Console.log(`  Window: ${targetWindow}`)
			yield* Console.log(`  Command: ${serverConfig.command}`)

			return { resultStatus: "started" as const }
		}).pipe(Effect.provide(fullLayer))

		if (args.json && result) {
			yield* Console.log(JSON.stringify(result, null, 2))
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

		const tmuxLayer = TmuxService.Default
		const fullLayer = Layer.merge(tmuxLayer, BunContext.layer)

		const result = yield* Effect.gen(function* () {
			const tmux = yield* TmuxService

			const sessionName = getBeadSessionName(args.beadId)

			const hasSession = yield* tmux.hasSession(sessionName)
			if (!hasSession) {
				if (args.json) {
					return { resultStatus: "not_found" as const, message: "No session found" }
				}
				yield* Console.log(`No session found for ${args.beadId}`)
				return { resultStatus: "not_found" as const }
			}

			const metadataJson = yield* tmux.getUserOption(sessionName, TMUX_OPT_DEV_METADATA)
			if (Option.isNone(metadataJson)) {
				if (args.json) {
					return { resultStatus: "not_running" as const, message: "Dev server is not running" }
				}
				yield* Console.log(`Dev server '${serverName}' is not running for ${args.beadId}`)
				return { resultStatus: "not_running" as const }
			}

			const metadataOpt = yield* Schema.decodeUnknown(Schema.parseJson(DevServerMetadataSchema))(
				metadataJson.value,
			).pipe(Effect.option)

			if (Option.isNone(metadataOpt) || metadataOpt.value.status !== "running") {
				if (args.json) {
					return { resultStatus: "not_running" as const, message: "Dev server is not running" }
				}
				yield* Console.log(`Dev server '${serverName}' is not running for ${args.beadId}`)
				return { resultStatus: "not_running" as const }
			}

			const metadata = metadataOpt.value

			if (metadata.paneId) {
				yield* tmux.killPane(metadata.paneId).pipe(Effect.ignore)
			} else {
				const targetWindow = `${sessionName}:${WINDOW_NAMES.DEV}`
				yield* tmux.sendKeys(targetWindow, "C-c").pipe(Effect.ignore)
				yield* Effect.sleep("500 millis")
			}

			const updatedMetadata: DevServerMetadata = {
				beadId: metadata.beadId,
				serverName: metadata.serverName,
				status: "idle",
				worktreePath: metadata.worktreePath,
				projectPath: metadata.projectPath,
			}
			const updatedJson = yield* Schema.encode(Schema.parseJson(DevServerMetadataSchema))(
				updatedMetadata,
			).pipe(Effect.catchAll(() => Effect.succeed("{}")))
			yield* tmux.setUserOption(sessionName, TMUX_OPT_DEV_METADATA, updatedJson).pipe(Effect.ignore)

			if (args.json) {
				return { resultStatus: "stopped" as const, beadId: args.beadId, serverName }
			}

			yield* Console.log(`Stopped dev server '${serverName}' for ${args.beadId}`)
			return { resultStatus: "stopped" as const }
		}).pipe(Effect.provide(fullLayer))

		if (args.json && result) {
			yield* Console.log(JSON.stringify(result, null, 2))
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

		if (!args.json) {
			yield* Console.log(`Restarting dev server '${serverName}' for ${args.beadId}...`)
		}

		yield* devStopHandler({
			beadId: args.beadId,
			server: Option.some(serverName),
			verbose: args.verbose,
			json: false,
		}).pipe(Effect.ignore)

		yield* Effect.sleep("500 millis")

		yield* devStartHandler({
			beadId: args.beadId,
			server: Option.some(serverName),
			projectDir: args.projectDir,
			verbose: args.verbose,
			json: args.json,
		})
	})

const devStatusHandler = (args: {
	readonly beadId: string
	readonly verbose: boolean
	readonly json: boolean
}) =>
	Effect.gen(function* () {
		const tmuxLayer = TmuxService.Default
		const fullLayer = Layer.merge(tmuxLayer, BunContext.layer)

		yield* Effect.gen(function* () {
			const tmux = yield* TmuxService

			const sessionName = getBeadSessionName(args.beadId)

			const hasSession = yield* tmux.hasSession(sessionName)
			if (!hasSession) {
				if (args.json) {
					yield* Console.log(JSON.stringify({ beadId: args.beadId, servers: [] }))
					return
				}
				yield* Console.log(`No session found for ${args.beadId}`)
				return
			}

			const metadataJson = yield* tmux.getUserOption(sessionName, TMUX_OPT_DEV_METADATA)

			if (Option.isNone(metadataJson)) {
				if (args.json) {
					yield* Console.log(JSON.stringify({ beadId: args.beadId, servers: [] }))
					return
				}
				yield* Console.log(`No dev servers configured for ${args.beadId}`)
				return
			}

			const metadataOpt = yield* Schema.decodeUnknown(Schema.parseJson(DevServerMetadataSchema))(
				metadataJson.value,
			).pipe(Effect.option)

			if (Option.isNone(metadataOpt)) {
				if (args.json) {
					yield* Console.log(JSON.stringify({ beadId: args.beadId, servers: [] }))
					return
				}
				yield* Console.log(`No dev servers configured for ${args.beadId}`)
				return
			}

			const metadata = metadataOpt.value

			if (args.json) {
				yield* Console.log(
					JSON.stringify(
						{
							beadId: args.beadId,
							servers: [
								{
									name: metadata.serverName,
									status: metadata.status,
									port: metadata.port,
									uptime: metadata.startedAt ? formatUptime(metadata.startedAt) : null,
									startedAt: metadata.startedAt,
								},
							],
						},
						null,
						2,
					),
				)
				return
			}

			yield* Console.log(`Dev servers for ${args.beadId}:`)
			yield* Console.log("")

			const statusIcon =
				metadata.status === "running"
					? "🟢"
					: metadata.status === "starting"
						? "🟡"
						: metadata.status === "error"
							? "🔴"
							: "⚪"

			yield* Console.log(`  ${statusIcon} ${metadata.serverName}`)
			yield* Console.log(`      Status: ${metadata.status}`)
			if (metadata.port) {
				yield* Console.log(`      Port:   ${metadata.port}`)
			}
			if (metadata.startedAt && metadata.status === "running") {
				yield* Console.log(`      Uptime: ${formatUptime(metadata.startedAt)}`)
			}
			if (metadata.error) {
				yield* Console.log(`      Error:  ${metadata.error}`)
			}
		}).pipe(Effect.provide(fullLayer))
	})

const devListHandler = (args: { readonly verbose: boolean; readonly json: boolean }) =>
	Effect.gen(function* () {
		const tmuxLayer = TmuxService.Default
		const fullLayer = Layer.merge(tmuxLayer, BunContext.layer)

		yield* Effect.gen(function* () {
			const tmux = yield* TmuxService

			const sessions = yield* tmux.listSessions()
			const servers: Array<{ beadId: string; metadata: DevServerMetadata }> = []

			for (const session of sessions) {
				const parsed = parseSessionName(session.name)
				if (!parsed || parsed.type !== "bead") continue

				const metadataJson = yield* tmux.getUserOption(session.name, TMUX_OPT_DEV_METADATA)
				if (Option.isNone(metadataJson)) continue

				const metadataOpt = yield* Schema.decodeUnknown(Schema.parseJson(DevServerMetadataSchema))(
					metadataJson.value,
				).pipe(Effect.option)

				if (Option.isSome(metadataOpt)) {
					servers.push({ beadId: parsed.beadId, metadata: metadataOpt.value })
				}
			}

			const runningServers = servers.filter((s) => s.metadata.status === "running")

			if (args.json) {
				yield* Console.log(
					JSON.stringify(
						{
							servers: runningServers.map((s) => ({
								beadId: s.beadId,
								name: s.metadata.serverName,
								status: s.metadata.status,
								port: s.metadata.port,
								uptime: formatUptime(s.metadata.startedAt),
								startedAt: s.metadata.startedAt,
							})),
						},
						null,
						2,
					),
				)
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

			for (const server of runningServers) {
				const port = server.metadata.port?.toString() ?? "-"
				const uptime = formatUptime(server.metadata.startedAt)
				yield* Console.log(
					`  ${server.beadId.padEnd(12)} ${server.metadata.serverName.padEnd(9)} ${port.padEnd(7)} ${uptime}`,
				)
			}

			yield* Console.log("")
			yield* Console.log(`${runningServers.length} server(s) running`)
		}).pipe(Effect.provide(fullLayer))
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
