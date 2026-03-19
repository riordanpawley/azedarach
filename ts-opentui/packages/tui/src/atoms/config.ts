/**
 * Config atoms
 */

import { AppConfig } from "@azedarach/config"
import { Atom, Result } from "@effect-atom/atom"
import { Effect } from "effect"
import { appRuntime } from "./runtime.js"

export const appConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		return appConfig.config
	}),
)

export const configLoadWarningAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		return appConfig.loadWarning
	}),
)

export const loadedConfigPathAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		return appConfig.loadedConfigPath
	}),
)

export const workflowModeAtom = Atom.readable((get) => {
	const configResult = get(appConfigAtom)
	if (!Result.isSuccess(configResult)) return "origin" as const
	const config = configResult.value
	return config.git.workflowMode
})
