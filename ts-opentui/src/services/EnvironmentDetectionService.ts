/**
 * Environment Detection Service
 *
 * Detects whether Azedarach is running in:
 * - Standalone mode (traditional Azedarach with bd CLI)
 * - Gastown mode (integrated with Gastown multi-agent orchestration)
 *
 * Detection Strategy:
 * 1. Check explicit config (gastown.enabled)
 * 2. Check environment variables (GASTOWN_TOWN_DIR, GASTOWN_RIG_NAME)
 * 3. Check filesystem for .gastown markers
 * 4. Default to standalone mode
 */

import { FileSystem, Path } from "@effect/platform"
import { Effect, SubscriptionRef } from "effect"
import { AppConfig } from "../config/AppConfig.js"

/**
 * Environment mode
 */
export type EnvironmentMode = "standalone" | "gastown"

/**
 * Environment information
 */
export interface EnvironmentInfo {
	/** Current operating mode */
	mode: EnvironmentMode
	/** Path to Gastown town directory (only in Gastown mode) */
	townDir?: string
	/** Name of the rig (project) in Gastown */
	rigName?: string
	/** Crew member name (user workspace in Gastown) */
	crewMember?: string
}

/**
 * Detects the current environment mode
 *
 * Returns EnvironmentInfo with detected mode and metadata.
 */
const detectEnvironmentInfo = Effect.gen(function* () {
	const config = yield* AppConfig
	const fs = yield* FileSystem.FileSystem
	const pathService = yield* Path.Path

	// 1. Check explicit config override
	if (config.gastown.enabled === false) {
		return {
			mode: "standalone" as const,
		}
	}

	if (config.gastown.enabled === true) {
		// If explicitly enabled, use Gastown mode with configured town dir
		return {
			mode: "gastown" as const,
			townDir: config.gastown.townDir,
			rigName: process.env.GASTOWN_RIG_NAME,
			crewMember: process.env.GASTOWN_CREW_MEMBER,
		}
	}

	// 2. Check environment variables (set by Gastown when spawning sessions)
	const townDirEnv = process.env.GASTOWN_TOWN_DIR
	if (townDirEnv) {
		return {
			mode: "gastown" as const,
			townDir: townDirEnv,
			rigName: process.env.GASTOWN_RIG_NAME,
			crewMember: process.env.GASTOWN_CREW_MEMBER,
		}
	}

	// 3. Check for .gastown directory in current or parent directories
	const cwd = process.cwd()
	const checkPath = (dir: string): Effect.Effect<string | undefined, never, FileSystem.FileSystem | Path.Path> =>
		Effect.gen(function* () {
			const gastownPath = pathService.join(dir, ".gastown")
			const exists = yield* fs.exists(gastownPath)
			if (exists) return dir

			// Check parent directory (stop at root)
			const parent = pathService.dirname(dir)
			if (parent === dir) return undefined // reached root

			return yield* checkPath(parent)
		})

	const detectedTownDir = yield* checkPath(cwd).pipe(Effect.orElseSucceed(() => undefined))

	if (detectedTownDir) {
		return {
			mode: "gastown" as const,
			townDir: detectedTownDir,
			rigName: process.env.GASTOWN_RIG_NAME,
			crewMember: process.env.GASTOWN_CREW_MEMBER,
		}
	}

	// 4. Default to standalone mode
	return {
		mode: "standalone" as const,
	}
})

/**
 * Environment Detection Service
 *
 * Provides information about the current environment mode (standalone vs Gastown).
 * This is detected once at startup and cached.
 */
export class EnvironmentDetectionService extends Effect.Service<EnvironmentDetectionService>()(
	"EnvironmentDetectionService",
	{
		effect: Effect.gen(function* () {
			// Detect environment once at startup
			const info = yield* detectEnvironmentInfo

			// Store in SubscriptionRef for reactive access
			const environmentInfo = yield* SubscriptionRef.make<EnvironmentInfo>(info)

			return {
				/** Current environment information */
				environmentInfo,

				/** Check if running in Gastown mode */
				isGastownMode: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(environmentInfo)
						return current.mode === "gastown"
					}),

				/** Get the current mode */
				getMode: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(environmentInfo)
						return current.mode
					}),

				/** Get town directory (only in Gastown mode) */
				getTownDir: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(environmentInfo)
						return current.townDir
					}),

				/** Get UI labels appropriate for current mode */
				getLabels: () =>
					Effect.gen(function* () {
						const current = yield* SubscriptionRef.get(environmentInfo)
						if (current.mode === "gastown") {
							return {
								project: "Rig",
								session: "Polecat",
								workspace: "Crew Member",
								board: "Town View",
							}
						}
						return {
							project: "Project",
							session: "Session",
							workspace: "Worktree",
							board: "Board",
						}
					}),
			}
		}),
	},
) {}
