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
type WorktreePathOps = Pick<Path.Path, "basename" | "dirname" | "normalize">

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

export const isWorktreePathForProject = (
	cwdPath: string,
	projectPath: string,
	pathOps: WorktreePathOps,
): boolean => {
	const cwdNorm = pathOps.normalize(cwdPath)
	const projectNorm = pathOps.normalize(projectPath)
	const projectParent = pathOps.dirname(projectNorm)
	const projectBase = pathOps.basename(projectNorm)
	let candidate = cwdNorm

	while (true) {
		if (pathOps.dirname(candidate) === projectParent) {
			const candidateBase = pathOps.basename(candidate)
			if (candidateBase.startsWith(`${projectBase}-`)) {
				return true
			}
		}

		const parent = pathOps.dirname(candidate)
		if (parent === candidate) {
			return false
		}
		candidate = parent
	}
}

export const resolveRegisteredProjectRootForWorktree = (options: {
	readonly cwdPath: string
	readonly projectPath: string
	readonly pathOps: WorktreePathOps
	readonly isTrackedGitWorktree: boolean
}): string | undefined =>
	options.isTrackedGitWorktree ||
	isWorktreePathForProject(options.cwdPath, options.projectPath, options.pathOps)
		? options.projectPath
		: undefined

export const resolveConfigBasePath = (options: {
	readonly cwdPath: string
	readonly projectPath: string
	readonly pathOps: WorktreePathOps
	readonly cwdHasConfig: boolean
}): string => {
	const registeredProjectRoot = resolveRegisteredProjectRootForWorktree({
		cwdPath: options.cwdPath,
		projectPath: options.projectPath,
		pathOps: options.pathOps,
		isTrackedGitWorktree: false,
	})
	if (registeredProjectRoot !== undefined) {
		return registeredProjectRoot
	}

	return options.cwdHasConfig ? options.cwdPath : options.projectPath
}
