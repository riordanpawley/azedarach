/**
 * Environment Detection Atoms
 *
 * Exposes environment detection state to React components.
 * Determines if running in standalone or Gastown mode.
 */

import { Effect } from "effect"
import { EnvironmentDetectionService } from "../../services/EnvironmentDetectionService.js"
import { appRuntime } from "./runtime.js"

/**
 * Environment information atom
 *
 * Provides current mode (standalone/gastown) and related metadata.
 */
export const environmentInfoAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const envService = yield* EnvironmentDetectionService
		return envService.environmentInfo
	}),
)

/**
 * UI labels atom
 *
 * Returns mode-appropriate labels for UI elements.
 * In Gastown mode: "Rig", "Polecat", "Crew Member"
 * In standalone mode: "Project", "Session", "Worktree"
 */
export const uiLabelsAtom = appRuntime.fn(
	Effect.gen(function* () {
		const envService = yield* EnvironmentDetectionService
		return yield* envService.getLabels()
	}),
)

/**
 * Check if running in Gastown mode
 */
export const isGastownModeAtom = appRuntime.fn(
	Effect.gen(function* () {
		const envService = yield* EnvironmentDetectionService
		return yield* envService.isGastownMode()
	}),
)
