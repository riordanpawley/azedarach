import type { DaemonDevServerState } from "@azedarach/shared/rpc"
import { DaemonRpcClient } from "@azedarach/shared/rpc"
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
import { LocalIssueStore } from "../core/LocalIssueStore.js"
import {
	getDevWindowName,
	getIssueSessionName,
	getWorktreePath,
	parseDevWindowName,
	parseIssueSessionName,
} from "../core/paths.js"
import { TmuxService } from "../core/TmuxService.js"
import { WorktreeSessionService } from "../core/WorktreeSessionService.js"
import { BoardService } from "./BoardService.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { NavigationService } from "./NavigationService.js"
import { OverlayService } from "./OverlayService.js"
import { ProjectService } from "./ProjectService.js"

const TMUX_OPT_METADATA = "@az-devserver-meta"
const PORT_POLL_INTERVAL = 500
const PORT_DETECTION_TIMEOUT = 30000
const HEALTH_CHECK_INTERVAL = 5000
const PORT_CHECK_TIMEOUT_MS = 1000
const PackageScriptsSchema = Schema.Struct({
	scripts: Schema.optional(Schema.Record({ key: Schema.String, value: Schema.Unknown })),
})

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
		const logSocketError = (error: unknown): void => {
			void Effect.runFork(Effect.logWarning(error))
		}

		// Timeout handling
		const timeout = setTimeout(() => {
			socket
				.then((s) => s.end())
				.catch((error) => {
					logSocketError(error)
				})
			resume(Effect.succeed(false))
		}, PORT_CHECK_TIMEOUT_MS)

		// Cleanup timeout on success/error
		socket
			.then(() => clearTimeout(timeout))
			.catch((error) => {
				logSocketError(error)
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
	issueId: Schema.String,
	serverName: Schema.String,
	status: DevServerStatusSchema,
	port: Schema.optional(Schema.Number),
	windowName: Schema.optional(Schema.String),
	worktreePath: Schema.optional(Schema.String),
	projectPath: Schema.optional(Schema.String),
	startedAt: Schema.optional(Schema.String),
	error: Schema.optional(Schema.String),
	// All issue ports - shared across all servers for this issue
	issuePorts: Schema.optional(Schema.Record({ key: Schema.String, value: Schema.Number })),
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

export type IssueDevServersState = HashMap.HashMap<string, DevServerState>
export type DevServersState = HashMap.HashMap<string, IssueDevServersState>

export class DevServerError extends Data.TaggedError("DevServerError")<{
	readonly message: string
	readonly issueId?: string
}> {}

export class NoWorktreeError extends Data.TaggedError("NoWorktreeError")<{
	readonly issueId: string
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

const toDateOrUndefined = (value: string | null): Date | undefined => {
	if (value === null) return undefined
	const parsed = new Date(value)
	return Number.isNaN(parsed.getTime()) ? undefined : parsed
}

export class DevServerService extends Effect.Service<DevServerService>()("DevServerService", {
	dependencies: [
		TmuxService.Default,
		AppConfig.Default,
		ProjectService.Default,
		DiagnosticsService.Default,
		OverlayService.Default,
		WorktreeSessionService.Default,
		LocalIssueStore.Default,
		BoardService.Default,
		NavigationService.Default,
	],
	scoped: Effect.gen(function* () {
		const boardService = yield* BoardService
		const navigationService = yield* NavigationService
		const tmux = yield* TmuxService
		const worktreeSession = yield* WorktreeSessionService
		const appConfig = yield* AppConfig
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const overlayService = yield* OverlayService
		const projectService = yield* ProjectService
		const localIssueStore = yield* LocalIssueStore
		const diagnostics = yield* DiagnosticsService
		const daemonRpcClient = yield* DaemonRpcClient
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
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed("{}")),
						),
					),
				)
				yield* tmux.setUserOption(sessionName, TMUX_OPT_METADATA, json).pipe(Effect.ignore)
			})

		interface DiscoveredMetadata {
			state: DevServerState
			issuePorts?: Record<string, number>
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
					issuePorts: m.issuePorts,
				}))
			})

		interface DiscoveryResult {
			servers: DevServersState
			issuePorts: Map<string, Record<string, number>>
		}

		const discoverDevServers = (
			currentProjectPath: string,
		): Effect.Effect<DiscoveryResult, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const sessions = yield* tmux.listSessions()
				let servers: DevServersState = HashMap.empty()
				const issuePorts = new Map<string, Record<string, number>>()

				for (const session of sessions) {
					const parsed = parseIssueSessionName(session.name, currentProjectPath)
					if (!parsed || parsed.type !== "issue") continue

					// First try to restore from tmux metadata
					const metadataOpt = yield* readTmuxMetadata(session.name)
					if (Option.isSome(metadataOpt)) {
						const { state, issuePorts: discoveredPorts } = metadataOpt.value
						const issueServers = HashMap.get(servers, parsed.issueId).pipe(
							Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
						)
						servers = HashMap.set(
							servers,
							parsed.issueId,
							HashMap.set(issueServers, state.name, state),
						)

						// Restore issuePorts from first discovered server with ports
						if (discoveredPorts && !issuePorts.has(parsed.issueId)) {
							issuePorts.set(parsed.issueId, discoveredPorts)
						}
					}

					// Also scan for dev-* windows as fallback (durability improvement)
					// This catches servers that may be running but metadata was lost
					const windows = yield* tmux.listWindows(session.name)
					for (const windowName of windows) {
						const serverName = parseDevWindowName(windowName)
						if (!serverName) continue

						// Check if we already discovered this server via metadata
						const existingIssueServers = HashMap.get(servers, parsed.issueId).pipe(
							Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
						)
						if (Option.isSome(HashMap.get(existingIssueServers, serverName))) continue

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
							parsed.issueId,
							HashMap.set(existingIssueServers, serverName, fallbackState),
						)
					}
				}
				return { servers, issuePorts }
			})

		const currentProjectPath = yield* getEffectiveProjectPath()
		const discovery = yield* discoverDevServers(currentProjectPath)
		const serversRef = yield* SubscriptionRef.make<DevServersState>(discovery.servers)

		// Collect all allocated ports from discovery (both individual server ports and issue ports)
		const allDiscoveredPorts = new Set<number>()
		// Add individual server ports
		for (const issueServers of HashMap.values(discovery.servers)) {
			for (const server of HashMap.values(issueServers)) {
				if (server.port !== undefined) allDiscoveredPorts.add(server.port)
			}
		}
		// Add all ports from issuePorts mappings
		for (const ports of discovery.issuePorts.values()) {
			for (const port of Object.values(ports)) {
				allDiscoveredPorts.add(port)
			}
		}
		const allocatedPortsRef = yield* Ref.make<Set<number>>(allDiscoveredPorts)

		// Track per-issue port allocations so all servers for an issue share the same ports
		// Map from issueId -> { envVar -> allocatedPort }
		// Initialize from discovered metadata for persistence across restarts
		const issuePortsRef = yield* Ref.make<Map<string, Record<string, number>>>(discovery.issuePorts)

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

		const hasActiveDevServer = (issueServers: IssueDevServersState): boolean =>
			Array.from(HashMap.values(issueServers)).some(
				(server) => server.status === "running" || server.status === "starting",
			)

		const syncIssueBoardProjection = (
			issueId: string,
			hasDevServer: boolean,
		): Effect.Effect<void> =>
			Effect.gen(function* () {
				const effectiveProjectPath = yield* getEffectiveProjectPath()
				yield* localIssueStore
					.upsertBoardTaskState(
						{
							issueId,
							hasDevServer: hasDevServer ? true : undefined,
						},
						effectiveProjectPath,
					)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.void),
							),
						),
					)
				yield* boardService.patchTaskFromMutation(issueId, {
					hasDevServer: hasDevServer ? true : undefined,
				})
			})

		const syncBoardProjectionFromServers = (): Effect.Effect<void> =>
			Effect.gen(function* () {
				const effectiveProjectPath = yield* getEffectiveProjectPath()
				const [persistedTasks, servers] = yield* Effect.all([
					localIssueStore
						.listBoardTasks(effectiveProjectPath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed([])),
								),
							),
						),
					SubscriptionRef.get(serversRef),
				])
				const issueIds = new Set<string>(persistedTasks.map((task) => task.id))
				for (const issueId of HashMap.keys(servers)) {
					issueIds.add(issueId)
				}
				for (const issueId of issueIds) {
					const issueServers = HashMap.get(servers, issueId).pipe(
						Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
					)
					yield* syncIssueBoardProjection(issueId, hasActiveDevServer(issueServers))
				}
			})

		yield* syncBoardProjectionFromServers()

		const updateState = (
			issueId: string,
			serverName: string,
			update: Partial<DevServerState> | ((s: DevServerState) => DevServerState),
		): Effect.Effect<DevServerState, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const newState = yield* SubscriptionRef.modify(serversRef, (servers) => {
					const issueServers = HashMap.get(servers, issueId).pipe(
						Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
					)
					const current = HashMap.get(issueServers, serverName).pipe(
						Option.getOrElse(() => makeIdleState(serverName)),
					)
					const next = typeof update === "function" ? update(current) : { ...current, ...update }

					const nextIssueServers =
						next.status === "idle" && !next.error
							? HashMap.remove(issueServers, serverName)
							: HashMap.set(issueServers, serverName, next)

					const nextServers =
						HashMap.size(nextIssueServers) === 0
							? HashMap.remove(servers, issueId)
							: HashMap.set(servers, issueId, nextIssueServers)

					return [
						{
							state: next,
							hasDevServer: hasActiveDevServer(nextIssueServers),
						},
						nextServers,
					]
				})

				if (newState.state.tmuxSession && newState.state.status !== "idle") {
					const effectiveProjectPath = yield* getEffectiveProjectPath()
					// Include issuePorts in metadata for persistence across restarts
					const currentIssuePorts = yield* Ref.get(issuePortsRef)
					yield* storeTmuxMetadata(newState.state.tmuxSession, {
						issueId,
						serverName,
						status: newState.state.status,
						port: newState.state.port,
						windowName: newState.state.windowName,
						worktreePath: newState.state.worktreePath,
						projectPath: effectiveProjectPath,
						startedAt: newState.state.startedAt?.toISOString(),
						error: newState.state.error,
						issuePorts: currentIssuePorts.get(issueId),
					})
				}
				yield* syncIssueBoardProjection(issueId, newState.hasDevServer)
				return newState.state
			})

		const toLocalStateFromDaemon = (server: DaemonDevServerState): DevServerState => ({
			name: server.serverName,
			status: server.status,
			port: server.port ?? undefined,
			windowName: server.windowName ?? undefined,
			tmuxSession: server.tmuxSession ?? undefined,
			worktreePath: server.worktreePath ?? undefined,
			startedAt: toDateOrUndefined(server.startedAt),
			error: server.error ?? undefined,
		})

		const shouldPersistState = (state: DevServerState): boolean =>
			state.status !== "idle" || state.error !== undefined

		const syncIssueFromDaemonServers = (
			issueId: string,
			servers: ReadonlyArray<DaemonDevServerState>,
		): Effect.Effect<void> =>
			Effect.gen(function* () {
				let nextIssueServers: IssueDevServersState = HashMap.empty()
				for (const server of servers) {
					const localState = toLocalStateFromDaemon(server)
					if (!shouldPersistState(localState)) {
						continue
					}
					nextIssueServers = HashMap.set(nextIssueServers, localState.name, localState)
				}

				yield* SubscriptionRef.update(serversRef, (current) =>
					HashMap.size(nextIssueServers) === 0
						? HashMap.remove(current, issueId)
						: HashMap.set(current, issueId, nextIssueServers),
				)
				yield* syncIssueBoardProjection(issueId, hasActiveDevServer(nextIssueServers))
			})

		const syncAllFromDaemonServers = (
			servers: ReadonlyArray<DaemonDevServerState>,
		): Effect.Effect<void> =>
			Effect.gen(function* () {
				let nextServers: DevServersState = HashMap.empty()
				for (const server of servers) {
					const localState = toLocalStateFromDaemon(server)
					if (!shouldPersistState(localState)) {
						continue
					}
					const currentIssueServers = HashMap.get(nextServers, server.issueId).pipe(
						Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
					)
					nextServers = HashMap.set(
						nextServers,
						server.issueId,
						HashMap.set(currentIssueServers, localState.name, localState),
					)
				}
				yield* SubscriptionRef.set(serversRef, nextServers)
				yield* syncBoardProjectionFromServers()
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
				}).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(Effect.succeed(Option.none<number>())),
						),
					),
				)

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
		 * Get or allocate ports for an issue. All servers for the same issue share the same port allocations.
		 * First server to start for an issue allocates ALL ports from ALL configured servers.
		 * Subsequent servers reuse the same allocations.
		 */
		const getOrAllocateIssuePorts = (
			issueId: string,
			allServers: Record<string, { command: string; cwd?: string; ports?: Record<string, number> }>,
		) =>
			Effect.gen(function* () {
				const existing = yield* Ref.get(issuePortsRef)
				if (existing.has(issueId)) {
					return existing.get(issueId)!
				}

				// First server for this issue - allocate ALL ports from ALL configured servers
				const ports: Record<string, number> = {}
				const issueServers = yield* getIssueServersLocal(issueId)
				const offset = HashMap.size(HashMap.filter(issueServers, (s) => s.status === "running"))

				for (const serverConfig of Object.values(allServers)) {
					for (const [envVar, basePort] of Object.entries(serverConfig.ports ?? {})) {
						// Skip if already allocated (handles duplicate env vars across servers)
						if (ports[envVar] === undefined) {
							ports[envVar] = yield* allocatePort(basePort + offset)
						}
					}
				}

				yield* Ref.update(issuePortsRef, (m) => new Map(m).set(issueId, ports))
				return ports
			})

		/**
		 * Release all ports for an issue and clear its port allocation tracking.
		 */
		const releaseIssuePorts = (issueId: string) =>
			Effect.gen(function* () {
				const issuePorts = yield* Ref.get(issuePortsRef)
				const ports = issuePorts.get(issueId)
				if (ports) {
					for (const port of Object.values(ports)) {
						yield* releasePort(port)
					}
					yield* Ref.update(issuePortsRef, (m) => {
						const next = new Map(m)
						next.delete(issueId)
						return next
					})
				}
			})

		const _detectCommand = (worktreePath: string) =>
			Effect.gen(function* () {
				const pkgPath = pathService.join(worktreePath, "package.json")
				if (
					!(yield* fs
						.exists(pkgPath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						))
				) {
					return "npm run dev"
				}
				const content = yield* fs.readFileString(pkgPath)
				const pkg = yield* Schema.decode(Schema.parseJson(PackageScriptsSchema))(content).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
							Effect.zipRight(
								Effect.succeed<{ readonly scripts?: Readonly<Record<string, unknown>> }>({
									scripts: {},
								}),
							),
						),
					),
				)
				const scripts: Readonly<Record<string, unknown>> = pkg.scripts ?? {}
				const pm = (yield* fs
					.exists(pathService.join(worktreePath, "bun.lockb"))
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					))
					? "bun"
					: "npm"
				const hasDevScript = typeof scripts["dev"] === "string"
				const hasStartScript = typeof scripts["start"] === "string"
				return hasDevScript ? `${pm} run dev` : hasStartScript ? `${pm} run start` : `${pm} run dev`
			})

		const daemonHooks = yield* Effect.gen(function* () {
			const status = daemonRpcClient.devServerStatus
			const list = daemonRpcClient.devServerList
			const start = daemonRpcClient.devServerStart
			const stop = daemonRpcClient.devServerStop
			if (status === undefined || list === undefined || start === undefined || stop === undefined) {
				return yield* Effect.fail(
					new DevServerError({
						message:
							"Daemon dev-server RPC is unavailable (devServerStatus/devServerList/devServerStart/devServerStop required).",
					}),
				)
			}
			return { status, list, start, stop }
		})

		const healthCheckFiber = yield* Effect.scheduleForked(
			Schedule.spaced(`${HEALTH_CHECK_INTERVAL} millis`),
		)(
			Effect.gen(function* () {
				const projectPath = yield* getEffectiveProjectPath()
				yield* daemonHooks.list({ projectPath }).pipe(
					Effect.tap((result) => syncAllFromDaemonServers(result.servers).pipe(Effect.ignore)),
					Effect.catchAll((error) =>
						Effect.logWarning(`Dev server daemon health refresh failed: ${String(error)}`),
					),
				)
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

		function getServerStateLocal(issueId: string, name: string) {
			return SubscriptionRef.get(serversRef).pipe(
				Effect.map((s) =>
					HashMap.get(s, issueId).pipe(
						Option.flatMap(HashMap.get(name)),
						Option.getOrElse(() => makeIdleState(name)),
					),
				),
			)
		}

		function getIssueServersLocal(issueId: string) {
			return SubscriptionRef.get(serversRef).pipe(
				Effect.map((s) => HashMap.get(s, issueId).pipe(Option.getOrElse(() => HashMap.empty()))),
			)
		}

		function startLocal(issueId: string, projectPath: string, name: string) {
			return Effect.gen(function* () {
				const current = yield* getServerStateLocal(issueId, name)
				if (current.status === "running" || current.status === "starting") return current

				// Use canonical path computation instead of inline
				const worktreePath = getWorktreePath(projectPath, issueId)
				if (
					!(yield* fs
						.exists(worktreePath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						))
				) {
					return yield* Effect.fail(new NoWorktreeError({ issueId, message: "No worktree found" }))
				}

				yield* updateState(issueId, name, { status: "starting", worktreePath })
				const config = yield* appConfig.getDevServerConfig()
				const srvConfig = config.servers?.[name]

				if (!srvConfig) {
					return yield* Effect.fail(
						new DevServerError({
							issueId,
							message: `No server configuration found for '${name}'. Define it in devServer.servers.`,
						}),
					)
				}

				const command = srvConfig.command
				const thisPorts = srvConfig.ports ?? { PORT: 3000 }

				// Get or allocate ALL ports for this issue (shared across all servers)
				const allServerConfigs = config.servers ?? {}
				const issuePorts = yield* getOrAllocateIssuePorts(issueId, allServerConfigs)

				// Build env string with ALL issue ports
				const envStr = Object.entries(issuePorts)
					.map(([k, v]) => `${k}=${v}`)
					.join(" ")

				// Primary port is this server's first configured port
				const primaryEnvVar = Object.keys(thisPorts)[0]
				const primary = primaryEnvVar
					? issuePorts[primaryEnvVar]
					: (Object.values(issuePorts)[0] ?? 3000)

				// Use canonical session name and window naming
				const tmuxSessionName = getIssueSessionName(issueId, projectPath)
				const windowName = getDevWindowName(name)
				const targetWindow = `${tmuxSessionName}:${windowName}`
				const cwd = srvConfig?.cwd ? pathService.join(worktreePath, srvConfig.cwd) : worktreePath

				// Ensure the issue session exists
				yield* worktreeSession.getOrCreateSession(issueId, {
					worktreePath,
					projectPath,
					initCommands: (yield* appConfig.getWorktreeConfig()).initCommands,
				})

				// Create a dedicated window for this dev server
				// Each server gets its own window (e.g., dev-frontend, dev-api)
				yield* worktreeSession.ensureWindow(tmuxSessionName, windowName, {
					command: `${envStr} ${command}`,
					cwd,
				})

				const newState = yield* updateState(issueId, name, {
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
						p ? updateState(issueId, name, { port: p as number }) : Effect.void,
					),
					Effect.annotateLogs({
						issueId,
						serverName: name,
					}),
					Effect.forkIn(serviceScope),
				)

				yield* diagnostics.registerFiberIn(serviceScope, {
					id: `devserver-poll-${issueId}-${name}`,
					name: `Dev Server Poll (${name})`,
					description: `Polling for port on issue ${issueId}`,
					fiber: pollFiber,
				})

				return newState
			})
		}

		function stopLocal(issueId: string, name: string) {
			return Effect.gen(function* () {
				const s = yield* getServerStateLocal(issueId, name)
				// Kill the window for this dev server
				if (s.tmuxSession && s.windowName) {
					const windowTarget = `${s.tmuxSession}:${s.windowName}`
					yield* tmux.killWindow(windowTarget).pipe(Effect.ignore)
				}

				// Check if this is the last running server for this issue
				const issueServers = yield* getIssueServersLocal(issueId)
				const remainingRunning = HashMap.filter(
					issueServers,
					(srv) => srv.name !== name && (srv.status === "running" || srv.status === "starting"),
				)

				if (HashMap.size(remainingRunning) === 0) {
					// Last server stopping - release all issue ports
					yield* releaseIssuePorts(issueId)
				}

				yield* updateState(issueId, name, makeIdleState(name))
			})
		}

		const getServerState = (issueId: string, name: string) =>
			Effect.gen(function* () {
				const projectPath = yield* getEffectiveProjectPath()
				const daemonResult = yield* daemonHooks
					.status({
						issueId,
						serverName: name,
						projectPath,
					})
					.pipe(
						Effect.tap((result) =>
							syncIssueFromDaemonServers(issueId, [result.server]).pipe(Effect.ignore),
						),
					)
				return toLocalStateFromDaemon(daemonResult.server)
			})

		const getIssueServers = (issueId: string) =>
			Effect.gen(function* () {
				const projectPath = yield* getEffectiveProjectPath()
				const daemonResult = yield* daemonHooks
					.list({
						issueId,
						projectPath,
					})
					.pipe(
						Effect.tap((result) =>
							syncIssueFromDaemonServers(issueId, result.servers).pipe(Effect.ignore),
						),
					)
				let issueServers: IssueDevServersState = HashMap.empty()
				for (const server of daemonResult.servers) {
					const localState = toLocalStateFromDaemon(server)
					if (!shouldPersistState(localState)) {
						continue
					}
					issueServers = HashMap.set(issueServers, localState.name, localState)
				}
				return issueServers
			})

		const start = (issueId: string, projectPath: string, name: string) =>
			Effect.gen(function* () {
				const daemonResult = yield* daemonHooks
					.start({
						issueId,
						projectPath,
						serverName: name,
					})
					.pipe(
						Effect.tap((result) =>
							syncIssueFromDaemonServers(issueId, [result.server]).pipe(Effect.ignore),
						),
					)
				return toLocalStateFromDaemon(daemonResult.server)
			})

		const stop = (issueId: string, name: string, projectPath?: string) =>
			Effect.gen(function* () {
				const daemonProjectPath = projectPath ?? (yield* getEffectiveProjectPath())
				const daemonResult = yield* daemonHooks
					.stop({
						issueId,
						serverName: name,
						projectPath: daemonProjectPath,
					})
					.pipe(
						Effect.tap((result) =>
							syncIssueFromDaemonServers(issueId, [result.server]).pipe(Effect.ignore),
						),
					)
				return toLocalStateFromDaemon(daemonResult.server)
			})

		return {
			servers: serversRef,
			getStatus: (issueId: string, name = DEFAULT_SERVER_NAME) => getServerState(issueId, name),
			getIssueServers: (issueId: string) => getIssueServers(issueId),
			start: (issueId: string, projectPath: string, name = DEFAULT_SERVER_NAME) =>
				start(issueId, projectPath, name),
			stop: (issueId: string, name = DEFAULT_SERVER_NAME, projectPath?: string) =>
				stop(issueId, name, projectPath),
			getServersForOverlay: Effect.gen(function* () {
				const overlayIssueId = yield* overlayService
					.current()
					.pipe(Effect.map((o) => (o?._tag === "devServerMenu" ? o.issueId : null)))
				if (!overlayIssueId) {
					return yield* Effect.fail("Not in dev server overlay context")
				}
				const devServerConfig = yield* appConfig.getDevServerConfig()
				const servers = yield* getIssueServers(overlayIssueId)
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
				const issueId = yield* navigationService.focusedTaskId.pipe(SubscriptionRef.get)
				if (!issueId) {
					const empty: IssueDevServersState = HashMap.empty()
					return empty
				}
				const devServerConfig = yield* appConfig.getDevServerConfig()
				const servers = yield* getIssueServers(issueId)
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
			toggle: (issueId: string, projectPath: string, name = DEFAULT_SERVER_NAME) =>
				Effect.gen(function* () {
					const s = yield* getServerState(issueId, name)
					if (s.status === "running" || s.status === "starting") {
						yield* stop(issueId, name, projectPath)
						return yield* getServerState(issueId, name)
					}
					return yield* start(issueId, projectPath, name)
				}),
			syncState: (issueId: string, name = DEFAULT_SERVER_NAME) =>
				Effect.gen(function* () {
					return yield* getServerState(issueId, name)
				}),
		}
	}),
}) {}
