import { type CommandExecutor, FileSystem, Path } from "@effect/platform"
import {
	Data,
	Effect,
	HashMap,
	Option,
	Record,
	Ref,
	Schedule,
	Schema,
	SubscriptionRef,
} from "effect"
import { AppConfig } from "../config/index.js"
import { parseSessionName, WINDOW_NAMES } from "../core/paths.js"
import { TmuxService } from "../core/TmuxService.js"
import { WorktreeSessionService } from "../core/WorktreeSessionService.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { NavigationService } from "./NavigationService.js"
import { OverlayService } from "./OverlayService.js"
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
	paneId: Schema.optional(Schema.String),
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
	readonly paneId: string | undefined
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
	paneId: undefined,
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
		OverlayService.Default,
		WorktreeSessionService.Default,
		NavigationService.Default,
	],
	scoped: Effect.gen(function* () {
		const navigationService = yield* NavigationService
		const tmux = yield* TmuxService
		const worktreeSession = yield* WorktreeSessionService
		const appConfig = yield* AppConfig
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const overlayService = yield* OverlayService
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
					paneId: m.paneId,
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
					if (!parsed || parsed.type !== "bead") continue

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
						paneId: newState.paneId,
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
							Schedule.untilInput((o: unknown): o is Option.Some<number> =>
								Option.isSome(o as Option.Option<number>),
							),
						),
					),
				)
				return Option.isOption(result) ? Option.getOrUndefined(result) : undefined
			})

		const getPortEnv = (ports: Record<string, number>, offset: number) =>
			Effect.gen(function* () {
				const env: Record<string, string> = {}
				let primary: number | undefined

				for (const [envVar, basePort] of Object.entries(ports)) {
					const port = yield* allocatePort(basePort + offset)
					if (primary === undefined) primary = port
					env[envVar] = String(port)
				}
				return { env, primary: primary ?? 3000 }
			})

		const _detectCommand = (worktreePath: string) =>
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

		const healthCheckFiber = yield* Effect.scheduleForked(
			Schedule.spaced(`${HEALTH_CHECK_INTERVAL} millis`),
		)(
			Effect.gen(function* () {
				const servers = yield* SubscriptionRef.get(serversRef)
				for (const [beadId, beadServers] of HashMap.entries(servers)) {
					for (const [name, state] of HashMap.entries(beadServers)) {
						if (state.status === "running" && state.tmuxSession) {
							// Check if session and pane still exist
							const hasSession = yield* tmux.hasSession(state.tmuxSession.split(":")[0])
							let hasPane = false
							if (hasSession && state.paneId) {
								const panes = yield* tmux.listPanes(state.tmuxSession)
								hasPane = panes.some((p) => p.id === state.paneId)
							}

							if (!hasSession || (state.paneId && !hasPane)) {
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
		)

		yield* diagnostics.registerFiber({
			id: "devserver-health-check",
			name: "Dev Server Health Check",
			description: "Monitors dev server tmux sessions and panes",
			fiber: healthCheckFiber,
		})

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

		function getBeadServers(beadId: string) {
			return SubscriptionRef.get(serversRef).pipe(
				Effect.map((s) => HashMap.get(s, beadId).pipe(Option.getOrElse(() => HashMap.empty()))),
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
				const srvConfig = config.servers?.[name]

				if (!srvConfig) {
					return yield* Effect.fail(
						new DevServerError({
							beadId,
							message: `No server configuration found for '${name}'. Define it in devServer.servers.`,
						}),
					)
				}

				const command = srvConfig.command
				const ports = srvConfig.ports ?? { PORT: 3000 }

				const beadServers = yield* SubscriptionRef.get(serversRef).pipe(
					Effect.map((s) => HashMap.get(s, beadId).pipe(Option.getOrElse(() => HashMap.empty()))),
				)
				const beadRunning = HashMap.size(HashMap.filter(beadServers, (s) => s.status === "running"))

				const { env, primary } = yield* getPortEnv(ports, beadRunning)
				const envStr = Object.entries(env)
					.map(([k, v]) => `${k}=${v}`)
					.join(" ")

				const tmuxSessionName = beadId
				const targetWindow = `${tmuxSessionName}:${WINDOW_NAMES.DEV}`
				const cwd = srvConfig?.cwd ? pathService.join(worktreePath, srvConfig.cwd) : worktreePath

				yield* worktreeSession.getOrCreateSession(beadId, {
					worktreePath,
					projectPath: currentProjectPath,
					initCommands: (yield* appConfig.getWorktreeConfig()).initCommands,
				})

				// If there are already running servers for this bead, we should split the window
				const runningServers = HashMap.filter(beadServers, (s) => s.status === "running")
				let paneId: string | undefined

				if (HashMap.size(runningServers) > 0) {
					// Use the last running server's pane to split from, or just the window
					const lastRunning = Array.from(HashMap.values(runningServers)).pop()
					const splitTarget = lastRunning?.paneId ?? targetWindow

					paneId = yield* tmux.splitWindow(splitTarget, {
						cwd,
						command: `exec ${yield* appConfig.getSessionConfig().pipe(Effect.map((c) => c.shell))} -i`,
					})

					// Wait for the new pane's shell to be ready
					yield* Effect.sleep("500 millis")
					const marker = `tmux set-option -t ${tmuxSessionName} @az_pane_ready_${paneId.replace("%", "")} 1`
					yield* tmux.sendKeys(paneId, marker)

					yield* Effect.retry(
						Effect.gen(function* () {
							const ready = yield* tmux.getUserOption(
								tmuxSessionName,
								`@az_pane_ready_${paneId?.replace("%", "")}`,
							)
							if (Option.isNone(ready)) yield* Effect.fail("Not ready")
						}),
						{ times: 20, schedule: Schedule.spaced("100 millis") },
					)

					yield* tmux.sendKeys(paneId, `${envStr} ${command}`)
				} else {
					yield* worktreeSession.ensureWindow(tmuxSessionName, WINDOW_NAMES.DEV, {
						command: `${envStr} ${command}`,
						cwd,
					})
					// For the first pane, we don't have a specific pane ID easily from ensureWindow,
					// but we can list panes to find it.
					const panes = yield* tmux.listPanes(targetWindow)
					paneId = panes[0]?.id
				}

				const newState = yield* updateState(beadId, name, {
					status: "running",
					tmuxSession: targetWindow,
					paneId,
					port: primary,
					startedAt: new Date(),
				})

				const pollFiber = yield* pollForPort(
					paneId ?? targetWindow,
					new RegExp(config.portPattern ?? "localhost:(\\d+)|127\\.0\\.0\\.1:(\\d+)"),
				).pipe(
					Effect.flatMap((p) =>
						p ? updateState(beadId, name, { port: p as number }) : Effect.void,
					),
					Effect.annotateLogs({
						beadId,
						serverName: name,
					}),
					Effect.forkIn(serviceScope),
				)

				yield* diagnostics.registerFiberIn(serviceScope, {
					id: `devserver-poll-${beadId}-${name}`,
					name: `Dev Server Poll (${name})`,
					description: `Polling for port on bead ${beadId}`,
					fiber: pollFiber,
				})

				return newState
			})
		}

		function stop(beadId: string, name: string) {
			return Effect.gen(function* () {
				const s = yield* getServerState(beadId, name)
				if (s.paneId) {
					yield* tmux.killPane(s.paneId).pipe(Effect.ignore)
				} else if (s.tmuxSession) {
					yield* tmux.killSession(s.tmuxSession).pipe(Effect.ignore)
				}
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
			getServersForOverlay: Effect.gen(function* () {
				const overlayBeadId = yield* overlayService
					.current()
					.pipe(Effect.map((o) => (o?._tag === "devServerMenu" ? o.beadId : null)))
				if (!overlayBeadId) {
					return yield* Effect.fail("Not in dev server overlay context")
				}
				const devServerConfig = yield* appConfig.getDevServerConfig()
				const servers = yield* getBeadServers(overlayBeadId)
				yield* Effect.log({ overlay: servers, devServerConfig })

				// TODO: remove non servers record as config option
				return Record.toEntries(devServerConfig.servers ?? {}).map(
					([key, value]): DevServerState => {
						const portValues = Object.values(value.ports ?? {})
						const defaultPort = portValues.length > 0 ? portValues[0] : 3000
						return HashMap.get(servers, key).pipe(
							Option.match({
								onNone: (): DevServerState => ({
									name: key,
									status: "idle",
									port: defaultPort,
									startedAt: undefined,
									error: undefined,
									tmuxSession: undefined,
									worktreePath: "",
								}),
								onSome: (s): DevServerState => ({
									name: key,
									port: s.port ?? defaultPort,
									status: s.status,
									startedAt: s.startedAt,
									error: s.error,
									tmuxSession: s.tmuxSession,
									worktreePath: s.worktreePath,
								}),
							}),
						)
					},
				)
			}),
			getServersForTaskCard: Effect.gen(function* () {
				const beadId = yield* navigationService.focusedTaskId.pipe(SubscriptionRef.get)
				if (!beadId) {
					const empty: BeadDevServersState = HashMap.empty()
					return empty
				}
				const devServerConfig = yield* appConfig.getDevServerConfig()
				const servers = yield* getBeadServers(beadId)
				return Record.toEntries(devServerConfig.servers ?? {}).map(
					([key, _value]): {
						name: string
						status: "running" | "started" | "stopped"
						port: string
					} => {
						return {
							name: key,
							status: HashMap.get(servers, key).pipe(
								Option.match({
									onNone: () => "stopped" as const,
									onSome: (s) =>
										s.status === "running" ? ("running" as const) : ("started" as const),
								}),
							),
							port: HashMap.get(servers, key).pipe(
								Option.match({
									onNone: () => "N/A",
									onSome: (s) => (s.port ? String(s.port) : "Detecting..."),
								}),
							),
						}
					},
				)
			}),
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
					if (s.status === "running") {
						const hasSession = yield* tmux.hasSession(s.tmuxSession?.split(":")[0] ?? "")
						let hasPane = false
						if (hasSession && s.paneId) {
							const panes = yield* tmux.listPanes(s.tmuxSession ?? "")
							hasPane = panes.some((p) => p.id === s.paneId)
						}

						if (!hasSession || (s.paneId && !hasPane)) {
							if (s.port) yield* releasePort(s.port)
							return yield* updateState(beadId, name, {
								...makeIdleState(name),
								error: "Stopped unexpectedly",
							})
						}
					}
					return s
				}),
		}
	}),
}) {}
