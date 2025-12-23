import { type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, HashMap, Option, Ref, Schedule, Schema, SubscriptionRef } from "effect"
import { AppConfig } from "../config/index.js"
import { getDevSessionName, parseSessionName } from "../core/paths.js"
import { TmuxService } from "../core/TmuxService.js"
import { WorktreeSessionService } from "../core/WorktreeSessionService.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { ProjectService } from "./ProjectService.js"

const TMUX_OPT_METADATA = "@az-devserver-meta"
const PORT_POLL_INTERVAL = 500
const PORT_DETECTION_TIMEOUT = 30000
const HEALTH_CHECK_INTERVAL = 5000

export type DevServerStatus = "idle" | "starting" | "running" | "error"

const DevServerStatusSchema = Schema.Literal("idle", "starting", "running", "error")

const DevServerMetadata = Schema.Struct({
	beadId: Schema.String,
	serverName: Schema.String,
	status: DevServerStatusSchema,
	port: Schema.optional(Schema.Number),
	worktreePath: Schema.optional(Schema.String),
	projectPath: Schema.optional(Schema.String),
	startedAt: Schema.optional(Schema.String),
	error: Schema.optional(Schema.String),
})
type DevServerMetadata = Schema.Schema.Type<typeof DevServerMetadata>

export interface DevServerState {
	readonly name: string
	readonly status: DevServerStatus
	readonly port: number | undefined
	readonly tmuxSession: string | undefined
	readonly worktreePath: string | undefined
	readonly startedAt: Date | undefined
	readonly error: string | undefined
}

export type BeadDevServersState = HashMap.HashMap<string, DevServerState>
export type DevServersState = HashMap.HashMap<string, BeadDevServersState>

export class DevServerError extends Data.TaggedError("DevServerError")<{
	readonly message: string
	readonly beadId?: string
}> {}

export class NoWorktreeError extends Data.TaggedError("NoWorktreeError")<{
	readonly beadId: string
	readonly message: string
}> {}

const DEFAULT_SERVER_NAME = "default"

const makeIdleState = (name: string): DevServerState => ({
	name,
	status: "idle",
	port: undefined,
	tmuxSession: undefined,
	worktreePath: undefined,
	startedAt: undefined,
	error: undefined,
})

export class DevServerService extends Effect.Service<DevServerService>()("DevServerService", {
	dependencies: [
		TmuxService.Default,
		AppConfig.Default,
		ProjectService.Default,
		DiagnosticsService.Default,
		WorktreeSessionService.Default,
	],
	scoped: Effect.gen(function* () {
		const tmux = yield* TmuxService
		const worktreeSession = yield* WorktreeSessionService
		const appConfig = yield* AppConfig
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const projectService = yield* ProjectService
		const diagnostics = yield* DiagnosticsService
		const serviceScope = yield* Effect.scope

		yield* diagnostics.trackService("DevServerService", "Simplified dev server management")

		const getEffectiveProjectPath = (): Effect.Effect<string> =>
			projectService.getCurrentPath().pipe(Effect.map((p) => p ?? process.cwd()))

		const storeTmuxMetadata = (
			sessionName: string,
			metadata: DevServerMetadata,
		): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const json = yield* Schema.encode(Schema.parseJson(DevServerMetadata))(metadata).pipe(
					Effect.catchAll(() => Effect.succeed("{}")),
				)
				yield* tmux.setUserOption(sessionName, TMUX_OPT_METADATA, json).pipe(Effect.ignore)
			})

		const readTmuxMetadata = (
			sessionName: string,
		): Effect.Effect<Option.Option<DevServerState>, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const hasSession = yield* tmux.hasSession(sessionName)
				if (!hasSession) return Option.none()

				const jsonOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_METADATA)
				if (Option.isNone(jsonOpt)) return Option.none()

				const metadata = yield* Schema.decodeUnknown(Schema.parseJson(DevServerMetadata))(
					jsonOpt.value,
				).pipe(Effect.option)

				return Option.map(metadata, (m) => ({
					name: m.serverName,
					status: m.status,
					port: m.port,
					tmuxSession: sessionName,
					worktreePath: m.worktreePath,
					startedAt: m.startedAt ? new Date(m.startedAt) : undefined,
					error: m.error,
				}))
			})

		const discoverDevServers = (
			_currentProjectPath: string,
		): Effect.Effect<DevServersState, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const sessions = yield* tmux.listSessions()
				let result: DevServersState = HashMap.empty()

				for (const session of sessions) {
					const parsed = parseSessionName(session.name)
					if (!parsed || parsed.type !== "dev") continue

					const stateOpt = yield* readTmuxMetadata(session.name)
					if (Option.isNone(stateOpt)) continue

					const state = stateOpt.value
					const beadServers = HashMap.get(result, parsed.beadId).pipe(
						Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
					)
					result = HashMap.set(result, parsed.beadId, HashMap.set(beadServers, state.name, state))
				}
				return result
			})

		const currentProjectPath = yield* getEffectiveProjectPath()
		const initialServers = yield* discoverDevServers(currentProjectPath)
		const serversRef = yield* SubscriptionRef.make<DevServersState>(initialServers)

		const allocatedPortsRef = yield* Ref.make<Set<number>>(
			new Set(
				Array.from(HashMap.values(initialServers))
					.flatMap((m) => Array.from(HashMap.values(m)))
					.map((s) => s.port)
					.filter((p): p is number => p !== undefined),
			),
		)

		const allocatePort = (basePort: number): Effect.Effect<number> =>
			Ref.modify(allocatedPortsRef, (allocated) => {
				let port = basePort
				while (allocated.has(port)) port++
				const next = new Set(allocated).add(port)
				return [port, next]
			})

		const releasePort = (port: number): Effect.Effect<void> =>
			Ref.update(allocatedPortsRef, (allocated) => {
				const next = new Set(allocated)
				next.delete(port)
				return next
			})

		const updateState = (
			beadId: string,
			serverName: string,
			update: Partial<DevServerState> | ((s: DevServerState) => DevServerState),
		): Effect.Effect<DevServerState, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const newState = yield* SubscriptionRef.modify(serversRef, (servers) => {
					const beadServers = HashMap.get(servers, beadId).pipe(
						Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
					)
					const current = HashMap.get(beadServers, serverName).pipe(
						Option.getOrElse(() => makeIdleState(serverName)),
					)
					const next = typeof update === "function" ? update(current) : { ...current, ...update }

					const nextBeadServers =
						next.status === "idle" && !next.error
							? HashMap.remove(beadServers, serverName)
							: HashMap.set(beadServers, serverName, next)

					const nextServers =
						HashMap.size(nextBeadServers) === 0
							? HashMap.remove(servers, beadId)
							: HashMap.set(servers, beadId, nextBeadServers)

					return [next, nextServers]
				})

				if (newState.tmuxSession && newState.status !== "idle") {
					yield* storeTmuxMetadata(newState.tmuxSession, {
						beadId,
						serverName,
						status: newState.status,
						port: newState.port,
						worktreePath: newState.worktreePath,
						projectPath: currentProjectPath,
						startedAt: newState.startedAt?.toISOString(),
						error: newState.error,
					})
				}
				return newState
			})

		const pollForPort = (session: string, pattern: RegExp) =>
			Effect.gen(function* () {
				const tryDetect = Effect.gen(function* () {
					const output = yield* tmux.capturePane(session, 100)
					const match = output.match(pattern)
					if (match) {
						const port = parseInt(match[1] || match[2], 10)
						if (!Number.isNaN(port)) return Option.some(port)
					}
					return Option.none<number>()
				}).pipe(Effect.catchAll(() => Effect.succeed(Option.none<number>())))

				const result = yield* tryDetect.pipe(
					Effect.repeat(
						Schedule.spaced(`${PORT_POLL_INTERVAL} millis`).pipe(
							Schedule.upTo(`${PORT_DETECTION_TIMEOUT} millis`),
							Schedule.untilInput((o: any) => Option.isSome(o)),
						),
					),
				)
				return Option.getOrUndefined(result as any)
			})

		const getPortEnv = (offset: number) =>
			Effect.gen(function* () {
				const config = yield* appConfig.getDevServerConfig()
				const env: Record<string, string> = {}
				let primary: number | undefined

				const portsEntries = Object.entries(config.ports) as [
					string,
					{ default: number; aliases: string[] },
				][]
				for (const [_, p] of portsEntries) {
					const port = yield* allocatePort(p.default + offset)
					if (primary === undefined) primary = port
					for (const alias of p.aliases) env[alias] = String(port)
				}
				return { env, primary: primary ?? 3000 }
			})

		const detectCommand = (worktreePath: string) =>
			Effect.gen(function* () {
				const pkgPath = pathService.join(worktreePath, "package.json")
				if (!(yield* fs.exists(pkgPath).pipe(Effect.catchAll(() => Effect.succeed(false))))) {
					return "npm run dev"
				}
				const content = yield* fs.readFileString(pkgPath)
				const pkg = JSON.parse(content)
				const scripts = pkg.scripts ?? {}
				const pm = (yield* fs
					.exists(pathService.join(worktreePath, "bun.lockb"))
					.pipe(Effect.catchAll(() => Effect.succeed(false))))
					? "bun"
					: "npm"
				return scripts.dev ? `${pm} run dev` : scripts.start ? `${pm} run start` : `${pm} run dev`
			})

		yield* Effect.scheduleForked(Schedule.spaced(`${HEALTH_CHECK_INTERVAL} millis`))(
			Effect.gen(function* () {
				const servers = yield* SubscriptionRef.get(serversRef)
				for (const [beadId, beadServers] of HashMap.entries(servers)) {
					for (const [name, state] of HashMap.entries(beadServers)) {
						if (state.status === "running" && state.tmuxSession) {
							if (!(yield* tmux.hasSession(state.tmuxSession))) {
								if (state.port) yield* releasePort(state.port)
								yield* updateState(beadId, name, {
									...makeIdleState(name),
									error: "Stopped unexpectedly",
								})
							}
						}
					}
				}
			}),
		).pipe(Effect.forkIn(serviceScope))

		yield* Effect.addFinalizer(() =>
			Effect.gen(function* () {
				const servers = yield* SubscriptionRef.get(serversRef)
				for (const m of HashMap.values(servers)) {
					for (const s of HashMap.values(m)) {
						if (s.tmuxSession) yield* tmux.killSession(s.tmuxSession).pipe(Effect.ignore)
					}
				}
			}),
		)

		function getServerState(beadId: string, name: string) {
			return SubscriptionRef.get(serversRef).pipe(
				Effect.map((s) =>
					HashMap.get(s, beadId).pipe(
						Option.flatMap(HashMap.get(name)),
						Option.getOrElse(() => makeIdleState(name)),
					),
				),
			)
		}

		function start(beadId: string, projectPath: string, name: string) {
			return Effect.gen(function* () {
				const current = yield* getServerState(beadId, name)
				if (current.status === "running" || current.status === "starting") return current

				const projectName = pathService.basename(projectPath)
				const worktreePath = pathService.join(
					pathService.dirname(projectPath),
					`${projectName}-${beadId}`,
				)
				if (!(yield* fs.exists(worktreePath).pipe(Effect.catchAll(() => Effect.succeed(false))))) {
					return yield* Effect.fail(new NoWorktreeError({ beadId, message: "No worktree found" }))
				}

				yield* updateState(beadId, name, { status: "starting", worktreePath })
				const config = yield* appConfig.getDevServerConfig()
				const srvConfig = (config as any).servers?.[name]
				const command = srvConfig?.command ?? (yield* detectCommand(worktreePath))

				const running = HashMap.size(
					HashMap.flatMap(yield* SubscriptionRef.get(serversRef), (m) =>
						HashMap.filter(m, (s) => s.status === "running"),
					),
				)
				const { env, primary } = yield* getPortEnv(running)
				const envStr = Object.entries(env)
					.map(([k, v]) => `${k}=${v}`)
					.join(" ")

				const session =
					name === DEFAULT_SERVER_NAME
						? getDevSessionName(beadId)
						: `${getDevSessionName(beadId)}-${name}`
				const cwd = srvConfig?.cwd ? pathService.join(worktreePath, srvConfig.cwd) : worktreePath

				yield* worktreeSession.create({
					sessionName: session,
					worktreePath,
					command: `${envStr} ${command}`,
					cwd,
					initCommands: (yield* appConfig.getWorktreeConfig()).initCommands,
				})

				const newState = yield* updateState(beadId, name, {
					status: "running",
					tmuxSession: session,
					port: primary,
					startedAt: new Date(),
				})

				yield* pollForPort(
					session,
					new RegExp(config.portPattern ?? "localhost:(\\d+)|127\\.0\\.0\\.1:(\\d+)"),
				).pipe(
					Effect.flatMap((p) =>
						p ? updateState(beadId, name, { port: p as number }) : Effect.void,
					),
					Effect.forkIn(serviceScope),
				)

				return newState
			})
		}

		function stop(beadId: string, name: string) {
			return Effect.gen(function* () {
				const s = yield* getServerState(beadId, name)
				if (s.tmuxSession) yield* tmux.killSession(s.tmuxSession).pipe(Effect.ignore)
				if (s.port) yield* releasePort(s.port)
				yield* updateState(beadId, name, makeIdleState(name))
			})
		}

		return {
			servers: serversRef,
			getStatus: (beadId: string, name = DEFAULT_SERVER_NAME) => getServerState(beadId, name),
			getBeadServers: (beadId: string) =>
				SubscriptionRef.get(serversRef).pipe(
					Effect.map((s) => HashMap.get(s, beadId).pipe(Option.getOrElse(() => HashMap.empty()))),
				),
			start: (beadId: string, projectPath: string, name = DEFAULT_SERVER_NAME) =>
				start(beadId, projectPath, name),
			stop: (beadId: string, name = DEFAULT_SERVER_NAME) => stop(beadId, name),
			toggle: (beadId: string, projectPath: string, name = DEFAULT_SERVER_NAME) =>
				Effect.gen(function* () {
					const s = yield* getServerState(beadId, name)
					if (s.status === "running" || s.status === "starting") {
						yield* stop(beadId, name)
						return yield* getServerState(beadId, name)
					}
					return yield* start(beadId, projectPath, name)
				}),
			syncState: (beadId: string, name = DEFAULT_SERVER_NAME) =>
				Effect.gen(function* () {
					const s = yield* getServerState(beadId, name)
					if (s.tmuxSession && !(yield* tmux.hasSession(s.tmuxSession)) && s.status === "running") {
						if (s.port) yield* releasePort(s.port)
						return yield* updateState(beadId, name, {
							...makeIdleState(name),
							error: "Stopped unexpectedly",
						})
					}
					return s
				}),
		}
	}),
}) {}
