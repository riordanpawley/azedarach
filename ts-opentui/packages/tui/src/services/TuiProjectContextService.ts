import {
	type AzedarachConfig,
	AzedarachConfigJsonSchema,
	AzedarachConfigSchema,
	getProjectStoragePaths,
	resolveConfigBasePath,
	resolveConfigSchemaPath,
} from "@azedarach/config"
import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Schema, SubscriptionRef } from "effect"
import type { Project } from "../contracts.js"

export interface TuiProjectContextReadApi {
	readonly currentProject: SubscriptionRef.SubscriptionRef<Project | undefined>
	readonly projects: SubscriptionRef.SubscriptionRef<ReadonlyArray<Project>>
	readonly getCurrentPath: () => Effect.Effect<string | undefined>
}

export interface TuiProjectContextServiceApi extends TuiProjectContextReadApi {
	readonly getProjects: () => Effect.Effect<ReadonlyArray<Project>>
	readonly requireCurrentProject: () => Effect.Effect<Project, TuiProjectContextError>
	readonly switchProject: (name: string) => Effect.Effect<Project, TuiProjectContextError>
}

export class TuiProjectContextError extends Data.TaggedError("TuiProjectContextError")<{
	readonly message: string
}> {}

type RegisteredProjectConfig = NonNullable<AzedarachConfig["projects"]>[number]

const configJsonSchemaString = `${JSON.stringify(AzedarachConfigJsonSchema, null, 2)}\n`

const getConfiguredProjects = (config: AzedarachConfig): ReadonlyArray<RegisteredProjectConfig> =>
	config.projects ?? []

const getConfiguredCurrentProject = (
	config: AzedarachConfig,
): RegisteredProjectConfig | undefined => {
	const projects = getConfiguredProjects(config)
	if (projects.length === 0) {
		return undefined
	}

	if (config.defaultProject === undefined) {
		return projects[0]
	}

	return projects.find((project) => project.name === config.defaultProject) ?? projects[0]
}

export class TuiProjectContextService extends Effect.Service<TuiProjectContextService>()(
	"TuiProjectContextService",
	{
		scoped: Effect.gen(function* () {
			const fs = yield* FileSystem.FileSystem
			const pathService = yield* Path.Path

			const loadWritableConfig = (configPath: string) =>
				Effect.gen(function* () {
					const exists = yield* fs.exists(configPath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						return yield* Schema.decodeUnknown(AzedarachConfigSchema)({}).pipe(
							Effect.mapError(
								(error) =>
									new TuiProjectContextError({
										message: `Failed to create default config snapshot: ${String(error)}`,
									}),
							),
						)
					}

					const content = yield* fs.readFileString(configPath).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Failed to read config file ${configPath}: ${String(error)}`,
								}),
						),
					)

					return yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(content).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Config parse/validation failed for ${configPath}: ${String(error)}`,
								}),
						),
					)
				})

			const saveWritableConfig = (configPath: string, config: AzedarachConfig) =>
				Effect.gen(function* () {
					const configDir = pathService.dirname(configPath)
					const encoded = yield* Schema.encode(AzedarachConfigSchema)(config).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Failed to encode config: ${String(error)}`,
								}),
						),
					)
					yield* fs
						.makeDirectory(configDir, { recursive: true })
						.pipe(Effect.orElseSucceed(() => void 0))
					yield* fs.writeFileString(configPath, `${JSON.stringify(encoded, null, 2)}\n`).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Failed to write config file ${configPath}: ${String(error)}`,
								}),
						),
					)
					const schemaPath = resolveConfigSchemaPath(configPath, pathService)
					yield* fs
						.writeFileString(schemaPath, configJsonSchemaString)
						.pipe(Effect.orElseSucceed(() => void 0))
				})

			const loadConfigIfExists = (configPath: string) =>
				Effect.gen(function* () {
					const exists = yield* fs.exists(configPath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						return undefined
					}
					return yield* loadWritableConfig(configPath)
				})

			const resolveSelectedProjectPathFromWorkspaceConfig = (cwdPath: string) =>
				Effect.gen(function* () {
					const storagePaths = getProjectStoragePaths(cwdPath, pathService)
					const config =
						(yield* loadConfigIfExists(storagePaths.canonicalConfigPath)) ??
						(yield* loadConfigIfExists(storagePaths.legacyConfigPath))
					return getConfiguredCurrentProject(config ?? {})?.path
				})

			const resolveWritableConfigPath = (cwdPath: string, projectPath: string | undefined) =>
				Effect.gen(function* () {
					const cwdStoragePaths = getProjectStoragePaths(cwdPath, pathService)
					const cwdHasCanonicalConfig = yield* fs
						.exists(cwdStoragePaths.canonicalConfigPath)
						.pipe(Effect.orElseSucceed(() => false))
					const cwdHasLegacyConfig = cwdHasCanonicalConfig
						? false
						: yield* fs
								.exists(cwdStoragePaths.legacyConfigPath)
								.pipe(Effect.orElseSucceed(() => false))
					const configBasePath = resolveConfigBasePath({
						cwdPath,
						projectPath: projectPath ?? cwdPath,
						pathOps: pathService,
						cwdHasConfig: cwdHasCanonicalConfig || cwdHasLegacyConfig,
					})
					const storagePaths = getProjectStoragePaths(configBasePath, pathService)
					const canonicalExists = yield* fs
						.exists(storagePaths.canonicalConfigPath)
						.pipe(Effect.orElseSucceed(() => false))
					if (canonicalExists) {
						return storagePaths.canonicalConfigPath
					}
					const legacyExists = yield* fs
						.exists(storagePaths.legacyConfigPath)
						.pipe(Effect.orElseSucceed(() => false))
					return legacyExists ? storagePaths.legacyConfigPath : storagePaths.canonicalConfigPath
				})

			const determineInitialProject = (
				projectList: ReadonlyArray<Project>,
				defaultName: string | undefined,
				cwdPath: string,
			): Project | undefined => {
				const normalizedCwd = pathService.normalize(cwdPath)
				const cwdProject = projectList.find(
					(project) => pathService.normalize(project.path) === normalizedCwd,
				)
				if (cwdProject !== undefined) {
					return cwdProject
				}

				const parentProject = projectList.find((project) =>
					normalizedCwd.startsWith(`${pathService.normalize(project.path)}${pathService.sep}`),
				)
				if (parentProject !== undefined) {
					return parentProject
				}

				if (defaultName !== undefined) {
					const defaultProject = projectList.find((project) => project.name === defaultName)
					if (defaultProject !== undefined) {
						return defaultProject
					}
				}

				const worktreeProject = projectList.find((project) => {
					const registeredRoot = resolveConfigBasePath({
						cwdPath: normalizedCwd,
						projectPath: project.path,
						pathOps: pathService,
						cwdHasConfig: false,
					})
					return pathService.normalize(registeredRoot) === pathService.normalize(project.path)
				})
				if (worktreeProject !== undefined) {
					return worktreeProject
				}

				return projectList[0]
			}

			const cwdPath = process.cwd()
			const selectedProjectPath = yield* resolveSelectedProjectPathFromWorkspaceConfig(cwdPath)
			const configPath = yield* resolveWritableConfigPath(cwdPath, selectedProjectPath)
			const initialConfig = yield* loadWritableConfig(configPath)
			const initialProjects = getConfiguredProjects(initialConfig)
			const currentProject = yield* SubscriptionRef.make<Project | undefined>(
				determineInitialProject(initialProjects, initialConfig.defaultProject, cwdPath),
			)
			const projects = yield* SubscriptionRef.make<ReadonlyArray<Project>>(initialProjects)

			const persistSelection = (projectName: string) =>
				Effect.gen(function* () {
					const currentConfig = yield* loadWritableConfig(configPath)
					yield* saveWritableConfig(configPath, {
						...currentConfig,
						projects: [...(currentConfig.projects ?? [])],
						defaultProject: projectName,
					})
				})

			const service: TuiProjectContextServiceApi = {
				currentProject,
				projects,
				getProjects: () => SubscriptionRef.get(projects),
				requireCurrentProject: () =>
					Effect.gen(function* () {
						const project = yield* SubscriptionRef.get(currentProject)
						if (project === undefined) {
							return yield* Effect.fail(
								new TuiProjectContextError({
									message: "No project selected. Use 'az project add' to register a project.",
								}),
							)
						}
						return project
					}),
				getCurrentPath: () =>
					Effect.gen(function* () {
						const project = yield* SubscriptionRef.get(currentProject)
						return project?.path
					}),
				switchProject: (name: string) =>
					Effect.gen(function* () {
						const currentProjects = yield* SubscriptionRef.get(projects)
						const nextProject = currentProjects.find((project) => project.name === name)
						if (nextProject === undefined) {
							return yield* Effect.fail(
								new TuiProjectContextError({
									message: `Project not found: ${name}`,
								}),
							)
						}

						yield* SubscriptionRef.set(currentProject, nextProject)
						yield* persistSelection(name)
						return nextProject
					}),
			}

			return service
		}),
	},
) {}

export const getTuiProjectContextRead = Effect.gen(function* () {
	const projectContext = yield* TuiProjectContextService
	return {
		currentProject: projectContext.currentProject,
		projects: projectContext.projects,
		getCurrentPath: projectContext.getCurrentPath,
	} satisfies TuiProjectContextReadApi
})

export const getTuiCurrentProjectRef = getTuiProjectContextRead.pipe(
	Effect.map((projectContext) => projectContext.currentProject),
)

export const getTuiProjectsRef = getTuiProjectContextRead.pipe(
	Effect.map((projectContext) => projectContext.projects),
)
