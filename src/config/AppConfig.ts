/**
 * AppConfig - Effect service for application configuration
 *
 * Loads configuration from (in priority order):
 * 1. Explicit config path (--config flag)
 * 2. .azedarach.json in project root
 * 3. package.json under "azedarach" key
 * 4. Defaults
 *
 * Follows the service patterns established in BeadsClient.ts and ClaudeSessionManager.ts.
 */

import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Schema, Stream, SubscriptionRef } from "effect"
import { ProjectService } from "../services/ProjectService.js"
import { mergeWithDefaults, type ResolvedConfig } from "./defaults.js"
import { type AzedarachConfig, AzedarachConfigSchema } from "./schema.js"

// ============================================================================
// Error Types
// ============================================================================

/**
 * Generic configuration error
 */
export class ConfigError extends Data.TaggedError("ConfigError")<{
	readonly message: string
	readonly path?: string
}> {}

/**
 * Error when parsing configuration file fails
 */
export class ConfigParseError extends Data.TaggedError("ConfigParseError")<{
	readonly message: string
	readonly path: string
	readonly details?: string
}> {}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * AppConfig service interface
 *
 * Provides access to validated, resolved application configuration.
 * Config is reactive - changes when ProjectService's current project changes.
 * All fields are guaranteed to have values (defaults applied).
 */
export interface AppConfigService {
	/** The reactive configuration - updates when current project changes */
	readonly config: SubscriptionRef.SubscriptionRef<ResolvedConfig>

	/** Get CLI tool to use for AI sessions */
	readonly getCliTool: () => Effect.Effect<ResolvedConfig["cliTool"]>

	/** Get model configuration */
	readonly getModelConfig: () => Effect.Effect<ResolvedConfig["model"]>

	/** Get worktree configuration section */
	readonly getWorktreeConfig: () => Effect.Effect<ResolvedConfig["worktree"]>

	/** Get git configuration section */
	readonly getGitConfig: () => Effect.Effect<ResolvedConfig["git"]>

	/** Get session configuration section */
	readonly getSessionConfig: () => Effect.Effect<ResolvedConfig["session"]>

	/** Get patterns configuration section */
	readonly getPatternsConfig: () => Effect.Effect<ResolvedConfig["patterns"]>

	/** Get PR configuration section */
	readonly getPRConfig: () => Effect.Effect<ResolvedConfig["pr"]>

	/** Get merge configuration section */
	readonly getMergeConfig: () => Effect.Effect<ResolvedConfig["merge"]>

	/** Get notifications configuration section */
	readonly getNotificationsConfig: () => Effect.Effect<ResolvedConfig["notifications"]>

	/** Get beads configuration section */
	readonly getBeadsConfig: () => Effect.Effect<ResolvedConfig["beads"]>

	/** Get network configuration section */
	readonly getNetworkConfig: () => Effect.Effect<ResolvedConfig["network"]>

	/** Get devServer configuration section */
	readonly getDevServerConfig: () => Effect.Effect<ResolvedConfig["devServer"]>
}

export class AppConfigConfig extends Effect.Service<AppConfigConfig>()("AppConfig", {
	effect: (projectPath?: string, configPath?: string) =>
		Effect.succeed({
			configPath: configPath ?? null,
			projectPath: projectPath ?? process.cwd(),
		}),
}) {}

/**
 * AppConfig service
 *
 * Reactive configuration that updates when ProjectService's current project changes.
 * Uses scoped service pattern to manage the project change watcher fiber.
 */
export class AppConfig extends Effect.Service<AppConfig>()("AppConfig", {
	dependencies: [ProjectService.Default],
	scoped: Effect.gen(function* () {
		const pathService = yield* Path.Path
		const fs = yield* FileSystem.FileSystem
		const projectService = yield* ProjectService
		const { configPath } = yield* Effect.serviceOption(AppConfigConfig).pipe(
			Effect.map(
				Option.getOrElse(() => ({
					projectPath: process.cwd(),
					configPath: null as string | null,
				})),
			),
		)
		// ============================================================================
		// Config Loading Helpers
		// ============================================================================
		//
		// Note: Config migration is handled automatically by AzedarachConfigSchema
		// which uses Schema.transform to migrate legacy formats (e.g., pr.baseBranch → git.baseBranch)
		//

		/**
		 * Try to load .azedarach.json from project root
		 */
		const loadJsonConfig = (
			targetPath: string,
		): Effect.Effect<AzedarachConfig | null, ConfigParseError> =>
			Effect.gen(function* () {
				const targetConfigPath = pathService.join(targetPath, ".azedarach.json")

				const exists = yield* fs
					.exists(targetConfigPath)
					.pipe(Effect.catchAll(() => Effect.succeed(false)))
				if (!exists) {
					return null
				}

				const content = yield* fs.readFileString(targetConfigPath).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Failed to read config file",
								path: targetConfigPath,
								details: String(e),
							}),
					),
				)

				const json = yield* Effect.try({
					try: () => JSON.parse(content),
					catch: (e) =>
						new ConfigParseError({
							message: "Invalid JSON in config file",
							path: targetConfigPath,
							details: String(e),
						}),
				})

				// Schema.transform in AzedarachConfigSchema handles migration automatically
				const validated = yield* Schema.decodeUnknown(AzedarachConfigSchema)(json).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Config validation failed",
								path: targetConfigPath,
								details: String(e),
							}),
					),
				)

				return validated
			})

		/**
		 * Try to load config from package.json "azedarach" key
		 */
		const loadPackageJsonConfig = (
			targetPath: string,
		): Effect.Effect<AzedarachConfig | null, ConfigParseError> =>
			Effect.gen(function* () {
				const pkgPath = pathService.join(targetPath, "package.json")

				const exists = yield* fs.exists(pkgPath).pipe(Effect.catchAll(() => Effect.succeed(false)))
				if (!exists) {
					return null
				}

				const content = yield* fs.readFileString(pkgPath).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Failed to read package.json",
								path: pkgPath,
								details: String(e),
							}),
					),
				)

				const pkg = yield* Effect.try({
					try: () => JSON.parse(content),
					catch: () =>
						new ConfigParseError({
							message: "Invalid JSON in package.json",
							path: pkgPath,
						}),
				})

				// Check if azedarach key exists using schema validation
				const PackageJsonSchema = Schema.Struct({
					azedarach: Schema.optional(Schema.Unknown),
				})

				const pkgResult = yield* Schema.decodeUnknown(PackageJsonSchema)(pkg).pipe(
					Effect.catchAll(() => Effect.succeed({ azedarach: undefined })),
				)

				if (pkgResult.azedarach === undefined) {
					return null
				}

				// Schema.transform in AzedarachConfigSchema handles migration automatically
				const validated = yield* Schema.decodeUnknown(AzedarachConfigSchema)(
					pkgResult.azedarach,
				).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Config validation failed in package.json",
								path: pkgPath,
								details: String(e),
							}),
					),
				)

				return validated
			})

		/**
		 * Load config from environment variables
		 *
		 * Supports:
		 * - AZEDARACH_WORKTREE_INIT_COMMANDS (comma-separated)
		 * - AZEDARACH_SESSION_COMMAND
		 * - AZEDARACH_SESSION_SHELL
		 */
		const loadEnvConfig = (): AzedarachConfig => {
			const initCommandsEnv = process.env.AZEDARACH_WORKTREE_INIT_COMMANDS
			const sessionCommand = process.env.AZEDARACH_SESSION_COMMAND
			const sessionShell = process.env.AZEDARACH_SESSION_SHELL

			const worktree = initCommandsEnv
				? {
						initCommands: initCommandsEnv
							.split(",")
							.map((s) => s.trim())
							.filter(Boolean),
					}
				: undefined

			const session =
				sessionCommand || sessionShell
					? {
							...(sessionCommand && { command: sessionCommand }),
							...(sessionShell && { shell: sessionShell }),
						}
					: undefined

			return {
				...(worktree && { worktree }),
				...(session && { session }),
			}
		}

		// ============================================================================
		// Main Config Loader
		// ============================================================================

		/**
		 * Load configuration for a project path with fallback chain
		 *
		 * Priority: explicit configPath > .azedarach.json > package.json > env vars > defaults
		 */
		const loadConfigForPath = (
			projectPath: string,
		): Effect.Effect<ResolvedConfig, ConfigParseError> =>
			Effect.gen(function* () {
				// If explicit config path provided, use only that
				if (configPath) {
					const content = yield* fs.readFileString(configPath).pipe(
						Effect.mapError(
							() =>
								new ConfigParseError({
									message: "Failed to read config file",
									path: configPath,
								}),
						),
					)

					const json = yield* Effect.try({
						try: () => JSON.parse(content),
						catch: (e) =>
							new ConfigParseError({
								message: "Invalid JSON in config file",
								path: configPath,
								details: String(e),
							}),
					})

					// Schema.transform in AzedarachConfigSchema handles migration automatically
					const validated = yield* Schema.decodeUnknown(AzedarachConfigSchema)(json).pipe(
						Effect.mapError(
							(e) =>
								new ConfigParseError({
									message: "Config validation failed",
									path: configPath,
									details: String(e),
								}),
						),
					)

					return mergeWithDefaults(validated)
				}

				// Try .azedarach.json first
				const jsonConfig = yield* loadJsonConfig(projectPath).pipe(
					Effect.catchAll(() => Effect.succeed(null)),
				)

				if (jsonConfig) {
					return mergeWithDefaults(jsonConfig)
				}

				// Try package.json "azedarach" key
				const pkgConfig = yield* loadPackageJsonConfig(projectPath).pipe(
					Effect.catchAll(() => Effect.succeed(null)),
				)

				if (pkgConfig) {
					return mergeWithDefaults(pkgConfig)
				}

				// Fall back to env vars + defaults
				const envConfig = loadEnvConfig()
				return mergeWithDefaults(envConfig)
			})

		// ============================================================================
		// Reactive Config Setup
		// ============================================================================

		// Get initial project path from ProjectService
		const initialProjectPath = yield* projectService.getCurrentPath()
		const effectiveProjectPath = initialProjectPath ?? process.cwd()

		// Load initial config
		const initialConfig = yield* loadConfigForPath(effectiveProjectPath).pipe(
			Effect.catchAll(() => Effect.succeed(mergeWithDefaults({}))),
		)

		// Create reactive config ref
		const configRef = yield* SubscriptionRef.make<ResolvedConfig>(initialConfig)

		// Watch for project changes and reload config
		yield* Effect.forkScoped(
			projectService.currentProject.changes.pipe(
				Stream.runForEach((project) =>
					Effect.gen(function* () {
						const newProjectPath = project?.path ?? process.cwd()
						const newConfig = yield* loadConfigForPath(newProjectPath).pipe(
							Effect.catchAll(() => Effect.succeed(mergeWithDefaults({}))),
						)
						yield* SubscriptionRef.set(configRef, newConfig)
					}),
				),
			),
		)

		return {
			config: configRef,
			getCliTool: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.cliTool),
			getModelConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.model),
			getWorktreeConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.worktree),
			getGitConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.git),
			getSessionConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.session),
			getPatternsConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.patterns),
			getPRConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.pr),
			getMergeConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.merge),
			getNotificationsConfig: () =>
				Effect.map(SubscriptionRef.get(configRef), (c) => c.notifications),
			getBeadsConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.beads),
			getNetworkConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.network),
			getDevServerConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.devServer),
		}
	}),
}) {}

// /**
//  * Get the worktree configuration
//  */
// export const getWorktreeConfig = (): Effect.Effect<ResolvedConfig["worktree"], never, AppConfig> =>
// 	Effect.map(AppConfig, (service) => service.getWorktreeConfig())

// /**
//  * Get the session configuration
//  */
// export const getSessionConfig = (): Effect.Effect<ResolvedConfig["session"], never, AppConfig> =>
// 	Effect.map(AppConfig, (service) => service.getSessionConfig())

// /**
//  * Get the patterns configuration
//  */
// export const getPatternsConfig = (): Effect.Effect<ResolvedConfig["patterns"], never, AppConfig> =>
// 	Effect.map(AppConfig, (service) => service.getPatternsConfig())

// /**
//  * Get the PR configuration
//  */
// export const getPRConfig = (): Effect.Effect<ResolvedConfig["pr"], never, AppConfig> =>
// 	Effect.map(AppConfig, (service) => service.getPRConfig())

// /**
//  * Get the notifications configuration
//  */
// export const getNotificationsConfig = (): Effect.Effect<
// 	ResolvedConfig["notifications"],
// 	never,
// 	AppConfig
// > => Effect.map(AppConfig, (service) => service.getNotificationsConfig())
