/**
 * DevServerService - Manages per-worktree dev servers
 *
 * Each worktree can have its own dev server running with injected port environment variables.
 * Ports are allocated sequentially to avoid conflicts between parallel sessions.
 *
 * Key features:
 * - Per-worktree dev server lifecycle
 * - Multi-port injection (PORT, VITE_PORT, VITE_SERVER_PORT, etc.)
 * - Automatic port detection from server output
 * - Command detection from package.json/lock files
 * - Health monitoring: detects dead sessions every 5s and updates state
 * - Auto-cleanup on service scope exit
 */

import { type CommandExecutor, FileSystem, Path } from "@effect/platform"
import { Data, Effect, HashMap, Option, Ref, Schedule, Schema, SubscriptionRef } from "effect"
import { AppConfig } from "../config/index.js"
import { type TmuxError, TmuxService } from "../core/TmuxService.js"
import {
	type WorktreeSessionError,
	WorktreeSessionService,
} from "../core/WorktreeSessionService.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { ProjectService } from "./ProjectService.js"

// ============================================================================
// Constants
// ============================================================================

/** tmux session name prefix for dev servers */
const DEV_SESSION_PREFIX = "az-dev-"

/** How often to poll for port detection (ms) */
const PORT_POLL_INTERVAL = 500

/** Maximum time to wait for port detection (ms) */
const PORT_DETECTION_TIMEOUT = 30000

/** How often to check if dev server sessions are still alive (ms) */
const HEALTH_CHECK_INTERVAL = 5000

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Dev server status
 */
export type DevServerStatus = "idle" | "starting" | "running" | "error"

/**
 * Dev server state for a single worktree
 */
export interface DevServerState {
	readonly status: DevServerStatus
	readonly port: number | undefined
	readonly tmuxSession: string | undefined
	readonly worktreePath: string | undefined
	readonly startedAt: Date | undefined
	readonly error: string | undefined
}

/**
 * All dev servers state (map from beadId to state)
 */
export type DevServersState = HashMap.HashMap<string, DevServerState>

// ============================================================================
// Error Types
// ============================================================================

export class DevServerError extends Data.TaggedError("DevServerError")<{
	readonly message: string
	readonly beadId?: string
}> {}

export class NoWorktreeError extends Data.TaggedError("NoWorktreeError")<{
	readonly beadId: string
	readonly message: string
}> {}

// ============================================================================
// Persistence Schema
// ============================================================================

/**
 * Schema for persisted dev server state
 * Uses UndefinedOr with proper JSON representation
 */
const PersistedDevServerStateSchema = Schema.Struct({
	status: Schema.Literal("idle", "starting", "running", "error"),
	port: Schema.UndefinedOr(Schema.Number),
	tmuxSession: Schema.UndefinedOr(Schema.String),
	worktreePath: Schema.UndefinedOr(Schema.String),
	startedAt: Schema.UndefinedOr(Schema.Date),
	error: Schema.UndefinedOr(Schema.String),
})

/**
 * Schema for persisted dev servers map (with JSON parsing)
 */
const PersistedDevServersSchema = Schema.parseJson(
	Schema.HashMap({
		key: Schema.String,
		value: PersistedDevServerStateSchema,
	}),
)

// ============================================================================
// Helper Functions
// ============================================================================

/**
 * Get tmux session name for a bead's dev server
 */
const getDevSessionName = (beadId: string): string => `${DEV_SESSION_PREFIX}${beadId}`

/**
 * Create empty/idle state
 */
const idleState: DevServerState = {
	status: "idle",
	port: undefined,
	tmuxSession: undefined,
	worktreePath: undefined,
	startedAt: undefined,
	error: undefined,
}

// ============================================================================
// Service Implementation
// ============================================================================

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

		// Capture service scope for forking background tasks
		// This ensures polling fibers are cleaned up when service shuts down
		const serviceScope = yield* Effect.scope

		// Register with diagnostics - tracks service health
		yield* diagnostics.trackService("DevServerService", "Per-worktree dev server lifecycle")

		// Persistence file path
		const devServersFilePath = ".azedarach/devservers.json"

		/**
		 * Get the current project path from ProjectService, falling back to process.cwd()
		 */
		const getEffectiveProjectPath = (): Effect.Effect<string> =>
			Effect.gen(function* () {
				const projectPath = yield* projectService.getCurrentPath()
				return projectPath ?? process.cwd()
			})

		/**
		 * Load persisted dev servers from disk
		 */
		const loadPersistedServers = (): Effect.Effect<DevServersState> =>
			Effect.gen(function* () {
				const projectPath = yield* getEffectiveProjectPath()
				const filePath = pathService.join(projectPath, devServersFilePath)

				const exists = yield* fs.exists(filePath)
				if (!exists) return HashMap.empty<string, DevServerState>()

				const content = yield* fs.readFileString(filePath)
				// Schema.parseJson expects string input, so decode (not decodeUnknown) is correct
				return yield* Schema.decode(PersistedDevServersSchema)(content)
			}).pipe(Effect.catchAll(() => Effect.succeed(HashMap.empty<string, DevServerState>())))

		/**
		 * Save dev servers to disk
		 */
		const persistServers = (servers: DevServersState): Effect.Effect<void> =>
			Effect.gen(function* () {
				const projectPath = yield* getEffectiveProjectPath()
				const dirPath = pathService.join(projectPath, ".azedarach")
				const filePath = pathService.join(dirPath, "devservers.json")

				yield* fs.makeDirectory(dirPath, { recursive: true }).pipe(Effect.ignore)
				// Schema.parseJson handles JSON stringification
				const json = yield* Schema.encode(PersistedDevServersSchema)(servers)
				yield* fs.writeFileString(filePath, json).pipe(Effect.ignore)
			}).pipe(Effect.catchAll(() => Effect.void))

		/**
		 * Sync persisted state with actual tmux sessions
		 * Removes entries for sessions that no longer exist
		 */
		const syncWithTmux = (
			servers: DevServersState,
		): Effect.Effect<DevServersState, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				let updated = servers
				for (const [beadId, state] of HashMap.entries(servers)) {
					if (state.tmuxSession) {
						const hasSession = yield* tmux.hasSession(state.tmuxSession)
						if (!hasSession) {
							// Session died, mark as idle
							yield* Effect.log(`Dev server for ${beadId} no longer running, marking idle`)
							updated = HashMap.set(updated, beadId, idleState)
						}
					}
				}
				return updated
			})

		// Load persisted state and sync with tmux on startup
		const persisted = yield* loadPersistedServers()
		const synced = yield* syncWithTmux(persisted)

		// Populate allocated ports from recovered state
		const initialPorts = new Set<number>()
		for (const state of HashMap.values(synced)) {
			if (state.port) {
				initialPorts.add(state.port)
			}
		}

		// Track all dev servers state
		const serversRef = yield* SubscriptionRef.make<DevServersState>(synced)

		// Track allocated ports to avoid conflicts
		const allocatedPortsRef = yield* Ref.make<Set<number>>(initialPorts)

		yield* Effect.log(`DevServerService: Recovered ${HashMap.size(synced)} dev servers`)

		/**
		 * Allocate a port for a given port type, avoiding conflicts
		 */
		const allocatePort = (basePort: number): Effect.Effect<number> =>
			Ref.modify(allocatedPortsRef, (allocated) => {
				let port = basePort
				// Find next available port starting from base
				while (allocated.has(port)) {
					port++
				}
				const newAllocated = new Set(allocated)
				newAllocated.add(port)
				return [port, newAllocated]
			})

		/**
		 * Release a port back to the pool
		 */
		const releasePort = (port: number): Effect.Effect<void> =>
			Ref.update(allocatedPortsRef, (allocated) => {
				const newAllocated = new Set(allocated)
				newAllocated.delete(port)
				return newAllocated
			})

		/**
		 * Build environment variables for port injection
		 */
		const buildPortEnv = (
			portOffset: number,
		): Effect.Effect<{ env: Record<string, string>; primaryPort: number }> =>
			Effect.gen(function* () {
				const devServerConfig = yield* appConfig.getDevServerConfig()
				const portsConfig = devServerConfig.ports
				const env: Record<string, string> = {}
				let primaryPort: number | undefined

				for (const [portType, portConfig] of Object.entries(portsConfig)) {
					const basePort = portConfig.default + portOffset
					const port = yield* allocatePort(basePort)

					// First port type becomes the primary (displayed in StatusBar)
					if (primaryPort === undefined) {
						primaryPort = port
					}

					// Set all aliases to this port
					for (const alias of portConfig.aliases) {
						env[alias] = String(port)
					}

					yield* Effect.log(
						`Allocated ${portType} port ${port} for aliases: ${portConfig.aliases.join(", ")}`,
					)
				}

				return { env, primaryPort: primaryPort ?? 3000 }
			})

		/**
		 * Detect package manager from lock files
		 */
		const detectPackageManager = (
			worktreePath: string,
		): Effect.Effect<"bun" | "pnpm" | "yarn" | "npm"> =>
			Effect.gen(function* () {
				const bunLock = pathService.join(worktreePath, "bun.lockb")
				const pnpmLock = pathService.join(worktreePath, "pnpm-lock.yaml")
				const yarnLock = pathService.join(worktreePath, "yarn.lock")
				const npmLock = pathService.join(worktreePath, "package-lock.json")

				if (yield* fs.exists(bunLock).pipe(Effect.catchAll(() => Effect.succeed(false)))) {
					return "bun"
				}
				if (yield* fs.exists(pnpmLock).pipe(Effect.catchAll(() => Effect.succeed(false)))) {
					return "pnpm"
				}
				if (yield* fs.exists(yarnLock).pipe(Effect.catchAll(() => Effect.succeed(false)))) {
					return "yarn"
				}
				if (yield* fs.exists(npmLock).pipe(Effect.catchAll(() => Effect.succeed(false)))) {
					return "npm"
				}
				return "npm" // fallback
			})

		/**
		 * Detect dev command from package.json
		 */
		const detectDevCommand = (
			worktreePath: string,
		): Effect.Effect<string, DevServerError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const devServerConfig = yield* appConfig.getDevServerConfig()

				// If config override exists, use it
				if (devServerConfig.command) {
					return devServerConfig.command
				}

				const packageJsonPath = pathService.join(worktreePath, "package.json")
				const exists = yield* fs
					.exists(packageJsonPath)
					.pipe(Effect.catchAll(() => Effect.succeed(false)))

				if (!exists) {
					return yield* Effect.fail(
						new DevServerError({
							message: "No package.json found in worktree",
						}),
					)
				}

				// Read and parse package.json
				const content = yield* fs.readFileString(packageJsonPath).pipe(
					Effect.mapError(
						() =>
							new DevServerError({
								message: "Failed to read package.json",
							}),
					),
				)

				const pkg = yield* Effect.try({
					try: () => JSON.parse(content),
					catch: () =>
						new DevServerError({
							message: "Invalid JSON in package.json",
						}),
				})

				const scripts = pkg.scripts ?? {}
				const pm = yield* detectPackageManager(worktreePath)

				// Check for dev, start, serve scripts in order
				if (scripts.dev) {
					return `${pm} run dev`
				}
				if (scripts.start) {
					return `${pm} run start`
				}
				if (scripts.serve) {
					return `${pm} run serve`
				}

				// Fallback
				return `${pm} run dev`
			})

		/**
		 * Poll tmux pane for port detection using proper Effect scheduling
		 *
		 * Uses Schedule.spaced with upTo and untilInput for:
		 * - Periodic polling every PORT_POLL_INTERVAL
		 * - Timeout after PORT_DETECTION_TIMEOUT
		 * - Early termination when port is detected
		 */
		const pollForPort = (
			beadId: string,
			tmuxSession: string,
		): Effect.Effect<number | undefined, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const devServerConfig = yield* appConfig.getDevServerConfig()
				const pattern = new RegExp(devServerConfig.portPattern)
				const portRef = yield* Ref.make<number | undefined>(undefined)

				// Single poll attempt - captures port in ref and returns true if found
				const tryDetectPort = Effect.gen(function* () {
					const output = yield* tmux.capturePane(tmuxSession, 100)
					const match = output.match(pattern)

					if (match) {
						const port = parseInt(match[1] || match[2], 10)
						if (!Number.isNaN(port)) {
							yield* Ref.set(portRef, port)
							return true
						}
					}
					return false
				}).pipe(Effect.catchAll(() => Effect.succeed(false)))

				// Schedule: poll every 500ms, up to 30 seconds, stop when port found
				const schedule = Schedule.spaced(`${PORT_POLL_INTERVAL} millis`).pipe(
					Schedule.upTo(`${PORT_DETECTION_TIMEOUT} millis`),
					Schedule.untilInput((found: boolean) => found),
				)

				// Run the repeated poll (ignoring the schedule output)
				yield* Effect.repeat(tryDetectPort, schedule).pipe(Effect.ignore)

				const port = yield* Ref.get(portRef)
				if (port !== undefined) {
					yield* Effect.log(`Detected port ${port} for ${beadId}`)
				} else {
					yield* Effect.log(`Port detection timed out for ${beadId}`)
				}
				return port
			})

		/**
		 * Update server state and persist to disk
		 */
		const updateServerState = (
			beadId: string,
			update: Partial<DevServerState>,
		): Effect.Effect<DevServerState> =>
			Effect.gen(function* () {
				const newState = yield* SubscriptionRef.modify(serversRef, (servers) => {
					const current = HashMap.get(servers, beadId)
					const currentState = Option.getOrElse(current, () => idleState)
					const newState: DevServerState = { ...currentState, ...update }
					const newServers = HashMap.set(servers, beadId, newState)
					return [newState, newServers]
				})

				// Persist after state update
				const servers = yield* SubscriptionRef.get(serversRef)
				yield* persistServers(servers)

				return newState
			})

		/**
		 * Get server state for a bead
		 */
		const getServerState = (beadId: string): Effect.Effect<DevServerState> =>
			Effect.gen(function* () {
				const servers = yield* SubscriptionRef.get(serversRef)
				const state = HashMap.get(servers, beadId)
				return Option.getOrElse(state, () => idleState)
			})

		/**
		 * Check if worktree exists for a bead
		 */
		const checkWorktreeExists = (
			beadId: string,
			projectPath: string,
		): Effect.Effect<string, NoWorktreeError> =>
			Effect.gen(function* () {
				// Worktree path pattern: ../ProjectName-beadId/
				const projectName = pathService.basename(projectPath)
				const parentDir = pathService.dirname(projectPath)
				const worktreePath = pathService.join(parentDir, `${projectName}-${beadId}`)

				const exists = yield* fs
					.exists(worktreePath)
					.pipe(Effect.catchAll(() => Effect.succeed(false)))

				if (!exists) {
					return yield* Effect.fail(
						new NoWorktreeError({
							beadId,
							message: `No worktree found. Start a session first (Space+s)`,
						}),
					)
				}

				return worktreePath
			})

		// ========================================================================
		// Health Check Fiber - detects dead dev server sessions
		// ========================================================================

		/**
		 * Check all running dev servers and update state if session died
		 */
		const healthCheckAllServers = Effect.gen(function* () {
			const servers = yield* SubscriptionRef.get(serversRef)

			for (const [beadId, state] of HashMap.entries(servers)) {
				if (state.status === "running" && state.tmuxSession) {
					const hasSession = yield* tmux.hasSession(state.tmuxSession)
					if (!hasSession) {
						yield* Effect.log(`Dev server session ${state.tmuxSession} died, marking as stopped`)

						// Release the port
						if (state.port) {
							yield* releasePort(state.port)
						}

						// Update state to indicate server stopped unexpectedly
						yield* updateServerState(beadId, {
							...idleState,
							error: "Server stopped unexpectedly",
						})
					}
				}
			}
		})

		// Start the health check polling fiber
		const healthCheckFiber = yield* healthCheckAllServers.pipe(
			Effect.repeat(Schedule.spaced(`${HEALTH_CHECK_INTERVAL} millis`)),
			Effect.catchAll(() => Effect.void), // Don't crash on errors
			Effect.forkIn(serviceScope),
		)

		// Register with diagnostics for visibility
		yield* diagnostics.registerFiberIn(serviceScope, {
			id: "dev-server-health-check",
			name: "Dev Server Health",
			description: "Monitors dev server sessions and detects when they die",
			fiber: healthCheckFiber,
		})

		// Auto-cleanup on scope exit
		yield* Effect.addFinalizer(() =>
			Effect.gen(function* () {
				const servers = yield* SubscriptionRef.get(serversRef)
				for (const [beadId, state] of HashMap.entries(servers)) {
					if (state.tmuxSession) {
						yield* tmux.killSession(state.tmuxSession).pipe(Effect.catchAll(() => Effect.void))
						yield* Effect.log(`Cleaned up dev server for ${beadId}`)
					}
				}
			}),
		)

		return {
			// Expose the SubscriptionRef for atoms to subscribe to
			servers: serversRef,

			/**
			 * Get the state of a specific dev server
			 */
			getStatus: (beadId: string) => getServerState(beadId),

			/**
			 * Start a dev server for a bead's worktree
			 */
			start: (
				beadId: string,
				projectPath: string,
			): Effect.Effect<
				DevServerState,
				DevServerError | NoWorktreeError | TmuxError | WorktreeSessionError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					// Check current state
					const current = yield* getServerState(beadId)
					if (current.status === "running" || current.status === "starting") {
						return current
					}

					// Verify worktree exists
					const worktreePath = yield* checkWorktreeExists(beadId, projectPath)

					// Update to starting
					yield* updateServerState(beadId, {
						status: "starting",
						worktreePath,
						error: undefined,
					})

					// Detect command and build env
					const command = yield* detectDevCommand(worktreePath)

					// Calculate port offset based on number of running servers
					const servers = yield* SubscriptionRef.get(serversRef)
					const runningCount = HashMap.size(HashMap.filter(servers, (s) => s.status === "running"))
					const { env, primaryPort } = yield* buildPortEnv(runningCount)

					// Build the full command with env vars
					const envString = Object.entries(env)
						.map(([k, v]) => `${k}=${v}`)
						.join(" ")
					const rawCommand = `${envString} ${command}`

					// Get initCommands and devServer config from current project
					const worktreeConfig = yield* appConfig.getWorktreeConfig()
					const devServerConfig = yield* appConfig.getDevServerConfig()
					const initCommands = worktreeConfig.initCommands

					// Create tmux session - initCommands are chained with dev command in same shell
					const tmuxSession = getDevSessionName(beadId)
					const cwd =
						devServerConfig.cwd === "."
							? worktreePath
							: pathService.join(worktreePath, devServerConfig.cwd)

					yield* worktreeSession.create({
						sessionName: tmuxSession,
						worktreePath,
						command: rawCommand,
						cwd,
						initCommands,
					})

					// Update state
					yield* updateServerState(beadId, {
						status: "running",
						tmuxSession,
						port: primaryPort,
						startedAt: new Date(),
					})

					// Start port polling in background to get actual port
					// Fork into service scope so fiber is cleaned up on service shutdown
					const pollerFiber = yield* pollForPort(beadId, tmuxSession).pipe(
						Effect.tap((detectedPort) =>
							detectedPort !== undefined
								? updateServerState(beadId, { port: detectedPort })
								: Effect.void,
						),
						Effect.catchAll(() => Effect.void),
						Effect.forkIn(serviceScope),
					)

					// Register fiber with diagnostics for visibility
					yield* diagnostics.registerFiberIn(serviceScope, {
						id: `dev-server-port-poller-${beadId}`,
						name: `Port Poller (${beadId})`,
						description: `Detecting port for ${beadId} dev server`,
						fiber: pollerFiber,
					})

					return yield* getServerState(beadId)
				}),

			/**
			 * Stop a dev server
			 */
			stop: (beadId: string): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const current = yield* getServerState(beadId)

					if (current.tmuxSession) {
						yield* tmux.killSession(current.tmuxSession).pipe(Effect.catchAll(() => Effect.void))
					}

					// Release the port
					if (current.port) {
						yield* releasePort(current.port)
					}

					yield* updateServerState(beadId, idleState)
				}),

			/**
			 * Toggle a dev server (start if stopped, stop if running)
			 */
			toggle: (
				beadId: string,
				projectPath: string,
			): Effect.Effect<
				DevServerState,
				DevServerError | NoWorktreeError | TmuxError | WorktreeSessionError,
				CommandExecutor.CommandExecutor
			> =>
				Effect.gen(function* () {
					const current = yield* getServerState(beadId)

					if (current.status === "running" || current.status === "starting") {
						yield* Effect.log(`Stopping dev server for ${beadId}`)

						if (current.tmuxSession) {
							yield* tmux.killSession(current.tmuxSession).pipe(Effect.catchAll(() => Effect.void))
						}
						if (current.port) {
							yield* releasePort(current.port)
						}

						return yield* updateServerState(beadId, idleState)
					}

					yield* Effect.log(`Starting dev server for ${beadId}`)

					// Verify worktree exists
					const worktreePath = yield* checkWorktreeExists(beadId, projectPath)

					// Update to starting
					yield* updateServerState(beadId, {
						status: "starting",
						worktreePath,
						error: undefined,
					})

					// Detect command
					const command = yield* detectDevCommand(worktreePath)

					// Calculate port offset
					const servers = yield* SubscriptionRef.get(serversRef)
					const runningCount = HashMap.size(HashMap.filter(servers, (s) => s.status === "running"))
					const { env, primaryPort } = yield* buildPortEnv(runningCount)

					// Build command with env
					const envString = Object.entries(env)
						.map(([k, v]) => `${k}=${v}`)
						.join(" ")
					const rawCommand = `${envString} ${command}`

					// Get initCommands and devServer config from current project
					const worktreeConfig = yield* appConfig.getWorktreeConfig()
					const devServerConfig = yield* appConfig.getDevServerConfig()
					const initCommands = worktreeConfig.initCommands

					// Create tmux session - initCommands are chained with dev command in same shell
					const tmuxSession = getDevSessionName(beadId)
					const cwd =
						devServerConfig.cwd === "."
							? worktreePath
							: pathService.join(worktreePath, devServerConfig.cwd)

					yield* worktreeSession.create({
						sessionName: tmuxSession,
						worktreePath,
						command: rawCommand,
						cwd,
						initCommands,
					})

					// Update state
					yield* updateServerState(beadId, {
						status: "running",
						tmuxSession,
						port: primaryPort,
						startedAt: new Date(),
					})

					// Poll for port in background
					// Fork into service scope so fiber is cleaned up on service shutdown
					const pollerFiber = yield* pollForPort(beadId, tmuxSession).pipe(
						Effect.tap((detectedPort) =>
							detectedPort !== undefined
								? updateServerState(beadId, { port: detectedPort })
								: Effect.void,
						),
						Effect.catchAll(() => Effect.void),
						Effect.forkIn(serviceScope),
					)

					// Register fiber with diagnostics for visibility
					yield* diagnostics.registerFiberIn(serviceScope, {
						id: `dev-server-port-poller-${beadId}`,
						name: `Port Poller (${beadId})`,
						description: `Detecting port for ${beadId} dev server`,
						fiber: pollerFiber,
					})

					return yield* getServerState(beadId)
				}),

			/**
			 * Check if tmux session is still alive and sync state
			 */
			syncState: (
				beadId: string,
			): Effect.Effect<DevServerState, never, CommandExecutor.CommandExecutor> =>
				Effect.gen(function* () {
					const current = yield* getServerState(beadId)

					if (current.tmuxSession) {
						const hasSession = yield* tmux.hasSession(current.tmuxSession)
						if (!hasSession && current.status === "running") {
							// Session died, update state
							if (current.port) {
								yield* releasePort(current.port)
							}
							return yield* updateServerState(beadId, {
								...idleState,
								error: "Server stopped unexpectedly",
							})
						}
					}

					return current
				}),
		}
	}),
}) {}
