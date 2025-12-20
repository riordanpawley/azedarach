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
 * Saves the current project's UI state before switching, then restores
 * the new project's saved state after loading.
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
		const projectState = yield* ProjectStateService
		const board = yield* BoardService
		const editor = yield* EditorService
		const navigation = yield* NavigationService
		const view = yield* ViewService
		const toast = yield* ToastService

		// Get current project to save its state
		const currentProject = yield* SubscriptionRef.get(projectService.currentProject)

		// Save current project's UI state before switching
		if (currentProject) {
			const focusedTaskId = yield* SubscriptionRef.get(navigation.focusedTaskId)
			const filterConfig = yield* SubscriptionRef.get(editor.filterConfig)
			const sortConfig = yield* SubscriptionRef.get(editor.sortConfig)
			const viewMode = yield* SubscriptionRef.get(view.viewMode)

			const state = buildProjectUIState(focusedTaskId, filterConfig, sortConfig, viewMode)
			yield* projectState.saveState(currentProject.path, state)
		}

		// Switch project (fast - just updates SubscriptionRef)
		yield* projectService.switchProject(projectName)

		// Show toast immediately (user sees feedback right away)
		yield* toast.show("success", `Switching to: ${projectName}`)

		// Get the new project's path and load its saved state
		const newProject = yield* SubscriptionRef.get(projectService.currentProject)

		// Fork the refresh + state restoration as daemon so it survives parent completion
		// (Effect.fork would be interrupted when the atom effect returns)
		// The loading indicator in StatusBar shows progress
		yield* Effect.gen(function* () {
			// Refresh board first to get the task list
			yield* board.refresh()

			// Load and restore saved UI state for the new project
			if (newProject) {
				const savedState = yield* projectState.loadState(newProject.path)

				// Restore editor state (filters and sort)
				yield* editor.restoreState(extractSortConfig(savedState), extractFilterConfig(savedState))

				// Restore view mode
				yield* view.setViewMode(extractViewMode(savedState))

				// Restore cursor position (navigation will validate if task still exists)
				const savedFocusId = extractFocusedTaskId(savedState)
				if (savedFocusId) {
					yield* navigation.setFocusedTask(savedFocusId)
				}
				// NavigationService.ensureValidFocus() will handle invalid task IDs automatically
			}

			yield* toast.show("success", `Loaded: ${projectName}`)
		}).pipe(
			Effect.catchAll((error) => toast.show("error", `Failed to load: ${error}`)),
			Effect.forkDaemon,
		)
	}).pipe(Effect.catchAll(Effect.logError)),
)
