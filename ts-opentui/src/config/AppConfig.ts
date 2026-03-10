/**
 * AppConfig - Effect service for application configuration
 *
 * Loads configuration from (in priority order):
 * 1. Explicit config path (--config flag)
 * 2. project config file (.azedarach/config.json, with legacy .azedarach.json fallback)
 * 3. package.json under "azedarach" key
 * 4. Defaults
 *
 * Follows the service patterns established in IssueTrackerClient.ts and SessionManager.ts.
 */

import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Option, Ref, Schema, Stream, SubscriptionRef } from "effect"
import { getProjectStoragePaths, resolveConfigSchemaPath } from "../core/storagePaths.js"
import { ProjectService, resolveConfigBasePath } from "../services/ProjectService.js"
import { ToastService } from "../services/ToastService.js"
import { mergeWithDefaults, type ResolvedConfig } from "./defaults.js"
import { type AzedarachConfig, AzedarachConfigJsonSchema, AzedarachConfigSchema } from "./schema.js"

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

const isJsonObject = (value: unknown): value is Readonly<Record<string, unknown>> =>
	typeof value === "object" && value !== null && !Array.isArray(value)

const canonicalizeJson = (value: unknown): unknown => {
	if (Array.isArray(value)) {
		return value.map((item) => canonicalizeJson(item))
	}

	if (isJsonObject(value)) {
		const sortedEntries = Object.entries(value).sort(([left], [right]) => left.localeCompare(right))
		const normalized: Record<string, unknown> = {}
		for (const [key, entryValue] of sortedEntries) {
			normalized[key] = canonicalizeJson(entryValue)
		}
		return normalized
	}

	return value
}

const shouldPersistMigratedConfig = (rawConfig: unknown, migratedConfig: unknown): boolean =>
	JSON.stringify(canonicalizeJson(rawConfig)) !== JSON.stringify(canonicalizeJson(migratedConfig))

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

	/** Most recent config parse/validation warning when fallback defaults are active */
	readonly loadWarning: SubscriptionRef.SubscriptionRef<ConfigParseError | null>

	/** Path of config source currently loaded into `config` (null means env/default fallback) */
	readonly loadedConfigPath: SubscriptionRef.SubscriptionRef<string | null>

	/** Reload config from disk for current project */
	readonly reload: () => Effect.Effect<void, ConfigParseError>

	/** Get CLI tool to use for AI sessions */
	readonly getCliTool: () => Effect.Effect<ResolvedConfig["cliTool"]>

	/** Get model configuration */
	readonly getModelConfig: () => Effect.Effect<ResolvedConfig["model"]>

	/** Get worktree configuration section */
	readonly getWorktreeConfig: () => Effect.Effect<ResolvedConfig["worktree"]>

	/** Get git configuration section */
	readonly getGitConfig: () => Effect.Effect<ResolvedConfig["git"]>

	/** Get spec configuration section */
	readonly getSpecConfig: () => Effect.Effect<ResolvedConfig["spec"]>

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

	/** Get TUI issue editor configuration section */
	readonly getIssueEditorConfig: () => Effect.Effect<ResolvedConfig["issueEditor"]>

	/** Get effective issue backend configuration (tracker + sync flag) */
	readonly getIssueTrackerSyncConfig: () => Effect.Effect<{
		readonly issueTracker: ResolvedConfig["issueTracker"]
		readonly syncEnabled: boolean
	}>

	/** Get issue backend configuration for an explicit project path (non-reactive, path-scoped load) */
	readonly getIssueTrackerSyncConfigForProjectPath: (projectPath: string) => Effect.Effect<
		{
			readonly issueTracker: ResolvedConfig["issueTracker"]
			readonly syncEnabled: boolean
		},
		ConfigParseError
	>

	/** Get spec configuration for an explicit project path (non-reactive, path-scoped load) */
	readonly getSpecConfigForProjectPath: (
		projectPath: string,
	) => Effect.Effect<ResolvedConfig["spec"], ConfigParseError>

	/** Get network configuration section */
	readonly getNetworkConfig: () => Effect.Effect<ResolvedConfig["network"]>

	/** Get devServer configuration section */
	readonly getDevServerConfig: () => Effect.Effect<ResolvedConfig["devServer"]>

	/** Get keyboard configuration section */
	readonly getKeyboardConfig: () => Effect.Effect<ResolvedConfig["keyboard"]>

	/** Get session recovery configuration section */
	readonly getSessionRecoveryConfig: () => Effect.Effect<ResolvedConfig["sessionRecovery"]>

	/** Get hooks configuration section */
	readonly getHooksConfig: () => Effect.Effect<ResolvedConfig["hooks"]>

	/** Get workflow mode ('local' or 'origin') */
	readonly getWorkflowMode: () => Effect.Effect<ResolvedConfig["git"]["workflowMode"]>

	/** Get effective base branch for diffs/conflicts (adds origin/ prefix in origin mode) */
	readonly getEffectiveBaseBranch: () => Effect.Effect<string>
}

export class AppConfigConfig extends Effect.Service<AppConfigConfig>()("AppConfigConfig", {
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
	dependencies: [ProjectService.Default, ToastService.Default],
	scoped: Effect.gen(function* () {
		const pathService = yield* Path.Path
		const fs = yield* FileSystem.FileSystem
		const projectService = yield* ProjectService
		const toast = yield* ToastService
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

		const formatConfigJson = (config: unknown): string => `${JSON.stringify(config, null, 2)}\n`
		const configJsonSchemaString = `${JSON.stringify(AzedarachConfigJsonSchema, null, 2)}\n`

		const loadWarningRef = yield* SubscriptionRef.make<ConfigParseError | null>(null)
		const loadedConfigPathRef = yield* SubscriptionRef.make<string | null>(null)
		const lastToastWarningRef = yield* Ref.make<string | null>(null)

		const configWarningKey = (warning: ConfigParseError): string =>
			`${warning.path}:${warning.message}:${warning.details ?? ""}`

		const showConfigFallbackToast = (warning: ConfigParseError): Effect.Effect<void> =>
			Effect.gen(function* () {
				const key = configWarningKey(warning)
				const lastKey = yield* Ref.get(lastToastWarningRef)
				if (lastKey === key) return
				yield* Ref.set(lastToastWarningRef, key)
				yield* toast.show(
					"error",
					`Config parse failed at ${warning.path}; using fallback defaults. Open settings (e) to fix.`,
				)
			})

		const readJsonConfigAtPath = (
			targetConfigPath: string,
		): Effect.Effect<
			{ readonly resolved: ResolvedConfig; readonly encoded: unknown; readonly raw: unknown },
			ConfigParseError
		> =>
			Effect.gen(function* () {
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

				const json = yield* Schema.decode(Schema.parseJson(Schema.Unknown))(content).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Invalid JSON in config file",
								path: targetConfigPath,
								details: String(e),
							}),
					),
				)

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

				const resolved = mergeWithDefaults(validated)
				const normalizedForEncode = yield* Schema.decodeUnknown(AzedarachConfigSchema)(
					resolved,
				).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Config normalization failed",
								path: targetConfigPath,
								details: String(e),
							}),
					),
				)

				const encoded = yield* Schema.encode(AzedarachConfigSchema)(normalizedForEncode).pipe(
					Effect.mapError(
						(e) =>
							new ConfigParseError({
								message: "Config encoding failed",
								path: targetConfigPath,
								details: String(e),
							}),
					),
				)

				return { resolved, encoded, raw: json }
			})

		/**
		 * Try to load project config, preferring the canonical .azedarach/config.json path
		 * and falling back to legacy .azedarach.json when needed.
		 */
		const loadJsonConfig = (
			targetPath: string,
		): Effect.Effect<
			{ readonly config: ResolvedConfig; readonly loadedConfigPath: string } | null,
			ConfigParseError
		> =>
			Effect.gen(function* () {
				const storagePaths = getProjectStoragePaths(targetPath, pathService)
				const canonicalConfigExists = yield* fs
					.exists(storagePaths.canonicalConfigPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					)
				const legacyConfigExists = canonicalConfigExists
					? false
					: yield* fs
							.exists(storagePaths.legacyConfigPath)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.succeed(false)),
									),
								),
							)

				if (!canonicalConfigExists && !legacyConfigExists) {
					return null
				}

				const sourceConfigPath = canonicalConfigExists
					? storagePaths.canonicalConfigPath
					: storagePaths.legacyConfigPath
				const decoded = yield* readJsonConfigAtPath(sourceConfigPath).pipe(
					Effect.tap(({ resolved }) =>
						Effect.log(
							`[DEBUG] Loaded config path=${sourceConfigPath} cliTool=${resolved.cliTool}`,
						),
					),
				)

				const canonicalTargetPath = storagePaths.canonicalConfigPath
				const shouldPersistCanonical =
					sourceConfigPath !== canonicalTargetPath ||
					shouldPersistMigratedConfig(decoded.raw, decoded.encoded)

				if (shouldPersistCanonical) {
					yield* fs
						.makeDirectory(storagePaths.storageDirectory, { recursive: true })
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(
									`Failed to create config directory at ${storagePaths.storageDirectory}: ${String(error)}`,
								),
							),
						)
					yield* fs.writeFileString(canonicalTargetPath, formatConfigJson(decoded.encoded)).pipe(
						Effect.tap(() =>
							Effect.log(`[DEBUG] Persisted canonical config to ${canonicalTargetPath}`),
						),
						Effect.catchAll((error) =>
							Effect.logWarning(
								`Failed to persist canonical config at ${canonicalTargetPath}: ${String(error)}`,
							),
						),
					)
					const schemaPath = resolveConfigSchemaPath(canonicalTargetPath, pathService)
					yield* fs
						.writeFileString(schemaPath, configJsonSchemaString)
						.pipe(
							Effect.catchAll((error) =>
								Effect.logWarning(
									`Failed to persist config JSON schema at ${schemaPath}: ${String(error)}`,
								),
							),
						)
				}

				return {
					config: decoded.resolved,
					loadedConfigPath: shouldPersistCanonical ? canonicalTargetPath : sourceConfigPath,
				}
			})

		/**
		 * Try to load config from package.json "azedarach" key
		 */
		const loadPackageJsonConfig = (
			targetPath: string,
		): Effect.Effect<AzedarachConfig | null, ConfigParseError> =>
			Effect.gen(function* () {
				const pkgPath = pathService.join(targetPath, "package.json")

				const exists = yield* fs
					.exists(pkgPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
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
							}),
					),
				)

				const pkg = yield* Schema.decode(Schema.parseJson(Schema.Unknown))(content).pipe(
					Effect.mapError(
						() =>
							new ConfigParseError({
								message: "Invalid JSON in package.json",
								path: pkgPath,
							}),
					),
				)

				// Check if azedarach key exists using schema validation
				const PackageJsonSchema = Schema.Struct({
					azedarach: Schema.optional(Schema.Unknown),
				})

				const pkgResult = yield* Schema.decodeUnknown(PackageJsonSchema)(pkg).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(error).pipe(
							Effect.zipRight(Effect.succeed({ azedarach: undefined })),
						),
					),
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
		 * Priority: explicit configPath > project config file > package.json > env vars > defaults
		 */
		const loadConfigForPath = (
			projectPath: string,
		): Effect.Effect<
			{ readonly config: ResolvedConfig; readonly loadedConfigPath: string | null },
			ConfigParseError
		> =>
			Effect.gen(function* () {
				// If explicit config path provided, use only that
				if (configPath) {
					const json = yield* fs.readFileString(configPath).pipe(
						Effect.mapError(
							() =>
								new ConfigParseError({
									message: "Failed to read config file",
									path: configPath,
								}),
						),
					)

					// Schema.transform in AzedarachConfigSchema handles migration automatically
					const validated = yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(
						json,
					).pipe(
						Effect.mapError(
							(e) =>
								new ConfigParseError({
									message: "Config validation failed",
									path: configPath,
									details: String(e),
								}),
						),
					)

					return { config: mergeWithDefaults(validated), loadedConfigPath: configPath }
				}

				const cwdPath = process.cwd()
				const cwdStoragePaths = getProjectStoragePaths(cwdPath, pathService)
				const cwdHasCanonicalConfig = yield* fs
					.exists(cwdStoragePaths.canonicalConfigPath)
					.pipe(
						Effect.catchAll((error) =>
							Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
								Effect.zipRight(Effect.succeed(false)),
							),
						),
					)
				const cwdHasLegacyConfig = cwdHasCanonicalConfig
					? false
					: yield* fs
							.exists(cwdStoragePaths.legacyConfigPath)
							.pipe(
								Effect.catchAll((error) =>
									Effect.logWarning(`Recovering after caught error: ${String(error)}`).pipe(
										Effect.zipRight(Effect.succeed(false)),
									),
								),
							)
				const configBasePath = resolveConfigBasePath({
					cwdPath,
					projectPath,
					pathOps: pathService,
					cwdHasConfig: cwdHasCanonicalConfig || cwdHasLegacyConfig,
				})

				// Try project config file first
				const jsonConfig = yield* loadJsonConfig(configBasePath).pipe(
					Effect.catchAll((warning) =>
						Effect.logWarning(
							`[DEBUG] Failed to load project config for projectPath=${configBasePath}: ${warning.message} (path=${warning.path}${warning.details ? ` details=${warning.details}` : ""})`,
						).pipe(
							Effect.zipRight(SubscriptionRef.set(loadWarningRef, warning)),
							Effect.zipRight(showConfigFallbackToast(warning)),
							Effect.as(null),
						),
					),
				)

				if (jsonConfig) {
					yield* SubscriptionRef.set(loadWarningRef, null)
					yield* Effect.log(
						`[DEBUG] Loaded resolved project config: cliTool=${jsonConfig.config.cliTool}`,
					)
					return {
						config: jsonConfig.config,
						loadedConfigPath: jsonConfig.loadedConfigPath,
					}
				}

				// Try package.json "azedarach" key
				const pkgConfig = yield* loadPackageJsonConfig(configBasePath).pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(
							`[DEBUG] Failed to load package.json azedarach config for projectPath=${configBasePath}: ${error.message} (path=${error.path}${error.details ? ` details=${error.details}` : ""})`,
						).pipe(Effect.as(null)),
					),
				)

				if (pkgConfig) {
					yield* SubscriptionRef.set(loadWarningRef, null)
					return {
						config: mergeWithDefaults(pkgConfig),
						loadedConfigPath: pathService.join(configBasePath, "package.json"),
					}
				}

				// Fall back to env vars + defaults
				const envConfig = loadEnvConfig()
				yield* SubscriptionRef.set(loadWarningRef, null)
				return { config: mergeWithDefaults(envConfig), loadedConfigPath: null }
			})

		// ============================================================================
		// Reactive Config Setup
		// ============================================================================

		const getIssueBackendSyncEnabled = (config: ResolvedConfig): boolean => {
			if ("tracker" in config.issueTracker) {
				return config.issueTracker.tracker.syncEnabled
			}
			if ("legacy" in config.issueTracker) {
				return config.issueTracker.legacy.syncEnabled
			}
			if ("linear" in config.issueTracker) {
				return config.issueTracker.linear.syncEnabled
			}
			return config.issueTracker.local.syncEnabled
		}

		// Get initial project path from ProjectService
		const initialProjectPath = yield* projectService.getCurrentPath()
		const effectiveProjectPath = initialProjectPath ?? process.cwd()

		// Load initial config
		yield* Effect.log(`[DEBUG] Loading initial config from: ${effectiveProjectPath}`)
		const initialLoaded = yield* loadConfigForPath(effectiveProjectPath).pipe(
			Effect.tap((loaded) =>
				Effect.log(`[DEBUG] Initial config loaded: cliTool=${loaded.config.cliTool}`),
			),
			Effect.catchAll((e) => {
				if (e._tag === "ConfigParseError") {
					return SubscriptionRef.set(loadWarningRef, e).pipe(
						Effect.zipRight(showConfigFallbackToast(e)),
						Effect.zipRight(Effect.log(`[DEBUG] Initial config load failed: ${e}`)),
						Effect.map(() => ({ config: mergeWithDefaults({}), loadedConfigPath: null })),
					)
				}
				return Effect.log(`[DEBUG] Initial config load failed: ${e}`).pipe(
					Effect.map(() => ({ config: mergeWithDefaults({}), loadedConfigPath: null })),
				)
			}),
		)
		const initialConfig = initialLoaded.config
		yield* SubscriptionRef.set(loadedConfigPathRef, initialLoaded.loadedConfigPath)
		yield* Effect.log(`[DEBUG] Creating configRef with cliTool=${initialConfig.cliTool}`)

		// Create reactive config ref
		const configRef = yield* SubscriptionRef.make<ResolvedConfig>(initialConfig)

		// Watch for project changes and reload config
		yield* Effect.forkScoped(
			projectService.currentProject.changes.pipe(
				Stream.runForEach((project) =>
					Effect.gen(function* () {
						const newProjectPath = project?.path ?? process.cwd()
						yield* Effect.log(`[DEBUG] Project watcher triggered: path=${newProjectPath}`)
						const loaded = yield* loadConfigForPath(newProjectPath).pipe(
							Effect.tap((v) =>
								Effect.log(`[DEBUG] Watcher loaded config: cliTool=${v.config.cliTool}`),
							),
							Effect.catchAll((e) => {
								if (e._tag === "ConfigParseError") {
									return SubscriptionRef.set(loadWarningRef, e).pipe(
										Effect.zipRight(showConfigFallbackToast(e)),
										Effect.zipRight(Effect.log(`[DEBUG] Watcher config load failed: ${e}`)),
										Effect.map(() => ({ config: mergeWithDefaults({}), loadedConfigPath: null })),
									)
								}
								return Effect.log(`[DEBUG] Watcher config load failed: ${e}`).pipe(
									Effect.map(() => ({ config: mergeWithDefaults({}), loadedConfigPath: null })),
								)
							}),
						)
						const newConfig = loaded.config
						yield* Effect.log(`[DEBUG] Watcher setting configRef: cliTool=${newConfig.cliTool}`)
						yield* SubscriptionRef.set(loadedConfigPathRef, loaded.loadedConfigPath)
						yield* SubscriptionRef.set(configRef, newConfig)
					}),
				),
			),
		)

		return {
			config: configRef,
			loadWarning: loadWarningRef,
			loadedConfigPath: loadedConfigPathRef,
			/**
			 * Reload config from disk for current project
			 *
			 * Used by SettingsService after saving config changes to ensure
			 * the reactive config atoms update immediately in the UI.
			 *
			 * Falls back to default config if loading fails, with error logging.
			 */
			reload: () =>
				Effect.gen(function* () {
					const currentProjectPath = yield* projectService.getCurrentPath()
					const effectiveProjectPath = currentProjectPath ?? process.cwd()
					const loaded = yield* loadConfigForPath(effectiveProjectPath).pipe(
						Effect.catchAll((e) => {
							if (e._tag === "ConfigParseError") {
								return SubscriptionRef.set(loadWarningRef, e).pipe(
									Effect.zipRight(showConfigFallbackToast(e)),
									Effect.zipRight(Effect.log(`[DEBUG] Config reload failed: ${e}`)),
									Effect.map(() => ({ config: mergeWithDefaults({}), loadedConfigPath: null })),
								)
							}
							return Effect.log(`[DEBUG] Config reload failed: ${e}`).pipe(
								Effect.map(() => ({ config: mergeWithDefaults({}), loadedConfigPath: null })),
							)
						}),
					)
					const newConfig = loaded.config
					yield* SubscriptionRef.set(loadedConfigPathRef, loaded.loadedConfigPath)
					yield* SubscriptionRef.set(configRef, newConfig)
				}),
			getCliTool: () =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(configRef)
					yield* Effect.log(`[DEBUG] getCliTool: config.cliTool=${config.cliTool}`)
					return config.cliTool
				}),
			getModelConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.model),
			getWorktreeConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.worktree),
			getGitConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.git),
			getSpecConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.spec),
			getSessionConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.session),
			getPatternsConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.patterns),
			getPRConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.pr),
			getMergeConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.merge),
			getNotificationsConfig: () =>
				Effect.map(SubscriptionRef.get(configRef), (c) => c.notifications),
			getIssueEditorConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.issueEditor),
			getIssueTrackerSyncConfig: () =>
				Effect.map(SubscriptionRef.get(configRef), (c) => ({
					issueTracker: c.issueTracker,
					syncEnabled: getIssueBackendSyncEnabled(c),
				})),
			getIssueTrackerSyncConfigForProjectPath: (projectPath: string) =>
				Effect.map(loadConfigForPath(projectPath), ({ config }) => ({
					issueTracker: config.issueTracker,
					syncEnabled: getIssueBackendSyncEnabled(config),
				})),
			getSpecConfigForProjectPath: (projectPath: string) =>
				Effect.map(loadConfigForPath(projectPath), ({ config }) => config.spec),
			getNetworkConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.network),
			getDevServerConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.devServer),
			getKeyboardConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.keyboard),
			getSessionRecoveryConfig: () =>
				Effect.map(SubscriptionRef.get(configRef), (c) => c.sessionRecovery),
			getHooksConfig: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.hooks),
			getWorkflowMode: () => Effect.map(SubscriptionRef.get(configRef), (c) => c.git.workflowMode),
			getEffectiveBaseBranch: () =>
				Effect.map(SubscriptionRef.get(configRef), (c) =>
					c.git.workflowMode === "origin" ? `origin/${c.git.baseBranch}` : c.git.baseBranch,
				),
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
