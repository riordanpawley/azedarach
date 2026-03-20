/**
 * Diagnostics Atoms
 *
 * Handles system health monitoring and fiber diagnostics.
 */

import { Effect } from "effect"
import { DiagnosticsService, type DiagnosticsState } from "../utils/runtimeServices.js"
import { appRuntime } from "./runtime.js"

// Re-export DiagnosticsState type for consumers
export type { DiagnosticsState }

// ============================================================================
// Diagnostics Atoms
// ============================================================================

/**
 * Diagnostics state atom - subscribes to DiagnosticsService state
 *
 * Provides reactive access to system health info including:
 * - Running fibers and their status
 * - Service health (TmuxSessionMonitor and package-local TUI services)
 * - Last activity timestamps
 *
 * Usage: const diagnostics = useAtomValue(diagnosticsAtom)
 */
export const diagnosticsAtom = appRuntime.subscriptionRef(
	Effect.gen(function* () {
		const diagnostics = yield* DiagnosticsService
		// Return the SubscriptionRef for reactive updates
		return diagnostics.state
	}),
)
