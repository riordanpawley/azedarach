/**
 * Project Service Atoms
 *
 * Handles project selection and management.
 */

import { Effect } from "effect"
import { BoardService } from "../../services/BoardService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { ToastService } from "../../services/ToastService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Project State Atoms
// ============================================================================

/**
 * Current project atom - subscribes to ProjectService currentProject changes
 *
 * Usage: const currentProject = useAtomValue(currentProjectAtom)
 */
export const currentProjectAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		return projectService.currentProject
	}),
)

/**
 * Projects list atom - subscribes to ProjectService projects changes
 *
 * Usage: const projects = useAtomValue(projectsAtom)
 */
export const projectsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		return projectService.projects
	}),
)

// ============================================================================
// Project Action Atoms
// ============================================================================

/**
 * Switch project atom - change the active project
 *
 * The refresh is forked (non-blocking) so the UI stays responsive.
 * A "Loading..." indicator shows in the status bar during refresh.
 *
 * Usage: const switchProject = useAtomSet(switchProjectAtom, { mode: "promise" })
 *        await switchProject("project-name")
 */
export const switchProjectAtom = appRuntime.fn((projectName: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const board = yield* BoardService
		const toast = yield* ToastService

		// Switch project (fast - just updates SubscriptionRef)
		yield* projectService.switchProject(projectName)

		// Show toast immediately (user sees feedback right away)
		yield* toast.show("success", `Switching to: ${projectName}`)

		// Fork as daemon so it survives parent completion
		// (Effect.fork would be interrupted when the atom effect returns)
		// The loading indicator in StatusBar shows progress
		yield* board.refresh().pipe(
			Effect.tap(() => toast.show("success", `Loaded: ${projectName}`)),
			Effect.catchAll((error) => toast.show("error", `Failed to load: ${error}`)),
			Effect.forkDaemon,
		)
	}).pipe(Effect.catchAll(Effect.logError)),
)
