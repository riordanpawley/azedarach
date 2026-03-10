import { FileSystem, Path } from "@effect/platform"
import { Data, Effect, Schema, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import {
	type AzedarachConfig,
	AzedarachConfigJsonSchema,
	AzedarachConfigSchema,
} from "../config/schema.js"
import { getProjectStoragePaths, resolveConfigSchemaPath } from "../core/storagePaths.js"
import { ProjectService, resolveConfigBasePath } from "./ProjectService.js"
import { ToastService } from "./ToastService.js"

export type SettingValue = boolean | string | number

export interface SettingDefinition {
	readonly key: string
	readonly group: readonly string[]
	readonly label: string
	readonly getValue: (config: AzedarachConfig) => SettingValue
	readonly nextValue: (config: AzedarachConfig) => AzedarachConfig
	readonly isVisible?: (config: AzedarachConfig) => boolean
}

/**
 * Settings config parse/validation failed while handling in-app edits.
 */
export class SettingsConfigLoadError extends Data.TaggedError("SettingsConfigLoadError")<{
	readonly message: string
	readonly cause: string
}> {}

const cycleStringValue = <T extends string>(current: T, options: readonly T[]): T => {
	const currentIndex = options.indexOf(current)
	if (currentIndex < 0) return options[0] ?? current
	return options[(currentIndex + 1) % options.length] ?? current
}

const cycleNumberValue = <T extends number>(current: T, options: readonly T[]): T => {
	const currentIndex = options.indexOf(current)
	if (currentIndex < 0) return options[0] ?? current
	return options[(currentIndex + 1) % options.length] ?? current
}

const CLI_TOOL_OPTIONS: readonly ("claude" | "opencode" | "codex")[] = [
	"claude",
	"opencode",
	"codex",
]
const WORKFLOW_MODE_OPTIONS: readonly ("origin" | "local")[] = ["origin", "local"]
const ISSUE_BACKEND_OPTIONS: readonly ("local" | "tracker" | "legacy" | "linear")[] = [
	"local",
	"tracker",
	"legacy",
	"linear",
]

const isLinearBackend = (config: AzedarachConfig): boolean =>
	config.issueTracker?.linear !== undefined

const isLocalBackend = (config: AzedarachConfig): boolean =>
	config.issueTracker?.local !== undefined

const getIssueTrackerBackend = (
	config: AzedarachConfig,
): "tracker" | "legacy" | "linear" | "local" => {
	if (config.issueTracker?.tracker !== undefined) return "tracker"
	if (config.issueTracker?.legacy !== undefined) return "legacy"
	if (config.issueTracker?.linear !== undefined) return "linear"
	return "local"
}

export const EDITABLE_SETTINGS: readonly SettingDefinition[] = [
	{
		key: "cliTool",
		group: ["General"],
		label: "CLI Tool",
		getValue: (c) => c.cliTool ?? "codex",
		nextValue: (c) => ({
			...c,
			cliTool: cycleStringValue(c.cliTool ?? "codex", CLI_TOOL_OPTIONS),
		}),
	},
	{
		key: "workflowMode",
		group: ["General"],
		label: "Workflow Mode",
		getValue: (c) => c.git?.workflowMode ?? "origin",
		nextValue: (c) => ({
			...c,
			git: {
				...c.git,
				workflowMode: cycleStringValue(c.git?.workflowMode ?? "origin", WORKFLOW_MODE_OPTIONS),
			},
		}),
	},
	{
		key: "specEnabled",
		group: ["Features"],
		label: "Spec Enabled",
		getValue: (c) => c.spec?.enabled ?? true,
		nextValue: (c) => ({
			...c,
			spec: {
				...c.spec,
				enabled: !(c.spec?.enabled ?? true),
			},
		}),
	},
	{
		key: "issueTrackerBackend",
		group: ["Issue Tracker"],
		label: "Backend",
		getValue: getIssueTrackerBackend,
		nextValue: (c) => {
			const nextBackend = cycleStringValue(getIssueTrackerBackend(c), ISSUE_BACKEND_OPTIONS)

			if (nextBackend === "local") {
				return {
					...c,
					issueTracker: {
						local: {
							syncEnabled: c.issueTracker?.local?.syncEnabled ?? false,
							backups: c.issueTracker?.local?.backups,
						},
					},
				}
			}

			if (nextBackend === "tracker") {
				return {
					...c,
					issueTracker: {
						tracker: {
							syncEnabled: c.issueTracker?.tracker?.syncEnabled ?? true,
						},
					},
				}
			}

			if (nextBackend === "legacy") {
				return {
					...c,
					issueTracker: {
						legacy: {
							syncEnabled: c.issueTracker?.legacy?.syncEnabled ?? true,
						},
					},
				}
			}

			return {
				...c,
				issueTracker: {
					linear: {
						syncEnabled: c.issueTracker?.linear?.syncEnabled ?? true,
						command: c.issueTracker?.linear?.command,
						team: c.issueTracker?.linear?.team,
						project: c.issueTracker?.linear?.project,
						webhooks: c.issueTracker?.linear?.webhooks,
						syncThrottle: c.issueTracker?.linear?.syncThrottle,
					},
				},
			}
		},
	},
	{
		key: "dangerouslySkipPermissions",
		group: ["Session"],
		label: "Skip Permissions",
		getValue: (c) => c.session?.dangerouslySkipPermissions ?? false,
		nextValue: (c) => ({
			...c,
			session: {
				...c.session,
				dangerouslySkipPermissions: !(c.session?.dangerouslySkipPermissions ?? false),
			},
		}),
	},
	{
		key: "sessionMaxSessions",
		group: ["Session"],
		label: "Max Sessions",
		getValue: (c) => c.session?.maxSessions ?? 10,
		nextValue: (c) => ({
			...c,
			session: {
				...c.session,
				maxSessions: cycleNumberValue(c.session?.maxSessions ?? 10, [5, 10, 15, 20]),
			},
		}),
	},
	{
		key: "pushBranchOnCreate",
		group: ["Git"],
		label: "Push on Create",
		getValue: (c) => c.git?.pushBranchOnCreate ?? true,
		nextValue: (c) => ({
			...c,
			git: { ...c.git, pushBranchOnCreate: !(c.git?.pushBranchOnCreate ?? true) },
		}),
	},
	{
		key: "pushEnabled",
		group: ["Git"],
		label: "Git Push",
		getValue: (c) => c.git?.pushEnabled ?? true,
		nextValue: (c) => ({
			...c,
			git: { ...c.git, pushEnabled: !(c.git?.pushEnabled ?? true) },
		}),
	},
	{
		key: "fetchEnabled",
		group: ["Git"],
		label: "Git Fetch",
		getValue: (c) => c.git?.fetchEnabled ?? true,
		nextValue: (c) => ({
			...c,
			git: { ...c.git, fetchEnabled: !(c.git?.fetchEnabled ?? true) },
		}),
	},
	{
		key: "showLineChanges",
		group: ["Git"],
		label: "Line Changes",
		getValue: (c) => c.git?.showLineChanges ?? false,
		nextValue: (c) => ({
			...c,
			git: { ...c.git, showLineChanges: !(c.git?.showLineChanges ?? false) },
		}),
	},
	{
		key: "prEnabled",
		group: ["Pull Requests"],
		label: "PR Enabled",
		getValue: (c) => c.pr?.enabled ?? true,
		nextValue: (c) => ({
			...c,
			pr: { ...c.pr, enabled: !(c.pr?.enabled ?? true) },
		}),
	},
	{
		key: "autoDraft",
		group: ["Pull Requests", "Defaults"],
		label: "Auto Draft PR",
		getValue: (c) => c.pr?.autoDraft ?? true,
		nextValue: (c) => ({
			...c,
			pr: { ...c.pr, autoDraft: !(c.pr?.autoDraft ?? true) },
		}),
		isVisible: (c) => c.pr?.enabled ?? true,
	},
	{
		key: "autoMerge",
		group: ["Pull Requests", "Defaults"],
		label: "Auto Merge PR",
		getValue: (c) => c.pr?.autoMerge ?? false,
		nextValue: (c) => ({
			...c,
			pr: { ...c.pr, autoMerge: !(c.pr?.autoMerge ?? false) },
		}),
		isVisible: (c) => c.pr?.enabled ?? true,
	},
	{
		key: "bell",
		group: ["Notifications"],
		label: "Bell Notify",
		getValue: (c) => c.notifications?.bell ?? true,
		nextValue: (c) => ({
			...c,
			notifications: { ...c.notifications, bell: !(c.notifications?.bell ?? true) },
		}),
	},
	{
		key: "systemNotify",
		group: ["Notifications"],
		label: "System Notify",
		getValue: (c) => c.notifications?.system ?? false,
		nextValue: (c) => ({
			...c,
			notifications: { ...c.notifications, system: !(c.notifications?.system ?? false) },
		}),
	},
	{
		key: "networkAutoDetect",
		group: ["Network"],
		label: "Auto Detect Network",
		getValue: (c) => c.network?.autoDetect ?? true,
		nextValue: (c) => ({
			...c,
			network: { ...c.network, autoDetect: !(c.network?.autoDetect ?? true) },
		}),
	},
	{
		key: "networkCheckIntervalSeconds",
		group: ["Network", "Checks"],
		label: "Check Interval (s)",
		getValue: (c) => c.network?.checkIntervalSeconds ?? 30,
		nextValue: (c) => ({
			...c,
			network: {
				...c.network,
				checkIntervalSeconds: cycleNumberValue(
					c.network?.checkIntervalSeconds ?? 30,
					[15, 30, 60, 120],
				),
			},
		}),
		isVisible: (c) => c.network?.autoDetect ?? true,
	},
	{
		key: "issueSyncEnabled",
		group: ["Issue Tracker"],
		label: "Issue Sync",
		getValue: (c) => {
			if (c.issueTracker?.tracker !== undefined) return c.issueTracker.tracker.syncEnabled ?? true
			if (c.issueTracker?.legacy !== undefined) return c.issueTracker.legacy.syncEnabled ?? true
			if (c.issueTracker?.linear !== undefined) return c.issueTracker.linear.syncEnabled ?? true
			if (c.issueTracker?.local !== undefined) return c.issueTracker.local.syncEnabled ?? false
			return false
		},
		nextValue: (c) => {
			if (c.issueTracker?.tracker !== undefined) {
				return {
					...c,
					issueTracker: {
						tracker: {
							...c.issueTracker.tracker,
							syncEnabled: !(c.issueTracker.tracker.syncEnabled ?? true),
						},
					},
				}
			}
			if (c.issueTracker?.legacy !== undefined) {
				return {
					...c,
					issueTracker: {
						legacy: {
							...c.issueTracker.legacy,
							syncEnabled: !(c.issueTracker.legacy.syncEnabled ?? true),
						},
					},
				}
			}
			if (c.issueTracker?.linear !== undefined) {
				return {
					...c,
					issueTracker: {
						linear: {
							...c.issueTracker.linear,
							syncEnabled: !(c.issueTracker.linear.syncEnabled ?? true),
						},
					},
				}
			}
			if (c.issueTracker?.local !== undefined) {
				return {
					...c,
					issueTracker: {
						local: {
							...c.issueTracker.local,
							syncEnabled: !(c.issueTracker.local.syncEnabled ?? false),
						},
					},
				}
			}
			return {
				...c,
				issueTracker: {
					local: {
						syncEnabled: true,
					},
				},
			}
		},
	},
	{
		key: "linearWebhooksEnabled",
		group: ["Issue Tracker", "Linear"],
		label: "Linear Webhooks",
		getValue: (c) => {
			if (c.issueTracker?.linear !== undefined)
				return c.issueTracker.linear.webhooks?.enabled ?? true
			return false
		},
		nextValue: (c) => {
			if (c.issueTracker?.linear === undefined) return c
			return {
				...c,
				issueTracker: {
					linear: {
						...c.issueTracker.linear,
						webhooks: {
							...c.issueTracker.linear.webhooks,
							enabled: !(c.issueTracker.linear.webhooks?.enabled ?? true),
						},
					},
				},
			}
		},
		isVisible: isLinearBackend,
	},
	{
		key: "localBackupsEnabled",
		group: ["Issue Tracker", "Local"],
		label: "SQLite Backups",
		getValue: (c) => c.issueTracker?.local?.backups?.enabled ?? true,
		nextValue: (c) => {
			if (c.issueTracker?.local === undefined) return c
			return {
				...c,
				issueTracker: {
					local: {
						...c.issueTracker.local,
						backups: {
							...c.issueTracker.local.backups,
							enabled: !(c.issueTracker.local.backups?.enabled ?? true),
						},
					},
				},
			}
		},
		isVisible: isLocalBackend,
	},
	{
		key: "localBackupsIntervalMinutes",
		group: ["Issue Tracker", "Local", "Backups"],
		label: "Backup Interval (m)",
		getValue: (c) => c.issueTracker?.local?.backups?.intervalMinutes ?? 60,
		nextValue: (c) => {
			if (c.issueTracker?.local === undefined) return c
			return {
				...c,
				issueTracker: {
					local: {
						...c.issueTracker.local,
						backups: {
							...c.issueTracker.local.backups,
							intervalMinutes: cycleNumberValue(
								c.issueTracker.local.backups?.intervalMinutes ?? 60,
								[15, 30, 60, 120],
							),
						},
					},
				},
			}
		},
		isVisible: (c) =>
			c.issueTracker?.local !== undefined && (c.issueTracker.local.backups?.enabled ?? true),
	},
	{
		key: "patternMatching",
		group: ["State Detection"],
		label: "Pattern Matching",
		getValue: (c) => c.stateDetection?.patternMatching ?? false,
		nextValue: (c) => ({
			...c,
			stateDetection: {
				...c.stateDetection,
				patternMatching: !(c.stateDetection?.patternMatching ?? false),
			},
		}),
	},
]

export const getVisibleSettings = (config: AzedarachConfig): readonly SettingDefinition[] =>
	EDITABLE_SETTINGS.filter((setting) => (setting.isVisible ? setting.isVisible(config) : true))

export interface SettingsState {
	readonly focusIndex: number
	readonly isOpen: boolean
}

export class SettingsService extends Effect.Service<SettingsService>()("SettingsService", {
	dependencies: [AppConfig.Default, ProjectService.Default, ToastService.Default],
	effect: Effect.gen(function* () {
		const appConfigService = yield* AppConfig
		const projectService = yield* ProjectService
		const toast = yield* ToastService
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const state = yield* SubscriptionRef.make<SettingsState>({
			focusIndex: 0,
			isOpen: false,
		})

		const getConfigPaths = (): Effect.Effect<{
			readonly canonicalPath: string
			readonly existingPath: string
		}> =>
			Effect.gen(function* () {
				const projectPath = yield* projectService.getCurrentPath()
				const effectiveProjectPath = projectPath ?? process.cwd()
				const cwdPath = process.cwd()
				const cwdStoragePaths = getProjectStoragePaths(cwdPath, pathService)
				const cwdHasCanonicalConfig = yield* fs.exists(cwdStoragePaths.canonicalConfigPath).pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => false),
				)
				const cwdHasLegacyConfig = cwdHasCanonicalConfig
					? false
					: yield* fs.exists(cwdStoragePaths.legacyConfigPath).pipe(
							Effect.tapError((error) =>
								Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
							),
							Effect.orElseSucceed(() => false),
						)
				const configBasePath = resolveConfigBasePath({
					cwdPath,
					projectPath: effectiveProjectPath,
					pathOps: pathService,
					cwdHasConfig: cwdHasCanonicalConfig || cwdHasLegacyConfig,
				})
				const storagePaths = getProjectStoragePaths(configBasePath, pathService)
				const canonicalExists = yield* fs.exists(storagePaths.canonicalConfigPath).pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => false),
				)
				const legacyExists = canonicalExists
					? false
					: yield* fs.exists(storagePaths.legacyConfigPath).pipe(
							Effect.tapError((error) =>
								Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
							),
							Effect.orElseSucceed(() => false),
						)

				return {
					canonicalPath: storagePaths.canonicalConfigPath,
					existingPath: canonicalExists
						? storagePaths.canonicalConfigPath
						: legacyExists
							? storagePaths.legacyConfigPath
							: storagePaths.canonicalConfigPath,
				}
			})

		const getConfigPath = (): Effect.Effect<string> =>
			getConfigPaths().pipe(Effect.map((paths) => paths.canonicalPath))

		const loadRawConfig = (): Effect.Effect<AzedarachConfig, SettingsConfigLoadError> =>
			Effect.gen(function* () {
				const configPaths = yield* getConfigPaths()
				const exists = yield* fs.exists(configPaths.existingPath).pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => false),
				)
				if (!exists) {
					return yield* Schema.decodeUnknown(AzedarachConfigSchema)({}).pipe(
						Effect.mapError(
							(error) =>
								new SettingsConfigLoadError({
									message: "Failed to create default config snapshot",
									cause: String(error),
								}),
						),
					)
				}

				const content = yield* fs.readFileString(configPaths.existingPath).pipe(
					Effect.mapError(
						(error) =>
							new SettingsConfigLoadError({
								message: "Failed to read project config",
								cause: String(error),
							}),
					),
				)

				return yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(content).pipe(
					Effect.mapError(
						(error) =>
							new SettingsConfigLoadError({
								message: "Config parse/validation failed for project config",
								cause: String(error),
							}),
					),
				)
			})

		const configJsonSchemaString = `${JSON.stringify(AzedarachConfigJsonSchema, null, 2)}\n`
		const writeConfigJsonSchema = (configPath: string): Effect.Effect<void> => {
			const schemaPath = resolveConfigSchemaPath(configPath, pathService)
			return fs
				.writeFileString(schemaPath, configJsonSchemaString)
				.pipe(
					Effect.catchAll((error) =>
						Effect.logWarning(
							`Failed to write config JSON schema at ${schemaPath}: ${String(error)}`,
						),
					),
				)
		}

		const saveConfig = (config: AzedarachConfig) =>
			Effect.gen(function* () {
				const configPath = yield* getConfigPath()
				yield* fs.makeDirectory(pathService.dirname(configPath), { recursive: true }).pipe(
					Effect.tapError((error) =>
						Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
					),
					Effect.orElseSucceed(() => undefined),
				)
				const json = yield* Schema.encode(Schema.parseJson(AzedarachConfigSchema))(config)
				yield* fs.writeFileString(configPath, json).pipe(Effect.orDie)
				yield* writeConfigJsonSchema(configPath)
			})

		return {
			state,
			settings: EDITABLE_SETTINGS,

			open: () => SubscriptionRef.set(state, { focusIndex: 0, isOpen: true }),

			close: () => SubscriptionRef.set(state, { focusIndex: 0, isOpen: false }),

			moveUp: () =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(appConfigService.config)
					const visibleSettings = getVisibleSettings(config)
					const maxIndex = Math.max(0, visibleSettings.length - 1)
					yield* SubscriptionRef.update(state, (s) => ({
						...s,
						focusIndex: Math.min(maxIndex, Math.max(0, s.focusIndex - 1)),
					}))
				}),

			moveDown: () =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(appConfigService.config)
					const visibleSettings = getVisibleSettings(config)
					const maxIndex = Math.max(0, visibleSettings.length - 1)
					yield* SubscriptionRef.update(state, (s) => ({
						...s,
						focusIndex: Math.min(maxIndex, s.focusIndex + 1),
					}))
				}),

			getCurrentValue: (setting: SettingDefinition): Effect.Effect<SettingValue> =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(appConfigService.config)
					return setting.getValue(config)
				}),

			toggleCurrent: () =>
				Effect.gen(function* () {
					const { focusIndex } = yield* SubscriptionRef.get(state)
					const currentConfig = yield* SubscriptionRef.get(appConfigService.config)
					const visibleSettings = getVisibleSettings(currentConfig)
					const setting = visibleSettings[focusIndex]
					if (!setting) return

					const config = yield* loadRawConfig()
					const newConfig = setting.nextValue(config)
					const nextVisibleSettings = getVisibleSettings(newConfig)
					const maxIndex = Math.max(0, nextVisibleSettings.length - 1)
					yield* SubscriptionRef.update(state, (s) => ({
						...s,
						focusIndex: Math.min(s.focusIndex, maxIndex),
					}))

					yield* saveConfig(newConfig)
					// Reload the config in AppConfig service so the UI updates immediately
					yield* appConfigService.reload()
					yield* toast.show("success", `${setting.label}: ${String(setting.getValue(newConfig))}`)
				}).pipe(
					Effect.catchTag("SettingsConfigLoadError", (error) =>
						toast.show("error", `${error.message}. Open in editor (e) to fix JSON first.`),
					),
					Effect.catchAllDefect((e) =>
						toast.show("error", `Failed to update: ${e instanceof Error ? e.message : String(e)}`),
					),
				),

			getConfigPath,

			openInEditor: () =>
				Effect.gen(function* () {
					const configPaths = yield* getConfigPaths()
					const configPath = configPaths.canonicalPath

					const exists = yield* fs.exists(configPath).pipe(
						Effect.tapError((error) =>
							Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
						),
						Effect.orElseSucceed(() => false),
					)
					if (!exists) {
						yield* fs.makeDirectory(pathService.dirname(configPath), { recursive: true }).pipe(
							Effect.tapError((error) =>
								Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
							),
							Effect.orElseSucceed(() => undefined),
						)
						const existingContent = yield* fs.readFileString(configPaths.existingPath).pipe(
							Effect.tapError((error) =>
								Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
							),
							Effect.orElseSucceed(() => ""),
						)
						const json =
							existingContent.length > 0
								? yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(
										existingContent,
									).pipe(
										Effect.flatMap((decoded) =>
											Schema.encode(Schema.parseJson(AzedarachConfigSchema))(decoded),
										),
										Effect.orElse(() => Schema.encode(Schema.parseJson(AzedarachConfigSchema))({})),
									)
								: yield* Schema.encode(Schema.parseJson(AzedarachConfigSchema))({})
						yield* fs.writeFileString(configPath, json).pipe(Effect.orDie)
						yield* writeConfigJsonSchema(configPath)
					}

					const backupContent = yield* fs.readFileString(configPath).pipe(
						Effect.tapError((error) =>
							Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
						),
						Effect.orElseSucceed(() => "{}"),
					)

					// Mirror bead editor behavior: open $EDITOR in a blocking tmux popup.
					const editor = process.env.EDITOR || "vim"
					const channel = `az-settings-editor-${Date.now()}`
					Bun.spawnSync(
						[
							"tmux",
							"display-popup",
							"-E",
							"-w",
							"90%",
							"-h",
							"90%",
							"-T",
							" Edit Config ",
							"--",
							"sh",
							"-c",
							`${editor} "${configPath}"; tmux wait-for -S ${channel}`,
						],
						{ stdin: "inherit", stdout: "inherit", stderr: "inherit" },
					)
					Bun.spawnSync(["tmux", "wait-for", channel], {
						stdin: "inherit",
						stdout: "inherit",
						stderr: "inherit",
					})

					return { configPath, backupContent }
				}),

			validateAfterEdit: (configPath: string, backupContent: string) =>
				Effect.gen(function* () {
					const newContent = yield* fs.readFileString(configPath).pipe(
						Effect.tapError((error) =>
							Effect.logWarning(`Recovering from error before fallback: ${String(error)}`),
						),
						Effect.orElseSucceed(() => "{}"),
					)

					const parseResult = yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(
						newContent,
					).pipe(Effect.either)

					if (parseResult._tag === "Left") {
						yield* fs.writeFileString(configPath, backupContent).pipe(Effect.orDie)
						yield* appConfigService.reload()
						yield* toast.show("error", `Invalid config, rolled back`)
						return { valid: false, error: "Schema validation failed" }
					}

					yield* writeConfigJsonSchema(configPath)
					yield* appConfigService.reload()
					yield* toast.show("success", "Settings updated")
					return { valid: true }
				}),
		}
	}),
}) {}
