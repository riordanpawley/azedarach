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
 * Get dev server state for a specific bead
 *
 * Returns idle state if no server exists for the bead.
 */
export const devServerStateAtom = (beadId: string) =>
	Atom.readable((get) => {
		const serversResult = get(devServersAtom)
		if (!Result.isSuccess(serversResult)) {
			return {
				status: "idle",
				port: undefined,
				tmuxSession: undefined,
				worktreePath: undefined,
				startedAt: undefined,
				error: undefined,
			} satisfies DevServerState
		}

		const servers = serversResult.value
		const state = HashMap.get(servers, beadId)
		return Option.getOrElse(state, () => ({
			status: "idle",
			port: undefined,
			tmuxSession: undefined,
			worktreePath: undefined,
			startedAt: undefined,
			error: undefined,
		})) satisfies DevServerState
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
export const toggleDevServerAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const devServer = yield* DevServerService
		const projectService = yield* ProjectService
		const project = yield* projectService.requireCurrentProject()

		return yield* devServer.toggle(beadId, project.path)
	}),
)

/**
 * Stop a dev server
 */
export const stopDevServerAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const devServer = yield* DevServerService
		yield* devServer.stop(beadId)
	}),
)

/**
 * Sync dev server state (check if tmux session is still alive)
 */
export const syncDevServerStateAtom = appRuntime.fn((beadId: string) =>
	Effect.gen(function* () {
		const devServer = yield* DevServerService
		return yield* devServer.syncState(beadId)
	}),
)
