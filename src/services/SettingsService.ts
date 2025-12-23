import { FileSystem, Path } from "@effect/platform"
import { Effect, Schema, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"
import { type AzedarachConfig, AzedarachConfigSchema } from "../config/schema.js"
import { ProjectService } from "./ProjectService.js"
import { ToastService } from "./ToastService.js"

type SettingType = "boolean" | "enum"

export interface SettingDefinition {
	readonly key: string
	readonly label: string
	readonly path: readonly string[]
	readonly type: SettingType
	readonly options?: readonly string[]
}

export const EDITABLE_SETTINGS: readonly SettingDefinition[] = [
	{
		key: "cliTool",
		label: "CLI Tool",
		path: ["cliTool"],
		type: "enum",
		options: ["claude", "opencode"],
	},
	{
		key: "dangerouslySkipPermissions",
		label: "Skip Permissions",
		path: ["session", "dangerouslySkipPermissions"],
		type: "boolean",
	},
	{
		key: "pushBranchOnCreate",
		label: "Push on Create",
		path: ["git", "pushBranchOnCreate"],
		type: "boolean",
	},
	{ key: "pushEnabled", label: "Git Push", path: ["git", "pushEnabled"], type: "boolean" },
	{ key: "fetchEnabled", label: "Git Fetch", path: ["git", "fetchEnabled"], type: "boolean" },
	{
		key: "showLineChanges",
		label: "Line Changes",
		path: ["git", "showLineChanges"],
		type: "boolean",
	},
	{ key: "prEnabled", label: "PR Enabled", path: ["pr", "enabled"], type: "boolean" },
	{ key: "autoDraft", label: "Auto Draft PR", path: ["pr", "autoDraft"], type: "boolean" },
	{ key: "autoMerge", label: "Auto Merge PR", path: ["pr", "autoMerge"], type: "boolean" },
	{ key: "bell", label: "Bell Notify", path: ["notifications", "bell"], type: "boolean" },
	{
		key: "systemNotify",
		label: "System Notify",
		path: ["notifications", "system"],
		type: "boolean",
	},
	{
		key: "networkAutoDetect",
		label: "Auto Detect Network",
		path: ["network", "autoDetect"],
		type: "boolean",
	},
	{ key: "beadsSyncEnabled", label: "Beads Sync", path: ["beads", "syncEnabled"], type: "boolean" },
	{
		key: "patternMatching",
		label: "Pattern Matching",
		path: ["stateDetection", "patternMatching"],
		type: "boolean",
	},
]

export interface SettingsState {
	readonly focusIndex: number
	readonly isOpen: boolean
}

const getNestedValue = (obj: Record<string, unknown>, path: readonly string[]): unknown => {
	let current: unknown = obj
	for (const key of path) {
		if (current === null || current === undefined || typeof current !== "object") return undefined
		current = (current as Record<string, unknown>)[key]
	}
	return current
}

const setNestedValue = <T extends Record<string, unknown>>(
	obj: T,
	path: readonly string[],
	value: unknown,
): T => {
	if (path.length === 0) return obj
	if (path.length === 1) {
		return { ...obj, [path[0]]: value }
	}
	const [head, ...tail] = path
	const nested = (obj[head] as Record<string, unknown>) ?? {}
	return { ...obj, [head]: setNestedValue(nested, tail, value) }
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
				if (!exists) return {} as AzedarachConfig

				const content = yield* fs.readFileString(configPath).pipe(Effect.orElseSucceed(() => "{}"))
				const parsed = yield* Schema.decode(Schema.parseJson(AzedarachConfigSchema))(content).pipe(
					Effect.orElseSucceed(() => ({}) as AzedarachConfig),
				)
				return parsed
			}).pipe(Effect.orElseSucceed(() => ({}) as AzedarachConfig))

		const saveConfig = (config: AzedarachConfig) =>
			Effect.gen(function* () {
				const configPath = yield* getConfigPath()
				const json = yield* Schema.encode(Schema.parseJson(AzedarachConfigSchema))(config)
				yield* fs.writeFileString(configPath, json).pipe(Effect.orDie)
			})

		const validateConfig = (config: unknown) =>
			Schema.decodeUnknown(AzedarachConfigSchema)(config).pipe(Effect.orDie)

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
					return getNestedValue(config as unknown as Record<string, unknown>, setting.path)
				}),

			toggleCurrent: () =>
				Effect.gen(function* () {
					const { focusIndex } = yield* SubscriptionRef.get(state)
					const setting = EDITABLE_SETTINGS[focusIndex]
					if (!setting) return

					const rawConfig = yield* loadRawConfig()
					const currentValue = getNestedValue(
						rawConfig as unknown as Record<string, unknown>,
						setting.path,
					)

					let newValue: unknown
					if (setting.type === "boolean") {
						newValue = !currentValue
					} else if (setting.type === "enum" && setting.options) {
						const currentIdx = setting.options.indexOf(String(currentValue ?? setting.options[0]))
						const nextIdx = (currentIdx + 1) % setting.options.length
						newValue = setting.options[nextIdx]
					} else {
						return
					}

					const newConfig = setNestedValue(
						rawConfig as unknown as Record<string, unknown>,
						setting.path,
						newValue,
					) as AzedarachConfig

					yield* validateConfig(newConfig)
					yield* saveConfig(newConfig)
					yield* toast.show("success", `${setting.label}: ${String(newValue)}`)
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
