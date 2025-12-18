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

import * as os from "node:os"
import * as path from "node:path"
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
	beadsPath: Schema.optional(Schema.String),
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
// Constants
// ============================================================================

const CONFIG_DIR = path.join(os.homedir(), ".config", "azedarach")
const PROJECTS_FILE = path.join(CONFIG_DIR, "projects.json")

// ============================================================================
// Service Implementation
// ============================================================================

export class ProjectService extends Effect.Service<ProjectService>()("ProjectService", {
	scoped: Effect.gen(function* () {
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		// ========================================================================
		// Config Loading/Saving
		// ========================================================================

		/**
		 * Load projects config from global file
		 */
		const loadProjectsConfig = (): Effect.Effect<ProjectsConfig, ProjectError> =>
			Effect.gen(function* () {
				const exists = yield* fs
					.exists(PROJECTS_FILE)
					.pipe(Effect.catchAll(() => Effect.succeed(false)))

				if (!exists) {
					return emptyProjectsConfig
				}

				const content = yield* fs.readFileString(PROJECTS_FILE).pipe(
					Effect.mapError(
						(e) =>
							new ProjectError({
								message: `Failed to read projects config: ${e}`,
							}),
					),
				)

				const json = yield* Effect.try({
					try: () => JSON.parse(content),
					catch: (e) =>
						new ProjectError({
							message: `Invalid JSON in projects config: ${e}`,
						}),
				})

				return yield* Schema.decodeUnknown(ProjectsConfigSchema)(json).pipe(
					Effect.mapError(
						(e) =>
							new ProjectError({
								message: `Projects config validation failed: ${e}`,
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
					.makeDirectory(CONFIG_DIR, { recursive: true })
					.pipe(Effect.catchAll(() => Effect.void))

				const content = JSON.stringify(config, null, 2)
				yield* fs.writeFileString(PROJECTS_FILE, content).pipe(
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
			Effect.catchAll(() => Effect.succeed(emptyProjectsConfig)),
		)

		// Create reactive state refs
		const projects = yield* SubscriptionRef.make<ReadonlyArray<Project>>(initialConfig.projects)
		const defaultProjectName = yield* SubscriptionRef.make<string | undefined>(
			initialConfig.defaultProject,
		)

		/**
		 * Determine initial project based on:
		 * 1. Check if cwd matches a registered project
		 * 2. Fall back to default project
		 * 3. Fall back to first project
		 * 4. Return undefined if no projects
		 */
		const determineInitialProject = (
			projectList: ReadonlyArray<Project>,
			defaultName: string | undefined,
		): Project | undefined => {
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

			// Fall back to default project
			if (defaultName) {
				const defaultProject = projectList.find((p) => p.name === defaultName)
				if (defaultProject) return defaultProject
			}

			// Fall back to first project
			return projectList[0]
		}

		const initialProject = determineInitialProject(
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
			getConfigPath: (): string => PROJECTS_FILE,
		}
	}),
}) {}
