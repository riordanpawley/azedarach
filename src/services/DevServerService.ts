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
import {
	DEV_SESSION_PREFIX,
	getDevSessionName,
	getProjectName,
	parseSessionName,
} from "../core/paths.js"
import { type SessionNotFoundError, type TmuxError, TmuxService } from "../core/TmuxService.js"
import {
	type WorktreeSessionError,
	WorktreeSessionService,
} from "../core/WorktreeSessionService.js"
import { DiagnosticsService } from "./DiagnosticsService.js"
import { ProjectService } from "./ProjectService.js"

// ============================================================================
// Constants
// ============================================================================

/** Legacy tmux session name prefix for dev servers (for migration support) */
const LEGACY_DEV_SESSION_PREFIX = "az-dev-"

/** How often to poll for port detection (ms) */
const PORT_POLL_INTERVAL = 500

/** Maximum time to wait for port detection (ms) */
const PORT_DETECTION_TIMEOUT = 30000

/** How often to check if dev server sessions are still alive (ms) */
const HEALTH_CHECK_INTERVAL = 5000

// ============================================================================
// tmux User-Option Keys (source of truth for dev server state)
// ============================================================================

/** tmux option key for dev server port */
const TMUX_OPT_PORT = "@az-devserver-port"

/** tmux option key for dev server status */
const TMUX_OPT_STATUS = "@az-devserver-status"

/** tmux option key for bead ID this dev server belongs to */
const TMUX_OPT_BEAD_ID = "@az-devserver-bead-id"

/** tmux option key for worktree path */
const TMUX_OPT_WORKTREE_PATH = "@az-devserver-worktree-path"

/** tmux option key for start timestamp (ISO 8601) */
const TMUX_OPT_STARTED_AT = "@az-devserver-started-at"

/** tmux option key for error message */
const TMUX_OPT_ERROR = "@az-devserver-error"

/** tmux option key for project path (enables multi-project support) */
const TMUX_OPT_PROJECT_PATH = "@az-devserver-project-path"

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
// Helper Functions
// ============================================================================

// Note: getDevSessionName is imported from paths.ts

/**
 * Schema for parsing DevServerStatus from string
 * Uses Schema.Literal for compile-time and runtime type safety
 */
const DevServerStatusSchema = Schema.Literal("idle", "starting", "running", "error")

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

		/**
		 * Get the current project path from ProjectService, falling back to process.cwd()
		 */
		const getEffectiveProjectPath = (): Effect.Effect<string> =>
			Effect.gen(function* () {
				const projectPath = yield* projectService.getCurrentPath()
				return projectPath ?? process.cwd()
			})

		// ====================================================================
		// tmux-based State - store/recover dev server state from tmux options
		// ====================================================================

		/**
		 * Store dev server metadata in tmux user-options.
		 * Makes tmux the source of truth for dev server state.
		 * Stores project path for multi-project filtering on recovery.
		 */
		const storeTmuxMetadata = (
			sessionName: string,
			beadId: string,
			port: number,
			worktreePath: string,
			projectPath: string,
			status: DevServerStatus,
		): Effect.Effect<void, SessionNotFoundError, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				yield* tmux.setUserOption(sessionName, TMUX_OPT_BEAD_ID, beadId)
				yield* tmux.setUserOption(sessionName, TMUX_OPT_PORT, String(port))
				yield* tmux.setUserOption(sessionName, TMUX_OPT_WORKTREE_PATH, worktreePath)
				yield* tmux.setUserOption(sessionName, TMUX_OPT_PROJECT_PATH, projectPath)
				yield* tmux.setUserOption(sessionName, TMUX_OPT_STATUS, status)
				yield* tmux.setUserOption(sessionName, TMUX_OPT_STARTED_AT, new Date().toISOString())
			})

		/**
		 * Update a single tmux option (e.g., status or port after detection)
		 */
		const updateTmuxOption = (
			sessionName: string,
			key: string,
			value: string,
		): Effect.Effect<void, never, CommandExecutor.CommandExecutor> =>
			tmux.setUserOption(sessionName, key, value).pipe(Effect.catchAll(() => Effect.void))

		/**
		 * Read dev server state from a tmux session's user-options.
		 * Returns None if session doesn't exist or options not found.
		 */
		const readTmuxMetadata = (
			sessionName: string,
		): Effect.Effect<Option.Option<DevServerState>, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				// Check if session exists first
				const hasSession = yield* tmux.hasSession(sessionName)
				if (!hasSession) return Option.none()

				// Read all options
				const beadIdOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_BEAD_ID)
				const portOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_PORT)
				const worktreePathOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_WORKTREE_PATH)
				const statusOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_STATUS)
				const startedAtOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_STARTED_AT)
				const errorOpt = yield* tmux.getUserOption(sessionName, TMUX_OPT_ERROR)

				// Validate we have minimum required fields
				if (Option.isNone(beadIdOpt) || Option.isNone(statusOpt)) {
					return Option.none()
				}

				// Parse status using Schema (parse don't validate)
				const statusResult = yield* Schema.decodeUnknown(DevServerStatusSchema)(
					statusOpt.value,
				).pipe(Effect.option)

				if (Option.isNone(statusResult)) {
					return Option.none()
				}
				const status = statusResult.value

				// Parse port (may be undefined)
				const port = Option.match(portOpt, {
					onNone: () => undefined,
					onSome: (p) => {
						const parsed = parseInt(p, 10)
						return Number.isNaN(parsed) ? undefined : parsed
					},
				})

				// Parse startedAt (may be undefined)
				const startedAt = Option.match(startedAtOpt, {
					onNone: () => undefined,
					onSome: (s) => {
						const date = new Date(s)
						return Number.isNaN(date.getTime()) ? undefined : date
					},
				})

				return Option.some({
					status,
					port,
					tmuxSession: sessionName,
					worktreePath: Option.getOrUndefined(worktreePathOpt),
					startedAt,
					error: Option.getOrUndefined(errorOpt),
				})
			})

		/**
		 * Discover all running dev server sessions from tmux for the current project.
		 * Filters by project path to support multiple projects running Azedarach.
		 * This enables recovery on startup - tmux holds the source of truth.
		 *
		 * Supports both formats:
		 * - New: dev-{projectName}-{beadId}
		 * - Legacy: az-dev-{beadId}
		 */
		const discoverDevServersFromTmux = (
			currentProjectPath: string,
		): Effect.Effect<DevServersState, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				// Get all tmux sessions
				const allSessions = yield* tmux.listSessions()
				const currentProjectName = getProjectName(currentProjectPath)

				// Filter to dev server sessions (dev-* or legacy az-dev-* prefix)
				const devSessions = allSessions.filter(
					(s) =>
						s.name.startsWith(DEV_SESSION_PREFIX) || s.name.startsWith(LEGACY_DEV_SESSION_PREFIX),
				)

				let result = HashMap.empty<string, DevServerState>()

				for (const session of devSessions) {
					let beadId: string
					let sessionProjectName: string

					// Try parsing as new format first
					const parsed = parseSessionName(session.name)
					if (parsed && parsed.type === "dev") {
						// New format: dev-{project}-{beadId}
						beadId = parsed.beadId
						sessionProjectName = parsed.projectName
					} else if (session.name.startsWith(LEGACY_DEV_SESSION_PREFIX)) {
						// Legacy format: az-dev-{beadId}
						beadId = session.name.slice(LEGACY_DEV_SESSION_PREFIX.length)
						// For legacy, check project path from tmux option or assume current project
						const projectPathOpt = yield* tmux.getUserOption(session.name, TMUX_OPT_PROJECT_PATH)
						if (Option.isSome(projectPathOpt)) {
							sessionProjectName = getProjectName(projectPathOpt.value)
						} else {
							sessionProjectName = currentProjectName // Assume current for legacy without metadata
						}
					} else {
						continue // Unknown format
					}

					// Only recover sessions for the current project
					if (sessionProjectName !== currentProjectName) continue

					const metadataOpt = yield* readTmuxMetadata(session.name)
					if (Option.isSome(metadataOpt)) {
						result = HashMap.set(result, beadId, metadataOpt.value)
					}
				}

				return result
			})

		// Get current project path for filtering
		const currentProjectPath = yield* getEffectiveProjectPath()

		// Recover state from tmux on startup (filtered to current project)
		const synced = yield* discoverDevServersFromTmux(currentProjectPath)

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
		 * Update server state and sync to tmux
		 */
		const updateServerState = (
			beadId: string,
			update: Partial<DevServerState>,
		): Effect.Effect<DevServerState, never, CommandExecutor.CommandExecutor> =>
			Effect.gen(function* () {
				const newState = yield* SubscriptionRef.modify(serversRef, (servers) => {
					const current = HashMap.get(servers, beadId)
					const currentState = Option.getOrElse(current, () => idleState)
					const newState: DevServerState = { ...currentState, ...update }
					const newServers = HashMap.set(servers, beadId, newState)
					return [newState, newServers]
				})

				// Sync changed fields to tmux (tmux is the source of truth)
				if (newState.tmuxSession) {
					if (update.status !== undefined) {
						yield* updateTmuxOption(newState.tmuxSession, TMUX_OPT_STATUS, update.status)
					}
					if (update.port !== undefined) {
						yield* updateTmuxOption(newState.tmuxSession, TMUX_OPT_PORT, String(update.port))
					}
					if (update.error !== undefined) {
						yield* updateTmuxOption(newState.tmuxSession, TMUX_OPT_ERROR, update.error)
					}
				}

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
		// Health Check Fiber - detects dead sessions AND re-discovers missed ones
		// ========================================================================

		/**
		 * Check all running dev servers, detect dead ones, and re-discover any missed sessions.
		 * This ensures we don't lose track of dev servers if startup discovery failed.
		 */
		const healthCheckAllServers = Effect.gen(function* () {
			const servers = yield* SubscriptionRef.get(serversRef)

			// Part 1: Check if existing sessions are still alive
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

			// Part 2: Re-discover sessions from tmux that we might have missed
			// This handles cases where startup discovery failed (e.g., timing issues)
			const projectPath = yield* getEffectiveProjectPath()
			const discoveredServers = yield* discoverDevServersFromTmux(projectPath)

			// Merge discovered servers into our state (only add, don't overwrite)
			for (const [beadId, discoveredState] of HashMap.entries(discoveredServers)) {
				const currentState = HashMap.get(servers, beadId)
				if (Option.isNone(currentState) || currentState.value.status === "idle") {
					// This is a new or idle server we didn't know about - add it
					yield* Effect.log(`Re-discovered dev server for ${beadId} from tmux`)
					yield* SubscriptionRef.update(serversRef, (s) => HashMap.set(s, beadId, discoveredState))

					// Track the port as allocated
					if (discoveredState.port) {
						yield* Ref.update(allocatedPortsRef, (allocated) => {
							const newAllocated = new Set(allocated)
							newAllocated.add(discoveredState.port!)
							return newAllocated
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
			description:
				"Monitors dev server sessions, detects dead ones, and re-discovers missed sessions",
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
				DevServerError | NoWorktreeError | TmuxError | WorktreeSessionError | SessionNotFoundError,
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
					const tmuxSession = getDevSessionName(projectPath, beadId)
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

					// Store metadata in tmux user-options (source of truth for recovery)
					yield* storeTmuxMetadata(
						tmuxSession,
						beadId,
						primaryPort,
						worktreePath,
						projectPath,
						"running",
					)

					// Update in-memory state (for reactive UI)
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
				DevServerError | NoWorktreeError | TmuxError | WorktreeSessionError | SessionNotFoundError,
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
					const tmuxSession = getDevSessionName(projectPath, beadId)
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

					// Store metadata in tmux user-options (source of truth for recovery)
					yield* storeTmuxMetadata(
						tmuxSession,
						beadId,
						primaryPort,
						worktreePath,
						projectPath,
						"running",
					)

					// Update in-memory state (for reactive UI)
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
