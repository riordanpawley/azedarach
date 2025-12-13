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

import { Effect, Context, Layer, Data } from "effect"
import * as Schema from "effect/Schema"
import { FileSystem } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import * as path from "node:path"
import { AzedarachConfigSchema, type AzedarachConfig } from "./schema.js"
import { mergeWithDefaults, type ResolvedConfig } from "./defaults.js"

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

  /** Get session configuration section */
  readonly getSessionConfig: () => ResolvedConfig["session"]

  /** Get patterns configuration section */
  readonly getPatternsConfig: () => ResolvedConfig["patterns"]

  /** Get PR configuration section */
  readonly getPRConfig: () => ResolvedConfig["pr"]

  /** Get notifications configuration section */
  readonly getNotificationsConfig: () => ResolvedConfig["notifications"]
}

/**
 * AppConfig service tag
 */
export class AppConfig extends Context.Tag("AppConfig")<AppConfig, AppConfigService>() {}

// ============================================================================
// Config Loading Helpers
// ============================================================================

/**
 * Try to load .azedarach.json from project root
 */
const loadJsonConfig = (
  projectPath: string
): Effect.Effect<AzedarachConfig | null, ConfigParseError, FileSystem.FileSystem> =>
  Effect.gen(function* () {
    const fs = yield* FileSystem.FileSystem
    const configPath = path.join(projectPath, ".azedarach.json")

    const exists = yield* fs.exists(configPath).pipe(
      Effect.catchAll(() => Effect.succeed(false))
    )
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
          })
      )
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

    const validated = yield* Schema.decodeUnknown(AzedarachConfigSchema)(json).pipe(
      Effect.mapError(
        (e) =>
          new ConfigParseError({
            message: "Config validation failed",
            path: configPath,
            details: String(e),
          })
      )
    )

    return validated
  })

/**
 * Try to load config from package.json "azedarach" key
 */
const loadPackageJsonConfig = (
  projectPath: string
): Effect.Effect<AzedarachConfig | null, ConfigParseError, FileSystem.FileSystem> =>
  Effect.gen(function* () {
    const fs = yield* FileSystem.FileSystem
    const pkgPath = path.join(projectPath, "package.json")

    const exists = yield* fs.exists(pkgPath).pipe(
      Effect.catchAll(() => Effect.succeed(false))
    )
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
          })
      )
    )

    const pkg = yield* Effect.try({
      try: () => JSON.parse(content) as { azedarach?: unknown },
      catch: () =>
        new ConfigParseError({
          message: "Invalid JSON in package.json",
          path: pkgPath,
        }),
    })

    if (!pkg.azedarach) {
      return null
    }

    const validated = yield* Schema.decodeUnknown(AzedarachConfigSchema)(pkg.azedarach).pipe(
      Effect.mapError(
        (e) =>
          new ConfigParseError({
            message: "Config validation failed in package.json",
            path: pkgPath,
            details: String(e),
          })
      )
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
export const loadConfig = (
  projectPath: string,
  configPath?: string
): Effect.Effect<ResolvedConfig, ConfigParseError, FileSystem.FileSystem> =>
  Effect.gen(function* () {
    // If explicit config path provided, use only that
    if (configPath) {
      const fs = yield* FileSystem.FileSystem
      const content = yield* fs.readFileString(configPath).pipe(
        Effect.mapError(
          () =>
            new ConfigParseError({
              message: "Failed to read config file",
              path: configPath,
            })
        )
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

      const validated = yield* Schema.decodeUnknown(AzedarachConfigSchema)(json).pipe(
        Effect.mapError(
          (e) =>
            new ConfigParseError({
              message: "Config validation failed",
              path: configPath,
              details: String(e),
            })
        )
      )

      return mergeWithDefaults(validated)
    }

    // Try .azedarach.json first
    const jsonConfig = yield* loadJsonConfig(projectPath).pipe(
      Effect.catchAll(() => Effect.succeed(null))
    )

    if (jsonConfig) {
      return mergeWithDefaults(jsonConfig)
    }

    // Try package.json "azedarach" key
    const pkgConfig = yield* loadPackageJsonConfig(projectPath).pipe(
      Effect.catchAll(() => Effect.succeed(null))
    )

    if (pkgConfig) {
      return mergeWithDefaults(pkgConfig)
    }

    // Fall back to env vars + defaults
    const envConfig = loadEnvConfig()
    return mergeWithDefaults(envConfig)
  })

// ============================================================================
// Layer Factories
// ============================================================================

/**
 * Create AppConfig layer for a specific project path
 *
 * This is the primary way to create the AppConfig layer. The projectPath
 * and optional configPath are captured at layer creation time.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const appConfig = yield* AppConfig
 *   const { initCommands } = appConfig.getWorktreeConfig()
 *   // Use initCommands...
 * }).pipe(Effect.provide(AppConfigLive("/path/to/project")))
 * ```
 */
export const AppConfigLive = (
  projectPath: string,
  configPath?: string
): Layer.Layer<AppConfig, ConfigParseError, FileSystem.FileSystem> =>
  Layer.effect(
    AppConfig,
    Effect.gen(function* () {
      const config = yield* loadConfig(projectPath, configPath)

      return AppConfig.of({
        config,
        getWorktreeConfig: () => config.worktree,
        getSessionConfig: () => config.session,
        getPatternsConfig: () => config.patterns,
        getPRConfig: () => config.pr,
        getNotificationsConfig: () => config.notifications,
      })
    })
  )

/**
 * AppConfig layer with BunContext for file system operations
 *
 * Use this in applications where you want a self-contained layer
 * that includes all platform dependencies.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const appConfig = yield* AppConfig
 *   // ...
 * }).pipe(Effect.provide(AppConfigLiveWithPlatform("/path/to/project")))
 * ```
 */
export const AppConfigLiveWithPlatform = (
  projectPath: string,
  configPath?: string
): Layer.Layer<AppConfig, ConfigParseError, never> =>
  AppConfigLive(projectPath, configPath).pipe(Layer.provide(BunContext.layer))

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Get the worktree configuration
 */
export const getWorktreeConfig = (): Effect.Effect<
  ResolvedConfig["worktree"],
  never,
  AppConfig
> => Effect.map(AppConfig, (service) => service.getWorktreeConfig())

/**
 * Get the session configuration
 */
export const getSessionConfig = (): Effect.Effect<
  ResolvedConfig["session"],
  never,
  AppConfig
> => Effect.map(AppConfig, (service) => service.getSessionConfig())

/**
 * Get the patterns configuration
 */
export const getPatternsConfig = (): Effect.Effect<
  ResolvedConfig["patterns"],
  never,
  AppConfig
> => Effect.map(AppConfig, (service) => service.getPatternsConfig())

/**
 * Get the PR configuration
 */
export const getPRConfig = (): Effect.Effect<ResolvedConfig["pr"], never, AppConfig> =>
  Effect.map(AppConfig, (service) => service.getPRConfig())

/**
 * Get the notifications configuration
 */
export const getNotificationsConfig = (): Effect.Effect<
  ResolvedConfig["notifications"],
  never,
  AppConfig
> => Effect.map(AppConfig, (service) => service.getNotificationsConfig())
