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

import { Data, Effect } from "effect"

// ============================================================================
// Type Definitions
// ============================================================================

/**
 * Session state types
 */
export type SessionState = "idle" | "initializing" | "busy" | "waiting" | "done" | "error"

/**
 * Agent workflow phase types
 *
 * Tracks where Claude is in its typical workflow:
 * - planning: Analyzing, reading code, formulating approach
 * - action: Writing code, making edits, running commands
 * - verification: Running tests, type checks, validating results
 * - planMode: Claude Code's formal plan mode (read-only permission state)
 */
export type AgentPhase = "idle" | "planning" | "action" | "verification" | "planMode"

/**
 * State detection patterns with priority levels
 */
export interface StatePattern {
	readonly state: SessionState
	readonly patterns: readonly RegExp[]
	readonly priority: number
}

/**
 * Phase detection patterns with priority levels
 */
export interface PhasePattern {
	readonly phase: AgentPhase
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
			// AskUserQuestion tool - numbered choices and "Other" option
			/^\s*\d+\.\s+Other\b/im, // "1. Other" or "  2. Other" etc.
			/\bOther\s*\(describe\)/i, // "Other (describe)"
			/select.*option/i, // "Select an option"
			/choose.*option/i, // "Choose an option"
			/enter.*number/i, // "Enter a number"
			/type.*number.*select/i, // "Type a number to select"
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

/**
 * Phase detection patterns ordered by priority (highest to lowest)
 *
 * Verification takes highest priority (tests/checks are explicit signals)
 * Action is next (tool usage, code writes)
 * Planning is lowest (intent statements are common)
 */
const PHASE_PATTERNS: readonly PhasePattern[] = [
	{
		phase: "planMode",
		priority: 110, // Highest priority - plan mode is a distinct operational state
		patterns: [
			// Plan mode entry/active indicators
			/plan mode/i,
			/entering plan mode/i,
			/in plan mode/i,
			/ExitPlanMode/i, // Tool name indicates plan mode is active
			/exit plan mode/i,
			/read-only mode/i,
			// Plan mode prompts
			/would you like (?:me )?to enter plan mode/i,
			/enter plan mode\?/i,
			// Plan mode status indicators (Claude Code terminal output)
			/\[plan\]/i, // Status bar indicator
			/mode:\s*plan/i,
			/permission.?mode.*plan/i,
		],
	},
	{
		phase: "verification",
		priority: 100,
		patterns: [
			// Test execution
			/running tests?/i,
			/bun test/i,
			/npm test/i,
			/pnpm test/i,
			/jest/i,
			/vitest/i,
			/pytest/i,
			/cargo test/i,
			/go test/i,
			// Type checking
			/type[- ]?check/i,
			/tsc/i,
			/typecheck/i,
			// Build verification
			/bun run build/i,
			/npm run build/i,
			/pnpm build/i,
			/cargo build/i,
			// Linting
			/eslint/i,
			/biome/i,
			/prettier/i,
			// Validation signals
			/verifying/i,
			/validating/i,
			/checking/i,
			/tests? pass/i,
			/all tests/i,
		],
	},
	{
		phase: "action",
		priority: 80,
		patterns: [
			// Tool usage indicators (Claude Code output patterns)
			/\bEdit\b.*tool/i,
			/\bWrite\b.*tool/i,
			/\bBash\b.*tool/i,
			/\bRead\b.*tool/i,
			// File operations
			/writing to/i,
			/creating file/i,
			/editing file/i,
			/modifying/i,
			// Code output signals
			/```[\w]*\n/i, // Code block start
			/implementing/i,
			/adding/i,
			/updating/i,
			/fixing/i,
			/refactoring/i,
			// Command execution
			/running command/i,
			/executing/i,
			/\$ /i, // Shell prompt
		],
	},
	{
		phase: "planning",
		priority: 60,
		patterns: [
			// Intent statements
			/I'll /i,
			/Let me /i,
			/I will /i,
			/I need to /i,
			/I should /i,
			/First,? I/i,
			/Now I/i,
			/Next,? I/i,
			// Analysis signals
			/looking at/i,
			/analyzing/i,
			/understanding/i,
			/exploring/i,
			/searching/i,
			/reading/i,
			/checking/i,
			/investigating/i,
			// Planning language
			/my plan/i,
			/the approach/i,
			/strategy/i,
			/to understand/i,
			/to figure out/i,
		],
	},
	// "idle" is the default when no phase patterns match
]

// ============================================================================
// Service Definition
// ============================================================================

/**
 * Combined state and phase detection result
 */
export interface DetectionResult {
	readonly state: SessionState | null
	readonly phase: AgentPhase | null
}

/**
 * StateDetector service interface
 *
 * Provides pattern-matching capabilities for detecting Claude session state
 * and workflow phase from PTY output chunks.
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
	 * Detect agent phase from a single output chunk
	 *
	 * Returns null if no phase is detected.
	 * Returns AgentPhase if a pattern matches.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const phase = yield* detector.detectPhaseFromChunk("I'll start by reading the file")
	 * // phase === "planning"
	 * ```
	 */
	readonly detectPhaseFromChunk: (chunk: string) => Effect.Effect<AgentPhase | null, never>

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

	/**
	 * Create a combined stateful detector for both state and phase
	 *
	 * Returns a function that detects both session state and agent phase.
	 * Phase detection uses a longer debounce window to avoid rapid transitions.
	 *
	 * @example
	 * ```ts
	 * const detector = yield* StateDetector
	 * const detect = yield* detector.createCombinedDetector()
	 *
	 * const result = detect("I'll read the file first")
	 * // result === { state: "busy", phase: "planning" }
	 * ```
	 */
	readonly createCombinedDetector: () => Effect.Effect<(chunk: string) => DetectionResult, never>
}

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
 * Detect agent phase from a chunk by checking phase patterns in priority order
 *
 * Returns the first matching phase, or null if no patterns match.
 * Unlike state detection, no fallback phase is assumed.
 */
const detectPhase = (chunk: string): AgentPhase | null => {
	// Ignore empty or whitespace-only chunks
	if (!chunk || chunk.trim().length === 0) {
		return null
	}

	// Check patterns in priority order
	for (const { phase, patterns } of PHASE_PATTERNS) {
		if (matchesPattern(chunk, patterns)) {
			return phase
		}
	}

	// No phase detected from this chunk
	return null
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

/**
 * Create a combined stateful detector for both state and phase
 *
 * Phase detection uses a longer debounce window (500ms) than state detection
 * because phases tend to persist longer and we want to avoid rapid flickering.
 */
const createCombinedStatefulDetector = (): ((chunk: string) => DetectionResult) => {
	let lastState: SessionState | null = null
	let lastPhase: AgentPhase | null = null
	let lastStateTime = Date.now()
	let lastPhaseTime = Date.now()
	const STATE_DEBOUNCE_MS = 100
	const PHASE_DEBOUNCE_MS = 500 // Phases persist longer

	return (chunk: string): DetectionResult => {
		const now = Date.now()
		const detectedState = detectState(chunk)
		const detectedPhase = detectPhase(chunk)

		// State detection logic (same as createStatefulDetector)
		let newState: SessionState | null = null
		if (detectedState !== null) {
			// Terminal states are sticky
			if (lastState === "done" || lastState === "error") {
				newState = lastState
			}
			// High-priority states report immediately
			else if (
				detectedState === "waiting" ||
				detectedState === "error" ||
				detectedState === "done"
			) {
				lastState = detectedState
				lastStateTime = now
				newState = detectedState
			}
			// Busy state with debouncing
			else if (detectedState === "busy") {
				if (lastState !== "busy" || now - lastStateTime >= STATE_DEBOUNCE_MS) {
					lastState = "busy"
					lastStateTime = now
					newState = "busy"
				}
			} else {
				lastState = detectedState
				lastStateTime = now
				newState = detectedState
			}
		}

		// Phase detection with longer debounce
		let newPhase: AgentPhase | null = null
		if (detectedPhase !== null) {
			// Only update phase if different and past debounce window
			if (detectedPhase !== lastPhase || now - lastPhaseTime >= PHASE_DEBOUNCE_MS) {
				lastPhase = detectedPhase
				lastPhaseTime = now
				newPhase = detectedPhase
			}
		}

		return { state: newState, phase: newPhase }
	}
}

// ============================================================================
// Service Definition
// ============================================================================

/**
 * StateDetector service
 *
 * Provides stateless pattern matching and stateful detector creation.
 *
 * @example
 * ```ts
 * const program = Effect.gen(function* () {
 *   const detector = yield* StateDetector
 *   const state = yield* detector.detectFromChunk("Error: something went wrong")
 *   return state // "error"
 * }).pipe(Effect.provide(StateDetector.Default))
 * ```
 */
export class StateDetector extends Effect.Service<StateDetector>()("StateDetector", {
	effect: Effect.succeed({
		detectFromChunk: (chunk: string) => Effect.sync(() => detectState(chunk)),

		detectPhaseFromChunk: (chunk: string) => Effect.sync(() => detectPhase(chunk)),

		createDetector: () => Effect.sync(() => createStatefulDetector()),

		createCombinedDetector: () => Effect.sync(() => createCombinedStatefulDetector()),
	}),
}) {}
