import { getProjectStoragePaths } from "@azedarach/config"
import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Ref, Schema, Stream } from "effect"
import { EditorService } from "./EditorService.js"
import { NavigationService } from "./NavigationService.js"
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
import { TuiProjectContextService } from "./TuiProjectContextService.js"
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

export class ProjectStateService extends Effect.Service<ProjectStateService>()(
	"ProjectStateService",
	{
		scoped: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path
			const editor = yield* EditorService
			const navigation = yield* NavigationService
			const projectContext = yield* TuiProjectContextService
			const view = yield* ViewService
			const persistenceSuspended = yield* Ref.make(false)

			const getStateFilePath = (projectPath: string): string =>
				getProjectStoragePaths(projectPath, pathService).legacyProjectUiStatePath

			const buildCurrentUiState = (): Effect.Effect<ProjectUIState | undefined> =>
				Effect.gen(function* () {
					const currentProject = yield* projectContext.getCurrentPath()
					if (currentProject === undefined) {
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
				Effect.gen(function* () {
					const stateFilePath = getStateFilePath(projectPath)
					yield* fs
						.makeDirectory(pathService.dirname(stateFilePath), { recursive: true })
						.pipe(Effect.orElseSucceed(() => void 0))
					const encoded = yield* Schema.encode(ProjectUIStateJsonSchema)(state).pipe(
						Effect.mapError(
							(error) =>
								new ProjectStateError({
									message: `Failed to encode project UI state: ${String(error)}`,
								}),
						),
					)
					yield* fs.writeFileString(stateFilePath, encoded).pipe(
						Effect.mapError(
							(error) =>
								new ProjectStateError({
									message: `Failed to write project UI state: ${String(error)}`,
								}),
						),
					)
				}).pipe(
					Effect.catchAll((error) =>
						Effect.logDebug("ProjectStateService: Failed to save state", { error, projectPath }),
					),
				)

			const loadState = (projectPath: string): Effect.Effect<ProjectUIState> =>
				Effect.gen(function* () {
					const stateFilePath = getStateFilePath(projectPath)
					const exists = yield* fs.exists(stateFilePath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						return DEFAULT_UI_STATE
					}

					const content = yield* fs
						.readFileString(stateFilePath)
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
					return decoded ?? DEFAULT_UI_STATE
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

					const currentProjectPath = projectPath ?? (yield* projectContext.getCurrentPath())
					if (currentProjectPath === undefined) {
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

			const initialProjectPath = yield* projectContext.getCurrentPath()
			if (initialProjectPath !== undefined) {
				yield* restoreProjectState(initialProjectPath)
			}

			return {
				saveState,
				loadState,
				saveCurrentProjectState,
				restoreProjectState,
				withPersistenceSuspended,
				getStateFilePath,
			}
		}),
	},
) {}
