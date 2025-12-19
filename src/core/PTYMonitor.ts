/**
 * PTYMonitor - Effect service for monitoring Claude session PTY output
 *
 * Continuously monitors tmux pane output for active sessions and:
 * - Detects session state (busy, error, done) via pattern matching
 * - Extracts session metrics (tokens, agent phase, recent output)
 * - Reports state changes to ClaudeSessionManager
 *
 * Works in tandem with HookReceiver:
 * - PTY provides: busy detection, error detection, done detection, metrics
 * - Hooks provide: waiting, idle (authoritative)
 * - Hooks always take priority over PTY signals (2s priority window)
 *
 * State aggregation flow:
 * 1. PTYMonitor polls tmux panes every 500ms
 * 2. Output is fed to StateDetector for pattern matching
 * 3. Detected state is compared against hook priority window
 * 4. If hooks haven't fired recently, PTY state updates ClaudeSessionManager
 */

import { Effect, HashMap, Ref, Schedule, SubscriptionRef } from "effect"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import type { AgentPhase, SessionState } from "../ui/types.js"
import { ClaudeSessionManager } from "./ClaudeSessionManager.js"
import { type DetectionResult, StateDetector } from "./StateDetector.js"
import { TmuxService } from "./TmuxService.js"

// ============================================================================
// Types
// ============================================================================

/**
 * Extracted metrics from PTY output
 */
export interface ExtractedMetrics {
	/** Estimated token count parsed from Claude status line */
	readonly estimatedTokens?: number
	/** Recent meaningful output line (truncated) */
	readonly recentOutput?: string
	/** Detected agent phase (planning/action/verification) */
	readonly agentPhase?: AgentPhase
}

/**
 * Per-session monitoring state
 */
interface SessionMonitor {
	readonly beadId: string
	readonly tmuxSessionName: string
	readonly detector: (chunk: string) => DetectionResult
	readonly lastOutput: string
	readonly lastStateFromHook: SessionState | null
	readonly lastHookTime: number
}

// ============================================================================
// Constants
// ============================================================================

/** Polling interval for PTY capture (matches HookReceiver) */
const POLL_INTERVAL_MS = 500

/** Number of lines to capture from tmux pane */
const CAPTURE_LINES = 50

/** Time window during which hook signals take priority */
const HOOK_PRIORITY_WINDOW_MS = 2000

// ============================================================================
// Metrics Extraction Helpers
// ============================================================================

/**
 * Extract token count from Claude's status line
 *
 * Patterns:
 * - "↓ 41 tokens" → 41
 * - "↑ 1.5k tokens" → 1500
 * - "3.2K tokens" → 3200
 */
const extractTokenCount = (output: string): number | undefined => {
	// Look for token count patterns in status line
	const patterns = [
		/[↓↑]\s*(\d+(?:\.\d+)?)\s*([kK])?\s*tokens?/i,
		/(\d+(?:\.\d+)?)\s*([kK])\s*tokens?/i,
	]

	for (const pattern of patterns) {
		const match = output.match(pattern)
		if (match) {
			let value = parseFloat(match[1])
			if (match[2]?.toLowerCase() === "k") {
				value *= 1000
			}
			return Math.floor(value)
		}
	}
	return undefined
}

/**
 * Extract recent output snippet (last meaningful line)
 *
 * Skips status bars and control characters to find actual content.
 */
const extractRecentOutput = (output: string): string | undefined => {
	const lines = output
		.trim()
		.split("\n")
		.filter((line) => line.trim().length > 0)

	// Look for the last non-status line
	for (let i = lines.length - 1; i >= 0; i--) {
		const line = lines[i].trim()
		// Skip status bar patterns
		if (!line.match(/^[·✶⏺]\s|esc to interrupt|-- INSERT|-- NORMAL/)) {
			return line.slice(0, 100) // Truncate to reasonable length
		}
	}
	return undefined
}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * PTYMonitor service
 *
 * Polls tmux panes for active sessions and detects state/metrics.
 * Uses a scoped effect so the polling fiber is automatically interrupted
 * when the service is disposed.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const monitor = yield* PTYMonitor
 *
 *   // Register a session for monitoring
 *   yield* monitor.registerSession("az-123", "az-123")
 *
 *   // Get current metrics
 *   const metrics = yield* monitor.getMetrics("az-123")
 *
 *   // Unregister when done
 *   yield* monitor.unregisterSession("az-123")
 * }).pipe(Effect.provide(PTYMonitor.Default))
 * ```
 */
export class PTYMonitor extends Effect.Service<PTYMonitor>()("PTYMonitor", {
	dependencies: [
		TmuxService.Default,
		ClaudeSessionManager.Default,
		StateDetector.Default,
		DiagnosticsService.Default,
	],
	scoped: Effect.gen(function* () {
		const tmux = yield* TmuxService
		const sessionManager = yield* ClaudeSessionManager
		const stateDetector = yield* StateDetector
		const diagnostics = yield* DiagnosticsService

		// Register with diagnostics - will mark unhealthy when scope closes
		yield* diagnostics.trackService("PTYMonitor", "Polling tmux panes every 2s")

		// Per-session monitoring state
		const monitors = yield* Ref.make<HashMap.HashMap<string, SessionMonitor>>(HashMap.empty())

		// Metrics output (published per-session via SubscriptionRef)
		const metricsRef = yield* SubscriptionRef.make<HashMap.HashMap<string, ExtractedMetrics>>(
			HashMap.empty(),
		)

		// ========================================================================
		// Session Registration
		// ========================================================================

		/**
		 * Register a session for PTY monitoring
		 *
		 * Creates a stateful detector for the session and starts monitoring.
		 */
		const registerSession = (beadId: string, tmuxSessionName: string) =>
			Effect.gen(function* () {
				const detector = yield* stateDetector.createCombinedDetector()
				const monitor: SessionMonitor = {
					beadId,
					tmuxSessionName,
					detector,
					lastOutput: "",
					lastStateFromHook: null,
					lastHookTime: 0,
				}
				yield* Ref.update(monitors, (m) => HashMap.set(m, beadId, monitor))
				yield* Effect.log(`PTYMonitor: Registered session ${beadId}`)
			})

		/**
		 * Unregister a session from PTY monitoring
		 *
		 * Cleans up state and metrics for the session.
		 */
		const unregisterSession = (beadId: string) =>
			Effect.gen(function* () {
				yield* Ref.update(monitors, (m) => HashMap.remove(m, beadId))
				yield* SubscriptionRef.update(metricsRef, (m) => HashMap.remove(m, beadId))
				yield* Effect.log(`PTYMonitor: Unregistered session ${beadId}`)
			})

		/**
		 * Record a hook signal for priority handling
		 *
		 * Called by HookReceiver integration to notify PTYMonitor of authoritative
		 * state changes. The priority window ensures hooks always take precedence.
		 */
		const recordHookSignal = (beadId: string, state: SessionState) =>
			Ref.update(monitors, (m) => {
				const existing = HashMap.get(m, beadId)
				if (existing._tag === "Some") {
					return HashMap.set(m, beadId, {
						...existing.value,
						lastStateFromHook: state,
						lastHookTime: Date.now(),
					})
				}
				return m
			})

		// ========================================================================
		// Polling Logic
		// ========================================================================

		/**
		 * Poll a single session for state and metrics
		 */
		const pollSession = (beadId: string, monitor: SessionMonitor) =>
			Effect.gen(function* () {
				// Capture recent output from tmux pane
				const output = yield* tmux.capturePane(monitor.tmuxSessionName, CAPTURE_LINES)

				// Skip if no change from last poll
				if (output === monitor.lastOutput) {
					return
				}

				// Run detection on new output
				const { state: detectedState, phase: detectedPhase } = monitor.detector(output)

				// Extract metrics
				const metrics: ExtractedMetrics = {
					estimatedTokens: extractTokenCount(output),
					recentOutput: extractRecentOutput(output),
					agentPhase: detectedPhase ?? undefined,
				}

				// Update metrics SubscriptionRef
				yield* SubscriptionRef.update(metricsRef, (m) => HashMap.set(m, beadId, metrics))

				// State aggregation: respect hook priority window
				const hookRecency = Date.now() - monitor.lastHookTime
				const hookHasPriority = hookRecency < HOOK_PRIORITY_WINDOW_MS

				if (detectedState && !hookHasPriority) {
					// Get current state from ClaudeSessionManager
					const currentState = yield* sessionManager
						.getState(beadId)
						.pipe(Effect.catchAll(() => Effect.succeed("idle" as SessionState)))

					// Determine if we should update state
					// PTY can transition: idle → busy, busy → error, busy → done
					const shouldUpdate =
						(currentState === "idle" && detectedState === "busy") ||
						(currentState === "busy" && detectedState === "error") ||
						(currentState === "busy" && detectedState === "done")

					if (shouldUpdate) {
						yield* sessionManager.updateState(beadId, detectedState)
						yield* Effect.log(
							`PTYMonitor: ${beadId} state ${currentState} → ${detectedState} (PTY detected)`,
						)
					}
				}

				// Update monitor state with new output
				yield* Ref.update(monitors, (m) =>
					HashMap.set(m, beadId, {
						...monitor,
						lastOutput: output,
					}),
				)
			}).pipe(
				Effect.catchAll((e) =>
					Effect.logWarning(`PTYMonitor: Error polling ${beadId}: ${e}`).pipe(Effect.asVoid),
				),
			)

		/**
		 * Poll all registered sessions
		 */
		const pollAll = () =>
			Effect.gen(function* () {
				const allMonitors = yield* Ref.get(monitors)
				yield* Effect.all(
					Array.from(HashMap.entries(allMonitors)).map(([beadId, monitor]) =>
						pollSession(beadId, monitor),
					),
					{ concurrency: "unbounded" },
				)
			})

		// ========================================================================
		// Start Polling Loop
		// ========================================================================

		// Start the polling fiber - scoped by service lifetime (auto-interrupted on dispose)
		yield* Effect.scheduleForked(Schedule.spaced(`${POLL_INTERVAL_MS} millis`))(
			pollAll().pipe(
				Effect.catchAll((e) =>
					Effect.logWarning(`PTYMonitor: Poll cycle error: ${e}`).pipe(Effect.asVoid),
				),
			),
		)

		yield* Effect.log("PTYMonitor: Started polling for PTY output")

		// ========================================================================
		// Service Interface
		// ========================================================================

		return {
			/** Register a session for PTY monitoring */
			registerSession,

			/** Unregister a session from monitoring */
			unregisterSession,

			/** Record a hook signal for priority handling */
			recordHookSignal,

			/** Metrics SubscriptionRef for reactive UI updates */
			metrics: metricsRef,

			/** Get metrics for a specific session */
			getMetrics: (beadId: string) =>
				Effect.gen(function* () {
					const all = yield* SubscriptionRef.get(metricsRef)
					const found = HashMap.get(all, beadId)
					return found._tag === "Some" ? found.value : undefined
				}),
		}
	}),
}) {}
