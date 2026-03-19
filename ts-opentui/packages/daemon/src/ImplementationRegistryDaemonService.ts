import { Database } from "bun:sqlite"
import { getProjectStoragePaths } from "@azedarach/config"
import { FileSystem, Path } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Data, Effect, Schema } from "effect"

const IMPLEMENTATION_REGISTRY_META_KEY = "impl:registry:v1"
const IMPLEMENTATION_DEFAULT_META_KEY = "impl:default"
const DEFAULT_IMPLEMENTATION = "default"
const BUILTIN_IMPLEMENTATION_TIMESTAMP = "1970-01-01T00:00:00.000Z"
const IMPLEMENTATION_NAME_PATTERN = /^[a-z][a-z0-9-]{0,63}$/

const ImplementationRegistryJsonSchema = Schema.parseJson(
	Schema.Array(
		Schema.Struct({
			name: Schema.String,
			description: Schema.NullOr(Schema.String),
			directory: Schema.NullOr(Schema.String).pipe(Schema.optional),
			created_at: Schema.String,
			updated_at: Schema.String,
		}),
	),
)

const SpecImplementationsJsonSchema = Schema.parseJson(Schema.Array(Schema.String))

type ImplementationRegistryEntry = Schema.Schema.Type<
	typeof ImplementationRegistryJsonSchema
>[number]

interface MetaRow {
	readonly value: string
}

interface IssueImplementationsRow {
	readonly id: string
	readonly implementations_json: string | null
}

interface SpecLinkImplementationsRow {
	readonly issue_id: string
	readonly requirement_id: string
	readonly link_type: string
	readonly implementations_json: string | null
}

export interface ImplementationRecord {
	readonly name: string
	readonly description?: string
	readonly directory?: string
	readonly created_at: string
	readonly updated_at: string
	readonly is_default: boolean
	readonly is_builtin: boolean
}

export interface ImplementationRegistry {
	readonly default_implementation: string
	readonly implicit_default_allowed: boolean
	readonly implementations: ReadonlyArray<ImplementationRecord>
}

export class ImplementationRegistryDaemonError extends Data.TaggedError(
	"ImplementationRegistryDaemonError",
)<{
	readonly reason: "invalid-name" | "already-exists" | "not-found" | "in-use" | "storage"
	readonly message: string
}> {}

export interface ImplementationRegistryDaemonServiceApi {
	readonly getRegistry: (
		projectPath?: string,
	) => Effect.Effect<ImplementationRegistry, ImplementationRegistryDaemonError>
	readonly create: (
		params: {
			readonly name: string
			readonly description?: string
			readonly directory?: string
			readonly setDefault?: boolean
		},
		projectPath?: string,
	) => Effect.Effect<ImplementationRecord, ImplementationRegistryDaemonError>
	readonly update: (
		currentName: string,
		fields: {
			readonly name?: string
			readonly description?: string | null
			readonly directory?: string | null
			readonly setDefault?: boolean
		},
		projectPath?: string,
	) => Effect.Effect<ImplementationRecord, ImplementationRegistryDaemonError>
	readonly delete: (
		name: string,
		projectPath?: string,
	) => Effect.Effect<void, ImplementationRegistryDaemonError>
	readonly setDefault: (
		name: string,
		projectPath?: string,
	) => Effect.Effect<ImplementationRegistry, ImplementationRegistryDaemonError>
}

const nowIso = (): string => new Date().toISOString()

const buildBuiltinDefaultImplementation = (): ImplementationRegistryEntry => ({
	name: DEFAULT_IMPLEMENTATION,
	description: null,
	directory: null,
	created_at: BUILTIN_IMPLEMENTATION_TIMESTAMP,
	updated_at: BUILTIN_IMPLEMENTATION_TIMESTAMP,
})

const normalizeImplementationName = (value: string): string => value.trim().toLowerCase()

const requireImplementationName = (
	value: string,
): Effect.Effect<string, ImplementationRegistryDaemonError> => {
	const normalized = normalizeImplementationName(value)
	return IMPLEMENTATION_NAME_PATTERN.test(normalized)
		? Effect.succeed(normalized)
		: Effect.fail(
				new ImplementationRegistryDaemonError({
					reason: "invalid-name",
					message: `Invalid implementation name: ${value}`,
				}),
			)
}

const normalizeImplementationDescription = (
	value: string | null | undefined,
): string | null | undefined => {
	if (value === undefined) {
		return undefined
	}
	if (value === null) {
		return null
	}
	const normalized = value.trim()
	return normalized.length === 0 ? null : normalized
}

const normalizeImplementationDirectory = (
	value: string | null | undefined,
): string | null | undefined => {
	if (value === undefined) {
		return undefined
	}
	if (value === null) {
		return null
	}
	const normalized = value.trim()
	return normalized.length === 0 ? null : normalized
}

const decodeImplementationRegistryEntries = (
	value: string | undefined,
): ReadonlyArray<ImplementationRegistryEntry> => {
	if (value === undefined) {
		return [buildBuiltinDefaultImplementation()]
	}

	try {
		const decoded = Schema.decodeUnknownSync(ImplementationRegistryJsonSchema)(value)
		return decoded.some((entry) => entry.name === DEFAULT_IMPLEMENTATION)
			? decoded
			: [buildBuiltinDefaultImplementation(), ...decoded]
	} catch {
		return [buildBuiltinDefaultImplementation()]
	}
}

const encodeImplementationRegistryEntries = (
	value: ReadonlyArray<ImplementationRegistryEntry>,
): string => Schema.encodeSync(ImplementationRegistryJsonSchema)([...value])

const decodeSpecImplementations = (value: string | null): ReadonlyArray<string> => {
	if (value === null) {
		return [DEFAULT_IMPLEMENTATION]
	}

	try {
		const decoded = Schema.decodeUnknownSync(SpecImplementationsJsonSchema)(value)
		const normalized = decoded
			.map((implementation) => normalizeImplementationName(implementation))
			.filter(
				(implementation, index, items) =>
					implementation.length > 0 && items.indexOf(implementation) === index,
			)
		return normalized.length > 0 ? normalized : [DEFAULT_IMPLEMENTATION]
	} catch {
		return [DEFAULT_IMPLEMENTATION]
	}
}

const encodeSpecImplementations = (implementations: ReadonlyArray<string>): string =>
	Schema.encodeSync(SpecImplementationsJsonSchema)([...implementations])

const resolveDefaultImplementationName = (
	value: string | undefined,
	entries: ReadonlyArray<ImplementationRegistryEntry>,
): string => {
	const normalized =
		value === undefined ? DEFAULT_IMPLEMENTATION : normalizeImplementationName(value)
	return entries.some((entry) => entry.name === normalized)
		? normalized
		: (entries.find((entry) => entry.name === DEFAULT_IMPLEMENTATION)?.name ??
				entries[0]?.name ??
				DEFAULT_IMPLEMENTATION)
}

const sortImplementationEntries = (
	entries: ReadonlyArray<ImplementationRegistryEntry>,
	defaultImplementation: string,
): ReadonlyArray<ImplementationRegistryEntry> =>
	[...entries].sort((left, right) => {
		if (left.name === defaultImplementation && right.name !== defaultImplementation) {
			return -1
		}
		if (right.name === defaultImplementation && left.name !== defaultImplementation) {
			return 1
		}
		if (left.name === DEFAULT_IMPLEMENTATION && right.name !== DEFAULT_IMPLEMENTATION) {
			return -1
		}
		if (right.name === DEFAULT_IMPLEMENTATION && left.name !== DEFAULT_IMPLEMENTATION) {
			return 1
		}
		return left.name.localeCompare(right.name)
	})

const implementationEntryToRecord = (
	entry: ImplementationRegistryEntry,
	defaultImplementation: string,
): ImplementationRecord => ({
	name: entry.name,
	description: entry.description ?? undefined,
	directory: entry.directory ?? undefined,
	created_at: entry.created_at,
	updated_at: entry.updated_at,
	is_default: entry.name === defaultImplementation,
	is_builtin: entry.name === DEFAULT_IMPLEMENTATION,
})

const buildImplementationRegistry = (
	entries: ReadonlyArray<ImplementationRegistryEntry>,
	defaultImplementation: string,
): ImplementationRegistry => ({
	default_implementation: defaultImplementation,
	implicit_default_allowed: entries.length === 1 && entries[0]?.name === DEFAULT_IMPLEMENTATION,
	implementations: sortImplementationEntries(entries, defaultImplementation).map((entry) =>
		implementationEntryToRecord(entry, defaultImplementation),
	),
})

const mapStorageError = (message: string): ImplementationRegistryDaemonError =>
	new ImplementationRegistryDaemonError({
		reason: "storage",
		message,
	})

const ensureRegistrySchema = (database: Database): void => {
	database.run("PRAGMA journal_mode = WAL")
	database.run("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
	database.run(
		"CREATE TABLE IF NOT EXISTS issues (id TEXT PRIMARY KEY, implementations_json TEXT, updated_at TEXT NOT NULL DEFAULT '', deleted_at TEXT)",
	)
	database.run(
		"CREATE TABLE IF NOT EXISTS spec_issue_links (issue_id TEXT NOT NULL, requirement_id TEXT NOT NULL, link_type TEXT NOT NULL, implementations_json TEXT NOT NULL DEFAULT '[\"default\"]', updated_at TEXT NOT NULL DEFAULT '', deleted_at TEXT)",
	)
}

const loadImplementationRegistryState = (
	database: Database,
): {
	readonly entries: ReadonlyArray<ImplementationRegistryEntry>
	readonly defaultImplementation: string
} => {
	const registryRow = database
		.query<MetaRow, [string]>("SELECT value FROM meta WHERE key = ?")
		.get(IMPLEMENTATION_REGISTRY_META_KEY)
	const defaultRow = database
		.query<MetaRow, [string]>("SELECT value FROM meta WHERE key = ?")
		.get(IMPLEMENTATION_DEFAULT_META_KEY)
	const entries = decodeImplementationRegistryEntries(registryRow?.value)
	return {
		entries,
		defaultImplementation: resolveDefaultImplementationName(defaultRow?.value, entries),
	}
}

const renameImplementationReferences = (
	database: Database,
	currentName: string,
	nextName: string,
	now: string,
): void => {
	const issueRows = database
		.query<IssueImplementationsRow, []>(
			"SELECT id, implementations_json FROM issues WHERE deleted_at IS NULL",
		)
		.all()
	for (const row of issueRows) {
		const implementations = decodeSpecImplementations(row.implementations_json)
		if (!implementations.includes(currentName)) {
			continue
		}
		const renamed = implementations.map((implementation) =>
			implementation === currentName ? nextName : implementation,
		)
		database
			.prepare("UPDATE issues SET implementations_json = ?, updated_at = ? WHERE id = ?")
			.run(encodeSpecImplementations(renamed), now, row.id)
	}

	const linkRows = database
		.query<SpecLinkImplementationsRow, []>(
			"SELECT issue_id, requirement_id, link_type, implementations_json FROM spec_issue_links WHERE deleted_at IS NULL",
		)
		.all()
	for (const row of linkRows) {
		const implementations = decodeSpecImplementations(row.implementations_json)
		if (!implementations.includes(currentName)) {
			continue
		}
		const renamed = implementations.map((implementation) =>
			implementation === currentName ? nextName : implementation,
		)
		database
			.prepare(
				"UPDATE spec_issue_links SET implementations_json = ?, updated_at = ? WHERE issue_id = ? AND requirement_id = ? AND link_type = ? AND deleted_at IS NULL",
			)
			.run(encodeSpecImplementations(renamed), now, row.issue_id, row.requirement_id, row.link_type)
	}
}

export class ImplementationRegistryDaemonService extends Effect.Service<ImplementationRegistryDaemonService>()(
	"ImplementationRegistryDaemonService",
	{
		dependencies: [BunContext.layer],
		effect: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path

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
					const canonicalExists = yield* fs
						.exists(storagePaths.canonicalDbPath)
						.pipe(Effect.orElseSucceed(() => false))
					const legacyExists = canonicalExists
						? false
						: yield* fs.exists(storagePaths.legacyDbPath).pipe(Effect.orElseSucceed(() => false))
					return canonicalExists
						? storagePaths.canonicalDbPath
						: legacyExists
							? storagePaths.legacyDbPath
							: storagePaths.canonicalDbPath
				})

			const withDatabase = <A>(
				projectPath: string | undefined,
				use: (database: Database) => Effect.Effect<A, ImplementationRegistryDaemonError>,
			): Effect.Effect<A, ImplementationRegistryDaemonError> =>
				resolveDbPath(projectPath).pipe(
					Effect.flatMap((dbPath) =>
						Effect.acquireUseRelease(
							Effect.try({
								try: () => {
									const database = new Database(dbPath)
									ensureRegistrySchema(database)
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

			return {
				getRegistry: (projectPath?: string) =>
					withDatabase(projectPath, (database) =>
						Effect.sync(() => {
							const { entries, defaultImplementation } = loadImplementationRegistryState(database)
							return buildImplementationRegistry(entries, defaultImplementation)
						}).pipe(
							Effect.mapError(() =>
								mapStorageError("Failed to load implementation registry from sqlite"),
							),
						),
					),

				create: (params, projectPath?: string) =>
					withDatabase(projectPath, (database) =>
						Effect.gen(function* () {
							const normalizedName = yield* requireImplementationName(params.name)
							return yield* Effect.try({
								try: () =>
									database.transaction(() => {
										const { entries, defaultImplementation } =
											loadImplementationRegistryState(database)
										if (entries.some((entry) => entry.name === normalizedName)) {
											throw new ImplementationRegistryDaemonError({
												reason: "already-exists",
												message: `Implementation already exists: ${normalizedName}`,
											})
										}

										const timestamp = nowIso()
										const nextEntries: ReadonlyArray<ImplementationRegistryEntry> = [
											...entries,
											{
												name: normalizedName,
												description: normalizeImplementationDescription(params.description) ?? null,
												directory: normalizeImplementationDirectory(params.directory) ?? null,
												created_at: timestamp,
												updated_at: timestamp,
											},
										]
										const nextDefaultImplementation =
											params.setDefault === true ? normalizedName : defaultImplementation
										const encodedEntries = encodeImplementationRegistryEntries(nextEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_REGISTRY_META_KEY, encodedEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_DEFAULT_META_KEY, nextDefaultImplementation)
										const created = nextEntries.find((entry) => entry.name === normalizedName)
										if (created === undefined) {
											throw mapStorageError("Created implementation could not be reloaded")
										}
										return implementationEntryToRecord(created, nextDefaultImplementation)
									})(),
								catch: (error) =>
									error instanceof ImplementationRegistryDaemonError
										? error
										: mapStorageError("Failed to create implementation registry entry"),
							})
						}),
					),

				update: (currentName, fields, projectPath?: string) =>
					withDatabase(projectPath, (database) =>
						Effect.gen(function* () {
							const normalizedCurrentName = yield* requireImplementationName(currentName)
							const normalizedNextName =
								fields.name === undefined
									? normalizedCurrentName
									: yield* requireImplementationName(fields.name)
							if (
								normalizedCurrentName === DEFAULT_IMPLEMENTATION &&
								normalizedNextName !== normalizedCurrentName
							) {
								return yield* Effect.fail(
									new ImplementationRegistryDaemonError({
										reason: "invalid-name",
										message: "The built-in default implementation cannot be renamed",
									}),
								)
							}

							return yield* Effect.try({
								try: () =>
									database.transaction(() => {
										const { entries, defaultImplementation } =
											loadImplementationRegistryState(database)
										const existing = entries.find((entry) => entry.name === normalizedCurrentName)
										if (existing === undefined) {
											throw new ImplementationRegistryDaemonError({
												reason: "not-found",
												message: `Implementation not found: ${normalizedCurrentName}`,
											})
										}
										if (
											normalizedNextName !== normalizedCurrentName &&
											entries.some((entry) => entry.name === normalizedNextName)
										) {
											throw new ImplementationRegistryDaemonError({
												reason: "already-exists",
												message: `Implementation already exists: ${normalizedNextName}`,
											})
										}

										const nextDescription = normalizeImplementationDescription(fields.description)
										const nextDirectory = normalizeImplementationDirectory(fields.directory)
										const timestamp = nowIso()
										const nextEntries = entries.map((entry) =>
											entry.name !== normalizedCurrentName
												? entry
												: {
														name: normalizedNextName,
														description:
															nextDescription === undefined ? entry.description : nextDescription,
														directory:
															nextDirectory === undefined
																? (entry.directory ?? null)
																: nextDirectory,
														created_at: entry.created_at,
														updated_at: timestamp,
													},
										)
										const nextDefaultImplementation =
											fields.setDefault === true
												? normalizedNextName
												: defaultImplementation === normalizedCurrentName
													? normalizedNextName
													: defaultImplementation
										const encodedEntries = encodeImplementationRegistryEntries(nextEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_REGISTRY_META_KEY, encodedEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_DEFAULT_META_KEY, nextDefaultImplementation)
										if (normalizedNextName !== normalizedCurrentName) {
											renameImplementationReferences(
												database,
												normalizedCurrentName,
												normalizedNextName,
												timestamp,
											)
										}
										const updated = nextEntries.find((entry) => entry.name === normalizedNextName)
										if (updated === undefined) {
											throw mapStorageError("Updated implementation could not be reloaded")
										}
										return implementationEntryToRecord(updated, nextDefaultImplementation)
									})(),
								catch: (error) =>
									error instanceof ImplementationRegistryDaemonError
										? error
										: mapStorageError("Failed to update implementation registry entry"),
							})
						}),
					),

				delete: (name, projectPath?: string) =>
					withDatabase(projectPath, (database) =>
						Effect.gen(function* () {
							const normalizedName = yield* requireImplementationName(name)
							if (normalizedName === DEFAULT_IMPLEMENTATION) {
								return yield* Effect.fail(
									new ImplementationRegistryDaemonError({
										reason: "invalid-name",
										message: "The built-in default implementation cannot be deleted",
									}),
								)
							}

							return yield* Effect.try({
								try: () =>
									database.transaction(() => {
										const { entries, defaultImplementation } =
											loadImplementationRegistryState(database)
										if (!entries.some((entry) => entry.name === normalizedName)) {
											throw new ImplementationRegistryDaemonError({
												reason: "not-found",
												message: `Implementation not found: ${normalizedName}`,
											})
										}
										const issueRows = database
											.query<IssueImplementationsRow, []>(
												"SELECT id, implementations_json FROM issues WHERE deleted_at IS NULL",
											)
											.all()
										if (
											issueRows.some((row) =>
												decodeSpecImplementations(row.implementations_json).includes(
													normalizedName,
												),
											)
										) {
											throw new ImplementationRegistryDaemonError({
												reason: "in-use",
												message: `Implementation ${normalizedName} is still assigned to one or more issues`,
											})
										}
										const linkRows = database
											.query<SpecLinkImplementationsRow, []>(
												"SELECT issue_id, requirement_id, link_type, implementations_json FROM spec_issue_links WHERE deleted_at IS NULL",
											)
											.all()
										if (
											linkRows.some((row) =>
												decodeSpecImplementations(row.implementations_json).includes(
													normalizedName,
												),
											)
										) {
											throw new ImplementationRegistryDaemonError({
												reason: "in-use",
												message: `Implementation ${normalizedName} is still referenced by one or more spec links`,
											})
										}
										const nextEntries = entries.filter((entry) => entry.name !== normalizedName)
										const nextDefaultImplementation =
											defaultImplementation === normalizedName
												? DEFAULT_IMPLEMENTATION
												: defaultImplementation
										const encodedEntries = encodeImplementationRegistryEntries(nextEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_REGISTRY_META_KEY, encodedEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_DEFAULT_META_KEY, nextDefaultImplementation)
										return
									})(),
								catch: (error) =>
									error instanceof ImplementationRegistryDaemonError
										? error
										: mapStorageError("Failed to delete implementation registry entry"),
							})
						}),
					),

				setDefault: (name, projectPath?: string) =>
					withDatabase(projectPath, (database) =>
						Effect.gen(function* () {
							const normalizedName = yield* requireImplementationName(name)
							return yield* Effect.try({
								try: () =>
									database.transaction(() => {
										const { entries } = loadImplementationRegistryState(database)
										if (!entries.some((entry) => entry.name === normalizedName)) {
											throw new ImplementationRegistryDaemonError({
												reason: "not-found",
												message: `Implementation not found: ${normalizedName}`,
											})
										}
										const encodedEntries = encodeImplementationRegistryEntries(entries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_REGISTRY_META_KEY, encodedEntries)
										database
											.prepare(
												"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
											)
											.run(IMPLEMENTATION_DEFAULT_META_KEY, normalizedName)
										return buildImplementationRegistry(entries, normalizedName)
									})(),
								catch: (error) =>
									error instanceof ImplementationRegistryDaemonError
										? error
										: mapStorageError("Failed to set default implementation"),
							})
						}),
					),
			} satisfies ImplementationRegistryDaemonServiceApi
		}),
	},
) {}
