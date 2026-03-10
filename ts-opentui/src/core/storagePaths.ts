import type { Path } from "@effect/platform"

export const AZEDARACH_STORAGE_DIRECTORY = ".azedarach"
export const AZEDARACH_DB_FILENAME = "azedarach.db"
export const LEGACY_AZEDARACH_DB_FILENAME = "issues.db"
export const AZEDARACH_CONFIG_FILENAME = "config.json"
export const LEGACY_AZEDARACH_CONFIG_FILENAME = ".azedarach.json"
export const AZEDARACH_CONFIG_SCHEMA_FILENAME = "config.schema.json"
export const LEGACY_AZEDARACH_CONFIG_SCHEMA_FILENAME = ".azedarach.schema.json"
export const LEGACY_PROJECT_UI_STATE_FILENAME = "state.json"

type JoinPathOps = Pick<Path.Path, "join">
type ConfigSchemaPathOps = Pick<Path.Path, "dirname" | "join">

export interface ProjectStoragePaths {
	readonly storageDirectory: string
	readonly canonicalDbPath: string
	readonly legacyDbPath: string
	readonly canonicalConfigPath: string
	readonly legacyConfigPath: string
	readonly canonicalConfigSchemaPath: string
	readonly legacyConfigSchemaPath: string
	readonly legacyProjectUiStatePath: string
}

export const getProjectStoragePaths = (
	projectPath: string,
	pathOps: JoinPathOps,
): ProjectStoragePaths => {
	const storageDirectory = pathOps.join(projectPath, AZEDARACH_STORAGE_DIRECTORY)
	return {
		storageDirectory,
		canonicalDbPath: pathOps.join(storageDirectory, AZEDARACH_DB_FILENAME),
		legacyDbPath: pathOps.join(storageDirectory, LEGACY_AZEDARACH_DB_FILENAME),
		canonicalConfigPath: pathOps.join(storageDirectory, AZEDARACH_CONFIG_FILENAME),
		legacyConfigPath: pathOps.join(projectPath, LEGACY_AZEDARACH_CONFIG_FILENAME),
		canonicalConfigSchemaPath: pathOps.join(storageDirectory, AZEDARACH_CONFIG_SCHEMA_FILENAME),
		legacyConfigSchemaPath: pathOps.join(projectPath, LEGACY_AZEDARACH_CONFIG_SCHEMA_FILENAME),
		legacyProjectUiStatePath: pathOps.join(storageDirectory, LEGACY_PROJECT_UI_STATE_FILENAME),
	}
}

export const resolveConfigSchemaPath = (
	configPath: string,
	pathOps: ConfigSchemaPathOps,
): string => {
	const schemaFilename = configPath.endsWith(`/${AZEDARACH_CONFIG_FILENAME}`)
		? AZEDARACH_CONFIG_SCHEMA_FILENAME
		: LEGACY_AZEDARACH_CONFIG_SCHEMA_FILENAME
	return pathOps.join(pathOps.dirname(configPath), schemaFilename)
}
