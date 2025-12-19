/**
 * AppConfig - Effect service for application configuration
 *
 * Loads configuration from (in priority order):
 * 1. Explicit config path (--config flag)
 * 2. .azedarach.json in project root
 * 3. package.json under "azedarach" key
 * 4. Defaults
 *
 * Follows the service patterns established in BeadsClient.ts and SessionManager.ts.
 */

import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Schema } from "effect"
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
 * All fields are guaranteed to have values (defaults applied).
 */
export interface AppConfigService {
	/** The fully resolved configuration with all defaults applied */
	readonly config: ResolvedConfig

	/** Get worktree configuration section */
	readonly getWorktreeConfig: () => ResolvedConfig["worktree"]

	/** Get git configuration section */
	readonly getGitConfig: () => ResolvedConfig["git"]

	/** Get session configuration section */
	readonly getSessionConfig: () => ResolvedConfig["session"]

	/** Get patterns configuration section */
	readonly getPatternsConfig: () => ResolvedConfig["patterns"]

	/** Get PR configuration section */
	readonly getPRConfig: () => ResolvedConfig["pr"]

	/** Get merge configuration section */
	readonly getMergeConfig: () => ResolvedConfig["merge"]

	/** Get notifications configuration section */
	readonly getNotificationsConfig: () => ResolvedConfig["notifications"]

	/** Get beads configuration section */
	readonly getBeadsConfig: () => ResolvedConfig["beads"]

	/** Get network configuration section */
	readonly getNetworkConfig: () => ResolvedConfig["network"]
}

export class AppConfigConfig extends Effect.Service<AppConfigConfig>()("AppConfig", {
	effect: (projectPath?: string, configPath?: string) =>
		Effect.succeed({
			configPath: configPath ?? null,
			projectPath: projectPath ?? process.cwd(),
		}),
}) {}

/**
 * AppConfig service tag
 *
 * Note: AppConfig uses Context.Tag with factory functions (AppConfigLive)
 * because it requires runtime parameters (projectPath, configPath).
 * It cannot use Effect.Service pattern without additional indirection.
 */
export class AppConfig extends Effect.Service<AppConfig>()("AppConfig", {
	effect: Effect.gen(function* () {
		const path = yield* Path.Path
		const fs = yield* FileSystem.FileSystem
		const { projectPath, configPath } = yield* Effect.serviceOption(AppConfigConfig).pipe(
			Effect.map(
				Option.getOrElse(() => ({
					projectPath: process.cwd(),
					configPath: null,
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
			projectPath: string,
		): Effect.Effect<AzedarachConfig | null, ConfigParseError, FileSystem.FileSystem> =>
			Effect.gen(function* () {
				const configPath = path.join(projectPath, ".azedarach.json")

				const exists = yield* fs
					.exists(configPath)
					.pipe(Effect.catchAll(() => Effect.succeed(false)))
				if (!exists) {
					return null
				}

				const content = yield* fs.readFileString(configPath).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Failed to read config file",
								path: configPath,
								details: String(e),
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

				return validated
			})

		/**
		 * Try to load config from package.json "azedarach" key
		 */
		const loadPackageJsonConfig = (
			projectPath: string,
		): Effect.Effect<AzedarachConfig | null, ConfigParseError, FileSystem.FileSystem> =>
			Effect.gen(function* () {
				const pkgPath = path.join(projectPath, "package.json")

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
		 * Load configuration with fallback chain
		 *
		 * Priority: explicit path > .azedarach.json > package.json > env vars > defaults
		 *
		 * @param projectPath - Root directory of the project
		 * @param configPath - Optional explicit config file path (--config flag)
		 */
		const loadConfig = (): Effect.Effect<ResolvedConfig, ConfigParseError, FileSystem.FileSystem> =>
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

		const config = yield* loadConfig()

		return {
			config,
			getWorktreeConfig: () => config.worktree,
			getGitConfig: () => config.git,
			getSessionConfig: () => config.session,
			getPatternsConfig: () => config.patterns,
			getPRConfig: () => config.pr,
			getMergeConfig: () => config.merge,
			getNotificationsConfig: () => config.notifications,
			getBeadsConfig: () => config.beads,
			getNetworkConfig: () => config.network,
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
