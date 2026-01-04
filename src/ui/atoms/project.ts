/**
 * Project Service Atoms
 *
 * Handles project selection and management.
 */

import { Effect, SubscriptionRef } from "effect"
import { BoardService } from "../../services/BoardService.js"
import { EditorService } from "../../services/EditorService.js"
import { NavigationService } from "../../services/NavigationService.js"
import { ProjectService } from "../../services/ProjectService.js"
import {
	buildProjectUIState,
	extractFilterConfig,
	extractFocusedTaskId,
	extractSortConfig,
	extractViewMode,
	ProjectStateService,
} from "../../services/ProjectStateService.js"
import { ToastService } from "../../services/ToastService.js"
import { ViewService } from "../../services/ViewService.js"
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
 * Each service handles its own state save/restore via switchProject methods.
 * This simplified flow:
 * 1. Finds the target project
 * 2. Saves current project state to disk (for persistence across restarts)
 * 3. Calls switchProject on each service (clears project-specific state)
 * 4. Switches ProjectService to new project
 * 5. Loads new project board with state restoration callback
 *
 * Usage: const switchProject = useAtomSet(switchProjectAtom, { mode: "promise" })
 *        await switchProject("project-name")
 */
export const switchProjectAtom = appRuntime.fn((projectName: string) =>
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		const projectState = yield* ProjectStateService
		const board = yield* BoardService
		const editor = yield* EditorService
		const navigation = yield* NavigationService
		const view = yield* ViewService
		const toast = yield* ToastService

		// Find the target project
		const projects = yield* SubscriptionRef.get(projectService.projects)
		const project = projects.find((p) => p.name === projectName)
		if (!project) {
			yield* toast.show("error", `Project not found: ${projectName}`)
			return
		}

		// Save current project state to disk (for persistence across app restarts)
		const currentProject = yield* SubscriptionRef.get(projectService.currentProject)
		if (currentProject) {
			const navState = yield* navigation.getStateForSave()
			const editorState = yield* editor.getStateForSave()
			const viewMode = yield* SubscriptionRef.get(view.viewMode)

			const state = buildProjectUIState(
				navState.focusedTaskId,
				editorState.filterConfig,
				editorState.sortConfig,
				viewMode,
			)
			yield* projectState.saveState(currentProject.path, state)
			yield* board.saveToCache(currentProject.path)
		}

		// Load saved state for new project (from disk)
		const savedState = yield* projectState.loadState(project.path)

		// Switch each service to the new project
		// EditorService uses internal per-project state map, so we just pass the path
		// Then we apply the saved state from disk (if any) to initialize the project's state
		yield* editor.switchProject(project.path)

		// Apply saved state from disk (overrides any defaults for first-time projects)
		yield* editor.restoreState(extractSortConfig(savedState), extractFilterConfig(savedState))

		// Switch navigation to new project (uses internal per-project state map)
		yield* navigation.switchProject(project.path)

		// If we have a saved focus position from disk (cross-session persistence),
		// apply it to override the in-memory state (which may be empty for new projects)
		const savedFocusId = extractFocusedTaskId(savedState)
		if (savedFocusId) {
			yield* navigation.setFocusedTask(savedFocusId)
		}

		// Switch ProjectService to track new current project
		yield* projectService.switchProject(projectName)

		// Restore view mode
		yield* view.setViewMode(extractViewMode(savedState))

		// Switch board with a callback to show success toast after refresh
		const onRefreshComplete = toast.show("success", `Loaded: ${projectName}`)
		const { cacheHit } = yield* board.switchToProject(project.path, onRefreshComplete)

		if (cacheHit) {
			yield* toast.show("success", `Loaded: ${projectName}`)
		} else {
			yield* toast.show("info", `Loading: ${projectName}...`)
		}
	}).pipe(Effect.catchAll(Effect.logError)),
)
