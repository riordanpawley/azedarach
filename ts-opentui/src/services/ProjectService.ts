/**
 * ProjectService - Multi-project management for Azedarach
 *
 * Manages project registry and current project selection.
 * Projects are stored globally in ~/.config/azedarach/projects.json
 *
 * Key responsibilities:
 * - Load/save project registry from global config
 * - Track current active project via SubscriptionRef
 * - Provide project switching functionality
 * - Auto-select project based on cwd on startup
 */

import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Schema, SubscriptionRef } from "effect"

// ============================================================================
// Types
// ============================================================================

/**
 * Project definition schema
 */
const ProjectSchema = Schema.Struct({
	name: Schema.String,
	path: Schema.String,
	issueStorePath: Schema.optional(Schema.String),
})

export type Project = Schema.Schema.Type<typeof ProjectSchema>

/**
 * Global projects config schema
 */
const ProjectsConfigSchema = Schema.Struct({
	projects: Schema.Array(ProjectSchema),
	defaultProject: Schema.optional(Schema.String),
})

type ProjectsConfig = Schema.Schema.Type<typeof ProjectsConfigSchema>

/**
 * Empty projects config - typed at definition
 */
const emptyProjectsConfig: ProjectsConfig = {
	projects: [],
	defaultProject: undefined,
}

// ============================================================================
// Error Types
// ============================================================================

export class ProjectError extends Data.TaggedError("ProjectError")<{
	readonly message: string
}> {}

export class NoProjectsError extends Data.TaggedError("NoProjectsError")<{
	readonly message: string
}> {}

// ============================================================================
// Path Helpers
// ============================================================================

/**
 * Build config paths using the Path service
 * Returns { configDir, projectsFile }
 */
const getConfigPaths = (pathService: Path.Path) => {
	// Use process.env.HOME as homedir - this is standard and works across platforms
	const homedir = process.env.HOME || process.env.USERPROFILE || "~"
	const configDir = pathService.join(homedir, ".config", "azedarach")
	const projectsFile = pathService.join(configDir, "projects.json")
	return { configDir, projectsFile }
}

type WorktreePathOps = Pick<Path.Path, "basename" | "dirname" | "normalize">

/**
 * Check if cwdPath is a worktree path (or inside one) for projectPath.
 *
 * Worktrees are created as siblings: /path/to/project-branchname.
 * This matcher also handles subdirectories, e.g. /path/to/project-branchname/subdir.
 */
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

const parseGitdirPointer = (content: string): string | undefined => {
	const trimmed = content.trim()
	if (!trimmed.startsWith("gitdir:")) {
		return undefined
	}
	const pointer = trimmed.slice("gitdir:".length).trim()
	return pointer.length > 0 ? pointer : undefined
}

export const isWorktreeGitdirPointerForProject = (
	gitdirPointer: string,
	projectPath: string,
	pathOps: Pick<Path.Path, "join" | "normalize" | "sep">,
): boolean => {
	const normalizedGitdir = pathOps.normalize(gitdirPointer)
	const worktreesRoot = pathOps.normalize(pathOps.join(projectPath, ".git", "worktrees"))
	return (
		normalizedGitdir === worktreesRoot ||
		normalizedGitdir.startsWith(`${worktreesRoot}${pathOps.sep}`)
	)
}

/**
 * Resolve which base path should be used for loading project config.
 *
 * When running from a sibling worktree, always use the registered project root
 * so config/storage remains shared across all tracked worktrees.
 */
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

// ============================================================================
// Service Implementation
// ============================================================================

export class ProjectService extends Effect.Service<ProjectService>()("ProjectService", {
	scoped: Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		// Get config paths using the path service
		const { configDir, projectsFile } = getConfigPaths(pathService)

		// ========================================================================
		// Config Loading/Saving
		// ========================================================================

		/**
		 * Load projects config from global file
		 */
		const loadProjectsConfig = (): Effect.Effect<ProjectsConfig, ProjectError> =>
			Effect.gen(function* () {
				const exists = yield* fs
					.exists(projectsFile)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					)

				if (!exists) {
					return emptyProjectsConfig
				}

				const content = yield* fs.readFileString(projectsFile).pipe(
					Effect.mapError(
						(e) =>
							new ProjectError({
								message: `Failed to read projects config: ${e}`,
							}),
					),
				)

				return yield* Schema.decode(Schema.parseJson(ProjectsConfigSchema))(content).pipe(
					Effect.mapError(
						(e) =>
							new ProjectError({
								message: `Projects config parse/validation failed: ${e}`,
							}),
					),
				)
			})

		/**
		 * Save projects config to global file
		 */
		const saveProjectsConfig = (config: ProjectsConfig): Effect.Effect<void, ProjectError> =>
			Effect.gen(function* () {
				// Ensure config directory exists
				yield* fs
					.makeDirectory(configDir, { recursive: true })
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.void),
							),
						),
					)

				const content = JSON.stringify(config, null, 2)
				yield* fs.writeFileString(projectsFile, content).pipe(
					Effect.mapError(
						(e) =>
							new ProjectError({
								message: `Failed to write projects config: ${e}`,
							}),
					),
				)
			})

		// ========================================================================
		// State Initialization
		// ========================================================================

		// Load initial config
		const initialConfig = yield* loadProjectsConfig().pipe(
			Effect.catchAll((error) =>
				Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
					Effect.zipRight(Effect.succeed(emptyProjectsConfig)),
				),
			),
		)

		// Create reactive state refs
		const projects = yield* SubscriptionRef.make<ReadonlyArray<Project>>(initialConfig.projects)
		const defaultProjectName = yield* SubscriptionRef.make<string | undefined>(
			initialConfig.defaultProject,
		)

		const isTrackedGitWorktreeOf = (cwdPath: string, projectPath: string): Effect.Effect<boolean> =>
			Effect.gen(function* () {
				let candidate = pathService.normalize(cwdPath)
				while (true) {
					const gitPath = pathService.join(candidate, ".git")
					const gitPointer = yield* fs.readFileString(gitPath).pipe(
						Effect.map(parseGitdirPointer),
						Effect.catchAll(() => Effect.succeed(undefined)),
					)
					if (gitPointer !== undefined) {
						const absoluteGitdir = pathService.isAbsolute(gitPointer)
							? gitPointer
							: pathService.join(candidate, gitPointer)
						if (isWorktreeGitdirPointerForProject(absoluteGitdir, projectPath, pathService)) {
							return true
						}
					}

					const parent = pathService.dirname(candidate)
					if (parent === candidate) {
						return false
					}
					candidate = parent
				}
			})

		const findNearestAzedarachWorkspaceRoot = (
			startPath: string,
		): Effect.Effect<string | undefined> =>
			Effect.gen(function* () {
				let candidate = pathService.normalize(startPath)
				while (true) {
					const storageDir = pathService.join(candidate, ".azedarach")
					const legacyConfig = pathService.join(candidate, ".azedarach.json")
					const hasStorageDir = yield* fs.exists(storageDir).pipe(Effect.orElseSucceed(() => false))
					if (hasStorageDir) {
						return candidate
					}
					const hasLegacyConfig = yield* fs
						.exists(legacyConfig)
						.pipe(Effect.orElseSucceed(() => false))
					if (hasLegacyConfig) {
						return candidate
					}

					const parent = pathService.dirname(candidate)
					if (parent === candidate) {
						return undefined
					}
					candidate = parent
				}
			})

		/**
		 * Determine initial project based on:
		 * 1. Check if cwd matches a registered project
		 * 2. Check if cwd is inside a registered project
		 * 3. Check if cwd is a tracked git worktree of a registered project
		 * 4. Check if cwd looks like a standalone azedarach workspace
		 * 5. Fall back to default project
		 * 6. Fall back to first project
		 * 7. Return undefined if no projects
		 */
		const determineInitialProject = (
			projectList: ReadonlyArray<Project>,
			defaultName: string | undefined,
		): Effect.Effect<Project | undefined> =>
			Effect.gen(function* () {
				const cwd = process.cwd()

				// Check if cwd matches a registered project
				const cwdProject = projectList.find(
					(p) => pathService.normalize(p.path) === pathService.normalize(cwd),
				)
				if (cwdProject) return cwdProject

				// Check if cwd is inside a registered project
				const parentProject = projectList.find((p) =>
					cwd.startsWith(pathService.normalize(p.path) + pathService.sep),
				)
				if (parentProject) return parentProject

				// Check if cwd is a tracked git worktree of a registered project.
				for (const project of projectList) {
					const isTrackedWorktree = yield* isTrackedGitWorktreeOf(cwd, project.path)
					const registeredProjectRoot = resolveRegisteredProjectRootForWorktree({
						cwdPath: cwd,
						projectPath: project.path,
						pathOps: pathService,
						isTrackedGitWorktree: isTrackedWorktree,
					})
					if (registeredProjectRoot !== undefined) {
						return project
					}
				}

				// If cwd looks like a standalone Azedarach project clone/workspace,
				// prefer it over a registry default from another sibling repo.
				const workspaceRoot = yield* findNearestAzedarachWorkspaceRoot(cwd)
				if (workspaceRoot !== undefined) {
					return {
						name: pathService.basename(workspaceRoot),
						path: workspaceRoot,
					}
				}

				// Fall back to default project
				if (defaultName) {
					const defaultProject = projectList.find((p) => p.name === defaultName)
					if (defaultProject) return defaultProject
				}

				// Fall back to first project
				return projectList[0]
			})

		const initialProject = yield* determineInitialProject(
			initialConfig.projects,
			initialConfig.defaultProject,
		)
		const currentProject = yield* SubscriptionRef.make<Project | undefined>(initialProject)

		// ========================================================================
		// Service Methods
		// ========================================================================

		/**
		 * Persist current state to config file
		 */
		const persistConfig = (): Effect.Effect<void, ProjectError> =>
			Effect.gen(function* () {
				const projectList = yield* SubscriptionRef.get(projects)
				const defaultName = yield* SubscriptionRef.get(defaultProjectName)

				yield* saveProjectsConfig({
					projects: [...projectList],
					defaultProject: defaultName,
				})
			})

		return {
			// Expose SubscriptionRefs for atom subscription
			currentProject,
			projects,

			/**
			 * Get current project path, or undefined if no project selected
			 */
			getCurrentPath: (): Effect.Effect<string | undefined> =>
				Effect.gen(function* () {
					const project = yield* SubscriptionRef.get(currentProject)
					return project?.path
				}),

			/**
			 * Get current project, failing if none selected
			 */
			requireCurrentProject: (): Effect.Effect<Project, NoProjectsError> =>
				Effect.gen(function* () {
					const project = yield* SubscriptionRef.get(currentProject)
					if (!project) {
						return yield* Effect.fail(
							new NoProjectsError({
								message: "No project selected. Use 'az project add' to register a project.",
							}),
						)
					}
					return project
				}),

			/**
			 * Get all registered projects
			 */
			getProjects: (): Effect.Effect<ReadonlyArray<Project>> => SubscriptionRef.get(projects),

			/**
			 * Switch to a different project by name
			 */
			switchProject: (name: string): Effect.Effect<void, ProjectError> =>
				Effect.gen(function* () {
					const projectList = yield* SubscriptionRef.get(projects)
					const project = projectList.find((p) => p.name === name)

					if (!project) {
						return yield* Effect.fail(
							new ProjectError({
								message: `Project not found: ${name}`,
							}),
						)
					}

					yield* SubscriptionRef.set(currentProject, project)
				}),

			/**
			 * Add a new project to the registry
			 */
			addProject: (project: Project): Effect.Effect<void, ProjectError> =>
				Effect.gen(function* () {
					const projectList = yield* SubscriptionRef.get(projects)

					// Check for duplicate name
					if (projectList.some((p) => p.name === project.name)) {
						return yield* Effect.fail(
							new ProjectError({
								message: `Project with name '${project.name}' already exists`,
							}),
						)
					}

					// Check for duplicate path
					if (
						projectList.some(
							(p) => pathService.normalize(p.path) === pathService.normalize(project.path),
						)
					) {
						return yield* Effect.fail(
							new ProjectError({
								message: `Project with path '${project.path}' already exists`,
							}),
						)
					}

					// Add project
					yield* SubscriptionRef.update(projects, (list) => [...list, project])

					// If this is the first project, set it as current
					const current = yield* SubscriptionRef.get(currentProject)
					if (!current) {
						yield* SubscriptionRef.set(currentProject, project)
					}

					// Persist
					yield* persistConfig()
				}),

			/**
			 * Remove a project from the registry
			 */
			removeProject: (name: string): Effect.Effect<void, ProjectError> =>
				Effect.gen(function* () {
					const projectList = yield* SubscriptionRef.get(projects)

					if (!projectList.some((p) => p.name === name)) {
						return yield* Effect.fail(
							new ProjectError({
								message: `Project not found: ${name}`,
							}),
						)
					}

					// Remove project
					yield* SubscriptionRef.update(projects, (list) => list.filter((p) => p.name !== name))

					// If removed project was current, switch to first remaining
					const current = yield* SubscriptionRef.get(currentProject)
					if (current?.name === name) {
						const remaining = yield* SubscriptionRef.get(projects)
						yield* SubscriptionRef.set(currentProject, remaining[0])
					}

					// Clear default if it was the removed project
					const defaultName = yield* SubscriptionRef.get(defaultProjectName)
					if (defaultName === name) {
						yield* SubscriptionRef.set(defaultProjectName, undefined)
					}

					// Persist
					yield* persistConfig()
				}),

			/**
			 * Set the default project
			 */
			setDefaultProject: (name: string): Effect.Effect<void, ProjectError> =>
				Effect.gen(function* () {
					const projectList = yield* SubscriptionRef.get(projects)

					if (!projectList.some((p) => p.name === name)) {
						return yield* Effect.fail(
							new ProjectError({
								message: `Project not found: ${name}`,
							}),
						)
					}

					yield* SubscriptionRef.set(defaultProjectName, name)
					yield* persistConfig()
				}),

			/**
			 * Get the config file path (for display/debugging)
			 */
			getConfigPath: (): string => projectsFile,
		}
	}),
}) {}
