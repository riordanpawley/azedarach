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
import {
	DEV_WINDOW_PREFIX,
	getBeadSessionName,
	getDevWindowName,
	getWorktreePath,
	parseDevWindowName,
	parseSessionName,
} from "../core/paths.js"
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
const PORT_CHECK_TIMEOUT_MS = 1000

/**
 * Check if a port is open on a specific host by attempting a TCP connection.
 * Returns true if connection succeeds, false otherwise.
 */
const checkPortOpenOnHost = (port: number, host: string): Effect.Effect<boolean> =>
	Effect.async<boolean>((resume) => {
		const socket = Bun.connect({
			hostname: host,
			port,
			socket: {
				open(socket) {
					socket.end()
					resume(Effect.succeed(true))
				},
				error() {
					resume(Effect.succeed(false))
				},
				close() {
					// Connection closed after successful open is fine
				},
				data() {
					// We don't expect data, just checking connectivity
				},
			},
		})

		// Timeout handling
		const timeout = setTimeout(() => {
			socket.then((s) => s.end()).catch(() => {})
			resume(Effect.succeed(false))
		}, PORT_CHECK_TIMEOUT_MS)

		// Cleanup timeout on success/error
		socket
			.then(() => clearTimeout(timeout))
			.catch(() => {
				clearTimeout(timeout)
				resume(Effect.succeed(false))
			})
	})

/**
 * Check if a port is open on localhost (either IPv4 or IPv6).
 * Checks both 127.0.0.1 and ::1 in parallel, returns true if either succeeds.
 * This handles dev servers that bind to IPv6-only (like Bun's default).
 */
const checkPortOpen = (port: number): Effect.Effect<boolean> =>
	Effect.all([checkPortOpenOnHost(port, "127.0.0.1"), checkPortOpenOnHost(port, "::1")], {
		concurrency: "unbounded",
	}).pipe(Effect.map(([ipv4, ipv6]) => ipv4 || ipv6))

export type DevServerStatus = "idle" | "starting" | "running" | "stopped" | "error"

const DevServerStatusSchema = Schema.Literal("idle", "starting", "running", "stopped", "error")

const DevServerMetadata = Schema.Struct({
	beadId: Schema.String,
	serverName: Schema.String,
	status: DevServerStatusSchema,
	port: Schema.optional(Schema.Number),
	windowName: Schema.optional(Schema.String),
	worktreePath: Schema.optional(Schema.String),
	projectPath: Schema.optional(Schema.String),
	startedAt: Schema.optional(Schema.String),
	error: Schema.optional(Schema.String),
	// All bead ports - shared across all servers for this bead
	beadPorts: Schema.optional(Schema.Record({ key: Schema.String, value: Schema.Number })),
})
type DevServerMetadata = Schema.Schema.Type<typeof DevServerMetadata>

export interface DevServerState {
	readonly name: string
	readonly status: DevServerStatus
	readonly port: number | undefined
	readonly windowName: string | undefined
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
	windowName: undefined,
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

		interface DiscoveredMetadata {
			state: DevServerState
			beadPorts?: Record<string, number>
		}

		const readTmuxMetadata = (
			sessionName: string,
		): Effect.Effect<Option.Option<DiscoveredMetadata>, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const hasSession = yield* tmux.hasSession(sessionName)
				if (!hasSession) return Option.none()

				const jsonOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_METADATA)
				if (Option.isNone(jsonOpt)) return Option.none()

				const metadata = yield* Schema.decodeUnknown(Schema.parseJson(DevServerMetadata))(
					jsonOpt.value,
				).pipe(Effect.option)

				return Option.map(metadata, (m) => ({
					state: {
						name: m.serverName,
						status: m.status,
						port: m.port,
						windowName: m.windowName,
						tmuxSession: sessionName,
						worktreePath: m.worktreePath,
						startedAt: m.startedAt ? new Date(m.startedAt) : undefined,
						error: m.error,
					},
					beadPorts: m.beadPorts,
				}))
			})

		interface DiscoveryResult {
			servers: DevServersState
			beadPorts: Map<string, Record<string, number>>
		}

		const discoverDevServers = (
			_currentProjectPath: string,
		): Effect.Effect<DiscoveryResult, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const sessions = yield* tmux.listSessions()
				let servers: DevServersState = HashMap.empty()
				const beadPorts = new Map<string, Record<string, number>>()

				for (const session of sessions) {
					const parsed = parseSessionName(session.name)
					if (!parsed || parsed.type !== "bead") continue

					// First try to restore from tmux metadata
					const metadataOpt = yield* readTmuxMetadata(session.name)
					if (Option.isSome(metadataOpt)) {
						const { state, beadPorts: discoveredPorts } = metadataOpt.value
						const beadServers = HashMap.get(servers, parsed.beadId).pipe(
							Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
						)
						servers = HashMap.set(
							servers,
							parsed.beadId,
							HashMap.set(beadServers, state.name, state),
						)

						// Restore beadPorts from first discovered server with ports
						if (discoveredPorts && !beadPorts.has(parsed.beadId)) {
							beadPorts.set(parsed.beadId, discoveredPorts)
						}
					}

					// Also scan for dev-* windows as fallback (durability improvement)
					// This catches servers that may be running but metadata was lost
					const windows = yield* tmux.listWindows(session.name)
					for (const windowName of windows) {
						const serverName = parseDevWindowName(windowName)
						if (!serverName) continue

						// Check if we already discovered this server via metadata
						const existingBeadServers = HashMap.get(servers, parsed.beadId).pipe(
							Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
						)
						if (Option.isSome(HashMap.get(existingBeadServers, serverName))) continue

						// Found a dev window without metadata - create a fallback state
						// We'll mark it as running and let the health check verify via port
						const fallbackState: DevServerState = {
							name: serverName,
							status: "running",
							port: undefined, // Will be detected by port polling
							windowName,
							tmuxSession: session.name,
							worktreePath: undefined,
							startedAt: undefined,
							error: undefined,
						}
						servers = HashMap.set(
							servers,
							parsed.beadId,
							HashMap.set(existingBeadServers, serverName, fallbackState),
						)
					}
				}
				return { servers, beadPorts }
			})

		const currentProjectPath = yield* getEffectiveProjectPath()
		const discovery = yield* discoverDevServers(currentProjectPath)
		const serversRef = yield* SubscriptionRef.make<DevServersState>(discovery.servers)

		// Collect all allocated ports from discovery (both individual server ports and bead ports)
		const allDiscoveredPorts = new Set<number>()
		// Add individual server ports
		for (const beadServers of HashMap.values(discovery.servers)) {
			for (const server of HashMap.values(beadServers)) {
				if (server.port !== undefined) allDiscoveredPorts.add(server.port)
			}
		}
		// Add all ports from beadPorts mappings
		for (const ports of discovery.beadPorts.values()) {
			for (const port of Object.values(ports)) {
				allDiscoveredPorts.add(port)
			}
		}
		const allocatedPortsRef = yield* Ref.make<Set<number>>(allDiscoveredPorts)

		// Track per-bead port allocations so all servers for a bead share the same ports
		// Map from beadId -> { envVar -> allocatedPort }
		// Initialize from discovered metadata for persistence across restarts
		const beadPortsRef = yield* Ref.make<Map<string, Record<string, number>>>(discovery.beadPorts)

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
					// Include beadPorts in metadata for persistence across restarts
					const currentBeadPorts = yield* Ref.get(beadPortsRef)
					yield* storeTmuxMetadata(newState.tmuxSession, {
						beadId,
						serverName,
						status: newState.status,
						port: newState.port,
						windowName: newState.windowName,
						worktreePath: newState.worktreePath,
						projectPath: currentProjectPath,
						startedAt: newState.startedAt?.toISOString(),
						error: newState.error,
						beadPorts: currentBeadPorts.get(beadId),
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

		/**
		 * Get or allocate ports for a bead. All servers for the same bead share the same port allocations.
		 * First server to start for a bead allocates ALL ports from ALL configured servers.
		 * Subsequent servers reuse the same allocations.
		 */
		const getOrAllocateBeadPorts = (
			beadId: string,
			allServers: Record<string, { command: string; cwd?: string; ports?: Record<string, number> }>,
		) =>
			Effect.gen(function* () {
				const existing = yield* Ref.get(beadPortsRef)
				if (existing.has(beadId)) {
					return existing.get(beadId)!
				}

				// First server for this bead - allocate ALL ports from ALL configured servers
				const ports: Record<string, number> = {}
				const beadServers = yield* getBeadServers(beadId)
				const offset = HashMap.size(HashMap.filter(beadServers, (s) => s.status === "running"))

				for (const serverConfig of Object.values(allServers)) {
					for (const [envVar, basePort] of Object.entries(serverConfig.ports ?? {})) {
						// Skip if already allocated (handles duplicate env vars across servers)
						if (ports[envVar] === undefined) {
							ports[envVar] = yield* allocatePort(basePort + offset)
						}
					}
				}

				yield* Ref.update(beadPortsRef, (m) => new Map(m).set(beadId, ports))
				return ports
			})

		/**
		 * Release all ports for a bead and clear its port allocation tracking.
		 */
		const releaseBeadPorts = (beadId: string) =>
			Effect.gen(function* () {
				const beadPorts = yield* Ref.get(beadPortsRef)
				const ports = beadPorts.get(beadId)
				if (ports) {
					for (const port of Object.values(ports)) {
						yield* releasePort(port)
					}
					yield* Ref.update(beadPortsRef, (m) => {
						const next = new Map(m)
						next.delete(beadId)
						return next
					})
				}
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
						// Check running servers - verify window exists and port is responding
						if (state.status === "running" && state.tmuxSession) {
							// Check if session and window still exist
							const hasSession = yield* tmux.hasSession(state.tmuxSession)
							let hasWindow = false
							if (hasSession && state.windowName) {
								hasWindow = yield* tmux.hasWindow(state.tmuxSession, state.windowName)
							}

							if (!hasSession || (state.windowName && !hasWindow)) {
								// Session/window gone - mark as idle
								if (state.port) yield* releasePort(state.port)
								yield* updateState(beadId, name, {
									...makeIdleState(name),
									error: "Stopped unexpectedly",
								})
							} else if (state.port) {
								// Window exists - check if port is still responding
								const portOpen = yield* checkPortOpen(state.port)
								if (!portOpen) {
									// Port is down but window exists - server was stopped manually (e.g., Ctrl+C)
									yield* updateState(beadId, name, {
										status: "stopped",
										error: undefined,
									})
								}
							}
						}

						// Check stopped servers - detect if port comes back (manual restart)
						if (state.status === "stopped" && state.port && state.tmuxSession) {
							const portOpen = yield* checkPortOpen(state.port)
							if (portOpen) {
								// Port is back! Server was manually restarted
								yield* updateState(beadId, name, {
									status: "running",
									startedAt: new Date(),
									error: undefined,
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
			description: "Monitors dev server tmux sessions and windows",
			fiber: healthCheckFiber,
		})

		// NOTE: We intentionally do NOT add a finalizer to kill sessions here.
		// The finalizer would run when CLI commands exit (since cliLayer includes DevServerService),
		// which would incorrectly kill Claude sessions that have dev servers running.
		// Sessions should persist until explicitly stopped by the user.

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

				// Use canonical path computation instead of inline
				const worktreePath = getWorktreePath(projectPath, beadId)
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
				const thisPorts = srvConfig.ports ?? { PORT: 3000 }

				// Get or allocate ALL ports for this bead (shared across all servers)
				const allServerConfigs = config.servers ?? {}
				const beadPorts = yield* getOrAllocateBeadPorts(beadId, allServerConfigs)

				// Build env string with ALL bead ports
				const envStr = Object.entries(beadPorts)
					.map(([k, v]) => `${k}=${v}`)
					.join(" ")

				// Primary port is this server's first configured port
				const primaryEnvVar = Object.keys(thisPorts)[0]
				const primary = primaryEnvVar
					? beadPorts[primaryEnvVar]
					: (Object.values(beadPorts)[0] ?? 3000)

				// Use canonical session name and window naming
				const tmuxSessionName = getBeadSessionName(beadId)
				const windowName = getDevWindowName(name)
				const targetWindow = `${tmuxSessionName}:${windowName}`
				const cwd = srvConfig?.cwd ? pathService.join(worktreePath, srvConfig.cwd) : worktreePath

				// Ensure the bead session exists
				yield* worktreeSession.getOrCreateSession(beadId, {
					worktreePath,
					projectPath: currentProjectPath,
					initCommands: (yield* appConfig.getWorktreeConfig()).initCommands,
				})

				// Create a dedicated window for this dev server
				// Each server gets its own window (e.g., dev-frontend, dev-api)
				yield* worktreeSession.ensureWindow(tmuxSessionName, windowName, {
					command: `${envStr} ${command}`,
					cwd,
				})

				const newState = yield* updateState(beadId, name, {
					status: "running",
					tmuxSession: tmuxSessionName,
					windowName,
					port: primary,
					startedAt: new Date(),
				})

				// Poll for port detection in the dedicated window
				const pollFiber = yield* pollForPort(
					targetWindow,
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
				// Kill the window for this dev server
				if (s.tmuxSession && s.windowName) {
					const windowTarget = `${s.tmuxSession}:${s.windowName}`
					yield* tmux.killWindow(windowTarget).pipe(Effect.ignore)
				}

				// Check if this is the last running server for this bead
				const beadServers = yield* getBeadServers(beadId)
				const remainingRunning = HashMap.filter(
					beadServers,
					(srv) => srv.name !== name && (srv.status === "running" || srv.status === "starting"),
				)

				if (HashMap.size(remainingRunning) === 0) {
					// Last server stopping - release all bead ports
					yield* releaseBeadPorts(beadId)
				}

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
									windowName: undefined,
									startedAt: undefined,
									error: undefined,
									tmuxSession: undefined,
									worktreePath: "",
								}),
								onSome: (s): DevServerState => ({
									name: key,
									port: s.port ?? defaultPort,
									status: s.status,
									windowName: s.windowName,
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
									onSome: (s) => {
										if (s.status === "running") return "running" as const
										if (s.status === "stopped" || s.status === "idle" || s.status === "error")
											return "stopped" as const
										return "started" as const // "starting" status
									},
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
						const hasSession = yield* tmux.hasSession(s.tmuxSession ?? "")
						let hasWindow = false
						if (hasSession && s.windowName) {
							hasWindow = yield* tmux.hasWindow(s.tmuxSession ?? "", s.windowName)
						}

						if (!hasSession || (s.windowName && !hasWindow)) {
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
