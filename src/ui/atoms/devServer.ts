/**
 * Dev Server Atoms
 *
 * Bridges DevServerService state to React components via effect-atom.
 */

import { Atom, Result } from "@effect-atom/atom"
import { Effect, HashMap, Option } from "effect"
import { DevServerService, type DevServerState } from "../../services/DevServerService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// State Atoms
// ============================================================================

/**
 * Subscribe to all dev servers state
 */
export const devServersAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const devServer = yield* DevServerService
		return devServer.servers
	}),
)

// ============================================================================
// Derived Atoms
// ============================================================================

/**
 * Get all dev servers state for a specific bead
 */
export const beadDevServersAtom = (beadId: string) =>
	Atom.readable((get) => {
		const serversResult = get(devServersAtom)
		if (!Result.isSuccess(serversResult)) {
			return HashMap.empty<string, DevServerState>()
		}

		const servers = serversResult.value
		return HashMap.get(servers, beadId).pipe(
			Option.getOrElse(() => HashMap.empty<string, DevServerState>()),
		)
	})

/**
 * Get dev server state for a specific bead and server name
 */
export const devServerStateAtom = (beadId: string, serverName: string = "default") =>
	Atom.readable((get) => {
		const beadServers = get(beadDevServersAtom(beadId))
		return HashMap.get(beadServers, serverName).pipe(
			Option.getOrElse(() => ({
				name: serverName,
				status: "idle" as const,
				port: undefined,
				tmuxSession: undefined,
				worktreePath: undefined,
				startedAt: undefined,
				error: undefined,
			})),
		)
	})

// ============================================================================
// Action Atoms
// ============================================================================

/**
 * Toggle dev server for a bead
 *
 * Starts the server if stopped, stops if running.
 * Requires project path to locate the worktree.
 */
export const toggleDevServerAtom = appRuntime.fn(
	({ beadId, serverName = "default" }: { beadId: string; serverName?: string }) =>
		Effect.gen(function* () {
			const devServer = yield* DevServerService
			const projectService = yield* ProjectService
			const project = yield* projectService.requireCurrentProject()

			return yield* devServer.toggle(beadId, project.path, serverName)
		}),
)

/**
 * Stop a dev server
 */
export const stopDevServerAtom = appRuntime.fn(
	({ beadId, serverName = "default" }: { beadId: string; serverName?: string }) =>
		Effect.gen(function* () {
			const devServer = yield* DevServerService
			yield* devServer.stop(beadId, serverName)
		}),
)

/**
 * Sync dev server state (check if tmux session is still alive)
 */
export const syncDevServerStateAtom = appRuntime.fn(
	({ beadId, serverName = "default" }: { beadId: string; serverName?: string }) =>
		Effect.gen(function* () {
			const devServer = yield* DevServerService
			return yield* devServer.syncState(beadId, serverName)
		}),
)
