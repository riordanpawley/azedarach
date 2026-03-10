/**
 * ProjectStateService - Per-project UI state persistence
 *
 * Saves and restores UI state (cursor position, filters, sort config, view mode,
 * search state, drill-down context) per project using the project's SQLite store.
 *
 * State is saved when switching away from a project and restored when switching to it.
 */

import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Ref, Schema, Stream, SubscriptionRef } from "effect"
import { LocalIssueStore } from "../core/LocalIssueStore.js"
import { getProjectStoragePaths } from "../core/storagePaths.js"
import { EditorService } from "./EditorService.js"
import { NavigationService } from "./NavigationService.js"
import { ProjectService } from "./ProjectService.js"
import {
	buildProjectUIState,
	DEFAULT_UI_STATE,
	extractDrillDownEpicId,
	extractFilterConfig,
	extractFocusedTaskId,
	extractSavedFocusedTaskId,
	extractSearchQuery,
	extractSortConfig,
	extractViewMode,
	type ProjectUIState,
	ProjectUIStateJsonSchema,
} from "./projectUiState.js"
import { ViewService } from "./ViewService.js"

export {
	buildProjectUIState,
	DEFAULT_UI_STATE,
	extractDrillDownEpicId,
	extractFilterConfig,
	extractFocusedTaskId,
	extractSavedFocusedTaskId,
	extractSearchQuery,
	extractSortConfig,
	extractViewMode,
}

export class ProjectStateError extends Data.TaggedError("ProjectStateError")<{
	readonly message: string
}> {}

// ============================================================================
// Service Implementation
// ============================================================================

export class ProjectStateService extends Effect.Service<ProjectStateService>()(
	"ProjectStateService",
	{
		dependencies: [
			LocalIssueStore.Default,
			EditorService.Default,
			NavigationService.Default,
			ProjectService.Default,
			ViewService.Default,
		],
		scoped: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const localIssueStore = yield* LocalIssueStore
			const editor = yield* EditorService
			const navigation = yield* NavigationService
			const projectService = yield* ProjectService
			const view = yield* ViewService
			const persistenceSuspended = yield* Ref.make(false)

			/**
			 * Get the canonical persistence path for project UI state.
			 */
			const getStateFilePath = (projectPath: string): string =>
				getProjectStoragePaths(projectPath, pathService).canonicalDbPath

			const buildCurrentUiState = (): Effect.Effect<ProjectUIState | undefined> =>
				Effect.gen(function* () {
					const currentProject = yield* SubscriptionRef.get(projectService.currentProject)
					if (!currentProject) {
						return undefined
					}

					const navState = yield* navigation.getStateForSave()
					const editorState = yield* editor.getStateForSave()
					const viewMode = yield* view.getViewMode()

					return buildProjectUIState({
						focusedTaskId: navState.focusedTaskId,
						filterConfig: editorState.filterConfig,
						sortConfig: editorState.sortConfig,
						viewMode,
						searchQuery: editorState.searchQuery,
						drillDownEpicId: navState.drillDownEpicId,
						savedFocusedTaskId: navState.savedFocusedTaskId,
					})
				})

			const saveState = (projectPath: string, state: ProjectUIState): Effect.Effect<void> =>
				localIssueStore
					.saveProjectUiState(state, projectPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logDebug("ProjectStateService: Failed to save state", { error, projectPath }),
						),
					)

			const loadState = (projectPath: string): Effect.Effect<ProjectUIState> =>
				Effect.gen(function* () {
					const persisted = yield* localIssueStore
						.loadProjectUiState(projectPath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(undefined)),
								),
							),
						)
					if (persisted !== undefined) {
						return persisted
					}

					const legacyStatePath = getProjectStoragePaths(
						projectPath,
						pathService,
					).legacyProjectUiStatePath
					const legacyExists = yield* fs
						.exists(legacyStatePath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed(false)),
								),
							),
						)
					if (!legacyExists) {
						return DEFAULT_UI_STATE
					}

					const content = yield* fs
						.readFileString(legacyStatePath)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
									Effect.zipRight(Effect.succeed("")),
								),
							),
						)
					if (!content) {
						return DEFAULT_UI_STATE
					}

					const decoded = yield* Schema.decode(ProjectUIStateJsonSchema)(content).pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(undefined)),
							),
						),
					)
					if (decoded === undefined) {
						return DEFAULT_UI_STATE
					}

					yield* saveState(projectPath, decoded)
					return decoded
				}).pipe(
					Effect.catchAll((error) => {
						Effect.logDebug("ProjectStateService: Failed to load state, using defaults", {
							error,
							projectPath,
						})
						return Effect.succeed(DEFAULT_UI_STATE)
					}),
				)

			const applyState = (projectPath: string, state: ProjectUIState) =>
				Effect.gen(function* () {
					yield* editor.switchProject(projectPath)
					yield* editor.restoreState(
						extractSortConfig(state),
						extractFilterConfig(state),
						extractSearchQuery(state),
					)
					yield* navigation.switchProject(projectPath)
					yield* navigation.restorePersistedState({
						focusedTaskId: extractFocusedTaskId(state),
						drillDownEpicId: extractDrillDownEpicId(state),
						savedFocusedTaskId: extractSavedFocusedTaskId(state),
					})
					yield* view.setViewMode(extractViewMode(state))
				})

			const withPersistenceSuspended = <A, E, R>(
				effect: Effect.Effect<A, E, R>,
			): Effect.Effect<A, E, R> =>
				Effect.gen(function* () {
					const alreadySuspended = yield* Ref.get(persistenceSuspended)
					if (alreadySuspended) {
						return yield* effect
					}
					yield* Ref.set(persistenceSuspended, true)
					return yield* effect.pipe(Effect.ensuring(Ref.set(persistenceSuspended, false)))
				})

			const saveCurrentProjectState = (projectPath?: string): Effect.Effect<void> =>
				Effect.gen(function* () {
					if (yield* Ref.get(persistenceSuspended)) {
						return
					}

					const currentProjectPath =
						projectPath ?? (yield* SubscriptionRef.get(projectService.currentProject))?.path
					if (!currentProjectPath) {
						return
					}

					const state = yield* buildCurrentUiState()
					if (state === undefined) {
						return
					}

					yield* saveState(currentProjectPath, state)
				})

			const restoreProjectState = (projectPath: string) =>
				Effect.gen(function* () {
					const state = yield* loadState(projectPath)
					yield* withPersistenceSuspended(applyState(projectPath, state))
					return state
				})

			const uiStateChanges = Stream.merge(
				Stream.merge(editor.mode.changes, editor.sortConfig.changes),
				Stream.merge(
					editor.filterConfig.changes,
					Stream.merge(
						navigation.focusedTaskId.changes,
						Stream.merge(navigation.drillDownEpic.changes, view.viewMode.changes),
					),
				),
			)

			yield* Effect.forkScoped(
				Stream.runForEach(uiStateChanges.pipe(Stream.debounce("150 millis")), () =>
					saveCurrentProjectState().pipe(
						Effect.catchAll((error) =>
							Effect.logDebug("ProjectStateService: autosave failed", { error }).pipe(
								Effect.asVoid,
							),
						),
					),
				),
			)

			const initialProject = yield* SubscriptionRef.get(projectService.currentProject)
			if (initialProject) {
				yield* restoreProjectState(initialProject.path)
			}

			return {
				/**
				 * Save UI state for a project
				 *
				 * Silently succeeds even if write fails (state is non-critical).
				 */
				saveState,

				/**
				 * Load UI state for a project
				 *
				 * Returns default state if no persisted state exists or legacy migration fails.
				 */
				loadState,

				saveCurrentProjectState,

				restoreProjectState,

				withPersistenceSuspended,

				/**
				 * Get the canonical sqlite path backing persisted UI state.
				 */
				getStateFilePath,
			}
		}),
	},
) {}
