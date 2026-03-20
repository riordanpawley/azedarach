/**
 * Project Service Atoms
 *
 * Handles project selection and management.
 */

import { Effect, SubscriptionRef } from "effect"
import { ProjectStateService } from "../services/ProjectStateService.js"
import { TuiBoardStoreService } from "../services/TuiBoardStoreService.js"
import { TuiProjectContextService } from "../services/TuiProjectContextService.js"
import { ToastService } from "../utils/runtimeServices.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// Project State Atoms
// ============================================================================

/**
 * Current project atom - subscribes to the TUI project context currentProject changes
 *
 * Usage: const currentProject = useAtomValue(currentProjectAtom)
 */
export const currentProjectAtom = appRuntime.subscriptionRef(() =>
	Effect.gen(function* () {
		const projectContext = yield* TuiProjectContextService
		return projectContext.currentProject
	}),
)

/**
 * Projects list atom - subscribes to the TUI project context projects changes
 *
 * Usage: const projects = useAtomValue(projectsAtom)
 */
export const projectsAtom = appRuntime.subscriptionRef(() =>
	Effect.gen(function* () {
		const projectContext = yield* TuiProjectContextService
		return projectContext.projects
	}),
)

// ============================================================================
// Project Action Atoms
// ============================================================================

/**
 * Switch project atom - change the active project
 *
 * Each service handles its own state save/restore via switchProject methods.
 * This simplified flow:
 * 1. Finds the target project
 * 2. Saves current project state to disk (for persistence across restarts)
 * 3. Calls switchProject on each service (clears project-specific state)
 * 4. Switches the TUI project context to the new project
 * 5. Loads new project board with state restoration callback
 *
 * Usage: const switchProject = useAtomSet(switchProjectAtom, { mode: "promise" })
 *        await switchProject("project-name")
 */
export const switchProjectAtom = appRuntime.fn((projectName: string) =>
	Effect.gen(function* () {
		const projectContext = yield* TuiProjectContextService
		const projectState = yield* ProjectStateService
		const board = yield* TuiBoardStoreService
		const toast = yield* ToastService

		// Find the target project
		const projects = yield* SubscriptionRef.get(projectContext.projects)
		const project = projects.find((p) => p.name === projectName)
		if (!project) {
			yield* toast.show("error", `Project not found: ${projectName}`)
			return
		}

		// Save current project state to disk (for persistence across app restarts)
		const currentProject = yield* SubscriptionRef.get(projectContext.currentProject)
		if (currentProject) {
			yield* projectState.saveCurrentProjectState(currentProject.path)
			yield* board.saveToCache(currentProject.path)
		}

		yield* projectContext.switchProject(project.name)

		// Switch board with a callback to show success toast after refresh
		const onRefreshComplete = toast.show("success", `Loaded: ${projectName}`)
		const { cacheHit } = yield* board.switchToProject(project.path, onRefreshComplete)
		yield* projectState.withPersistenceSuspended(projectState.restoreProjectState(project.path))
		yield* projectState.saveCurrentProjectState(project.path)

		if (cacheHit) {
			yield* toast.show("success", `Loaded: ${projectName}`)
		} else {
			yield* toast.show("info", `Loading: ${projectName}...`)
		}
	}).pipe(Effect.catchAll(Effect.logError)),
)
