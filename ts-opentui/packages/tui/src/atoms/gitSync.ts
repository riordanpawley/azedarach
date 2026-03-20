/**
 * Git Sync Atoms
 *
 * Transitional boundary-safe facade while git sync migrates to daemon RPC.
 * These atoms keep command surfaces available without depending on legacy
 * TUI runtime service injection.
 */

import { Effect } from "effect"
import { TuiBoardStoreService } from "../services/TuiBoardStoreService.js"
import { appRuntime } from "./runtime.js"

// ============================================================================
// State Atoms
// ============================================================================

/**
 * Number of commits the local base branch is behind origin
 *
 * In origin mode, this is updated every 30 seconds and on manual refresh.
 * Returns 0 in local mode or when offline.
 *
 * Usage: const commitsBehind = useAtomValue(commitsBehindAtom)
 */
export const commitsBehindAtom = appRuntime.atom(Effect.succeed(0), { initialValue: 0 })

/**
 * Whether a git fetch is currently in progress
 *
 * Usage: const isFetching = useAtomValue(isFetchingAtom)
 */
export const isFetchingAtom = appRuntime.atom(Effect.succeed(false), { initialValue: false })

// ============================================================================
// Action Atoms
// ============================================================================

/**
 * Trigger a manual git fetch and check for updates
 *
 * This is used by explicit sync actions that need a fetch check.
 *
 * Usage: const fetchAndCheck = useAtomSet(fetchAndCheckAtom, { mode: "promise" })
 *        fetchAndCheck()
 */
export const fetchAndCheckAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const board = yield* TuiBoardStoreService
		yield* board.refreshGitStats()
	}).pipe(Effect.catchAll(Effect.logError)),
)

/**
 * Pull updates from origin to local base branch
 *
 * Usage: const pull = useAtomSet(pullAtom, { mode: "promise" })
 *        pull()
 */
export const pullAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const board = yield* TuiBoardStoreService
		yield* board.refreshGitStats()
	}).pipe(Effect.catchAll(Effect.logError)),
)
