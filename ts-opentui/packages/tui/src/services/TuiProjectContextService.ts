import {
	type AzedarachConfig,
	AzedarachConfigSchema,
	getProjectStoragePaths,
	resolveConfigBasePath,
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

const PROJECT_REGISTRY_FILENAME = "projects.json"

const getConfiguredProjects = (config: AzedarachConfig): ReadonlyArray<RegisteredProjectConfig> =>
	config.projects ?? []

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

			const saveRegistryConfig = (configPath: string, config: AzedarachConfig) =>
				Effect.gen(function* () {
					const encoded = yield* Schema.encode(Schema.parseJson(AzedarachConfigSchema))(
						config,
					).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Failed to encode registry config: ${String(error)}`,
								}),
						),
					)
					yield* fs.makeDirectory(pathService.dirname(configPath), { recursive: true }).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Failed to create registry directory ${pathService.dirname(configPath)}: ${String(error)}`,
								}),
						),
					)
					yield* fs.writeFileString(configPath, encoded).pipe(
						Effect.mapError(
							(error) =>
								new TuiProjectContextError({
									message: `Failed to write registry file ${configPath}: ${String(error)}`,
								}),
						),
					)
				})

			const loadConfigIfExists = (configPath: string) =>
				Effect.gen(function* () {
					const exists = yield* fs.exists(configPath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						return undefined
					}
					return yield* loadWritableConfig(configPath)
				})

			const resolveUserProjectRegistryPath = Effect.succeed(
				pathService.join(
					process.env.XDG_CONFIG_HOME ??
						(process.env.HOME
							? pathService.join(process.env.HOME, ".config")
							: pathService.join(process.cwd(), ".config")),
					"azedarach",
					PROJECT_REGISTRY_FILENAME,
				),
			)

			const loadProjectRegistryConfig = Effect.gen(function* () {
				const registryConfigPath = yield* resolveUserProjectRegistryPath
				return {
					configPath: registryConfigPath,
					config:
						(yield* loadConfigIfExists(registryConfigPath)) ??
						(yield* Schema.decodeUnknown(AzedarachConfigSchema)({}).pipe(
							Effect.mapError(
								(error) =>
									new TuiProjectContextError({
										message: `Failed to create default registry snapshot: ${String(error)}`,
									}),
							),
						)),
				} as const
			})

			const resolveWorkspaceConfigPath = (cwdPath: string) =>
				Effect.gen(function* () {
					const storagePaths = getProjectStoragePaths(cwdPath, pathService)
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
			const registrySnapshot = yield* loadProjectRegistryConfig
			const workspaceConfigPath = yield* resolveWorkspaceConfigPath(cwdPath)
			const workspaceConfig = yield* loadConfigIfExists(workspaceConfigPath)
			const initialConfig =
				getConfiguredProjects(registrySnapshot.config).length > 0
					? registrySnapshot.config
					: (workspaceConfig ?? registrySnapshot.config)
			const initialProjects = getConfiguredProjects(initialConfig)
			const currentProject = yield* SubscriptionRef.make<Project | undefined>(
				determineInitialProject(initialProjects, initialConfig.defaultProject, cwdPath),
			)
			const projects = yield* SubscriptionRef.make<ReadonlyArray<Project>>(initialProjects)

			const persistSelection = (projectName: string) =>
				Effect.gen(function* () {
					const currentRegistryConfig = yield* loadWritableConfig(registrySnapshot.configPath)
					yield* saveRegistryConfig(registrySnapshot.configPath, {
						...currentRegistryConfig,
						projects: [...(currentRegistryConfig.projects ?? [])],
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
