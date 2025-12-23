/**
 * Config atoms
 */

import { Atom, Result } from "@effect-rx/rx-react"
import { Effect } from "effect"
import { AppConfig } from "../../config/index.js"
import { appRuntime } from "./runtime.js"

export const appConfigAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const appConfig = yield* AppConfig
		return appConfig.config
	}),
)

export const workflowModeAtom = Atom.readable((get) => {
	const configResult = get(appConfigAtom)
	if (!Result.isSuccess(configResult)) return "origin" as const
	return configResult.value.git.workflowMode
})
