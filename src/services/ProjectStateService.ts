/**
 * ProjectStateService - Per-project UI state persistence
 *
 * Saves and restores UI state (cursor position, filters, sort config, view mode)
 * per project, stored in <project-path>/.azedarach/state.json
 *
 * State is saved when switching away from a project and restored when switching to it.
 */

import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Schema } from "effect"
import {
	DEFAULT_FILTER_CONFIG,
	DEFAULT_SORT_CONFIG,
	type FilterConfig,
	type SortConfig,
} from "./EditorService.js"
import type { ViewMode } from "./ViewService.js"

// ============================================================================
// Schema Definitions
// ============================================================================

/**
 * Literal schemas for filter values
 */
const IssueStatusLiteral = Schema.Literal("open", "in_progress", "blocked", "closed")
const IssueTypeLiteral = Schema.Literal("bug", "feature", "task", "epic", "chore")
const SessionStateLiteral = Schema.Literal(
	"idle",
	"initializing",
	"busy",
	"waiting",
	"done",
	"error",
	"paused",
)
const SortFieldLiteral = Schema.Literal("session", "priority", "updated")
const SortDirectionLiteral = Schema.Literal("asc", "desc")
const ViewModeLiteral = Schema.Literal("kanban", "compact")

/**
 * Schema for SortConfig serialization
 */
const SortConfigSchema = Schema.Struct({
	field: SortFieldLiteral,
	direction: SortDirectionLiteral,
})

/**
 * Schema for FilterConfig serialization (uses arrays for JSON compatibility)
 * Transform handles Set <-> Array conversion
 */
const FilterConfigSchema = Schema.transform(
	// From (encoded/JSON form with arrays)
	Schema.Struct({
		status: Schema.Array(IssueStatusLiteral),
		priority: Schema.Array(Schema.Number),
		type: Schema.Array(IssueTypeLiteral),
		session: Schema.Array(SessionStateLiteral),
		hideEpicSubtasks: Schema.Boolean,
	}),
	// To (runtime form with Sets matching FilterConfig interface)
	Schema.Struct({
		status: Schema.ReadonlySetFromSelf(IssueStatusLiteral),
		priority: Schema.ReadonlySetFromSelf(Schema.Number),
		type: Schema.ReadonlySetFromSelf(IssueTypeLiteral),
		session: Schema.ReadonlySetFromSelf(SessionStateLiteral),
		hideEpicSubtasks: Schema.Boolean,
	}),
	{
		strict: true,
		decode: (encoded) =>
			Data.struct({
				status: new Set(encoded.status),
				priority: new Set(encoded.priority),
				type: new Set(encoded.type),
				session: new Set(encoded.session),
				hideEpicSubtasks: encoded.hideEpicSubtasks,
			}),
		encode: (decoded) => ({
			status: [...decoded.status],
			priority: [...decoded.priority],
			type: [...decoded.type],
			session: [...decoded.session],
			hideEpicSubtasks: decoded.hideEpicSubtasks,
		}),
	},
)

/**
 * Schema for the full UI state
 */
const ProjectUIStateSchema = Schema.Struct({
	focusedTaskId: Schema.NullOr(Schema.String),
	filterConfig: FilterConfigSchema,
	sortConfig: SortConfigSchema,
	viewMode: ViewModeLiteral,
	savedAt: Schema.String,
})

/**
 * Full schema with JSON parsing wrapper
 */
const ProjectUIStateJsonSchema = Schema.parseJson(ProjectUIStateSchema)

// ============================================================================
// Types
// ============================================================================

/**
 * Full UI state for a project (derived from schema)
 */
export type ProjectUIState = Schema.Schema.Type<typeof ProjectUIStateSchema>

// ============================================================================
// Helpers
// ============================================================================

/**
 * Create default UI state
 */
const createDefaultUIState = (): ProjectUIState => ({
	focusedTaskId: null,
	filterConfig: DEFAULT_FILTER_CONFIG,
	sortConfig: DEFAULT_SORT_CONFIG,
	viewMode: "kanban",
	savedAt: new Date().toISOString(),
})

/**
 * Default UI state when no saved state exists
 */
export const DEFAULT_UI_STATE: ProjectUIState = createDefaultUIState()

// ============================================================================
// Error Types
// ============================================================================

export class ProjectStateError extends Data.TaggedError("ProjectStateError")<{
	readonly message: string
}> {}

// ============================================================================
// Pure Functions for State Building
// ============================================================================

/**
 * Build ProjectUIState from components (type-safe construction)
 */
export const buildProjectUIState = (
	focusedTaskId: string | null,
	filterConfig: FilterConfig,
	sortConfig: SortConfig,
	viewMode: ViewMode,
): ProjectUIState => ({
	focusedTaskId,
	filterConfig,
	sortConfig,
	viewMode,
	savedAt: new Date().toISOString(),
})

/**
 * Extract filter config from loaded state
 */
export const extractFilterConfig = (state: ProjectUIState): FilterConfig => state.filterConfig

/**
 * Extract sort config from loaded state
 */
export const extractSortConfig = (state: ProjectUIState): SortConfig => state.sortConfig

/**
 * Extract view mode from loaded state
 */
export const extractViewMode = (state: ProjectUIState): ViewMode => state.viewMode

/**
 * Extract focused task ID from loaded state
 */
export const extractFocusedTaskId = (state: ProjectUIState): string | null => state.focusedTaskId

// ============================================================================
// Service Implementation
// ============================================================================

export class ProjectStateService extends Effect.Service<ProjectStateService>()(
	"ProjectStateService",
	{
		effect: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path

			/**
			 * Get the state file path for a project
			 */
			const getStateFilePath = (projectPath: string): string =>
				pathService.join(projectPath, ".azedarach", "state.json")

			return {
				/**
				 * Save UI state for a project
				 *
				 * Creates .azedarach directory if it doesn't exist.
				 * Silently succeeds even if write fails (state is non-critical).
				 */
				saveState: (projectPath: string, state: ProjectUIState): Effect.Effect<void> =>
					Effect.gen(function* () {
						const stateDir = pathService.join(projectPath, ".azedarach")
						const stateFile = getStateFilePath(projectPath)

						// Ensure .azedarach directory exists
						yield* fs
							.makeDirectory(stateDir, { recursive: true })
							.pipe(Effect.catchAll(() => Effect.void))

						// Encode to JSON string using Schema (handles Set -> Array conversion)
						const jsonString = yield* Schema.encode(ProjectUIStateJsonSchema)(state).pipe(
							Effect.catchAll(() => Effect.succeed("")),
						)

						if (!jsonString) {
							return
						}

						// Write to file
						yield* fs
							.writeFileString(stateFile, jsonString)
							.pipe(Effect.catchAll(() => Effect.void))
					}).pipe(
						Effect.catchAll((error) =>
							Effect.logDebug("ProjectStateService: Failed to save state", { error, projectPath }),
						),
					),

				/**
				 * Load UI state for a project
				 *
				 * Returns default state if file doesn't exist or is invalid.
				 */
				loadState: (projectPath: string): Effect.Effect<ProjectUIState> =>
					Effect.gen(function* () {
						const stateFile = getStateFilePath(projectPath)

						// Check if file exists
						const exists = yield* fs
							.exists(stateFile)
							.pipe(Effect.catchAll(() => Effect.succeed(false)))

						if (!exists) {
							return createDefaultUIState()
						}

						// Read file content
						const content = yield* fs
							.readFileString(stateFile)
							.pipe(Effect.catchAll(() => Effect.succeed("")))

						if (!content) {
							return createDefaultUIState()
						}

						// Decode from JSON string using Schema (handles Array -> Set conversion)
						const decoded = yield* Schema.decode(ProjectUIStateJsonSchema)(content).pipe(
							Effect.catchAll(() => Effect.succeed(null)),
						)

						if (!decoded) {
							return createDefaultUIState()
						}

						return decoded
					}).pipe(
						Effect.catchAll((error) => {
							Effect.logDebug("ProjectStateService: Failed to load state, using defaults", {
								error,
								projectPath,
							})
							return Effect.succeed(createDefaultUIState())
						}),
					),

				/**
				 * Get the state file path for debugging/display
				 */
				getStateFilePath,
			}
		}),
	},
) {}
