import { Data, Schema } from "effect"
import {
	DEFAULT_FILTER_CONFIG,
	DEFAULT_SORT_CONFIG,
	type FilterConfig,
	type SortConfig,
} from "./EditorService.js"
import type { ViewMode } from "./ViewService.js"

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

const SortConfigSchema = Schema.Struct({
	field: SortFieldLiteral,
	direction: SortDirectionLiteral,
})

const FilterConfigSchema = Schema.transform(
	Schema.Struct({
		status: Schema.Array(IssueStatusLiteral),
		priority: Schema.Array(Schema.Number),
		type: Schema.Array(IssueTypeLiteral),
		session: Schema.Array(SessionStateLiteral),
		hideEpicSubtasks: Schema.optional(Schema.Boolean),
		updatedDaysAgo: Schema.NullOr(Schema.Number),
	}),
	Schema.Struct({
		status: Schema.ReadonlySetFromSelf(IssueStatusLiteral),
		priority: Schema.ReadonlySetFromSelf(Schema.Number),
		type: Schema.ReadonlySetFromSelf(IssueTypeLiteral),
		session: Schema.ReadonlySetFromSelf(SessionStateLiteral),
		updatedDaysAgo: Schema.NullOr(Schema.Number),
	}),
	{
		strict: true,
		decode: (encoded) =>
			Data.struct({
				status: new Set(encoded.status),
				priority: new Set(encoded.priority),
				type: new Set(encoded.type),
				session: new Set(encoded.session),
				updatedDaysAgo: encoded.updatedDaysAgo,
			}),
		encode: (decoded) => ({
			status: [...decoded.status],
			priority: [...decoded.priority],
			type: [...decoded.type],
			session: [...decoded.session],
			updatedDaysAgo: decoded.updatedDaysAgo,
		}),
	},
)

export const ProjectUIStateSchema = Schema.Struct({
	focusedTaskId: Schema.NullOr(Schema.String),
	filterConfig: FilterConfigSchema,
	sortConfig: SortConfigSchema,
	viewMode: ViewModeLiteral,
	searchQuery: Schema.optional(Schema.String),
	drillDownEpicId: Schema.optional(Schema.NullOr(Schema.String)),
	savedFocusedTaskId: Schema.optional(Schema.NullOr(Schema.String)),
	savedAt: Schema.String,
})

export const ProjectUIStateJsonSchema = Schema.parseJson(ProjectUIStateSchema)

export type ProjectUIState = Schema.Schema.Type<typeof ProjectUIStateSchema>

const createDefaultUIState = (): ProjectUIState => ({
	focusedTaskId: null,
	filterConfig: DEFAULT_FILTER_CONFIG,
	sortConfig: DEFAULT_SORT_CONFIG,
	viewMode: "kanban",
	searchQuery: "",
	drillDownEpicId: null,
	savedFocusedTaskId: null,
	savedAt: new Date().toISOString(),
})

export const DEFAULT_UI_STATE: ProjectUIState = createDefaultUIState()

export const buildProjectUIState = (params: {
	readonly focusedTaskId: string | null
	readonly filterConfig: FilterConfig
	readonly sortConfig: SortConfig
	readonly viewMode: ViewMode
	readonly searchQuery?: string
	readonly drillDownEpicId?: string | null
	readonly savedFocusedTaskId?: string | null
}): ProjectUIState => ({
	focusedTaskId: params.focusedTaskId,
	filterConfig: params.filterConfig,
	sortConfig: params.sortConfig,
	viewMode: params.viewMode,
	searchQuery: params.searchQuery ?? "",
	drillDownEpicId: params.drillDownEpicId ?? null,
	savedFocusedTaskId: params.savedFocusedTaskId ?? null,
	savedAt: new Date().toISOString(),
})

export const extractFilterConfig = (state: ProjectUIState): FilterConfig => state.filterConfig

export const extractSortConfig = (state: ProjectUIState): SortConfig => state.sortConfig

export const extractViewMode = (state: ProjectUIState): ViewMode => state.viewMode

export const extractFocusedTaskId = (state: ProjectUIState): string | null => state.focusedTaskId

export const extractSearchQuery = (state: ProjectUIState): string => state.searchQuery ?? ""

export const extractDrillDownEpicId = (state: ProjectUIState): string | null =>
	state.drillDownEpicId ?? null

export const extractSavedFocusedTaskId = (state: ProjectUIState): string | null =>
	state.savedFocusedTaskId ?? null
