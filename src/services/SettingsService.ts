import { FileSystem, Path } from "@effect/platform"
import { Effect, Schema, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { type AzedarachConfig, AzedarachConfigSchema } from "../config/schema.js"
import { ProjectService } from "./ProjectService.js"
import { ToastService } from "./ToastService.js"

export interface SettingDefinition {
	readonly key: string
	readonly label: string
	readonly toggle: (config: AzedarachConfig) => AzedarachConfig
	readonly getValue: (config: AzedarachConfig) => boolean | string
}

export const EDITABLE_SETTINGS: readonly SettingDefinition[] = [
	{
		key: "cliTool",
		label: "CLI Tool",
		getValue: (c) => c.cliTool ?? "claude",
		toggle: (c) => ({
			...c,
			cliTool: (c.cliTool ?? "claude") === "claude" ? "opencode" : "claude",
		}),
	},
	{
		key: "dangerouslySkipPermissions",
		label: "Skip Permissions",
		getValue: (c) => c.session?.dangerouslySkipPermissions ?? false,
		toggle: (c) => ({
			...c,
			session: {
				...c.session,
				dangerouslySkipPermissions: !(c.session?.dangerouslySkipPermissions ?? false),
			},
		}),
	},
	{
		key: "pushBranchOnCreate",
		label: "Push on Create",
		getValue: (c) => c.git?.pushBranchOnCreate ?? true,
		toggle: (c) => ({
			...c,
			git: { ...c.git, pushBranchOnCreate: !(c.git?.pushBranchOnCreate ?? true) },
		}),
	},
	{
		key: "pushEnabled",
		label: "Git Push",
		getValue: (c) => c.git?.pushEnabled ?? true,
		toggle: (c) => ({
			...c,
			git: { ...c.git, pushEnabled: !(c.git?.pushEnabled ?? true) },
		}),
	},
	{
		key: "fetchEnabled",
		label: "Git Fetch",
		getValue: (c) => c.git?.fetchEnabled ?? true,
		toggle: (c) => ({
			...c,
			git: { ...c.git, fetchEnabled: !(c.git?.fetchEnabled ?? true) },
		}),
	},
	{
		key: "showLineChanges",
		label: "Line Changes",
		getValue: (c) => c.git?.showLineChanges ?? false,
		toggle: (c) => ({
			...c,
			git: { ...c.git, showLineChanges: !(c.git?.showLineChanges ?? false) },
		}),
	},
	{
		key: "prEnabled",
		label: "PR Enabled",
		getValue: (c) => c.pr?.enabled ?? true,
		toggle: (c) => ({
			...c,
			pr: { ...c.pr, enabled: !(c.pr?.enabled ?? true) },
		}),
	},
	{
		key: "autoDraft",
		label: "Auto Draft PR",
		getValue: (c) => c.pr?.autoDraft ?? true,
		toggle: (c) => ({
			...c,
			pr: { ...c.pr, autoDraft: !(c.pr?.autoDraft ?? true) },
		}),
	},
	{
		key: "autoMerge",
		label: "Auto Merge PR",
		getValue: (c) => c.pr?.autoMerge ?? false,
		toggle: (c) => ({
			...c,
			pr: { ...c.pr, autoMerge: !(c.pr?.autoMerge ?? false) },
		}),
	},
	{
		key: "bell",
		label: "Bell Notify",
		getValue: (c) => c.notifications?.bell ?? true,
		toggle: (c) => ({
			...c,
			notifications: { ...c.notifications, bell: !(c.notifications?.bell ?? true) },
		}),
	},
	{
		key: "systemNotify",
		label: "System Notify",
		getValue: (c) => c.notifications?.system ?? false,
		toggle: (c) => ({
			...c,
			notifications: { ...c.notifications, system: !(c.notifications?.system ?? false) },
		}),
	},
	{
		key: "networkAutoDetect",
		label: "Auto Detect Network",
		getValue: (c) => c.network?.autoDetect ?? true,
		toggle: (c) => ({
			...c,
			network: { ...c.network, autoDetect: !(c.network?.autoDetect ?? true) },
		}),
	},
	{
		key: "beadsSyncEnabled",
		label: "Beads Sync",
		getValue: (c) => c.beads?.syncEnabled ?? true,
		toggle: (c) => ({
			...c,
			beads: { ...c.beads, syncEnabled: !(c.beads?.syncEnabled ?? true) },
		}),
	},
	{
		key: "patternMatching",
		label: "Pattern Matching",
		getValue: (c) => c.stateDetection?.patternMatching ?? false,
		toggle: (c) => ({
			...c,
			stateDetection: {
				...c.stateDetection,
				patternMatching: !(c.stateDetection?.patternMatching ?? false),
			},
		}),
	},
]

export interface SettingsState {
	readonly focusIndex: number
	readonly isOpen: boolean
}

export class SettingsService extends Effect.Service<SettingsService>()("SettingsService", {
	dependencies: [AppConfig.Default, ProjectService.Default, ToastService.Default],
	effect: Effect.gen(function* () {
		const appConfig = yield* AppConfig
		const projectService = yield* ProjectService
		const toast = yield* ToastService
		const fs = yield* FileSystem.FileSystem
		const pathService = yield* Path.Path

		const state = yield* SubscriptionRef.make<SettingsState>({
			focusIndex: 0,
			isOpen: false,
		})

		const getConfigPath = (): Effect.Effect<string> =>
			Effect.gen(function* () {
				const projectPath = yield* projectService.getCurrentPath()
				return pathService.join(projectPath ?? process.cwd(), ".azedarach.json")
			})

		const loadRawConfig = () =>
			Effect.gen(function* () {
				const configPath = yield* getConfigPath()
				const exists = yield* fs.exists(configPath).pipe(Effect.orElseSucceed(() => false))
				if (!exists) return yield* Schema.decodeUnknown(AzedarachConfigSchema)({})

				const content = yield* fs.readFileString(configPath).pipe(Effect.orElseSucceed(() => "{}"))
				const parsed = yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(content).pipe(
					Effect.catchAll(() => Schema.decodeUnknown(AzedarachConfigSchema)({})),
				)
				return parsed
			}).pipe(Effect.catchAll(() => Schema.decodeUnknown(AzedarachConfigSchema)({})))

		const saveConfig = (config: AzedarachConfig) =>
			Effect.gen(function* () {
				const configPath = yield* getConfigPath()
				const json = yield* Schema.encode(Schema.parseJson(AzedarachConfigSchema))(config)
				yield* fs.writeFileString(configPath, json).pipe(Effect.orDie)
			})

		return {
			state,
			settings: EDITABLE_SETTINGS,

			open: () => SubscriptionRef.set(state, { focusIndex: 0, isOpen: true }),

			close: () => SubscriptionRef.set(state, { focusIndex: 0, isOpen: false }),

			moveUp: () =>
				SubscriptionRef.update(state, (s) => ({
					...s,
					focusIndex: Math.max(0, s.focusIndex - 1),
				})),

			moveDown: () =>
				SubscriptionRef.update(state, (s) => ({
					...s,
					focusIndex: Math.min(EDITABLE_SETTINGS.length - 1, s.focusIndex + 1),
				})),

			getCurrentValue: (setting: SettingDefinition): Effect.Effect<unknown> =>
				Effect.gen(function* () {
					const config = yield* SubscriptionRef.get(appConfig.config)
					return setting.getValue(config)
				}),

			toggleCurrent: () =>
				Effect.gen(function* () {
					const { focusIndex } = yield* SubscriptionRef.get(state)
					const setting = EDITABLE_SETTINGS[focusIndex]
					if (!setting) return

					const config = yield* loadRawConfig()
					const newConfig = setting.toggle(config)

					yield* saveConfig(newConfig)
					yield* toast.show("success", `${setting.label}: ${String(setting.getValue(newConfig))}`)
				}).pipe(
					Effect.catchAllDefect((e) =>
						toast.show("error", `Failed to update: ${e instanceof Error ? e.message : String(e)}`),
					),
				),

			getConfigPath,

			openInEditor: () =>
				Effect.gen(function* () {
					const configPath = yield* getConfigPath()

					const exists = yield* fs.exists(configPath).pipe(Effect.orElseSucceed(() => false))
					if (!exists) {
						yield* fs.writeFileString(configPath, "{}\n").pipe(Effect.orDie)
					}

					const backupContent = yield* fs
						.readFileString(configPath)
						.pipe(Effect.orElseSucceed(() => "{}"))

					return { configPath, backupContent }
				}),

			validateAfterEdit: (configPath: string, backupContent: string) =>
				Effect.gen(function* () {
					const newContent = yield* fs
						.readFileString(configPath)
						.pipe(Effect.orElseSucceed(() => "{}"))

					const parseResult = yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(
						newContent,
					).pipe(Effect.either)

					if (parseResult._tag === "Left") {
						yield* fs.writeFileString(configPath, backupContent).pipe(Effect.orDie)
						yield* toast.show("error", `Invalid config, rolled back`)
						return { valid: false, error: "Schema validation failed" }
					}

					yield* toast.show("success", "Settings updated")
					return { valid: true }
				}),
		}
	}),
}) {}
