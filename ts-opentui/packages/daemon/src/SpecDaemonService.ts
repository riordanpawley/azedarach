import { Database } from "bun:sqlite"
import { AppConfig, getProjectStoragePaths } from "@azedarach/config"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { LinearClient } from "@linear/sdk"
import { Data, DateTime, Effect, Fiber, Ref, Schema } from "effect"

type SpecRequirementKind = "functional" | "acceptance" | "other"
type SpecLinkType = "implements" | "tests" | "blocks" | "relates"
type SpecLinkFulfillmentStatus = "planned" | "partial" | "complete" | "verified"
type SpecRequirementLookupSelector = "auto" | "id" | "local_id" | "external_code"

interface IssueRow {
	readonly id: string
	readonly title: string
	readonly status: string
	readonly issue_type: string
	readonly updated_at: string
	readonly deleted_at: string | null
}

interface SpecRequirementRow {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly body_md: string
	readonly kind: string
	readonly status: string
	readonly priority: number
	readonly created_at: string
	readonly updated_at: string
	readonly deleted_at: string | null
}

interface SpecIssueLinkRow {
	readonly issue_id: string
	readonly requirement_id: string
	readonly requirement_local_id: string
	readonly requirement_external_code: string | null
	readonly link_type: string
	readonly implementations_json: string | null
	readonly fulfillment_status: string | null
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
	readonly created_at: string
	readonly updated_at: string
	readonly deleted_at: string | null
}

interface MetaRow {
	readonly value: string
}

export interface SpecRequirement {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly body: string
	readonly kind: SpecRequirementKind
	readonly status: string
	readonly priority: number
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueLink {
	readonly issue_id: string
	readonly requirement_id: string
	readonly requirement_local_id: string
	readonly requirement_external_code: string | null
	readonly link_type: SpecLinkType
	readonly implementations: ReadonlyArray<string>
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
	readonly created_at: string
	readonly updated_at: string
}

export interface SpecIssueRef {
	readonly id: string
	readonly title?: string
	readonly status?: string
	readonly issue_type?: string
	readonly link_type: SpecLinkType
	readonly implementations: ReadonlyArray<string>
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
}

export interface SpecRequirementRef {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly kind: SpecRequirementKind
	readonly link_type: SpecLinkType
	readonly implementations: ReadonlyArray<string>
	readonly fulfillment_status: SpecLinkFulfillmentStatus
	readonly fulfillment_percent: number | null
	readonly evidence_note: string | null
}

export interface SpecRequirementWithStats extends SpecRequirement {
	readonly linked_issue_count: number
	readonly implemented_issue_count: number
}

export interface SpecRequirementListFilters {
	readonly query?: string
	readonly kind?: SpecRequirementKind
	readonly status?: string
	readonly priority?: number
}

export interface SpecCoverageGap {
	readonly kind: "unlinked_requirement" | "missing_issue" | "missing_requirement"
	readonly requirement_id?: string
	readonly issue_id?: string
	readonly message: string
}

export interface SpecCoverageReport {
	readonly requirements: ReadonlyArray<SpecRequirementWithStats>
	readonly unlinked_requirement_ids: ReadonlyArray<string>
	readonly fully_implemented_requirement_ids: ReadonlyArray<string>
	readonly partially_implemented_requirement_ids: ReadonlyArray<string>
	readonly integrity_gaps: ReadonlyArray<SpecCoverageGap>
}

export interface SpecParityRequirement {
	readonly id: string
	readonly local_id: string
	readonly external_code: string | null
	readonly title: string
	readonly implements_issue_ids: ReadonlyArray<string>
	readonly partial_issue_ids: ReadonlyArray<string>
	readonly tests_issue_ids: ReadonlyArray<string>
	readonly other_issue_ids: ReadonlyArray<string>
}

export interface SpecParityReport {
	readonly implementation: string
	readonly total_requirements: number
	readonly implemented_requirement_ids: ReadonlyArray<string>
	readonly partially_implemented_requirement_ids: ReadonlyArray<string>
	readonly tested_requirement_ids: ReadonlyArray<string>
	readonly uncovered_requirement_ids: ReadonlyArray<string>
	readonly related_only_requirement_ids: ReadonlyArray<string>
	readonly requirements: ReadonlyArray<SpecParityRequirement>
}

export interface SpecLintResult {
	readonly ok: boolean
	readonly requirement_count: number
	readonly linked_requirement_count: number
	readonly unlinked_requirement_count: number
	readonly integrity_gap_count: number
	readonly gap_counts: {
		readonly unlinked_requirement: number
		readonly missing_issue: number
		readonly missing_requirement: number
	}
	readonly report: SpecCoverageReport
}

export interface SpecMarkdownSyncDocumentResult {
	readonly key: "overview" | "requirements" | "acceptance" | "change_log"
	readonly path: string
	readonly status: "updated" | "unchanged"
	readonly changed: boolean
}

export interface SpecMarkdownSyncResult {
	readonly out_dir: string
	readonly check: boolean
	readonly ok: boolean
	readonly total_documents: number
	readonly changed_documents: number
	readonly documents: ReadonlyArray<SpecMarkdownSyncDocumentResult>
}

const SpecPublishConfigJsonSchema = Schema.parseJson(
	Schema.Struct({
		enabled: Schema.Boolean,
		debounce_ms: Schema.Number,
		target_project: Schema.NullOr(Schema.String),
		documents: Schema.Struct({
			overview: Schema.String,
			requirements: Schema.String,
			acceptance: Schema.String,
			change_log: Schema.String,
		}),
	}),
)

export type SpecPublishConfig = Schema.Schema.Type<typeof SpecPublishConfigJsonSchema>

const DEFAULT_SPEC_PUBLISH_CONFIG: SpecPublishConfig = {
	enabled: false,
	debounce_ms: 2000,
	target_project: null,
	documents: {
		overview: "Spec Overview",
		requirements: "Requirements Index",
		acceptance: "Acceptance Index",
		change_log: "Change Log",
	},
}

const SpecPublishOutcomeJsonSchema = Schema.parseJson(
	Schema.Struct({
		started_at: Schema.DateTimeUtc,
		finished_at: Schema.DateTimeUtc,
		status: Schema.Literal("success", "partial", "failed"),
		total_requirements: Schema.Number,
		total_links: Schema.Number,
		outcomes: Schema.Array(
			Schema.Struct({
				document_key: Schema.Literal("overview", "requirements", "acceptance", "change_log"),
				title: Schema.String,
				status: Schema.Literal("success", "failed", "skipped"),
				message: Schema.String,
				requirement_count: Schema.Number,
				link_count: Schema.Number,
			}),
		),
	}),
)

export type SpecPublishOutcome = Schema.Schema.Type<typeof SpecPublishOutcomeJsonSchema>

export interface SpecReadSnapshot {
	readonly requirements: ReadonlyArray<SpecRequirement>
	readonly links: ReadonlyArray<SpecIssueLink>
	readonly coverage: SpecCoverageReport
	readonly publishConfig: SpecPublishConfig
	readonly lastPublishOutcome: SpecPublishOutcome | undefined
}

export class SpecDaemonError extends Data.TaggedError("SpecDaemonError")<{
	readonly reason: "storage" | "ambiguous-reference" | "config" | "linear"
	readonly message: string
}> {}

export interface SpecDaemonServiceApi {
	readonly listRequirements: (
		projectPath?: string,
		filters?: SpecRequirementListFilters,
	) => Effect.Effect<ReadonlyArray<SpecRequirement>, SpecDaemonError>
	readonly getRequirement: (
		reference: string,
		projectPath?: string,
		selector?: SpecRequirementLookupSelector,
	) => Effect.Effect<SpecRequirement | undefined, SpecDaemonError>
	readonly createRequirement: (
		input: {
			readonly id?: string
			readonly local_id?: string
			readonly external_code?: string
			readonly title: string
			readonly body: string
			readonly kind?: SpecRequirementKind
			readonly status?: string
			readonly priority?: number
		},
		projectPath?: string,
	) => Effect.Effect<SpecRequirement, SpecDaemonError>
	readonly updateRequirement: (
		reference: string,
		fields: {
			readonly title?: string
			readonly body?: string
			readonly kind?: SpecRequirementKind
			readonly status?: string
			readonly priority?: number
		},
		projectPath?: string,
		selector?: SpecRequirementLookupSelector,
	) => Effect.Effect<boolean, SpecDaemonError>
	readonly deleteRequirement: (
		reference: string,
		projectPath?: string,
		selector?: SpecRequirementLookupSelector,
	) => Effect.Effect<boolean, SpecDaemonError>
	readonly listIssueRequirements: (
		issueId: string,
		projectPath?: string,
	) => Effect.Effect<ReadonlyArray<SpecRequirementRef>, SpecDaemonError>
	readonly listLinks: (
		filters?: {
			readonly issueId?: string
			readonly requirementId?: string
			readonly requirementSelector?: SpecRequirementLookupSelector
			readonly implementation?: string
		},
		projectPath?: string,
	) => Effect.Effect<ReadonlyArray<SpecIssueLink>, SpecDaemonError>
	readonly addIssueLink: (
		issueId: string,
		requirementReference: string,
		linkType: SpecLinkType,
		projectPath?: string,
		requirementSelector?: SpecRequirementLookupSelector,
		implementations?: ReadonlyArray<string>,
		fulfillment?: {
			readonly status?: SpecLinkFulfillmentStatus
			readonly percent?: number | null
			readonly evidenceNote?: string | null
		},
	) => Effect.Effect<void, SpecDaemonError>
	readonly removeIssueLink: (
		issueId: string,
		requirementReference: string,
		linkType?: SpecLinkType,
		projectPath?: string,
		requirementSelector?: SpecRequirementLookupSelector,
		implementations?: ReadonlyArray<string>,
	) => Effect.Effect<number, SpecDaemonError>
	readonly updateIssueLink: (
		issueId: string,
		requirementReference: string,
		fields: {
			readonly status?: SpecLinkFulfillmentStatus
			readonly percent?: number | null
			readonly evidenceNote?: string | null
		},
		linkType?: SpecLinkType,
		projectPath?: string,
		requirementSelector?: SpecRequirementLookupSelector,
	) => Effect.Effect<number, SpecDaemonError>
	readonly listRequirementIssues: (
		reference: string,
		projectPath?: string,
		selector?: SpecRequirementLookupSelector,
	) => Effect.Effect<ReadonlyArray<SpecIssueRef>, SpecDaemonError>
	readonly getCoverageReport: (
		projectPath?: string,
	) => Effect.Effect<SpecCoverageReport, SpecDaemonError>
	readonly getParityReport: (
		implementation: string,
		projectPath?: string,
	) => Effect.Effect<SpecParityReport, SpecDaemonError>
	readonly lint: (projectPath?: string) => Effect.Effect<SpecLintResult, SpecDaemonError>
	readonly getPublishConfig: (
		projectPath?: string,
	) => Effect.Effect<SpecPublishConfig, SpecDaemonError>
	readonly setPublishConfig: (
		config: SpecPublishConfig,
		projectPath?: string,
	) => Effect.Effect<void, SpecDaemonError>
	readonly getLastPublishOutcome: (
		projectPath?: string,
	) => Effect.Effect<SpecPublishOutcome | undefined, SpecDaemonError>
	readonly syncMarkdown: (
		options?: {
			readonly outDir?: string
			readonly check?: boolean
		},
		projectPath?: string,
	) => Effect.Effect<SpecMarkdownSyncResult, SpecDaemonError>
	readonly publish: (projectPath?: string) => Effect.Effect<SpecPublishOutcome, SpecDaemonError>
	readonly readSnapshot: (projectPath?: string) => Effect.Effect<SpecReadSnapshot, SpecDaemonError>
}

const DEFAULT_IMPLEMENTATION = "default"
const SPEC_PUBLISH_CONFIG_META_KEY = "spec:publish:config"
const SPEC_PUBLISH_OUTCOME_META_KEY = "spec:publish:last_outcome"
const SPEC_EXTERNAL_CODE_PATTERN = /^AZ-(FR|AT)-\d{4}[A-Z]?$/i
const SPEC_LOCAL_ID_PATTERN = /^[a-z][a-z0-9-]{0,47}$/
const SPEC_IMPLEMENTATION_PATTERN = /^[a-z][a-z0-9-]{0,63}$/
const SpecImplementationsJsonSchema = Schema.parseJson(Schema.Array(Schema.String))

const mapStorageError = (message: string): SpecDaemonError =>
	new SpecDaemonError({
		reason: "storage",
		message,
	})

const mapConfigError = (message: string): SpecDaemonError =>
	new SpecDaemonError({
		reason: "config",
		message,
	})

const mapLinearError = (message: string): SpecDaemonError =>
	new SpecDaemonError({
		reason: "linear",
		message,
	})

const normalizeIssueStatus = (status: string | undefined): string | undefined => {
	switch (status) {
		case "open":
		case "in_progress":
		case "blocked":
		case "closed":
		case "tombstone":
			return status
		case "deferred":
			return "blocked"
		case "draft":
		case "pinned":
			return "open"
		default:
			return status
	}
}

const normalizeIssueType = (issueType: string | undefined): string | undefined => {
	switch (issueType) {
		case "bug":
		case "feature":
		case "task":
		case "epic":
		case "chore":
			return issueType
		case "docs":
		case "question":
			return "task"
		default:
			return issueType
	}
}

const normalizeSpecRequirementKind = (kind: string | undefined): SpecRequirementKind => {
	switch (kind) {
		case "functional":
		case "acceptance":
		case "other":
			return kind
		default:
			return "other"
	}
}

const normalizeSpecLinkType = (linkType: string | undefined): SpecLinkType => {
	switch (linkType) {
		case "implements":
		case "tests":
		case "blocks":
		case "relates":
			return linkType
		default:
			return "relates"
	}
}

const normalizeSpecLinkFulfillmentStatus = (
	status: string | undefined | null,
): SpecLinkFulfillmentStatus => {
	switch (status) {
		case "planned":
		case "partial":
		case "complete":
		case "verified":
			return status
		default:
			return "planned"
	}
}

const normalizeSpecLinkFulfillmentPercent = (value: number | null | undefined): number | null => {
	if (value === null || value === undefined) {
		return null
	}
	if (!Number.isFinite(value)) {
		return null
	}
	const rounded = Math.round(value)
	return rounded < 0 || rounded > 100 ? null : rounded
}

const normalizeSpecLinkEvidenceNote = (value: string | null | undefined): string | null => {
	if (value === null || value === undefined) {
		return null
	}
	const trimmed = value.trim()
	return trimmed.length === 0 ? null : trimmed
}

const normalizeSpecExternalCode = (value: string): string =>
	value.trim().toUpperCase().replace(/\s+/g, "")

const normalizeSpecLocalId = (value: string): string =>
	value
		.trim()
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "")

const isValidSpecExternalCode = (value: string): boolean => SPEC_EXTERNAL_CODE_PATTERN.test(value)
const isValidSpecLocalId = (value: string): boolean => SPEC_LOCAL_ID_PATTERN.test(value)

const normalizeSpecImplementation = (implementation: string): string => {
	const normalized = implementation.trim().toLowerCase()
	return SPEC_IMPLEMENTATION_PATTERN.test(normalized) ? normalized : DEFAULT_IMPLEMENTATION
}

const nowIso = (): string => new Date().toISOString()

const deriveSpecLocalIdFromExternalCode = (externalCode: string): string => {
	const normalized = normalizeSpecExternalCode(externalCode)
	const match = /^AZ-(FR|AT)-(\d{4}[A-Z]?)$/i.exec(normalized)
	if (match === null) {
		return normalizeSpecLocalId(normalized)
	}
	return `${match[1].toLowerCase()}${match[2].toLowerCase()}`
}

const decodeSpecImplementations = (value: string | null): ReadonlyArray<string> => {
	if (value === null) {
		return [DEFAULT_IMPLEMENTATION]
	}

	try {
		const decoded = Schema.decodeUnknownSync(SpecImplementationsJsonSchema)(value)
		const normalized = decoded
			.map((implementation) => normalizeSpecImplementation(implementation))
			.filter(
				(implementation, index, items) =>
					implementation.length > 0 && items.indexOf(implementation) === index,
			)
			.sort((left, right) => left.localeCompare(right))
		return normalized.length > 0 ? normalized : [DEFAULT_IMPLEMENTATION]
	} catch {
		return [DEFAULT_IMPLEMENTATION]
	}
}

const encodeSpecImplementations = (implementations: ReadonlyArray<string>): string =>
	JSON.stringify(
		[...implementations]
			.map((implementation) => normalizeSpecImplementation(implementation))
			.filter(
				(implementation, index, values) =>
					implementation.length > 0 && values.indexOf(implementation) === index,
			)
			.sort((left, right) => left.localeCompare(right)),
	)

const rowToSpecRequirement = (row: SpecRequirementRow): SpecRequirement => ({
	id: row.id,
	local_id: row.local_id,
	external_code: row.external_code,
	title: row.title,
	body: row.body_md,
	kind: normalizeSpecRequirementKind(row.kind),
	status: row.status,
	priority: row.priority,
	created_at: row.created_at,
	updated_at: row.updated_at,
})

const rowToSpecIssueLink = (row: SpecIssueLinkRow): SpecIssueLink => ({
	issue_id: row.issue_id,
	requirement_id: row.requirement_id,
	requirement_local_id: row.requirement_local_id,
	requirement_external_code: row.requirement_external_code,
	link_type: normalizeSpecLinkType(row.link_type),
	implementations: decodeSpecImplementations(row.implementations_json),
	fulfillment_status: normalizeSpecLinkFulfillmentStatus(row.fulfillment_status),
	fulfillment_percent: normalizeSpecLinkFulfillmentPercent(row.fulfillment_percent),
	evidence_note: normalizeSpecLinkEvidenceNote(row.evidence_note),
	created_at: row.created_at,
	updated_at: row.updated_at,
})

const filterSpecRequirementRows = (
	rows: ReadonlyArray<SpecRequirementRow>,
	filters: SpecRequirementListFilters | undefined,
): ReadonlyArray<SpecRequirementRow> => {
	if (filters === undefined) {
		return rows
	}

	const normalizedQuery =
		filters.query === undefined ? undefined : filters.query.trim().toLowerCase()
	const normalizedStatus =
		filters.status === undefined ? undefined : filters.status.trim().toLowerCase()

	return rows.filter((row) => {
		if (normalizedQuery !== undefined && normalizedQuery.length > 0) {
			const matchesQuery =
				row.local_id.toLowerCase().includes(normalizedQuery) ||
				(row.external_code?.toLowerCase().includes(normalizedQuery) ?? false) ||
				row.title.toLowerCase().includes(normalizedQuery) ||
				row.body_md.toLowerCase().includes(normalizedQuery)
			if (!matchesQuery) {
				return false
			}
		}

		if (filters.kind !== undefined && normalizeSpecRequirementKind(row.kind) !== filters.kind) {
			return false
		}

		if (normalizedStatus !== undefined && normalizedStatus.length > 0) {
			if (row.status.toLowerCase() !== normalizedStatus) {
				return false
			}
		}

		if (filters.priority !== undefined && row.priority !== filters.priority) {
			return false
		}

		return true
	})
}

const decodeSpecPublishConfigMeta = (value: string | undefined): SpecPublishConfig => {
	if (value === undefined) {
		return DEFAULT_SPEC_PUBLISH_CONFIG
	}

	try {
		return Schema.decodeUnknownSync(SpecPublishConfigJsonSchema)(value)
	} catch {
		return DEFAULT_SPEC_PUBLISH_CONFIG
	}
}

const encodeSpecPublishConfigMeta = (value: SpecPublishConfig): string =>
	Schema.encodeSync(SpecPublishConfigJsonSchema)(value)

const decodeSpecPublishOutcomeMeta = (
	value: string | undefined,
): SpecPublishOutcome | undefined => {
	if (value === undefined) {
		return undefined
	}

	try {
		return Schema.decodeUnknownSync(SpecPublishOutcomeJsonSchema)(value)
	} catch {
		return undefined
	}
}

const encodeSpecPublishOutcomeMeta = (value: SpecPublishOutcome): string =>
	Schema.encodeSync(SpecPublishOutcomeJsonSchema)(value)

const formatLinkSummary = (
	links: ReadonlyArray<SpecIssueLink>,
	requirementInternalId: string,
): {
	readonly total: number
	readonly implementsCount: number
	readonly testsCount: number
} => {
	const requirementLinks = links.filter((link) => link.requirement_id === requirementInternalId)
	return {
		total: requirementLinks.length,
		implementsCount: requirementLinks.filter((link) => link.link_type === "implements").length,
		testsCount: requirementLinks.filter((link) => link.link_type === "tests").length,
	}
}

const requirementPublishIdentifier = (requirement: SpecRequirement): string =>
	requirement.external_code ?? requirement.local_id

const upsertManagedSection = (
	existingContent: string | null | undefined,
	key: string,
	renderedContent: string,
): string => {
	const start = `<!-- AZ-SPEC:${key}:START -->`
	const end = `<!-- AZ-SPEC:${key}:END -->`
	const managed = `${start}\n${renderedContent}\n${end}`
	const source = existingContent ?? ""
	const startIndex = source.indexOf(start)
	const endIndex = source.indexOf(end)
	if (startIndex >= 0 && endIndex >= 0 && endIndex > startIndex) {
		const before = source.slice(0, startIndex).trimEnd()
		const after = source.slice(endIndex + end.length).trimStart()
		return `${before.length > 0 ? `${before}\n\n` : ""}${managed}${after.length > 0 ? `\n\n${after}` : ""}`
	}
	return source.trim().length === 0 ? managed : `${source.trimEnd()}\n\n${managed}`
}

const markdownFilenameByKey: Readonly<Record<SpecMarkdownSyncDocumentResult["key"], string>> = {
	overview: "overview.md",
	requirements: "requirements.md",
	acceptance: "acceptance.md",
	change_log: "change-log.md",
}

const renderSyncedMarkdown = (
	key: SpecMarkdownSyncDocumentResult["key"],
	content: string,
): string => {
	const metadata = [
		"<!-- az-spec-sync:source=az-db -->",
		"<!-- az-spec-sync:mode=generated-readonly -->",
		`<!-- az-spec-sync:document-key=${key} -->`,
	].join("\n")
	return `${metadata}\n\n${content.trimEnd()}\n`
}

const ensureSpecSchema = (database: Database): void => {
	database.run("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
	database.run(
		"CREATE TABLE IF NOT EXISTS issues (id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open', issue_type TEXT NOT NULL DEFAULT 'task', updated_at TEXT NOT NULL DEFAULT '', deleted_at TEXT)",
	)
	database.run(
		"CREATE TABLE IF NOT EXISTS spec_requirements (id TEXT PRIMARY KEY, local_id TEXT NOT NULL, external_code TEXT, title TEXT NOT NULL DEFAULT '', body_md TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'other', status TEXT NOT NULL DEFAULT 'open', priority INTEGER NOT NULL DEFAULT 3, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '', deleted_at TEXT)",
	)
	database.run(
		"CREATE TABLE IF NOT EXISTS spec_issue_links (issue_id TEXT NOT NULL, requirement_id TEXT NOT NULL, link_type TEXT NOT NULL, implementations_json TEXT NOT NULL DEFAULT '[\"default\"]', fulfillment_status TEXT, fulfillment_percent INTEGER, evidence_note TEXT, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '', deleted_at TEXT)",
	)
}

export class SpecDaemonService extends Effect.Service<SpecDaemonService>()("SpecDaemonService", {
	dependencies: [BunContext.layer, AppConfig.Default],
	effect: Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path
		const pendingPublishFibers = yield* Ref.make(
			new Map<string, Fiber.RuntimeFiber<void, SpecDaemonError>>(),
		)

		const resolveDbPath = (projectPath?: string) =>
			Effect.gen(function* () {
				const resolvedProjectPath = projectPath ?? process.cwd()
				const storagePaths = getProjectStoragePaths(resolvedProjectPath, pathService)
				yield* fs
					.makeDirectory(storagePaths.storageDirectory, { recursive: true })
					.pipe(
						Effect.mapError(() =>
							mapStorageError(
								`Failed to create storage directory ${storagePaths.storageDirectory}`,
							),
						),
					)
				return storagePaths.canonicalDbPath
			})

		const withDatabase = <A>(
			projectPath: string | undefined,
			use: (database: Database) => Effect.Effect<A, SpecDaemonError>,
		): Effect.Effect<A, SpecDaemonError> =>
			resolveDbPath(projectPath).pipe(
				Effect.flatMap((dbPath) =>
					Effect.acquireUseRelease(
						Effect.try({
							try: () => {
								const database = new Database(dbPath)
								ensureSpecSchema(database)
								return database
							},
							catch: () => mapStorageError(`Failed to open sqlite database ${dbPath}`),
						}),
						use,
						(database) =>
							Effect.sync(() => {
								database.close()
							}).pipe(Effect.ignore),
					),
				),
			)

		const listRequirementRows = (database: Database): ReadonlyArray<SpecRequirementRow> =>
			database
				.query<SpecRequirementRow, []>(
					`SELECT id, local_id, external_code, title, body_md, kind, status, priority, created_at, updated_at, deleted_at
					 FROM spec_requirements
					 WHERE deleted_at IS NULL
					 ORDER BY local_id ASC, updated_at DESC, id ASC`,
				)
				.all()

		const listIssueRows = (database: Database): ReadonlyArray<IssueRow> =>
			database
				.query<IssueRow, []>(
					`SELECT id, title, status, issue_type, updated_at, deleted_at
					 FROM issues
					 WHERE deleted_at IS NULL`,
				)
				.all()

		const listLinkRows = (database: Database): ReadonlyArray<SpecIssueLinkRow> =>
			database
				.query<SpecIssueLinkRow, []>(
					`SELECT
						l.issue_id,
						l.requirement_id,
						COALESCE(r.local_id, l.requirement_id) AS requirement_local_id,
						r.external_code AS requirement_external_code,
						l.link_type,
						l.implementations_json,
						l.fulfillment_status,
						l.fulfillment_percent,
						l.evidence_note,
						l.created_at,
						l.updated_at,
						l.deleted_at
					FROM spec_issue_links l
					LEFT JOIN spec_requirements r
						ON r.id = l.requirement_id
						AND r.deleted_at IS NULL
					WHERE l.deleted_at IS NULL
					ORDER BY l.updated_at DESC, l.issue_id ASC, l.requirement_id ASC`,
				)
				.all()

		const resolveRequirementByReference = (
			database: Database,
			reference: string,
			selector: SpecRequirementLookupSelector,
		): Effect.Effect<SpecRequirementRow | undefined, SpecDaemonError> =>
			Effect.gen(function* () {
				const normalizedReference = reference.trim()
				if (normalizedReference.length === 0) {
					return undefined
				}

				const rows = listRequirementRows(database)
				if (selector !== "auto") {
					const normalizedLocalId = normalizeSpecLocalId(normalizedReference)
					const normalizedExternalCode = normalizeSpecExternalCode(normalizedReference)
					const exact = rows.filter((row) => {
						switch (selector) {
							case "id":
								return row.id === normalizedReference
							case "local_id":
								return isValidSpecLocalId(normalizedLocalId)
									? row.local_id === normalizedLocalId
									: false
							case "external_code":
								return isValidSpecExternalCode(normalizedExternalCode)
									? row.external_code === normalizedExternalCode
									: false
						}
						return false
					})
					return exact[0]
				}

				const localId = normalizeSpecLocalId(normalizedReference)
				const externalCode = normalizeSpecExternalCode(normalizedReference)
				const matches = rows.filter(
					(row) =>
						row.id === normalizedReference ||
						(isValidSpecLocalId(localId) && row.local_id === localId) ||
						(isValidSpecExternalCode(externalCode) && row.external_code === externalCode),
				)
				const uniqueMatches = new Map(matches.map((row) => [row.id, row] as const))
				if (uniqueMatches.size === 0) {
					return undefined
				}
				if (uniqueMatches.size > 1) {
					return yield* Effect.fail(
						new SpecDaemonError({
							reason: "ambiguous-reference",
							message: `Ambiguous spec requirement reference '${reference}'. Use --id, --local-id, or --external-code.`,
						}),
					)
				}
				return [...uniqueMatches.values()][0]
			})

		const buildCoverageReport = (database: Database): SpecCoverageReport => {
			const requirements = listRequirementRows(database)
			const links = listLinkRows(database)
			const issueRows = listIssueRows(database)
			const requirementById = new Map(requirements.map((row) => [row.id, row] as const))
			const issueIdSet = new Set(issueRows.map((row) => row.id))
			const linkCountByRequirement = new Map<string, number>()
			const implementedCountByRequirement = new Map<string, number>()
			const partialCountByRequirement = new Map<string, number>()

			for (const link of links) {
				linkCountByRequirement.set(
					link.requirement_id,
					(linkCountByRequirement.get(link.requirement_id) ?? 0) + 1,
				)
				const linkType = normalizeSpecLinkType(link.link_type)
				const fulfillmentStatus = normalizeSpecLinkFulfillmentStatus(link.fulfillment_status)
				if (
					linkType === "implements" &&
					(fulfillmentStatus === "complete" || fulfillmentStatus === "verified")
				) {
					implementedCountByRequirement.set(
						link.requirement_id,
						(implementedCountByRequirement.get(link.requirement_id) ?? 0) + 1,
					)
				} else if (linkType === "implements" && fulfillmentStatus === "partial") {
					partialCountByRequirement.set(
						link.requirement_id,
						(partialCountByRequirement.get(link.requirement_id) ?? 0) + 1,
					)
				}
			}

			const requirementStats: ReadonlyArray<SpecRequirementWithStats> = requirements.map((row) => ({
				...rowToSpecRequirement(row),
				linked_issue_count: linkCountByRequirement.get(row.id) ?? 0,
				implemented_issue_count: implementedCountByRequirement.get(row.id) ?? 0,
			}))

			const unlinkedRequirementIds = requirementStats
				.filter((item) => item.linked_issue_count === 0)
				.map((item) => item.local_id)
			const fullyImplementedRequirementIds = requirementStats
				.filter((item) => item.implemented_issue_count > 0)
				.map((item) => item.local_id)
			const partiallyImplementedRequirementIds = requirementStats
				.filter(
					(item) =>
						item.implemented_issue_count === 0 && (partialCountByRequirement.get(item.id) ?? 0) > 0,
				)
				.map((item) => item.local_id)

			const integrityGaps: Array<SpecCoverageGap> = []
			for (const link of links) {
				if (!requirementById.has(link.requirement_id)) {
					integrityGaps.push({
						kind: "missing_requirement",
						requirement_id: link.requirement_local_id,
						issue_id: link.issue_id,
						message: `Link references missing requirement ${link.requirement_local_id}`,
					})
				}
				if (!issueIdSet.has(link.issue_id)) {
					const linkedRequirement = requirementById.get(link.requirement_id)
					const requirementIdForMessage = linkedRequirement?.local_id ?? link.requirement_local_id
					integrityGaps.push({
						kind: "missing_issue",
						requirement_id: requirementIdForMessage,
						issue_id: link.issue_id,
						message: `Link references missing issue ${link.issue_id}`,
					})
				}
			}

			for (const requirementId of unlinkedRequirementIds) {
				integrityGaps.push({
					kind: "unlinked_requirement",
					requirement_id: requirementId,
					message: `Requirement ${requirementId} has no linked issues`,
				})
			}

			return {
				requirements: requirementStats,
				unlinked_requirement_ids: unlinkedRequirementIds,
				fully_implemented_requirement_ids: fullyImplementedRequirementIds,
				partially_implemented_requirement_ids: partiallyImplementedRequirementIds,
				integrity_gaps: integrityGaps,
			}
		}

		const readMetaValue = (database: Database, key: string): string | undefined =>
			database.query<MetaRow, [string]>("SELECT value FROM meta WHERE key = ?").get(key)?.value

		const writeMetaValue = (database: Database, key: string, value: string): void => {
			database
				.query<[string, string], [string, string]>(
					"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
				)
				.run(key, value)
		}

		const loadRequirementByInternalId = (
			database: Database,
			id: string,
		): SpecRequirementRow | undefined =>
			database
				.query<SpecRequirementRow, [string]>(
					`SELECT id, local_id, external_code, title, body_md, kind, status, priority, created_at, updated_at, deleted_at
					 FROM spec_requirements
					 WHERE id = ? AND deleted_at IS NULL
					 LIMIT 1`,
				)
				.get(id) ?? undefined

		const resolveSpecLinkImplementationsForWrite = (
			database: Database,
			issueId: string,
			implementations: ReadonlyArray<string> | undefined,
		): Effect.Effect<ReadonlyArray<string>, SpecDaemonError> =>
			Effect.gen(function* () {
				const normalized = (implementations ?? [])
					.map((implementation) => normalizeSpecImplementation(implementation))
					.filter(
						(implementation, index, values) =>
							implementation.length > 0 && values.indexOf(implementation) === index,
					)
				if (normalized.length > 0) {
					return normalized
				}

				const issue = database
					.query<{ readonly implementations: string | null }, [string]>(
						"SELECT implementations FROM issues WHERE id = ? AND deleted_at IS NULL LIMIT 1",
					)
					.get(issueId)
				if (issue?.implementations !== undefined && issue.implementations !== null) {
					try {
						const decoded = JSON.parse(issue.implementations)
						if (Array.isArray(decoded)) {
							const inferred = decoded
								.filter((value): value is string => typeof value === "string")
								.map((value) => normalizeSpecImplementation(value))
								.filter(
									(value, index, values) => value.length > 0 && values.indexOf(value) === index,
								)
							if (inferred.length > 0) {
								return inferred
							}
						}
					} catch {}
				}

				return [DEFAULT_IMPLEMENTATION]
			})

		const renderPublishDocuments = (
			requirements: ReadonlyArray<SpecRequirement>,
			links: ReadonlyArray<SpecIssueLink>,
		): ReadonlyArray<{
			readonly key: SpecMarkdownSyncDocumentResult["key"]
			readonly title: string
			readonly content: string
		}> => {
			const totalRequirements = requirements.length
			const totalLinks = links.length
			const generatedAt = nowIso()
			return [
				{
					key: "overview",
					title: "Spec Overview",
					content: [
						"# Spec Overview",
						"",
						`Generated at: ${generatedAt}`,
						"",
						`- Requirements: ${totalRequirements}`,
						`- Links: ${totalLinks}`,
					].join("\n"),
				},
				{
					key: "requirements",
					title: "Requirements Index",
					content: [
						"# Requirements Index",
						"",
						...requirements
							.filter((requirement) => requirement.kind !== "acceptance")
							.flatMap((requirement) => {
								const summary = formatLinkSummary(links, requirement.id)
								return [
									`## ${requirementPublishIdentifier(requirement)} ${requirement.title}`,
									`- Status: ${requirement.status}`,
									`- Priority: ${requirement.priority}`,
									`- Linked issues: ${summary.total}`,
									`- Implements links: ${summary.implementsCount}`,
									`- Tests links: ${summary.testsCount}`,
									"",
									requirement.body,
									"",
								]
							}),
					].join("\n"),
				},
				{
					key: "acceptance",
					title: "Acceptance Index",
					content: [
						"# Acceptance Index",
						"",
						...requirements
							.filter((requirement) => requirement.kind === "acceptance")
							.flatMap((requirement) => {
								const summary = formatLinkSummary(links, requirement.id)
								return [
									`## ${requirementPublishIdentifier(requirement)} ${requirement.title}`,
									`- Linked issues: ${summary.total}`,
									"",
									requirement.body,
									"",
								]
							}),
					].join("\n"),
				},
				{
					key: "change_log",
					title: "Change Log",
					content: [
						"# Change Log",
						"",
						`- ${generatedAt}: Published ${totalRequirements} requirements and ${totalLinks} links.`,
					].join("\n"),
				},
			]
		}

		const resolvePublishProjectReference = (
			projectPath: string,
			config: SpecPublishConfig,
		): Effect.Effect<string, SpecDaemonError> =>
			Effect.gen(function* () {
				if (config.target_project !== null && config.target_project.trim().length > 0) {
					return config.target_project.trim()
				}
				const issueTrackerConfig = yield* appConfig
					.getIssueTrackerSyncConfigForProjectPath(projectPath)
					.pipe(
						Effect.mapError((error) =>
							mapConfigError(`Failed to load project config for publish: ${error.message}`),
						),
					)
				if ("linear" in issueTrackerConfig.issueTracker) {
					const configuredProject = issueTrackerConfig.issueTracker.linear.project
					if (configuredProject !== undefined && configuredProject.trim().length > 0) {
						return configuredProject.trim()
					}
				}
				return yield* Effect.fail(
					mapConfigError(
						"No publish target project configured. Set target via `az spec publish config set --project ...`.",
					),
				)
			})

		const createLinearClient = (): Effect.Effect<LinearClient, SpecDaemonError> =>
			Effect.try({
				try: () => {
					const apiKey = process.env.LINEAR_API_KEY?.trim()
					if (apiKey === undefined || apiKey.length === 0) {
						throw new Error("LINEAR_API_KEY is required for spec publish")
					}
					return new LinearClient({ apiKey })
				},
				catch: (error) =>
					mapLinearError(error instanceof Error ? error.message : "Failed to create Linear client"),
			})

		const resolveLinearProjectId = (
			client: LinearClient,
			reference: string,
		): Effect.Effect<string, SpecDaemonError> =>
			Effect.gen(function* () {
				const trimmed = reference.trim()
				const normalized = trimmed.toLowerCase()
				const direct = yield* Effect.tryPromise({
					try: () => client.project(trimmed),
					catch: () => new Error(`Unable to resolve Linear project '${reference}' directly`),
				}).pipe(Effect.catchAll(() => Effect.succeed(null)))
				if (direct?.id !== undefined) {
					return direct.id
				}
				let cursor: string | undefined
				for (;;) {
					const page = yield* Effect.tryPromise({
						try: () =>
							client.projects({
								first: 250,
								...(cursor === undefined ? {} : { after: cursor }),
							}),
						catch: () => new Error(`Unable to resolve Linear project '${reference}'`),
					}).pipe(
						Effect.mapError((error) =>
							mapLinearError(
								error instanceof Error ? error.message : "Failed to list Linear projects",
							),
						),
					)
					const match = page.nodes.find((project) => {
						const byId = project.id === trimmed
						const bySlug = project.slugId.trim().toLowerCase() === normalized
						const byName = project.name.trim().toLowerCase() === normalized
						return byId || bySlug || byName
					})
					if (match !== undefined) {
						return match.id
					}
					if (!page.pageInfo.hasNextPage || page.pageInfo.endCursor === null) {
						break
					}
					cursor = page.pageInfo.endCursor
				}
				return yield* Effect.fail(mapLinearError(`Unable to resolve Linear project '${reference}'`))
			})

		const runPublish = (projectPath: string): Effect.Effect<SpecPublishOutcome, SpecDaemonError> =>
			withDatabase(projectPath, (database) =>
				Effect.gen(function* () {
					const requirements = listRequirementRows(database).map((row) => rowToSpecRequirement(row))
					const links = listLinkRows(database).map((row) => rowToSpecIssueLink(row))
					const config = decodeSpecPublishConfigMeta(
						readMetaValue(database, SPEC_PUBLISH_CONFIG_META_KEY),
					)
					const startedAt = yield* DateTime.now
					const client = yield* createLinearClient()
					const projectReference = yield* resolvePublishProjectReference(projectPath, config)
					const projectId = yield* resolveLinearProjectId(client, projectReference)
					const existingDocuments = yield* Effect.tryPromise({
						try: () => client.documents({ first: 250 }),
						catch: (error) =>
							mapLinearError(
								error instanceof Error
									? error.message
									: "Failed to fetch existing Linear documents",
							),
					})
					const renderedDocs = renderPublishDocuments(requirements, links).map((document) => ({
						...document,
						title: config.documents[document.key],
					}))
					const outcomes = yield* Effect.forEach(
						renderedDocs,
						(document) =>
							Effect.gen(function* () {
								const existing = existingDocuments.nodes.find(
									(candidate) =>
										candidate.projectId === projectId &&
										candidate.trashed !== true &&
										candidate.title.trim().toLowerCase() === document.title.trim().toLowerCase(),
								)
								const content = upsertManagedSection(
									existing?.content,
									document.key.toUpperCase(),
									document.content,
								)
								yield* Effect.tryPromise({
									try: () =>
										existing === undefined
											? client.createDocument({
													title: document.title,
													content,
													projectId,
												})
											: client.updateDocument(existing.id, {
													title: document.title,
													content,
												}),
									catch: (error) =>
										mapLinearError(
											error instanceof Error
												? error.message
												: `Failed to publish document '${document.title}'`,
										),
								})
								return {
									document_key: document.key,
									title: document.title,
									status: "success",
									message:
										existing === undefined
											? `Created document '${document.title}'`
											: `Updated document '${document.title}'`,
									requirement_count: requirements.length,
									link_count: links.length,
								} as const
							}).pipe(
								Effect.catchAll((error) =>
									Effect.succeed({
										document_key: document.key,
										title: document.title,
										status: "failed",
										message: error.message,
										requirement_count: requirements.length,
										link_count: links.length,
									} as const),
								),
							),
						{ concurrency: 1 },
					)
					const successCount = outcomes.filter((outcome) => outcome.status === "success").length
					const finishedAt = yield* DateTime.now
					const outcome: SpecPublishOutcome = {
						started_at: startedAt,
						finished_at: finishedAt,
						status:
							successCount === outcomes.length
								? "success"
								: successCount === 0
									? "failed"
									: "partial",
						total_requirements: requirements.length,
						total_links: links.length,
						outcomes,
					}
					writeMetaValue(
						database,
						SPEC_PUBLISH_OUTCOME_META_KEY,
						encodeSpecPublishOutcomeMeta(outcome),
					)
					return outcome
				}),
			)

		const scheduleAutoPublishInternal = (
			reason: string,
			projectPath: string,
		): Effect.Effect<void, SpecDaemonError> =>
			withDatabase(projectPath, (database) =>
				Effect.gen(function* () {
					const config = decodeSpecPublishConfigMeta(
						readMetaValue(database, SPEC_PUBLISH_CONFIG_META_KEY),
					)
					if (!config.enabled) {
						return
					}
					const existingFiber = yield* Ref.get(pendingPublishFibers).pipe(
						Effect.map((fibers) => fibers.get(projectPath)),
					)
					if (existingFiber !== undefined) {
						yield* Fiber.interrupt(existingFiber)
					}
					const nextFiber = yield* Effect.fork(
						Effect.sleep(`${Math.max(0, Math.floor(config.debounce_ms))} millis`).pipe(
							Effect.zipRight(runPublish(projectPath)),
							Effect.asVoid,
							Effect.catchAll((error) =>
								Effect.logWarning(
									`Spec auto-publish failed after reason='${reason}': ${error.message}`,
								),
							),
						),
					)
					yield* Ref.update(pendingPublishFibers, (fibers) => {
						const next = new Map(fibers)
						next.set(projectPath, nextFiber)
						return next
					})
				}),
			)

		return {
			listRequirements: (projectPath?: string, filters?: SpecRequirementListFilters) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() =>
						filterSpecRequirementRows(listRequirementRows(database), filters).map((row) =>
							rowToSpecRequirement(row),
						),
					).pipe(Effect.mapError(() => mapStorageError("Failed to load spec requirements"))),
				),
			getRequirement: (
				reference: string,
				projectPath?: string,
				selector: SpecRequirementLookupSelector = "auto",
			) =>
				withDatabase(projectPath, (database) =>
					resolveRequirementByReference(database, reference, selector).pipe(
						Effect.map((row) => (row === undefined ? undefined : rowToSpecRequirement(row))),
					),
				),
			createRequirement: (input, projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const legacyReference = input.id?.trim()
						let normalizedExternalCode =
							input.external_code === undefined || input.external_code.trim().length === 0
								? undefined
								: normalizeSpecExternalCode(input.external_code)
						if (
							legacyReference !== undefined &&
							legacyReference.length > 0 &&
							normalizedExternalCode === undefined &&
							isValidSpecExternalCode(normalizeSpecExternalCode(legacyReference))
						) {
							normalizedExternalCode = normalizeSpecExternalCode(legacyReference)
						}
						if (
							normalizedExternalCode !== undefined &&
							!isValidSpecExternalCode(normalizedExternalCode)
						) {
							return yield* Effect.fail(
								mapStorageError(
									`Invalid external spec code '${input.external_code}'. Expected AZ-FR-####[a-z]? or AZ-AT-####[a-z]?.`,
								),
							)
						}

						let normalizedLocalId =
							input.local_id === undefined || input.local_id.trim().length === 0
								? undefined
								: normalizeSpecLocalId(input.local_id)
						if (
							legacyReference !== undefined &&
							legacyReference.length > 0 &&
							normalizedLocalId === undefined &&
							(normalizedExternalCode === undefined ||
								!isValidSpecExternalCode(normalizeSpecExternalCode(legacyReference)))
						) {
							normalizedLocalId = normalizeSpecLocalId(legacyReference)
						}
						if (normalizedLocalId === undefined && normalizedExternalCode !== undefined) {
							normalizedLocalId = deriveSpecLocalIdFromExternalCode(normalizedExternalCode)
						}
						if (normalizedLocalId === undefined || normalizedLocalId.length === 0) {
							const autoRows = database
								.query<{ readonly local_id: string }, []>(
									"SELECT local_id FROM spec_requirements WHERE local_id GLOB 'r[0-9]*'",
								)
								.all()
							let maxIndex = 0
							for (const row of autoRows) {
								const match = /^r(\d+)$/.exec(row.local_id)
								if (match === null) {
									continue
								}
								const parsed = Number.parseInt(match[1] ?? "", 10)
								if (Number.isFinite(parsed) && parsed > maxIndex) {
									maxIndex = parsed
								}
							}
							normalizedLocalId = `r${maxIndex + 1}`
						}
						if (!isValidSpecLocalId(normalizedLocalId)) {
							return yield* Effect.fail(
								mapStorageError(
									`Invalid local_id '${normalizedLocalId}'. Expected lowercase token like 'r1', 'fr4201', or 'at2907'.`,
								),
							)
						}
						if (
							database
								.query<{ readonly id: string }, [string]>(
									"SELECT id FROM spec_requirements WHERE local_id = ? AND deleted_at IS NULL LIMIT 1",
								)
								.get(normalizedLocalId) !== undefined
						) {
							return yield* Effect.fail(
								mapStorageError(`Spec requirement local_id already exists: ${normalizedLocalId}`),
							)
						}
						if (
							normalizedExternalCode !== undefined &&
							database
								.query<{ readonly id: string }, [string]>(
									"SELECT id FROM spec_requirements WHERE external_code = ? AND deleted_at IS NULL LIMIT 1",
								)
								.get(normalizedExternalCode) !== undefined
						) {
							return yield* Effect.fail(
								mapStorageError(
									`Spec requirement external_code already exists: ${normalizedExternalCode}`,
								),
							)
						}

						let internalId = `sr_${crypto.randomUUID().replace(/-/g, "")}`
						while (
							database
								.query<{ readonly id: string }, [string]>(
									"SELECT id FROM spec_requirements WHERE id = ? LIMIT 1",
								)
								.get(internalId) !== undefined
						) {
							internalId = `sr_${crypto.randomUUID().replace(/-/g, "")}`
						}

						const now = nowIso()
						database
							.query<
								[],
								[
									string,
									string,
									string | null,
									string,
									string,
									string,
									string,
									number,
									string,
									string,
									null,
								]
							>(
								`INSERT INTO spec_requirements (
									id, local_id, external_code, title, body_md, kind, status, priority, created_at, updated_at, deleted_at
								) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
							)
							.run(
								internalId,
								normalizedLocalId,
								normalizedExternalCode ?? null,
								input.title,
								input.body,
								input.kind ?? "other",
								input.status ?? "active",
								input.priority ?? 2,
								now,
								now,
								null,
							)
						const created = loadRequirementByInternalId(database, internalId)
						if (created === undefined) {
							return yield* Effect.fail(
								mapStorageError(`Failed to load created spec requirement ${normalizedLocalId}`),
							)
						}
						const resolvedProjectPath = projectPath ?? process.cwd()
						yield* scheduleAutoPublishInternal("requirement_created", resolvedProjectPath)
						return rowToSpecRequirement(created)
					}),
				),
			updateRequirement: (
				reference: string,
				fields,
				projectPath?: string,
				selector: SpecRequirementLookupSelector = "auto",
			) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const existing = yield* resolveRequirementByReference(database, reference, selector)
						if (existing === undefined) {
							return false
						}
						const now = nowIso()
						database
							.query<[], [string, string, string, string, number, string, string]>(
								`UPDATE spec_requirements
								 SET title = ?, body_md = ?, kind = ?, status = ?, priority = ?, updated_at = ?
								 WHERE id = ? AND deleted_at IS NULL`,
							)
							.run(
								fields.title ?? existing.title,
								fields.body ?? existing.body_md,
								fields.kind ?? normalizeSpecRequirementKind(existing.kind),
								fields.status ?? existing.status,
								fields.priority ?? existing.priority,
								now,
								existing.id,
							)
						yield* scheduleAutoPublishInternal("requirement_updated", projectPath ?? process.cwd())
						return true
					}),
				),
			deleteRequirement: (
				reference: string,
				projectPath?: string,
				selector: SpecRequirementLookupSelector = "auto",
			) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const existing = yield* resolveRequirementByReference(database, reference, selector)
						if (existing === undefined) {
							return false
						}
						const now = nowIso()
						database
							.query<[], [string, string, string]>(
								"UPDATE spec_requirements SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL",
							)
							.run(now, now, existing.id)
						database
							.query<[], [string, string, string]>(
								"UPDATE spec_issue_links SET deleted_at = ?, updated_at = ? WHERE requirement_id = ? AND deleted_at IS NULL",
							)
							.run(now, now, existing.id)
						yield* scheduleAutoPublishInternal("requirement_deleted", projectPath ?? process.cwd())
						return true
					}),
				),
			listIssueRequirements: (issueId: string, projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() =>
						database
							.query<
								{
									readonly id: string
									readonly local_id: string
									readonly external_code: string | null
									readonly title: string
									readonly kind: string
									readonly link_type: string
									readonly implementations_json: string | null
									readonly fulfillment_status: string | null
									readonly fulfillment_percent: number | null
									readonly evidence_note: string | null
								},
								[string]
							>(
								`SELECT
									r.id,
									r.local_id,
									r.external_code,
									r.title,
									r.kind,
									l.link_type,
									l.implementations_json,
									l.fulfillment_status,
									l.fulfillment_percent,
									l.evidence_note
								FROM spec_issue_links l
								INNER JOIN spec_requirements r ON r.id = l.requirement_id
								WHERE l.issue_id = ? AND l.deleted_at IS NULL AND r.deleted_at IS NULL
								ORDER BY r.local_id ASC, l.link_type ASC`,
							)
							.all(issueId)
							.map((row) => ({
								id: row.id,
								local_id: row.local_id,
								external_code: row.external_code,
								title: row.title,
								kind: normalizeSpecRequirementKind(row.kind),
								link_type: normalizeSpecLinkType(row.link_type),
								implementations: decodeSpecImplementations(row.implementations_json),
								fulfillment_status: normalizeSpecLinkFulfillmentStatus(row.fulfillment_status),
								fulfillment_percent: normalizeSpecLinkFulfillmentPercent(row.fulfillment_percent),
								evidence_note: normalizeSpecLinkEvidenceNote(row.evidence_note),
							})),
					).pipe(
						Effect.mapError(() =>
							mapStorageError(`Failed to load linked requirements for issue ${issueId}`),
						),
					),
				),
			listLinks: (filters, projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const resolvedRequirementId =
							filters?.requirementId === undefined
								? undefined
								: yield* resolveRequirementByReference(
										database,
										filters.requirementId,
										filters.requirementSelector ?? "auto",
									).pipe(Effect.map((row) => row?.id))
						if (filters?.requirementId !== undefined && resolvedRequirementId === undefined) {
							return [] as ReadonlyArray<SpecIssueLink>
						}
						return listLinkRows(database)
							.map((row) => rowToSpecIssueLink(row))
							.filter((link) => {
								if (filters?.issueId !== undefined && link.issue_id !== filters.issueId) {
									return false
								}
								if (
									resolvedRequirementId !== undefined &&
									link.requirement_id !== resolvedRequirementId
								) {
									return false
								}
								if (
									filters?.implementation !== undefined &&
									!link.implementations.includes(
										normalizeSpecImplementation(filters.implementation),
									)
								) {
									return false
								}
								return true
							})
					}),
				),
			addIssueLink: (
				issueId: string,
				requirementReference: string,
				linkType: SpecLinkType,
				projectPath?: string,
				requirementSelector: SpecRequirementLookupSelector = "auto",
				implementations?: ReadonlyArray<string>,
				fulfillment?,
			) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const requirement = yield* resolveRequirementByReference(
							database,
							requirementReference,
							requirementSelector,
						)
						if (requirement === undefined) {
							return yield* Effect.fail(
								mapStorageError(`Spec requirement not found: ${requirementReference}`),
							)
						}
						const issueExists =
							database
								.query<{ readonly id: string }, [string]>(
									"SELECT id FROM issues WHERE id = ? AND deleted_at IS NULL LIMIT 1",
								)
								.get(issueId) !== undefined
						if (!issueExists) {
							return yield* Effect.fail(mapStorageError(`Issue not found: ${issueId}`))
						}
						const normalizedImplementations = yield* resolveSpecLinkImplementationsForWrite(
							database,
							issueId,
							implementations,
						)
						const existing =
							database
								.query<{ readonly implementations_json: string | null }, [string, string, string]>(
									`SELECT implementations_json
								 FROM spec_issue_links
								 WHERE issue_id = ? AND requirement_id = ? AND link_type = ?
								 LIMIT 1`,
								)
								.get(issueId, requirement.id, normalizeSpecLinkType(linkType)) ?? undefined
						const mergedImplementations =
							existing === undefined
								? normalizedImplementations
								: [
										...new Set([
											...decodeSpecImplementations(existing.implementations_json),
											...normalizedImplementations,
										]),
									].sort((left, right) => left.localeCompare(right))
						const now = nowIso()
						database
							.query<
								[],
								[
									string,
									string,
									string,
									string,
									string,
									number | null,
									string | null,
									string,
									string,
									null,
								]
							>(
								`INSERT INTO spec_issue_links (
									issue_id, requirement_id, link_type, implementations_json, fulfillment_status, fulfillment_percent, evidence_note, created_at, updated_at, deleted_at
								) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
								ON CONFLICT(issue_id, requirement_id, link_type)
								DO UPDATE SET
									implementations_json = excluded.implementations_json,
									fulfillment_status = excluded.fulfillment_status,
									fulfillment_percent = excluded.fulfillment_percent,
									evidence_note = excluded.evidence_note,
									deleted_at = NULL,
									updated_at = excluded.updated_at`,
							)
							.run(
								issueId,
								requirement.id,
								normalizeSpecLinkType(linkType),
								encodeSpecImplementations(mergedImplementations),
								normalizeSpecLinkFulfillmentStatus(fulfillment?.status),
								normalizeSpecLinkFulfillmentPercent(fulfillment?.percent),
								normalizeSpecLinkEvidenceNote(fulfillment?.evidenceNote),
								now,
								now,
								null,
							)
						yield* scheduleAutoPublishInternal("link_added", projectPath ?? process.cwd())
					}),
				),
			removeIssueLink: (
				issueId: string,
				requirementReference: string,
				linkType?: SpecLinkType,
				projectPath?: string,
				requirementSelector: SpecRequirementLookupSelector = "auto",
				implementations?: ReadonlyArray<string>,
			) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const requirement = yield* resolveRequirementByReference(
							database,
							requirementReference,
							requirementSelector,
						)
						if (requirement === undefined) {
							return 0
						}
						const rows =
							linkType === undefined
								? database
										.query<
											{ readonly link_type: string; readonly implementations_json: string | null },
											[string, string]
										>(
											`SELECT link_type, implementations_json
											 FROM spec_issue_links
											 WHERE issue_id = ? AND requirement_id = ? AND deleted_at IS NULL`,
										)
										.all(issueId, requirement.id)
								: database
										.query<
											{ readonly link_type: string; readonly implementations_json: string | null },
											[string, string, string]
										>(
											`SELECT link_type, implementations_json
											 FROM spec_issue_links
											 WHERE issue_id = ? AND requirement_id = ? AND link_type = ? AND deleted_at IS NULL`,
										)
										.all(issueId, requirement.id, normalizeSpecLinkType(linkType))
						if (rows.length === 0) {
							return 0
						}
						const normalizedImplementations = yield* resolveSpecLinkImplementationsForWrite(
							database,
							issueId,
							implementations,
						)
						const now = nowIso()
						for (const row of rows) {
							const remaining = decodeSpecImplementations(row.implementations_json).filter(
								(implementation) => !normalizedImplementations.includes(implementation),
							)
							if (remaining.length === 0) {
								database
									.query<[], [string, string, string, string, string]>(
										`UPDATE spec_issue_links
										 SET deleted_at = ?, updated_at = ?
										 WHERE issue_id = ? AND requirement_id = ? AND link_type = ? AND deleted_at IS NULL`,
									)
									.run(now, now, issueId, requirement.id, normalizeSpecLinkType(row.link_type))
							} else {
								database
									.query<[], [string, string, string, string, string]>(
										`UPDATE spec_issue_links
										 SET implementations_json = ?, updated_at = ?
										 WHERE issue_id = ? AND requirement_id = ? AND link_type = ? AND deleted_at IS NULL`,
									)
									.run(
										encodeSpecImplementations(remaining),
										now,
										issueId,
										requirement.id,
										normalizeSpecLinkType(row.link_type),
									)
							}
						}
						yield* scheduleAutoPublishInternal("link_removed", projectPath ?? process.cwd())
						return rows.length
					}),
				),
			updateIssueLink: (
				issueId: string,
				requirementReference: string,
				fields,
				linkType?: SpecLinkType,
				projectPath?: string,
				requirementSelector: SpecRequirementLookupSelector = "auto",
			) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const requirement = yield* resolveRequirementByReference(
							database,
							requirementReference,
							requirementSelector,
						)
						if (requirement === undefined) {
							return 0
						}
						if (
							fields.status === undefined &&
							fields.percent === undefined &&
							fields.evidenceNote === undefined
						) {
							return 0
						}
						const rows =
							linkType === undefined
								? database
										.query<
											{
												readonly link_type: string
												readonly fulfillment_status: string | null
												readonly fulfillment_percent: number | null
												readonly evidence_note: string | null
											},
											[string, string]
										>(
											`SELECT link_type, fulfillment_status, fulfillment_percent, evidence_note
											 FROM spec_issue_links
											 WHERE issue_id = ? AND requirement_id = ? AND deleted_at IS NULL`,
										)
										.all(issueId, requirement.id)
								: database
										.query<
											{
												readonly link_type: string
												readonly fulfillment_status: string | null
												readonly fulfillment_percent: number | null
												readonly evidence_note: string | null
											},
											[string, string, string]
										>(
											`SELECT link_type, fulfillment_status, fulfillment_percent, evidence_note
											 FROM spec_issue_links
											 WHERE issue_id = ? AND requirement_id = ? AND link_type = ? AND deleted_at IS NULL`,
										)
										.all(issueId, requirement.id, normalizeSpecLinkType(linkType))
						if (rows.length === 0) {
							return 0
						}
						const now = nowIso()
						for (const row of rows) {
							database
								.query<[], [string, number | null, string | null, string, string, string, string]>(
									`UPDATE spec_issue_links
									 SET fulfillment_status = ?, fulfillment_percent = ?, evidence_note = ?, updated_at = ?
									 WHERE issue_id = ? AND requirement_id = ? AND link_type = ? AND deleted_at IS NULL`,
								)
								.run(
									normalizeSpecLinkFulfillmentStatus(fields.status ?? row.fulfillment_status),
									normalizeSpecLinkFulfillmentPercent(fields.percent ?? row.fulfillment_percent),
									normalizeSpecLinkEvidenceNote(fields.evidenceNote ?? row.evidence_note),
									now,
									issueId,
									requirement.id,
									normalizeSpecLinkType(row.link_type),
								)
						}
						yield* scheduleAutoPublishInternal("link_updated", projectPath ?? process.cwd())
						return rows.length
					}),
				),
			listRequirementIssues: (
				reference: string,
				projectPath?: string,
				selector: SpecRequirementLookupSelector = "auto",
			) =>
				withDatabase(projectPath, (database) =>
					resolveRequirementByReference(database, reference, selector).pipe(
						Effect.flatMap((requirement) =>
							requirement === undefined
								? Effect.succeed<ReadonlyArray<SpecIssueRef>>([])
								: Effect.sync(() =>
										database
											.query<
												{
													readonly id: string
													readonly title: string
													readonly status: string
													readonly issue_type: string
													readonly link_type: string
													readonly implementations_json: string | null
													readonly fulfillment_status: string | null
													readonly fulfillment_percent: number | null
													readonly evidence_note: string | null
												},
												[string]
											>(
												`SELECT
													i.id,
													i.title,
													i.status,
													i.issue_type,
													l.link_type,
													l.implementations_json,
													l.fulfillment_status,
													l.fulfillment_percent,
													l.evidence_note
												FROM spec_issue_links l
												INNER JOIN issues i ON i.id = l.issue_id
												WHERE l.requirement_id = ? AND l.deleted_at IS NULL AND i.deleted_at IS NULL
												ORDER BY i.updated_at DESC, i.id ASC`,
											)
											.all(requirement.id)
											.map((row) => ({
												id: row.id,
												title: row.title,
												status: normalizeIssueStatus(row.status),
												issue_type: normalizeIssueType(row.issue_type),
												link_type: normalizeSpecLinkType(row.link_type),
												implementations: decodeSpecImplementations(row.implementations_json),
												fulfillment_status: normalizeSpecLinkFulfillmentStatus(
													row.fulfillment_status,
												),
												fulfillment_percent: normalizeSpecLinkFulfillmentPercent(
													row.fulfillment_percent,
												),
												evidence_note: normalizeSpecLinkEvidenceNote(row.evidence_note),
											})),
									),
						),
						Effect.mapError(() =>
							mapStorageError(`Failed to load linked issues for requirement ${reference}`),
						),
					),
				),
			getCoverageReport: (projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => buildCoverageReport(database)).pipe(
						Effect.mapError(() => mapStorageError("Failed to build spec coverage report")),
					),
				),
			getParityReport: (implementation: string, projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => {
						const normalizedImplementation = normalizeSpecImplementation(implementation)
						const requirements = listRequirementRows(database)
						const links = listLinkRows(database)
						const parityRequirements = requirements.map((row) => ({
							id: row.id,
							local_id: row.local_id,
							external_code: row.external_code,
							title: row.title,
							implements_issue_ids: [] as Array<string>,
							partial_issue_ids: [] as Array<string>,
							tests_issue_ids: [] as Array<string>,
							other_issue_ids: [] as Array<string>,
						}))
						const parityRequirementById = new Map(
							parityRequirements.map((requirement) => [requirement.id, requirement] as const),
						)

						for (const linkRow of links) {
							const link = rowToSpecIssueLink(linkRow)
							if (!link.implementations.includes(normalizedImplementation)) {
								continue
							}
							const parityRequirement = parityRequirementById.get(link.requirement_id)
							if (parityRequirement === undefined) {
								continue
							}

							switch (link.link_type) {
								case "implements":
									if (
										link.fulfillment_status === "complete" ||
										link.fulfillment_status === "verified"
									) {
										parityRequirement.implements_issue_ids.push(link.issue_id)
									} else if (link.fulfillment_status === "partial") {
										parityRequirement.partial_issue_ids.push(link.issue_id)
									} else {
										parityRequirement.other_issue_ids.push(link.issue_id)
									}
									break
								case "tests":
									if (
										link.fulfillment_status === "complete" ||
										link.fulfillment_status === "verified"
									) {
										parityRequirement.tests_issue_ids.push(link.issue_id)
									} else {
										parityRequirement.other_issue_ids.push(link.issue_id)
									}
									break
								default:
									parityRequirement.other_issue_ids.push(link.issue_id)
							}
						}

						const implementedRequirementIds = parityRequirements
							.filter((requirement) => requirement.implements_issue_ids.length > 0)
							.map((requirement) => requirement.local_id)
						const partiallyImplementedRequirementIds = parityRequirements
							.filter(
								(requirement) =>
									requirement.implements_issue_ids.length === 0 &&
									requirement.partial_issue_ids.length > 0,
							)
							.map((requirement) => requirement.local_id)
						const testedRequirementIds = parityRequirements
							.filter((requirement) => requirement.tests_issue_ids.length > 0)
							.map((requirement) => requirement.local_id)
						const relatedOnlyRequirementIds = parityRequirements
							.filter(
								(requirement) =>
									requirement.implements_issue_ids.length === 0 &&
									requirement.partial_issue_ids.length === 0 &&
									requirement.tests_issue_ids.length === 0 &&
									requirement.other_issue_ids.length > 0,
							)
							.map((requirement) => requirement.local_id)
						const uncoveredRequirementIds = parityRequirements
							.filter(
								(requirement) =>
									requirement.implements_issue_ids.length === 0 &&
									requirement.partial_issue_ids.length === 0 &&
									requirement.tests_issue_ids.length === 0 &&
									requirement.other_issue_ids.length === 0,
							)
							.map((requirement) => requirement.local_id)

						return {
							implementation: normalizedImplementation,
							total_requirements: requirements.length,
							implemented_requirement_ids: implementedRequirementIds,
							partially_implemented_requirement_ids: partiallyImplementedRequirementIds,
							tested_requirement_ids: testedRequirementIds,
							uncovered_requirement_ids: uncoveredRequirementIds,
							related_only_requirement_ids: relatedOnlyRequirementIds,
							requirements: parityRequirements,
						} satisfies SpecParityReport
					}).pipe(Effect.mapError(() => mapStorageError("Failed to build spec parity report"))),
				),
			lint: (projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => {
						const report = buildCoverageReport(database)
						const gapCounts = {
							unlinked_requirement: report.integrity_gaps.filter(
								(gap) => gap.kind === "unlinked_requirement",
							).length,
							missing_issue: report.integrity_gaps.filter((gap) => gap.kind === "missing_issue")
								.length,
							missing_requirement: report.integrity_gaps.filter(
								(gap) => gap.kind === "missing_requirement",
							).length,
						}
						return {
							ok: report.integrity_gaps.length === 0,
							requirement_count: report.requirements.length,
							linked_requirement_count:
								report.requirements.length - report.unlinked_requirement_ids.length,
							unlinked_requirement_count: report.unlinked_requirement_ids.length,
							integrity_gap_count: report.integrity_gaps.length,
							gap_counts: gapCounts,
							report,
						} satisfies SpecLintResult
					}).pipe(Effect.mapError(() => mapStorageError("Failed to build spec lint result"))),
				),
			getPublishConfig: (projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => {
						return decodeSpecPublishConfigMeta(
							readMetaValue(database, SPEC_PUBLISH_CONFIG_META_KEY),
						)
					}).pipe(Effect.mapError(() => mapStorageError("Failed to load spec publish config"))),
				),
			setPublishConfig: (config: SpecPublishConfig, projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => {
						writeMetaValue(
							database,
							SPEC_PUBLISH_CONFIG_META_KEY,
							encodeSpecPublishConfigMeta(config),
						)
					}).pipe(Effect.mapError(() => mapStorageError("Failed to update spec publish config"))),
				),
			getLastPublishOutcome: (projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => {
						return decodeSpecPublishOutcomeMeta(
							readMetaValue(database, SPEC_PUBLISH_OUTCOME_META_KEY),
						)
					}).pipe(Effect.mapError(() => mapStorageError("Failed to load spec publish outcome"))),
				),
			syncMarkdown: (
				options?: {
					readonly outDir?: string
					readonly check?: boolean
				},
				projectPath?: string,
			) =>
				withDatabase(projectPath, (database) =>
					Effect.gen(function* () {
						const resolvedProjectPath = projectPath ?? process.cwd()
						const check = options?.check === true
						const outDirInput = options?.outDir?.trim()
						const outDir =
							outDirInput === undefined || outDirInput.length === 0
								? pathService.join(resolvedProjectPath, "docs", "spec")
								: outDirInput.startsWith("/")
									? outDirInput
									: pathService.join(resolvedProjectPath, outDirInput)
						const requirements = listRequirementRows(database).map((row) =>
							rowToSpecRequirement(row),
						)
						const links = listLinkRows(database).map((row) => rowToSpecIssueLink(row))
						if (!check) {
							yield* fs
								.makeDirectory(outDir, { recursive: true })
								.pipe(
									Effect.mapError(() =>
										mapStorageError(`Failed to create spec sync directory ${outDir}`),
									),
								)
						}
						const documents = yield* Effect.forEach(
							renderPublishDocuments(requirements, links),
							(document) =>
								Effect.gen(function* () {
									const path = pathService.join(outDir, markdownFilenameByKey[document.key])
									const content = renderSyncedMarkdown(document.key, document.content)
									const exists = yield* fs.exists(path).pipe(Effect.orElseSucceed(() => false))
									const previousContent = exists
										? yield* fs
												.readFileString(path)
												.pipe(
													Effect.mapError(() =>
														mapStorageError(`Failed to read existing markdown file ${path}`),
													),
												)
										: undefined
									const changed = previousContent !== content
									if (changed && !check) {
										yield* fs
											.writeFileString(path, content)
											.pipe(
												Effect.mapError(() =>
													mapStorageError(`Failed to write markdown file ${path}`),
												),
											)
									}
									return {
										key: document.key,
										path,
										status: changed ? "updated" : "unchanged",
										changed,
									} satisfies SpecMarkdownSyncDocumentResult
								}),
							{ concurrency: 1 },
						)
						const changedDocuments = documents.filter((document) => document.changed).length
						return {
							out_dir: outDir,
							check,
							ok: check ? changedDocuments === 0 : true,
							total_documents: documents.length,
							changed_documents: changedDocuments,
							documents,
						} satisfies SpecMarkdownSyncResult
					}),
				),
			publish: (projectPath?: string) => runPublish(projectPath ?? process.cwd()),
			readSnapshot: (projectPath?: string) =>
				withDatabase(projectPath, (database) =>
					Effect.sync(() => {
						const requirements = listRequirementRows(database).map((row) =>
							rowToSpecRequirement(row),
						)
						const links = listLinkRows(database).map((row) => rowToSpecIssueLink(row))
						const coverage = buildCoverageReport(database)
						const publishConfig = decodeSpecPublishConfigMeta(
							readMetaValue(database, SPEC_PUBLISH_CONFIG_META_KEY),
						)
						const lastPublishOutcome = decodeSpecPublishOutcomeMeta(
							readMetaValue(database, SPEC_PUBLISH_OUTCOME_META_KEY),
						)
						return {
							requirements,
							links,
							coverage,
							publishConfig,
							lastPublishOutcome,
						} satisfies SpecReadSnapshot
					}).pipe(Effect.mapError(() => mapStorageError("Failed to build spec read snapshot"))),
				),
		} satisfies SpecDaemonServiceApi
	}),
}) {}
