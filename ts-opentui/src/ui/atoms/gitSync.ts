/**
 * Git Sync Atoms
 *
 * Reactive state for GitSyncService - git fetch polling and pull notifications.
 *
 * Note: The notification logic (showing overlay when commits behind) is handled
 * entirely within GitSyncService using Stream.changes - no React side effects needed.
 */

import { Effect } from "effect"
import { GitSyncService } from "../../services/GitSyncService.js"
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
export const commitsBehindAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const gitSync = yield* GitSyncService
		return gitSync.commitsBehind
	}),
)

/**
 * Whether a git fetch is currently in progress
 *
 * Usage: const isFetching = useAtomValue(isFetchingAtom)
 */
export const isFetchingAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const gitSync = yield* GitSyncService
		return gitSync.isFetching
	}),
)

// ============================================================================
// Action Atoms
// ============================================================================

/**
 * Trigger a manual git fetch and check for updates
 *
 * This is called when the user presses 'r' to refresh.
 *
 * Usage: const fetchAndCheck = useAtomSet(fetchAndCheckAtom, { mode: "promise" })
 *        fetchAndCheck()
 */
export const fetchAndCheckAtom = appRuntime.fn(() =>
	Effect.gen(function* () {
		const gitSync = yield* GitSyncService
		yield* gitSync.fetchAndCheck()
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
		const gitSync = yield* GitSyncService
		yield* gitSync.pull()
	}).pipe(Effect.catchAll(Effect.logError)),
)
