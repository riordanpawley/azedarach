/**
 * StateDetector - Effect service for detecting Claude session state from PTY output
 *
 * Analyzes output chunks from Claude Code sessions to determine current state:
 * - waiting: Claude is waiting for user input
 * - error: An error occurred
 * - done: Task completed successfully
 * - busy: Claude is actively working
 * - idle: No output (initial state)
 *
 * Pattern matching uses priority ordering - first match wins.
 */

import { Context, Data, Effect, Layer } from "effect"
import * as Schema from "effect/Schema"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Session state types
 */
export type SessionState = "idle" | "busy" | "waiting" | "done" | "error"

/**
 * State detection patterns with priority levels
 */
export interface StatePattern {
	readonly state: SessionState
	readonly patterns: readonly RegExp[]
	readonly priority: number
}

// ============================================================================
// Error Types
// ============================================================================

/**
 * Error when state detection fails unexpectedly
 */
export class StateDetectionError extends Data.TaggedError("StateDetectionError")<{
	readonly message: string
	readonly chunk?: string
}> {}

// ============================================================================
// Configuration
// ============================================================================

/**
 * Pattern definitions ordered by priority (highest to lowest)
 *
 * Patterns are checked in order, first match wins.
 * This ensures higher-priority states (like "waiting") take precedence
 * over lower-priority states (like "busy").
 */
const STATE_PATTERNS: readonly StatePattern[] = [
	{
		state: "waiting",
		priority: 100,
		patterns: [
			/\[y\/n\]/i,
			/Do you want to/i,
			/Press Enter/i,
			/waiting for input/i,
			/Continue\?/i,
			/Proceed\?/i,
		],
	},
	{
		state: "error",
		priority: 90,
		patterns: [
			/Error:/i,
			/Exception:/i,
			/Failed:/i,
			/ENOENT/i,
			/EACCES/i,
			/command not found/i,
			/permission denied/i,
		],
	},
	{
		state: "done",
		priority: 80,
		patterns: [/Task completed/i, /Successfully/i, /Done\./i, /Finished/i, /All tasks complete/i],
	},
	// "busy" is detected when output is flowing but no higher-priority pattern matches
	// "idle" is the default/initial state with no output
]

// ============================================================================
// Service Definition
// ============================================================================

/**
 * StateDetector service interface
 *
 * Provides pattern-matching capabilities for detecting Claude session state
 * from PTY output chunks.
 */
export interface StateDetectorService {
	/**
	 * Detect session state from a single output chunk
	 *
	 * Returns null if no state transition is detected (output doesn't match patterns).
	 * Returns SessionState if a pattern matches.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const state = yield* detector.detectFromChunk("Error: File not found")
	 * // state === "error"
	 * ```
	 */
	readonly detectFromChunk: (chunk: string) => Effect.Effect<SessionState | null, never>

	/**
	 * Create a stateful detector function
	 *
	 * Returns a pure function that can be called repeatedly with output chunks.
	 * The function maintains internal state for debouncing and pattern matching.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const detect = yield* detector.createDetector()
	 *
	 * // Use in a stream
	 * const state1 = detect("Building...")  // "busy"
	 * const state2 = detect("Still building...")  // "busy"
	 * const state3 = detect("Done.")  // "done"
	 * ```
	 */
	readonly createDetector: () => Effect.Effect<(chunk: string) => SessionState | null, never>
}

/**
 * StateDetector service tag
 */
export class StateDetector extends Context.Tag("StateDetector")<
	StateDetector,
	StateDetectorService
>() {}

// ============================================================================
// Implementation Helpers
// ============================================================================

/**
 * Check if a chunk matches any pattern for a given state
 */
const matchesPattern = (chunk: string, patterns: readonly RegExp[]): boolean => {
	return patterns.some((pattern) => pattern.test(chunk))
}

/**
 * Detect state from a chunk by checking all patterns in priority order
 *
 * Returns the first matching state, or "busy" if output exists but no patterns match,
 * or null if the chunk is empty/whitespace only.
 */
const detectState = (chunk: string): SessionState | null => {
	// Ignore empty or whitespace-only chunks
	if (!chunk || chunk.trim().length === 0) {
		return null
	}

	// Check patterns in priority order
	for (const { state, patterns } of STATE_PATTERNS) {
		if (matchesPattern(chunk, patterns)) {
			return state
		}
	}

	// If we have non-empty output that doesn't match any pattern, it's "busy"
	return "busy"
}

/**
 * Create a stateful detector with debouncing
 *
 * The detector maintains state across calls:
 * - Rapid "busy" signals are coalesced (debouncing)
 * - Terminal states ("done", "error") are sticky until explicit reset
 * - "waiting" state is detected immediately
 */
const createStatefulDetector = (): ((chunk: string) => SessionState | null) => {
	let lastState: SessionState | null = null
	let lastDetectionTime = Date.now()
	const DEBOUNCE_MS = 100 // Only report state changes after 100ms of consistent state

	return (chunk: string): SessionState | null => {
		const detectedState = detectState(chunk)

		// No output, no change
		if (detectedState === null) {
			return null
		}

		const now = Date.now()
		const timeSinceLastDetection = now - lastDetectionTime

		// If we're in a terminal state ("done" or "error"), stay there
		// until explicitly reset (detector recreation)
		if (lastState === "done" || lastState === "error") {
			return lastState
		}

		// High-priority states ("waiting", "error", "done") are reported immediately
		if (detectedState === "waiting" || detectedState === "error" || detectedState === "done") {
			lastState = detectedState
			lastDetectionTime = now
			return detectedState
		}

		// For "busy" state, apply debouncing
		// Only report if we've been consistently busy for DEBOUNCE_MS
		if (detectedState === "busy") {
			if (lastState === "busy" && timeSinceLastDetection < DEBOUNCE_MS) {
				// Still within debounce window, don't report
				return null
			}

			lastState = "busy"
			lastDetectionTime = now
			return "busy"
		}

		// Shouldn't reach here, but handle gracefully
		lastState = detectedState
		lastDetectionTime = now
		return detectedState
	}
}

// ============================================================================
// Live Implementation
// ============================================================================

/**
 * Live StateDetector implementation
 *
 * Provides stateless pattern matching and stateful detector creation.
 */
const StateDetectorServiceImpl = Effect.gen(function* () {
	return StateDetector.of({
		detectFromChunk: (chunk) => Effect.sync(() => detectState(chunk)),

		createDetector: () => Effect.sync(() => createStatefulDetector()),
	})
})

/**
 * Live StateDetector layer
 *
 * This layer provides the StateDetector service with no dependencies.
 * It can be used directly in any Effect program.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const detector = yield* StateDetector
 *   const state = yield* detector.detectFromChunk("Error: something went wrong")
 *   return state // "error"
 * }).pipe(Effect.provide(StateDetectorLive))
 * ```
 */
export const StateDetectorLive = Layer.effect(StateDetector, StateDetectorServiceImpl)

// ============================================================================
// Convenience Functions
// ============================================================================

/**
 * Detect state from a chunk (convenience function)
 *
 * @example
 * ```ts
 * const state = yield* detectFromChunk("Do you want to continue? [y/n]")
 * // state === "waiting"
 * ```
 */
export const detectFromChunk = (
	chunk: string,
): Effect.Effect<SessionState | null, never, StateDetector> =>
	Effect.flatMap(StateDetector, (detector) => detector.detectFromChunk(chunk))

/**
 * Create a stateful detector (convenience function)
 *
 * @example
 * ```ts
 * const detect = yield* createDetector()
 * const state1 = detect("Building...")
 * const state2 = detect("Error: build failed")
 * ```
 */
export const createDetector = (): Effect.Effect<
	(chunk: string) => SessionState | null,
	never,
	StateDetector
> => Effect.flatMap(StateDetector, (detector) => detector.createDetector())
